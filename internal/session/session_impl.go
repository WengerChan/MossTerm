// session_impl.go 实现 session.Session 接口。
//
// 生命周期（v0.2.0a 重构）：
//
//	Manager.Open(ctx, req)
//	  ├─ 同步：validate / uuid / 转换 / factory / NewSessionImpl / 注册
//	  ├─ 同步：s.Start(ctx) 启动 readLoop / writeLoop / fanoutLoop
//	  │        （fanoutLoop 立即可用；readLoop/writeLoop 在 waitForSess 阻塞）
//	  └─ 异步（dialInBackground goroutine）：
//	       ├─ connector.Dial → state=Authenticating → connector.OpenSession
//	       ├─ SetDialedSess → state=Established   （成功路径）
//	       └─ state=Failed + signalDone            （任意一步失败）
//	                                                    ↓
//	                                          全部 session 通过 events 通道汇聚
//	                                          → fanout → 广播给各 subscriber
//
// 并发模型（v0.2）：
//   - readLoop：内部再分两个 goroutine —— reader 持续 sess.Read(buf) 推到
//     dataCh；main loop 用 16ms ticker + dataCh + done 协调，把数据累积到
//     64 KiB accumulator 再一次性 publish 一个 data event。
//   - writeLoop：从 s.inputCh 取数据写到 sess
//   - fanoutLoop：从 s.events 取出事件，扇出到所有 sub；每次 broadcast 后
//     检查 overflowPending，若置位则 emit 一个 overflow 事件（不经过 events
//     通道，直接 broadcast 到 subs）。
//   - Close 一次性的关停：closeOnce + done channel
//
// 同步原语：
//   - id 是不可变字符串，启动后无锁
//   - state / info / overflowBytes / overflowPending 用 atomic
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
	// v0.2 配合 16 ms 批处理后的吞吐预算：
	//   - 稳态（每 tick 刚好 flush 一次 maxAccum）：64 KiB / 16 ms ≈ 4 MB/s
	//   - 理论上限（events 永远不空，每 tick flush N 个 maxAccum）：
	//     eventsBufferSize × readMaxAccum / readFlushInterval ≈ 256 MB/s
	//   - 典型 SSH 终端交互：< 100 KB/s
	// 单 session 超出稳态预算会触发 overflow 事件 —— 是预期降级路径，
	// 不应视为 bug（参见 readMaxAccum 注释）。
	eventsBufferSize = 64

	// subBufferSize 是单个 subscriber channel 的容量。
	//
	// 满时 fanoutLoop 会丢掉"该 sub 的最新事件"（非阻塞发送）。
	subBufferSize = 64

	// sessReadyPollInterval 是 readLoop/writeLoop 等待 sess 就绪的轮询间隔。
	sessReadyPollInterval = 20 * time.Millisecond

	// readFlushInterval 是 readLoop 把 accumulator 强制 flush 到 events 通道的周期。
	//
	// 16 ms ≈ 1 帧 (60 fps)，与 xterm.js 渲染节奏对齐；
	// 既保证低延迟，又能把每秒几千次的小包聚合成 ~60 个大包。
	readFlushInterval = 16 * time.Millisecond

	// readBufSize 是单次 sess.Read 的缓冲区大小。
	//
	// v0.1 用 4 KiB 在 cat 大文件时系统调用太频繁；
	// 32 KiB 是 Linux pipe/Socket 默认 SO_RCVBUF 的下沿，单次读能吃满
	// 一个 MTU 帧到 TCP 段。
	readBufSize = 32 * 1024

	// readMaxAccum 是 readLoop accumulator 的最大字节数。
	//
	// 超过则强制 flush（不等 ticker），避免一个超长 cat 把 accumulator
	// 撑到几 MB 之后才一次性 broadcast 导致前端一帧卡死。
	// 64 KiB / 16 ms ≈ 4 MB/s 的稳态吞吐；超出此值会触发 overflow 事件
	// —— 对终端交互（< 100 KB/s）充裕，对 cat GB 日志是预期降级路径。
	readMaxAccum = 64 * 1024

	// readDataChSize 是 readLoop 内部 dataCh 的容量。
	//
	// 8 × 32 KiB = 256 KiB in-flight；足够让 reader goroutine
	// 在 main loop 阻塞时仍持续搬运（不卡系统调用）。
	readDataChSize = 8
)

// sessionImpl 是 Session 接口的进程内实现。
type sessionImpl struct {
	id    ID
	info  atomic.Pointer[Info]
	state atomic.Int32

	// publishMu 序列化"setState + tryPublish"与 Close 的竞争。
	//
	// v0.2.4 新增：消除 v0.2.1 识别的"stale event" race —— CAS 成功之后
	// Close 抢先会让 out-of-order 事件通过，subscriber 短暂看到
	// state=Closed 但 event=Authenticating（前端 UI 闪烁）。
	//
	// 设计：
	//   - setStateAndPublishIf 持 publishMu 做 CAS + tryPublish
	//   - setStateAndPublish 持 publishMu 做无条件 set + tryPublish
	//   - Close 持 publishMu 做 setState(Closing/Closed) + tryPublish
	//   - readLoop / writeLoop 调 tryPublish **不**持 publishMu（hot path，
	//     它们发的是 data 事件，没有 stale 问题）
	//   - broadcast 持 subMu 但**不**持 publishMu（fanoutLoop 调的）
	//
	// 死锁防御：publishMu 不与 subMu / connMu 嵌套
	//   - Close 在 publishMu 内 publish 后释放，再去拿 connMu / subMu
	//   - broadcast 只在 subMu 内做 fan-out
	//   - tryPublish 自身不持任何锁（仅 channel send + atomic）
	publishMu sync.Mutex

	// 协议层对象。
	//
	// conn 持 connector 引用（用于 v0.2+ 重新拨号 / SFTP subsystem 复用）；
	// dialed 是 connector.Dial 的返回值，Close 时必须释放；
	// sess 是 connector.OpenSession 的返回值，Read/Write/Resize 都走它。
	conn   connect.Connector
	dialed net.Conn
	sess   connect.Session

	// 拨号参数（Open 时定，dialInBackground 时使用）。
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
	//
	// closeOnce 保护整个 Close 流程（多次 Close 调用幂等）。
	// doneOnce 专门保护 close(s.done)：v0.2.0a 起 Close 和
	// dialInBackground 失败路径都可能触发，用 sync.Once 防止 panic。
	closeOnce sync.Once
	doneOnce  sync.Once
	started   atomic.Bool

	// Fan-out。
	subMu     sync.Mutex
	subs      map[int]chan Event
	nextSubID int

	// Overflow 计数。
	//
	// overflowBytes 累计自上次 fanoutLoop 上报以来被丢弃的 data 字节数；
	// overflowPending 标记"有未上报的溢出"。
	// tryPublish 在 events 通道满时 Add + Store(true)；
	// fanoutLoop 每轮 broadcast 后 Load → 决定是否发 overflow 事件。
	// 两者都是 lock-free；唯一约束是 tryPublish 的 Add 必须在
	// Store(true) 之前（v0.2 单调用点天然满足）。
	overflowBytes   atomic.Int64
	overflowPending atomic.Bool

	// Sub drop 计数（v0.2.3 新增）。
	//
	// 与 overflowBytes 的区别（必须**严格区分**，前端据此分别诊断）：
	//   - overflowBytes：readLoop 太快 → 中央 events 通道（cap=64）丢
	//   - subDropBytes：subscriber 处理太慢 → 该 sub 的 channel（cap=64）丢
	//
	// 设计完全镜像 v0.2.0 的 overflow 机制：
	//   - broadcast 在 sub channel 满时 Add(len(ev.Data)) + Store(true)
	//   - fanoutLoop 每轮 broadcast 后调 maybeEmitSubOverflow
	//   - 两者 lock-free；唯一约束是 broadcast 的 Add 必须在 Store(true) 之前
	//     （broadcast 内部顺序写定，天然满足）
	//
	// **不变量**（与 overflow 不同的关键点）：
	//   - 累加条件是 `len(ev.Data) > 0`；overflow / sub:overflow / state /
	//     exit / error 事件被 sub drop 时**不**会被累加（Data 字段为空）
	//   - 因此 sub:overflow 事件被 sub drop 时不会触发新的 sub:overflow，
	//     从根本上避免了"广播一个 drop 通知、又被 drop、再广播一个..."
	//     的递归（最坏情况下 sub:overflow 事件被 drop 一次就丢失，副作用为零）
	subDropBytes   atomic.Int64
	subDropPending atomic.Bool

	// 可选 logger；nil 时回退到 slog.Default()。
	log *slog.Logger
}

// -----------------------------------------------------------------------------
// 公开方法（实现 Session 接口）
// -----------------------------------------------------------------------------

// Start 启动 readLoop / writeLoop / fanoutLoop 三个 goroutine。
//
// v0.2.0a 行为变更：Start 由 MemoryManager.Open 在注册 session 后立即同步调用，
// 不再由外部 caller 调用。重复调用 / 外部调用均返回 "already started" error。
// 签名保留是为了符合 Session 接口契约。
//
// 启动时机与生命周期：
//   - Open 注册 session 到 m.sessions 之后立即调用（同步）
//   - 此时 s.sess 还是 nil；readLoop / writeLoop 在 waitForSess 阻塞
//   - dialInBackground 在 SetDialedSess(conn, sess) 后，readLoop/writeLoop 解除阻塞
//
// 为什么在 Open 同步阶段就调 Start（而不是等 dial 完成后）：
//   - fanoutLoop 在 Open 返回时已经运行；后续 state 事件能立即广播给 subscriber
//     （state 事件在 dialInBackground 里 tryPublish）
//   - 如果等 dial 完成后才调 Start，那段窗口内的 Connecting → Authenticating
//     状态转换会丢失（fanoutLoop 没起，无法 broadcast）
//   - 失败路径（dial 失败 / OpenSession 失败）由 dialInBackground 调
//     signalDone 关闭 s.done；readLoop / writeLoop / fanoutLoop 自然退出
//
// 多次调用 Start 只会启动一次（started.CompareAndSwap 保护），第二次返回 error。
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
//  2. state → Closing（在 publishMu 内发布事件，dialInBackground 不会抢先）
//  3. 释放 publishMu，做 IO：sess.Close + dialed.Close（**不**持 publishMu）
//  4. close(done) 唤醒所有阻塞中的 goroutine
//  5. state → Closed（再次拿 publishMu 发布事件）
//  6. 关闭 sub channels（让订阅者 range 退出）
//
// v0.2.4 行为变更：状态转换与事件发布序列化在 publishMu 内，彻底消除
// v0.2.1 识别的 stale event race（详见 publishMu 字段注释）。
// IO 步骤（sess.Close / dialed.Close）放在 publishMu 外，**不**持锁
// 阻塞等外部协议层响应——避免 Close 在慢 IO 上挂住 publishMu，
// 间接卡住 dialInBackground 的状态转换。
func (s *sessionImpl) Close(_ bool) error {
	s.closeOnce.Do(func() {
		current := State(s.state.Load())
		if current == StateClosed {
			return
		}
		// 1. 推 Closing 事件（publishMu 内，Close 路径串行化）
		s.setStateAndPublish(StateClosing)

		// 2. 释放 publishMu，做 IO（conn/sess Close 可能阻塞）
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

		// 3. 唤醒 fanoutLoop / writeLoop / readLoop
		s.signalDone()

		// 4. 推 Closed 事件（再次拿 publishMu）
		s.setStateAndPublish(StateClosed)

		// 5. 关闭所有订阅 channel：让订阅者 range 退出
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
//
// v0.2.0a 行为变更：info.State 字段与 s.state 保持同步（每次读都从
// atomic.Int32 拉），caller 通过 Info().State 看到的总是最新状态。
// v0.1.x 的实现把 State 缓存在 Info 里，状态转换后 Info().State 仍是
// 初始值（Connecting），导致前端看不到 Authenticating / Established 转换。
// 本次重构顺手修复。
func (s *sessionImpl) Info() Info {
	if p := s.info.Load(); p != nil {
		info := *p
		info.State = State(s.state.Load()) // v0.2.0a: 动态 state（不再缓存）
		return info
	}
	return Info{}
}

// State 返回当前 session 状态（原子读）。
func (s *sessionImpl) State() State {
	return State(s.state.Load())
}

// Connector 返回构造本 Session 时使用的 connect.Connector。
//
// v0.5.1 新增（v0.5.1 之前是 unexported 字段 conn）：同模块其他包（典型：
// internal/app 的 sftpFor 路径）需要拿到 connector 来开 SFTP subsystem
// 等共享 SSH 连接的子系统。
//
// 返回值与 Open 时的 connector 完全等价，调用方可安全 type assert 回
// 具体类型（*sshclient.Connector 等）。
//
// 生命周期：Connector 引用与 Session 生命周期独立 —— Session Close 不会
// 调 Connector.Close（sshclient 文档明确：Connector 是 long-lived singleton，
// Close 只关 keepalive，不应随 Session 关闭）。具体协议层资源（*ssh.Client /
// *sshConn）由 Session 内部 connMu 保护，Close 路径会释放。
func (s *sessionImpl) Connector() connect.Connector {
	return s.conn
}

// -----------------------------------------------------------------------------
// 内部：状态 / 发布 / 广播
// -----------------------------------------------------------------------------

// setStateAndPublish 原子地更新 state 并发布对应的 state 事件。
//
// v0.2.4 新增：消除 v0.2.1 识别的 stale event race。持 publishMu 期间
// state.Store + tryPublish 是原子的，Close 不会在中间抢先发布 Closing/Closed。
//
// 与 setStateAndPublishIf 的区别：本方法**无 CAS**，无条件设置；适用
// 于 Close 路径（已经被 closeOnce 序列化，不会被另一个 Close 并发）。
//
// 跳过逻辑：若当前 state 已等于 st，则不发布事件（避免重复 transition）。
// v0.2.1 旧 setState 同样跳过——保留语义兼容。
func (s *sessionImpl) setStateAndPublish(st State) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	s.setStateAndPublishLocked(st)
}

// setStateAndPublishLocked 是 setStateAndPublish 的"持锁版"内部方法。
//
// 调用方必须已持有 publishMu；本方法只做 state 比较/写入 + tryPublish，
// **不**再获取 publishMu（否则会自死锁）。
//
// 锁顺序约束：调用本方法时 publishMu 必须被持有，且**不**持 subMu / connMu。
// 详见 publishMu 字段注释。
func (s *sessionImpl) setStateAndPublishLocked(st State) {
	if State(s.state.Load()) == st {
		return
	}
	s.state.Store(int32(st))
	s.tryPublish(newStateEvent(st))
}

// setStateAndPublishIf 是 setStateIf + tryPublish 的合并版本。
//
// v0.2.4 替换 v0.2.1 的"setStateIf + 单独 tryPublish"模式：持 publishMu
// 期间 CAS + publish 是原子的，Close 不会在中间抢先——彻底消除 v0.2.1
// 识别的 stale event race（subscriber 短暂看到 state=Closed 但
// event=Authenticating 的闪烁）。
//
// 返回 true 表示 CAS 成功。
//
// race 场景（v0.2.1 未修，v0.2.4 修复）：
//
//	goroutine A (dialInBackground): setStateIf(Connecting, Authenticating) ✅
//	goroutine B (Close):            setState(Closing) → setState(Closed) ✅
//	goroutine A:                    tryPublish(Authenticating) ❌ 发出过期事件
//
// 修复后：setStateAndPublishIf 在 publishMu 内做 CAS + publish，B 想
// 推 Closing 必须等 A 释放 publishMu；之后 A 的 publish 一定先于
// B 的 Closing/Closed publish，subscriber 看到的时序单调。
//
// 调用方：dialInBackground 在 Connecting → Authenticating / Failed /
// Authenticating → Established / Failed 等转换点。
func (s *sessionImpl) setStateAndPublishIf(expect, to State) bool {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if s.state.CompareAndSwap(int32(expect), int32(to)) {
		s.tryPublish(newStateEvent(to))
		return true
	}
	return false
}

// signalDone 一次性关闭 s.done channel。
//
// v0.2.0a 新增：Close 和 dialInBackground 失败路径都会调用；用 doneOnce
// 保护避免重复 close 引发 panic。
//
// 调用场景：
//   - Close 路径：state → Closing → 释放 conn/sess → signalDone → state → Closed
//   - dialInBackground 失败路径：state → Failed → signalDone（让 readLoop /
//     writeLoop / fanoutLoop 退出；session 保留在 m.sessions 供 caller 查询）
//
// 调用此方法前应当确认已设置好最终 state（Closed 或 Failed），方便
// 还在轮询 waitForSess 的协程看到正确的退出原因。
func (s *sessionImpl) signalDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

// tryPublish 非阻塞地往 events 通道发送一个事件。
//
// channel 满或 session 已关闭时丢弃；不阻塞调用方。
// readLoop / writeLoop / Close 等都可能调用。
//
// v0.2 行为变更：events 满时不再静默丢弃 ——
//   - 若 ev 是 data 事件（len(Data) > 0），累加 len(Data) 到 overflowBytes
//     并置位 overflowPending，由 fanoutLoop 在下一轮 broadcast 后 emit
//     一个 overflow 事件给所有 subscriber
//   - 若 ev 是 state / exit / error 等元事件，无 Data 字段，不计入溢出
//     （这些事件丢失对终端用户无可见影响；前端从 state 变化已能感知）
//
// v0.1.x 残留 bug 顺手修：原版 select 在 done 已关闭 + events 满时，
// Go runtime 会从 {<-done, default} 里随机选，1/3 概率错把正常关停
// 记成 overflow。修法是 default 分支里再 select 一次确认 done 未关闭。
// 性能代价：一次额外 atomic 读；语义上完全可接受。
//
// 该函数被 readLoop（hot path）频繁调用，无锁、无分配（除时间戳的 8 字节）；
// 性能与 v0.1 几乎相同。
func (s *sessionImpl) tryPublish(ev Event) {
	if ev.At == 0 {
		ev.At = time.Now().UnixMilli()
	}
	select {
	case s.events <- ev:
	case <-s.done:
	default:
		// events 满 + 未关闭 → 丢弃 data 字节数累加。
		if n := len(ev.Data); n > 0 {
			// 二次确认未关闭：避免关停瞬间被错记为 overflow。
			// 这次 select 与外层 select 之间的窗口里 done 可能新关闭，
			// 但那属于"已关停"语义（fanoutLoop 已 close 或即将 close），
			// 不会 broadcast 到任何 sub，副作用为零。
			select {
			case <-s.done:
				// 正常关停路径，不计入 overflow。
			default:
				s.overflowBytes.Add(int64(n))
				s.overflowPending.Store(true)
			}
		}
	}
}

// broadcast 把事件扇出到所有 subscriber，consumer 慢则丢弃。
//
// v0.2.3 新增 sub drop 累加：sub channel 满时把 ev.Data 字节数累加到
// subDropBytes + 置位 subDropPending，由 fanoutLoop 在下一轮 broadcast
// 后通过 maybeEmitSubOverflow emit 一个 sub:overflow 事件给所有 subscriber。
//
// 关键不变量（防递归的根因）：累加条件是 `len(ev.Data) > 0`，因此
//   - sub:overflow / overflow / state / exit / error 事件被 sub drop 时
//     **不**会被累加（它们的 Data 字段为空）
//   - 即使 sub:overflow 事件被某 sub 再次 drop，也只丢这一次，不会触发
//     "广播 drop 通知 → 又被 drop → 再广播 drop 通知..."的递归
//   - 最坏情况：sub:overflow 事件被 drop 一次就丢失，**副作用为零**
//
// 与 tryPublish 的 overflow 累加是**两套独立机制**，因为它们衡量的是
// 两个不同的瓶颈：
//   - tryPublish 累加 = "events 通道满"，后端 IO 太快（readLoop 速率 > fanout）
//   - broadcast 累加  = "sub channel 满"，前端处理太慢（subscriber < fanout）
func (s *sessionImpl) broadcast(ev Event) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- ev:
		default:
			// 该 sub 处理太慢，丢弃。v0.2.3 起累加到 subDropBytes
			// 供 fanoutLoop 后续 emit sub:overflow 事件。
			if n := len(ev.Data); n > 0 {
				s.subDropBytes.Add(int64(n))
				s.subDropPending.Store(true)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// 内部：三个 goroutine
// -----------------------------------------------------------------------------

// readLoop 持续从 sess 读取数据，按 16 ms / 64 KiB 阈值批量 publish 到 events 通道。
//
// v0.2 重构：拆分为两个 goroutine ——
//   - reader goroutine：纯 IO，sess.Read 拿到数据后推到内部 dataCh（buffered 8），
//     阻塞时由 main loop 的 16 ms tick 自然背压
//   - main loop：用 select 协调 done / ticker / dataCh 三个事件源，
//     把数据累积到 accumulator（最多 readMaxAccum 字节），超阈值或 tick
//     到点时一次性 publish 一个 data event
//
// 设计动机：
//   - v0.1 在 `cat large.log` 下每秒产生几千个 4 KiB event，cap-64 的
//     events 通道瞬间撑满，触发 tryPublish 静默丢事件（v0.1 行为）
//   - 16 ms batch + 32 KiB 读缓冲 + 64 KiB accumulator → 稳态吞吐 ~4 MB/s
//     （cat GB 文件仍会触发 overflow 事件 —— 预期降级路径）
//
// 远端断开时：reader 收到 err，关闭 dataCh，main loop 看到 dataCh ok=false，
// 把累积器最后 flush 一次，emit exit 事件，触发 Close。
func (s *sessionImpl) readLoop() {
	sess := s.waitForSess()
	if sess == nil {
		return
	}

	// 内部 data 通道：reader → main loop。
	// cap=readDataChSize 让 reader 在 main loop 阻塞（如 publish 卡住）时
	// 仍能继续搬运 256 KiB 的数据。
	dataCh := make(chan []byte, readDataChSize)
	// reader 的错误用单独通道传回，避免与正常数据混在 dataCh。
	// cap=1：reader 只发一次就退出。
	readErrCh := make(chan error, 1)

	// reader goroutine：纯 IO，无锁、sess 引用在闭包里安全持有。
	go func() {
		// defer close(dataCh) 让 main loop 自然走退出路径
		// （而不是从外部强杀）。
		defer close(dataCh)
		buf := make([]byte, readBufSize)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				// 复制一份再发送：buf 会被下次循环覆写。
				// 复制成本对 32 KiB 来说 < 1µs，比 syscalls 还便宜。
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case dataCh <- data:
				case <-s.done:
					return
				}
			}
			if err != nil {
				// 错误通知：s.done 已 close 时静默退出（main loop 也会走
				// 默认 EOF 路径，行为一致）。
				select {
				case readErrCh <- err:
				case <-s.done:
				}
				return
			}
		}
	}()

	// main loop：协调 ticker + data + done。
	ticker := time.NewTicker(readFlushInterval)
	defer ticker.Stop()

	// accum 初始 cap 设为 readMaxAccum 让首次 append 不触发 realloc。
	accum := make([]byte, 0, readMaxAccum)

	// flush 把累积器一次性 publish 到 events 通道；空 accum 时 no-op。
	// 注意 publish 走 tryPublish（非阻塞），所以即便 events 通道满也
	// 不会卡 main loop；代价是丢数据（计入 overflow）。
	flush := func() {
		if len(accum) == 0 {
			return
		}
		// 关键：传 accum 的切片头，tryPublish 内只读 len(ev.Data)，
		// 不会持有 accum 引用；下次 reset 时换新底层数组即可。
		ev := newDataEvent(accum)
		// 重新分配一个空 accumulator —— 不能再用 ev.Data 的内存，
		// 因为 ev 已经 publish 出去（subs 可能异步持有）。
		accum = make([]byte, 0, readMaxAccum)
		s.tryPublish(ev)
	}

	for {
		select {
		case <-s.done:
			// 关停前 best-effort flush 一次（accumulator 里的数据
			// 已经送不到 subs 也无所谓 —— session 都要关了）。
			flush()
			return
		case <-ticker.C:
			flush()
		case data, ok := <-dataCh:
			if !ok {
				// reader 退出：先把剩余 accumulator flush 出去，
				// 再 emit exit 事件，最后触发 Close。
				flush()
				var exitMsg string
				select {
				case err := <-readErrCh:
					if err == io.EOF {
						exitMsg = "EOF"
					} else {
						exitMsg = err.Error()
					}
				default:
					// reader 走 s.done 路径退出（无错误）。
					exitMsg = "EOF"
				}
				s.tryPublish(newExitEvent(exitMsg))
				_ = s.Close(true)
				return
			}
			accum = append(accum, data...)
			// 超阈值立即 flush：不等 16 ms tick，避免一个超长 cat
			// 把 accumulator 撑到几 MB 之后才一次性 broadcast。
			if len(accum) >= readMaxAccum {
				flush()
			}
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
//
// v0.2 新增：每次 broadcast 后检查 overflowPending；置位则清空计数器
// 并 broadcast 一个 overflow 事件（不经过 events 通道，直接发给 subs）。
//
// v0.2.3 新增：sub drop 旁路 emit。每次 broadcast 后再检查
// subDropPending；置位则清空计数器并 broadcast 一个 sub:overflow 事件。
// 两个 emit 顺序：先 maybeEmitOverflow（events 通道丢）再 maybeEmitSubOverflow
// （sub channel 丢）；两者**必须独立**，因为它们衡量的是不同瓶颈，
// 前端根据 type 分别诊断（"server too fast" vs "client too slow"）。
//
// 为什么不走 events 通道：overflow / sub:overflow 事件是元数据，
// 不是数据流的一环；走 events 会让 events 通道在已经溢出的场景下
// 再多占一个槽位，加剧溢出。直接 broadcast 由 fanoutLoop 的锁保护，
// 并发安全。
func (s *sessionImpl) fanoutLoop() {
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return
			}
			s.broadcast(ev)
			s.maybeEmitOverflow()
			s.maybeEmitSubOverflow()
		case <-s.done:
			// 关停前 drain 一遍 events 里的剩余事件，让 sub 看到完整时序
			for {
				select {
				case ev, ok := <-s.events:
					if !ok {
						return
					}
					s.broadcast(ev)
					s.maybeEmitOverflow()
					s.maybeEmitSubOverflow()
				default:
					return
				}
			}
		}
	}
}

// maybeEmitOverflow 在 overflowPending 置位时清空计数器并 broadcast
// 一个 overflow 事件。仅在 fanoutLoop 持有 broadcast 锁的上下文里调用。
//
// bytes > 0 才发：见 tryPublish 的"pending=true → counter>0"不变量
// 偶尔被并发 Add 打破的角落场景（v0.2 接受；v0.2.1 再优化）。
func (s *sessionImpl) maybeEmitOverflow() {
	if !s.overflowPending.Load() {
		return
	}
	// 先清 pending 再 Swap；若两者之间有并发 Add，新 Add 会再次
	// Store(true) 让下一轮再处理 —— 不丢字节。
	s.overflowPending.Store(false)
	bytes := s.overflowBytes.Swap(0)
	if bytes > 0 {
		s.broadcast(newOverflowEvent(bytes))
	}
}

// maybeEmitSubOverflow 在 subDropPending 置位时清空计数器并 broadcast
// 一个 sub:overflow 事件。仅在 fanoutLoop 里调用。
//
// v0.2.3 新增；与 maybeEmitOverflow 完全镜像（同样的并发安全论证）：
//   - pending 置位才检查（lock-free fast path）
//   - 先清 pending 再 Swap(0)，并发 Add 不会丢字节
//   - bytes > 0 才发（保留并发 corner case 的兜底）
//
// 与 maybeEmitOverflow **必须独立**：data overflow 与 sub drop 是不同瓶颈，
// 前端根据 type 分别诊断，**绝不能合并上报**（v0.2.0 既有机制 + v0.2.3
// 新增机制各管各的计数器）。
func (s *sessionImpl) maybeEmitSubOverflow() {
	if !s.subDropPending.Load() {
		return
	}
	// 先清 pending 再 Swap；若两者之间有并发 Add，新 Add 会再次
	// Store(true) 让下一轮再处理 —— 不丢字节。
	s.subDropPending.Store(false)
	bytes := s.subDropBytes.Swap(0)
	if bytes > 0 {
		s.broadcast(newSubOverflowEvent(bytes))
	}
}

// waitForSess 轮询等待 s.sess 就绪，返回 nil 表示 session 已关闭。
//
// v0.2.0a 行为变更：增加 StateFailed 退出条件。当 dialInBackground 把
// state 推到 Failed（dial 失败 / OpenSession 失败）时，readLoop / writeLoop
// 不应继续轮询等待 sess —— 失败后 s.sess 永远不会被设置，必须靠 s.done 唤醒。
// dialInBackground 失败时会先 setState(Failed) 再 signalDone；这里同时
// 查 state 是为了减少 wakeup latency。
func (s *sessionImpl) waitForSess() connect.Session {
	t := time.NewTicker(sessReadyPollInterval)
	defer t.Stop()
	for {
		st := State(s.state.Load())
		if st == StateClosed || st == StateFailed {
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
