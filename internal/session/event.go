// event.go 定义 Session 事件类型常量与构造 helper。
//
// 事件类型与 pkg/event 的 topic 字符串不是同一回事：
//   - pkg/event.EventSessionData = "session:data" —— Wails 事件总线 topic
//   - session.EventTypeData     = "data"          —— Session.Event.Type 字段值
//
// 前者是网络层事件名，后者是 Session 内部消息的 type 字段。
// Wails bindings 层负责把 session.Event 包装成 pkg/event 的 payload。
package session

import "time"

// EventType 是 Event.Type 字段的合法取值。
//
// 约定：Type 为 snake_case 字符串，便于前端通过 enum 镜像。
type EventType string

const (
	// EventTypeData 表示从远端读取到的一段 PTY 输出。
	//
	// Event.Data 非空，长度由 readLoop 的 buffer 决定（v0.1 为 4 KiB 一次读取）。
	EventTypeData EventType = "data"

	// EventTypeState 表示 Session 状态发生转换。
	//
	// Event.State 携带新状态。订阅者应根据新状态更新 UI 徽标。
	EventTypeState EventType = "state"

	// EventTypeExit 表示 Session 已关闭（无论用户主动 Close 还是远端 EOF）。
	//
	// 远端主动断开时 ExitMsg 通常为 "EOF" 或 "exit status N"。
	// EventTypeExit 与 EventTypeState(StateClosed) 通常会紧邻出现，
	// 但 Exit 在前，便于订阅者先做"清理"逻辑再渲染关闭状态。
	EventTypeExit EventType = "exit"

	// EventTypeError 表示 Session 发生不可恢复错误（拨号失败 / 鉴权失败 / 远端拒绝）。
	//
	// Event.Err 携带可读的错误描述。
	// Error 事件后，Session 会自动进入 Closing → Closed 状态。
	EventTypeError EventType = "error"

	// EventTypeOverflow 表示 events 通道发生过丢数据。
	//
	// 当中央 events 通道（cap=64）被 readLoop 的 batched data event 撑满时，
	// 后续整批 data 会被 tryPublish 丢弃，并累加字节数到 overflowBytes。
	// fanoutLoop 在每条事件广播后会检查并 emit 一个 overflow 事件，
	// 携带自上次上报以来丢弃的总字节数，提醒前端做丢帧/告警。
	//
	// 触发场景：cat GB 级日志、tail -f 高频输出等"远端 > 后端 fanout 能力"
	// 的极端情况；正常交互式终端不会触发。
	EventTypeOverflow EventType = "overflow"
)

// newDataEvent 构造一个 data 事件，附上当前时间戳。
//
// helper 集中在这里避免在 readLoop 里写一堆字面量。
func newDataEvent(data []byte) Event {
	return Event{
		Type: string(EventTypeData),
		Data: data,
		At:   time.Now().UnixMilli(),
	}
}

// newStateEvent 构造一个 state 事件。
func newStateEvent(state State) Event {
	return Event{
		Type:  string(EventTypeState),
		State: state,
		At:    time.Now().UnixMilli(),
	}
}

// newErrorEvent 构造一个 error 事件。
func newErrorEvent(err error) Event {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return Event{
		Type: string(EventTypeError),
		Err:  msg,
		At:   time.Now().UnixMilli(),
	}
}

// newExitEvent 构造一个 exit 事件。
func newExitEvent(msg string) Event {
	return Event{
		Type:    string(EventTypeExit),
		ExitMsg: msg,
		At:      time.Now().UnixMilli(),
	}
}

// newOverflowEvent 构造一个 overflow 事件，携带自上次上报以来
// 因 events 通道满而丢失的字节总数。
//
// helper 集中在这里避免在 fanoutLoop 里写 Event 字面量。
func newOverflowEvent(droppedBytes int64) Event {
	return Event{
		Type:          string(EventTypeOverflow),
		OverflowBytes: droppedBytes,
		At:            time.Now().UnixMilli(),
	}
}
