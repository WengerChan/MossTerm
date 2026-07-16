// sftpd_test.go 自测 in-process SSH + 真实 OpenSSH sftp-server 桩。
//
// windows runner / 找不到 sftp-server binary → t.Skip（**不 Fatal**），
// 让 CI 在不同 runner 上都能跑得通。
//
//go:build !windows

package sftpd

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// dialSFTP 拿 server host:port + user/password 构造 SSH client + SFTP client。
// 失败 t.Fatal。
//
// **Cleanup 顺序很重要**：t.Cleanup 是 LIFO，所以最后注册的先跑。这里把
// sshClient.Close 放在第二个注册位置 → 它会**先**跑 → 关掉 SSH conn →
// sftp-server 进程收到 stdin EOF → 退出 → sftp client 的 readLoop 也退出
// → 然后 sftpCli.Close 顺利返回。如果反过来（sftp 先关），sftp 内部
// 等 readLoop 退出，但 readLoop 等 SSH channel 关闭，而 SSH channel 又
// 等 sftp-server 退出 → 死锁。
func dialSFTP(t *testing.T, s *SFTPD) *sftp.Client {
	t.Helper()
	host, port := s.HostPort()
	clientCfg := &ssh.ClientConfig{
		User:            s.User(),
		Auth:            []ssh.AuthMethod{ssh.Password(s.Password())},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	sshClient, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		t.Fatalf("ssh.Dial: %v", err)
	}

	sftpCli, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		t.Fatalf("sftp.NewClient: %v", err)
	}
	// 注册顺序：sftp 先注册（晚跑），ssh 后注册（先跑）→ 退出时 ssh 先关
	t.Cleanup(func() { _ = sftpCli.Close() })
	t.Cleanup(func() { _ = sshClient.Close() })
	return sftpCli
}

// TestSFTPD_BasicRoundTrip 最小化 SFTP 端到端：写文件 + 读回 + 字节级一致。
//
// 用**相对路径**（sftp-server -d 只 chdir 不 chroot；绝对路径会越界）。
func TestSFTPD_BasicRoundTrip(t *testing.T) {
	srv := Start(t, Options{})
	cli := dialSFTP(t, srv)

	const path = "hello.txt"
	want := []byte("hello sftpd\n")

	f, err := cli.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write(want); err != nil {
		_ = f.Close()
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close write: %v", err)
	}

	rf, err := cli.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rf.Close()
	got, err := io.ReadAll(rf)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("content: got %q, want %q", got, want)
	}

	// 落到真实文件系统（sftp-server -d 行为：相对路径落 WorkDir）
	diskPath := filepath.Join(srv.WorkDir, path)
	diskData, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", diskPath, err)
	}
	if !bytes.Equal(diskData, want) {
		t.Errorf("disk content: got %q, want %q", diskData, want)
	}
}

// TestSFTPD_Mkdir_Remove 覆盖 mkdir + write + remove + stat。
func TestSFTPD_Mkdir_Remove(t *testing.T) {
	srv := Start(t, Options{})
	cli := dialSFTP(t, srv)

	dir := "sub"
	if err := cli.Mkdir(dir); err != nil {
		t.Fatalf("Mkdir(%s): %v", dir, err)
	}

	// 写一个文件到子目录
	filePath := dir + "/file.txt"
	want := []byte("inside sub\n")
	f, err := cli.Create(filePath)
	if err != nil {
		t.Fatalf("Create(%s): %v", filePath, err)
	}
	if _, err := f.Write(want); err != nil {
		_ = f.Close()
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// stat 应存在
	info, err := cli.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() != int64(len(want)) {
		t.Errorf("Stat size: got %d, want %d", info.Size(), len(want))
	}
	if info.IsDir() {
		t.Errorf("Stat IsDir: got true, want false")
	}

	// 删文件
	if err := cli.Remove(filePath); err != nil {
		t.Fatalf("Remove(%s): %v", filePath, err)
	}
	if _, err := cli.Stat(filePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat after Remove: got err=%v, want os.ErrNotExist", err)
	}

	// 删目录
	if err := cli.RemoveDirectory(dir); err != nil {
		t.Fatalf("RemoveDirectory(%s): %v", dir, err)
	}
	if _, err := cli.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat dir after Remove: got err=%v, want os.ErrNotExist", err)
	}
}

// TestSFTPD_WorkDirIsolated 验证 sftp-server 的"相对路径落在 WorkDir 内"行为。
//
// 关键发现（v0.6.3 集成测试设计依据）：
//   - sftp-server 的 -d 标志**只 chdir 不 chroot**
//   - 相对路径（"foo.txt"）落在 WorkDir 内
//   - 绝对路径（"/foo.txt"）走真实文件系统根（sftp-server 不阻止 ../ 越界）
//
// 验证策略：
//   - 在 WorkDir 预置 anchor.txt（content = "INSIDE"）
//   - SFTP 客户端用相对路径读 "anchor.txt" → 应拿到 INSIDE
//   - SFTP 客户端用相对路径写 "new.txt" → 应落在 WorkDir/new.txt
//   - 同时验证 RealPath(".") 返回 WorkDir 路径
func TestSFTPD_WorkDirIsolated(t *testing.T) {
	srv := Start(t, Options{})

	// WorkDir 内的 anchor
	const anchor = "anchor.txt"
	if err := os.WriteFile(filepath.Join(srv.WorkDir, anchor), []byte("INSIDE"), 0o600); err != nil {
		t.Fatalf("WriteFile anchor: %v", err)
	}

	cli := dialSFTP(t, srv)

	// 1) RealPath(".") 应映射到 WorkDir（说明 sftp-server cwd 正确）
	rp, err := cli.RealPath(".")
	if err != nil {
		t.Fatalf("RealPath(.): %v", err)
	}
	// macOS 的 /var/folders → /private/var/folders 符号链接，要 resolve
	absWorkDir, err := filepath.EvalSymlinks(srv.WorkDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", srv.WorkDir, err)
	}
	if rp != absWorkDir {
		t.Errorf("RealPath(.): got %q, want %q", rp, absWorkDir)
	}

	// 2) 读 anchor（应成功 + 内容 INSIDE）
	got, err := readAllSFTP(t, cli, anchor)
	if err != nil {
		t.Fatalf("read %s: %v", anchor, err)
	}
	if string(got) != "INSIDE" {
		t.Errorf("anchor content: got %q, want %q", got, "INSIDE")
	}

	// 3) 用相对路径写新文件 → 应落在 WorkDir
	const newFile = "created-by-sftp.txt"
	f, err := cli.Create(newFile)
	if err != nil {
		t.Fatalf("Create(%s): %v", newFile, err)
	}
	if _, err := f.Write([]byte("CREATED")); err != nil {
		_ = f.Close()
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 4) 验证文件真的落在 WorkDir 内（sftp-server -d 行为）
	diskPath := filepath.Join(srv.WorkDir, newFile)
	diskData, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v (relative path NOT isolated to WorkDir!)", diskPath, err)
	}
	if string(diskData) != "CREATED" {
		t.Errorf("disk content: got %q, want %q", diskData, "CREATED")
	}

	// 5) ReadDir(".") 应只看到 WorkDir 内的文件（preload + new），不会暴露
	//    整个文件系统（sftp-server 的 cwd 是 WorkDir）
	infos, err := cli.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	names := make(map[string]bool, len(infos))
	for _, i := range infos {
		names[i.Name()] = true
	}
	if !names[anchor] {
		t.Errorf("ReadDir(.) missing anchor: %v", names)
	}
	if !names[newFile] {
		t.Errorf("ReadDir(.) missing newFile: %v", names)
	}
}

// readAllSFTP 打开 path 读完全部内容。错误时 t.Fatal。
func readAllSFTP(t *testing.T, cli *sftp.Client, path string) ([]byte, error) {
	t.Helper()
	f, err := cli.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// TestSFTPD_OptionsOverride 验证 Options 字段（User / Password）生效。
func TestSFTPD_OptionsOverride(t *testing.T) {
	srv := Start(t, Options{
		User:     "alice",
		Password: "s3cret",
	})
	if srv.User() != "alice" {
		t.Errorf("User: got %q, want %q", srv.User(), "alice")
	}
	if srv.Password() != "s3cret" {
		t.Errorf("Password: got %q, want %q", srv.Password(), "s3cret")
	}
	// 实际 dial 用这套凭证
	host, port := srv.HostPort()
	cfg := &ssh.ClientConfig{
		User:            "alice",
		Auth:            []ssh.AuthMethod{ssh.Password("s3cret")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	c, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("ssh.Dial with custom creds: %v", err)
	}
	_ = c.Close()
}
