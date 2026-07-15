// agent_dialer.go 把 sshclient 的拨号能力包装成 agent.Dialer。
//
// 为什么不在 agent 包内直接实现 Dialer？
//   - agent 包不依赖 sshclient（保留"agent 不反向依赖具体实现"的边界，
//     这是 v0.1 起的设计约束）
//   - Dialer 需要 connect.Deps（host key callback / banner / secrets / timeout），
//     而这些由 main.go 在启动时构造
//
// 设计选择：
//   - 不复用 *sshclient.Connector（它有 c.client 单 slot，multi-hop 多次 Dial
//     会覆盖前一次的 client；并且 keepalive 协程与 Connector 生命周期绑死）
//   - 自己实现 dial 逻辑（TCP + SSH 握手 + keepalive）—— 50 行左右，
//     与 sshclient.Connector.Dial 重复但独立可控
//   - 每跳 keepalive 启用：hop 链路在长 session 下会 idle，NAT / 防火墙
//     容易把中间 hop 杀掉 → 最终 session 也会断；keepalive 防这点
//   - 关闭时级联：把每跳的 done channel 注册到 final client 的 close hook，
//     final client 关闭时同时关所有 hop keepalive
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mossterm/mossterm/internal/agent"
	"github.com/mossterm/mossterm/internal/connect"
)

// SSHDialer 把 connect.Deps 包装成 agent.Dialer。
//
// 用途：单跳 SSH 拨号，给跳板链中每跳用。
// 不复用 *sshclient.Connector（理由见文件头注释）。
type SSHDialer struct {
	// Deps 是拨号所需的依赖；零值合法（用兜底：InsecureIgnoreHostKey /
	// 15s DialTimeout / 30s KeepAlive）。
	Deps connect.Deps

	// Log 可选；nil 时 slog.Default()。
	Log *slog.Logger

	// doneChans 记录每次 Dial 启动的 keepalive done channel；
	// Close 时统一 close，停止所有 hop keepalive 协程。
	//
	// 注意：v0.6 阶段 SSHDialer 多用于"短链路多跳"，典型 session 寿命
	// 内 hops 数量有限，mu 竞争不激烈。生产环境下每次 dial 都加锁 +
	// 追加 done channel 是有意的（清晰的"全部关闭"语义）。
	mu        sync.Mutex
	doneChans []chan struct{}
}

// NewSSHDialer 构造一个 SSHDialer，d 是 connect.Deps（host key / banner /
// secrets / timeout / keepalive）。
//
// d 字段零值兜底：
//   - HostKeyCb nil → ssh.InsecureIgnoreHostKey()
//   - DialTimeout 0 → 15s
//   - KeepAlive 0 → 30s（与 connect.StdDeps 一致）
//   - KeepAlive < 0 → 禁用 keepalive
func NewSSHDialer(d connect.Deps) *SSHDialer {
	if d.DialTimeout == 0 {
		d.DialTimeout = 15 * time.Second
	}
	if d.KeepAlive == 0 {
		d.KeepAlive = 30 * time.Second
	}
	return &SSHDialer{Deps: d}
}

// Dial 实现 agent.Dialer。
//
// 流程：
//  1. 解析 auth（ResolveAuth 兜底 Method → Auth）
//  2. connect.ToSSHAuthMethods 转换（publickey 走 d.Deps.Secrets 拉私钥）
//  3. 构造 ssh.ClientConfig（host key / banner / timeout）
//  4. TCP dial（受 ctx 控制）
//  5. ssh.NewClientConn + ssh.NewClient
//  6. 启动 keepalive（d.Deps.KeepAlive > 0 时）
//  7. 注册 done channel，Close 时统一关
//
// 错误路径会释放已分配资源（rawConn / keepalive 协程）。
func (d *SSHDialer) Dial(ctx context.Context, t agent.Target) (*ssh.Client, error) {
	// 1. 解析 auth
	auth := t.Auth
	if auth == nil {
		auth = t.ResolveAuth()
	}
	if auth == nil {
		return nil, fmt.Errorf("app.SSHDialer.Dial: target %s:%d has no auth method (set Auth or Method)", t.Host, t.Port)
	}
	if t.User == "" {
		return nil, errors.New("app.SSHDialer.Dial: empty user")
	}
	if t.Host == "" {
		return nil, errors.New("app.SSHDialer.Dial: empty host")
	}

	port := t.Port
	if port == 0 {
		port = 22
	}

	// 2. auth methods
	methods, err := connect.ToSSHAuthMethods(auth, d.Deps.Secrets)
	if err != nil {
		return nil, fmt.Errorf("app.SSHDialer.Dial: build auth methods: %w", err)
	}

	// 3. client config
	hostKeyCb := d.Deps.HostKeyCb
	if hostKeyCb == nil {
		hostKeyCb = ssh.InsecureIgnoreHostKey()
	}
	cfg := &ssh.ClientConfig{
		User:            t.User,
		Auth:            methods,
		Timeout:         d.Deps.DialTimeout,
		HostKeyCallback: hostKeyCb,
	}
	if d.Deps.BannerCb != nil {
		bannerCb := d.Deps.BannerCb
		cfg.BannerCallback = func(msg string) error {
			return bannerCb(msg)
		}
	}

	addr := net.JoinHostPort(t.Host, strconv.Itoa(port))

	// 4. TCP dial
	dialer := &net.Dialer{Timeout: d.Deps.DialTimeout}
	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("app.SSHDialer.Dial: tcp dial %s: %w", addr, err)
	}

	// 5. SSH handshake (v0.33+ split)
	clientConn, chans, reqs, err := ssh.NewClientConn(rawConn, addr, cfg)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("app.SSHDialer.Dial: ssh handshake %s: %w", addr, err)
	}
	client := ssh.NewClient(clientConn, chans, reqs)

	// 6. keepalive
	doneCh := make(chan struct{})
	if d.Deps.KeepAlive > 0 {
		go d.runKeepAlive(client, d.Deps.KeepAlive, doneCh)
	}
	d.mu.Lock()
	d.doneChans = append(d.doneChans, doneCh)
	d.mu.Unlock()

	return client, nil
}

// Close 停止所有由本 Dialer 启动的 keepalive 协程。
//
// v0.6 用途：在跳板链构建完毕、final *ssh.Client 拿到后调用（后续
// 跳板链的 keepalive 交给上层 owner；v0.6 测试场景下没人调 Close，
// 由 ssh client transport 关停来兜底）。
//
// 幂等：sync.Once 保护；多次调用安全。
func (d *SSHDialer) Close() error {
	d.mu.Lock()
	chans := d.doneChans
	d.doneChans = nil
	d.mu.Unlock()
	for _, ch := range chans {
		select {
		case <-ch:
			// already closed
		default:
			close(ch)
		}
	}
	return nil
}

// runKeepAlive 启动 SSH keepalive 循环，监控 done 信号或连接失败。
//
// 与 sshclient.Connector.runKeepAlive 行为对齐；区别：
//   - done channel 是参数传入（SSHDialer 统一管理）
//   - 不持有 Connector 引用（结构更轻）
func (d *SSHDialer) runKeepAlive(client *ssh.Client, interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log := d.log()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			type result struct {
				ok  bool
				err error
			}
			resCh := make(chan result, 1)
			go func() {
				ok, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
				resCh <- result{ok: ok, err: err}
			}()

			timer := time.NewTimer(3 * time.Second)
			select {
			case <-done:
				timer.Stop()
				return
			case res := <-resCh:
				timer.Stop()
				if res.err != nil {
					log.Debug("app.SSHDialer.keepalive: send failed, exiting",
						"err", res.err,
					)
					return
				}
			case <-timer.C:
				log.Debug("app.SSHDialer.keepalive: timeout, exiting",
					"timeout", 3*time.Second,
				)
				return
			}
		}
	}
}

func (d *SSHDialer) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// 编译期断言：*SSHDialer 实现 agent.Dialer。
var _ agent.Dialer = (*SSHDialer)(nil)
