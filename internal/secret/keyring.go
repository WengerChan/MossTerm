// Package secret 的 keyring.go：基于系统 keyring 的 Store 实现。
//
// 持久化策略：
//   - 每个 secret 在系统 keyring 中存为一条 (service="mossterm", user=id, password=base64(content))
//   - 另一条索引 (service="mossterm", user="__index__") 持有全部 Item 元数据
//     （name/kind/fingerprint/createdAt/lastUsed），通过 JSON 编码
//   - unlockedCache 是进程内的内容缓存，命中后避免每次走系统 keyring IPC
//
// 安全约束（沿用 secret.go 包级注释）：
//   - 内存中的 []byte 在 Close() 时清零
//   - 日志 / 错误信息绝不输出 Get 返回值
//   - 调用方用完 Get 返回值后自行清零
//
// v0.1 不支持的特性：文件锁、跨进程并发写、敏感字段二次加密。
// TODO(security): v1.0 安全审计
package secret

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/zalando/go-keyring"
)

// keyringService 是 keyring 中所有条目的 service 标识。
//
// 进程内唯一；不要在外部代码中直接使用，而是通过 Store 接口。
const keyringService = "mossterm"

// keyringIndexUser 是 keyring 中索引条目的 user 标识。
const keyringIndexUser = "__index__"

// keyringIndexEntry 是 keyring 中"索引条目"反序列化后的结构。
type keyringIndexEntry struct {
	ID          ID     `json:"id"`
	Name        string `json:"name"`
	Kind        Kind   `json:"kind"`
	Fingerprint string `json:"fingerprint,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	LastUsed    int64  `json:"lastUsed"`
}

// keyringStore 是 Store 的系统 keyring 后端实现。
type keyringStore struct {
	cfg Config

	// unlockedCache 缓存已取出的内容。容量上限由调用频率自然约束
	//（不会无限增长因为 ID 是有限的）。
	mu            sync.RWMutex
	unlockedCache map[ID][]byte
}

// newKeyringStore 构造一个 keyringStore。
//
// 实现细节：
//  1. 不做 keyring 可用性探测（探测成本高且 zalando/go-keyring 自身
//     在不支持的平台上会返回明确 error，由 Set/Get 时再报错）。
//  2. 不读取现有索引 —— 索引的第一次写入会在第一次 Set 时发生。
//  3. HasPassphrase 永远 false（keyring 后端不需要主密码）。
func newKeyringStore(cfg Config) (Store, error) {
	return &keyringStore{
		cfg:           cfg,
		unlockedCache: make(map[ID][]byte),
	}, nil
}

// Set 实现 Store.Set。
func (s *keyringStore) Set(name string, kind Kind, secret []byte, meta map[string]string) (ID, error) {
	if name == "" {
		return "", errors.New("secret.keyringStore.Set: empty name")
	}
	if kind == "" {
		return "", errors.New("secret.keyringStore.Set: empty kind")
	}
	if len(secret) == 0 {
		return "", errors.New("secret.keyringStore.Set: empty secret content")
	}

	id := ID(uuid.NewString())
	fingerprint := ""
	if meta != nil {
		fingerprint = meta["fingerprint"]
	}
	now := time.Now().UnixMilli()

	// 1. 写 content
	encoded := base64.StdEncoding.EncodeToString(secret)
	if err := keyring.Set(keyringService, string(id), encoded); err != nil {
		return "", fmt.Errorf("secret.keyringStore.Set: keyring set content: %w", err)
	}

	// 2. 更新索引
	if err := s.appendIndex(keyringIndexEntry{
		ID:          id,
		Name:        name,
		Kind:        kind,
		Fingerprint: fingerprint,
		CreatedAt:   now,
		LastUsed:    now,
	}); err != nil {
		// 回滚 content（best-effort）
		_ = keyring.Delete(keyringService, string(id))
		return "", fmt.Errorf("secret.keyringStore.Set: update index: %w", err)
	}

	// 3. 写缓存（拷贝一份避免调用方修改底层）
	s.mu.Lock()
	s.unlockedCache[id] = append([]byte(nil), secret...)
	s.mu.Unlock()

	return id, nil
}

// Get 实现 Store.Get。
//
// 内容从 unlockedCache 读，缓存未命中时回源到 keyring。
// 调用方负责用完后清零返回的 []byte。
func (s *keyringStore) Get(id ID) ([]byte, error) {
	if id == "" {
		return nil, errors.New("secret.keyringStore.Get: empty id")
	}

	// 缓存命中
	s.mu.RLock()
	if cached, ok := s.unlockedCache[id]; ok {
		out := append([]byte(nil), cached...)
		s.mu.RUnlock()
		s.touchLastUsed(id)
		return out, nil
	}
	s.mu.RUnlock()

	// 缓存未命中 → 走 keyring
	encoded, err := keyring.Get(keyringService, string(id))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("secret.keyringStore.Get: keyring get: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("secret.keyringStore.Get: base64 decode: %w", err)
	}

	// 写回缓存
	s.mu.Lock()
	s.unlockedCache[id] = append([]byte(nil), decoded...)
	s.mu.Unlock()

	s.touchLastUsed(id)
	return decoded, nil
}

// Delete 实现 Store.Delete。
func (s *keyringStore) Delete(id ID) error {
	if id == "" {
		return errors.New("secret.keyringStore.Delete: empty id")
	}
	// 1. 删 content（不存在不报错，幂等）
	if err := keyring.Delete(keyringService, string(id)); err != nil {
		// keyring.ErrNotFound 视作成功
		if !errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("secret.keyringStore.Delete: keyring delete: %w", err)
		}
	}
	// 2. 清缓存
	s.mu.Lock()
	if cached, ok := s.unlockedCache[id]; ok {
		zeroBytes(cached)
		delete(s.unlockedCache, id)
	}
	s.mu.Unlock()
	// 3. 从索引中移除
	if err := s.removeFromIndex(id); err != nil {
		return fmt.Errorf("secret.keyringStore.Delete: update index: %w", err)
	}
	return nil
}

// List 实现 Store.List。
func (s *keyringStore) List() ([]Item, error) {
	index, err := s.readIndex()
	if err != nil {
		return nil, fmt.Errorf("secret.keyringStore.List: read index: %w", err)
	}
	out := make([]Item, 0, len(index))
	for _, e := range index {
		out = append(out, Item(e))
	}
	return out, nil
}

// HasPassphrase 实现 Store.HasPassphrase。
//
// keyring 后端不需要主密码 —— 系统 keyring 自己负责访问控制。
func (s *keyringStore) HasPassphrase() bool {
	return false
}

// SetPassphrase 实现 Store.SetPassphrase。
//
// keyring 后端不支持主密码：直接返回 nil 让调用方继续。
// 如果未来需要"keyring + 本地加密"双层保护，这里改成 wrap content。
func (s *keyringStore) SetPassphrase(pass string) error {
	_ = pass
	return nil
}

// Close 实现 Store.Close。
//
// 清零 unlockedCache 内的所有 []byte。
func (s *keyringStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, b := range s.unlockedCache {
		zeroBytes(b)
		delete(s.unlockedCache, id)
	}
	return nil
}

// -----------------------------------------------------------------------------
// 内部：索引
// -----------------------------------------------------------------------------

// readIndex 从 keyring 读取索引条目。
//
// 索引不存在（首次启动）返回 (nil, nil)。
func (s *keyringStore) readIndex() ([]keyringIndexEntry, error) {
	encoded, err := keyring.Get(keyringService, keyringIndexUser)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var entries []keyringIndexEntry
	if err := json.Unmarshal([]byte(encoded), &entries); err != nil {
		return nil, fmt.Errorf("unmarshal index: %w", err)
	}
	return entries, nil
}

// writeIndex 整体替换索引条目。
func (s *keyringStore) writeIndex(entries []keyringIndexEntry) error {
	if entries == nil {
		entries = []keyringIndexEntry{}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}
	if err := keyring.Set(keyringService, keyringIndexUser, string(data)); err != nil {
		return fmt.Errorf("keyring set index: %w", err)
	}
	return nil
}

// appendIndex 在索引中追加一条。
func (s *keyringStore) appendIndex(e keyringIndexEntry) error {
	entries, err := s.readIndex()
	if err != nil {
		return err
	}
	// 重复 ID 替换（防御性，正常 Set 不会重复）
	for i, existing := range entries {
		if existing.ID == e.ID {
			entries[i] = e
			return s.writeIndex(entries)
		}
	}
	entries = append(entries, e)
	return s.writeIndex(entries)
}

// removeFromIndex 从索引中移除一条。
func (s *keyringStore) removeFromIndex(id ID) error {
	entries, err := s.readIndex()
	if err != nil {
		return err
	}
	out := entries[:0]
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		return nil
	}
	return s.writeIndex(out)
}

// touchLastUsed 更新指定条目的 LastUsed。
//
// 异步失败不影响主流程：返回 error 但不向调用方冒泡。
func (s *keyringStore) touchLastUsed(id ID) {
	entries, err := s.readIndex()
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()
	updated := false
	for i := range entries {
		if entries[i].ID == id {
			entries[i].LastUsed = now
			updated = true
			break
		}
	}
	if !updated {
		return
	}
	_ = s.writeIndex(entries)
}

// zeroBytes 把 b 全部清零（用于 Close 时清缓存）。
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// 编译期断言：*keyringStore 满足 Store 接口。
var _ Store = (*keyringStore)(nil)
