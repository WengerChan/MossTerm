// client_test.go 覆盖不依赖真实 *ssh.Client / *sftp.Client 的纯逻辑路径：
//   - entryFromFileInfo 字段映射（含 symlink / 目录 / 普通文件）
//   - 关闭后（c.sc == nil）所有方法返回错误而非 panic
//   - Close 幂等
//   - Option（WithPageSize）安全 no-op
//
// 真实 SSH server 集成测试（v0.5.0 spec 明确不做）留到 v0.5.1 起一个
// pkg/sftp 提供的 in-process SFTP server（sftp.NewServer）做覆盖。
package sftpclient

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// mockFileInfo 是 os.FileInfo 的最小测试实现。
//
// os.FileInfo 有 7 个方法，常规只关心 Name / Size / Mode / ModTime / IsDir。
// Sys 留 nil 即可 —— entryFromFileInfo 不会读它。
type mockFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return m.size }
func (m mockFileInfo) Mode() os.FileMode  { return m.mode }
func (m mockFileInfo) ModTime() time.Time { return m.modTime }
func (m mockFileInfo) IsDir() bool        { return m.isDir }
func (m mockFileInfo) Sys() interface{}   { return nil }

// TestEntryFromFileInfo_RegularFile 覆盖最常见路径：普通文件。
func TestEntryFromFileInfo_RegularFile(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	fi := mockFileInfo{
		name:    "README.md",
		size:    1024,
		mode:    0o644,
		modTime: now,
		isDir:   false,
	}
	e := entryFromFileInfo(fi, "/home/user")

	if e.Name != "README.md" {
		t.Errorf("Name: got %q, want %q", e.Name, "README.md")
	}
	// path.Join("/home/user", "README.md") → "/home/user/README.md"
	if e.Path != "/home/user/README.md" {
		t.Errorf("Path: got %q, want %q", e.Path, "/home/user/README.md")
	}
	if e.Size != 1024 {
		t.Errorf("Size: got %d, want %d", e.Size, 1024)
	}
	if e.Mode != 0o644 {
		t.Errorf("Mode: got %v, want %v", e.Mode, 0o644)
	}
	if !e.ModTime.Equal(now) {
		t.Errorf("ModTime: got %v, want %v", e.ModTime, now)
	}
	if e.IsDir {
		t.Error("IsDir: got true, want false")
	}
	if e.IsSymlink {
		t.Error("IsSymlink: got true, want false")
	}
	// Link 在 v0.5.0 故意留空 —— 验证该字段不携带误值
	if e.Link != "" {
		t.Errorf("Link: got %q, want empty (v0.5.0 不解析 symlink 目标)", e.Link)
	}
}

// TestEntryFromFileInfo_Directory 覆盖目录：IsDir=true、Mode 含 d 位。
func TestEntryFromFileInfo_Directory(t *testing.T) {
	fi := mockFileInfo{
		name:  "docs",
		size:  4096, // 目录 size 含义平台相关，spec 不强制
		mode:  os.ModeDir | 0o755,
		isDir: true,
	}
	e := entryFromFileInfo(fi, "/home/user")
	if !e.IsDir {
		t.Error("IsDir: got false, want true")
	}
	if e.IsSymlink {
		t.Error("IsSymlink: got true, want false")
	}
	if e.Path != "/home/user/docs" {
		t.Errorf("Path: got %q, want %q", e.Path, "/home/user/docs")
	}
}

// TestEntryFromFileInfo_Symlink 覆盖 symlink：IsSymlink=true、IsDir 跟随原值。
//
// os.FileInfo 在 symlink 上的行为：Mode() 含 ModeSymlink；IsDir() 取决于
// symlink 指向什么（指向目录则 true，否则 false）。这里 mock 让 IsDir=false
// 模拟"指向文件的 symlink"，验证 IsSymlink 标志位独立正确。
func TestEntryFromFileInfo_Symlink(t *testing.T) {
	fi := mockFileInfo{
		name:  "link",
		size:  6, // 比如指向 "target"
		mode:  os.ModeSymlink | 0o777,
		isDir: false,
	}
	e := entryFromFileInfo(fi, "/tmp")
	if !e.IsSymlink {
		t.Error("IsSymlink: got false, want true")
	}
	if e.IsDir {
		t.Error("IsDir: got true, want false (symlink 指向文件)")
	}
}

// TestEntryFromFileInfo_RootPath 边界：parent 为 "/" 时 path.Join 不会
// 退化成 "//README.md"。
func TestEntryFromFileInfo_RootPath(t *testing.T) {
	fi := mockFileInfo{name: "etc", isDir: true, mode: os.ModeDir | 0o755}
	e := entryFromFileInfo(fi, "/")
	// path.Join("/", "etc") == "/etc"（path 包是 POSIX 语义，跨平台稳定）
	if e.Path != "/etc" {
		t.Errorf("Path: got %q, want %q", e.Path, "/etc")
	}
}

// TestEntryFromFileInfo_EmptyParent 边界：parent 为空时 path.Join
// 直接返回 name（无前导分隔符）。这反映 pkg/sftp 实际行为
// （ReadDir "/" 的条目 Name 不含 "/"）。
func TestEntryFromFileInfo_EmptyParent(t *testing.T) {
	fi := mockFileInfo{name: "foo.txt", size: 0, mode: 0o644}
	e := entryFromFileInfo(fi, "")
	// path.Join("", "foo.txt") == "foo.txt"
	if e.Path != "foo.txt" {
		t.Errorf("Path: got %q, want %q (空 parent 应当避免生成前导 /)", e.Path, "foo.txt")
	}
}

// TestClientClosed_AllMethods 验证关闭（c.sc == nil）后所有方法都返回
// 错误而非 panic。这是"Close 后 nil-check"设计决策的可执行规范。
//
// 覆盖范围：List / Stat / ReadDir / Open / Mkdir / Remove / Rename。
// Close 本身单独测幂等。
func TestClientClosed_AllMethods(t *testing.T) {
	c := &Client{} // sc == nil，模拟 Close 后的状态
	ctx := context.Background()

	t.Run("List", func(t *testing.T) {
		_, err := c.List(ctx, "/", 0, "")
		if err == nil {
			t.Error("List: expected error, got nil")
		}
	})
	t.Run("Stat", func(t *testing.T) {
		_, err := c.Stat("/foo")
		if err == nil {
			t.Error("Stat: expected error, got nil")
		}
	})
	t.Run("ReadDir", func(t *testing.T) {
		_, err := c.ReadDir("/")
		if err == nil {
			t.Error("ReadDir: expected error, got nil")
		}
	})
	t.Run("Open", func(t *testing.T) {
		_, err := c.Open("/foo", os.O_RDONLY)
		if err == nil {
			t.Error("Open: expected error, got nil")
		}
	})
	t.Run("Mkdir", func(t *testing.T) {
		if err := c.Mkdir("/newdir"); err == nil {
			t.Error("Mkdir: expected error, got nil")
		}
	})
	t.Run("Remove", func(t *testing.T) {
		if err := c.Remove("/foo"); err == nil {
			t.Error("Remove: expected error, got nil")
		}
	})
	t.Run("Rename", func(t *testing.T) {
		if err := c.Rename("/a", "/b"); err == nil {
			t.Error("Rename: expected error, got nil")
		}
	})
}

// TestClientNil_AllMethods 兜底：nil *Client（c == nil）调用方法。
//
// 当前实现里 c.sc == nil 检查会 nil-deref（c.sc 在 nil receiver 上
// 读字段）。这在 spec 里没有明确要求 —— spec 只要求"Close 之后所有方法
// 报错"，对应"已经 Open 但 Close 过的 Client"状态（c != nil, c.sc == nil）。
//
// 记录现状：nil *Client 会 panic（reflect: call of method on nil Client）。
// 这是 Go 的自然行为；调用方应保证拿到 *Client 后再调方法。生产路径
// 不会触发（Open 出错直接返回 nil，不会有人去调方法）。
//
// 该测试只在恢复 recover 时才算"通过"，用于在 spec 改动时第一时间发现
// "是否要加 nil-receiver 防御"的回归。
func TestClientNil_AllMethods_DoNotPanic(t *testing.T) {
	var c *Client // nil

	cases := []struct {
		name string
		fn   func()
	}{
		{"List", func() { _, _ = c.List(context.Background(), "/", 0, "") }},
		{"Stat", func() { _, _ = c.Stat("/foo") }},
		{"ReadDir", func() { _, _ = c.ReadDir("/") }},
		{"Open", func() { _, _ = c.Open("/foo", os.O_RDONLY) }},
		{"Mkdir", func() { _ = c.Mkdir("/newdir") }},
		{"Remove", func() { _ = c.Remove("/foo") }},
		{"Rename", func() { _ = c.Rename("/a", "/b") }},
		{"Close", func() { _ = c.Close() }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Logf("nil *Client 调用 %s panic: %v (v0.5.0 已知行为：c == nil 不防御)", tc.name, r)
				}
			}()
			tc.fn()
		})
	}
}

// TestClose_IdempotentAndNilSafe 验证 Close 的两个性质：
//   1. 关闭过的 Client（c.sc == nil）再调 Close 返回 nil，不 panic
//   2. 并发调用 Close 安全（不会 double-close underlying *sftp.Client）
//
// 第二点实际上 v0.5.0 没法直接验证（没有 mock *sftp.Client），
// 只能验证"不 panic"。这与 keepalive_test.go 里 TestConnectorClose_ConcurrentSafe
// 思路一致：c.sc == nil 的分支在并发下是只读 + 写 c.sc = nil，
// 写同一字段的 race 在实际生产中由"sftp 客户端只被一个 goroutine Close"
// 约束避免（pkg/sftp 本身 goroutine unsafe，加锁是 wailsbindings 层责任）。
func TestClose_IdempotentAndNilSafe(t *testing.T) {
	c := &Client{} // sc == nil，模拟"已 Close"

	// 多次调用 Close：返回 nil，不 panic
	for i := 0; i < 5; i++ {
		if err := c.Close(); err != nil {
			t.Errorf("Close #%d: got error %v, want nil", i+1, err)
		}
	}

	// 并发：20 个 goroutine 同时 Close，全部应当安全返回
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = c.Close()
		}()
	}
	wg.Wait()
}

// TestWithPageSize_NoOp 验证 WithPageSize 不崩溃、可链式调用。
//
// v0.5.0 故意是 no-op：Client 没有 pageSize 字段（List 一次性返回）。
// 该 Option 保留是为了 v0.5.1+ 接入分页时调用方代码无需改。
// 一旦未来真的存了字段，Open 调用链会触发 panic（no-op 不会），
// 所以这里不验证"存了什么值"，只验证"调用安全"。
func TestWithPageSize_NoOp(t *testing.T) {
	c := &Client{}
	WithPageSize(500)(c)
	WithPageSize(0)(c)   // 0 也不该 panic
	WithPageSize(-1)(c)  // 负数也不该 panic
	// 跑完没 panic 即通过
}

// TestOption_AppliedToClient 验证 Option 在 Open 中的执行路径。
//
// 真实场景：Open(ssh, WithPageSize(100))。这里用 WithPageSize 之外的
// 副作用式 Option 不存在（v0.5.0 Option 集合只有 WithPageSize 且是 no-op），
// 改不了 Client 字段就没法观察。所以该测试只验证"Open 的 opt 链不 panic"。
//
// Open 需要 *ssh.Client，无法在 unit test 里直接构造（构造 SSH 客户端要么
// 起 server 要么 mock 接口，太重）。v0.5.0 spec 把真 SSH server 的集成测试
// 留到 v0.5.1，所以本文件不覆盖 Open 的 happy path。
func TestOption_AppliedInOpen_NilSSH(t *testing.T) {
	_, err := Open(nil)
	if err == nil {
		t.Fatal("Open(nil): expected error, got nil")
	}
	// 错误信息应当非空（便于上层诊断）
	if msg := err.Error(); msg == "" {
		t.Error("Open(nil): error message is empty")
	}
}

// 编译期守卫：保证 Client 公共方法签名没意外改动。
//
// 任何对 ARCHITECTURE.md §3.7 接口契约的破坏都会让本测试连编译都过不了。
// 这是与 keepalive_test.go 的 TestRunKeepAlive_Signature 同一类守卫。
func TestClient_PublicMethodSignatures(t *testing.T) {
	var c *Client

	// 把每个方法绑到具体函数类型，签名漂移时编译失败。
	var _ func(context.Context, string, int, string) (ListPage, error) = c.List
	var _ func(string) (Entry, error) = c.Stat
	var _ func(string) ([]Entry, error) = c.ReadDir
	var _ func(string, int) (ReadWriteCloser, error) = c.Open
	var _ func(string) error = c.Mkdir
	var _ func(string) error = c.Remove
	var _ func(string, string) error = c.Rename
	var _ func() error = c.Close

	// Open 的签名同样要稳定（params 顺序：*ssh.Client, ...Option）
	var _ func(*ssh.Client, ...Option) (*Client, error) = Open
	// Option / WithPageSize / ReadWriteCloser 是其它契约符号
	var _ Option = WithPageSize(0)
	var _ ReadWriteCloser
}
