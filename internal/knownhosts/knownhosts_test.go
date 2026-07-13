package knownhosts

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
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
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kh")
	// 写一个简单的测试 known_hosts 文件
	content := "# comment\n\nexample.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... user@host\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1", m.Size())
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
}
