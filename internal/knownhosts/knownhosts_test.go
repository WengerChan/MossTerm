package knownhosts

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
