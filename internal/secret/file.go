// Package secret 的 file.go：基于 AES-256-GCM + Argon2id 的加密文件 Store。
//
// 文件位置：Config.FallbackPath（默认 ~/.config/mossterm/secrets.enc）
// 文件结构（JSON）：
//
//	{
//	  "kdf": { "salt": "<base64>", "params": { "time": 2, "memory": 65536, "threads": 4, "keyLen": 32, "saltLen": 16 } },
//	  "items": {
//	    "<id>": {
//	      "name": "...", "kind": "...", "fingerprint": "...",
//	      "createdAt": 12345, "lastUsed": 12345,
//	      "ciphertext": "<base64>", "nonce": "<base64>"
//	    },
//	    ...
//	  }
//	}
//
// 安全约束：
//   - 整个文件落在用户目录，权限 0600
//   - 加密算法 AES-256-GCM（authenticated encryption）
//   - 密钥派生 Argon2id，参数见 DefaultArgon2Params
//   - 每个 item 一个独立 nonce（96-bit random）
//
// v0.1 不支持的特性：
//   - 文件锁（仅单进程安全；多实例并发 Set 可能丢更新）
//   - 篡改检测后的事务回滚
//   - 敏感字段二次哈希
//
// TODO(security): v1.0 安全审计
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

// fileStoreFormatVersion 是 on-disk 格式的版本号（写文件时嵌入）。
const fileStoreFormatVersion = 1

// encryptedFileStore 是 Store 的加密文件后端实现。
type encryptedFileStore struct {
	cfg Config

	mu   sync.Mutex
	path string

	// 当前文件内容（解密后）
	salt []byte
	key  []byte

	items map[ID]fileStoreItem
	meta  fileStoreMeta

	// hasPassphrase 记录 SetPassphrase 是否被调用过（v0.1 简化标记）。
	//
	// 真正安全的方法是把 passphrase 持久化在 keyring 或在文件头部加 magic；
	// 当前 v0.1 不做，TODO 留给 v0.2+。
	hasPassphrase bool
}

// fileStoreMeta 是文件级元数据。
type fileStoreMeta struct {
	KDFParams Params `json:"params"`
	Salt      string `json:"salt"` // base64
	// Protected 标记文件是否已设置主密码。
	//
	// true 时启动后必须先调 SetPassphrase 才能 Set/Get。
	// 持久化在文件里，重启后仍能识别"已上锁"状态。
	Protected bool `json:"protected"`
}

// fileStoreItem 是文件中单个条目的（解密后）形态。
//
// 写入文件时 Ciphertext / Nonce 被序列化；读取后 Ciphertext 仍是密文，
// 每次 Get 时当场解密。
type fileStoreItem struct {
	Name        string `json:"name"`
	Kind        Kind   `json:"kind"`
	Fingerprint string `json:"fingerprint,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	LastUsed    int64  `json:"lastUsed"`

	Ciphertext string `json:"ciphertext"` // base64
	Nonce      string `json:"nonce"`      // base64
}

// fileStoreFile 是磁盘上文件的最外层结构。
type fileStoreFile struct {
	Version int                      `json:"version"`
	KDF     fileStoreMeta            `json:"kdf"`
	Items   map[string]fileStoreItem `json:"items"`
}

// newEncryptedFileStore 构造一个 encryptedFileStore。
//
// 行为：
//   - 解析 path（为空则用默认 ~/.config/mossterm/secrets.enc）
//   - 父目录不存在则创建
//   - 文件不存在 → 写一个空文件（无 item，等第一次 Set 时填）
//   - 文件存在 → 读 + 解码 KDF + 派生 key
//
// 派生 key 需要主密码；v0.1 行为：
//   - 启动时如果文件已存在，passphrase 必传（New 时 check HasPassphrase）
//   - 启动时如果文件不存在，passphrase 可选 —— 留空则使用空 passphrase 派生
func newEncryptedFileStore(cfg Config) (Store, error) {
	path := cfg.FallbackPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			path = filepath.Join(".config", "mossterm", "secrets.enc")
		} else {
			path = filepath.Join(home, ".config", "mossterm", "secrets.enc")
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("secret.newEncryptedFileStore: resolve path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("secret.newEncryptedFileStore: mkdir: %w", err)
	}

	s := &encryptedFileStore{
		cfg:   cfg,
		path:  abs,
		items: make(map[ID]fileStoreItem),
	}

	// 加载或初始化
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		// 新建：随机 salt + 空 item
		salt, err := randomBytes(16)
		if err != nil {
			return nil, fmt.Errorf("secret.newEncryptedFileStore: random salt: %w", err)
		}
		params := argon2ParamsOrDefault(cfg.Argon2Params)
		s.salt = salt
		s.meta = fileStoreMeta{KDFParams: params, Salt: base64.StdEncoding.EncodeToString(salt)}
		s.key = deriveKey("", salt, params) // 空 passphrase；SetPassphrase 之前不可用
		s.hasPassphrase = false
		if err := s.flushLocked(); err != nil {
			return nil, fmt.Errorf("secret.newEncryptedFileStore: write init: %w", err)
		}
		return s, nil
	} else if err != nil {
		return nil, fmt.Errorf("secret.newEncryptedFileStore: stat: %w", err)
	}

	// 存在 → 读元数据（不解密 content，只保留 key 派生所需的 salt/params）
	if err := s.loadMetaLocked(); err != nil {
		return nil, fmt.Errorf("secret.newEncryptedFileStore: load: %w", err)
	}
	// 若文件标记为 Protected，保持 s.key = nil：
	//   Set/Get 会返回 ErrLocked，调用方必须先调 SetPassphrase(pass) 解锁。
	// 若未标记 Protected（v0.1 默认情况），用空 passphrase 派生 key。
	if s.meta.Protected {
		s.hasPassphrase = true
		s.key = nil
	} else {
		s.key = deriveKey("", s.salt, s.meta.KDFParams)
		s.hasPassphrase = false
	}
	return s, nil
}

// Set 实现 Store.Set。
func (s *encryptedFileStore) Set(name string, kind Kind, secretBytes []byte, meta map[string]string) (ID, error) {
	if name == "" {
		return "", errors.New("secret.encryptedFileStore.Set: empty name")
	}
	if kind == "" {
		return "", errors.New("secret.encryptedFileStore.Set: empty kind")
	}
	if len(secretBytes) == 0 {
		return "", errors.New("secret.encryptedFileStore.Set: empty content")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.key == nil {
		return "", ErrLocked
	}

	// 1. 加密 content
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", fmt.Errorf("secret.encryptedFileStore.Set: aes new: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("secret.encryptedFileStore.Set: gcm new: %w", err)
	}
	nonce, err := randomBytes(gcm.NonceSize())
	if err != nil {
		return "", fmt.Errorf("secret.encryptedFileStore.Set: random nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, secretBytes, nil)

	// 2. 构造条目
	id := ID(uuid.NewString())
	fingerprint := ""
	if meta != nil {
		fingerprint = meta["fingerprint"]
	}
	now := time.Now().UnixMilli()
	s.items[id] = fileStoreItem{
		Name:        name,
		Kind:        kind,
		Fingerprint: fingerprint,
		CreatedAt:   now,
		LastUsed:    now,
		Ciphertext:  base64.StdEncoding.EncodeToString(ciphertext),
		Nonce:       base64.StdEncoding.EncodeToString(nonce),
	}

	// 3. 落盘
	if err := s.flushLocked(); err != nil {
		delete(s.items, id)
		return "", fmt.Errorf("secret.encryptedFileStore.Set: flush: %w", err)
	}

	return id, nil
}

// Get 实现 Store.Get。
func (s *encryptedFileStore) Get(id ID) ([]byte, error) {
	if id == "" {
		return nil, errors.New("secret.encryptedFileStore.Get: empty id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.key == nil {
		return nil, ErrLocked
	}
	item, ok := s.items[id]
	if !ok {
		return nil, ErrNotFound
	}

	ciphertext, err := base64.StdEncoding.DecodeString(item.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("secret.encryptedFileStore.Get: b64 ciphertext: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(item.Nonce)
	if err != nil {
		return nil, fmt.Errorf("secret.encryptedFileStore.Get: b64 nonce: %w", err)
	}

	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, fmt.Errorf("secret.encryptedFileStore.Get: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret.encryptedFileStore.Get: gcm: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// 解密失败 → 主密码不对，或文件被篡改
		return nil, fmt.Errorf("secret.encryptedFileStore.Get: decrypt: %w", err)
	}

	// 异步更新 LastUsed（不阻塞返回）
	item.LastUsed = time.Now().UnixMilli()
	s.items[id] = item
	go func() {
		s.mu.Lock()
		_ = s.flushLocked()
		s.mu.Unlock()
	}()

	return plain, nil
}

// Delete 实现 Store.Delete。
func (s *encryptedFileStore) Delete(id ID) error {
	if id == "" {
		return errors.New("secret.encryptedFileStore.Delete: empty id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.items[id]; !ok {
		return nil // 幂等
	}
	delete(s.items, id)
	if err := s.flushLocked(); err != nil {
		return fmt.Errorf("secret.encryptedFileStore.Delete: flush: %w", err)
	}
	return nil
}

// List 实现 Store.List。
func (s *encryptedFileStore) List() ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Item, 0, len(s.items))
	for id, it := range s.items {
		out = append(out, Item{
			ID:          id,
			Name:        it.Name,
			Kind:        it.Kind,
			Fingerprint: it.Fingerprint,
			CreatedAt:   it.CreatedAt,
			LastUsed:    it.LastUsed,
		})
	}
	return out, nil
}

// HasPassphrase 报告是否设置了主密码。
//
// v0.1 实现：所有 file 后端的 secret 都用同一 key 加密。
// 一旦 SetPassphrase 被调用过，认为有主密码。
// 简化判断："key 派生时用的 passphrase 是否为空" → 不易追踪。
// 这里用一个未导出的标志位记录。
func (s *encryptedFileStore) HasPassphrase() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasPassphrase
}

// SetPassphrase 设置 / 修改主密码，并起 unlock 作用。
//
// 设置或重置：重新生成 salt + 派生 key + flush 文件。
// 启动时 unlock：仅重新派生 key（salt 来自文件），不重写文件。
//
// 调用时机：
//   - 首次启动后：若 HasPassphrase() == true，必须调一次以 unlock。
//   - 任意时刻：可调用于修改主密码。
func (s *encryptedFileStore) SetPassphrase(pass string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.meta.Protected && s.salt != nil {
		// 启动后 unlock：salt 已在 loadMetaLocked 读出，只重新派生 key。
		s.key = deriveKey(pass, s.salt, s.meta.KDFParams)
		// 不 flush（salt 没变）；但要保留 Protected 标记以便后续 reload
		return nil
	}

	// 首次设置 / 修改主密码：重新生成 salt + 派生 key + flush 文件
	salt, err := randomBytes(16)
	if err != nil {
		return fmt.Errorf("secret.encryptedFileStore.SetPassphrase: random salt: %w", err)
	}
	s.salt = salt
	s.meta = fileStoreMeta{
		KDFParams: argon2ParamsOrDefault(s.cfg.Argon2Params),
		Salt:      base64.StdEncoding.EncodeToString(salt),
		Protected: true,
	}
	s.key = deriveKey(pass, salt, s.meta.KDFParams)
	s.hasPassphrase = true

	if err := s.flushLocked(); err != nil {
		return fmt.Errorf("secret.encryptedFileStore.SetPassphrase: flush: %w", err)
	}
	return nil
}

// Close 实现 Store.Close。
func (s *encryptedFileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		zeroBytes(s.key)
		s.key = nil
	}
	return nil
}

// -----------------------------------------------------------------------------
// 内部
// -----------------------------------------------------------------------------
// 内部
// -----------------------------------------------------------------------------

// flushLocked 把内存中的 items 写回磁盘；调用方必须持锁。
func (s *encryptedFileStore) flushLocked() error {
	f := fileStoreFile{
		Version: fileStoreFormatVersion,
		KDF:     s.meta,
		Items:   make(map[string]fileStoreItem, len(s.items)),
	}
	for id, it := range s.items {
		f.Items[string(id)] = it
	}
	data, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// loadMetaLocked 从文件读 KDF 元数据；不加载 item content。
//
// v0.1 简化：直接 unmarshal 整个文件到内存，items 内存常驻。
// 解密延后到 Get 时进行。
func (s *encryptedFileStore) loadMetaLocked() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	var f fileStoreFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	if f.Version != fileStoreFormatVersion {
		return fmt.Errorf("unsupported file format version %d (want %d)", f.Version, fileStoreFormatVersion)
	}
	salt, err := base64.StdEncoding.DecodeString(f.KDF.Salt)
	if err != nil {
		return fmt.Errorf("b64 salt: %w", err)
	}
	s.salt = salt
	s.meta = f.KDF
	s.items = make(map[ID]fileStoreItem, len(f.Items))
	for k, v := range f.Items {
		s.items[ID(k)] = v
	}
	return nil
}

// argon2ParamsOrDefault 返回 cfg 中的参数或 DefaultArgon2Params。
func argon2ParamsOrDefault(p Params) Params {
	if p.Time == 0 && p.Memory == 0 && p.Threads == 0 {
		return DefaultArgon2Params
	}
	if p.KeyLen == 0 {
		p.KeyLen = 32
	}
	if p.SaltLen == 0 {
		p.SaltLen = 16
	}
	return p
}

// deriveKey 用 Argon2id 派生 32 字节 key。
func deriveKey(passphrase string, salt []byte, p Params) []byte {
	return argon2.IDKey([]byte(passphrase), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
}

// randomBytes 从 crypto/rand 读 n 字节。
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

// 编译期断言：*encryptedFileStore 满足 Store 接口。
var _ Store = (*encryptedFileStore)(nil)
