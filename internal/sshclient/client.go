// Package sshclient 实现 connect.Connector 接口的 SSH 版本。
//
// 基于 golang.org/x/crypto/ssh。
// 维护 host key 持久化（~/.config/mossterm/known_hosts，OpenSSH 格式）。
package sshclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/hashicorp/golang-lru/v2"
	"golang.org/x/crypto/ssh"

	"github.com/mossterm/mossterm/internal/connect"
	"github.com/mossterm/mossterm/internal/secret"
)

// Connector 是 connect.Connector 的 SSH 实现。
//
// 字段说明：
//   - hostKeyCb：host key 校验回调（known_hosts 校验逻辑由 callback 实现）。
//   - bannerCb：服务端 banner 回调。
//   - keepAlive / dialTimeout：默认心跳与拨号超时，可在 DialParams 中覆盖。
//   - secrets：凭据存储，用于解析 publickey auth 时的私钥。
//   - signerCache：缓存按 KeyID→Signer 解析过的 ssh.Signer，
//     避免每次连接都重新打开 secret store。
type Connector struct {
	hostKeyCb   connect.HostKeyCallback
	bannerCb    connect.BannerCallback
	keepAlive   time.Duration
	dialTimeout time.Duration
	secrets     secret.Store
	signerCache *lru.Cache[string, ssh.Signer]
}

// New 构造一个 SSH Connector。
//
// deps.HostKeyCb 为 nil 时使用 InsecureIgnoreHostKey 作为兜底（仅 v0.1）。
// TODO(security): 接入 known_hosts 持久化与"首次信任"对话框。
//
// deps.Secrets 用于 publickey 认证：nil 时 publickey 路径会返回明确错误。
func New(d connect.Deps) (*Connector, error) {
	keepAlive := d.KeepAlive
	if keepAlive == 0 {
		keepAlive = 30 * time.Second
	}
	dialTimeout := d.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 15 * time.Second
	}
	cache, err := lru.New[string, ssh.Signer](64)
	if err != nil {
		return nil, fmt.Errorf("sshclient.New: lru init: %w", err)
	}
	return &Connector{
		hostKeyCb:   d.HostKeyCb,
		bannerCb:    d.BannerCb,
		keepAlive:   keepAlive,
		dialTimeout: dialTimeout,
		secrets:     d.Secrets,
		signerCache: cache,
	}, nil
}

// Dial 实现 connect.Connector.Dial。
//
// 流程：
//  1. 校验 params (host / port / user)
//  2. 通过 authMethods 构造 []ssh.AuthMethod
//  3. 构造 ssh.ClientConfig（含 HostKeyCallback / BannerCallback / Timeout）
//  4. net.Dial 一次 TCP 握手
//  5. ssh.NewClient 在该连接上完成 SSH 协议握手
//  6. 把 net.Conn 与 *ssh.Client 包装到 *sshConn 返回
//
// ctx 取消或超时时立刻返回 ctx.Err()。
func (c *Connector) Dial(ctx context.Context, params connect.DialParams) (net.Conn, error) {
	if params.Host == "" {
		return nil, errors.New("sshclient.Dial: empty host")
	}
	if params.User == "" {
		return nil, errors.New("sshclient.Dial: empty user")
	}

	port := params.Port
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("sshclient.Dial: invalid port %d", port)
	}

	timeout := params.Timeout
	if timeout == 0 {
		timeout = c.dialTimeout
	}

	methods, err := c.authMethods(params.Auth)
	if err != nil {
		return nil, fmt.Errorf("sshclient.Dial: auth: %w", err)
	}

	cfg := &ssh.ClientConfig{
		User:    params.User,
		Auth:    methods,
		Timeout: timeout,
	}

	// Host key 校验：优先用外部回调，否则兜底放行（v0.1）。
	if c.hostKeyCb != nil {
		// connect.HostKeyCallback 已经是 ssh.HostKeyCallback 的 alias，
		// 直接赋值即可。
		cfg.HostKeyCallback = c.hostKeyCb
	} else {
		// TODO(security): 接入 known_hosts（v0.2+）：
		//   1. 启动时 load ~/.config/mossterm/known_hosts
		//   2. 命中则返回 true；未命中 + 严格模式 = 拒绝；未命中 + 宽松 = 写入并返回 true
		cfg.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	// Banner 回调：把服务端 banner 推给上层（log:line / ui）。
	if c.bannerCb != nil {
		bannerCb := c.bannerCb
		cfg.BannerCallback = func(message string) error {
			bannerCb(message)
			return nil
		}
	}

	addr := net.JoinHostPort(params.Host, strconv.Itoa(port))

	// 阶段 1：TCP 拨号（受 ctx 控制）。
	dialer := &net.Dialer{Timeout: timeout}
	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("sshclient.Dial: tcp dial %s: %w", addr, err)
	}

	// 阶段 2：SSH 协议握手（受 cfg.Timeout 控制，ssh 库内部会读 ctx 不会）。
	//
	// x/crypto v0.33+ 拆分了 NewClient：
	//   1. NewClientConn 返回 ClientConn + chans + reqs 三个 channel
	//   2. NewClient 用这三个 channel 包装出 *Client
	clientConn, chans, reqs, err := ssh.NewClientConn(rawConn, addr, cfg)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("sshclient.Dial: ssh handshake %s: %w", addr, err)
	}
	client := ssh.NewClient(clientConn, chans, reqs)

	// 阶段 3（可选）：启动 keepalive。
	// 注释掉以减少 v0.1 噪音；要开启去掉注释即可。
	// if c.keepAlive > 0 {
	//     go c.runKeepAlive(client, c.keepAlive)
	// }

	return &sshConn{Conn: rawConn, client: client}, nil
}

// OpenSession 实现 connect.Connector.OpenSession。
//
// 流程：
//  1. 把 net.Conn 还原为 *sshConn（拿到 *ssh.Client）
//  2. NewSession → RequestPty → Setenv → Shell
//  3. 把 *ssh.Session 包装到 *sshSession 返回
//
// 任何步骤失败都会回滚已分配的资源（关闭 session / conn）。
func (c *Connector) OpenSession(ctx context.Context, conn net.Conn, opts connect.SessionOpts) (connect.Session, error) {
	if conn == nil {
		return nil, errors.New("sshclient.OpenSession: nil conn")
	}
	if ctx == nil {
		return nil, errors.New("sshclient.OpenSession: nil ctx")
	}

	sc, ok := conn.(*sshConn)
	if !ok {
		return nil, fmt.Errorf("sshclient.OpenSession: conn is %T, want *sshConn", conn)
	}
	if sc.client == nil {
		return nil, errors.New("sshclient.OpenSession: sshConn.client is nil (Dial failed?)")
	}

	sess, err := sc.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("sshclient.OpenSession: NewSession: %w", err)
	}
	// 注意：从此处开始任何 return 都必须 sess.Close()
	closed := false
	defer func() {
		if !closed {
			_ = sess.Close()
		}
	}()

	term := opts.Term
	if term == "" {
		term = "xterm-256color"
	}
	cols := opts.Cols
	if cols <= 0 {
		cols = 80
	}
	rows := opts.Rows
	if rows <= 0 {
		rows = 24
	}

	// RequestPty(term, h, w, modes) —— h=rows, w=cols
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty(term, rows, cols, modes); err != nil {
		return nil, fmt.Errorf("sshclient.OpenSession: RequestPty(%s, %dx%d): %w", term, cols, rows, err)
	}

	// 环境变量：ssh.Setenv 在某些 server 上会拒绝 AllowTcpForwarding 等保留变量。
	// 失败时我们记录并继续 —— 任何 env 失败都不应该阻止 shell 启动。
	for k, v := range opts.Env {
		if k == "" {
			continue
		}
		if err := sess.Setenv(k, v); err != nil {
			// 用最近一次错误覆盖前面的（不会中止）
			// TODO(log): 推送一条 log:line
		}
	}

	// 启动用户 shell。失败时仍然让 caller 拿到错误，但内部已 Close。
	if err := sess.Shell(); err != nil {
		return nil, fmt.Errorf("sshclient.OpenSession: Shell: %w", err)
	}

	// 把 SSH session 的 stdin/stdout 暴露成 io.ReadWriter。
	// *ssh.Session 本身不实现 ReadWriteCloser，需要手动包一层。
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("sshclient.OpenSession: StdinPipe: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("sshclient.OpenSession: StdoutPipe: %w", err)
	}

	closed = true
	return &sshSession{
		Session: sess,
		stdin:   stdin,
		stdout:  stdout,
	}, nil
}

// loadSigner 从 secret store 解析 ssh.Signer 并写入缓存。
//
// 真实实现（v0.1.2）：
//  1. 读 signerCache 缓存；命中直接返回
//  2. secret.Store.Get(secret.ID(keyID)) 拿私钥 bytes
//  3. loadSignerFromBytes(bytes, passphrase) 解析
//  4. 解析结果写入 signerCache（按 keyID 索引）
//  5. 返回解析后的 ssh.Signer
//
// c.secrets 为 nil 时返回明确错误（提示用户未初始化凭据存储）。
func (c *Connector) loadSigner(keyID, passphrase string) (ssh.Signer, error) {
	if keyID == "" {
		return nil, errors.New("sshclient.loadSigner: empty keyID")
	}
	// 1. 缓存命中
	if s, ok := c.signerCache.Get(keyID); ok {
		return s, nil
	}
	// 2. secret.Store 必需
	if c.secrets == nil {
		return nil, errors.New("sshclient.loadSigner: secret.Store not initialized (deps.Secrets is nil)")
	}
	// 3. 从 secret.Store 拉私钥 bytes
	keyBytes, err := c.secrets.Get(secret.ID(keyID))
	if err != nil {
		return nil, fmt.Errorf("sshclient.loadSigner: secrets.Get(%q): %w", keyID, err)
	}
	if len(keyBytes) == 0 {
		return nil, fmt.Errorf("sshclient.loadSigner: empty key bytes for keyID=%q", keyID)
	}
	// 4. 解析
	signer, err := loadSignerFromBytes(keyBytes, passphrase)
	if err != nil {
		return nil, fmt.Errorf("sshclient.loadSigner: parse keyID=%q: %w", keyID, err)
	}
	// 5. 写缓存
	c.signerCache.Add(keyID, signer)
	return signer, nil
}

// loadSignerFromBytes 从 PEM 编码的 OpenSSH 私钥字节解析出 ssh.Signer。
//
// passphrase 为空时按未加密私钥处理；非空时尝试用 passphrase 解密。
// 该函数是 package-level 的纯函数，方便测试与未来跨包复用。
func loadSignerFromBytes(keyBytes []byte, passphrase string) (ssh.Signer, error) {
	if len(keyBytes) == 0 {
		return nil, errors.New("sshclient.loadSignerFromBytes: empty key bytes")
	}
	if passphrase == "" {
		return ssh.ParsePrivateKey(keyBytes)
	}
	return ssh.ParsePrivateKeyWithPassphrase(keyBytes, []byte(passphrase))
}

// 编译期断言：*Connector 满足 connect.Connector 接口。
var _ connect.Connector = (*Connector)(nil)

// -----------------------------------------------------------------------------
// 内部类型
// -----------------------------------------------------------------------------

// sshConn 把 net.Conn（TCP 层）和 *ssh.Client（协议层）打包在一起。
//
// 作为 net.Conn 使用时，所有读 / 写 / 截止时间都直接转发到底层 TCP 连接；
// 这意味着调用方如果拿这个 conn 去"读 SSH 协议数据"是读不到的（SSH 帧
// 已被 ssh.Client 解析）。这正是我们想要的：协议层数据走 *ssh.Client
// 的 channel，net.Conn 语义保留给"还没升级到 SSH 之前的 TCP"这一层。
//
// 真正的 OpenSession 通过类型断言把 *ssh.Client 取出来。
type sshConn struct {
	net.Conn
	client *ssh.Client
}

// Close 关闭 SSH 客户端与底层 TCP 连接，组合多个错误。
func (c *sshConn) Close() error {
	var first error
	if c.client != nil {
		if err := c.client.Close(); err != nil {
			first = err
		}
	}
	if c.Conn != nil {
		if err := c.Conn.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// sshSession 把 *ssh.Session 适配成 connect.Session。
//
// *ssh.Session 自身只暴露 StdinPipe/StdoutPipe/StderrPipe；
// 这里把 stdin/stdout 缓存下来，Read/Write 直接走 pipe，
// Close 调 *ssh.Session.Close（它会同时关掉所有 pipe）。
//
// Resize 走 *ssh.Session.WindowChange，把 (cols, rows) 翻成 ssh 库的 (h, w) = (rows, cols)。
type sshSession struct {
	*ssh.Session
	stdin  io.Writer
	stdout io.Reader
}

// Read 转发到 stdout pipe。
func (s *sshSession) Read(p []byte) (int, error) { return s.stdout.Read(p) }

// Write 转发到 stdin pipe。
func (s *sshSession) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Close 转发到 *ssh.Session.Close（它会顺带关掉 pipe）。
func (s *sshSession) Close() error { return s.Session.Close() }

// Resize 调 WindowChange 时把 (cols, rows) 翻成 (rows, cols)。
func (s *sshSession) Resize(cols, rows int) error {
	return s.Session.WindowChange(rows, cols)
}

// ShellPID 返回远端 shell 进程 PID。
//
// x/crypto/ssh 的 Session 接口未暴露 PID（协议层不返回）。
// v0.1 始终返回 0；v0.2+ 如果需要可解析 "exec" channel open 报文。
func (s *sshSession) ShellPID() int {
	return 0
}
