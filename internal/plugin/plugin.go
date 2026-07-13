// Package plugin 提供 WASM 插件宿主。
//
// v0.3+ 启用。当前 v0.1 状态：
//   - 接口 + stub 已存在。
//   - Load 拒绝任何 .wasm（return error）。
//   - 进程仍会启动 / 关闭，但无实际功能。
//
// 未来实现采用 tetratelabs/wazero 作为 WASM 运行时。
package plugin

import (
	"context"
	"sync"
)

// Manifest 描述一个插件的元数据（来自 plugin.toml）。
type Manifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Author      string   `json:"author"`
	Entry       string   `json:"entry"` // 入口 wasm 文件相对路径
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

// Event 是插件订阅 / 发布的消息。
type Event struct {
	Topic   string
	Payload []byte
}

// Plugin 是一个已加载的插件实例。
type Plugin interface {
	// Name 返回插件名（与 Manifest.Name 一致）。
	Name() string
	// Manifest 返回插件元数据。
	Manifest() Manifest
	// Call 调用插件导出函数 fn，args 是位置参数。
	// 返回值是插件侧用 wazero 写出的字节流（由宿主解码）。
	Call(ctx context.Context, fn string, args ...interface{}) (interface{}, error)
	// Subscribe 订阅事件总线上的 topic。
	Subscribe(topic string) (<-chan Event, func())
}

// Host 是插件宿主的抽象。
type Host interface {
	// Load 从 wasm 字节流加载并实例化一个插件。
	// v0.1：拒绝任何非空字节流（"not yet enabled"）。
	Load(name string, wasm []byte) (Plugin, error)
	// Get 按 name 取插件。
	Get(name string) (Plugin, bool)
	// List 列出全部已加载插件的 Manifest。
	List() []Manifest
	// Unload 卸载插件并释放 wazero 实例。
	Unload(name string) error
}

// MemoryHost 是 Host 的进程内实现（v0.1 stub）。
type MemoryHost struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
}

// NewMemoryHost 构造一个空的 Host。
func NewMemoryHost() *MemoryHost {
	return &MemoryHost{
		plugins: make(map[string]Plugin),
	}
}

// Load 实现 Host.Load。
//
// v0.1：任何非空 wasm 都返回 error。
func (h *MemoryHost) Load(name string, wasm []byte) (Plugin, error) {
	if len(wasm) > 0 {
		panic("plugin.MemoryHost.Load: plugin system not yet enabled (v0.3+)")
	}
	return nil, nil
}

// Get 实现 Host.Get。
func (h *MemoryHost) Get(name string) (Plugin, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p, ok := h.plugins[name]
	return p, ok
}

// List 实现 Host.List。
func (h *MemoryHost) List() []Manifest {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Manifest, 0, len(h.plugins))
	for _, p := range h.plugins {
		out = append(out, p.Manifest())
	}
	return out
}

// Unload 实现 Host.Unload。
func (h *MemoryHost) Unload(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.plugins[name]; !ok {
		return nil
	}
	delete(h.plugins, name)
	return nil
}

// 编译期断言。
var _ Host = (*MemoryHost)(nil)
