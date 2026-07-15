// Package agent 提供本地 SSH agent 客户端与跳板链构建能力。
//
// 拆分：
//   - Client  ——  对本地 ssh-agent（$SSH_AUTH_SOCK）的封装，提供 Signer 列表
//   - Registry ——  跳板策略的注册表（direct / single-jump / multi-hop）
//   - 具体策略 BuildFunc ——  由调用方实现并注册；agent 包不依赖
//     internal/sshclient，避免循环 import
//
// 与 internal/connect 的关系：
//   - connect.Registry 管理"协议"（ssh / telnet / serial）
//   - agent.Registry  管理"跳板策略"（direct / single-jump / multi-hop）
//   - 两者正交：先选 protocol connector，再用 agent strategy 构造最终 *ssh.Client
//
// v0.6 起：BuildOptions 增加 Method 字段，Hops/Target 显式标记每跳
// 用的 auth kind（password / publickey / agent），让 hops 之间能用
// 不同认证；Dialer 抽象承担"单跳拨号"职责，策略函数复用同一份实现。
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
	"github.com/mossterm/mossterm/internal/secret"
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

// -----------------------------------------------------------------------------
// 跳板链类型（v0.6 扩字段：Method）
// -----------------------------------------------------------------------------

// MethodKind 是业务层识别 auth 方式的字符串，对应 connect.AuthKind。
//
// 与 connect.AuthKind 取值保持一致（"password" / "publickey" / "agent" /
// "keyboard-interactive"），通过 const alias 而非 string 复制，避免漂移。
type MethodKind = string

// Method 常量。直接用 const alias 到 connect.AuthKind* 的值。
const (
	MethodPassword            MethodKind = connect.AuthKindPassword
	MethodPublicKey           MethodKind = connect.AuthKindPublicKey
	MethodAgent               MethodKind = connect.AuthKindAgent
	MethodKeyboardInteractive MethodKind = connect.AuthKindKeyboardInteractive
)

// Hop 描述跳板链中的一跳。
//
// v0.1：Hops 为空（直连）；v0.5+ 用 ProfileID 引用具体 profile。
// v0.6：补 Method 字段，标识该跳应该用哪种 auth kind（让 hops 之间
// 能用不同认证，例如 hop1 = agent / hop2 = password）。
//
// Method 为空时由调用方自行决定兜底（策略函数在 auth 解析阶段会
// 把它当 "any" 处理 —— 走 Auth 字段或 ProfileResolver 的结果）。
type Hop struct {
	// ProfileID 通过 ProfileResolver 解析为 HopTarget。
	ProfileID string
	// Method 标识该跳的 auth kind（password / publickey / agent / keyboard-interactive）。
	// 与 Auth 二选一时，Auth 优先。
	Method MethodKind
}

// Target 描述最终目标（或一个具体的 hop 解析结果）。
//
// v0.6：补 Method 字段，意义同 Hop.Method。
type Target struct {
	Host   string
	Port   int
	User   string
	Auth   connect.AuthMethod
	Method MethodKind
}

// ResolveAuth 按以下优先级返回一个具体 AuthMethod：
//  1. t.Auth 非 nil → 直接返回
//  2. t.Method 非空 → 根据 kind 构造默认 Auth：
//     - "agent" → connect.AgentAuth{}
//     - "password" → 需 Password（无默认，返回 nil + nil）
//     - "publickey" → 需 KeyID（无默认，返回 nil + nil）
//  3. 都没设 → 返回 nil + nil（调用方应报"未指定 auth"）
//
// 返回的 AuthMethod 与 secrets 解析无关 —— 真正的私钥拉取由
// connect.ToSSHAuthMethods 在 dial 阶段处理。
func (t Target) ResolveAuth() connect.AuthMethod {
	if t.Auth != nil {
		return t.Auth
	}
	switch t.Method {
	case MethodAgent:
		return connect.AgentAuth{}
	default:
		// password / publickey / keyboard-interactive 都缺具体 payload
		// （password / KeyID / questions 都需要外部输入），无默认
		return nil
	}
}

// -----------------------------------------------------------------------------
// Dialer：单跳拨号抽象
// -----------------------------------------------------------------------------

// Dialer 把"单跳 SSH 拨号"封装成一个可注入接口。
//
// 策略函数（direct / single-jump / multi-hop）通过 BuildOptions.Dialer
// 拿到一个 Dialer，对每跳调一次 Dial 拿到 *ssh.Client，再决定是否转发。
//
// 注入的好处：
//   - agent 包不直接依赖 sshclient（保留"agent 不反向依赖具体实现"的边界）
//   - 测试可以注入 mock Dialer 走单测（v0.6 集成测试用 in-process SSH server
//     不依赖 mock；单元测试由后续 milestone 补）
type Dialer interface {
	// Dial 拨号到一个具体 target，返回已 SSH 握手成功的 *ssh.Client。
	//
	// 调用方负责最终 Close *ssh.Client。
	// ctx 取消或超时时立刻返回 ctx.Err()。
	Dial(ctx context.Context, target Target) (*ssh.Client, error)
}

// DialFunc 是 Dialer 的函数式适配器。
//
// 用法：dialer := agent.DialFunc(myFunc)；等价于实现 Dialer。
type DialFunc func(ctx context.Context, target Target) (*ssh.Client, error)

// Dial 实现 Dialer.Dial。
func (f DialFunc) Dial(ctx context.Context, target Target) (*ssh.Client, error) {
	return f(ctx, target)
}

// -----------------------------------------------------------------------------
// BuildFunc + BuildOptions
// -----------------------------------------------------------------------------

// BuildFunc 是跳板链构建函数。
//
// 每种"跳板策略"注册一个 BuildFunc；Registry.Build 是它们的薄包装。
// 调用方应负责 Close 返回的 *ssh.Client。
type BuildFunc func(ctx context.Context, opts BuildOptions) (*ssh.Client, error)

// BuildOptions 是 Build 的入参。
//
// v0.6 字段：
//   - Hops：跳板链中除 finalTarget 外的中间跳（顺序）
//   - FinalTarget：最终目标
//   - Dialer：单跳拨号抽象；策略函数用它来连每跳
//   - ProfileResolver：把 ProfileID 解析为具体 Target，由 main.go 注入
//   - HostKeyCallback：跳板 host key 校验回调（与 FinalTarget 共用）
//   - DialTimeout / KeepAlive：透传给 Dialer
//   - Secrets：publickey 路径需要从 secret.Store 拉私钥
type BuildOptions struct {
	// Hops 是跳板链中除 finalTarget 外的中间跳（顺序）。
	Hops []Hop
	// FinalTarget 是最终目标。
	FinalTarget Target
	// Dialer 是单跳拨号抽象（direct / single-jump / multi-hop 都用它）。
	// 必填；nil 时 BuildFunc 返回 error。
	Dialer Dialer
	// ProfileResolver 把 ProfileID 解析为跳板目标信息；
	// 由 main.go / app 包注入（持有 profile 注册表）。
	// v0.1 可为 nil —— direct 策略不需要。
	// single-jump / multi-hop 策略对每个 Hop 调一次 Resolver，nil 时
	// 返回 "profile resolver not configured" 错误。
	ProfileResolver func(profileID string) (HopTarget, bool)
	// HostKeyCallback 是跳板 host key 校验回调；与 FinalTarget 共用。
	// nil 时 Dialer 兜底为 ssh.InsecureIgnoreHostKey（v0.1 行为，⚠️ MITM 风险）。
	// 推荐 v0.6 接入：main.go 注入 knownhosts.Manager.HostKeyCallback()。
	HostKeyCallback connect.HostKeyCallback
	// DialTimeout / KeepAlive 透传给 Dialer。
	DialTimeout time.Duration
	KeepAlive   time.Duration
	// Secrets 用于 publickey auth 时从 secret.Store 拉私钥（agent 路径用）。
	// nil 时 publickey 路径返回明确错误。
	Secrets secret.Store
}

// HopTarget 是从 profile 解析出的跳板目标信息。
type HopTarget struct {
	Host   string
	Port   int
	User   string
	Auth   connect.AuthMethod
	Method MethodKind
}

// -----------------------------------------------------------------------------
// Registry
// -----------------------------------------------------------------------------

// Registry 管理跳板策略（direct / single-jump / multi-hop）。
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
	mu         sync.RWMutex
	strategies map[string]BuildFunc
}

// NewMemoryRegistry 构造一个空 Registry。
//
// v0.1：什么都不预注册 —— 由 main.go / app/wire.go 注入 "direct" +
// "single-jump" 策略（避免 agent 包反向依赖 sshclient）。这样保证 agent 包
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

// -----------------------------------------------------------------------------
// 编译期断言
// -----------------------------------------------------------------------------

var (
	_ Registry = (*MemoryRegistry)(nil)
	_ Client   = (*agentClient)(nil)
	_ Dialer   = DialFunc(nil)
)
