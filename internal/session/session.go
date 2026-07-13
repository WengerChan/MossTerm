// Package session 维护 MossTerm 业务层的"会话"概念。
//
// 一个 Session 对应一个终端 tab（v0.2+ 引入 Pane 子树）。
// 维护状态机：Connecting → Authenticating → Established → Closing → Closed → Failed。
//
// 并发模型：每个 Session 三个 goroutine —— connectLoop（握手指令）、
// writeLoop（合并 inputCh 写入 PTY）、readLoop（PTY → Events）。
// 输入批处理：16 ms 或 4 KB 阈值 flush。
package session

import (
	"context"
	"errors"
)

// ID 是会话唯一标识，UUID v4 字符串形式。
type ID string

// State 表示 Session 当前所处的状态。
//
// 状态机：
//   Connecting → Authenticating → Established → Closing → Closed
//                                              ↘ Failed（任意阶段）
type State int32

const (
	StateConnecting State = iota
	StateAuthenticating
	StateEstablished
	StateClosing
	StateClosed
	StateFailed
)

// String 返回 State 的稳定字符串形式。
//
// 该字符串会序列化到前端，前端 SessionState 类型必须与之对应。
func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateAuthenticating:
		return "authenticating"
	case StateEstablished:
		return "established"
	case StateClosing:
		return "closing"
	case StateClosed:
		return "closed"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Info 是序列化给前端的会话元数据，不含任何秘密。
type Info struct {
	ID        ID     `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	User      string `json:"user"`
	Protocol  string `json:"protocol"`
	State     State  `json:"state"`
	CreatedAt int64  `json:"createdAt"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// OpenRequest 是 session.Manager.Open 的入参。
//
// 所有字段都来自前端 OpenSession 调用，profileID 可选（手动连接时为空）。
type OpenRequest struct {
	ProfileID string            `json:"profileId,omitempty"`
	Host      string            `json:"host"`
	Port      int               `json:"port"`
	User      string            `json:"user"`
	Auth      AuthSpec          `json:"auth"`
	Columns   int               `json:"cols"`
	Rows      int               `json:"rows"`
	Env       map[string]string `json:"env,omitempty"`
	JumpVia   []JumpHop         `json:"jumpVia,omitempty"`
}

// AuthSpec 描述一次会话所需的身份验证方式。
//
// kind 取值："password" | "publickey" | "agent" | "keyboard-interactive"。
// password / keyId / passphrase 仅对应 kind 时才有意义。
type AuthSpec struct {
	Kind       string `json:"kind"`
	Password   string `json:"password,omitempty"`
	KeyID      string `json:"keyId,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// JumpHop 描述跳板链中的一跳。
//
// 当前 v0.1 不使用，留作 v0.2 接口。
type JumpHop struct {
	ProfileID string `json:"profileId"`
}

// Resize 表示一次 PTY 窗口尺寸调整。
type Resize struct {
	Cols int
	Rows int
}

// Event 是 Session 推送给订阅者的消息。
//
// type 取值："data" | "state" | "exit" | "error"；其余字段按 type 解释。
type Event struct {
	Type    string `json:"type"`
	Data    []byte `json:"data,omitempty"`
	State   State  `json:"state,omitempty"`
	ExitMsg string `json:"exitMsg,omitempty"`
	Err     string `json:"err,omitempty"`
	At      int64  `json:"at"`
}

// Session 是 MossTerm 业务层会话。
//
// 业务层包装一个 connect.Connector + pty.PTY，
// 对外暴露：Start / Input / Resize / Subscribe / Close / Info / State。
type Session interface {
	// Start 启动连接与交互循环。
	// 必须在 New 之后立刻调用；多次调用返回 error。
	Start(ctx context.Context) error
	// Input 把用户按键写入 PTY。非阻塞。
	// 当 inputCh 满时返回 ErrInputFull，调用方应等待后重发。
	Input(data []byte) error
	// Resize 调整 PTY 窗口大小。
	Resize(cols, rows int) error
	// Subscribe 订阅 Session 事件；返回的 cancel 取消订阅。
	Subscribe() (<-chan Event, func())
	// Close 关闭 Session。force=true 时跳过优雅退出。
	Close(force bool) error
	// Info 返回当前会话元数据快照（原子读）。
	Info() Info
	// State 返回当前状态（原子读）。
	State() State
}

// ErrInputFull 在 Session.Input 的输入通道已满时返回。
//
// 调用方应等待极短时间后重发；v0.1 不会做 backpressure 监控。
var ErrInputFull = errors.New("session input channel full")

// ErrSessionClosed 在已 Close 的 Session 上调用 Start / Input 时返回。
var ErrSessionClosed = errors.New("session already closed")
