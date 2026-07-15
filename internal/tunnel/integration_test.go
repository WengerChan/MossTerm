// integration_test.go 端到端覆盖三种 SSH 端口转发模式。
//
// 测试基础设施：
//   - 复用 internal/sshclient 的 sshIntegrationServer 思路
//   - 但因为 sshclient.integration_test.go 的 server **不**处理
//     "tcpip-forward" / "forwarded-tcpip"，本测试自己构造一个
//     minimal in-process SSH server（testSSHServer），支持：
//   - 任何用户名/密码
//   - session channel（pty-req / shell / subsystem reply true）
//   - tcpip-forward 请求（接受 + 在本进程监听 forwarded port）
//   - forwarded-tcpip channel（接受 + 把 conn 喂给本进程 listener）
//   - TCP echo server 跑在 localhost:<rand> 当 target
//
// 测试矩阵：
//   - TestTunnel_Local：listen 0.0.0.0:0 → local conn → ssh.Dial(target) → echo
//   - TestTunnel_Remote：ssh.Listen → server-side listener 接受 conn → local dial(target) → echo
//   - TestTunnel_Dynamic：SOCKS5 客户端握手 + CONNECT → ssh.Dial(target) → echo
//   - TestTunnel_Local_ProviderMissing：未注入 provider 时 Open 报错
package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// -----------------------------------------------------------------------------
// TCP echo target（最简）
// -----------------------------------------------------------------------------

// startEchoServer 启动一个 TCP echo server，绑 127.0.0.1:0。
// 返回 "host:port" + cleanup。
func startEchoServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo.Listen: %v", err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				// 包一层：屏蔽 WriterTo/ReaderFrom 优化，
				// 强制 io.Copy 走通用 copyBuffer 路径（每次循环 1 read + 1 write）。
				// 否则 io.Copy(c, c) 会触发 TCPConn.WriteTo 死锁。
				src := struct{ io.Reader }{c}
				dst := struct{ io.Writer }{c}
				_, _ = io.Copy(dst, src)
				_ = c.Close()
			}(conn)
		}
	}()
	cleanup := func() {
		_ = ln.Close()
		// 等待所有 conn goroutine 退出
		wg.Wait()
		<-done
	}
	return ln.Addr().String(), cleanup
}

// echoRoundTrip 客户端通过 dialAddr 写 payload，期望 echo 回。
// 返回 echo 字节数；n==0 + err 表示失败。
//
// 重要：defer conn.Close() + CloseWrite 触发对端 EOF 传播，让
// io.Copy goroutine 优雅退出（避免测试 hang 在 cleanup）。
func echoRoundTrip(t *testing.T, dialAddr string, payload []byte, timeout time.Duration) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp", dialAddr, timeout)
	if err != nil {
		t.Fatalf("echoRoundTrip dial %s: %v", dialAddr, err)
	}
	// 用 cleanup 闭包确保 conn 关闭，defer 内部已隐含
	defer func() {
		// 显式先 CloseWrite 让对端 io.Copy 收到 EOF
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		_ = conn.Close()
	}()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("echoRoundTrip write: %v", err)
	}
	buf := make([]byte, len(payload))
	n, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("echoRoundTrip read: %v (got %d bytes)", err, n)
	}
	return n
}

// waitForStats 轮询直到 tun.Stats() 累计到 >= minBytes（in + out 双向），或超时。
// 解决 io.Copy goroutine 退异步于 conn 关闭的 race —— 测试要断言 stats 时需要等。
func waitForStats(t *testing.T, tun Tunnel, minBytes int64, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s := tun.Stats()
		if s.BytesIn >= minBytes && s.BytesOut >= minBytes {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := tun.Stats()
	return fmt.Errorf("timeout waiting for stats >= %d (got in=%d out=%d)", minBytes, s.BytesIn, s.BytesOut)
}

// -----------------------------------------------------------------------------
// testSSHServer：支持 session + tcpip-forward + forwarded-tcpip
// -----------------------------------------------------------------------------

// testSSHServer 是 in-process SSH server，绑 127.0.0.1:0。
//
// 与 sshclient/integration_test.go 的 sshIntegrationServer 区别：
//   - 增加 tcpip-forward 请求处理（remote forward）
//   - 增加 forwarded-tcpip channel 接受（remote forward）
type testSSHServer struct {
	listener net.Listener
	cfg      *ssh.ServerConfig

	// forwardListeners 记录 tcpip-forward 成功的 listener；
	// client 端 ssh.Listen 关联到其中一个。
	// key = 远端地址 "host:port"（listener.Addr()）
	forwardMu    sync.Mutex
	forwardLn    net.Listener
	forwardedChs map[string]chan net.Conn // remoteListenerAddr -> 客户端新 conn 通知

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once

	// t 用于把 server 内部事件通过 t.Log 报告（不通过 slog，避免污染 stderr）
	t *testing.T
}

// newTestSSHServer 构造 + 启动；t.Cleanup 调 Close。
func newTestSSHServer(t *testing.T) *testSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(signer)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &testSSHServer{
		listener:     l,
		cfg:          cfg,
		forwardedChs: make(map[string]chan net.Conn),
		ctx:          ctx,
		cancel:       cancel,
		t:            t,
	}
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

func (s *testSSHServer) acceptLoop() {
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

// handleConn 处理一个 SSH 连接。
func (s *testSSHServer) handleConn(c net.Conn) {
	defer c.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(c, s.cfg)
	if err != nil {
		return
	}
	defer sconn.Close()

	var connWg sync.WaitGroup
	defer connWg.Wait()

	connWg.Add(1)
	go func() {
		defer connWg.Done()
		<-s.ctx.Done()
		sconn.Close()
	}()

	// 全局请求：keepalive / tcpip-forward
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		for req := range reqs {
			switch req.Type {
			case "tcpip-forward":
				s.handleTCPIPForward(sconn, req, &connWg)
			case "cancel-tcpip-forward":
				// 简化：accept + 不做事（forwardListener 关时 client 会拿到 EOF）
				if req.WantReply {
					req.Reply(true, nil)
				}
			default:
				if req.WantReply {
					req.Reply(true, nil)
				}
			}
		}
	}()

	// 通道：session + forwarded-tcpip + direct-tcpip
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
			case "forwarded-tcpip":
				s.handleForwardedTCPIP(newChan, &connWg)
			case "direct-tcpip":
				s.handleDirectTCPIP(newChan, &connWg)
			default:
				_ = newChan.Reject(ssh.UnknownChannelType, "")
			}
		}
	}()

	_ = sconn.Wait()
}

// handleTCPIPForward 处理 global "tcpip-forward" 请求。
//
// 协议：payload = [4B addr-len][addr-bytes][4B port]
// 返回 reply payload = [4B port]（client 端用真实监听 port 替换）
//
// 简化：直接绑 127.0.0.1:<port>。client 端 ssh.Listen 拿到的 net.Listener
// 上 Accept 拿到的 conn 就是 server 接受后转发的 conn。
func (s *testSSHServer) handleTCPIPForward(sconn *ssh.ServerConn, req *ssh.Request, connWg *sync.WaitGroup) {
	host, port, err := parseForwardMsg(req.Payload)
	if err != nil {
		if req.WantReply {
			req.Reply(false, nil)
		}
		return
	}

	// 在 127.0.0.1 上 listen（如果 port=0 由 OS 分配）
	listenAddr := net.JoinHostPort(host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		if req.WantReply {
			req.Reply(false, nil)
		}
		return
	}

	// 记录 listener + 准备 forwarded conn channel
	s.forwardMu.Lock()
	s.forwardLn = ln
	listenerKey := ln.Addr().String()
	ch := make(chan net.Conn, 16)
	s.forwardedChs[listenerKey] = ch
	s.forwardMu.Unlock()

	// reply: [4B port]
	respPort := uint32(ln.Addr().(*net.TCPAddr).Port)
	respPayload := make([]byte, 4)
	binary.BigEndian.PutUint32(respPayload, respPort)
	if req.WantReply {
		req.Reply(true, respPayload)
	}

	// accept loop：每条 conn → 走 ssh forwarded-tcpip channel → 转发到 client
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.t.Logf("testSSHServer: accepted remote-forward conn from %s", c.RemoteAddr())
			// 开 forwarded-tcpip channel
			//
			// RFC 4254 7.2：connected = SSH server 的 forward listener（client 端
			// ssh.Listener 注册的 laddr），origin = 实际连接过来的 client。
			// x/crypto v0.31 client.handleForwards 用 connected 与注册表匹配，
			// 匹配不上 → "no forward for address"。
			listenerAddr := parseTCPAddr(ln.Addr().String())
			origin := parseTCPAddr(c.RemoteAddr().String())
			ch, reqs, err := sconn.OpenChannel("forwarded-tcpip", ssh.Marshal(&struct {
				ConnectedAddr string
				ConnectedPort uint32
				OriginAddr    string
				OriginPort    uint32
			}{
				ConnectedAddr: listenerAddr.host,
				ConnectedPort: uint32(listenerAddr.port),
				OriginAddr:    origin.host,
				OriginPort:    uint32(origin.port),
			}))
			if err != nil {
				s.t.Logf("testSSHServer: OpenChannel forwarded-tcpip failed: %v", err)
				_ = c.Close()
				continue
			}
			s.t.Logf("testSSHServer: OpenChannel forwarded-tcpip OK")
			go ssh.DiscardRequests(reqs)
			// pipe: local c <-> ssh ch
			connWg.Add(1)
			go func() {
				defer connWg.Done()
				defer ch.Close()
				defer c.Close()

				// 任意方向 EOF → 关两端（unblock 对端 io.Copy goroutine；避免死锁）
				var wg sync.WaitGroup
				buf := make([]byte, 32*1024)
				wg.Add(2)
				closeOnce := &sync.Once{}
				closeBoth := func() {
					closeOnce.Do(func() {
						_ = ch.Close()
						_ = c.Close()
					})
				}
				go func() {
					defer wg.Done()
					_, _ = io.CopyBuffer(ch, c, buf) // 读 c → 写 ch
					_ = c.(*net.TCPConn).CloseWrite()
					closeBoth()
				}()
				go func() {
					defer wg.Done()
					_, _ = io.CopyBuffer(c, ch, buf) // 读 ch → 写 c
					_ = ch.CloseWrite()
					closeBoth()
				}()
				wg.Wait()
			}()
		}
	}()
}

// handleForwardedTCPIP 接受 forwarded-tcpip channel（client 主动开时）。
// 当前测试不需要这条路径（server 主动开 + 转发到 client），
// 但 ssh 库会发；这里直接 Reject 防干扰。
func (s *testSSHServer) handleForwardedTCPIP(newChan ssh.NewChannel, connWg *sync.WaitGroup) {
	_ = connWg
	_ = newChan.Reject(ssh.UnknownChannelType, "")
}

// handleDirectTCPIP 接受 direct-tcpip channel（用于 -L / SOCKS5 转发）。
//
// 协议：payload = [4B dest-addr-len][dest-addr][4B dest-port][4B src-addr-len][src-addr][4B src-port]
// 解析后 net.Dial("tcp", dest) → pipe 双向数据到 ssh channel。
//
// 重要：任一 io.Copy 返回就 Close 另一端，避免「echo server read ↔ ch
// read」循环死锁（v0.6.1 真实 server 用 ssh 协议 EOF 触发，本测试 server
// 不模拟那么细）。
func (s *testSSHServer) handleDirectTCPIP(newChan ssh.NewChannel, connWg *sync.WaitGroup) {
	destHost, destPort, err := parseDirectTCPIP(newChan.ExtraData())
	if err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)

	target, err := net.Dial("tcp", net.JoinHostPort(destHost, strconv.Itoa(destPort)))
	if err != nil {
		_ = ch.Close()
		return
	}

	connWg.Add(1)
	go func() {
		defer connWg.Done()
		defer ch.Close()
		defer target.Close()

		// 任意方向 EOF → 关另一端的 WRITE 半边（让对端 io.Copy 拿到 EOF）
		// 避免「echo server read ↔ ch read」循环死锁。
		// v0.6.1 真实 server 用 ssh 协议 EOF 触发；本测试 server
		// 不模拟那么细，靠 CloseWrite 显式半关。
		var wg sync.WaitGroup
		buf := make([]byte, 32*1024)
		wg.Add(2)
		closeOnce := &sync.Once{}
		closeBoth := func() {
			closeOnce.Do(func() {
				_ = ch.Close()
				_ = target.Close()
			})
		}

		go func() {
			defer wg.Done()
			_, _ = io.CopyBuffer(ch, target, buf) // 读 target → 写 ch
			// target EOF → 关闭 ch 写半边（让 localTunnel 收到 EOF）
			_ = ch.CloseWrite()
			closeBoth()
		}()
		go func() {
			defer wg.Done()
			_, _ = io.CopyBuffer(target, ch, buf) // 读 ch → 写 target
			// ch EOF → 关闭 target 写半边（让 echo server 收到 EOF）
			_ = target.(*net.TCPConn).CloseWrite()
			closeBoth()
		}()

		wg.Wait()
	}()
}

// parseDirectTCPIP 解析 direct-tcpip / forwarded-tcpip channel payload。
//
// 协议：
//
//	[4B dest-addr-len][dest-addr]
//	[4B dest-port]
//	[4B src-addr-len][src-addr]
//	[4B src-port]
func parseDirectTCPIP(payload []byte) (host string, port int, err error) {
	readStr := func(b []byte) (string, []byte, error) {
		if len(b) < 4 {
			return "", nil, errors.New("parseDirectTCPIP: too short for string")
		}
		l := binary.BigEndian.Uint32(b[:4])
		if int(l) > len(b)-4 {
			return "", nil, errors.New("parseDirectTCPIP: string len out of range")
		}
		return string(b[4 : 4+l]), b[4+l:], nil
	}
	readU32 := func(b []byte) (uint32, []byte, error) {
		if len(b) < 4 {
			return 0, nil, errors.New("parseDirectTCPIP: too short for u32")
		}
		return binary.BigEndian.Uint32(b[:4]), b[4:], nil
	}
	rest := payload
	dest, rest, err := readStr(rest)
	if err != nil {
		return "", 0, err
	}
	p, rest, err := readU32(rest)
	if err != nil {
		return "", 0, err
	}
	// 跳过 src addr + port（解析但不验证）
	if _, rest, err = readStr(rest); err != nil {
		return "", 0, err
	}
	if _, _, err = readU32(rest); err != nil {
		return "", 0, err
	}
	return dest, int(p), nil
}

// handleSession 处理 session channel：reply true 给所有标准请求。
func (s *testSSHServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		if req.WantReply {
			req.Reply(true, nil)
		}
	}
}

func (s *testSSHServer) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.listener.Close()
		s.forwardMu.Lock()
		if s.forwardLn != nil {
			_ = s.forwardLn.Close()
		}
		s.forwardMu.Unlock()
		s.wg.Wait()
	})
}

func (s *testSSHServer) hostPort() string { return s.listener.Addr().String() }

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	h, ps, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	p, err := strconv.Atoi(ps)
	if err != nil {
		t.Fatalf("Atoi port: %v", err)
	}
	return h, p
}

// dialTestSSH 拿 *ssh.Client。
func dialTestSSH(t *testing.T, addr string) *ssh.Client {
	t.Helper()
	h, p := splitHostPort(t, addr)
	cfg := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("hunter2")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	c, err := ssh.Dial("tcp", net.JoinHostPort(h, strconv.Itoa(p)), cfg)
	if err != nil {
		t.Fatalf("ssh.Dial: %v", err)
	}
	return c
}

// providerForClient 返回一个 ClientProvider，恒返回同一个 *ssh.Client。
// 用于 Local / Dynamic 测试。
func providerForClient(c *ssh.Client) ClientProvider {
	return ClientFunc(func(_ string) (*ssh.Client, bool) {
		return c, c != nil
	})
}

// parseForwardMsg 解析 tcpip-forward / cancel-tcpip-forward / forwarded-tcpip payload。
//
// 协议：SSH string = [4B big-endian length][bytes]
// forward msg = [addr-string][4B port]
func parseForwardMsg(payload []byte) (host string, port int, err error) {
	if len(payload) < 4 {
		return "", 0, errors.New("parseForwardMsg: too short")
	}
	addrLen := binary.BigEndian.Uint32(payload[:4])
	if int(addrLen) > len(payload)-4 {
		return "", 0, errors.New("parseForwardMsg: addr len out of range")
	}
	host = string(payload[4 : 4+addrLen])
	if len(payload) < int(4+addrLen+4) {
		return "", 0, errors.New("parseForwardMsg: missing port")
	}
	port = int(binary.BigEndian.Uint32(payload[4+addrLen : 4+addrLen+4]))
	return host, port, nil
}

type tcpAddr struct {
	host string
	port int
}

func parseTCPAddr(addr string) tcpAddr {
	h, ps, err := net.SplitHostPort(addr)
	if err != nil {
		return tcpAddr{host: "127.0.0.1", port: 0}
	}
	p, _ := strconv.Atoi(ps)
	return tcpAddr{host: h, port: p}
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

// TestTunnel_Local：-L 转发连通 + 字节数对得上。
func TestTunnel_Local(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()
	echoHost, echoPort := splitHostPort(t, echoAddr)

	sshSrv := newTestSSHServer(t)
	sshClient := dialTestSSH(t, sshSrv.hostPort())
	defer sshClient.Close()

	mgr := NewMemoryManager().WithClientProvider(providerForClient(sshClient))
	tun, err := mgr.Open(context.Background(), Spec{
		ID:         "test-local",
		Mode:       Local,
		BindHost:   "127.0.0.1",
		BindPort:   0,
		TargetHost: echoHost,
		TargetPort: echoPort,
		SessionID:  "fake-session",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer mgr.Close(tun.Spec().ID)

	if st := tun.State(); st != TunnelStateActive {
		t.Errorf("State = %s, want active", st)
	}

	// 通过 tun 拿 listen address（Stats 无 addr；额外从 Stop 后 listener 拿。
	// 但 listener 在 Stop 后关——这里用 mgr 暴露 listener 不可能。
	// 改用：拿 Spec() 的 BindPort 0 表示 OS 分配——这不够。
	//
	// 解决：起一个小的 *localTunnel 直接用，再反射 listener 出来？
	// 太脆。改用：把 BindPort 设为 0 后，Open 完先 Close 拿 listener.Addr() 不可能。
	//
	// 简单方案：先开 tunnel，再开一个临时 client conn 探测。
	// 探测方式：从 Start 状态后，listener 必然已建（同步返回）。
	// 我们在 Open 后短暂 wait，然后靠 accept 拿到 echo 来回环。
	//
	// v0.6 取巧：让 localTunnel 暴露 BoundAddr() 接口？
	// 不改 API：直接 type-assert 到 *localTunnel 拿 listener 字段。
	lt, ok := tun.(*localTunnel)
	if !ok {
		t.Fatalf("tun is %T, want *localTunnel", tun)
	}
	boundAddr := lt.listener.Addr().String()

	// 客户端连 → 写 → 期望 echo 回
	payload := []byte("hello-local-tunnel")
	got := echoRoundTrip(t, boundAddr, payload, 3*time.Second)
	if got != len(payload) {
		t.Errorf("echo bytes = %d, want %d", got, len(payload))
	}

	// 验证 Stats 累计到（至少）payload 字节
	// Stats 在 io.Copy goroutine 退出时更新；轮询最多 2s 等待
	if err := waitForStats(t, tun, int64(len(payload)), 2*time.Second); err != nil {
		t.Errorf("Stats bytes: %v", err)
	}

	// 显式 mgr.Close，让 io.Copy goroutine 收到 listener 关闭信号后退出
	// （避免 defer 顺序 + echo server cleanup hang）
	if err := mgr.Close(tun.Spec().ID); err != nil {
		t.Errorf("mgr.Close: %v", err)
	}
}

// TestTunnel_Remote：-R 转发连通。
//
// in-process server 通过 handleTCPIPForward 模拟远端监听；
// 客户端用 sshClient.Listen 拿到 net.Listener 后，由 in-process server
// 的 accept → forwarded-tcpip channel 转发。
func TestTunnel_Remote(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()
	echoHost, echoPort := splitHostPort(t, echoAddr)

	sshSrv := newTestSSHServer(t)
	sshClient := dialTestSSH(t, sshSrv.hostPort())
	defer sshClient.Close()

	mgr := NewMemoryManager().WithClientProvider(providerForClient(sshClient))
	tun, err := mgr.Open(context.Background(), Spec{
		ID:         "test-remote",
		Mode:       Remote,
		BindHost:   "127.0.0.1",
		BindPort:   0, // OS 分配
		TargetHost: echoHost,
		TargetPort: echoPort,
		SessionID:  "fake-session",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer mgr.Close(tun.Spec().ID)

	rt, ok := tun.(*remoteTunnel)
	if !ok {
		t.Fatalf("tun is %T, want *remoteTunnel", tun)
	}

	// ssh.Listener.Addr() 拿 server 端 listen 的地址
	boundAddr := rt.listener.Addr().String()

	// 给 in-process server 一点时间把 forwarded-tcpip channel 建好
	// （forward 是 async，但 reply 已收到 → 链路 OK）
	payload := []byte("hello-remote-tunnel")
	got := echoRoundTrip(t, boundAddr, payload, 5*time.Second)
	if got != len(payload) {
		t.Errorf("echo bytes = %d, want %d", got, len(payload))
	}
	if err := waitForStats(t, tun, int64(len(payload)), 2*time.Second); err != nil {
		t.Errorf("Stats bytes: %v", err)
	}

	// 显式 mgr.Close：ssh.Listener.Close 触发 cancel-tcpip-forward
	// 让所有 forwarded-tcpip conn 收到 EOF → io.Copy goroutine 退出
	if err := mgr.Close(tun.Spec().ID); err != nil {
		t.Errorf("mgr.Close: %v", err)
	}
}

// TestTunnel_Dynamic：SOCKS5 握手 + CONNECT 转发。
func TestTunnel_Dynamic(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()
	echoHost, echoPort := splitHostPort(t, echoAddr)

	sshSrv := newTestSSHServer(t)
	sshClient := dialTestSSH(t, sshSrv.hostPort())
	defer sshClient.Close()

	mgr := NewMemoryManager().WithClientProvider(providerForClient(sshClient))
	tun, err := mgr.Open(context.Background(), Spec{
		ID:        "test-dynamic",
		Mode:      Dynamic,
		BindHost:  "127.0.0.1",
		BindPort:  0,
		SessionID: "fake-session",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer mgr.Close(tun.Spec().ID)

	dt, ok := tun.(*dynamicTunnel)
	if !ok {
		t.Fatalf("tun is %T, want *dynamicTunnel", tun)
	}
	socksAddr := dt.listener.Addr().String()

	// SOCKS5 客户端：greeting + CONNECT + payload
	conn, err := net.DialTimeout("tcp", socksAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial socks: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// 1. greeting: [ver=5][nmethods=1][methods=0x00 (NO AUTH)]
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("greeting write: %v", err)
	}
	// reply: [ver=5][method=0x00]
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatalf("greeting read: %v", err)
	}
	if methodReply[0] != 0x05 || methodReply[1] != 0x00 {
		t.Fatalf("method reply = %v, want [5 0]", methodReply)
	}

	// 2. request: [ver=5][cmd=CONNECT=1][rsv=0][atyp=DOMAIN=3]
	//    [len][domain "127.0.0.1" → 用 domain 测一遍解析]
	//    [port:2B]
	targetHost := echoHost // 用 IPv4 literal 测 IPv4 分支
	var req []byte
	req = append(req, 0x05, 0x01, 0x00)
	if net.ParseIP(targetHost) != nil {
		req = append(req, 0x01) // IPv4
		req = append(req, net.ParseIP(targetHost).To4()...)
	} else {
		req = append(req, 0x03) // domain
		req = append(req, byte(len(targetHost)))
		req = append(req, targetHost...)
	}
	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, uint16(echoPort))
	req = append(req, portBuf...)

	if _, err := conn.Write(req); err != nil {
		t.Fatalf("request write: %v", err)
	}
	// reply: [ver=5][rep=0][rsv=0][atyp=1][4B addr][2B port]
	replyHdr := make([]byte, 10)
	if _, err := io.ReadFull(conn, replyHdr); err != nil {
		t.Fatalf("reply read: %v", err)
	}
	if replyHdr[0] != 0x05 || replyHdr[1] != 0x00 {
		t.Fatalf("reply = %v, want ver=5 rep=0", replyHdr[:2])
	}

	// 3. payload
	payload := []byte("hello-socks5")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("payload write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("payload read: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Errorf("echo = %q, want %q", buf, payload)
	}
}

// TestTunnel_Dynamic_BindUnsupported：cmd=BIND → 0x07 reply。
func TestTunnel_Dynamic_BindUnsupported(t *testing.T) {
	sshSrv := newTestSSHServer(t)
	sshClient := dialTestSSH(t, sshSrv.hostPort())
	defer sshClient.Close()

	mgr := NewMemoryManager().WithClientProvider(providerForClient(sshClient))
	tun, err := mgr.Open(context.Background(), Spec{
		ID:        "test-dynamic-bind",
		Mode:      Dynamic,
		BindHost:  "127.0.0.1",
		BindPort:  0,
		SessionID: "fake-session",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer mgr.Close(tun.Spec().ID)
	dt := tun.(*dynamicTunnel)
	socksAddr := dt.listener.Addr().String()

	conn, err := net.DialTimeout("tcp", socksAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	// greeting
	_, _ = conn.Write([]byte{0x05, 0x01, 0x00})
	methodReply := make([]byte, 2)
	_, _ = io.ReadFull(conn, methodReply)

	// request: cmd=2 (BIND) → expect rep=7
	req := []byte{0x05, 0x02, 0x00, 0x01, 127, 0, 0, 1, 0, 0}
	_, _ = conn.Write(req)
	replyHdr := make([]byte, 10)
	if _, err := io.ReadFull(conn, replyHdr); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if replyHdr[1] != 0x07 {
		t.Errorf("BIND reply rep = %d, want 7 (CmdNotSupported)", replyHdr[1])
	}
}

// TestTunnel_Dynamic_AuthRefused：client 只支持 USER/PASS (0x02) → 0xFF reply。
func TestTunnel_Dynamic_AuthRefused(t *testing.T) {
	sshSrv := newTestSSHServer(t)
	sshClient := dialTestSSH(t, sshSrv.hostPort())
	defer sshClient.Close()

	mgr := NewMemoryManager().WithClientProvider(providerForClient(sshClient))
	tun, err := mgr.Open(context.Background(), Spec{
		ID:        "test-dynamic-auth",
		Mode:      Dynamic,
		BindHost:  "127.0.0.1",
		BindPort:  0,
		SessionID: "fake-session",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer mgr.Close(tun.Spec().ID)
	dt := tun.(*dynamicTunnel)
	socksAddr := dt.listener.Addr().String()

	conn, err := net.DialTimeout("tcp", socksAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	// greeting: nmethods=1, methods=0x02 (USER/PASS, unsupported)
	_, _ = conn.Write([]byte{0x05, 0x01, 0x02})
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatalf("read: %v", err)
	}
	if methodReply[1] != 0xFF {
		t.Errorf("auth reply method = %d, want 0xFF (NoAcceptable)", methodReply[1])
	}
}

// TestTunnel_Open_ProviderMissing：未注入 provider 时 Open 报错。
func TestTunnel_Open_ProviderMissing(t *testing.T) {
	mgr := NewMemoryManager() // 不调 WithClientProvider
	_, err := mgr.Open(context.Background(), Spec{
		ID:         "no-provider",
		Mode:       Local,
		BindHost:   "127.0.0.1",
		BindPort:   0,
		TargetHost: "127.0.0.1",
		TargetPort: 1,
		SessionID:  "fake",
	})
	if err == nil {
		t.Fatal("Open with nil provider should error")
	}
}

// TestTunnel_Open_DuplicateID：相同 ID 第二次 Open 报错。
func TestTunnel_Open_DuplicateID(t *testing.T) {
	sshSrv := newTestSSHServer(t)
	sshClient := dialTestSSH(t, sshSrv.hostPort())
	defer sshClient.Close()

	mgr := NewMemoryManager().WithClientProvider(providerForClient(sshClient))
	t1, err := mgr.Open(context.Background(), Spec{
		ID: "dup", Mode: Local,
		BindHost: "127.0.0.1", BindPort: 0,
		TargetHost: "127.0.0.1", TargetPort: 1,
		SessionID: "fake",
	})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer mgr.Close(t1.Spec().ID)

	_, err = mgr.Open(context.Background(), Spec{
		ID: "dup", Mode: Local,
		BindHost: "127.0.0.1", BindPort: 0,
		TargetHost: "127.0.0.1", TargetPort: 1,
		SessionID: "fake",
	})
	if err == nil {
		t.Fatal("second Open with same ID should error")
	}
}

// TestTunnel_Open_InvalidSpec：参数校验。
func TestTunnel_Open_InvalidSpec(t *testing.T) {
	mgr := NewMemoryManager().WithClientProvider(providerForClient(nil))
	cases := []struct {
		name string
		spec Spec
	}{
		{"empty id", Spec{Mode: Local, BindHost: "x", TargetHost: "y", SessionID: "s"}},
		{"empty session", Spec{ID: "1", Mode: Local, BindHost: "x", TargetHost: "y"}},
		{"empty bind", Spec{ID: "2", Mode: Local, TargetHost: "y", SessionID: "s"}},
		{"empty target for local", Spec{ID: "3", Mode: Local, BindHost: "x", SessionID: "s"}},
		{"unknown mode", Spec{ID: "4", Mode: Mode(99), BindHost: "x", TargetHost: "y", SessionID: "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := mgr.Open(context.Background(), tc.spec)
			if err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

// TestTunnel_Close_NotFound：Close 不存在的 ID 报错。
func TestTunnel_Close_NotFound(t *testing.T) {
	mgr := NewMemoryManager()
	if err := mgr.Close("nope"); err == nil {
		t.Error("Close non-existent should error")
	}
}
