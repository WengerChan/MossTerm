// manager_test.go 覆盖 v0.2.0a 引入的 Manager.Open 异步行为：
//  1. Open 立即返回（dial 慢也不阻塞）
//  2. subscriber 能看到 Connecting → Authenticating → Established 状态序列
//  3. dial 失败时 state=Failed 且 Info().State 可见
//  4. OpenSession 失败时 state=Failed 且 dialed conn 被 close
//  5. Close 在 dial 中途被调用时 goroutine 正常退出、conn 不泄漏
//  6. Info().State 总是反映当前 state（v0.2.0a 修复了 state 缓存 bug）
//
// mock 设计：testConnector + stubConn + stubSession 是 in-process 的可控桩。
// 通过 connect.MemoryRegistry 注册 "ssh" factory 返回 testConnector，
// 让 Manager 拿到一个完全可观测的 connector。
//
// 不覆盖（已说明）：
//   - 真实 SSH 拨号 / 鉴权流程：留 v0.2.1 integration test harness
//   - goroutine 泄漏的硬验证：留 race detector 跑 CI 时检测
package session

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mossterm/mossterm/internal/connect"
)

// -----------------------------------------------------------------------------
// 桩实现
// -----------------------------------------------------------------------------

// stubConn 是最小可用的 net.Conn 实现，仅供测试用。
//
// 不做真实 IO；Read 立即返回 io.EOF，Write 假装成功，Close 标记 closed。
// closed 字段用 atomic 是因为 conn.Close 可能在 conn 由 dialInBackground
// 持有的情况下并发调用（conn.Close vs goroutine 的 conn.Close）。
type stubConn struct {
	closed atomic.Bool
}

func (c *stubConn) Read(b []byte) (int, error)       { return 0, io.EOF }
func (c *stubConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *stubConn) Close() error                     { c.closed.Store(true); return nil }
func (c *stubConn) LocalAddr() net.Addr              { return stubAddr("local") }
func (c *stubConn) RemoteAddr() net.Addr             { return stubAddr("remote") }
func (c *stubConn) SetDeadline(time.Time) error      { return nil }
func (c *stubConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stubConn) SetWriteDeadline(time.Time) error { return nil }

type stubAddr string

func (a stubAddr) Network() string { return "stub" }
func (a stubAddr) String() string  { return string(a) }

// stubSession 是最小可用的 connect.Session 实现。
//
// Read 默认**阻塞**，直到 Close 被调用（或 t.Cleanup 触发 close）。
// 原因：v0.2.0a 起 readLoop 在 reader 退出时会自动调 sessionImpl.Close，
// 把 state 推到 Closed —— 立即返回 io.EOF 会让 readLoop 在 100ms 内
// 把测试观察窗口里的 Established 状态改成 Closed，破坏
// TestOpen_AsyncReturnsBeforeDial 这类"观察 Established 状态"测试。
// 真实 SSH session 的 Read 会一直阻塞等远端数据，正好符合这里的需求。
type stubSession struct {
	conn     net.Conn
	closed   atomic.Bool
	readDone chan struct{} // 闭包后 Read 返回 EOF
}

func newStubSession(conn net.Conn) *stubSession {
	return &stubSession{conn: conn, readDone: make(chan struct{})}
}

func (s *stubSession) Read(b []byte) (int, error) {
	<-s.readDone
	return 0, io.EOF
}
func (s *stubSession) Write(b []byte) (int, error) { return len(b), nil }
func (s *stubSession) Close() error {
	s.closed.Store(true)
	select {
	case <-s.readDone:
		// 已 closed
	default:
		close(s.readDone)
	}
	return nil
}
func (s *stubSession) Resize(cols, rows int) error { return nil }
func (s *stubSession) ShellPID() int               { return 0 }

// testConnector 是可配置行为的 connect.Connector 桩。
//
// dialDelay / openDelay 用 time.After 模拟慢网络（同时支持 ctx 取消）。
// dialErr / openErr 让 Dial / OpenSession 返回指定错误，验证失败路径。
// lastDialed 在 Dial 成功时记录返回的 stubConn，让测试断言 conn 被 close。
type testConnector struct {
	dialDelay time.Duration
	dialErr   error
	openDelay time.Duration
	openErr   error

	dialCalls  atomic.Int32
	openCalls  atomic.Int32
	lastDialed atomic.Pointer[stubConn]
}

func (c *testConnector) Dial(ctx context.Context, params connect.DialParams) (net.Conn, error) {
	c.dialCalls.Add(1)
	if c.dialDelay > 0 {
		select {
		case <-time.After(c.dialDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.dialErr != nil {
		return nil, c.dialErr
	}
	sc := &stubConn{}
	c.lastDialed.Store(sc)
	return sc, nil
}

func (c *testConnector) OpenSession(ctx context.Context, conn net.Conn, opts connect.SessionOpts) (connect.Session, error) {
	c.openCalls.Add(1)
	if c.openDelay > 0 {
		select {
		case <-time.After(c.openDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.openErr != nil {
		return nil, c.openErr
	}
	return newStubSession(conn), nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// newTestManager 用 testConnector 构造一个可测的 Manager。
//
// 步骤：构造 registry → 注册 "ssh" factory → 注入到 Manager。
// 返回 manager（caller 用 Open / Get / Close 测试）。
func newTestManager(t *testing.T, tc *testConnector) *MemoryManager {
	t.Helper()
	reg := connect.NewMemoryRegistry()
	if err := reg.Register("ssh", func(deps connect.Deps) (connect.Connector, error) {
		return tc, nil
	}); err != nil {
		t.Fatalf("register test factory: %v", err)
	}
	return NewMemoryManager().WithConnectors(reg)
}

// standardOpenRequest 构造一个最小可用的 OpenRequest（密码认证）。
func standardOpenRequest() OpenRequest {
	return OpenRequest{
		Host:    "example.com",
		Port:    22,
		User:    "test",
		Auth:    AuthSpec{Kind: "password", Password: "x"},
		Columns: 80,
		Rows:    24,
	}
}

// waitForState 在 timeout 内轮询 sess.Info().State == want。
// 找不到则 t.Fatal。
func waitForState(t *testing.T, sess Session, want State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s := sess.Info().State; s == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := sess.Info().State
	t.Fatalf("waitForState: timeout after %v, got %s, want %s", timeout, got, want)
}

// waitForStateEvent 在 timeout 内等待 subCh 收到 type=state 且 State==want 的事件。
// 非 state 事件 / 其它 state 值直接跳过。
// 找不到则 t.Fatal。
func waitForStateEvent(t *testing.T, subCh <-chan Event, want State, timeout time.Duration) Event {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev := <-subCh:
			if ev.Type == "state" && ev.State == want {
				return ev
			}
		case <-timer.C:
			t.Fatalf("waitForStateEvent: timeout waiting for state=%s", want)
			return Event{}
		}
	}
}

// waitForTrue 在 timeout 内轮询 cond()，返回 true 即通过。
// 超时则 t.Fatalf(format, ...)。
func waitForTrue(t *testing.T, timeout time.Duration, cond func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	args = append(args, timeout)
	t.Fatalf("waitForTrue timeout after %v: "+format, args...)
}

// -----------------------------------------------------------------------------
// 测试
// -----------------------------------------------------------------------------

// TestOpen_AsyncReturnsBeforeDial 验证 Open 在 dial 慢的情况下立即返回，
// subscriber 能看到 Connecting → Authenticating → Established 完整状态序列。
//
// 1. testConnector.dialDelay = 100ms（Open 同步阶段远小于此）
// 2. 调 Open，期望 < 50ms 返回（v0.1.x 同步 dial 会卡 100ms+）
// 3. 立即 Subscribe，验证收到 StateAuthenticating + StateEstablished 事件
// 4. 最终 Info().State == StateEstablished
// 5. 验证 connector.Dial 和 connector.OpenSession 都被精确调用 1 次
func TestOpen_AsyncReturnsBeforeDial(t *testing.T) {
	tc := &testConnector{dialDelay: 100 * time.Millisecond}
	mm := newTestManager(t, tc)

	start := time.Now()
	sess, err := mm.Open(context.Background(), standardOpenRequest())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("Open took %v, want < 50ms (dial should be async)", elapsed)
	}

	// Open 返回时 session 已注册；Get 应能找到
	if _, ok := mm.Get(sess.Info().ID); !ok {
		t.Error("Get(sessionID) failed immediately after Open returned")
	}

	// 立即 Subscribe，应能看到 Authenticating → Established
	// 注意：StateConnecting 初始值在 Open 同步阶段设到 s.state，
	// 但没有 publish state event（init state 不算 transition）。
	// 所以 subscriber 看到的 state 事件序列从 Authenticating 开始。
	subCh, cancel := sess.Subscribe()
	defer cancel()

	waitForStateEvent(t, subCh, StateAuthenticating, 500*time.Millisecond)
	waitForStateEvent(t, subCh, StateEstablished, 500*time.Millisecond)

	// 最终态
	waitForState(t, sess, StateEstablished, 100*time.Millisecond)

	// 验证 connector.Dial 和 connector.OpenSession 都被调了
	if got := tc.dialCalls.Load(); got != 1 {
		t.Errorf("dialCalls = %d, want 1", got)
	}
	if got := tc.openCalls.Load(); got != 1 {
		t.Errorf("openCalls = %d, want 1", got)
	}
}

// TestOpen_DialFailure_VisibleViaInfo 验证 dial 失败时：
//   - state=Failed
//   - session 仍在 m.sessions（caller 可 Get）
//   - subscriber 收到 StateFailed 事件
//   - connector.OpenSession 不被调用（dial 已失败）
func TestOpen_DialFailure_VisibleViaInfo(t *testing.T) {
	tc := &testConnector{
		dialDelay: 20 * time.Millisecond,
		dialErr:   errors.New("boom"),
	}
	mm := newTestManager(t, tc)

	sess, err := mm.Open(context.Background(), standardOpenRequest())
	if err != nil {
		t.Fatalf("Open: %v (should be nil, dial is async)", err)
	}

	subCh, cancel := sess.Subscribe()
	defer cancel()

	waitForState(t, sess, StateFailed, 500*time.Millisecond)
	waitForStateEvent(t, subCh, StateFailed, 100*time.Millisecond)

	// session 仍在 registry（caller 可 Get 后决定是否 Close）
	if _, ok := mm.Get(sess.Info().ID); !ok {
		t.Error("Get(sessionID) should succeed after dial failure")
	}

	// OpenSession 不应被调用（dial 已失败）
	if got := tc.openCalls.Load(); got != 0 {
		t.Errorf("openCalls = %d, want 0 (dial failed)", got)
	}
}

// TestOpen_OpenSessionFailure_VisibleViaInfo 验证 OpenSession 失败时：
//   - state=Failed
//   - dial 阶段分配的 conn 被 close（无泄漏）
func TestOpen_OpenSessionFailure_VisibleViaInfo(t *testing.T) {
	tc := &testConnector{
		dialDelay: 10 * time.Millisecond,
		openErr:   errors.New("auth rejected"),
	}
	mm := newTestManager(t, tc)

	sess, err := mm.Open(context.Background(), standardOpenRequest())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	waitForState(t, sess, StateFailed, 500*time.Millisecond)

	// dial 成功 → conn 已分配，OpenSession 失败时 conn 应被 close
	sc := tc.lastDialed.Load()
	if sc == nil {
		t.Fatal("test setup: lastDialed should be non-nil (dial must have succeeded)")
	}
	// 给 dialInBackground 一点时间调 conn.Close()
	waitForTrue(t, 100*time.Millisecond, sc.closed.Load,
		"dialed conn was not closed after OpenSession failure (leak!)")
}

// TestOpen_CloseDuringDial_NoLeak 验证 Close 在 dial 中途被调用时：
//   - dialInBackground 在 dial 完成后看到 Closed，关闭已分配的 conn 然后退出
//   - 最终 state=Closed
//   - session 已从 m.sessions 移除
func TestOpen_CloseDuringDial_NoLeak(t *testing.T) {
	tc := &testConnector{
		dialDelay: 100 * time.Millisecond, // 长到能在中间 close
	}
	mm := newTestManager(t, tc)

	sess, err := mm.Open(context.Background(), standardOpenRequest())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// 等 dial 真正开始（避免在 dial 真正开始前就 close）
	time.Sleep(20 * time.Millisecond)

	// 在 dial 中途 Close
	if err := mm.Close(sess.Info().ID, true); err != nil {
		t.Errorf("Close: %v", err)
	}

	// 等 dial 退出（dial 100ms 后完成；goroutine 看到 state=Closed 应立即释放并退出）
	// 留 200ms 余量防止 CI 抖动
	time.Sleep(200 * time.Millisecond)

	// 最终 state = Closed（不是 Established 也不是 Failed）
	waitForState(t, sess, StateClosed, 100*time.Millisecond)

	// session 已从 m.sessions 移除
	if _, ok := mm.Get(sess.Info().ID); ok {
		t.Error("Get should return false after Close")
	}

	// dial 完成后 conn 仍应被 close（dialInBackground 在看到 Closed 时释放）
	// lastDialed 可能为 nil（如果 close 在 dial 真正开始前被调，dial 立刻看到
	// ctx.Err?不，ctx 不会被 cancel）—— 这里 dialDelay=100ms，close 在 20ms 时
	// 调，dial 在 100ms 完成，dialInBackground 此时看到 state=Closed，
	// 调用 conn.Close()。
	sc := tc.lastDialed.Load()
	if sc == nil {
		t.Fatal("test setup: lastDialed should be non-nil (dial completed before close path ran)")
	}
	waitForTrue(t, 100*time.Millisecond, sc.closed.Load,
		"dialed conn was not closed when Close interrupted dial (leak!)")
}

// TestOpen_Info_StateIsCurrent 验证 Info().State 总是返回当前 state
// （v0.2.0a 修复：info 字段不再缓存 state；之前状态转换后 Info().State
// 仍是初始值 Connecting，前端看不到 Authenticating/Established）。
func TestOpen_Info_StateIsCurrent(t *testing.T) {
	tc := &testConnector{dialDelay: 30 * time.Millisecond}
	mm := newTestManager(t, tc)

	sess, err := mm.Open(context.Background(), standardOpenRequest())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Open 返回时 state 至少应是 Connecting（goroutine 还没来得及跑）
	initial := sess.Info().State
	if initial != StateConnecting && initial != StateAuthenticating && initial != StateEstablished {
		t.Errorf("initial Info().State = %s, want Connecting/Authenticating/Established", initial)
	}

	// 等到 Established
	waitForState(t, sess, StateEstablished, 500*time.Millisecond)

	// 关键回归点：再次 Info() 仍然要返回 Established
	if got := sess.Info().State; got != StateEstablished {
		t.Errorf("Info().State after settle = %s, want StateEstablished", got)
	}
}

// TestOpen_SubscribeAfterOpen_SeesAuthenticating 验证 subscriber 在 Open
// 返回后立即 Subscribe（fanoutLoop 已起），能捕获到 dial 期间的 state
// 转换事件（这是 v0.2.0a 的核心新行为 —— v0.1.x subscriber 永远看不到
// Connecting/Authenticating，因为 Open 同步 dial 后才返回）。
func TestOpen_SubscribeAfterOpen_SeesAuthenticating(t *testing.T) {
	tc := &testConnector{dialDelay: 50 * time.Millisecond}
	mm := newTestManager(t, tc)

	sess, err := mm.Open(context.Background(), standardOpenRequest())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Open 返回后立即 Subscribe —— v0.1.x 永远拿不到 Authenticating 事件
	subCh, cancel := sess.Subscribe()
	defer cancel()

	// 收集所有 state 事件直到看到 Established
	states := []State{}
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
collect:
	for {
		select {
		case ev := <-subCh:
			if ev.Type == "state" {
				states = append(states, ev.State)
				if ev.State == StateEstablished {
					break collect
				}
			}
		case <-timer.C:
			break collect
		}
	}

	// 至少要看到 Authenticating 和 Established（Connecting 是初始值，不 publish）
	hasAuth := false
	hasEst := false
	for _, st := range states {
		if st == StateAuthenticating {
			hasAuth = true
		}
		if st == StateEstablished {
			hasEst = true
		}
	}
	if !hasAuth {
		t.Errorf("subscriber missed StateAuthenticating, got sequence: %v", states)
	}
	if !hasEst {
		t.Errorf("subscriber missed StateEstablished, got sequence: %v", states)
	}

	// 顺序必须是 Authenticating → Established（不能反过来）
	for i, st := range states {
		if st == StateEstablished && i > 0 {
			// 检查之前是否见过 Authenticating
			prev := states[:i]
			sawAuthBefore := false
			for _, p := range prev {
				if p == StateAuthenticating {
					sawAuthBefore = true
				}
			}
			if !sawAuthBefore {
				t.Errorf("StateEstablished appeared before StateAuthenticating: %v", states)
			}
		}
	}
}
