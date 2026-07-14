// Package transfer 提供多连接并发 + 断点续传的文件传输能力。
//
// 设计要点：
//   - 多连接并发（默认 4 分片）+ 断点续传（SHA-256 校验）。
//   - 进度通过 Wails 事件总线推送到前端（transfer:progress）。
//   - 队列上限：默认同时 3 任务 × 4 chunk = 12 并发 SFTP 请求。
package transfer

import (
	"context"
	"time"
)

// Direction 表示传输方向。
type Direction int

const (
	// DirectionUpload 是本地 → 远端。
	DirectionUpload Direction = iota
	// DirectionDownload 是远端 → 本地。
	DirectionDownload
)

// JobID 是传输任务的唯一标识。
type JobID string

// JobState 是传输任务的运行时状态。
//
// 数值与 v0.5.10 引入的 streaming upload Manager 共享（StateRunning
// / StateCompleted / StateFailed / StateCanceled 互通）。
type JobState int

const (
	StateQueued JobState = iota
	StateRunning
	StatePaused
	StateCompleted
	StateFailed
	StateCanceled
)

// Job 是传输任务的完整描述。
//
// 序列化到前端时所有字段都暴露，但 *secret* 类字段不会出现
// （Engine 在 Enqueue 时已经把凭据消化掉）。
type Job struct {
	ID          JobID     `json:"id"`
	Direction   Direction `json:"direction"`
	LocalPath   string    `json:"localPath"`
	RemotePath  string    `json:"remotePath"`
	Size        int64     `json:"size"`
	Transferred int64     `json:"transferred"`
	Speed       int64     `json:"speed"`
	State       JobState  `json:"state"`
	Error       string    `json:"error,omitempty"`
	StartedAt   int64     `json:"startedAt"`
}

// OverwriteMode 是当目标已存在时的处理策略。
type OverwriteMode int

const (
	// OverwriteAsk 弹窗询问用户（v0.2+）。
	OverwriteAsk OverwriteMode = iota
	// OverwriteAlways 总是覆盖。
	OverwriteAlways
	// OverwriteNever 总是跳过。
	OverwriteNever
	// OverwriteResume 断点续传（要求同 size 且 SHA-256 校验通过）。
	OverwriteResume
)

// Options 描述一次传输的可调参数。
type Options struct {
	Chunks        int
	ChunkSize     int64
	Overwrite     OverwriteMode
	PreserveMode  bool
}

// Progress 是订阅者收到的事件。
//
// v0.5.10 重命名为 EngineProgress（避开 streaming upload 的 Progress 类型）。
// 字段含义：v0.1 占位，未实际 emit；订阅 API 也不实现。
type EngineProgress struct {
	JobID       JobID         `json:"jobId"`
	Transferred int64         `json:"transferred"`
	Speed       int64         `json:"speed"`
	Eta         time.Duration `json:"eta"`
}

// Engine 是传输队列与执行器。
type Engine interface {
	// Enqueue 把一个 Job 放进队列；返回 JobID。
	// 凭据必须已经在调用方消化（不要把密码塞进 Job）。
	Enqueue(job Job, opts Options) (JobID, error)
	// List 列出全部 Job 状态。
	List() []Job
	// Get 按 ID 取一个 Job。
	Get(id JobID) (Job, bool)
	// Pause / Resume / Cancel 控制单个 Job。
	Pause(id JobID) error
	Resume(id JobID) error
	Cancel(id JobID) error
	// Subscribe 订阅 Progress 事件。
	Subscribe() (<-chan EngineProgress, func())
}

// MemoryEngine 是 Engine 的进程内实现。
//
// 内部维护一个 channel queue + 多个 worker goroutine。
type MemoryEngine struct {
	// 真实实现走 dependency injection（注入 sftpclient.Client 工厂）。
	// 当前仅占位。
}

// New 构造一个 MemoryEngine。
func New() *MemoryEngine {
	return &MemoryEngine{}
}

// Enqueue 实现 Engine.Enqueue。
func (e *MemoryEngine) Enqueue(job Job, opts Options) (JobID, error) {
	panic("transfer.MemoryEngine.Enqueue: not implemented")
}

// List 实现 Engine.List。
func (e *MemoryEngine) List() []Job {
	panic("transfer.MemoryEngine.List: not implemented")
}

// Get 实现 Engine.Get。
func (e *MemoryEngine) Get(id JobID) (Job, bool) {
	panic("transfer.MemoryEngine.Get: not implemented")
}

// Pause 实现 Engine.Pause。
func (e *MemoryEngine) Pause(id JobID) error {
	panic("transfer.MemoryEngine.Pause: not implemented")
}

// Resume 实现 Engine.Resume。
func (e *MemoryEngine) Resume(id JobID) error {
	panic("transfer.MemoryEngine.Resume: not implemented")
}

// Cancel 实现 Engine.Cancel。
func (e *MemoryEngine) Cancel(id JobID) error {
	panic("transfer.MemoryEngine.Cancel: not implemented")
}

// Subscribe 实现 Engine.Subscribe。
func (e *MemoryEngine) Subscribe() (<-chan EngineProgress, func()) {
	panic("transfer.MemoryEngine.Subscribe: not implemented")
}

// 编译期断言。
var _ Engine = (*MemoryEngine)(nil)

// 占位：context 包预引用。
var _ = context.Background
