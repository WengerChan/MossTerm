package connect

import (
	"fmt"
	"sync"
)

// Registry 是协议 Connector 的全局注册中心。
//
// App 启动时遍历已注册的 scheme→Factory 映射，
// session.Manager 在打开新 session 时按 scheme 查找对应 Connector。
type Registry interface {
	// Register 注册一个 scheme（"ssh" / "telnet" / "serial" 等）到对应的 Factory。
	// 重复注册同一 scheme 时返回 error，调用方决定是否覆盖。
	Register(scheme string, f Factory) error
	// Get 按 scheme 取出 Factory。
	Get(scheme string) (Factory, bool)
	// Schemes 返回当前已注册的全部 scheme（顺序不保证）。
	Schemes() []string
	// Build 用 Factory 构造一个 Connector。
	// 这是一个便捷方法：先 Get 再调用 Factory。
	Build(scheme string, deps Deps) (Connector, error)
}

// MemoryRegistry 是 Registry 的进程内实现。
//
// 线程安全：所有读写都用 RWMutex 保护。
type MemoryRegistry struct {
	mu    sync.RWMutex
	factories map[string]Factory
}

// NewMemoryRegistry 构造一个空的注册中心。
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{
		factories: make(map[string]Factory),
	}
}

// Register 实现 Registry.Register。
func (r *MemoryRegistry) Register(scheme string, f Factory) error {
	if scheme == "" {
		return fmt.Errorf("connect: registry: empty scheme")
	}
	if f == nil {
		return fmt.Errorf("connect: registry: nil factory for scheme %q", scheme)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[scheme]; exists {
		return fmt.Errorf("connect: registry: scheme %q already registered", scheme)
	}
	r.factories[scheme] = f
	return nil
}

// Get 实现 Registry.Get。
func (r *MemoryRegistry) Get(scheme string) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[scheme]
	return f, ok
}

// Schemes 实现 Registry.Schemes。
func (r *MemoryRegistry) Schemes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.factories))
	for k := range r.factories {
		out = append(out, k)
	}
	return out
}

// Build 实现 Registry.Build。
func (r *MemoryRegistry) Build(scheme string, deps Deps) (Connector, error) {
	f, ok := r.Get(scheme)
	if !ok {
		return nil, fmt.Errorf("connect: registry: no factory for scheme %q", scheme)
	}
	return f(deps)
}

// 编译期断言：*MemoryRegistry 满足 Registry 接口。
var _ Registry = (*MemoryRegistry)(nil)
