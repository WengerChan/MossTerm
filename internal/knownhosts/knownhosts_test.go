package knownhosts

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// generateTestKey 生成测试用 ed25519 signer。
func generateTestKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}
	return signer
}

// khLine 生成一个 OpenSSH 格式的 known_hosts 行（无 comment）。
func khLine(t *testing.T, host string, signer ssh.Signer) string {
	t.Helper()
	return khLineWithComment(t, host, "", signer)
}

// khLineWithComment 生成一个 OpenSSH 格式的 known_hosts 行（带 comment）。
func khLineWithComment(t *testing.T, host, comment string, signer ssh.Signer) string {
	t.Helper()
	keyBase64 := base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal())
	line := fmt.Sprintf("%s ssh-ed25519 %s", host, keyBase64)
	if comment != "" {
		line += " " + comment
	}
	return line
}

// writeKH 把 content 写入临时目录下的 known_hosts 文件并返回路径。
func writeKH(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "kh")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test known_hosts: %v", err)
	}
	return path
}

// =============================================================================
// 基础 API 测试（v0.1.3 行为保持）
// =============================================================================

func TestNew_EmptyPath(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("New(\"\") should return error")
	}
}

func TestNew_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "kh") // 测试父目录自动创建
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Size() != 0 {
		t.Errorf("Size = %d, want 0", m.Size())
	}
	if m.Path() != path {
		t.Errorf("Path = %q, want %q", m.Path(), path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestAdd_And_HostKeyCallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kh")
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	signer := generateTestKey(t)
	host := "example.com"
	cb := m.HostKeyCallback()

	// 1. 第一次连接：未找到 → 自动 Add + 放行
	if err := cb(host, &net.TCPAddr{IP: nil, Port: 22}, signer.PublicKey()); err != nil {
		t.Fatalf("first connect: HostKeyCallback returned err = %v", err)
	}
	if m.Size() != 1 {
		t.Errorf("Size after Add = %d, want 1", m.Size())
	}

	// 2. 第二次连接：找到 + 匹配 → 放行
	cb2 := m.HostKeyCallback()
	if err := cb2(host, &net.TCPAddr{}, signer.PublicKey()); err != nil {
		t.Errorf("second connect (same key): err = %v, want nil", err)
	}

	// 3. 不同 key 同一 host → MITM 拒绝
	otherSigner := generateTestKey(t)
	if err := cb2(host, &net.TCPAddr{}, otherSigner.PublicKey()); err == nil {
		t.Error("third connect (different key): err = nil, want mismatch error")
	} else if err != ErrHostKeyMismatch {
		t.Errorf("third connect: err = %v, want ErrHostKeyMismatch", err)
	}
}

func TestLoadFromFile(t *testing.T) {
	signer := generateTestKey(t)
	content := "# comment line\n\n" + khLineWithComment(t, "example.com", "user@host", signer) + "\n"
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1", m.Size())
	}

	// 验证：example.com 应该通过校验
	if err := m.Authorize("example.com", signer.PublicKey()); err != nil {
		t.Errorf("Authorize(example.com) = %v, want nil", err)
	}
}

func TestAdd_PersistsToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kh")
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer := generateTestKey(t)

	if err := m.Add("foo.example.com", signer.PublicKey(), "test"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// 重新加载
	m2, err := New(path)
	if err != nil {
		t.Fatalf("New (reload): %v", err)
	}
	if m2.Size() != 1 {
		t.Errorf("Size after reload = %d, want 1", m2.Size())
	}
	if err := m2.Authorize("foo.example.com", signer.PublicKey()); err != nil {
		t.Errorf("Authorize after reload = %v, want nil", err)
	}
}

// =============================================================================
// v0.2.0b 新增：通配符 / 端口 / 多 host / 范围匹配
// =============================================================================

func TestWildcardMatch(t *testing.T) {
	cases := []struct {
		pat, str string
		want     bool
	}{
		// 精确匹配
		{"example.com", "example.com", true},
		{"example.com", "foo.example.com", false},
		// `*` 匹配任意序列（含空）
		{"*", "", true},
		{"*", "anything", true},
		{"*", "example.com", true},
		// `*` 不跨端口边界（无端口概念，port 由外层独立匹配）
		{"*.example.com", "example.com", false},  // OpenSSH 规则：不匹配基域
		{"*.example.com", "foo.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.org", false},
		// IP 范围
		{"192.168.1.*", "192.168.1.1", true},
		{"192.168.1.*", "192.168.1.255", true},
		{"192.168.1.*", "192.168.2.1", false},
		// `?` 匹配任意单字符
		{"hos?", "host", true},
		{"hos?", "hose", true},
		{"hos?", "hosts", false}, // 长度不匹配
		// 混合
		{"a*c", "abc", true},
		{"a*c", "ac", true},
		{"a*c", "abbbbc", true},
		{"a*c", "abd", false},
	}
	for _, c := range cases {
		got := wildcardMatch(c.pat, c.str)
		if got != c.want {
			t.Errorf("wildcardMatch(%q, %q) = %v, want %v", c.pat, c.str, got, c.want)
		}
	}
}

func TestParsePattern(t *testing.T) {
	cases := []struct {
		in      string
		wantH   string
		wantP   string
		wantErr bool
	}{
		{"example.com", "example.com", "22", false},
		{"example.com:22", "example.com", "22", false},
		{"example.com:2222", "example.com", "2222", false},
		{"[example.com]:2222", "example.com", "2222", false},
		{"[::1]:2222", "::1", "2222", false},
		{"192.168.1.10", "192.168.1.10", "22", false},
		{"*.example.com", "*.example.com", "22", false},
		{"192.168.1.*", "192.168.1.*", "22", false},
	}
	for _, c := range cases {
		p, err := parsePattern(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parsePattern(%q) err = %v, wantErr = %v", c.in, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if p.host != c.wantH || p.port != c.wantP {
			t.Errorf("parsePattern(%q) = {host:%q, port:%q}, want {host:%q, port:%q}",
				c.in, p.host, p.port, c.wantH, c.wantP)
		}
	}
}

func TestFormatPattern(t *testing.T) {
	cases := []struct {
		in   pattern
		want string
	}{
		{pattern{"example.com", "22"}, "example.com"},
		{pattern{"example.com", "2222"}, "[example.com]:2222"},
		{pattern{"::1", "22"}, "[::1]"},
		{pattern{"::1", "2222"}, "[::1]:2222"},
		{pattern{"192.168.1.10", "22"}, "192.168.1.10"},
		{pattern{"*.example.com", "22"}, "*.example.com"},
		{pattern{"192.168.1.*", "22"}, "192.168.1.*"},
	}
	for _, c := range cases {
		got := formatPattern(c.in)
		if got != c.want {
			t.Errorf("formatPattern({%q,%q}) = %q, want %q", c.in.host, c.in.port, got, c.want)
		}
	}
}

func TestLoadFromFile_WildcardPattern(t *testing.T) {
	signer := generateTestKey(t)
	content := khLine(t, "*.example.com", signer) + "\n"
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Size() != 1 {
		t.Fatalf("Size = %d, want 1", m.Size())
	}

	// 子域应该匹配
	for _, host := range []string{"foo.example.com", "a.b.example.com", "deep.nested.example.com"} {
		if err := m.Authorize(host, signer.PublicKey()); err != nil {
			t.Errorf("Authorize(%q) = %v, want nil", host, err)
		}
	}
	// 基域不匹配（OpenSSH 规则）
	if err := m.Authorize("example.com", signer.PublicKey()); err != ErrHostUnknown {
		t.Errorf("Authorize(example.com) = %v, want ErrHostUnknown", err)
	}
	// 不相关域名不匹配
	if err := m.Authorize("foo.org", signer.PublicKey()); err != ErrHostUnknown {
		t.Errorf("Authorize(foo.org) = %v, want ErrHostUnknown", err)
	}
}

func TestLoadFromFile_IPRangePattern(t *testing.T) {
	signer := generateTestKey(t)
	content := khLine(t, "192.168.1.*", signer) + "\n"
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 范围内 IP 匹配
	for _, host := range []string{"192.168.1.1", "192.168.1.100", "192.168.1.255"} {
		if err := m.Authorize(host, signer.PublicKey()); err != nil {
			t.Errorf("Authorize(%q) = %v, want nil", host, err)
		}
	}
	// 范围外不匹配
	for _, host := range []string{"192.168.2.1", "10.0.0.1", "192.168.1"} {
		if err := m.Authorize(host, signer.PublicKey()); err != ErrHostUnknown {
			t.Errorf("Authorize(%q) = %v, want ErrHostUnknown", host, err)
		}
	}
}

func TestLoadFromFile_PortPattern(t *testing.T) {
	signer := generateTestKey(t)
	content := khLine(t, "[example.com]:2222", signer) + "\n"
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 显式带端口的查询命中
	if err := m.Authorize("example.com:2222", signer.PublicKey()); err != nil {
		t.Errorf("Authorize(example.com:2222) = %v, want nil", err)
	}
	// 显式 [host]:port 形式（SSH client 传 IPv6 习惯）也命中
	if err := m.Authorize("[example.com]:2222", signer.PublicKey()); err != nil {
		t.Errorf("Authorize([example.com]:2222) = %v, want nil", err)
	}
	// 默认端口 22 不命中（OpenSSH 规则：端口必须匹配）
	if err := m.Authorize("example.com", signer.PublicKey()); err != ErrHostUnknown {
		t.Errorf("Authorize(example.com) = %v, want ErrHostUnknown", err)
	}
	if err := m.Authorize("example.com:22", signer.PublicKey()); err != ErrHostUnknown {
		t.Errorf("Authorize(example.com:22) = %v, want ErrHostUnknown", err)
	}
	// 其他端口也不命中
	if err := m.Authorize("example.com:3333", signer.PublicKey()); err != ErrHostUnknown {
		t.Errorf("Authorize(example.com:3333) = %v, want ErrHostUnknown", err)
	}
}

func TestLoadFromFile_MultipleHostsPerLine(t *testing.T) {
	signer := generateTestKey(t)
	// 同一行声明多个 host：example.com 和 192.168.1.10 共用同一 key
	content := khLine(t, "example.com,192.168.1.10", signer) + "\n"
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1 (one entry, two patterns)", m.Size())
	}

	// 两个 host 都应该匹配
	if err := m.Authorize("example.com", signer.PublicKey()); err != nil {
		t.Errorf("Authorize(example.com) = %v, want nil", err)
	}
	if err := m.Authorize("192.168.1.10", signer.PublicKey()); err != nil {
		t.Errorf("Authorize(192.168.1.10) = %v, want nil", err)
	}
	// 不相关的 host 不匹配
	if err := m.Authorize("foo.com", signer.PublicKey()); err != ErrHostUnknown {
		t.Errorf("Authorize(foo.com) = %v, want ErrHostUnknown", err)
	}

	// 验证：MITM 检测对任一 host 都生效
	otherSigner := generateTestKey(t)
	if err := m.Authorize("example.com", otherSigner.PublicKey()); err != ErrHostKeyMismatch {
		t.Errorf("Authorize(example.com, other) = %v, want ErrHostKeyMismatch", err)
	}
}

func TestLoadFromFile_MultipleHostsAndPort(t *testing.T) {
	signer := generateTestKey(t)
	// 多 host 含端口 + 通配符
	content := khLine(t, "example.com,[192.168.1.10]:2222,*.internal", signer) + "\n"
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := m.Authorize("example.com", signer.PublicKey()); err != nil {
		t.Errorf("Authorize(example.com) = %v, want nil", err)
	}
	if err := m.Authorize("192.168.1.10:2222", signer.PublicKey()); err != nil {
		t.Errorf("Authorize(192.168.1.10:2222) = %v, want nil", err)
	}
	if err := m.Authorize("foo.internal", signer.PublicKey()); err != nil {
		t.Errorf("Authorize(foo.internal) = %v, want nil", err)
	}
	if err := m.Authorize("a.b.internal", signer.PublicKey()); err != nil {
		t.Errorf("Authorize(a.b.internal) = %v, want nil", err)
	}
}

func TestLoadFromFile_HashEntry_Skipped(t *testing.T) {
	signer := generateTestKey(t)
	// hash 编码行 + 正常行混合；hash 行应被静默跳过
	content := strings.Join([]string{
		"# hash entry below should be skipped",
		"|1|salt|hash ssh-ed25519 " + base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal()),
		khLine(t, "example.com", signer),
		"",
	}, "\n")
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1 (hash entry should be skipped)", m.Size())
	}
	if err := m.Authorize("example.com", signer.PublicKey()); err != nil {
		t.Errorf("Authorize(example.com) = %v, want nil", err)
	}
}

func TestLoadFromFile_MarkerSkipped(t *testing.T) {
	signer := generateTestKey(t)
	// @revoked / @cert-authority 行应被跳过
	content := strings.Join([]string{
		"@revoked ssh-ed25519 " + base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal()),
		"@cert-authority example.com ssh-ed25519 " + base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal()),
		khLine(t, "example.com", signer),
		"",
	}, "\n")
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1 (marker lines should be skipped)", m.Size())
	}
}

func TestLoadFromFile_MalformedLine_Skipped(t *testing.T) {
	signer := generateTestKey(t)
	// 混合：注释、空行、字段不足、无效 base64、无效 key、有效行
	validLine := khLine(t, "example.com", signer)
	content := strings.Join([]string{
		"# this is a comment",
		"",
		"only-one-field-without-spaces",                                                              // 1 字段（< 3）
		"host keytype-without-key",                                                                   // 2 字段（< 3）
		"example.com ssh-ed25519 !!!notbase64!!!",                                                    // base64 解码失败
		"example.com ssh-ed25519 " + base64.StdEncoding.EncodeToString([]byte("not a valid ssh key")), // ssh.ParsePublicKey 失败
		validLine, // 唯一合法
		"",
	}, "\n")
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1 (malformed lines should be skipped)", m.Size())
	}
	if err := m.Authorize("example.com", signer.PublicKey()); err != nil {
		t.Errorf("Authorize(example.com) = %v, want nil", err)
	}
}

func TestLoadFromFile_BOMStripped(t *testing.T) {
	signer := generateTestKey(t)
	// UTF-8 BOM 在文件开头（Windows 记事本有时会写）
	content := "\xEF\xBB\xBF" + khLine(t, "example.com", signer) + "\n"
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1 (BOM should be stripped)", m.Size())
	}
	if err := m.Authorize("example.com", signer.PublicKey()); err != nil {
		t.Errorf("Authorize(example.com) = %v, want nil", err)
	}
}

// =============================================================================
// HostKeyCallback 行为（含自动信任 + 端口感知）
// =============================================================================

func TestHostKeyCallback_AutoTrustOnUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kh")
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer := generateTestKey(t)
	cb := m.HostKeyCallback()

	// 第一次：未找到 → 自动 Add + 放行
	if err := cb("newhost.com", &net.TCPAddr{Port: 22}, signer.PublicKey()); err != nil {
		t.Fatalf("first connect: err = %v, want nil", err)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1", m.Size())
	}

	// 第二次：已知 + 匹配 → 放行
	if err := cb("newhost.com", &net.TCPAddr{Port: 22}, signer.PublicKey()); err != nil {
		t.Errorf("second connect: err = %v, want nil", err)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1 (no duplicate add)", m.Size())
	}
}

func TestHostKeyCallback_PortMismatch_NotFound(t *testing.T) {
	signer := generateTestKey(t)
	// 文件中 host 绑在默认端口 22
	content := khLine(t, "example.com", signer) + "\n"
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cb := m.HostKeyCallback()

	// 用非默认端口连：22 端口的条目不匹配 2222 → 视为未找到 → 自动信任并写入新条目
	if err := cb("example.com:2222", &net.TCPAddr{Port: 2222}, signer.PublicKey()); err != nil {
		t.Errorf("connect to example.com:2222: err = %v, want nil (auto-trust)", err)
	}
	// 现在应有 2 个 entry（example.com:22 + [example.com]:2222）
	if m.Size() != 2 {
		t.Errorf("Size = %d, want 2 (different ports = different entries)", m.Size())
	}
}

func TestHostKeyCallback_IPv6RoundTrip(t *testing.T) {
	signer := generateTestKey(t)
	content := khLine(t, "[::1]:2222", signer) + "\n"
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cb := m.HostKeyCallback()

	// SSH client 传 "[::1]:2222" → SplitHostPort → ("::1", "2222") → 匹配
	if err := cb("[::1]:2222", &net.TCPAddr{Port: 2222}, signer.PublicKey()); err != nil {
		t.Errorf("connect to [::1]:2222: err = %v, want nil", err)
	}
	// 不同端口视为不同 host → 未知 → 自动信任（OpenSSH 行为）
	if err := cb("[::1]:3333", &net.TCPAddr{Port: 3333}, signer.PublicKey()); err != nil {
		t.Errorf("connect to [::1]:3333: err = %v, want nil (auto-trust on different port)", err)
	}
	// 现在有 2 个 entry：原始 [::1]:2222 + 新增的 [::1]:3333
	if m.Size() != 2 {
		t.Errorf("Size = %d, want 2 (different ports = different entries)", m.Size())
	}
}

func TestHostKeyCallback_MITMWithPort(t *testing.T) {
	signer := generateTestKey(t)
	otherSigner := generateTestKey(t)
	content := khLine(t, "[example.com]:2222", signer) + "\n"
	path := writeKH(t, content)

	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cb := m.HostKeyCallback()

	// 端口对但 key 错 → MITM 拒绝
	if err := cb("example.com:2222", &net.TCPAddr{Port: 2222}, otherSigner.PublicKey()); err != ErrHostKeyMismatch {
		t.Errorf("MITM: err = %v, want ErrHostKeyMismatch", err)
	}
	// Size 不变（被拒绝的连接不会写入）
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1 (rejected MITM should not add entry)", m.Size())
	}
}

// =============================================================================
// Add 写回文件格式
// =============================================================================

func TestAdd_NormalizesPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kh")
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer := generateTestKey(t)

	if err := m.Add("example.com:2222", signer.PublicKey(), "test"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "[example.com]:2222") {
		t.Errorf("file content missing bracketed port: %q", content)
	}
	if strings.Contains(content, "example.com:2222 ssh-ed25519") {
		// 不应该有未加括号的端口 2222 形式
		t.Errorf("file should use [host]:port form for non-default port: %q", content)
	}
}

func TestAdd_NormalizesIPv6(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kh")
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer := generateTestKey(t)

	if err := m.Add("[::1]:2222", signer.PublicKey(), "test"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "[::1]:2222") {
		t.Errorf("file content should contain [::1]:2222: %q", content)
	}
}

func TestAdd_DefaultPortOmits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kh")
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer := generateTestKey(t)

	if err := m.Add("example.com", signer.PublicKey(), "test"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "example.com ssh-ed25519") {
		t.Errorf("file content missing bare host: %q", content)
	}
	if strings.Contains(content, "[example.com]:22") {
		t.Errorf("default port should not write brackets: %q", content)
	}
}

// =============================================================================
// 边界条件 & 安全检查
// =============================================================================

func TestAdd_RejectsEmptyHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kh")
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer := generateTestKey(t)

	if err := m.Add("", signer.PublicKey(), ""); err == nil {
		t.Error("Add(\"\") should return error, got nil")
	}
	if m.Size() != 0 {
		t.Errorf("Size = %d, want 0 (rejected Add should not change state)", m.Size())
	}
}

func TestAdd_RejectsMultiHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kh")
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer := generateTestKey(t)

	// Add 只接受单 host — 多 host 形式（逗号分隔）应该被拒绝
	if err := m.Add("example.com,foo.com", signer.PublicKey(), ""); err == nil {
		t.Error("Add(\"example.com,foo.com\") should return error (multi-host not allowed in Add)")
	}
	if m.Size() != 0 {
		t.Errorf("Size = %d, want 0", m.Size())
	}
}

func TestHostKeyCallback_RejectsUnparseableHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kh")
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer := generateTestKey(t)
	cb := m.HostKeyCallback()

	// 完全无法解析的 host（空字符串 + 空的 remote）必须拒绝
	// 不允许静默放行 — 那会构成对未知 host 自动信任任意 key
	if err := cb("", nil, signer.PublicKey()); err == nil {
		t.Error("cb(\"\", nil) should return error (cannot determine host)")
	}
	if m.Size() != 0 {
		t.Errorf("Size = %d, want 0 (rejected callback should not auto-add)", m.Size())
	}
}

// =============================================================================
// v0.5.0 First-Use Trust 测试
// =============================================================================

// mockEmitter 实现 knownhosts.EventEmitter 接口，捕获所有 EmitTrustRequest 调用。
//
// 行为：把 ctx + req 写入 FIFO 队列 + signal channel（buffered），
// 测试 goroutine 通过 signal channel 同步等待 emitter 被调用，然后
// 调 Manager.ReplyTrust 模拟前端用户响应。
//
// FIFO 语义：每次 waitForCall 弹出**最早一条**未消费 call，匹配
// "第 N 次 Emit 触发第 N 次 waitForCall"的自然测试节奏。
// 之前版本固定返回 calls[0]，导致多次 emit 后 waitForCall 拿到的
// 是过期的 call，破坏了 "emit a → reply a; emit b → reply b" 的语义。
type mockEmitter struct {
	mu     sync.Mutex
	calls  []capturedEmit // FIFO queue
	signal chan struct{}  // buffered; size = 同时等待的 emit 数
}

type capturedEmit struct {
	Ctx context.Context
	Req TrustRequest
}

func newMockEmitter(bufSize int) *mockEmitter {
	return &mockEmitter{
		signal: make(chan struct{}, bufSize),
	}
}

// EmitTrustRequest 捕获请求并 signal。
func (e *mockEmitter) EmitTrustRequest(ctx context.Context, req TrustRequest) {
	e.mu.Lock()
	e.calls = append(e.calls, capturedEmit{Ctx: ctx, Req: req})
	e.mu.Unlock()
	// 非阻塞 signal —— buffer 必须够大
	select {
	case e.signal <- struct{}{}:
	default:
	}
}

// waitForCall 阻塞直到至少一次 Emit 被调用（用于同步 HostKeyCallback 等待点）。
//
// FIFO 弹出：每次 waitForCall 消费**最早一条**未读 call，确保多次 Emit
// 场景下每次都能拿到与本次 Emit 配对的 TrustRequest（从而拿对 ID）。
func (e *mockEmitter) waitForCall(t *testing.T, d time.Duration) capturedEmit {
	t.Helper()
	select {
	case <-e.signal:
		e.mu.Lock()
		defer e.mu.Unlock()
		if len(e.calls) == 0 {
			t.Fatal("mockEmitter signaled but no calls recorded")
		}
		// FIFO: 弹出最早一条
		call := e.calls[0]
		e.calls = e.calls[1:]
		return call
	case <-time.After(d):
		t.Fatalf("mockEmitter: no Emit within %s", d)
		return capturedEmit{}
	}
}

// -----------------------------------------------------------------------------
// NewWithTrust 基础测试
// -----------------------------------------------------------------------------

func TestNewWithTrust_NilEmitter_FallsBackToAutoTrust(t *testing.T) {
	// nil emitter 行为：和 New() 一样，自动 Add 写入 + 放行
	// （v0.1.3 兜底路径，单元测试 / CLI 子命令场景）
	path := filepath.Join(t.TempDir(), "kh")
	m, err := NewWithTrust(path, nil)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}
	signer := generateTestKey(t)
	cb := m.HostKeyCallback()

	if err := cb("newhost.com", &net.TCPAddr{Port: 22}, signer.PublicKey()); err != nil {
		t.Fatalf("callback (nil emitter): err = %v, want nil (auto-trust)", err)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1 (auto-trusted)", m.Size())
	}
}

func TestNewWithTrust_RequiresNonEmptyPath(t *testing.T) {
	// NewWithTrust 仍要求非空 path
	if _, err := NewWithTrust("", nil); err == nil {
		t.Error("NewWithTrust(\"\", nil) should return error (empty path)")
	}
}

func TestNew_DoesNotEnableTrust(t *testing.T) {
	// New() 返回的 Manager 即使 m.emitter 看起来是 nil，也不应尝试
	// EmitTrustRequest。直接验证 HostKeyCallback 在 New() 路径上
	// 走自动 Add 兜底（不报错 + 写入 1 个 entry）。
	path := filepath.Join(t.TempDir(), "kh")
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.emitter != nil {
		t.Errorf("New() should leave emitter nil, got %v", m.emitter)
	}
	signer := generateTestKey(t)
	cb := m.HostKeyCallback()

	if err := cb("legacy.example.com", &net.TCPAddr{}, signer.PublicKey()); err != nil {
		t.Fatalf("cb: %v", err)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1", m.Size())
	}
}

// -----------------------------------------------------------------------------
// 完整 trust 流程
// -----------------------------------------------------------------------------

func TestHostKeyCallback_TrustRequest_TrustFlow(t *testing.T) {
	dir := t.TempDir()
	emitter := newMockEmitter(1)
	m, err := NewWithTrust(filepath.Join(dir, "kh"), emitter)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}
	signer := generateTestKey(t)
	cb := m.HostKeyCallback()

	// 模拟前端 modal 行为：收到 EmitTrustRequest 后调 ReplyTrust(trust)
	responderDone := make(chan struct{})
	go func() {
		defer close(responderDone)
		call := emitter.waitForCall(t, 2*time.Second)
		if call.Req.Host != "newhost.com" {
			t.Errorf("TrustRequest.Host = %q, want %q", call.Req.Host, "newhost.com")
		}
		if call.Req.KeyType != "ssh-ed25519" {
			t.Errorf("TrustRequest.KeyType = %q, want %q", call.Req.KeyType, "ssh-ed25519")
		}
		if call.Req.ID == "" {
			t.Error("TrustRequest.ID is empty")
		}
		if call.Req.Fingerprint == "" {
			t.Error("TrustRequest.Fingerprint is empty")
		}
		if call.Req.FullKey == "" {
			t.Error("TrustRequest.FullKey is empty")
		}
		if err := m.ReplyTrust(call.Req.ID, "trust"); err != nil {
			t.Errorf("ReplyTrust: %v", err)
		}
	}()

	if err := cb("newhost.com", &net.TCPAddr{Port: 22}, signer.PublicKey()); err != nil {
		t.Fatalf("callback: %v", err)
	}
	<-responderDone

	// 1) entry 已写入
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1 (user trusted)", m.Size())
	}
	// 2) 文件持久化了
	data, err := os.ReadFile(filepath.Join(dir, "kh"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "mossterm-user") {
		t.Errorf("file content missing 'mossterm-user' comment: %q", string(data))
	}
	if !strings.Contains(string(data), "newhost.com") {
		t.Errorf("file content missing 'newhost.com': %q", string(data))
	}
	// 3) v0.5.2 验证：trustWaiters map 在 callback 返回后被清空（无泄露）
	m.trustMu.Lock()
	leaked := len(m.trustWaiters)
	m.trustMu.Unlock()
	if leaked != 0 {
		t.Errorf("trustWaiters leaked %d entries after trust, want 0", leaked)
	}
	// 4) 第二次连接应当走"已知 + 匹配"路径，不再触发 trust
	emitter2 := newMockEmitter(1)
	m2, err := NewWithTrust(filepath.Join(dir, "kh"), emitter2)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	cb2 := m2.HostKeyCallback()
	if err := cb2("newhost.com", &net.TCPAddr{Port: 22}, signer.PublicKey()); err != nil {
		t.Errorf("second connect (known+matched): err = %v, want nil", err)
	}
	// 第二次不应该触发 emit
	if len(emitter2.calls) != 0 {
		t.Errorf("second connect should not emit, got %d emits", len(emitter2.calls))
	}
}

func TestHostKeyCallback_TrustRequest_RejectFlow(t *testing.T) {
	dir := t.TempDir()
	emitter := newMockEmitter(1)
	m, err := NewWithTrust(filepath.Join(dir, "kh"), emitter)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}
	signer := generateTestKey(t)
	cb := m.HostKeyCallback()

	responderDone := make(chan struct{})
	go func() {
		defer close(responderDone)
		call := emitter.waitForCall(t, 2*time.Second)
		if err := m.ReplyTrust(call.Req.ID, "reject"); err != nil {
			t.Errorf("ReplyTrust: %v", err)
		}
	}()

	err = cb("newhost.com", &net.TCPAddr{Port: 22}, signer.PublicKey())
	<-responderDone

	if err == nil {
		t.Fatal("callback should return error when user rejects")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("err = %v, want 'rejected' in message", err)
	}
	if m.Size() != 0 {
		t.Errorf("Size = %d, want 0 (rejected should not add entry)", m.Size())
	}
	// 文件不应有该 host 的记录
	data, _ := os.ReadFile(filepath.Join(dir, "kh"))
	if strings.Contains(string(data), "newhost.com") {
		t.Errorf("file should not contain rejected host: %q", string(data))
	}
}

func TestHostKeyCallback_TrustRequest_UnknownActionTreatedAsReject(t *testing.T) {
	// "trust" 以外的任何 action 都按 reject 处理（保守：宁拒勿纵）
	dir := t.TempDir()
	emitter := newMockEmitter(1)
	m, err := NewWithTrust(filepath.Join(dir, "kh"), emitter)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}
	signer := generateTestKey(t)
	cb := m.HostKeyCallback()

	responderDone := make(chan struct{})
	go func() {
		defer close(responderDone)
		call := emitter.waitForCall(t, 2*time.Second)
		_ = m.ReplyTrust(call.Req.ID, "maybe") // 非 trust / 非 reject
	}()

	err = cb("newhost.com", &net.TCPAddr{}, signer.PublicKey())
	<-responderDone

	if err == nil {
		t.Fatal("callback should return error for non-trust action")
	}
	if m.Size() != 0 {
		t.Errorf("Size = %d, want 0", m.Size())
	}
}

func TestHostKeyCallback_TrustRequest_Timeout(t *testing.T) {
	// 用户不响应 → HostKeyCallback 在 TrustRequestTimeout 后返回 error
	//
	// 用一个非常短的临时超时（避免真的等 60s）；结束后恢复。
	dir := t.TempDir()
	emitter := newMockEmitter(1)
	m, err := NewWithTrust(filepath.Join(dir, "kh"), emitter)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}
	signer := generateTestKey(t)
	cb := m.HostKeyCallback()

	// 临时缩短 timeout；测试结束恢复
	origTimeout := TrustRequestTimeout
	TrustRequestTimeout = 200 * time.Millisecond
	t.Cleanup(func() { TrustRequestTimeout = origTimeout })

	// 启动 goroutine 监 emit（不做 reply，让 HostKeyCallback 自然 timeout）
	emitConfirmed := make(chan struct{})
	go func() {
		defer close(emitConfirmed)
		_ = emitter.waitForCall(t, 2*time.Second) // 确认请求已发出
	}()

	start := time.Now()
	err = cb("newhost.com", &net.TCPAddr{}, signer.PublicKey())
	elapsed := time.Since(start)
	<-emitConfirmed

	if err == nil {
		t.Fatal("callback should return timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("err = %v, want 'timeout' in message", err)
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("elapsed = %s, want >= 200ms (timeout should be honored)", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("elapsed = %s, want < 2s (timeout was overridden)", elapsed)
	}
	if m.Size() != 0 {
		t.Errorf("Size = %d, want 0 (timed-out should not add entry)", m.Size())
	}
	// v0.5.2 验证：trustWaiters map 在 timeout 路径也被清空（defer 删除生效）
	m.trustMu.Lock()
	leaked := len(m.trustWaiters)
	m.trustMu.Unlock()
	if leaked != 0 {
		t.Errorf("trustWaiters leaked %d entries after timeout, want 0", leaked)
	}
}

func TestHostKeyCallback_TrustRequest_IDMismatch(t *testing.T) {
	// v0.5.2 删除：旧的"单 channel + ID mismatch"是 v0.5.0 的设计缺陷
	// —— replyCh 容量 1 时多个并发连接共享一条 channel，第一个 callback
	// 拿到"别人的 reply"时 ID 必然不匹配。
	//
	// v0.5.2 per-request channel 模式下 replyCh 一一对应，select 拿到的
	// reply.ID 必然等于 req.ID，ID mismatch 不可能发生。相关测试场景
	// 改为 "ReplyTrust 用错 ID → 立即 error"（见 TestReplyTrust_UnknownID_ReturnsError）。
	t.Skip("v0.5.2 removed: ID mismatch impossible under per-request channel design; see TestReplyTrust_UnknownID_ReturnsError")
}

func TestHostKeyCallback_ConcurrentTrustRequests(t *testing.T) {
	// v0.5.2 并发 trust 场景的核心测试：
	// 5 个 goroutine 同时触发 5 个不同 host 的 HostKeyCallback，验证：
	//   1) 5 个都收到 emit
	//   2) 5 个 ID 唯一
	//   3) 用各自 ID 调 ReplyTrust，对应 goroutine 都能拿到正确 reply
	//   4) trust 全部成功（5 个 entry 全部写入文件）
	//
	// 旧版 trustReplyCh 容量 1 时这个测试会失败：第二个 callback 起 timeout。
	const N = 5
	dir := t.TempDir()
	emitter := newMockEmitter(N)
	m, err := NewWithTrust(filepath.Join(dir, "kh"), emitter)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}

	hosts := make([]string, N)
	signers := make([]ssh.Signer, N)
	for i := 0; i < N; i++ {
		hosts[i] = fmt.Sprintf("host%d.example.com", i)
		signers[i] = generateTestKey(t)
	}

	// 收集 callback 错误
	type cbResult struct {
		host string
		err  error
	}
	results := make(chan cbResult, N)

	// 启动 N 个 callback goroutine
	for i := 0; i < N; i++ {
		go func(i int) {
			cb := m.HostKeyCallback()
			err := cb(hosts[i], &net.TCPAddr{Port: 22}, signers[i].PublicKey())
			results <- cbResult{host: hosts[i], err: err}
		}(i)
	}

	// 启动 N 个 responder goroutine：每个等一个 emit，拿到 ID 后用 trust 回复
	var responderWG sync.WaitGroup
	for i := 0; i < N; i++ {
		responderWG.Add(1)
		go func() {
			defer responderWG.Done()
			call := emitter.waitForCall(t, 5*time.Second)
			// 用拿到的 ID 调 ReplyTrust（必须是正确的 ID）
			if err := m.ReplyTrust(call.Req.ID, "trust"); err != nil {
				t.Errorf("ReplyTrust(%q): %v", call.Req.ID, err)
			}
		}()
	}
	responderWG.Wait()

	// 收集所有 callback 结果
	for i := 0; i < N; i++ {
		r := <-results
		if r.err != nil {
			t.Errorf("host %q: callback err = %v, want nil", r.host, r.err)
		}
	}

	// 1) 5 个 emit 全部被消费（mockEmitter.waitForCall 是 FIFO 弹出）
	if got := len(emitter.calls); got != 0 {
		t.Errorf("emitter.calls remaining = %d, want 0 (all consumed by responders)", got)
	}

	// 2) 验证 ID 唯一 + 全部成功
	//    通过文件内容间接验证 5 个 host 都写入
	data, err := os.ReadFile(filepath.Join(dir, "kh"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	for i, h := range hosts {
		if !strings.Contains(content, h) {
			t.Errorf("file missing host[%d] = %q: %q", i, h, content)
		}
	}
	// 3) Size 应该 = N（5 个 entry 全部 Add 成功）
	if m.Size() != N {
		t.Errorf("Size = %d, want %d (all N trusts should add entry)", m.Size(), N)
	}
	// 4) trustWaiters map 在所有 callback 退出后应为空（无泄露）
	m.trustMu.Lock()
	leaked := len(m.trustWaiters)
	m.trustMu.Unlock()
	if leaked != 0 {
		t.Errorf("trustWaiters leaked %d entries after all callbacks, want 0", leaked)
	}
	// 5) 文件应有 N 行
	lineCount := strings.Count(strings.TrimSpace(content), "\n") + 1
	if lineCount != N {
		t.Errorf("file line count = %d, want %d", lineCount, N)
	}
}

func TestHostKeyCallback_ConcurrentTrustRequests_MixedActions(t *testing.T) {
	// v0.5.2 进阶：5 个并发 trust 决策混合（trust / reject），
	// 验证互不干扰：trust 的进 known_hosts，reject 的不进。
	//
	// 这测试并发场景下 per-request channel 的隔离性：reject 不会"污染"trust
	// 的 reply。
	//
	// 关键点：responder goroutine 的 i 不一定对应 callback goroutine 的 i
	// （emit 顺序不保证），所以 responder 必须根据**捕获的 host** 决定 action，
	// 而不是根据 goroutine i。这样无论哪个 callback 先 emit，决策都正确。
	const N = 5
	dir := t.TempDir()
	emitter := newMockEmitter(N)
	m, err := NewWithTrust(filepath.Join(dir, "kh"), emitter)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}

	hosts := make([]string, N)
	signers := make([]ssh.Signer, N)
	for i := 0; i < N; i++ {
		hosts[i] = fmt.Sprintf("mixed%d.example.com", i)
		signers[i] = generateTestKey(t)
	}

	// 决策：偶数 index → trust，奇数 index → reject
	wantTrusted := map[int]bool{0: true, 1: false, 2: true, 3: false, 4: true}

	type cbResult struct {
		idx int
		host string
		err  error
	}
	results := make(chan cbResult, N)

	// 启动 N 个 callback goroutine
	for i := 0; i < N; i++ {
		go func(i int) {
			cb := m.HostKeyCallback()
			err := cb(hosts[i], &net.TCPAddr{Port: 22}, signers[i].PublicKey())
			results <- cbResult{idx: i, host: hosts[i], err: err}
		}(i)
	}

	// 启动 N 个 responder：每个根据**捕获的 host name** 决定 action
	var responderWG sync.WaitGroup
	responderWG.Add(N)
	for k := 0; k < N; k++ {
		go func() {
			defer responderWG.Done()
			call := emitter.waitForCall(t, 5*time.Second)
			// 从 host 名解析 index（host 形如 "mixed3.example.com"）
			var idx int
			if _, scanErr := fmt.Sscanf(call.Req.Host, "mixed%d.example.com", &idx); scanErr != nil {
				t.Errorf("responder: cannot parse host %q: %v", call.Req.Host, scanErr)
				return
			}
			action := "reject"
			if wantTrusted[idx] {
				action = "trust"
			}
			if err := m.ReplyTrust(call.Req.ID, action); err != nil {
				t.Errorf("ReplyTrust[%d](%q, action=%s): %v", idx, call.Req.ID, action, err)
			}
		}()
	}
	responderWG.Wait()

	// 收集 callback 结果
	for i := 0; i < N; i++ {
		r := <-results
		if wantTrusted[r.idx] {
			// trust 路径：err 应为 nil
			if r.err != nil {
				t.Errorf("host %q (index %d, expect trust): err = %v, want nil", r.host, r.idx, r.err)
			}
		} else {
			// reject 路径：err 应包含 "rejected"
			if r.err == nil {
				t.Errorf("host %q (index %d, expect reject): err = nil, want rejected", r.host, r.idx)
			} else if !strings.Contains(r.err.Error(), "rejected") {
				t.Errorf("host %q (index %d, expect reject): err = %v, want 'rejected' in message", r.host, r.idx, r.err)
			}
		}
	}

	// 验证文件里只有 trust 的 host
	data, err := os.ReadFile(filepath.Join(dir, "kh"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	for i, h := range hosts {
		shouldHave := wantTrusted[i]
		has := strings.Contains(content, h)
		if has != shouldHave {
			t.Errorf("host %q (index %d, wantTrusted=%v): file contains = %v, want %v; file: %q",
				h, i, shouldHave, has, shouldHave, content)
		}
	}
	// Size 应 = trust 数 = 3
	if m.Size() != 3 {
		t.Errorf("Size = %d, want 3 (3 trust out of 5)", m.Size())
	}
}

// -----------------------------------------------------------------------------
// ReplyTrust 边界
// -----------------------------------------------------------------------------

func TestReplyTrust_NoPendingRequest_ReturnsError(t *testing.T) {
	// v0.5.2 起：没有挂起的 HostKeyCallback 时调 ReplyTrust → 立即返回 error
	// （旧版 replyCh 容量 1 让 send 默默成功；新版用 map 查找，未命中立即 error）
	//
	// 这个测试原本在 v0.5.0 名字叫 _ReturnsError 但实际断言不返回 error
	// （旧版 send 默默成功）。v0.5.2 起名字终于和语义对齐。
	dir := t.TempDir()
	emitter := newMockEmitter(1)
	m, err := NewWithTrust(filepath.Join(dir, "kh"), emitter)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}

	start := time.Now()
	err = m.ReplyTrust("any-id", "trust")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("ReplyTrust (no pending) should return error, got nil")
	}
	if !strings.Contains(err.Error(), "no pending request") {
		t.Errorf("err = %v, want 'no pending request' in message", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed = %s, want < 100ms (should not block 5s)", elapsed)
	}
	// trustWaiters map 仍应为空（这个 ID 根本不在 map 里）
	m.trustMu.Lock()
	waiters := len(m.trustWaiters)
	m.trustMu.Unlock()
	if waiters != 0 {
		t.Errorf("trustWaiters size = %d, want 0", waiters)
	}
}

func TestReplyTrust_UnknownID_ReturnsError(t *testing.T) {
	// ReplyTrust 用不存在的 ID → 立即返回 error（不阻塞 5s）
	//
	// 这是 v0.5.2 行为变化的关键测试：旧版 replyCh 容量 1 让 send 默默成功
	// （无 waiter 也写入 buffered channel），新版用 map 查找严格按 ID 匹配，
	// 未命中立即 error。前端拿到 error 后可以选择重发或放弃。
	dir := t.TempDir()
	emitter := newMockEmitter(1)
	m, err := NewWithTrust(filepath.Join(dir, "kh"), emitter)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}

	// 没有 HostKeyCallback 在跑，直接用未知 ID
	start := time.Now()
	err = m.ReplyTrust("never-existed-xyz", "trust")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("ReplyTrust (unknown ID) should return error, got nil")
	}
	if !strings.Contains(err.Error(), "no pending request") {
		t.Errorf("err = %v, want 'no pending request' in message", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed = %s, want < 100ms (should not block 5s)", elapsed)
	}

	// 第二个变体：模拟 "有 waiter 挂着，但用错的 ID 调 ReplyTrust"
	//   启一个 HostKeyCallback 在 select 等，缩短 timeout 让它 timeout 后退出
	//   期间用错 ID 调 ReplyTrust → 立即 error
	signer := generateTestKey(t)
	cb := m.HostKeyCallback()

	origTimeout := TrustRequestTimeout
	TrustRequestTimeout = 500 * time.Millisecond
	t.Cleanup(func() { TrustRequestTimeout = origTimeout })

	// 启动 callback 在后台跑（会 emit，然后等 500ms timeout）
	callbackDone := make(chan struct{})
	go func() {
		defer close(callbackDone)
		_ = cb("blocked.example.com", &net.TCPAddr{}, signer.PublicKey())
	}()

	// 等 emit 被消费（mockEmitter 收到 emit 后 unblock 没人消费，但 waitForCall 不在主路径上）
	// 简单起见，等 50ms 给 callback 时间注册到 map
	time.Sleep(50 * time.Millisecond)

	start = time.Now()
	err = m.ReplyTrust("wrong-id-12345", "trust")
	elapsed = time.Since(start)

	if err == nil {
		t.Fatal("ReplyTrust (wrong ID while waiter pending) should return error, got nil")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed = %s, want < 100ms (should not block 5s)", elapsed)
	}

	// 等 callback 自然 timeout 退出，defer 清 map
	<-callbackDone
	m.trustMu.Lock()
	waiters := len(m.trustWaiters)
	m.trustMu.Unlock()
	if waiters != 0 {
		t.Errorf("trustWaiters size = %d after callback timeout, want 0", waiters)
	}
}

func TestReplyTrust_AfterRejection_StateRecovers(t *testing.T) {
	// 验证：第一次连接 reject 后，trustReplyCh 已被消费；
	// 第二次连接的 HostKeyCallback 走正常路径（不残留 stale reply）。
	dir := t.TempDir()
	emitter := newMockEmitter(2)
	m, err := NewWithTrust(filepath.Join(dir, "kh"), emitter)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}
	signer := generateTestKey(t)
	cb := m.HostKeyCallback()

	// 第一次：拒绝
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		call := emitter.waitForCall(t, 2*time.Second)
		_ = m.ReplyTrust(call.Req.ID, "reject")
	}()
	_ = cb("a.example.com", &net.TCPAddr{}, signer.PublicKey())
	<-firstDone

	// 第二次：信任（应正常走通，channel 不应残留旧 reply）
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		call := emitter.waitForCall(t, 2*time.Second)
		_ = m.ReplyTrust(call.Req.ID, "trust")
	}()
	if err := cb("b.example.com", &net.TCPAddr{}, signer.PublicKey()); err != nil {
		t.Errorf("second connect after reject: err = %v, want nil", err)
	}
	<-secondDone

	// 第二次信任后应写入 entry
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1 (only b should be added; a was rejected)", m.Size())
	}
}

// -----------------------------------------------------------------------------
// 辅助函数测试
// -----------------------------------------------------------------------------

func TestGenerateTrustID_UniqueAndNonEmpty(t *testing.T) {
	a := generateTrustID()
	b := generateTrustID()
	if a == "" || b == "" {
		t.Fatalf("generateTrustID returned empty: a=%q, b=%q", a, b)
	}
	if a == b {
		t.Errorf("generateTrustID returned duplicate: %q", a)
	}
	// base64.RawURLEncoding 22 字符
	if len(a) != 22 {
		t.Errorf("ID length = %d, want 22 (base64.RawURLEncoding(16 bytes))", len(a))
	}
}

func TestShortFingerprint(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"short", "short"},
		{"", ""},
		{"0123456789abcdef", "0123456789abcdef"}, // 16 字符不加 "..."
		{"0123456789abcdefg", "0123456789abcdef..."}, // 17 字符 → 截 16 + "..."
		{"0123456789abcdefghijklmnop", "0123456789abcdef..."},
	}
	for _, c := range cases {
		got := shortFingerprint(c.in)
		if got != c.want {
			t.Errorf("shortFingerprint(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// -----------------------------------------------------------------------------
// 已知 host 不走 trust 路径
// -----------------------------------------------------------------------------

func TestHostKeyCallback_KnownHost_DoesNotEmit(t *testing.T) {
	// 已知 + 匹配 → 不应 emit TrustRequest
	signer := generateTestKey(t)
	content := khLine(t, "known.com", signer) + "\n"
	path := writeKH(t, content)

	emitter := newMockEmitter(1)
	m, err := NewWithTrust(path, emitter)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}
	cb := m.HostKeyCallback()

	if err := cb("known.com", &net.TCPAddr{}, signer.PublicKey()); err != nil {
		t.Fatalf("cb: %v", err)
	}
	if len(emitter.calls) != 0 {
		t.Errorf("known+matched should not emit, got %d emits", len(emitter.calls))
	}
}

func TestHostKeyCallback_MITM_DoesNotEmit(t *testing.T) {
	// 已知 + 不匹配（MITM）→ 拒绝，不应 emit
	signer := generateTestKey(t)
	otherSigner := generateTestKey(t)
	content := khLine(t, "known.com", signer) + "\n"
	path := writeKH(t, content)

	emitter := newMockEmitter(1)
	m, err := NewWithTrust(path, emitter)
	if err != nil {
		t.Fatalf("NewWithTrust: %v", err)
	}
	cb := m.HostKeyCallback()

	err = cb("known.com", &net.TCPAddr{}, otherSigner.PublicKey())
	if err != ErrHostKeyMismatch {
		t.Errorf("MITM: err = %v, want ErrHostKeyMismatch", err)
	}
	if len(emitter.calls) != 0 {
		t.Errorf("MITM should not emit, got %d emits", len(emitter.calls))
	}
}
