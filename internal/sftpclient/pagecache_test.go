// pagecache_test.go 覆盖 pageCache 数据结构 + slicePage + token 编解码
// 的所有边界。不依赖 *sftp.Client，纯内存逻辑。
//
// 为什么 pageCache 单测足够：
//   - Client.List 的 "切分 + 算 next token" 全部委托给 slicePage + encodeToken
//   - Client.List 的 "拿缓存" 委托给 pageCache.get
//   - Client.List 的 "存缓存" 委托给 pageCache.put
//   - 真 SFTP 路径（ReadDir 失败）由 integration_test.go::TestSftpList_Pagination 覆盖
//
// 覆盖率目标：100% 路径（pageCache + slicePage + encodeToken + decodeToken）
package sftpclient

import (
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// token 编解码
// -----------------------------------------------------------------------------

// TestEncodeTokenDecodeToken_RoundTrip 验证基本往返：encode → decode 拿到原值。
//
// 覆盖典型 path（无冒号）+ 各种 offset（含 0、含大数）。
func TestEncodeTokenDecodeToken_RoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		offset int
	}{
		{"simple", "/home/user", 0},
		{"root", "/", 0},
		{"non-zero-offset", "/var/log", 200},
		{"large-offset", "/tmp", 10000},
		{"path-with-dots", "/home/user/.ssh", 50},
		{"deep-path", "/a/b/c/d/e/f/g/h/i/j", 999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := encodeToken(tc.path, tc.offset)
			if tok == "" {
				t.Fatal("encodeToken returned empty string")
			}
			gotPath, gotOffset, err := decodeToken(tok)
			if err != nil {
				t.Fatalf("decodeToken(%q): %v", tok, err)
			}
			if gotPath != tc.path {
				t.Errorf("path: got %q, want %q", gotPath, tc.path)
			}
			if gotOffset != tc.offset {
				t.Errorf("offset: got %d, want %d", gotOffset, tc.offset)
			}
		})
	}
}

// TestEncodeTokenDecodeToken_PathWithColon 覆盖关键边界：path 里含 ':'。
//
// 这是 v0.5.3 spec 显式提到的边界：路径里可能含 ':'（POSIX 允许）。
// encodeToken 用 last-colon 分隔（offset 在最后），所以 path 里的 ':' 不会
// 与分隔符冲突。验证 round-trip 不丢 ':'。
func TestEncodeTokenDecodeToken_PathWithColon(t *testing.T) {
	cases := []struct {
		path string
		off  int
	}{
		{"/data/2025-07-14T10:00:00Z", 0},
		{"/tmp/log:error", 100},
		{"/a:b:c:d:e", 42},
		{"/x:y", 1},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			tok := encodeToken(tc.path, tc.off)
			gotPath, gotOff, err := decodeToken(tok)
			if err != nil {
				t.Fatalf("decodeToken: %v", err)
			}
			if gotPath != tc.path {
				t.Errorf("path: got %q, want %q", gotPath, tc.path)
			}
			if gotOff != tc.off {
				t.Errorf("offset: got %d, want %d", gotOff, tc.off)
			}
		})
	}
}

// TestDecodeToken_InvalidBase64 验证 base64 解码失败统一返回 ErrInvalidPageToken。
func TestDecodeToken_InvalidBase64(t *testing.T) {
	cases := []string{
		"",                       // 空
		"!!!",                    // 完全非法字符
		"not_base64_!@#$%^&*()",  // 杂字符
		"a",                      // 单字符（RawURLEncoding 要求 padding-free multiple of 4 after re-pad；但单字符确实不是 valid base64）
		"====",                   // 只有 padding
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, _, err := decodeToken(c)
			if err == nil {
				t.Errorf("decodeToken(%q): expected error, got nil", c)
			}
			// 错误应当统一为 ErrInvalidPageToken（不暴露内部细节）
			if err != ErrInvalidPageToken {
				t.Errorf("decodeToken(%q): error = %v, want ErrInvalidPageToken", c, err)
			}
		})
	}
}

// TestDecodeToken_WrongVersion 验证 version prefix 错误时返回 ErrInvalidPageToken。
func TestDecodeToken_WrongVersion(t *testing.T) {
	// base64("v0:path:0") —— v0 不存在
	tok := "djA6cGF0aDow" // 实际编码值，下面解释
	// 上面的 token 是手算的，更稳妥的方式是用 encodeToken 借位：
	// 这里我们直接用 RawURLEncoding 构造一个 "v0:..." 的 token
	tok = mustEncode(t, "v0:/foo:0")
	_, _, err := decodeToken(tok)
	if err != ErrInvalidPageToken {
		t.Errorf("v0 token: error = %v, want ErrInvalidPageToken", err)
	}

	tok = mustEncode(t, "v2:/foo:0")
	_, _, err = decodeToken(tok)
	if err != ErrInvalidPageToken {
		t.Errorf("v2 token: error = %v, want ErrInvalidPageToken", err)
	}

	// 没有 version prefix
	tok = mustEncode(t, "/foo:0")
	_, _, err = decodeToken(tok)
	if err != ErrInvalidPageToken {
		t.Errorf("no-version token: error = %v, want ErrInvalidPageToken", err)
	}
}

// TestDecodeToken_MalformedPayload 验证 base64 decode 成功但 payload 结构不合法。
func TestDecodeToken_MalformedPayload(t *testing.T) {
	cases := []struct {
		name    string
		rawBody string
	}{
		{"missing-version-prefix", "/foo:0"},
		{"only-version-prefix", "v1:"},
		{"version-but-no-colon", "v1:no-colon-here"},
		{"empty-offset", "v1:/foo:"},
		{"non-numeric-offset", "v1:/foo:abc"},
		{"negative-offset", "v1:/foo:-1"},
		{"hex-offset", "v1:/foo:0x10"},
		{"float-offset", "v1:/foo:1.5"},
		{"trailing-garbage", "v1:/foo:0/extra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := mustEncode(t, tc.rawBody)
			_, _, err := decodeToken(tok)
			if err != ErrInvalidPageToken {
				t.Errorf("decodeToken(%q from body %q): error = %v, want ErrInvalidPageToken",
					tok, tc.rawBody, err)
			}
		})
	}
}

// TestEncodeToken_FormatExact 钉死 token 字符级格式（防 regression）。
//
// 格式：base64.RawURLEncoding("v1:" + path + ":" + strconv.Itoa(offset))
//   - 没有 padding（'=' 不会出现）
//   - 不含 '+' '/'（RawURL 用 '-' '_' 替代）
//   - 大小写不敏感（base64 字母表 + 大写），但 RawURL 是小写 + 大写混合
func TestEncodeToken_FormatExact(t *testing.T) {
	tok := encodeToken("/foo", 10)

	// 1. RawURLEncoding 的字符集：字母 + 数字 + '-' + '_'
	for i, r := range tok {
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			t.Errorf("token[%d] = %q is not in RawURLEncoding alphabet", i, r)
		}
	}

	// 2. 没有 padding
	if strings.Contains(tok, "=") {
		t.Errorf("token %q contains '=' (RawURLEncoding should not pad)", tok)
	}

	// 3. 长度验证：raw 是 "v1:/foo:10" = 10 字节，base64 后 14 字符（4n 向上取整 + 减 padding）
	//    实际计算：10 字节 → ceil(10/3)*4 = 16，但 RawURL 不 padding → 16 字符（10 字节 = 3*3 + 1 → 16）
	//    手工验算：base64("v1:/foo:10") → 13 个数据字符 + 0 padding = 实际 base64 长度 16? 让我验算
	//    更稳的做法：直接 decode 回来，看是否等于 raw
	rawBytes := rawURLDecode(t, tok)
	if string(rawBytes) != "v1:/foo:10" {
		t.Errorf("decoded = %q, want %q", rawBytes, "v1:/foo:10")
	}
}

// TestEncodeToken_ZeroOffset 验证 offset=0 的 token 能 round-trip。
//
// 边界：offset 字符串是 "0"（单个字符），path 段为空时
// raw = "v1::0"，decode 必须能切出 path=""、offset=0。
func TestEncodeToken_ZeroOffset(t *testing.T) {
	tok := encodeToken("/foo", 0)
	gotPath, gotOff, err := decodeToken(tok)
	if err != nil {
		t.Fatalf("decodeToken: %v", err)
	}
	if gotPath != "/foo" || gotOff != 0 {
		t.Errorf("got (%q, %d), want (%q, 0)", gotPath, gotOff, "/foo")
	}
}

// -----------------------------------------------------------------------------
// slicePage 切分逻辑
// -----------------------------------------------------------------------------

// TestSlicePage_BasicPagination 覆盖典型分页场景。
func TestSlicePage_BasicPagination(t *testing.T) {
	entries := makeEntries(30) // 0..29

	// page 1: offset=0, pageSize=10 → [0,10), next=10
	p, next := slicePage(entries, 0, 10)
	if len(p) != 10 || p[0].Name != "entry-0" || p[9].Name != "entry-9" {
		t.Errorf("page 1: %+v", names(p))
	}
	if next != 10 {
		t.Errorf("page 1 next: got %d, want 10", next)
	}

	// page 2: offset=10, pageSize=10 → [10,20), next=20
	p, next = slicePage(entries, 10, 10)
	if len(p) != 10 || p[0].Name != "entry-10" || p[9].Name != "entry-19" {
		t.Errorf("page 2: %+v", names(p))
	}
	if next != 20 {
		t.Errorf("page 2 next: got %d, want 20", next)
	}

	// page 3: offset=20, pageSize=10 → [20,30), next=-1 (无更多)
	p, next = slicePage(entries, 20, 10)
	if len(p) != 10 || p[0].Name != "entry-20" || p[9].Name != "entry-29" {
		t.Errorf("page 3: %+v", names(p))
	}
	if next != -1 {
		t.Errorf("page 3 next: got %d, want -1", next)
	}
}

// TestSlicePage_PartialLastPage 覆盖最后一页不足 pageSize 的情况。
func TestSlicePage_PartialLastPage(t *testing.T) {
	entries := makeEntries(25)

	// 30 entries 不行，先用 25
	// page 1: offset=0, pageSize=10 → 10 entries, next=10
	// page 2: offset=10, pageSize=10 → 10 entries, next=20
	// page 3: offset=20, pageSize=10 → 5 entries, next=-1

	p, next := slicePage(entries, 20, 10)
	if len(p) != 5 {
		t.Errorf("last page size: got %d, want 5", len(p))
	}
	if p[0].Name != "entry-20" || p[4].Name != "entry-24" {
		t.Errorf("last page content: %+v", names(p))
	}
	if next != -1 {
		t.Errorf("last page next: got %d, want -1", next)
	}
}

// TestSlicePage_PageSizeLargerThanEntries 覆盖 pageSize > len(entries)。
func TestSlicePage_PageSizeLargerThanEntries(t *testing.T) {
	entries := makeEntries(5)
	p, next := slicePage(entries, 0, 100)
	if len(p) != 5 {
		t.Errorf("size: got %d, want 5", len(p))
	}
	if next != -1 {
		t.Errorf("next: got %d, want -1", next)
	}
}

// TestSlicePage_EmptyEntries 覆盖空目录。
func TestSlicePage_EmptyEntries(t *testing.T) {
	var entries []Entry // nil
	p, next := slicePage(entries, 0, 10)
	if len(p) != 0 {
		t.Errorf("empty page size: got %d, want 0", len(p))
	}
	if next != -1 {
		t.Errorf("empty next: got %d, want -1", next)
	}
}

// TestSlicePage_OffsetPastEnd 覆盖 offset > len(entries) 的越界情况。
//
// 防御性：用户可能持有一个 stale token（缓存里的目录被清空了），或
// offset 是其他原因算大了。返回空页 + next=-1 比 panic 安全。
func TestSlicePage_OffsetPastEnd(t *testing.T) {
	entries := makeEntries(10)
	p, next := slicePage(entries, 100, 10)
	if len(p) != 0 {
		t.Errorf("past-end size: got %d, want 0", len(p))
	}
	if next != -1 {
		t.Errorf("past-end next: got %d, want -1", next)
	}
}

// TestSlicePage_OffsetExactlyAtEnd 边界：offset == len(entries)。
func TestSlicePage_OffsetExactlyAtEnd(t *testing.T) {
	entries := makeEntries(10)
	p, next := slicePage(entries, 10, 10)
	if len(p) != 0 {
		t.Errorf("at-end size: got %d, want 0", len(p))
	}
	if next != -1 {
		t.Errorf("at-end next: got %d, want -1", next)
	}
}

// TestSlicePage_NegativeOffsetTreatedAsZero 防御性：负 offset 应被规整为 0。
//
// 正常路径下不会传负数（decodeToken 已经挡了），但 slicePage 单独被
// 调用时（如果将来）不应 panic。
func TestSlicePage_NegativeOffsetTreatedAsZero(t *testing.T) {
	entries := makeEntries(10)
	p, _ := slicePage(entries, -5, 3)
	if len(p) != 3 {
		t.Errorf("size: got %d, want 3", len(p))
	}
	if p[0].Name != "entry-0" {
		t.Errorf("first: got %q, want entry-0", p[0].Name)
	}
}

// -----------------------------------------------------------------------------
// pageCache 行为
// -----------------------------------------------------------------------------

// TestPageCache_PutAndGet 覆盖基本 put/get。
func TestPageCache_PutAndGet(t *testing.T) {
	pc := newPageCache()

	entries := makeEntries(5)
	pc.put("/a", pageState{entries: entries})

	got, ok := pc.get("/a")
	if !ok {
		t.Fatal("get /a: not found")
	}
	if len(got.entries) != 5 {
		t.Errorf("len: got %d, want 5", len(got.entries))
	}
	if got.entries[0].Name != "entry-0" {
		t.Errorf("first: got %q, want entry-0", got.entries[0].Name)
	}
}

// TestPageCache_GetMiss 覆盖未命中。
func TestPageCache_GetMiss(t *testing.T) {
	pc := newPageCache()
	_, ok := pc.get("/nonexistent")
	if ok {
		t.Error("get /nonexistent: got ok=true, want false")
	}
}

// TestPageCache_PutOverwrite 覆盖同名 path 重复 put → 覆盖 state，size 不变。
func TestPageCache_PutOverwrite(t *testing.T) {
	pc := newPageCache()
	pc.put("/a", pageState{entries: makeEntries(3)})
	pc.put("/a", pageState{entries: makeEntries(7)})

	if pc.size() != 1 {
		t.Errorf("size: got %d, want 1", pc.size())
	}
	got, _ := pc.get("/a")
	if len(got.entries) != 7 {
		t.Errorf("overwrite: got %d, want 7", len(got.entries))
	}
}

// TestPageCache_LRUEviction 钉死 LRU 上限：maxCacheEntries 个 path 后，
// 继续 put 触发 LRU 尾部淘汰。
func TestPageCache_LRUEviction(t *testing.T) {
	pc := newPageCache()

	// 装满 maxCacheEntries 个
	for i := 0; i < maxCacheEntries; i++ {
		pc.put(pathFromInt(i), pageState{entries: makeEntries(1)})
	}
	if pc.size() != maxCacheEntries {
		t.Fatalf("after fill: size = %d, want %d", pc.size(), maxCacheEntries)
	}

	// 触发一次淘汰
	pc.put(pathFromInt(maxCacheEntries), pageState{entries: makeEntries(1)})
	if pc.size() != maxCacheEntries {
		t.Errorf("after 1 over-fill: size = %d, want %d (LRU should cap)", pc.size(), maxCacheEntries)
	}

	// 第 0 个 path 应该被淘汰（最久未访问）
	_, ok := pc.get(pathFromInt(0))
	if ok {
		t.Error("path 0 should be evicted (oldest)")
	}

	// 第 1 个 path 应该还在
	_, ok = pc.get(pathFromInt(1))
	if !ok {
		t.Error("path 1 should still be present")
	}

	// 第 maxCacheEntries 个应该被缓存
	_, ok = pc.get(pathFromInt(maxCacheEntries))
	if !ok {
		t.Error("newest path should be present")
	}
}

// TestPageCache_GetMovesToFront 钉死 LRU 语义：get 命中时把节点挪到队首。
//
// 关键：put 100 个后，get 第 50 个会把它挪到队首。但 back 不变
// （path 0 仍然是最久未用的，因为它是第一个 put 的），所以再 put
// 第 100 个时淘汰的是 path 0，不是 path 1。
func TestPageCache_GetMovesToFront(t *testing.T) {
	pc := newPageCache()
	for i := 0; i < maxCacheEntries; i++ {
		pc.put(pathFromInt(i), pageState{entries: makeEntries(1)})
	}
	// LRU 顺序（container/list 的语义：Front = 最近使用 / Back = 最久未用）：
	//   Front=99, 98, 97, ..., 51, 50, 49, ..., 1, 0=Back
	// 最后一个 put (99) 在最前，最早的 put (0) 在最后。

	// get 第 50 个 → 把它从中间挪到 front
	_, _ = pc.get(pathFromInt(50))
	// LRU 顺序：Front=50, 99, 98, ..., 51, 49, 48, ..., 1, 0=Back
	// 50 被抽到 front；其他相对顺序不变；back 仍是 path 0（最早 put）
	order := pc.lruOrder()
	if order[0] != pathFromInt(50) {
		t.Errorf("after get(50): order[0] = %q, want %q", order[0], pathFromInt(50))
	}
	if order[len(order)-1] != pathFromInt(0) {
		t.Errorf("after get(50): order[back] = %q, want %q (path 0 was the first put, still LRU back)", order[len(order)-1], pathFromInt(0))
	}

	// put 第 100 个 → 淘汰 back (path 0)
	pc.put(pathFromInt(maxCacheEntries), pageState{entries: makeEntries(1)})

	_, ok := pc.get(pathFromInt(0))
	if ok {
		t.Error("path 0 should be evicted (was LRU back after put 100; get(50) didn't change back)")
	}

	// 第 50 个应当还在（被 get 提升过）
	_, ok = pc.get(pathFromInt(50))
	if !ok {
		t.Error("path 50 should still be present (was promoted to MRU by get)")
	}

	// 第 100 个应当被缓存
	_, ok = pc.get(pathFromInt(maxCacheEntries))
	if !ok {
		t.Error("newest path 100 should be present")
	}

	// 验证 back 现在是 path 1（之前的 back=0 被淘汰了）
	order2 := pc.lruOrder()
	if order2[len(order2)-1] != pathFromInt(1) {
		t.Errorf("after put(100): order[back] = %q, want %q", order2[len(order2)-1], pathFromInt(1))
	}
}

// TestPageCache_PutMovesToFront 钉死 put 已存在 path → 提升到 MRU。
func TestPageCache_PutMovesToFront(t *testing.T) {
	pc := newPageCache()
	for i := 0; i < maxCacheEntries; i++ {
		pc.put(pathFromInt(i), pageState{entries: makeEntries(1)})
	}
	// 此时 LRU：Front=99, ..., 1, 0=Back（0 是最久未用的）
	// put 0 重新写一次 → 0 升到 front
	pc.put(pathFromInt(0), pageState{entries: makeEntries(2)})
	order := pc.lruOrder()
	if order[0] != pathFromInt(0) {
		t.Errorf("after re-put(0): order[0] = %q, want %q", order[0], pathFromInt(0))
	}
	// 此时 back 应该是 path 1（0 被提到 front 后，1 变成最久未用）
	if order[len(order)-1] != pathFromInt(1) {
		t.Errorf("after re-put(0): order[back] = %q, want %q", order[len(order)-1], pathFromInt(1))
	}

	// put 第 100 个 → 淘汰 back (path 1)
	pc.put(pathFromInt(maxCacheEntries), pageState{entries: makeEntries(1)})
	_, ok := pc.get(pathFromInt(1))
	if ok {
		t.Error("path 1 should be evicted (was LRU back after re-put of 0)")
	}
}

// TestPageCache_Clear 钉死 clear 后 size=0、所有 path 拿不到。
func TestPageCache_Clear(t *testing.T) {
	pc := newPageCache()
	pc.put("/a", pageState{entries: makeEntries(1)})
	pc.put("/b", pageState{entries: makeEntries(2)})
	if pc.size() != 2 {
		t.Fatalf("size before clear: got %d, want 2", pc.size())
	}

	pc.clear()
	if pc.size() != 0 {
		t.Errorf("size after clear: got %d, want 0", pc.size())
	}
	if _, ok := pc.get("/a"); ok {
		t.Error("/a should be gone after clear")
	}
}

// TestPageCache_ConcurrentSafe 验证 pageCache 在 race detector 下安全。
//
// 10 goroutine × 100 op 混 put/get/size，race 干净是底线。
func TestPageCache_ConcurrentSafe(t *testing.T) {
	pc := newPageCache()
	const goroutines = 10
	const ops = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				p := pathFromInt((gid * ops + i) % 50) // 50 个 path 轮转
				if i%3 == 0 {
					pc.put(p, pageState{entries: makeEntries(i)})
				} else if i%3 == 1 {
					_, _ = pc.get(p)
				} else {
					_ = pc.size()
				}
			}
		}(g)
	}
	wg.Wait()
	// 跑完没 panic / 没 race = 通过
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// makeEntries 生成 n 个有 Name="entry-i" 的 stub entries。
func makeEntries(n int) []Entry {
	out := make([]Entry, n)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		out[i] = Entry{
			Name:    "entry-" + itoa(i),
			Path:    "/test/entry-" + itoa(i),
			Size:    int64(i),
			Mode:    0o644,
			ModTime: now,
		}
	}
	return out
}

// itoa 是 strconv.Itoa 的零依赖本地实现（避免 import 加重）。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}

// pathFromInt 是测试里用到的 path 生成器（避免数字 → string 拼路径繁琐）。
func pathFromInt(i int) string {
	return "/p/" + itoa(i)
}

// names 提取 entries 的 Name 字段（断言失败的 error message 用）。
func names(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name
	}
	return out
}

// mustEncode 用 RawURLEncoding 把 s 编码出来，错了 t.Fatal（测试用 helper）。
func mustEncode(t *testing.T, s string) string {
	t.Helper()
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// rawURLDecode 是 base64.RawURLEncoding.DecodeString 的本地包装（t.Helper + 错误日志）。
func rawURLDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("RawURLEncoding.DecodeString(%q): %v", s, err)
	}
	return b
}
