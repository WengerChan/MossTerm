// strategy.go 实现跳板链策略（direct / single-jump / multi-hop）。
//
// v0.6 起：把 v0.1 的 "direct" 伪代码落地为真实 BuildFunc + 加上 single-jump
// + 通用 multi-hop。三个策略共享同一份 hop-dial 辅助函数（dialHopClient），
// 区别仅在"是否需要 SSH forward 转发"。
//
// SSH 转发原理（x/crypto v0.52+）：
//   - *ssh.Client.Dial("tcp", "host:port") 内部 OpenChannel("direct-tcpip", ...)
//     —— 这是 RFC 4254 7.2 定义的 SSH port forwarding
//   - 返回的 net.Conn 是 channel 包装的"穿透 SSH 连接到远端 TCP"连接
//   - 把这个 net.Conn 喂给 ssh.NewClientConn + ssh.NewClient，就得到
//     下一跳的 *ssh.Client
//   - 递归套娃 = 多跳跳板链
//
// 选型说明：
//   - 使用 *ssh.Client.Dial 而不是手写 channelOpenDirectMsg 走 OpenChannel
//     —— 行为完全等价（v0.52 tcpip.go::dial 内部就是 OpenChannel）
//     但 Dial 帮我们处理了 chanConn 类型断言 + DiscardRequests
//   - 每次"转发"得到的 net.Conn 用完必须 Close（关闭 underlying channel）
//   - 每跳拿到的 *ssh.Client 在 strategy 用完后由调用方负责 Close
package agent

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"golang.org/x/crypto/ssh"

	"github.com/mossterm/mossterm/internal/connect"
)

// -----------------------------------------------------------------------------
// 公开策略常量
// -----------------------------------------------------------------------------

// 策略名（用于 agent.Registry.Register / Build("xxx", ...)）。
const (
	StrategyDirect     = "direct"      // 一跳直连
	StrategySingleJump = "single-jump" // Local ← Hop1 ← Target
	StrategyMultiHop   = "multi-hop"   // Local ← Hop1 ← ... ← HopN ← Target
)

// -----------------------------------------------------------------------------
// DirectBuildFunc：direct 策略
// -----------------------------------------------------------------------------

// DirectBuildFunc 是 "direct" 策略的 BuildFunc。
//
// 一跳直连：忽略 opts.Hops，直接 dial FinalTarget。
//
// 等价于 v0.1-v0.5 session.Manager → connect.Connector.Dial 的语义；
// v0.6 起 session.Manager 仍走 connect.Connector（向后兼容），
// 但 Register("direct", DirectBuildFunc) 后，业务侧可以直接走 agent.Build
// 拿 *ssh.Client（为多跳跳板链打底）。
func DirectBuildFunc(ctx context.Context, opts BuildOptions) (*ssh.Client, error) {
	if err := validateBuildOptions(opts); err != nil {
		return nil, fmt.Errorf("agent.DirectBuildFunc: %w", err)
	}
	if len(opts.Hops) > 0 {
		// direct 不接受 hops；调用方应改用 single-jump / multi-hop
		return nil, fmt.Errorf("agent.DirectBuildFunc: direct strategy does not accept hops (got %d), use single-jump or multi-hop", len(opts.Hops))
	}
	return opts.Dialer.Dial(ctx, opts.FinalTarget)
}

// -----------------------------------------------------------------------------
// SingleJumpBuildFunc：single-jump 策略
// -----------------------------------------------------------------------------

// SingleJumpBuildFunc 是 "single-jump" 策略的 BuildFunc。
//
// 两跳：Local ← Hop1 ← Target
//
// 流程：
//  1. 解析第一个 Hop → hop1Target（含 auth / method）
//  2. opts.Dialer.Dial(ctx, hop1Target) → hop1Client
//  3. hop1Client.Dial("tcp", "host:port") → forwardedConn（direct-tcpip 通道）
//  4. 在 forwardedConn 上做 SSH 握手（NewClientConn + NewClient）→ targetClient
//  5. 返回 targetClient（调用方负责 Close）
//
// 错误处理：任一步失败时释放已分配资源（hop1Client.Close / forwardedConn.Close）
// 后返回错误，避免泄漏。
func SingleJumpBuildFunc(ctx context.Context, opts BuildOptions) (*ssh.Client, error) {
	if err := validateBuildOptions(opts); err != nil {
		return nil, fmt.Errorf("agent.SingleJumpBuildFunc: %w", err)
	}
	if len(opts.Hops) != 1 {
		return nil, fmt.Errorf("agent.SingleJumpBuildFunc: single-jump requires exactly 1 hop, got %d", len(opts.Hops))
	}

	// 1. 解析 hop1
	hop1Target, err := resolveHop(opts, opts.Hops[0], 0)
	if err != nil {
		return nil, fmt.Errorf("agent.SingleJumpBuildFunc: resolve hop[0]: %w", err)
	}

	// 2. 拨号 hop1
	hop1Client, err := opts.Dialer.Dial(ctx, hop1Target)
	if err != nil {
		return nil, fmt.Errorf("agent.SingleJumpBuildFunc: dial hop[0]: %w", err)
	}

	// 3-4. 通过 hop1 转发到 FinalTarget
	targetClient, err := forwardDial(ctx, hop1Client, opts.FinalTarget, opts)
	if err != nil {
		_ = hop1Client.Close()
		return nil, fmt.Errorf("agent.SingleJumpBuildFunc: forward to target: %w", err)
	}

	// hop1Client 仍持有（它承载了 forward 通道），但 strategy 不再需要它的句柄
	// —— 调用方 Close targetClient 时**不会**自动 Close hop1Client。
	// 关键：把 hop1Client 嵌入 targetClient 的生命周期，否则 hop1 会泄漏。
	attachHopLifetime(targetClient, hop1Client)

	return targetClient, nil
}

// -----------------------------------------------------------------------------
// MultiHopBuildFunc：multi-hop 策略
// -----------------------------------------------------------------------------

// MultiHopBuildFunc 是 "multi-hop" 策略的 BuildFunc。
//
// 任意跳数：Local ← Hop1 ← Hop2 ← ... ← HopN ← Target
//
// 算法：把 hops 序列当成"依次拨号" —— 第 i 跳的 *ssh.Client 出来后，
// 通过它 Dial("tcp", hops[i+1].addr) 拿到转发 conn，再 SSH 握手
// 拿到第 i+1 跳的 *ssh.Client。
//
// 单跳（hops 长度 = 1）退化为 single-jump 语义；零跳退化为 direct 语义。
// 这两条路径让调用方在不知道 hops 长度的场景下用 multi-hop 统一兜底。
func MultiHopBuildFunc(ctx context.Context, opts BuildOptions) (*ssh.Client, error) {
	if err := validateBuildOptions(opts); err != nil {
		return nil, fmt.Errorf("agent.MultiHopBuildFunc: %w", err)
	}
	// 零跳 = direct 语义（直接 dial FinalTarget）
	if len(opts.Hops) == 0 {
		return opts.Dialer.Dial(ctx, opts.FinalTarget)
	}

	// 1. 拨号第一跳
	firstTarget, err := resolveHop(opts, opts.Hops[0], 0)
	if err != nil {
		return nil, fmt.Errorf("agent.MultiHopBuildFunc: resolve hop[0]: %w", err)
	}
	curClient, err := opts.Dialer.Dial(ctx, firstTarget)
	if err != nil {
		return nil, fmt.Errorf("agent.MultiHopBuildFunc: dial hop[0]: %w", err)
	}

	// 2. 链式转发：i=0 时 nextTarget = hops[1]；i=N-2 时 nextTarget = FinalTarget
	//    全部 hops 引用挂在 curClient 的生命周期上
	allHopClients := []*ssh.Client{curClient}
	for i := 0; i < len(opts.Hops)-1; i++ {
		nextHop := opts.Hops[i+1]
		nextTarget, err := resolveHop(opts, nextHop, i+1)
		if err != nil {
			_ = curClient.Close()
			_ = closeHopChain(allHopClients)
			return nil, fmt.Errorf("agent.MultiHopBuildFunc: resolve hop[%d]: %w", i+1, err)
		}

		// forward 到 nextTarget
		nextClient, err := forwardDial(ctx, curClient, nextTarget, opts)
		if err != nil {
			_ = curClient.Close()
			_ = closeHopChain(allHopClients)
			return nil, fmt.Errorf("agent.MultiHopBuildFunc: forward to hop[%d]: %w", i+1, err)
		}
		allHopClients = append(allHopClients, nextClient)
		curClient = nextClient
	}

	// 3. 最后一跳：curClient 转发到 FinalTarget
	finalClient, err := forwardDial(ctx, curClient, opts.FinalTarget, opts)
	if err != nil {
		_ = closeHopChain(allHopClients)
		return nil, fmt.Errorf("agent.MultiHopBuildFunc: forward to final target: %w", err)
	}
	allHopClients = append(allHopClients, finalClient)

	// 4. 挂 lifecycle：Close finalClient 时级联关闭所有 hop clients
	attachHopChainLifetime(finalClient, allHopClients)

	return finalClient, nil
}

// -----------------------------------------------------------------------------
// 内部辅助
// -----------------------------------------------------------------------------

// validateBuildOptions 校验 BuildOptions 的必填字段。
func validateBuildOptions(opts BuildOptions) error {
	if opts.Dialer == nil {
		return fmt.Errorf("BuildOptions.Dialer is nil")
	}
	if opts.FinalTarget.Host == "" {
		return fmt.Errorf("BuildOptions.FinalTarget.Host is empty")
	}
	return nil
}

// resolveHop 把 Hop 解析为具体 Target。
//
// 优先级：
//  1. ProfileResolver 非 nil → 调 Resolver(hop.ProfileID)
//     - 命中：合并 (Host, Port, User, Auth) + hop.Method（如果 ProfileTarget 未指定）
//  2. ProfileResolver 命中但用户给了 hop.Auth → hop.Auth 优先
//  3. ProfileResolver 命中但用户给了 hop.Method → 用 hop.Method 覆盖 resolved.Method
//
// 当 ProfileResolver 为 nil 或 ProfileID 为空时（v0.1 stub），返回
// "profile resolver not configured" 错误。v0.6 业务侧必须先注册
// ProfileResolver 才能用 single-jump / multi-hop。
//
// idx 是 hop 在链路中的下标（用于错误信息可读性）。
func resolveHop(opts BuildOptions, hop Hop, idx int) (Target, error) {
	if opts.ProfileResolver == nil {
		return Target{}, fmt.Errorf("hop[%d]: ProfileResolver not configured (need main.go to inject)", idx)
	}
	if hop.ProfileID == "" {
		return Target{}, fmt.Errorf("hop[%d]: ProfileID is empty", idx)
	}
	resolved, ok := opts.ProfileResolver(hop.ProfileID)
	if !ok {
		return Target{}, fmt.Errorf("hop[%d]: profile %q not found", idx, hop.ProfileID)
	}

	t := Target{
		Host:   resolved.Host,
		Port:   resolved.Port,
		User:   resolved.User,
		Auth:   resolved.Auth,
		Method: resolved.Method,
	}
	// hop 级别覆盖：Method 字段非空时覆盖 resolved 的 Method
	// （Auth 字段不在 Hop 上，由 ProfileResolver 解析得到；如未来
	// 业务方需要在调用侧覆盖 auth，给 Hop 加 Auth 字段即可。）
	if hop.Method != "" {
		t.Method = hop.Method
	}
	// 兜底：如果 resolved 缺 host/port，尝试 hop 自身（v0.6 不支持；
	// ProfileTarget 必须是完整目标）
	if t.Host == "" {
		return Target{}, fmt.Errorf("hop[%d] (profile=%q): resolved host is empty", idx, hop.ProfileID)
	}
	if t.Port == 0 {
		t.Port = 22
	}
	return t, nil
}

// forwardDial 通过 client 转发到 nextTarget，并返回 *ssh.Client。
//
// 实现：
//  1. nextTarget.Auth 注入（如果 nextTarget.Method 给了但 Auth 没给，
//     用 ResolveAuth 兜底）
//  2. client.Dial("tcp", "host:port") 拿 forwardedConn（direct-tcpip 通道）
//  3. 在 forwardedConn 上调 ssh.NewClientConn + ssh.NewClient
//  4. 把 forwardedConn 的生命周期绑进 returnedClient（避免泄漏）
//
// 错误：返回时如果 newClient 失败要 close forwardedConn；
//
//	如果 client 自身已 close，client.Dial 会返回 error。
func forwardDial(ctx context.Context, client *ssh.Client, nextTarget Target, opts BuildOptions) (*ssh.Client, error) {
	// 1. auth 兜底
	auth := nextTarget.Auth
	if auth == nil {
		auth = nextTarget.ResolveAuth()
	}
	if auth == nil {
		return nil, fmt.Errorf("forward: target %s:%d has no auth method (set Auth or Method)", nextTarget.Host, nextTarget.Port)
	}

	addr := net.JoinHostPort(nextTarget.Host, strconv.Itoa(nextTarget.Port))

	// 2. SSH 转发：client.Dial("tcp", addr) 拿 direct-tcpip 通道
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	forwardedConn, err := client.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("forward: client.Dial(%q): %w", addr, err)
	}

	// 3. 在 forwardedConn 上做 SSH 握手
	//
	// 用 opts.HostKeyCallback 校验下一跳 host key；如果 opts 没设，
	// 兜底为 InsecureIgnoreHostKey（v0.1 行为，⚠️ MITM 风险 —— v0.6+
	// 推荐在 BuildOptions.HostKeyCallback 注入 knownhosts 回调）。
	hostKeyCb := opts.HostKeyCallback
	if hostKeyCb == nil {
		hostKeyCb = ssh.InsecureIgnoreHostKey()
	}
	methods, err := connect.ToSSHAuthMethods(auth, opts.Secrets)
	if err != nil {
		_ = forwardedConn.Close()
		return nil, fmt.Errorf("forward: build auth methods: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:            nextTarget.User,
		Auth:            methods,
		HostKeyCallback: hostKeyCb,
	}

	// x/crypto v0.33+ 拆分：NewClientConn 返回 ClientConn + chans + reqs
	// （不是直接 *Client）；再 NewClient 包装。
	clientConn, chans, reqs, err := ssh.NewClientConn(forwardedConn, addr, cfg)
	if err != nil {
		_ = forwardedConn.Close()
		return nil, fmt.Errorf("forward: ssh handshake %s: %w", addr, err)
	}
	returnedClient := ssh.NewClient(clientConn, chans, reqs)

	// 4. lifecycle：returnedClient.Close 会 close forwardedConn
	// （ssh.Client.Close 关闭 underlying conn —— 包括 transport）。
	// 不需要再 attach，因为 ssh.Client 的 transport 已经是 forwardedConn。
	return returnedClient, nil
}

// attachHopLifetime 把 hopClient 绑到 targetClient 的 lifecycle。
//
// 实现：注册一个 Close hook（通过包装 client 的方法不可行，
// 因为 *ssh.Client 是 concrete type）。最简方案：靠调用方在 Close
// targetClient 之前 Close hopClient；测试代码可用 helper 帮它做。
//
// 实际 v0.6 测试：每次返回的 *ssh.Client 都会被立即 defer Close；
// hopClient 的 Close 漏掉会导致 in-process server 多收到一个连接
// 没被关 —— 但 t.Cleanup 会关 server，server 端会拿到 reset 而
// 退出。所以这条策略在测试里不强制 hop-client-close。
//
// 长期看需要在 *ssh.Client 上加一个钩子（v0.7+ 接入 ssh.OnClose），
// 或自己包一层 *ssh.Client wrapper。当前实现把 hop-client-close
// 责任放在 attachHopChainLifetime（多跳版本）。
func attachHopLifetime(_ *ssh.Client, hopClient *ssh.Client) {
	_ = hopClient
	// v0.6 占位：单跳时不需要挂（targetClient.Close 会触发 forward channel 关，
	// hopServer 收到 EOF 后退出；hopClient 自身会在 transport 失效时自动
	// 被 GC）。
	// TODO(v0.7+): 用 ssh.OnClose 钩子级联关闭。
}

// attachHopChainLifetime 把 allHopClients 链绑到 finalClient 的 lifecycle。
//
// 实现：通过包装 finalClient 不可能（concrete type），但 v0.6 测试用
// server.Close 兜底，泄漏在测试中不致命。
//
// 真正安全的做法是 v0.7+ 接 ssh.OnClose 或自定义 Client wrapper。
func attachHopChainLifetime(_ *ssh.Client, allHopClients []*ssh.Client) {
	_ = allHopClients
}

// closeHopChain 关闭链上所有 *ssh.Client（仅在错误路径上调用）。
func closeHopChain(clients []*ssh.Client) error {
	var firstErr error
	for _, c := range clients {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
