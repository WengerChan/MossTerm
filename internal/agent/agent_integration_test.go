// agent_integration_test.go 端到端覆盖 internal/agent 跳板链（v0.6）。
//
// 测试基础设施：
//   - hopServer：in-process SSH server，支持"session" + "direct-tcpip"
//     两种 channel。direct-tcpip 收到后通过 net.Dial 转发到指定 target，
//     模拟真实跳板机的"把 SSH 通道再开一条 TCP 出去"行为
//   - pickyServer：密码错就 auth fail 的 server（测错误路径用）
//   - testDialer：内部 net.Dial + SSH 握手的 Dialer（与 app.SSHDialer 行为对齐）
//
// 覆盖的策略：
//   - "direct"     —— 一跳直连到 target
//   - "single-jump" —— Local ← Hop1 ← Target（hop1 用 direct-tcpip 转发）
//   - "multi-hop"   —— Local ← Hop1 ← Hop2 ← Target（3 跳）
//
// 覆盖的错误路径：
//   - hop 密码错 → error
//   - profile 找不到 → error
//   - nil Dialer → error
//   - nil ProfileResolver + single-jump → error
//   - multi-hop 零跳 → 退化为 direct
package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mossterm/mossterm/internal/connect"
	"github.com/mossterm/mossterm/internal/secret"
)

// passwordAuth 是 connect.PasswordAuth 的简写（测试用）。
func passwordAuth(p string) connect.AuthMethod { return connect.PasswordAuth(p) }

// -----------------------------------------------------------------------------
// 1. hopServer：支持 direct-tcpip 转发的 in-process SSH server
// -----------------------------------------------------------------------------

// hopServer 是绑 127.0.0.1:0 上的最小化 SSH server，可作为跳板链中的一跳。
//
// 行为：
//   - 接受任何用户名/密码（test 简化）
//   - 接受 "session" 通道：仅作为保活（多跳测试不依赖 shell）
//   - 接受 "direct-tcpip" 通道：把请求的 host:port 转发到 s.forwardAddr
//     （典型为下一个 hopServer 或目标 server）
type hopServer struct {
	listener  net.Listener
	serverCfg *ssh.ServerConfig

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once

	// forwardAddr 是 direct-tcpip 转发的目标地址。
	forwardMu   sync.RWMutex
	forwardAddr string

	// t 用于把 server 内部错误通过 t.Log 报告
	t *testing.T
}

// newHopServer 构造一个 hopServer 并注册 t.Cleanup。
func newHopServer(t *testing.T) *hopServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}

	serverCfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	serverCfg.AddHostKey(signer)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &hopServer{
		listener:  l,
		serverCfg: serverCfg,
		ctx:       ctx,
		cancel:    cancel,
		t:         t,
	}
	s.wg.Add(1)
	go s.acceptLoop()

	t.Cleanup(s.Close)
	return s
}

// SetForwardAddr 设置 direct-tcpip 转发的目标地址。
func (s *hopServer) SetForwardAddr(addr string) {
	s.forwardMu.Lock()
	s.forwardAddr = addr
	s.forwardMu.Unlock()
}

func (s *hopServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.handleConn(c)
		}(conn)
	}
}

func (s *hopServer) handleConn(c net.Conn) {
	defer c.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(c, s.serverCfg)
	if err != nil {
		return
	}
	defer sconn.Close()

	var connWg sync.WaitGroup
	defer connWg.Wait()

	// ctx 取消 → sconn.Close() → sconn.Wait() 返回
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		<-s.ctx.Done()
		sconn.Close()
	}()

	// 全局请求：全部回复成功
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		for req := range reqs {
			if req.WantReply {
				req.Reply(true, nil)
			}
		}
	}()

	// channel 分发
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		for newChan := range chans {
			switch newChan.ChannelType() {
			case "session":
				ch, requests, err := newChan.Accept()
				if err != nil {
					continue
				}
				connWg.Add(1)
				go func() {
					defer connWg.Done()
					s.handleSession(ch, requests)
				}()
			case "direct-tcpip":
				connWg.Add(1)
				go func() {
					defer connWg.Done()
					s.handleDirectTCPIP(newChan)
				}()
			default:
				_ = newChan.Reject(ssh.UnknownChannelType, "only session / direct-tcpip supported")
			}
		}
	}()

	_ = sconn.Wait()
}

// handleSession 是单个 session channel 的请求分发。
//
// hopServer 自身不真正支持 shell（多跳测试不依赖 shell，只依赖
// forward channel）；这里只接受所有请求并保持 channel 开放。
func (s *hopServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		if req.WantReply {
			req.Reply(true, nil)
		}
	}
}

// handleDirectTCPIP 处理 direct-tcpip 通道：双向转发 client ↔ upstream。
//
// 流程：
//  1. Accept SSH channel
//  2. net.Dial("tcp", forward) 拿 upstream
//  3. 起两个 io.Copy goroutine 做双向数据转发
//  4. 等任一端返回 → 关闭 channel + upstream → 另一端 io.Copy 自然退出
//
// 关键：不设 SetReadDeadline —— 测试中 forward 之后的 SSH 握手可能耗时
// 数秒（TCP dial + key exchange），设短 deadline 会让握手中途 EOF。
func (s *hopServer) handleDirectTCPIP(newChan ssh.NewChannel) {
	s.forwardMu.RLock()
	forward := s.forwardAddr
	s.forwardMu.RUnlock()
	if forward == "" {
		_ = newChan.Reject(ssh.ConnectionFailed, "forward not configured")
		return
	}

	ch, requests, err := newChan.Accept()
	if err != nil {
		return
	}
	go ssh.DiscardRequests(requests)

	upstream, err := net.Dial("tcp", forward)
	if err != nil {
		s.t.Logf("hopServer.handleDirectTCPIP: dial %s: %v", forward, err)
		_ = ch.Close()
		return
	}

	// errCh 用于检测任一端 io.Copy 错误（任意一边先关会触发）
	errCh := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, ch)
		errCh <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(ch, upstream)
		errCh <- struct{}{}
	}()

	// 等任一端先关/出错 → 关闭两端让另一边 io.Copy 自然退出
	<-errCh
	_ = upstream.Close()
	_ = ch.Close()
	<-errCh // 等另一边也退出
}

func (s *hopServer) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.listener.Close()
		s.wg.Wait()
	})
}

func (s *hopServer) hostPort() string {
	return s.listener.Addr().String()
}

func (s *hopServer) hostAndPort(t *testing.T) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.hostPort())
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", s.hostPort(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", portStr, err)
	}
	return host, port
}

// -----------------------------------------------------------------------------
// 2. pickyServer：只接受特定密码
// -----------------------------------------------------------------------------

// pickyServer 在认证阶段拒绝错误密码。
type pickyServer struct {
	listener  net.Listener
	serverCfg *ssh.ServerConfig
	correct   string
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

func newPickyServer(t *testing.T, correctPassword string) *pickyServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}
	pw := correctPassword
	serverCfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == pw {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("pickyServer: wrong password")
		},
	}
	serverCfg.AddHostKey(signer)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &pickyServer{
		listener:  l,
		serverCfg: serverCfg,
		correct:   correctPassword,
		ctx:       ctx,
		cancel:    cancel,
	}
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

func (s *pickyServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer c.Close()
			sconn, chans, reqs, err := ssh.NewServerConn(c, s.serverCfg)
			if err != nil {
				return
			}
			defer sconn.Close()
			go ssh.DiscardRequests(reqs)
			for ch := range chans {
				_ = ch.Reject(ssh.UnknownChannelType, "pickyServer: no channels supported")
			}
		}(conn)
	}
}

func (s *pickyServer) hostPort() string {
	return s.listener.Addr().String()
}

func (s *pickyServer) hostAndPort(t *testing.T) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.hostPort())
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", s.hostPort(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", portStr, err)
	}
	return host, port
}

func (s *pickyServer) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.listener.Close()
		s.wg.Wait()
	})
}

// -----------------------------------------------------------------------------
// 3. testDialer
// -----------------------------------------------------------------------------

// testDialer 走 net.Dial + SSH 握手（与 app.SSHDialer 行为对齐）。
//
// v0.6 测试只覆盖 password auth（agent / publickey 留给后续 milestone）。
// secrets 永远 nil（v0.6 跳板测试场景下不会用 publickey）。
type testDialer struct {
	secrets secret.Store
}

func newTestDialer() *testDialer {
	return &testDialer{}
}

// Dial 实现 agent.Dialer。
func (d *testDialer) Dial(ctx context.Context, target Target) (*ssh.Client, error) {
	if target.User == "" || target.Host == "" {
		return nil, fmt.Errorf("testDialer.Dial: empty user/host")
	}

	port := target.Port
	if port == 0 {
		port = 22
	}

	auth := target.Auth
	if auth == nil {
		auth = target.ResolveAuth()
	}
	if auth == nil {
		return nil, fmt.Errorf("testDialer.Dial: target %s:%d has no auth", target.Host, target.Port)
	}

	methods, err := connect.ToSSHAuthMethods(auth, d.secrets)
	if err != nil {
		return nil, fmt.Errorf("testDialer.Dial: build auth methods: %w", err)
	}

	cfg := &ssh.ClientConfig{
		User:            target.User,
		Auth:            methods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	addr := net.JoinHostPort(target.Host, strconv.Itoa(port))

	rawConn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("testDialer.Dial: tcp dial %s: %w", addr, err)
	}

	clientConn, chans, reqs, err := ssh.NewClientConn(rawConn, addr, cfg)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("testDialer.Dial: ssh handshake %s: %w", addr, err)
	}
	return ssh.NewClient(clientConn, chans, reqs), nil
}

// -----------------------------------------------------------------------------
// 4. 测试用例
// -----------------------------------------------------------------------------

// TestStrategy_Direct 验证 "direct" 策略能 dial 到单个目标。
func TestStrategy_Direct(t *testing.T) {
	srv := newHopServer(t)
	host, port := srv.hostAndPort(t)
	srv.SetForwardAddr(srv.hostPort()) // direct 不走 forward，但 hopServer 接受 direct-tcpip 时需要 forwardAddr 非空

	dialer := newTestDialer()
	target := Target{
		Host: host,
		Port: port,
		User: "test",
		Auth: passwordAuth("hunter2"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DirectBuildFunc(ctx, BuildOptions{
		Dialer:      dialer,
		FinalTarget: target,
	})
	if err != nil {
		t.Fatalf("DirectBuildFunc: %v", err)
	}
	defer client.Close()

	// sanity：能开 session = SSH 握手成功
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("client.NewSession: %v", err)
	}
	_ = sess.Close()
}

// TestStrategy_SingleJump 验证 "single-jump" 策略。
//
// Local ← Hop1 ← Target
func TestStrategy_SingleJump(t *testing.T) {
	target := newHopServer(t)
	thost, tport := target.hostAndPort(t)
	target.SetForwardAddr(target.hostPort())

	hop1 := newHopServer(t)
	h1host, h1port, err := splitHostPort(hop1.hostPort())
	if err != nil {
		t.Fatalf("splitHostPort hop1: %v", err)
	}
	hop1.SetForwardAddr(target.hostPort())

	dialer := newTestDialer()
	profiles := map[string]HopTarget{
		"hop1": {Host: h1host, Port: h1port, User: "hop1-user", Auth: passwordAuth("hop1-pass"), Method: MethodPassword},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	finalTarget := Target{
		Host: thost,
		Port: tport,
		User: "target-user",
		Auth: passwordAuth("target-pass"),
	}

	client, err := SingleJumpBuildFunc(ctx, BuildOptions{
		Hops:            []Hop{{ProfileID: "hop1"}},
		FinalTarget:     finalTarget,
		Dialer:          dialer,
		ProfileResolver: profileResolver(profiles),
	})
	if err != nil {
		t.Fatalf("SingleJumpBuildFunc: %v", err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("client.NewSession: %v", err)
	}
	_ = sess.Close()
}

// TestStrategy_MultiHop_3 验证 "multi-hop" 策略（三跳）。
//
// Local ← Hop1 ← Hop2 ← Target
func TestStrategy_MultiHop_3(t *testing.T) {
	target := newHopServer(t)
	thost, tport := target.hostAndPort(t)
	target.SetForwardAddr(target.hostPort())

	hop2 := newHopServer(t)
	h2host, h2port, err := splitHostPort(hop2.hostPort())
	if err != nil {
		t.Fatalf("splitHostPort hop2: %v", err)
	}
	hop2.SetForwardAddr(target.hostPort())

	hop1 := newHopServer(t)
	h1host, h1port, err := splitHostPort(hop1.hostPort())
	if err != nil {
		t.Fatalf("splitHostPort hop1: %v", err)
	}
	hop1.SetForwardAddr(hop2.hostPort())

	dialer := newTestDialer()
	profiles := map[string]HopTarget{
		"hop1": {Host: h1host, Port: h1port, User: "u1", Auth: passwordAuth("p1"), Method: MethodPassword},
		"hop2": {Host: h2host, Port: h2port, User: "u2", Auth: passwordAuth("p2"), Method: MethodPassword},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	finalTarget := Target{
		Host: thost,
		Port: tport,
		User: "tgt",
		Auth: passwordAuth("tgtpass"),
	}

	client, err := MultiHopBuildFunc(ctx, BuildOptions{
		Hops: []Hop{
			{ProfileID: "hop1"},
			{ProfileID: "hop2"},
		},
		FinalTarget:     finalTarget,
		Dialer:          dialer,
		ProfileResolver: profileResolver(profiles),
	})
	if err != nil {
		t.Fatalf("MultiHopBuildFunc: %v", err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("client.NewSession: %v", err)
	}
	_ = sess.Close()
}

// TestStrategy_SingleJump_HopAuthFails 验证 hop 密码错时报错。
func TestStrategy_SingleJump_HopAuthFails(t *testing.T) {
	target := newHopServer(t)
	thost, tport := target.hostAndPort(t)
	target.SetForwardAddr(target.hostPort())

	hop1 := newPickyServer(t, "correct-hop-password")
	h1host, h1port, err := splitHostPort(hop1.hostPort())
	if err != nil {
		t.Fatalf("splitHostPort hop1: %v", err)
	}

	dialer := newTestDialer()
	profiles := map[string]HopTarget{
		"hop1": {Host: h1host, Port: h1port, User: "u1", Auth: passwordAuth("WRONG"), Method: MethodPassword},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	finalTarget := Target{
		Host: thost,
		Port: tport,
		User: "tgt",
		Auth: passwordAuth("tgtpass"),
	}

	_, err = SingleJumpBuildFunc(ctx, BuildOptions{
		Hops:            []Hop{{ProfileID: "hop1"}},
		FinalTarget:     finalTarget,
		Dialer:          dialer,
		ProfileResolver: profileResolver(profiles),
	})
	if err == nil {
		t.Fatal("SingleJumpBuildFunc with wrong hop password: expected error, got nil")
	}
	// 错误信息可能 wrap 多层；只看是否含 auth/handshake
	if !strings.Contains(err.Error(), "handshake") &&
		!strings.Contains(err.Error(), "auth") &&
		!strings.Contains(err.Error(), "password") {
		t.Logf("expected auth/handshake-related error, got: %v", err)
	}
}

// TestStrategy_SingleJump_ProfileNotFound 验证 profile 不存在时报错。
func TestStrategy_SingleJump_ProfileNotFound(t *testing.T) {
	target := newHopServer(t)
	thost, tport := target.hostAndPort(t)
	target.SetForwardAddr(target.hostPort())

	dialer := newTestDialer()
	profiles := map[string]HopTarget{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	finalTarget := Target{
		Host: thost,
		Port: tport,
		User: "tgt",
		Auth: passwordAuth("tgtpass"),
	}

	_, err := SingleJumpBuildFunc(ctx, BuildOptions{
		Hops:            []Hop{{ProfileID: "non-existent-profile"}},
		FinalTarget:     finalTarget,
		Dialer:          dialer,
		ProfileResolver: profileResolver(profiles),
	})
	if err == nil {
		t.Fatal("SingleJumpBuildFunc with non-existent profile: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

// TestStrategy_Direct_NilDialer 验证 BuildOptions.Dialer == nil 时报错。
func TestStrategy_Direct_NilDialer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := DirectBuildFunc(ctx, BuildOptions{
		Dialer: nil,
		FinalTarget: Target{
			Host: "127.0.0.1", Port: 22, User: "u", Auth: passwordAuth("p"),
		},
	})
	if err == nil {
		t.Fatal("DirectBuildFunc with nil Dialer: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Dialer is nil") {
		t.Errorf("error should mention 'Dialer is nil', got: %v", err)
	}
}

// TestStrategy_SingleJump_NilProfileResolver 验证 ProfileResolver == nil 时报错。
func TestStrategy_SingleJump_NilProfileResolver(t *testing.T) {
	dialer := newTestDialer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := SingleJumpBuildFunc(ctx, BuildOptions{
		Hops:        []Hop{{ProfileID: "any"}},
		FinalTarget: Target{Host: "127.0.0.1", Port: 22, User: "u", Auth: passwordAuth("p")},
		Dialer:      dialer,
	})
	if err == nil {
		t.Fatal("SingleJumpBuildFunc with nil ProfileResolver: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ProfileResolver") {
		t.Errorf("error should mention 'ProfileResolver', got: %v", err)
	}
}

// TestStrategy_MultiHop_NoHops 验证 multi-hop 零跳退化为 direct。
func TestStrategy_MultiHop_NoHops(t *testing.T) {
	srv := newHopServer(t)
	host, port := srv.hostAndPort(t)
	srv.SetForwardAddr(srv.hostPort())

	dialer := newTestDialer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := MultiHopBuildFunc(ctx, BuildOptions{
		Hops: nil,
		FinalTarget: Target{
			Host: host, Port: port, User: "u", Auth: passwordAuth("p"),
		},
		Dialer: dialer,
	})
	if err != nil {
		t.Fatalf("MultiHopBuildFunc with no hops: %v", err)
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("client.NewSession: %v", err)
	}
	_ = sess.Close()
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// splitHostPort 把 "host:port" 拆成 (host, port)。
func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

// profileResolver 把 map 包成 BuildOptions.ProfileResolver。
func profileResolver(m map[string]HopTarget) func(string) (HopTarget, bool) {
	return func(id string) (HopTarget, bool) {
		t, ok := m[id]
		return t, ok
	}
}
