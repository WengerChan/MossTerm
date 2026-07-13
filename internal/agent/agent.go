// Package agent 提供本地 SSH agent 客户端与跳板链构建能力。
//
// 拆分：
//   - Client  ——  对本地 ssh-agent（$SSH_AUTH_SOCK）的封装，提供 Signer 列表
//   - Registry ——  跳板策略的注册表（direct / single-jump / ...）
//   - 具体策略 BuildFunc ——  由调用方实现并注册；agent 包不依赖
//                          internal/sshclient，避免循环 import
//
// 与 internal/connect 的关系：
//   - connect.Registry 管理"协议"（ssh / telnet / serial）
//   - agent.Registry  管理"跳板策略"（direct / single-jump / ...）
//   - 两者正交：先选 protocol connector，再用 agent strategy 构造最终 *ssh.Client
//
// v0.1 状态：只提供脚手架；具体 "direct" 策略在 app/wire.go 用 sshclient 实现。
package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	ssh_agent "golang.org/x/crypto/ssh/agent"

	"github.com/mossterm/mossterm/internal/connect"
)

// Client 是对本地 SSH agent 的封装。
type Client interface {
	// Signers 返回 agent 当前持有的全部 Signer。
	Signers() ([]ssh.Signer, error)
	// Close 关闭底层 socket 连接。
	Close() error
}

// agentClient 是 Client 的默认实现。
type agentClient struct {
	conn     ssh_agent.ExtendedAgent
	sockConn net.Conn
	closed   bool
}

// NewAgentClient 构造一个 Client。
//
// socketPath 为空时读 $SSH_AUTH_SOCK 环境变量。
// 显式指定 socketPath 在 v0.1 不支持（v0.2+ 接入自管 socket）。
func NewAgentClient(socketPath string) (Client, error) {
	if socketPath != "" {
		return nil, fmt.Errorf("agent.NewAgentClient: custom socket path not yet supported (v0.2+)")
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, fmt.Errorf("agent.NewAgentClient: SSH_AUTH_SOCK not set")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("agent.NewAgentClient: dial %s: %w", sock, err)
	}
	a := ssh_agent.NewClient(conn)
	return &agentClient{conn: a, sockConn: conn}, nil
}

// Signers 转发到 Agent.Signers。
func (c *agentClient) Signers() ([]ssh.Signer, error) {
	if c.closed {
		return nil, fmt.Errorf("agent.agentClient.Signers: closed")
	}
	if c.conn == nil {
		return nil, nil
	}
	return c.conn.Signers()
}

// Close 标记 closed。go-agent 的 client 没有 Close（持有的 conn 由
// ssh-agent 进程维护），所以这里只置位。
func (c *agentClient) Close() error {
	c.closed = true
	return nil
}

// Hop 描述跳板链中的一跳。
//
// v0.1：Hops 为空（直连）；v0.5+ 用 ProfileID 引用具体 profile。
type Hop struct {
	ProfileID string
}

// Target 描述最终目标。
type Target struct {
	Host string
	Port int
	User string
	Auth connect.AuthMethod
}

// BuildFunc 是跳板链构建函数。
//
// 每种"跳板策略"注册一个 BuildFunc；Registry.Build 是它们的薄包装。
// 调用方应负责 Close 返回的 *ssh.Client。
type BuildFunc func(ctx context.Context, opts BuildOptions) (*ssh.Client, error)

// BuildOptions 是 Build 的入参。
type BuildOptions struct {
	// Hops 是跳板链中除 finalTarget 外的中间跳（顺序）。
	Hops []Hop
	// FinalTarget 是最终目标。
	FinalTarget Target
	// DialTimeout / KeepAlive 透传给 connect.Connector.Dial。
	DialTimeout time.Duration
	KeepAlive   time.Duration
	// ProfileResolver 把 ProfileID 解析为跳板目标信息；
	// 由 main.go / app 包注入（持有 profile 注册表）。
	// v0.1 可为 nil —— direct 策略不需要。
	ProfileResolver func(profileID string) (HopTarget, bool)
}

// HopTarget 是从 profile 解析出的跳板目标信息。
type HopTarget struct {
	Host string
	Port int
	User string
	Auth connect.AuthMethod
}

// Registry 管理跳板策略（direct / single-jump / ...）。
//
// 设计参考 internal/connect.Registry：Register + Get + Schemes + Build
// 四个方法，线程安全。
type Registry interface {
	// Register 注册一个策略名到 BuildFunc 的映射。
	// 重复注册同一 name 返回 error。
	Register(name string, f BuildFunc) error
	// Get 按 name 取出 BuildFunc。
	Get(name string) (BuildFunc, bool)
	// Schemes 返回全部已注册策略名。
	Schemes() []string
	// Build 是 Get + 调用的薄包装；name 未注册返回 error。
	Build(ctx context.Context, name string, opts BuildOptions) (*ssh.Client, error)
}

// MemoryRegistry 是 Registry 的进程内实现。
type MemoryRegistry struct {
	mu        sync.RWMutex
	strategies map[string]BuildFunc
}

// NewMemoryRegistry 构造一个空 Registry。
//
// v0.1：什么都不预注册 —— 由 main.go / app/wire.go 注入 "direct" 策略
// （避免 agent 包反向依赖 sshclient）。这样保证 agent 包
// 可以在 sshclient 未就绪时编译通过。
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{
		strategies: make(map[string]BuildFunc),
	}
}

// Register 实现 Registry.Register。
func (r *MemoryRegistry) Register(name string, f BuildFunc) error {
	if name == "" {
		return fmt.Errorf("agent: registry: empty name")
	}
	if f == nil {
		return fmt.Errorf("agent: registry: nil BuildFunc for %q", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.strategies[name]; exists {
		return fmt.Errorf("agent: registry: strategy %q already registered", name)
	}
	r.strategies[name] = f
	return nil
}

// Get 实现 Registry.Get。
func (r *MemoryRegistry) Get(name string) (BuildFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.strategies[name]
	return f, ok
}

// Schemes 实现 Registry.Schemes。
func (r *MemoryRegistry) Schemes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.strategies))
	for k := range r.strategies {
		out = append(out, k)
	}
	return out
}

// Build 实现 Registry.Build。
func (r *MemoryRegistry) Build(ctx context.Context, name string, opts BuildOptions) (*ssh.Client, error) {
	f, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("agent: registry: no strategy %q", name)
	}
	return f(ctx, opts)
}

// "direct" 策略的实现（v0.1 跳板 = 一次直连）。
//
// 设计权衡：agent 包不 import internal/sshclient（避免循环 + 解耦），
// 所以 "direct" 的 BuildFunc 必须在引用了 sshclient 的包内实现
// （典型是 cmd/mossterm 或 app/wire.go）。
//
// 伪代码：
//
//	directFn := func(ctx context.Context, opts agent.BuildOptions) (*ssh.Client, error) {
//	    // 1. 用 connect.Connector.Dial(opts.FinalTarget) 拿到 net.Conn
//	    // 2. 通过类型断言取出 *ssh.Client
//	    // 3. opts.Hops 非空时按 v0.5 multi-hop 协议处理
//	    return client, nil
//	}
//	ag.Register("direct", directFn)
//
// v0.1：实际由 session.Manager 直接走 connect.Connector，不调 agent.Build。
// v0.5：main.go 启动时注册上述 "direct" + 真实 multi-hop 策略。

// 编译期断言。
var (
	_ Registry = (*MemoryRegistry)(nil)
	_ Client   = (*agentClient)(nil)
)
