// sftp_test.go 覆盖 internal/app 的 SFTP 客户端生命周期。
//
// 范围（v0.5.1-A spec）：
//  1. 生命周期：Open session → sftpFor 建 client → 再 sftpFor 复用 → 关 → sftpFor 重建
//  2. 并发安全：10 个 goroutine 同时 sftpFor（race detector 验证）
//  3. 错误路径：session 不存在 / 未 established / 已 closed
//  4. App.Close 清理：所有缓存的 sftp client 被 Close
//
// 测试基础设施：
//   - sshTestServer：in-process SSH server（accept any password / ed25519 host key）
//   - openSftp 钩子：替换为 mock factory，跳过真实 SFTP subsystem 协商
//     （避免在单元测试里启 sftp.NewServer）
//   - 真实 *sshclient.Connector：dial 成功 + c.client 设置 + sftpFor 的真路径
//
// 不覆盖（v0.5.1 spec 明确不做）：
//   - 真实 SFTP 远端 IO（那是 v0.6+ 的 integration test）
//   - keepalive 资源回收（Connector 是 long-lived singleton，Close 留给 main.go）
package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mossterm/mossterm/internal/connect"
	"github.com/mossterm/mossterm/internal/session"
	"github.com/mossterm/mossterm/internal/sftpclient"
	"github.com/mossterm/mossterm/internal/sshclient"
)

// -----------------------------------------------------------------------------
// 桩：in-process SSH server
// -----------------------------------------------------------------------------

// sshTestServer 是绑在 127.0.0.1:0 上的最小化 SSH server。
//
// 接受任何用户名/密码认证；
// 接受 "session" 类型的 channel open，并对所有 session request 回复成功
// （pty-req / shell / exec / env / subsystem / window-change），
// 这样 *sshclient.Connector 的 OpenSession 能走完流程，session 达 Established。
//
// 不真的执行 shell —— shell 收到后立即 close stdin 让 readLoop 拿到 EOF。
// 这对 sftpFor 的测试足够（不验证 PTY 数据流）。
//
// 清理：context 取消时，handleConn 主动 sconn.Close()，让 sconn.Wait() 返回，
// s.Close() 不会卡死。
type sshTestServer struct {
	listener  net.Listener
	serverCfg *ssh.ServerConfig

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

func newSSHTestServer(t *testing.T) *sshTestServer {
	t.Helper()

	// 生成临时 host key
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}
	serverCfg := &ssh.ServerConfig{
		// 接受任何密码 —— 测试环境用，生产用 known_hosts + 真实认证
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
	s := &sshTestServer{
		listener:  l,
		serverCfg: serverCfg,
		ctx:       ctx,
		cancel:    cancel,
	}
	s.wg.Add(1)
	go s.acceptLoop()

	t.Cleanup(s.Close)
	return s
}

func (s *sshTestServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.handleConn(c)
		}(conn)
	}
}

// handleConn 处理一个 SSH 连接：全局请求 + session 通道。
// ctx 取消时主动 sconn.Close()，让 sconn.Wait() 返回。
func (s *sshTestServer) handleConn(c net.Conn) {
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

	// 全局请求（keepalive 等）：全部回复成功
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		for req := range reqs {
			if req.WantReply {
				req.Reply(true, nil)
			}
		}
	}()

	// session 通道：接受 "session" 类型 + 所有请求回复成功
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		for newChan := range chans {
			if newChan.ChannelType() != "session" {
				_ = newChan.Reject(ssh.UnknownChannelType, "only session supported")
				continue
			}
			ch, requests, err := newChan.Accept()
			if err != nil {
				continue
			}
			connWg.Add(1)
			go func() {
				defer connWg.Done()
				defer ch.Close()
				for req := range requests {
					switch req.Type {
					case "shell", "exec", "pty-req", "env", "subsystem", "window-change":
						// 全部成功（不真的执行 shell）
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
		}
	}()

	_ = sconn.Wait()
}

func (s *sshTestServer) Close() {
	s.closeOnce.Do(func() {
		// 1. 取消 ctx → 所有 handleConn 收到信号，sconn.Close() → sconn.Wait() 返回
		s.cancel()
		// 2. 关 listener → acceptLoop 退出
		s.listener.Close()
		// 3. 等所有 goroutine 退出
		s.wg.Wait()
	})
}

// hostPort 返回 "host:port" 给 *sshclient.Connector.Dial 用。
func (s *sshTestServer) hostPort() string {
	return s.listener.Addr().String()
}

// hostAndPort 拆分成 host 和 numeric port，喂 session.OpenRequest。
func (s *sshTestServer) hostAndPort(t *testing.T) (string, int) {
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
// 桩：sftp factory（跳过真 SFTP subsystem 协商）
// -----------------------------------------------------------------------------

// newSftpFactory 构造一个 openSftp mock factory，递增 counter 计数。
//
// 返回的 *sftpclient.Client 是零值（sc=nil, sshClient=nil）——
// 单元测试不调 sftp 操作（spec 明确不做端到端 SFTP 传输测试），
// 仅验证 sftpFor 的 map 生命周期。
func newSftpFactory(counter *atomic.Int32) func(*ssh.Client) (*sftpclient.Client, error) {
	return func(_ *ssh.Client) (*sftpclient.Client, error) {
		counter.Add(1)
		return &sftpclient.Client{}, nil
	}
}

// errSftpFactory 构造一个总是返回 error 的 factory。
func errSftpFactory(msg string) func(*ssh.Client) (*sftpclient.Client, error) {
	return func(_ *ssh.Client) (*sftpclient.Client, error) {
		return nil, fmt.Errorf("mock sftp open: %s", msg)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// newTestApp 构造一个 *App 用于测试。
//
// 关键：sessions manager 是传入的 mm（caller 持有引用，便于测试中 Close / Get）；
// connectors 注入 ssh factory（真实 *sshclient.Connector dial 到 testServer）；
// openSftp 替换为 sftpFactory（跳过真 SFTP subsystem）。
func newTestApp(t *testing.T, mm *session.MemoryManager, sftpFactory func(*ssh.Client) (*sftpclient.Client, error)) *App {
	t.Helper()

	reg := connect.NewMemoryRegistry()
	if err := reg.Register("ssh", func(deps connect.Deps) (connect.Connector, error) {
		return sshclient.New(deps)
	}); err != nil {
		t.Fatalf("register ssh factory: %v", err)
	}
	mm.WithConnectors(reg)

	a := &App{
		sessions:    mm,
		connectors:  reg,
		sftpClients: make(map[session.ID]*sftpclient.Client),
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	a.openSftp = sftpFactory
	return a
}

// openEstablishedSession 用 mm 真实打开一个到 sshServer 的 session，
// 阻塞直到 state=Established（带 2s 超时）。
func openEstablishedSession(t *testing.T, mm *session.MemoryManager, sshServer *sshTestServer) session.Session {
	t.Helper()
	host, port := sshServer.hostAndPort(t)
	req := session.OpenRequest{
		Host:    host,
		Port:    port,
		User:    "test",
		Auth:    session.AuthSpec{Kind: "password", Password: "x"},
		Columns: 80,
		Rows:    24,
	}
	sess, err := mm.Open(context.Background(), req)
	if err != nil {
		t.Fatalf("mm.Open: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := sess.State(); st == session.StateEstablished {
			return sess
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session did not reach Established (state=%s)", sess.State())
	return nil
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

// TestSftpFor_Lifecycle 覆盖完整生命周期：
//
//	Open → sftpFor 建 → 复用 → 关闭 session → 重建
func TestSftpFor_Lifecycle(t *testing.T) {
	server := newSSHTestServer(t)
	mm := session.NewMemoryManager()

	var opens atomic.Int32
	a := newTestApp(t, mm, newSftpFactory(&opens))

	// 1. 打开第一个 session
	sess1 := openEstablishedSession(t, mm, server)
	id1 := sess1.Info().ID

	// 2. 第一次 sftpFor → 建 client
	c1, err := a.sftpFor(id1)
	if err != nil {
		t.Fatalf("sftpFor first: %v", err)
	}
	if c1 == nil {
		t.Fatal("sftpFor first: nil client")
	}
	if got := opens.Load(); got != 1 {
		t.Errorf("sftp open count = %d, want 1", got)
	}

	// 3. 第二次 sftpFor → 复用
	c2, err := a.sftpFor(id1)
	if err != nil {
		t.Fatalf("sftpFor second: %v", err)
	}
	if c1 != c2 {
		t.Error("sftpFor second: returned different instance (cache miss)")
	}
	if got := opens.Load(); got != 1 {
		t.Errorf("sftp open count = %d, want 1 (cache should be hit)", got)
	}

	// 4. 关闭 session
	if err := mm.Close(id1, true); err != nil {
		t.Fatalf("Close session: %v", err)
	}

	// 5. sftpFor on closed session → lazy evict + error
	//    session 已从 manager 移除 → "session not found"
	if _, err := a.sftpFor(id1); err == nil {
		t.Error("sftpFor on closed session: want error, got nil")
	}

	// 6. 重新打开 session → 新的 ID
	sess2 := openEstablishedSession(t, mm, server)
	id2 := sess2.Info().ID
	if id2 == id1 {
		t.Fatalf("reopened session has same ID %q (expected new UUID)", id1)
	}

	// 7. 新 session 的 sftpFor → 重建（新 mock client）
	c3, err := a.sftpFor(id2)
	if err != nil {
		t.Fatalf("sftpFor on new session: %v", err)
	}
	if c3 == nil {
		t.Fatal("sftpFor on new session: nil client")
	}
	if got := opens.Load(); got != 2 {
		t.Errorf("sftp open count = %d, want 2 (rebuild)", got)
	}
}

// TestSftpFor_Concurrent 10 个 goroutine 同时调 sftpFor。
// race detector 必须无报警。
//
// 验证：
//  1. 所有 goroutine 拿到的 client 是**同一个实例**（指针相等）
//  2. map 中只**一个** entry（其他 wasted open 的 client 被 closeSftpClients 逻辑里
//     的"二次检查"丢掉了 —— 见 sftpFor 内部"写回 map"段的 if existing check）
//  3. race detector 无报警
//
// 注意：sftp open 调用次数**可能**>1（两个 goroutine 同时 miss 后都调了 openSftp），
// 但只有一个 client 进 map。 wasted open 的 client 被 sftpFor 内
// 二次检查里 _ = client.Close() 关掉。spec 显式要求覆盖"concurrency" 即可，
// 不强制要求"零浪费"（那需要 sync.Once 之类的额外同步）。
func TestSftpFor_Concurrent(t *testing.T) {
	server := newSSHTestServer(t)
	mm := session.NewMemoryManager()

	var opens atomic.Int32
	a := newTestApp(t, mm, newSftpFactory(&opens))

	sess := openEstablishedSession(t, mm, server)
	id := sess.Info().ID

	const N = 10
	var wg sync.WaitGroup
	clients := make([]*sftpclient.Client, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c, err := a.sftpFor(id)
			clients[idx] = c
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// 1. 所有 client 是同一个实例
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d: %v", i, errs[i])
		}
		if clients[i] != clients[0] {
			t.Errorf("goroutine %d: got different client %p != %p", i, clients[i], clients[0])
		}
	}

	// 2. map 中只有 1 个 entry
	a.sftpMu.Lock()
	mapLen := len(a.sftpClients)
	a.sftpMu.Unlock()
	if mapLen != 1 {
		t.Errorf("sftpClients len = %d, want 1 (double-checked locking)", mapLen)
	}

	// 3. sftp open 至少被调 1 次（first-write-wins 最少 1 次）
	//     wasted opens（被二次检查关闭的）可能 > 0 —— 不强制
	if got := opens.Load(); got < 1 {
		t.Errorf("sftp open count = %d, want >= 1 (at least one cache miss → open)", got)
	}
}

// TestSftpFor_NonexistentSession 验证不存在的 sessionID 返回 error。
func TestSftpFor_NonexistentSession(t *testing.T) {
	mm := session.NewMemoryManager()
	a := newTestApp(t, mm, newSftpFactory(&(atomic.Int32{})))

	_, err := a.sftpFor("nonexistent-id")
	if err == nil {
		t.Error("sftpFor with nonexistent ID: want error, got nil")
	}
}

// TestSftpFor_SessionFailed 验证 session state=Failed 时 sftpFor 返回 error。
//
// 构造方法：open session 到一个不存在的端口 → dial 失败 → state=Failed
// （session 仍在 m.sessions，caller 可 Get + 决定是否 Close —— v0.2.0a 行为）。
func TestSftpFor_SessionFailed(t *testing.T) {
	mm := session.NewMemoryManager()

	// 必须先注册 ssh factory（即使我们故意 dial 失败，也需要 factory 返回 connector）
	reg := connect.NewMemoryRegistry()
	if err := reg.Register("ssh", func(deps connect.Deps) (connect.Connector, error) {
		return sshclient.New(deps)
	}); err != nil {
		t.Fatalf("register ssh factory: %v", err)
	}
	mm.WithConnectors(reg)

	// 用一个不存在的端口触发 dial 失败。
	// 注意：os 分配端口后立即释放可能给到不同进程，改为用一个显然不开放的端口。
	req := session.OpenRequest{
		Host:    "127.0.0.1",
		Port:    1, // privileged port, no listener
		User:    "test",
		Auth:    session.AuthSpec{Kind: "password", Password: "x"},
		Columns: 80,
		Rows:    24,
	}
	sess, err := mm.Open(context.Background(), req)
	if err != nil {
		t.Fatalf("mm.Open: %v", err)
	}

	// 等待 state=Failed
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess.State() == session.StateFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := sess.State(); st != session.StateFailed {
		t.Fatalf("session state = %s, want Failed", st)
	}

	// sftpFor should return error (state != Established)
	a := newTestApp(t, mm, newSftpFactory(&(atomic.Int32{})))
	if _, err := a.sftpFor(sess.Info().ID); err == nil {
		t.Error("sftpFor on Failed session: want error, got nil")
	}
}

// TestSftpFor_OpenSftpError 验证 openSftp 失败时 sftpFor 冒泡 error
// 且不污染 map。
func TestSftpFor_OpenSftpError(t *testing.T) {
	server := newSSHTestServer(t)
	mm := session.NewMemoryManager()

	a := newTestApp(t, mm, errSftpFactory("subsystem denied"))
	sess := openEstablishedSession(t, mm, server)
	id := sess.Info().ID

	_, err := a.sftpFor(id)
	if err == nil {
		t.Fatal("sftpFor: want error (openSftp failed), got nil")
	}

	// map 必须为空（openSftp 失败时不能写 map）
	a.sftpMu.Lock()
	if n := len(a.sftpClients); n != 0 {
		t.Errorf("sftpClients len = %d, want 0 (openSftp failed → no write)", n)
	}
	a.sftpMu.Unlock()
}

// TestSftpFor_ConnectorTypeAssertFailure 验证 session 的 connector 不是
// *sshclient.Connector 时返回明确错误（不是 panic、不是空指针）。
//
// 构造方法：注册一个返回 fakeConnector 的 factory。
// fakeConnector 嵌入 *sshclient.Connector（复用其 Dial/OpenSession 实现），
// 但本身是不同类型 → sftpFor 里 `connector.(*sshclient.Connector)` 失败。
func TestSftpFor_ConnectorTypeAssertFailure(t *testing.T) {
	server := newSSHTestServer(t)
	mm := session.NewMemoryManager()

	// 注册一个返回 fakeConnector 的 factory
	reg := connect.NewMemoryRegistry()
	if err := reg.Register("ssh", func(deps connect.Deps) (connect.Connector, error) {
		// 真实 sshclient.Connector（用 embed 复用）
		return &fakeConnector{Connector: nil, deps: deps}, nil
	}); err != nil {
		t.Fatalf("register ssh factory: %v", err)
	}
	mm.WithConnectors(reg)

	// 真实打开 session
	host, port := server.hostAndPort(t)
	req := session.OpenRequest{
		Host: host, Port: port, User: "test",
		Auth:    session.AuthSpec{Kind: "password", Password: "x"},
		Columns: 80, Rows: 24,
	}
	sess, err := mm.Open(context.Background(), req)
	if err != nil {
		t.Fatalf("mm.Open: %v", err)
	}
	// 等待 Established
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess.State() == session.StateEstablished {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != session.StateEstablished {
		t.Fatalf("session state = %s, want Established", sess.State())
	}

	// 构造 App（不要用 newTestApp 覆盖 factory）
	a := &App{
		sessions:    mm,
		connectors:  reg,
		sftpClients: make(map[session.ID]*sftpclient.Client),
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	a.openSftp = newSftpFactory(&(atomic.Int32{}))

	// sftpFor 必须返回 error，type assert 失败
	if _, err := a.sftpFor(sess.Info().ID); err == nil {
		t.Error("sftpFor: want error (connector type mismatch), got nil")
	}
}

// TestSftpFor_RawClientNil 验证 sshclient.Connector.RawClient() 返回 nil 时
// 返回明确错误。
//
// 构造方法：dial 成功后让 c.client 被外部清零。
// *sshclient.Connector 的 c.client 是 unexported —— 直接置 nil 需要
// 同包 helper。本次测试通过**重新 dial** 触发 *sshclient.Connector 的
// 行为（每次 Dial 覆盖 c.client）来近似：先 dial 一次拿到 client，
// 再 dial 一次让 c.client 被新值覆盖 —— 但这测试的是"两次 dial 行为"，
// 不是"RawClient() 返回 nil"，略偏离。
//
// 退而求其次：直接构造一个手动清零 c.client 的场景在本测试中做不到
// （c.client 是 unexported，需要 _test.go 的 helper）。
// **跳过本测试** —— spec 没明确要求，且实际触发条件是"Connector
// 被复用但从未 Dial"，session 路径下不会发生。
// func TestSftpFor_RawClientNil(t *testing.T) { t.Skip("requires test export") }

// TestAppClose_ClosesAllSftpClients 验证 App.Close 关闭所有缓存的 sftp client。
//
// 验证策略：
//   - openSftp factory 返回的 *sftpclient.Client 是零值（sc=nil）
//   - App.Close 调 c.Close() → sftpclient.Client.Close 在 sc=nil 时
//     立刻返回 nil（不报错，不 panic）
//   - 关键验证：map 被清空 + len == 0
//   - Close 被调用这一事实由 map 被清空 + 循环逻辑（见 closeSftpClients
//     源码：if c == nil continue; else c.Close()）共同证明
//   - 真 Close 的副作用（释放 SFTP subsystem channel）由
//     sftpclient 包的 unit test 覆盖（internal/sftpclient/client_test.go）
func TestAppClose_ClosesAllSftpClients(t *testing.T) {
	server := newSSHTestServer(t)
	mm := session.NewMemoryManager()

	a := newTestApp(t, mm, newSftpFactory(&(atomic.Int32{})))

	// 3 个 session × 各 1 个 sftp client
	const N = 3
	ids := make([]session.ID, N)
	for i := 0; i < N; i++ {
		sess := openEstablishedSession(t, mm, server)
		ids[i] = sess.Info().ID
		if _, err := a.sftpFor(ids[i]); err != nil {
			t.Fatalf("sftpFor session %d: %v", i, err)
		}
	}

	a.sftpMu.Lock()
	preLen := len(a.sftpClients)
	a.sftpMu.Unlock()
	if preLen != N {
		t.Fatalf("sftpClients len before Close = %d, want %d", preLen, N)
	}

	// App.Close
	a.Close()

	// map 必须被清空（closeSftpClients 拿新 map 替换 + 遍历旧 map 调 Close）
	a.sftpMu.Lock()
	postLen := len(a.sftpClients)
	a.sftpMu.Unlock()
	if postLen != 0 {
		t.Errorf("sftpClients len after Close = %d, want 0", postLen)
	}
}

// TestAppClose_NilSafe 验证 nil / 空 App 上调 Close 不 panic。
func TestAppClose_NilSafe(t *testing.T) {
	t.Run("nil App", func(t *testing.T) {
		var a *App
		a.Close() // must not panic
	})
	t.Run("empty App", func(t *testing.T) {
		a := &App{sftpClients: make(map[session.ID]*sftpclient.Client)}
		a.Close() // must not panic
	})
}

// TestSftpFor_MapEvictedOnClose 验证 close session 后 map entry 被 lazy evict。
func TestSftpFor_MapEvictedOnClose(t *testing.T) {
	server := newSSHTestServer(t)
	mm := session.NewMemoryManager()

	var opens atomic.Int32
	a := newTestApp(t, mm, newSftpFactory(&opens))

	sess := openEstablishedSession(t, mm, server)
	id := sess.Info().ID

	// 建 client
	if _, err := a.sftpFor(id); err != nil {
		t.Fatalf("sftpFor: %v", err)
	}
	a.sftpMu.Lock()
	preLen := len(a.sftpClients)
	a.sftpMu.Unlock()
	if preLen != 1 {
		t.Fatalf("sftpClients len = %d, want 1", preLen)
	}

	// 关闭 session
	if err := mm.Close(id, true); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 这次 sftpFor 失败（session 已被 Close 移除 from manager）
	if _, err := a.sftpFor(id); err == nil {
		t.Fatal("sftpFor on closed session: want error, got nil")
	}

	// map 已被 lazy evict（被删条目的代码路径：sftpFor 第一次拿 map 命中
	// → 拿 session 验状态 → session 不存在 → delete map）
	a.sftpMu.Lock()
	postLen := len(a.sftpClients)
	a.sftpMu.Unlock()
	if postLen != 0 {
		t.Errorf("sftpClients len after close = %d, want 0 (lazy evict)", postLen)
	}
}

// -----------------------------------------------------------------------------
// stub for TestSftpFor_ConnectorTypeAssertFailure
// -----------------------------------------------------------------------------

// fakeConnector 嵌入 *sshclient.Connector 但本身是不同类型 ——
// 触发 sftpFor 里的 type assert 失败路径。
//
// 字段：
//   - *sshclient.Connector 复用其 Dial/OpenSession 实现
//   - 第一次 Dial 时懒构造（保证 deps 注入正确）
type fakeConnector struct {
	*sshclient.Connector
	deps connect.Deps
	once sync.Once
}

func (f *fakeConnector) ensureInit() {
	f.once.Do(func() {
		c, err := sshclient.New(f.deps)
		if err != nil {
			panic(fmt.Sprintf("fakeConnector: sshclient.New: %v", err))
		}
		f.Connector = c
	})
}

func (f *fakeConnector) Dial(ctx context.Context, params connect.DialParams) (net.Conn, error) {
	f.ensureInit()
	return f.Connector.Dial(ctx, params)
}

func (f *fakeConnector) OpenSession(ctx context.Context, conn net.Conn, opts connect.SessionOpts) (connect.Session, error) {
	f.ensureInit()
	return f.Connector.OpenSession(ctx, conn, opts)
}
