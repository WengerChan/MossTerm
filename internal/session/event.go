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

	// EventTypeOverflow 是预留事件类型（v0.2+ 启用）。
	//
	// 当 readLoop 累积的未消费数据超过阈值时会 emit，提醒前端做丢帧。
	// v0.1 的实现是直接丢最早数据 + 静默，不 emit 此事件。
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
