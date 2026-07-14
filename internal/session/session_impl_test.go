// session_impl_test.go 覆盖 v0.2 引入的两个新行为：
//   1. tryPublish 在 events 通道满时累加 overflowBytes + 置位 overflowPending
//   2. fanoutLoop 在 broadcast 后通过 maybeEmitOverflow 发出 overflow 事件
//
// v0.2.3 扩展：sub drop 累加（mirror overflow 机制）
//   1. broadcast 在 sub channel 满时累加 subDropBytes + 置位 subDropPending
//   2. fanoutLoop 在 broadcast 后通过 maybeEmitSubOverflow 发出 sub:overflow 事件
//   3. 防递归不变量：sub:overflow / overflow / state / exit / error 事件被 drop
//      时**不**累加（Data 字段为空），sub:overflow 自身 drop 也不递归
//
// v0.2.4 扩展：publishMu 序列化"setState + tryPublish" + Close，消除 stale event race
//   1. setStateAndPublishIf CAS + publish 在 publishMu 内执行
//   2. setStateAndPublish 无条件 set + publish 在 publishMu 内执行
//   3. 关键不变量：subscriber 看到的 state 事件序列单调——
//      一旦收到 Closing/Closed，不会再出现 Connecting/Authenticating/Established
//   4. 多次并发 Close 安全（closeOnce 保证 Closing 事件只发一次）
//
// 不覆盖：
//   - readLoop 批处理行为：要起真实 SSH server + 喂可控速率的字节流，
//     留给 v0.2.1 integration test harness
//   - 端到端 "cat large.log → 触发 overflow 事件" 路径：同上
//
// 测试构造 sessionImpl 的方式：直接零值 + 手动填必要字段。
// 同包测试能访问 unexported 字段（overflowBytes / overflowPending /
// subDropBytes / subDropPending），正是我们要验证的对象。
package session

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// newTestSession 构造一个最小可用的 sessionImpl，仅供 tryPublish /
// fanoutLoop 行为的单测使用。
//
// 注意：subs map 必须非 nil（fanoutLoop 不读，但 broadcast 会迭代它）；
// events 容量由调用方指定（测试里用小 cap 容易触发溢出）；
// log 用 io.Discard 避免污染测试输出。
func newTestSession(eventsCap int) *sessionImpl {
	return &sessionImpl{
		events: make(chan Event, eventsCap),
		done:   make(chan struct{}),
		subs:   make(map[int]chan Event),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestTryPublish_OverflowIncrement 验证 events 通道满时：
//   - overflowBytes 累加被丢弃 data 的字节数
//   - overflowPending 置位
//   - 非 data 事件（state / exit / error）不计入 overflow
func TestTryPublish_OverflowIncrement(t *testing.T) {
	s := newTestSession(2)

	// 填满 events 通道：前 2 个 data 事件必须能直接投递。
	s.tryPublish(newDataEvent([]byte("aaa")))
	s.tryPublish(newDataEvent([]byte("bb")))

	// 第 3 个 data 事件：events 满 → 累加到 overflow。
	s.tryPublish(newDataEvent([]byte("12345")))

	if got := s.overflowBytes.Load(); got != 5 {
		t.Errorf("overflowBytes = %d, want 5 (3rd data event len)", got)
	}
	if !s.overflowPending.Load() {
		t.Error("overflowPending should be true after overflow")
	}

	// 第 4 个 data 事件：继续累加。
	s.tryPublish(newDataEvent([]byte("xxxxxx")))

	if got := s.overflowBytes.Load(); got != 11 {
		t.Errorf("overflowBytes = %d, want 11 (5 + 6)", got)
	}
}

// TestTryPublish_NonDataEventsDoNotIncrement 验证 state / exit / error
// 事件被丢弃时不计入 overflow（它们的丢失对终端用户无可见影响）。
func TestTryPublish_NonDataEventsDoNotIncrement(t *testing.T) {
	s := newTestSession(1)

	// 填满 events 通道。
	s.tryPublish(newDataEvent([]byte("x")))

	// 后续非 data 事件都应被静默丢弃、不计入 overflow。
	s.tryPublish(newStateEvent(StateClosing))
	s.tryPublish(newStateEvent(StateClosed))
	s.tryPublish(newExitEvent("EOF"))
	s.tryPublish(newErrorEvent(io.ErrUnexpectedEOF))

	if got := s.overflowBytes.Load(); got != 0 {
		t.Errorf("overflowBytes = %d, want 0 (non-data events must not increment)", got)
	}
	if s.overflowPending.Load() {
		t.Error("overflowPending should remain false for non-data overflow")
	}
}

// TestTryPublish_SessionClosed_DoesNotIncrement 验证 s.done 关闭后
// tryPublish 不计入 overflow（区别于"events 满"，done 关闭是正常关停）。
func TestTryPublish_SessionClosed_DoesNotIncrement(t *testing.T) {
	s := newTestSession(0) // cap=0 任何 send 都会立刻失败
	close(s.done)

	s.tryPublish(newDataEvent([]byte("anything")))

	if got := s.overflowBytes.Load(); got != 0 {
		t.Errorf("overflowBytes = %d, want 0 (done-closed publish is not overflow)", got)
	}
	if s.overflowPending.Load() {
		t.Error("overflowPending should remain false when session is closing")
	}
}

// TestMaybeEmitOverflow_BroadcastsEvent 验证 overflowPending 置位后，
// maybeEmitOverflow 会清空计数器并通过 broadcast 发出 overflow 事件。
//
// 实现思路：注册一个 sub 收集事件，触发 tryPublish 制造 overflow，
// 调 maybeEmitOverflow，验证 sub 收到一个 type=overflow 且
// OverflowBytes 正确的 Event。
func TestMaybeEmitOverflow_BroadcastsEvent(t *testing.T) {
	s := newTestSession(1)

	subCh, cancel := s.Subscribe()
	defer cancel()

	// 填满 events 通道
	s.tryPublish(newDataEvent([]byte("fill")))

	// 制造 overflow（42 字节）
	s.tryPublish(newDataEvent(make([]byte, 42)))

	if !s.overflowPending.Load() {
		t.Fatal("setup: overflowPending should be set after forced overflow")
	}

	// 触发 emit
	s.maybeEmitOverflow()

	// 验证 sub 收到 overflow 事件
	select {
	case ev := <-subCh:
		if ev.Type != string(EventTypeOverflow) {
			t.Errorf("event type = %q, want %q", ev.Type, EventTypeOverflow)
		}
		if ev.OverflowBytes != 42 {
			t.Errorf("OverflowBytes = %d, want 42", ev.OverflowBytes)
		}
		if ev.At == 0 {
			t.Error("overflow event should have non-zero At timestamp")
		}
	default:
		t.Fatal("sub did not receive overflow event")
	}

	// 验证计数器已清零
	if got := s.overflowBytes.Load(); got != 0 {
		t.Errorf("overflowBytes after emit = %d, want 0", got)
	}
	if s.overflowPending.Load() {
		t.Error("overflowPending should be false after maybeEmitOverflow")
	}
}

// TestMaybeEmitOverflow_NotPending_NoBroadcast 验证无 pending 时
// maybeEmitOverflow 是 no-op，不发任何事件。
func TestMaybeEmitOverflow_NotPending_NoBroadcast(t *testing.T) {
	s := newTestSession(4)

	subCh, cancel := s.Subscribe()
	defer cancel()

	// 不制造 overflow，直接调
	s.maybeEmitOverflow()

	// 给 goroutine 一个微小时刻（broadcast 是非阻塞立即执行，这里其实不需要）
	select {
	case ev := <-subCh:
		t.Errorf("unexpected event: %+v", ev)
	default:
		// 预期：没有事件
	}
}

// TestMaybeEmitOverflow_ConcurrentSafe 验证 tryPublish 和 maybeEmitOverflow
// 并发调用时 atomic 字段不丢字节（lock-free 安全性）。
//
// 测试逻辑：起 N 个 goroutine 并发 tryPublish（制造 overflow）；
// wg.Wait() 之后所有 publish 已落库（Add+Store happens-before wg.Done）；
// 主 goroutine 再调一次 maybeEmitOverflow 把最后的累计 drain 出来。
// 由于 wg.Wait() 之后的 maybeEmitOverflow 是单线程的，subCh 收到的
// overflow 事件 OverflowBytes 总和应等于所有 publish 的 data 字节总和。
//
// 不变量：events cap 极小（1），所有 publish 都进 overflow。
func TestMaybeEmitOverflow_ConcurrentSafe(t *testing.T) {
	const (
		eventsCap       = 1
		pubGoroutines   = 4
		pubsPerGoroutine = 100
		payloadSize     = 7
	)

	s := newTestSession(eventsCap)
	// 填满 events 通道：让所有后续 publish 都走 default 分支 → overflow
	for i := 0; i < eventsCap; i++ {
		s.tryPublish(newDataEvent([]byte("fill")))
	}

	subCh, cancel := s.Subscribe()
	defer cancel()

	// 并发 publish
	var wg sync.WaitGroup
	wg.Add(pubGoroutines)
	for g := 0; g < pubGoroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < pubsPerGoroutine; i++ {
				s.tryPublish(newDataEvent(make([]byte, payloadSize)))
			}
		}()
	}
	wg.Wait()

	// 全部 publish 完成后做一次最终 emit。
	// 此时 overflowPending 应为 true（最后一次 publish 置位）；
	// overflowBytes 应为 pubGoroutines * pubsPerGoroutine * payloadSize。
	expected := int64(pubGoroutines * pubsPerGoroutine * payloadSize)
	if got := s.overflowBytes.Load(); got != expected {
		t.Fatalf("pre-emit overflowBytes = %d, want %d (atomic Add should be lossless)", got, expected)
	}

	s.maybeEmitOverflow()

	// 收 overflow 事件
	var totalDropped int64
	select {
	case ev := <-subCh:
		if ev.Type != string(EventTypeOverflow) {
			t.Fatalf("event type = %q, want %q", ev.Type, EventTypeOverflow)
		}
		totalDropped = ev.OverflowBytes
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for overflow event")
	}

	if totalDropped != expected {
		t.Errorf("total dropped = %d, want %d", totalDropped, expected)
	}

	// 验证后置状态
	if got := s.overflowBytes.Load(); got != 0 {
		t.Errorf("post-emit overflowBytes = %d, want 0", got)
	}
	if s.overflowPending.Load() {
		t.Error("post-emit overflowPending should be false")
	}
}

// -----------------------------------------------------------------------------
// v0.2.3 新增：sub drop 累加 + maybeEmitSubOverflow
// -----------------------------------------------------------------------------

// TestBroadcast_SubDropAccumulate 验证 broadcast 在 sub channel 满时：
//   - subDropBytes 累加被丢弃 data 的字节数
//   - subDropPending 置位
//
// 模拟"sub 永远满"的方式：cap=0 的 channel（无 receiver），
// 任何 select send 都直接走 default 分支。
func TestBroadcast_SubDropAccumulate(t *testing.T) {
	s := newTestSession(2)

	// 模拟 broadcast 时 sub 队列满：cap=0 的 channel + 无 receiver。
	s.subs[1] = make(chan Event, 0)

	// broadcast 一个 5 字节的 data 事件
	s.broadcast(newDataEvent([]byte("12345")))

	if got := s.subDropBytes.Load(); got != 5 {
		t.Errorf("subDropBytes = %d, want 5", got)
	}
	if !s.subDropPending.Load() {
		t.Error("subDropPending should be true after sub drop")
	}

	// 继续 broadcast：累加应该持续生效
	s.broadcast(newDataEvent([]byte("xxxxxx")))

	if got := s.subDropBytes.Load(); got != 11 {
		t.Errorf("subDropBytes = %d, want 11 (5 + 6)", got)
	}
}

// TestBroadcast_SubDropNotForNonDataEvents 验证 state / exit / error
// 事件被 sub drop 时**不**计入 sub drop（与 v0.2.0 overflow 一致的不变量）。
func TestBroadcast_SubDropNotForNonDataEvents(t *testing.T) {
	s := newTestSession(2)

	// 永远满的 sub
	s.subs[1] = make(chan Event, 0)

	// 各种非 data 事件被 drop 都应静默、不计入 sub drop
	s.broadcast(newStateEvent(StateClosing))
	s.broadcast(newStateEvent(StateClosed))
	s.broadcast(newExitEvent("EOF"))
	s.broadcast(newErrorEvent(io.ErrUnexpectedEOF))

	if got := s.subDropBytes.Load(); got != 0 {
		t.Errorf("subDropBytes = %d, want 0 (non-data events must not increment)", got)
	}
	if s.subDropPending.Load() {
		t.Error("subDropPending should remain false for non-data sub drop")
	}
}

// TestBroadcast_SubOverflowEventDoesNotRecurse 是 v0.2.3 关键不变量测试：
// sub:overflow 事件自身被 sub drop 时**不**累加，避免"广播 drop 通知 →
// 又被 drop → 再广播 drop 通知..."的递归（最坏情况：sub:overflow 事件
// 被 drop 一次就丢失，副作用为零）。
//
// 验证 sub:overflow 事件走完 broadcast 完整路径后，subDropBytes 仍为 0。
func TestBroadcast_SubOverflowEventDoesNotRecurse(t *testing.T) {
	s := newTestSession(2)

	// 永远满的 sub
	s.subs[1] = make(chan Event, 0)

	// broadcast 一个 sub:overflow 事件（自身 Data 字段为空）
	s.broadcast(newSubOverflowEvent(999))

	// 不应累加 subDropBytes
	if got := s.subDropBytes.Load(); got != 0 {
		t.Errorf("subDropBytes = %d, want 0 (sub:overflow event must not increment on its own drop)", got)
	}
	if s.subDropPending.Load() {
		t.Error("subDropPending should remain false (sub:overflow event must not set on its own drop)")
	}

	// 同样，overflow 事件被 sub drop 也不应累加（它是 v0.2.0 的元事件）
	s.broadcast(newOverflowEvent(999))
	if got := s.subDropBytes.Load(); got != 0 {
		t.Errorf("subDropBytes = %d, want 0 (overflow event must not increment)", got)
	}
	if s.subDropPending.Load() {
		t.Error("subDropPending should remain false (overflow event must not set)")
	}
}

// TestBroadcast_SubDropMultipleSubs 验证多 sub 场景：只有"永远满"的 sub
// 贡献 drop 计数，正常的 sub 接收事件成功。
func TestBroadcast_SubDropMultipleSubs(t *testing.T) {
	s := newTestSession(2)

	// sub[1] 永远满（无 receiver 的 unbuffered channel）
	s.subs[1] = make(chan Event, 0)
	// sub[2] 正常（cap=64 buffered）
	s.subs[2] = make(chan Event, 64)

	s.broadcast(newDataEvent([]byte("12345")))

	// 整体 sub drop 计数 = 5（只来自 sub[1] 的 drop）
	if got := s.subDropBytes.Load(); got != 5 {
		t.Errorf("subDropBytes = %d, want 5 (only sub[1] dropped)", got)
	}
	if !s.subDropPending.Load() {
		t.Error("subDropPending should be true")
	}

	// sub[2] 应该收到那条 data 事件
	if got := len(s.subs[2]); got != 1 {
		t.Errorf("sub[2] queue len = %d, want 1 (normal sub should receive)", got)
	}
}

// TestMaybeEmitSubOverflow_BroadcastsEvent 验证 subDropPending 置位后，
// maybeEmitSubOverflow 会清空计数器并通过 broadcast 发出 sub:overflow 事件。
//
// 实现思路：注册一个 sub 收集事件，故意制造 sub drop，调
// maybeEmitSubOverflow，验证 sub 收到 type=sub:overflow 且
// OverflowBytes 正确的 Event（中间夹带的 data 事件先 drain）。
func TestMaybeEmitSubOverflow_BroadcastsEvent(t *testing.T) {
	s := newTestSession(2)

	// 正常 sub 用于接收事件
	subCh, cancel := s.Subscribe()
	defer cancel()

	// 永远满的 sub 制造 sub drop（直接挂到 map 上，绕开 Subscribe）
	s.subs[99] = make(chan Event, 0)

	// broadcast 一个 5 字节 data 事件
	s.broadcast(newDataEvent([]byte("12345")))

	if !s.subDropPending.Load() {
		t.Fatal("setup: subDropPending should be set after sub drop")
	}

	// 触发 emit
	s.maybeEmitSubOverflow()

	// sub 收到两个事件：先 data 后 sub:overflow
	// 第一个：原始 data 事件
	select {
	case ev := <-subCh:
		if ev.Type != string(EventTypeData) {
			t.Fatalf("first event type = %q, want %q", ev.Type, EventTypeData)
		}
		if string(ev.Data) != "12345" {
			t.Errorf("first event data = %q, want %q", ev.Data, "12345")
		}
	default:
		t.Fatal("sub did not receive the original data event")
	}

	// 第二个：sub:overflow 事件
	select {
	case ev := <-subCh:
		if ev.Type != string(EventTypeSubOverflow) {
			t.Errorf("event type = %q, want %q", ev.Type, EventTypeSubOverflow)
		}
		if ev.OverflowBytes != 5 {
			t.Errorf("OverflowBytes = %d, want 5", ev.OverflowBytes)
		}
		if ev.At == 0 {
			t.Error("sub:overflow event should have non-zero At timestamp")
		}
	default:
		t.Fatal("sub did not receive sub:overflow event")
	}

	// 验证计数器已清零
	if got := s.subDropBytes.Load(); got != 0 {
		t.Errorf("subDropBytes after emit = %d, want 0", got)
	}
	if s.subDropPending.Load() {
		t.Error("subDropPending should be false after maybeEmitSubOverflow")
	}
}

// TestMaybeEmitSubOverflow_NotPending_NoBroadcast 验证无 pending 时
// maybeEmitSubOverflow 是 no-op，不发任何事件。
func TestMaybeEmitSubOverflow_NotPending_NoBroadcast(t *testing.T) {
	s := newTestSession(2)

	subCh, cancel := s.Subscribe()
	defer cancel()

	// 不制造 sub drop，直接调
	s.maybeEmitSubOverflow()

	select {
	case ev := <-subCh:
		t.Errorf("unexpected event: %+v", ev)
	default:
		// 预期：没有事件
	}
}

// -----------------------------------------------------------------------------
// v0.2.4 新增：publishMu 序列化"setState + tryPublish"，消除 stale event race
//
// 关键不变量（详见 publishMu 字段注释）：
//
//	subscriber 看到的所有 state 事件都是单调的——
//	一旦收到 Closing 或 Closed，**之后不会**再收到 Authenticating / Established。
//
// v0.2.1 的实现有 race：
//	goroutine A: setStateIf(Connecting, Authenticating) CAS 成功
//	goroutine B: setState(Closing) → setState(Closed)          抢先
//	goroutine A: tryPublish(Authenticating)                    过期事件被发出
//
// v0.2.4 的修复：setState + tryPublish 合并在 publishMu 内执行，Close 也
// 必须拿 publishMu——两者的 state.Store + tryPublish 是原子的。
// -----------------------------------------------------------------------------

// startFanoutLoop 在 goroutine 中启动 s.fanoutLoop()，返回 stop 函数。
//
// v0.2.4 的新测试需要事件从 s.events 通道经 fanoutLoop 转发到 subscriber
// 通道（老测试用 maybeEmitOverflow 直接调 broadcast，绕过 events 通道）。
// 启动 fanoutLoop 后：setStateAndPublish* → s.events → fanoutLoop → broadcast → sub。
//
// stop 关闭 s.done 让 fanoutLoop 退出；测试通常用 defer 调 stop。
// 注意：如果测试已通过 s.Close(true) 关停 session，不要重复调 stop（会双关 done）。
func startFanoutLoop(s *sessionImpl) func() {
	go s.fanoutLoop()
	return func() {
		s.doneOnce.Do(func() { close(s.done) })
	}
}

// stateEventsTimeout 收集 subCh 收到的所有 type=state 事件，按收到顺序返回。
//
// 实现注意：subCh 可能被 s.Close(true) 关闭。Go 的关闭 channel 上的 receive
// 永远立即成功（零值），所以不能简单用 default 分支（会无限循环）。
// 改用 timeout：到时间或收不到新事件时返回。
//
// 第二个返回值 lastSeenAt 是最后一次收到事件的时间（time.Time），用于
// 调用方判断"已经 drain 完"还是"还有事件在路上"——v0.2.4 多数测试不需要。
func stateEventsTimeout(subCh <-chan Event, timeout time.Duration) []State {
	var out []State
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-subCh:
			// 关闭 channel 返回零值事件，Type 为 ""——视作结束信号。
			if ev.Type == "" {
				return out
			}
			if ev.Type == string(EventTypeState) {
				out = append(out, ev.State)
			}
		case <-deadline:
			return out
		}
	}
}

// TestSetStateAndPublish_BasicTransition 验证 setStateAndPublish 基本行为：
// state 从 A 转到 B 时，发布一个 type=state 且 State=B 的事件。
func TestSetStateAndPublish_BasicTransition(t *testing.T) {
	s := newTestSession(16)
	s.state.Store(int32(StateConnecting))
	stop := startFanoutLoop(s)
	defer stop()

	subCh, cancel := s.Subscribe()
	defer cancel()

	s.setStateAndPublish(StateAuthenticating)

	// state 已更新
	if got := s.State(); got != StateAuthenticating {
		t.Errorf("State() = %s, want Authenticating", got)
	}

	// 收到 1 个 state 事件（fanoutLoop 转发有时间窗口，用 timeout 等）
	events := stateEventsTimeout(subCh, 100*time.Millisecond)
	if len(events) != 1 || events[0] != StateAuthenticating {
		t.Errorf("state events = %v, want [Authenticating]", events)
	}
}

// TestSetStateAndPublish_SkipOnSame 验证设置成相同 state 时不发事件。
// （保留 v0.2.1 旧 setState 的"skip on same"语义，避免 transition 事件噪声。）
func TestSetStateAndPublish_SkipOnSame(t *testing.T) {
	s := newTestSession(16)
	s.state.Store(int32(StateAuthenticating))
	stop := startFanoutLoop(s)
	defer stop()

	subCh, cancel := s.Subscribe()
	defer cancel()

	s.setStateAndPublish(StateAuthenticating) // 相同 state

	if got := s.State(); got != StateAuthenticating {
		t.Errorf("State() = %s, want Authenticating (unchanged)", got)
	}

	// 等待一段时间确认没有任何 state 事件被发出
	events := stateEventsTimeout(subCh, 50*time.Millisecond)
	if len(events) != 0 {
		t.Errorf("state events = %v, want [] (skip on same)", events)
	}
}

// TestSetStateAndPublishIf_CASSuccessPublishes 验证 CAS 成功时发布事件。
func TestSetStateAndPublishIf_CASSuccessPublishes(t *testing.T) {
	s := newTestSession(16)
	s.state.Store(int32(StateConnecting))
	stop := startFanoutLoop(s)
	defer stop()

	subCh, cancel := s.Subscribe()
	defer cancel()

	if ok := s.setStateAndPublishIf(StateConnecting, StateAuthenticating); !ok {
		t.Fatal("setStateAndPublishIf should succeed when state == expect")
	}

	if got := s.State(); got != StateAuthenticating {
		t.Errorf("State() = %s, want Authenticating", got)
	}

	events := stateEventsTimeout(subCh, 100*time.Millisecond)
	if len(events) != 1 || events[0] != StateAuthenticating {
		t.Errorf("state events = %v, want [Authenticating]", events)
	}
}

// TestSetStateAndPublishIf_CASFailureNoPublish 验证 CAS 失败时：
//   - state 不变
//   - 不发布 state 事件
//
// 模拟场景：Close 已抢先（state=Closed），dialInBackground 试图
// CAS(Connecting, Authenticating) 失败。
func TestSetStateAndPublishIf_CASFailureNoPublish(t *testing.T) {
	s := newTestSession(16)
	s.state.Store(int32(StateClosed)) // Close 已抢先
	stop := startFanoutLoop(s)
	defer stop()

	subCh, cancel := s.Subscribe()
	defer cancel()

	if ok := s.setStateAndPublishIf(StateConnecting, StateAuthenticating); ok {
		t.Fatal("setStateAndPublishIf should fail when state != expect")
	}

	if got := s.State(); got != StateClosed {
		t.Errorf("State() = %s, want Closed (unchanged after failed CAS)", got)
	}

	// 等待一段时间确认没有任何 state 事件被发出
	events := stateEventsTimeout(subCh, 50*time.Millisecond)
	if len(events) != 0 {
		t.Errorf("state events = %v, want [] (no publish on CAS failure)", events)
	}
}

// TestStatePublishOrdering_NoStaleEvent 是 v0.2.4 修复 stale event race 的核心测试。
//
// v0.2.1 行为（应被本测试检测出来）：
//   - goroutine A: setStateIf(Connecting, Authenticating) CAS 成功
//   - goroutine B: Close 抢先（setState(Closing) + setState(Closed)）
//   - goroutine A: tryPublish(Authenticating) 发出过期事件
//   - subscriber 顺序: [Authenticating(过期), Closing, Closed]
//
// v0.2.4 行为（本测试必须通过）：
//   - A 和 B 都持 publishMu 做 state.Store + tryPublish
//   - subscriber 顺序: [Authenticating, Closing, Closed] 或 [Closing, Closed]
//     （取决于 A 的 CAS 是否成功——若 state 已被 B 改成 Closing/Closed，A CAS 失败不发事件）
//   - 关键不变量：**不**会出现 "Authenticating 在 Closing/Closed 之后"
//
// 测试方法：起 N 个 goroutine 并发跑 A 的逻辑，1 个 goroutine 模拟 Close 路径
// 的 state 转换（setStateAndPublish(Closing) + setStateAndPublish(Closed)）。
// 不调真实的 s.Close(true)（避免 IO + 关 sub channel 让事件收集变复杂），
// 直接验证 state-publish 序列化逻辑。
func TestStatePublishOrdering_NoStaleEvent(t *testing.T) {
	s := newTestSession(1024) // 够大避免 overflow
	s.state.Store(int32(StateConnecting))
	stop := startFanoutLoop(s)
	defer stop()

	subCh, cancel := s.Subscribe()
	defer cancel()

	// 100 个 A 试图 CAS(Connecting, Authenticating) + publish
	// 1 个 B 模拟 Close 路径的 state 转换
	// 多个 A 必然有 CAS 失败（state 不是 Connecting 时），不发事件
	const aGoroutines = 100
	var wg sync.WaitGroup
	wg.Add(aGoroutines + 1)

	barrier := make(chan struct{})

	for i := 0; i < aGoroutines; i++ {
		go func() {
			defer wg.Done()
			<-barrier
			s.setStateAndPublishIf(StateConnecting, StateAuthenticating)
		}()
	}

	go func() {
		defer wg.Done()
		<-barrier
		// 模拟 Close 路径的状态转换：Closing → Closed
		// （不调真实 s.Close 避免 IO + 关 sub channel 的复杂性）
		s.setStateAndPublish(StateClosing)
		s.setStateAndPublish(StateClosed)
	}()

	close(barrier)
	wg.Wait()

	// 等 fanoutLoop 转发完所有事件
	events := stateEventsTimeout(subCh, 200*time.Millisecond)

	// 关键不变量 1：一旦出现 Closing/Closed，**之后不能**再出现 Connecting/Authenticating/Established
	var seenClosingOrClosed bool
	for i, st := range events {
		if st == StateClosing || st == StateClosed {
			seenClosingOrClosed = true
			continue
		}
		if seenClosingOrClosed {
			t.Errorf("stale event at index %d: %s appeared after Closing/Closed in %v",
				i, st, events)
		}
	}

	// 关键不变量 2：最终 state 必须是 Closed（B 已转换）
	if got := s.State(); got != StateClosed {
		t.Errorf("final State() = %s, want Closed", got)
	}

	// 关键不变量 3：必须见过 Closing 和 Closed（按顺序）
	var sawClosing, sawClosed bool
	for _, st := range events {
		if st == StateClosing {
			sawClosing = true
		}
		if sawClosing && st == StateClosed {
			sawClosed = true
		}
	}
	if !sawClosing {
		t.Errorf("missing Closing event in %v", events)
	}
	if !sawClosed {
		t.Errorf("missing Closed event after Closing in %v", events)
	}

	// 关键不变量 4：所有 Authenticating 出现**之前**不能有 Closing/Closed
	// （即不存在 Connecting→Closed→Authenticating 荒谬时序）
	for i, st := range events {
		if st == StateAuthenticating {
			for j := 0; j < i; j++ {
				if events[j] == StateClosing || events[j] == StateClosed {
					t.Errorf("Authenticating at index %d preceded by %s at index %d in %v",
						i, events[j], j, events)
				}
			}
		}
	}
}

// TestStatePublishOrdering_MultipleCloses 验证多次 Close 调用安全：
// closeOnce 保证 Close 流程只跑一次，第二次 Close 是 no-op。
func TestStatePublishOrdering_MultipleCloses(t *testing.T) {
	s := newTestSession(16)
	s.state.Store(int32(StateAuthenticating))
	stop := startFanoutLoop(s)
	defer stop()

	subCh, cancel := s.Subscribe()
	defer cancel()

	// 并发 Close 5 次
	const closes = 5
	var wg sync.WaitGroup
	wg.Add(closes)
	for i := 0; i < closes; i++ {
		go func() {
			defer wg.Done()
			_ = s.Close(true)
		}()
	}
	wg.Wait()

	// state 必须是 Closed
	if got := s.State(); got != StateClosed {
		t.Errorf("final State() = %s, want Closed", got)
	}

	// 等 fanoutLoop drain 完所有事件
	events := stateEventsTimeout(subCh, 200*time.Millisecond)
	// Closing 事件**最多**出现一次（closeOnce 保证）
	closingCount := 0
	for _, st := range events {
		if st == StateClosing {
			closingCount++
		}
	}
	if closingCount > 1 {
		t.Errorf("Closing event appeared %d times, want <= 1: %v", closingCount, events)
	}
}

// TestSetStateAndPublish_EventInChannelSynchronously 验证 setStateAndPublish
// 把事件**同步**放入 s.events 通道（函数返回时事件已在通道里）。
//
// 这是 Close 路径时序正确性的基础：setStateAndPublish(Closing) 必须在
// signalDone 之前把 Closing 事件放入 events 通道——否则 tryPublish 在
// s.done 关闭后会被 <-s.done 分支抢先 select，导致 Closing 事件丢失。
//
// 不启 fanoutLoop、不订阅 subscriber——直接检查 s.events 通道，避免
// 引入"sub channel 在事件被 broadcast 之前就被 Close 关掉"的预存 bug
//（v0.2.x 已有，非 v0.2.4 修复范围）。
func TestSetStateAndPublish_EventInChannelSynchronously(t *testing.T) {
	s := newTestSession(16)
	s.state.Store(int32(StateAuthenticating))

	s.setStateAndPublish(StateClosing)

	// 事件已同步放入 events 通道（不会因为 s.done 未关闭而丢）
	select {
	case ev := <-s.events:
		if ev.Type != string(EventTypeState) {
			t.Errorf("event type = %q, want %q", ev.Type, EventTypeState)
		}
		if ev.State != StateClosing {
			t.Errorf("event State = %s, want %s", ev.State, StateClosing)
		}
	default:
		t.Error("Closing event not in events channel after setStateAndPublish returned")
	}
}
