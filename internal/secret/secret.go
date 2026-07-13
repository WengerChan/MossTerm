// Package secret 提供凭据与私钥的安全存储。
//
// 多级后端（优先级从高到低）：
//  1. 系统 keyring（macOS Keychain / Windows Credential Manager / Linux Secret Service）
//  2. 加密文件 fallback（AES-256-GCM + Argon2id 派生 key）
//  3. 内存（仅当前 session 生命周期）
//
// 安全约束：
//   - 不打印 Get 结果。
//   - 不在任何日志里序列化。
//   - 调用方用完调用 subtle.ConstantTimeCompare 比较后清零。
//   - 退出时 Close() 清零缓存。
//
// 审计红线：internal/secret 整体 < 500 行（CI 检查）。
package secret

import (
	"errors"
	"time"
)

// Kind 标识凭据类型。
type Kind string

const (
	// KindPassword 是远端 SSH 登录密码。
	KindPassword Kind = "password"
	// KindPrivateKey 是 OpenSSH 格式的私钥（PEM-encoded，可选 passphrase 加密）。
	KindPrivateKey Kind = "private_key"
	// KindAPIKey 是 AI Provider 的 API key（v0.2+）。
	KindAPIKey Kind = "api_key"
	// KindPassphrase 是加密私钥的解锁口令。
	KindPassphrase Kind = "passphrase"
)

// ID 是凭据条目的唯一标识。
type ID string

// Item 是 Secret 的元数据列表项。
//
// 不包含 secret 内容本身（content 必须通过 Get 显式拉取）。
type Item struct {
	ID          ID     `json:"id"`
	Name        string `json:"name"`
	Kind        Kind   `json:"kind"`
	Fingerprint string `json:"fingerprint,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	LastUsed    int64  `json:"lastUsed"`
}

// Store 是凭据存储的抽象接口。
type Store interface {
	// Set 把 secret 写入存储，返回条目 ID。
	// name 是用户可读名；meta 存储指纹等元数据（不存内容）。
	Set(name string, kind Kind, secret []byte, meta map[string]string) (ID, error)
	// Get 取出 secret 内容。调用方负责 zeroize。
	// 当存储加密且未解锁时返回 ErrLocked。
	Get(id ID) ([]byte, error)
	// Delete 删除条目。
	Delete(id ID) error
	// List 返回全部条目（仅元数据）。
	List() ([]Item, error)
	// HasPassphrase 报告是否设置了主密码（用于 UI 状态显示）。
	HasPassphrase() bool
	// SetPassphrase 设置 / 修改主密码。
	SetPassphrase(pass string) error
	// Close 清零缓存并释放文件句柄。
	Close() error
}

// Config 是 New 的入参。
type Config struct {
	// UseSystemKeyring 为 true 时优先使用系统 keyring；失败则降级。
	UseSystemKeyring bool
	// FallbackPath 是加密文件 fallback 的存储路径。
	// 为空时使用 ~/.config/mossterm/secrets.enc。
	FallbackPath string
	// Argon2Params 是主密码派生参数；零值使用推荐默认。
	Argon2Params Params
}

// New 构造一个 Store。
//
// 根据 Config.UseSystemKeyring 选择系统 keyring 后端或纯加密文件后端。
// 实际实现位于 keyring.go。
func New(cfg Config) (Store, error) {
	if cfg.UseSystemKeyring {
		// 优先 keyring；失败时降级。
		s, err := newKeyringStore(cfg)
		if err == nil {
			return s, nil
		}
	}
	return newEncryptedFileStore(cfg)
}

// ErrLocked 在主密码未解锁时返回。
var ErrLocked = errors.New("secret: store is locked (passphrase required)")

// ErrNotFound 在凭据 ID 不存在时返回。
var ErrNotFound = errors.New("secret: item not found")

// Params 描述 Argon2id 密钥派生参数。
//
// 字段类型与 golang.org/x/crypto/argon2.IDKey 的入参保持一致。
// 我们自己定义而不是从 argon2 包引入，因为该包不导出 Params 类型。
type Params struct {
	Time    uint32 // 迭代次数
	Memory  uint32 // 内存开销（KB）
	Threads uint8  // 并行度
	KeyLen  uint32 // 派生 key 长度（字节）
	SaltLen uint32 // 盐长度（字节）
}

// DefaultArgon2Params 是 OWASP 2024 推荐参数。
var DefaultArgon2Params = Params{
	Time:    2,
	Memory:  64 * 1024, // 64 MB
	Threads: 4,
	KeyLen:  32,
	SaltLen: 16,
}

// 占位以让 time 包被引用。
var _ = time.Now
