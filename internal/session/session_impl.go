// session_impl.go 实现 session.Session 接口。
//
// 生命周期：
//
//	Manager.Open(ctx, req)
//	  ├─ 构造 sessionImpl（id / info / connector / dialParams / sessionOpts）
//	  ├─ connector.Dial(ctx, dialParams)        ─┐
//	  ├─ connector.OpenSession(ctx, conn, opts)  │ 这两步骤由 Open 同步执行
//	  ├─ state 推到 Established                  ─┘
//	  └─ s.Start(ctx)  启动 readLoop / writeLoop / fanoutLoop
//	                                                    ↓
//	                                          全部 session 通过 events 通道汇聚
//	                                          → fanout → 广播给各 subscriber
//
// 并发模型：
//   - readLoop：把 sess.Read 的输出塞进 s.events（events 是 cap-64 的 channel）
//   - writeLoop：从 s.inputCh 取数据写到 sess
//   - fanoutLoop：从 s.events 取出事件，扇出到所有 sub
//   - Close 一次性的关停：closeOnce + done channel
//
// 同步原语：
//   - id 是不可变字符串，启动后无锁
//   - state / info 用 atomic（atomic.Int32 / atomic.Pointer[Info]）
//   - sub map 用 sync.Mutex 保护
//   - sess / dialed 用 sync.RWMutex 保护（readLoop/writeLoop 持读引用，
//     Close 持写锁置 nil）
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mossterm/mossterm/internal/connect"
)

const (
	// inputBufferSize 是 inputCh 的容量；满时 Session.Input 返回 ErrInputFull。
	inputBufferSize = 64

	// eventsBufferSize 是中央 events channel 的容量。
	//
	// 该值要 ≥ 单次状态转换的事件数 × 最大并发数；
	// v0.1 中 64 已远超实际需求。
	eventsBufferSize = 64

	// subBufferSize 是单个 subscriber channel 的容量。
	//
	// 满时 fanoutLoop 会丢掉"该 sub 的最新事件"（非阻塞发送）。
	subBufferSize = 64

	// sessReadyPollInterval 是 readLoop/writeLoop 等待 sess 就绪的轮询间隔。
	sessReadyPollInterval = 20 * time.Millisecond
)

// sessionImpl 是 Session 接口的进程内实现。
type sessionImpl struct {
	id    ID
	info  atomic.Pointer[Info]
	state atomic.Int32

	// 协议层对象。
	//
	// conn 持 connector 引用（用于 v0.2+ 重新拨号 / SFTP subsystem 复用）；
	// dialed 是 connector.Dial 的返回值，Close 时必须释放；
	// sess 是 connector.OpenSession 的返回值，Read/Write/Resize 都走它。
	conn   connect.Connector
	dialed net.Conn
	sess   connect.Session

	// 拨号参数（Open 时定，Start 时使用）。
	dialParams  connect.DialParams
	sessionOpts connect.SessionOpts

	// connMu 保护 dialed / sess 的并发读写。
	//
	// 读侧：readLoop / writeLoop 持读锁拿到本地引用，然后无锁使用。
	// 写侧：Close 持写锁置 nil 后再 close 引用。
	connMu sync.RWMutex

	// I/O 与事件 plumbing。
	inputCh chan []byte
	events  chan Event
	done    chan struct{}

	// 一次性触发。
	closeOnce sync.Once
	started   atomic.Bool

	// Fan-out。
	subMu     sync.Mutex
	subs      map[int]chan Event
	nextSubID int

	// 可选 logger；nil 时回退到 slog.Default()。
	log *slog.Logger
}

// -----------------------------------------------------------------------------
// 公开方法（实现 Session 接口）
// -----------------------------------------------------------------------------

// Start 启动 readLoop / writeLoop / fanoutLoop 三个 goroutine。
//
// 设计说明：Manager.Open 已经同步完成 Dial + OpenSession 并把 state
// 推到 Established；Start 不再处理握手，只负责循环 IO 与事件分发。
// 这样保持 3 个 goroutine 的"最小集合"，与架构文档一致。
//
// 多次调用 Start 只会启动一次（started 保护），第二次返回 error。
func (s *sessionImpl) Start(_ context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("session: already started")
	}
	go s.readLoop()
	go s.writeLoop()
	go s.fanoutLoop()
	return nil
}

// Input 把用户按键写入 sess。非阻塞。
//
// inputCh 已满时立即返回 ErrInputFull，调用方应稍后重发。
// state 不在 Established 时也返回 error（拒绝写入）。
func (s *sessionImpl) Input(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if State(s.state.Load()) != StateEstablished {
		return fmt.Errorf("session.Input: not established (state=%s)", State(s.state.Load()))
	}
	select {
	case s.inputCh <- data:
		return nil
	default:
		return ErrInputFull
	}
}

// Resize 调整远端 PTY 窗口大小并同步更新 info。
//
// 失败不影响本地 info 更新（让 UI 至少能反映用户期望）；
// sess 为 nil 时只更新 info（用于"打开前预先设定"场景）。
func (s *sessionImpl) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("session.Resize: invalid size %dx%d", cols, rows)
	}
	if p := s.info.Load(); p != nil {
		updated := *p
		updated.Cols = cols
		updated.Rows = rows
		s.info.Store(&updated)
	}
	s.connMu.RLock()
	sess := s.sess
	s.connMu.RUnlock()
	if sess == nil {
		// 协议层尚未就绪 —— 仅更新本地 info，等 OpenSession 完成后
		// 由调用方在合适时机再次 Resize。
		return nil
	}
	if err := sess.Resize(cols, rows); err != nil {
		return fmt.Errorf("session.Resize: %w", err)
	}
	return nil
}

// Subscribe 注册一个事件订阅者，返回 (channel, cancel)。
//
// 取消订阅时：先从 sub map 中移除，再关闭 channel。
// channel 关闭后，range 循环会自然退出，避免泄漏。
func (s *sessionImpl) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subBufferSize)
	s.subMu.Lock()
	id := s.nextSubID
	s.nextSubID++
	s.subs[id] = ch
	s.subMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.subMu.Lock()
			if existing, ok := s.subs[id]; ok {
				delete(s.subs, id)
				close(existing)
			}
			s.subMu.Unlock()
		})
	}
	return ch, cancel
}

// Close 关闭 session。force 参数 v0.1 暂未使用（SSH 协议只能"立即关"
// 没有"优雅退出"路径，调用方应自行等待 EOF）。
//
// 关闭流程：
//  1. closeOnce 保护，确保只执行一次
//  2. state → Closing（发布事件）
//  3. sess.Close + dialed.Close（释放协议层）
//  4. close(done) 唤醒所有阻塞中的 goroutine
//  5. state → Closed + 关闭 sub channels（让订阅者 range 退出）
func (s *sessionImpl) Close(_ bool) error {
	s.closeOnce.Do(func() {
		current := State(s.state.Load())
		if current == StateClosed {
			return
		}
		s.setState(StateClosing)
		s.tryPublish(newStateEvent(StateClosing))

		// 在持写锁的情况下取出引用、置 nil、释放锁
		s.connMu.Lock()
		sess := s.sess
		s.sess = nil
		dialed := s.dialed
		s.dialed = nil
		s.connMu.Unlock()

		if sess != nil {
			_ = sess.Close()
		}
		if dialed != nil {
			_ = dialed.Close()
		}

		// 唤醒 fanoutLoop / writeLoop / readLoop
		close(s.done)

		// 最后状态
		s.setState(StateClosed)
		s.tryPublish(newStateEvent(StateClosed))

		// 关闭所有订阅 channel：让订阅者 range 退出
		s.subMu.Lock()
		for id, ch := range s.subs {
			close(ch)
			delete(s.subs, id)
		}
		s.subMu.Unlock()
	})
	return nil
}

// Info 返回当前 session 元数据快照（原子读）。
func (s *sessionImpl) Info() Info {
	if p := s.info.Load(); p != nil {
		return *p
	}
	return Info{}
}

// State 返回当前 session 状态（原子读）。
func (s *sessionImpl) State() State {
	return State(s.state.Load())
}

// -----------------------------------------------------------------------------
// 内部：状态 / 发布 / 广播
// -----------------------------------------------------------------------------

// setState 原子地更新状态。
func (s *sessionImpl) setState(st State) {
	s.state.Store(int32(st))
}

// tryPublish 非阻塞地往 events 通道发送一个事件。
//
// channel 满或 session 已关闭时丢弃；不阻塞调用方。
// readLoop / writeLoop / Close 等都可能调用。
func (s *sessionImpl) tryPublish(ev Event) {
	if ev.At == 0 {
		ev.At = time.Now().UnixMilli()
	}
	select {
	case s.events <- ev:
	case <-s.done:
	default:
		// events 满 + 未关闭 → 丢弃。生产环境下可考虑发 session:overflow。
	}
}

// broadcast 把事件扇出到所有 subscriber，consumer 慢则丢弃。
func (s *sessionImpl) broadcast(ev Event) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- ev:
		default:
			// 该 sub 处理太慢，丢弃。生产环境可在这里计数 / log。
		}
	}
}

// -----------------------------------------------------------------------------
// 内部：三个 goroutine
// -----------------------------------------------------------------------------

// readLoop 持续从 sess 读取数据，封装成 Event 写入 events 通道。
//
// 远端断开时（io.EOF 或其它错误）发布 exit 事件并触发 Close。
func (s *sessionImpl) readLoop() {
	// 等到 sess 就绪
	sess := s.waitForSess()
	if sess == nil {
		return
	}

	buf := make([]byte, 4096)
	for {
		n, err := sess.Read(buf)
		if n > 0 {
			// 复制一份再 publish（buf 会在下次循环被覆写）
			data := make([]byte, n)
			copy(data, buf[:n])
			ev := newDataEvent(data)
			select {
			case s.events <- ev:
			case <-s.done:
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				s.tryPublish(newExitEvent(err.Error()))
			} else {
				s.tryPublish(newExitEvent("EOF"))
			}
			_ = s.Close(true)
			return
		}
	}
}

// writeLoop 从 inputCh 取数据并写入 sess。
//
// 输入批处理：v0.1 简单实现 —— 每条消息独立写入，不做 4KB/16ms 合并。
// 未来可在 writeLoop 里加 flush ticker 与累积 buffer。
func (s *sessionImpl) writeLoop() {
	sess := s.waitForSess()
	if sess == nil {
		return
	}

	for {
		select {
		case data, ok := <-s.inputCh:
			if !ok {
				return
			}
			if _, err := sess.Write(data); err != nil {
				s.tryPublish(newErrorEvent(err))
				_ = s.Close(true)
				return
			}
		case <-s.done:
			return
		}
	}
}

// fanoutLoop 从 events 通道读事件并广播到所有 subscriber。
//
// 该 goroutine 是 v0.1 的实现选择：readLoop / Close 不直接调 broadcast，
// 以避免 readLoop 因为某个慢 sub 而被阻塞。events 通道天然提供缓冲 + 解耦。
func (s *sessionImpl) fanoutLoop() {
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return
			}
			s.broadcast(ev)
		case <-s.done:
			// 关停前 drain 一遍 events 里的剩余事件，让 sub 看到完整时序
			for {
				select {
				case ev, ok := <-s.events:
					if !ok {
						return
					}
					s.broadcast(ev)
				default:
					return
				}
			}
		}
	}
}

// waitForSess 轮询等待 s.sess 就绪，返回 nil 表示 session 已关闭。
func (s *sessionImpl) waitForSess() connect.Session {
	t := time.NewTicker(sessReadyPollInterval)
	defer t.Stop()
	for {
		if State(s.state.Load()) == StateClosed {
			return nil
		}
		s.connMu.RLock()
		sess := s.sess
		s.connMu.RUnlock()
		if sess != nil {
			return sess
		}
		select {
		case <-s.done:
			return nil
		case <-t.C:
		}
	}
}

// -----------------------------------------------------------------------------
// 工厂
// -----------------------------------------------------------------------------

// NewSessionImpl 构造一个 sessionImpl。
//
// 必填：id、connector、dialParams、sessionOpts。
// dialed / sess 由调用方在 Dial + OpenSession 之后赋值。
//
// 该工厂是 internal 的：外部代码必须通过 session.MemoryManager.Open 进入。
func NewSessionImpl(
	id ID,
	connector connect.Connector,
	dialParams connect.DialParams,
	sessionOpts connect.SessionOpts,
	info Info,
) *sessionImpl {
	s := &sessionImpl{
		id:          id,
		conn:        connector,
		dialParams:  dialParams,
		sessionOpts: sessionOpts,
		inputCh:     make(chan []byte, inputBufferSize),
		events:      make(chan Event, eventsBufferSize),
		done:        make(chan struct{}),
		subs:        make(map[int]chan Event),
		log:         slog.Default(),
	}
	// info 是 Manager.Open 构造的初始快照（State 一定是 Connecting 或 Established）
	s.info.Store(&info)
	// 初始 state 用 info.State —— Open 完成后会覆盖为 Established
	s.state.Store(int32(info.State))
	return s
}

// SetDialedSess 由 Manager.Open 在拿到 Dial + OpenSession 结果后调用。
//
// 把 conn / sess 写入 sessionImpl；如果 conn / sess 为 nil，会清空之前
// 的引用（v0.1 不使用，仅作安全兜底）。
func (s *sessionImpl) SetDialedSess(dialed net.Conn, sess connect.Session) {
	s.connMu.Lock()
	s.dialed = dialed
	s.sess = sess
	s.connMu.Unlock()
}

// 编译期断言：*sessionImpl 实现 Session 接口。
var _ Session = (*sessionImpl)(nil)
