// session_impl_test.go 覆盖 v0.2 引入的两个新行为：
//   1. tryPublish 在 events 通道满时累加 overflowBytes + 置位 overflowPending
//   2. fanoutLoop 在 broadcast 后通过 maybeEmitOverflow 发出 overflow 事件
//
// 不覆盖：
//   - readLoop 批处理行为：要起真实 SSH server + 喂可控速率的字节流，
//     留给 v0.2.1 integration test harness
//   - 端到端 "cat large.log → 触发 overflow 事件" 路径：同上
//
// 测试构造 sessionImpl 的方式：直接零值 + 手动填必要字段。
// 同包测试能访问 unexported 字段（overflowBytes / overflowPending），
// 正是我们要验证的对象。
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
