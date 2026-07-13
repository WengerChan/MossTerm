// Package config 的 manager.go：Manager 类型 + 公开方法。
//
// 设计要点：
//   - Manager 是配置的运行时句柄，进程内单例（由 app 持有）。
//   - 公开方法全部走 RWMutex 保护；Get 返回深拷贝避免外部破坏内部状态。
//   - 持久化走 BurntSushi/toml（与 struct 上的 toml tag 配套）。
//   - 路径解析、默认数据工厂、首次启动拷贝等放在 loader.go。
//
// v0.1 状态：
//   - CRUD / 持久化 / 默认值填充：完整实现。
//   - Watch（fsnotify 热加载）：v0.1 不实现，预留签名 + TODO 注释。
//   - 多 profile 场景的并发安全由 RWMutex 保证；高频写请用 Update
//     （copy-on-write），不要 Get + 改 + Save 串行做。
package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// New 用 path 构造一个 Manager。
//
// 行为：
//   - path == ""：用 DefaultConfigPath() 解析；
//   - 文件存在：从磁盘加载到内存；
//   - 文件不存在：写一份 Defaults() 到磁盘，再加载；
//   - 父目录不存在：尝试创建（权限不足则返回 error）。
//
// 返回的 Manager 已持有有效 *Data 指针，调用方可以直接 Get / Update。
func New(path string) (*Manager, error) {
	if path == "" {
		path = DefaultConfigPath()
	}
	// 路径归一化 + 父目录创建
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config.New: resolve absolute path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("config.New: create parent dir: %w", err)
	}

	m := &Manager{
		path: abs,
		log:  slog.Default(),
	}

	// 文件存在 → 加载；不存在 → 写默认
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		// 首次启动：写默认 + 复制示例（best-effort，失败不阻断）
		if err := m.writeDefaultsToDisk(); err != nil {
			return nil, fmt.Errorf("config.New: write defaults: %w", err)
		}
		if err := CopyExampleTo(filepath.Dir(abs)); err != nil {
			// 示例拷贝失败只 log，不影响主流程
			m.log.Warn("config.New: copy example failed", "err", err)
		}
	}

	// 加载（或重新加载刚写的默认）
	if err := m.loadFromDisk(); err != nil {
		return nil, fmt.Errorf("config.New: load: %w", err)
	}
	return m, nil
}

// Path 返回配置文件绝对路径。
func (m *Manager) Path() string { return m.path }

// Load 从磁盘重新加载配置。
//
// 覆盖内存中的 *Data 指针；调用方需要自己保证没有其它 goroutine
// 正在持有 Get 返回的 Data 副本（v0.1 调用方是单线程 Wails 绑定层）。
func (m *Manager) Load() error {
	return m.loadFromDisk()
}

// Save 把当前内存配置写回磁盘。
//
// 走 atomic-rename 防止写入半截：先写到 .tmp，再 rename 到目标。
// 该函数不持有读锁（会死锁），内部用单独的短锁。
func (m *Manager) Save() error {
	if m == nil {
		return errors.New("config.Manager.Save: nil receiver")
	}
	if m.path == "" {
		return errors.New("config.Manager.Save: empty path")
	}
	m.mu.RLock()
	snap := cloneData(m.data)
	m.mu.RUnlock()

	if err := writeTOMLFile(m.path, &snap); err != nil {
		return fmt.Errorf("config.Manager.Save: %w", err)
	}
	return nil
}

// Get 返回当前 Data 的深拷贝。
//
// 返回值是 *Data（用户指令要求），但语义仍是只读 —— 改它不会影响
// Manager 内部状态，需要持久化请走 Update。
func (m *Manager) Get() *Data {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.data == nil {
		return Defaults()
	}
	out := cloneData(m.data)
	return &out
}

// Update 用调用方提供的 *Data 替换当前配置。
//
// 整段原子替换（写锁 → 拷指针 → 解锁），读侧看到的是完整的旧或新。
// 替换前会触发一次 Save 把新值落盘（OnShutdown 也再 Save 一次兜底）。
func (m *Manager) Update(d *Data) error {
	if m == nil {
		return errors.New("config.Manager.Update: nil receiver")
	}
	if d == nil {
		return errors.New("config.Manager.Update: nil data")
	}
	// 补全缺失的 map 字段，避免 nil-map 写
	normalizeData(d)

	m.mu.Lock()
	m.data = d
	m.mu.Unlock()

	if err := m.Save(); err != nil {
		return fmt.Errorf("config.Manager.Update: save: %w", err)
	}
	return nil
}

// AddProfile 把 p 添加到 Profiles（按 p.ID 索引）。
//
// 已存在同 ID 时返回 error；改 ID 后重试可以走 UpdateProfile。
func (m *Manager) AddProfile(p Profile) error {
	if p.ID == "" {
		return errors.New("config.Manager.AddProfile: empty ID")
	}
	m.mu.Lock()
	if m.data == nil {
		m.data = Defaults()
	}
	if _, exists := m.data.Profiles[p.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("config.Manager.AddProfile: profile %q already exists", p.ID)
	}
	if m.data.Profiles == nil {
		m.data.Profiles = make(map[string]Profile)
	}
	now := time.Now().Unix()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	m.data.Profiles[p.ID] = p
	snap := cloneData(m.data)
	m.mu.Unlock()

	if err := writeTOMLFile(m.path, &snap); err != nil {
		return fmt.Errorf("config.Manager.AddProfile: save: %w", err)
	}
	return nil
}

// UpdateProfile 按 ID 更新一个 Profile。
//
// 不存在时返回 error。新增请走 AddProfile。
func (m *Manager) UpdateProfile(p Profile) error {
	if p.ID == "" {
		return errors.New("config.Manager.UpdateProfile: empty ID")
	}
	m.mu.Lock()
	if m.data == nil {
		m.data = Defaults()
	}
	if _, exists := m.data.Profiles[p.ID]; !exists {
		m.mu.Unlock()
		return fmt.Errorf("config.Manager.UpdateProfile: profile %q not found", p.ID)
	}
	p.CreatedAt = m.data.Profiles[p.ID].CreatedAt // 保留原 CreatedAt
	p.UpdatedAt = time.Now().Unix()
	m.data.Profiles[p.ID] = p
	snap := cloneData(m.data)
	m.mu.Unlock()

	if err := writeTOMLFile(m.path, &snap); err != nil {
		return fmt.Errorf("config.Manager.UpdateProfile: save: %w", err)
	}
	return nil
}

// DeleteProfile 按 ID 删除一个 Profile。
//
// 不存在时返回 nil（幂等）。
func (m *Manager) DeleteProfile(id string) error {
	if id == "" {
		return errors.New("config.Manager.DeleteProfile: empty ID")
	}
	m.mu.Lock()
	if m.data == nil {
		m.data = Defaults()
	}
	if _, exists := m.data.Profiles[id]; !exists {
		m.mu.Unlock()
		return nil
	}
	delete(m.data.Profiles, id)
	snap := cloneData(m.data)
	m.mu.Unlock()

	if err := writeTOMLFile(m.path, &snap); err != nil {
		return fmt.Errorf("config.Manager.DeleteProfile: save: %w", err)
	}
	return nil
}

// GetProfile 按 ID 取一个 Profile 的拷贝。
//
// 不存在时返回 (zero, false)。
func (m *Manager) GetProfile(id string) (Profile, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.data == nil {
		return Profile{}, false
	}
	p, ok := m.data.Profiles[id]
	if !ok {
		return Profile{}, false
	}
	return p, true
}

// ListProfiles 返回全部 Profile（按 ID 字典序）。
func (m *Manager) ListProfiles() []Profile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.data == nil {
		return nil
	}
	out := make([]Profile, 0, len(m.data.Profiles))
	for _, p := range m.data.Profiles {
		out = append(out, p)
	}
	return out
}

// SetSetting 替换全局 Settings 并落盘。
func (m *Manager) SetSetting(s Settings) error {
	m.mu.Lock()
	if m.data == nil {
		m.data = Defaults()
	}
	m.data.Settings = s
	snap := cloneData(m.data)
	m.mu.Unlock()

	if err := writeTOMLFile(m.path, &snap); err != nil {
		return fmt.Errorf("config.Manager.SetSetting: save: %w", err)
	}
	return nil
}

// Watch 注册一个 fsnotify 监听器（v0.1 不实现）。
//
// TODO(v0.2+): 引入 fsnotify 后实现：
//  1. 监听 path 所在的目录（编辑器常做 atomic-rename，原文件 inode 会变）
//  2. 收到事件后去抖 100ms 再 reload
//  3. 重新解析 + 替换 m.data
//  4. 通过 fan-out channel 通知订阅者
//
// v0.1 行为：ctx 取消前阻塞不做任何事；保证调用方能安全传入 ctx。
func (m *Manager) Watch(ctx context.Context) error {
	if m == nil {
		return errors.New("config.Manager.Watch: nil receiver")
	}
	if ctx == nil {
		return errors.New("config.Manager.Watch: nil ctx")
	}
	// v0.1 静默等待 ctx 取消。
	<-ctx.Done()
	return nil
}

// -----------------------------------------------------------------------------
// 内部
// -----------------------------------------------------------------------------

// loadFromDisk 从 m.path 读取 TOML 并填充 m.data。
//
// 文件不存在（理论上 New 阶段已写过）→ 用 Defaults 兜底。
// 解析失败 → 返回 error，调用方决定是否覆盖。
func (m *Manager) loadFromDisk() error {
	data, err := readTOMLFile(m.path)
	if err != nil {
		return err
	}
	if data == nil {
		data = Defaults()
	}
	normalizeData(data)

	m.mu.Lock()
	m.data = data
	m.mu.Unlock()
	return nil
}

// writeDefaultsToDisk 把 Defaults() 写到 m.path。
//
// 首次启动专用：保证磁盘上有一份能加载的 config.toml。
func (m *Manager) writeDefaultsToDisk() error {
	d := Defaults()
	return writeTOMLFile(m.path, d)
}

// normalizeData 把 nil-map 字段填充为空 map，避免后续写时 panic。
func normalizeData(d *Data) {
	if d == nil {
		return
	}
	if d.Profiles == nil {
		d.Profiles = make(map[string]Profile)
	}
	if d.Layouts == nil {
		d.Layouts = make(map[string]Layout)
	}
	if d.Keymaps == nil {
		d.Keymaps = make(map[string]Keymap)
	}
	if d.Themes == nil {
		d.Themes = make(map[string]Theme)
	}
}
