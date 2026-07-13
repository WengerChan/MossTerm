// Package config 提供 MossTerm 的配置加载、持久化、热更新能力。
//
// 配置文件：TOML 格式，存于 ~/.config/mossterm/config.toml。
// 监听：fsnotify（仅 Linux/macOS/Windows 全平台支持）。
//
// Schema 变更必须写 migrations/00X_xxx.go（v0.2+ 引入）。
package config

import (
	"log/slog"
	"sync"
)

// Manager 是配置的运行时句柄。
//
// 并发安全：所有公开方法都用 RWMutex 保护。
// 内部维护一份 *Data 指针，Update 走 copy-on-write 避免读阻塞。
type Manager struct {
	path string
	mu   sync.RWMutex
	data *Data
	log  *slog.Logger
}

// Data 是配置文件的内存表示。
//
// 所有字段在 toml tag 上都标注了 snake_case 命名。
type Data struct {
	Version  int                  `toml:"version"`
	Settings Settings             `toml:"settings"`
	Profiles map[string]Profile   `toml:"profiles"`
	Layouts  map[string]Layout    `toml:"layouts"`
	Keymaps  map[string]Keymap    `toml:"keymaps"`
	Themes   map[string]Theme     `toml:"themes"`
	Recent   []string             `toml:"recent"`
}

// Settings 是全局偏好设置。
type Settings struct {
	DefaultTheme  string
	DefaultFont   string
	FontSize      int
	Scrollback    int
	KeepAliveSecs int

	// AllowPassword 默认 false（架构基线：默认禁用密码登录）。
	AllowPassword bool
	// Telemetry 默认 false（隐私优先）。
	Telemetry bool
	// CheckUpdate 默认 true。
	CheckUpdate bool

	// AI 相关（v0.2+）。
	AIProvider string
	AIEndpoint string
	AIKeyID    string
}

// Profile 描述一个 SSH 连接模板。
type Profile struct {
	ID        string
	Name      string
	Group     string
	Host      string
	Port      int
	User      string
	Color     string
	Icon      string
	Auth      AuthConfig
	Env       map[string]string
	JumpVia   []string
	Tags      []string
	CreatedAt int64
	UpdatedAt int64
}

// AuthConfig 是 Profile 的身份验证子配置。
type AuthConfig struct {
	// Kind: "password" | "publickey" | "agent" | "keyboard-interactive"。
	Kind string
	// KeyID 是 secret.Store 中的私钥条目 ID（仅 publickey 时使用）。
	KeyID string
	// Username 可选（覆盖 Profile.User）。
	Username string
	// Command 是 ProxyCommand（v0.2+ 跳板链备选）。
	Command string
}

// Layout 描述一个窗口布局模板（v0.2+ 引入）。
type Layout struct {
	ID   string
	Name string
	Tabs []Tab
}

// Tab 是 Layout 内的一个 tab。
type Tab struct {
	Title string
	Panes []Pane
}

// Pane 是 Tab 内的一个 pane。
type Pane struct {
	ProfileID string
	Split     string
	Size      int
}

// Keymap 描述一组快捷键绑定。
type Keymap struct {
	Bindings map[string]string `toml:"bindings"`
}

// Theme 描述一个颜色主题。
type Theme struct {
	Name   string
	Bg     string
	Fg     string
	Cursor string
	Ansi   []string `toml:"ansi"`
}
