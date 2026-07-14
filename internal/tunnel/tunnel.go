// Package tunnel 实现 SSH 端口转发。
//
// 支持三种模式：
//   - Local  (-L)：本地端口 → 远端主机
//   - Remote (-R)：远端端口 → 本地主机
//   - Dynamic (-D SOCKS5)：本地 SOCKS5 代理
//
// v0.1 留接口；v0.2 完整实现。
package tunnel

import (
	"context"
	"errors"
	"sync"
)

// Mode 表示转发方向。
type Mode int

const (
	// Local 是 -L 模式：监听本地端口，把流量转到远端 Target。
	Local Mode = iota
	// Remote 是 -R 模式：在远端监听端口，把流量转到本地。
	Remote
	// Dynamic 是 -D 模式：本地 SOCKS5 代理，通过 SSH 通道中继。
	Dynamic
)

// Spec 描述一个转发的配置。
type Spec struct {
	ID         string `json:"id"`
	Mode       Mode   `json:"mode"`
	BindHost   string `json:"bindHost"`
	BindPort   int    `json:"bindPort"`
	TargetHost string `json:"targetHost"`
	TargetPort int    `json:"targetPort"`
	// SessionID 关联的 SSH session；转发依赖该 session 的 channel。
	SessionID string `json:"sessionId"`
}

// TunnelState 是隧道的运行时状态。
type TunnelState int

const (
	TunnelStateNew TunnelState = iota
	TunnelStateActive
	TunnelStateStopped
	TunnelStateFailed
)

// Stats 描述一个隧道的累计统计。
type Stats struct {
	BytesIn     int64
	BytesOut    int64
	ActiveConns int
	StartedAt   int64
}

// Tunnel 是一个具体的转发实例。
type Tunnel interface {
	Start(ctx context.Context) error
	Stop() error
	Spec() Spec
	State() TunnelState
	Stats() Stats
}

// Manager 维护所有活跃转发。
type Manager interface {
	Open(ctx context.Context, spec Spec) (Tunnel, error)
	Close(id string) error
	List() []Spec
	Get(id string) (Tunnel, bool)
}

// MemoryManager 是 Manager 的进程内实现。
type MemoryManager struct {
	mu      sync.RWMutex
	tunnels map[string]Tunnel
}

// NewMemoryManager 构造一个空的 Manager。
func NewMemoryManager() *MemoryManager {
	return &MemoryManager{
		tunnels: make(map[string]Tunnel),
	}
}

// Open 实现 Manager.Open。
func (m *MemoryManager) Open(ctx context.Context, spec Spec) (Tunnel, error) {
	if spec.ID == "" {
		return nil, errors.New("tunnel.Manager.Open: empty ID")
	}
	panic("tunnel.MemoryManager.Open: not implemented")
}

// Close 实现 Manager.Close。
func (m *MemoryManager) Close(id string) error {
	m.mu.Lock()
	t, ok := m.tunnels[id]
	if !ok {
		m.mu.Unlock()
		return errors.New("tunnel.Manager.Close: id not found")
	}
	delete(m.tunnels, id)
	m.mu.Unlock()
	return t.Stop()
}

// List 实现 Manager.List。
func (m *MemoryManager) List() []Spec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Spec, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		out = append(out, t.Spec())
	}
	return out
}

// Get 实现 Manager.Get。
func (m *MemoryManager) Get(id string) (Tunnel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tunnels[id]
	return t, ok
}

// 编译期断言。
var _ Manager = (*MemoryManager)(nil)
