// Package tunnel 实现 SSH 端口转发。
//
// 支持三种模式：
//   - Local  (-L)：本地端口 → 远端主机
//   - Remote (-R)：远端端口 → 本地主机
//   - Dynamic (-D SOCKS5)：本地 SOCKS5 代理
//
// 架构：
//   - ClientProvider：把 sessionID 映射为 *ssh.Client（main.go 注入）
//   - baseTunnel：状态机 + spec 持有（Local / Remote / Dynamic 共享）
//   - local.go / remote.go / socks5.go：具体模式实现
//   - MemoryManager：进程内的 Manager，Open 按 spec.Mode 分派
//
// v0.6 接入：MemoryManager.Open 真实实现三种模式；测试通过 WithClientProvider
// 注入 provider；v0.6.1 接入 main.go 注入。
package tunnel

import (
	"context"
	"errors"
	"fmt"
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

// String 返回 mode 的可读名称（调试 + JSON 字段用）。
func (m Mode) String() string {
	switch m {
	case Local:
		return "local"
	case Remote:
		return "remote"
	case Dynamic:
		return "dynamic"
	default:
		return fmt.Sprintf("unknown(%d)", int(m))
	}
}

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

// String 返回 state 的可读名称。
func (s TunnelState) String() string {
	switch s {
	case TunnelStateNew:
		return "new"
	case TunnelStateActive:
		return "active"
	case TunnelStateStopped:
		return "stopped"
	case TunnelStateFailed:
		return "failed"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

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
	mu       sync.RWMutex
	tunnels  map[string]Tunnel
	provider ClientProvider
}

// NewMemoryManager 构造一个空的 Manager。
//
// ClientProvider 需要后续用 WithClientProvider 注入；未注入时 Open
// 返回 error（不让 tunnel 在没 SSH client 引用的情况下启动）。
func NewMemoryManager() *MemoryManager {
	return &MemoryManager{
		tunnels: make(map[string]Tunnel),
	}
}

// WithClientProvider 注入 *ssh.Client 提供器。
//
// v0.6 引入：MemoryManager.Open 需要知道每个 tunnel 关联的 session
// 的底层 *ssh.Client。Provider 是唯一入口（避免 tunnel 反向依赖
// *session.Manager / *sshclient.Connector）。
//
// main.go 在 New 之后调一次；线程安全（mu 保护）。
func (m *MemoryManager) WithClientProvider(p ClientProvider) *MemoryManager {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.provider = p
	return m
}

// Open 实现 Manager.Open。
//
// 校验：
//   - spec.ID 非空
//   - spec.Mode 合法
//   - spec.SessionID 非空
//   - spec.BindHost / TargetHost 非空
//   - 已注册 provider（不报"missing provider"会让错误信息更友好）
//
// 然后按 Mode 分派：Local / Remote / Dynamic；具体 runner 构造后调
// Start(ctx)。Start 失败时从 map 删除 + State → Failed。
func (m *MemoryManager) Open(ctx context.Context, spec Spec) (Tunnel, error) {
	if spec.ID == "" {
		return nil, errors.New("tunnel.Manager.Open: empty ID")
	}
	if spec.SessionID == "" {
		return nil, errors.New("tunnel.Manager.Open: empty SessionID")
	}
	if spec.BindHost == "" {
		return nil, errors.New("tunnel.Manager.Open: empty BindHost")
	}
	// Local/Remote 需要 Target；Dynamic 不需要
	if spec.Mode != Dynamic {
		if spec.TargetHost == "" {
			return nil, errors.New("tunnel.Manager.Open: empty TargetHost (only Dynamic mode allows empty target)")
		}
	}

	// 检查重复 ID
	m.mu.RLock()
	_, exists := m.tunnels[spec.ID]
	m.mu.RUnlock()
	if exists {
		return nil, fmt.Errorf("tunnel.Manager.Open: id %q already in use", spec.ID)
	}

	// provider 兜底：Dynamic 也需要 provider（转发时 client.Dial）
	m.mu.RLock()
	provider := m.provider
	m.mu.RUnlock()
	if provider == nil {
		return nil, errors.New("tunnel.Manager.Open: ClientProvider not set (call WithClientProvider first)")
	}

	// 按 mode 构造 + 启动
	var t Tunnel
	switch spec.Mode {
	case Local:
		t = newLocalTunnel(spec, provider)
	case Remote:
		t = newRemoteTunnel(spec, provider)
	case Dynamic:
		t = newDynamicTunnel(spec, provider)
	default:
		return nil, fmt.Errorf("tunnel.Manager.Open: unknown mode %d", spec.Mode)
	}

	if err := t.Start(ctx); err != nil {
		// 启动失败：t 可能已经 setState(Failed) 但未注册
		_ = t.Stop()
		return nil, fmt.Errorf("tunnel.Manager.Open: start %s: %w", spec.Mode, err)
	}

	// 注册到 map（覆盖式：上面 exists 检查已保证无重复）
	m.mu.Lock()
	m.tunnels[spec.ID] = t
	m.mu.Unlock()

	return t, nil
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
