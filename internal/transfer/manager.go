// Package transfer 的 manager.go：v0.5.10 streaming upload 任务管理器。
//
// 设计要点：
//   - Manager 持有 active + completed jobs 的内存 map（jobsMu 保护）
//   - StartUpload：spawn 后台 goroutine 跑 Upload 函数；不阻塞 wailsbinding
//   - CancelUpload：ctx cancel + 从 active 移除
//   - ListTransfers / GetTransfer：frontend 用 polling 兜底（事件丢失场景）
//   - ProgressCallback 把 transfer.Progress 转发到 Wails 事件总线
//     "transfer:progress" / "transfer:done" / "transfer:error"
//   - Manager 接受一个 Emitter 接口（与 app.EventEmitter 同款），
//     不直接 import wails —— main.go 注入 wailsEmitter 适配器
//
// 为什么单独的 Manager 不复用 transfer.Engine 接口：
//   - Engine 接口（v0.1 占位）描述通用多任务队列（含 Pause/Resume 等
//     未来扩展），与 v0.5.10 单一 upload 焦点不直接对齐
//   - v0.5.10 选择 focused Manager 简单实现；v0.6+ 再让 Engine 适配
//   - 现阶段保持 Engine stub 不动（panic 占位），新 Manager 走自己的路径
package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/mossterm/mossterm/internal/session"
)

// Emitter 是 Manager 推送 Wails 事件用的薄抽象。
//
// 对齐 app.EventEmitter 签名（避免 import 循环）。
// 实现由 wailsbindings 注入。
type Emitter interface {
	Emit(ctx context.Context, event string, data ...interface{})
}

// Wails 事件 topic。
const (
	// EventProgress 推送单次 Progress 快照（节流 200ms 一次）。
	EventProgress = "transfer:progress"
	// EventDone 推送 JobInfo（最终状态：State=Completed）。
	EventDone = "transfer:done"
	// EventError 推送 JobInfo（最终状态：State=Failed 或 Canceled）。
	EventError = "transfer:error"
	// EventAdded 推送 JobInfo（新任务入队，v0.5.10 由前端 polling 兜底；事件保留备用）。
	EventAdded = "transfer:added"
)

// UploaderFactory 根据 sessionID 返回 Uploader。
//
// wailsbinding 注入：用 *app.App.SftpClient() 拿 sftpclient.Client，
// 包装成 transfer.Uploader。
//
// 返回 nil 表示 session 不存在 / 未 established；调用方应回 error 给前端。
type UploaderFactory func(sessionID session.ID) (Uploader, error)

// Manager 是 v0.5.10 streaming upload 的运行时 manager。
//
// 线程安全：所有公开方法都走 jobsMu 保护。
// 生命周期：进程内单例（wailsbinding 持有），与 Wails App 同寿命。
type Manager struct {
	mu              sync.RWMutex
	jobs            map[string]*jobEntry
	manifestDir     string
	uploaderFactory UploaderFactory
	emitter         Emitter
	log             *slog.Logger
}

type jobEntry struct {
	Info   JobInfo
	cancel context.CancelFunc
	doneCh chan struct{} // 关闭即 job 结束
}

// NewManager 构造一个 Manager。
//
// 必要参数：factory（拿 sftpclient.Client）+ emitter（推 Wails 事件）。
// manifestDir 为空时用 DefaultManifestDir()。
// log 为 nil 时用 slog.Default()。
func NewManager(manifestDir string, factory UploaderFactory, emitter Emitter, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		jobs:            make(map[string]*jobEntry),
		manifestDir:     manifestDir,
		uploaderFactory: factory,
		emitter:         emitter,
		log:             log,
	}
}

// SetUploaderFactory 替换 uploader factory（单元测试注入 fake）。
func (m *Manager) SetUploaderFactory(f UploaderFactory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uploaderFactory = f
}

// SetEmitter 替换 emitter。
func (m *Manager) SetEmitter(e Emitter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emitter = e
}

// ManifestDir 返回 manager 用的 manifest 目录。
func (m *Manager) ManifestDir() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.manifestDir
}

// GenerateTransferID 生成新 transferID（16 字节随机 hex）。
//
// 调用方一般用 wailsbinding.StartUpload 内部调。
func GenerateTransferID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 在 Linux/macOS 不太可能失败；兜底用时间戳
		return fmt.Sprintf("tx-%d", time.Now().UnixNano())
	}
	return "tx-" + hex.EncodeToString(b[:])
}

// StartUpload 启动一次 streaming upload（后台 goroutine）。
//
// 返回 transferID（用 req.TransferID 若非空，否则生成）。
// 后台 goroutine 跑 Upload()，通过 emitter 推 Wails 事件。
//
// 同一 transferID 重复 StartUpload 会被 reject（防覆盖）。
// sessionID 通过 parentCtx 携带（用 WithSessionID 注入）；
// 立即取出来做 uploader factory 早失败检查（不 spawn 死 goroutine）。
func (m *Manager) StartUpload(parentCtx context.Context, req UploadRequest) (string, error) {
	// 校验 factory
	m.mu.RLock()
	factory := m.uploaderFactory
	emitter := m.emitter
	manifestDir := m.manifestDir
	m.mu.RUnlock()

	if factory == nil {
		return "", errors.New("transfer.Manager: uploader factory not configured")
	}

	// 校验 req
	if err := req.Validate(); err != nil {
		return "", err
	}

	// sessionID 必须从 ctx 注入
	sid := sessionIDFromCtx(parentCtx)
	if sid == "" {
		return "", errors.New("transfer.Manager: sessionID missing in ctx")
	}

	id := req.TransferID
	if id == "" {
		id = GenerateTransferID()
		req.TransferID = id
	}

	m.mu.Lock()
	if _, exists := m.jobs[id]; exists {
		m.mu.Unlock()
		return "", fmt.Errorf("transfer.Manager: transferID %q already running", id)
	}
	// 派生 ctx（带 cancel + sessionID）
	jobCtx, cancel := context.WithCancel(context.Background())
	jobCtx = WithSessionID(jobCtx, session.ID(sid))
	entry := &jobEntry{
		Info: JobInfo{
			TransferID:  id,
			LocalPath:   req.LocalPath,
			RemotePath:  req.RemotePath,
			ChunkSize:   normalizeChunkSize(req.ChunkSize),
			Concurrency: normalizeConcurrency(req.Concurrency),
			State:       StateRunning,
			StartedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
		cancel: cancel,
		doneCh: make(chan struct{}),
	}
	m.jobs[id] = entry
	m.mu.Unlock()

	// 后台 goroutine 跑 Upload
	go m.runJob(jobCtx, entry, req, manifestDir, emitter, factory)

	return id, nil
}

// runJob 是后台 goroutine 入口。
//
// 串接：factory 拿 uploader → 调 Upload → emit 进度事件 → emit done/error。
// ctx 取消时 Upload() 内部 worker 检测到，Upload 返回 ErrUploadFailed，
// 我们 emit error 事件 + 把 state 改 Failed。
func (m *Manager) runJob(ctx context.Context, entry *jobEntry, req UploadRequest, manifestDir string, emitter Emitter, factory UploaderFactory) {
	defer close(entry.doneCh)

	sid := sessionIDFromCtx(ctx)
	uploader, err := factory(session.ID(sid))
	if err != nil {
		m.finalizeJob(entry, StateFailed, "", fmt.Sprintf("uploader factory: %v", err), emitter)
		return
	}

	// 进度回调：更新 entry + emit Wails 事件
	progressFn := func(p Progress) {
		m.mu.Lock()
		entry.Info.BytesSent = p.BytesSent
		entry.Info.TotalBytes = p.TotalBytes
		entry.Info.UpdatedAt = time.Now()
		m.mu.Unlock()
		if emitter != nil {
			emitter.Emit(ctx, EventProgress, p)
		}
	}

	// 跑 Upload
	uploadErr := Upload(ctx, uploader, req, manifestDir, progressFn)

	// 收尾
	if uploadErr != nil {
		// 区分 cancel vs fail
		if errors.Is(uploadErr, context.Canceled) {
			m.finalizeJob(entry, StateCanceled, "", uploadErr.Error(), emitter)
			return
		}
		if errors.Is(uploadErr, ErrUploadFailed) && ctx.Err() != nil {
			m.finalizeJob(entry, StateCanceled, "", ctx.Err().Error(), emitter)
			return
		}
		m.finalizeJob(entry, StateFailed, "", uploadErr.Error(), emitter)
		return
	}

	// 成功：读 manifest 拿 checksum（Upload 完成后 manifest 已删，所以
	// checksum 在 done 事件里走单独路径——这里留空，wailsbinding.GetTransfer
	// 不再能拿，但前端也不需要了，传输已完成）
	m.finalizeJob(entry, StateCompleted, "", "", emitter)
}

func (m *Manager) finalizeJob(entry *jobEntry, state JobState, checksum, errMsg string, emitter Emitter) {
	m.mu.Lock()
	entry.Info.State = state
	entry.Info.Checksum = checksum
	entry.Info.Error = errMsg
	entry.Info.UpdatedAt = time.Now()
	info := entry.Info
	m.mu.Unlock()

	if emitter == nil {
		return
	}
	switch state {
	case StateCompleted:
		emitter.Emit(context.Background(), EventDone, info)
	case StateCanceled, StateFailed:
		emitter.Emit(context.Background(), EventError, info)
	}
}

// CancelUpload 取消一个 transfer。
//
// 不存在 → 返回 error。
// 取消后 entry 状态由 runJob 协程异步改 Canceled；前端用 ListTransfers
// 看到 State=Canceled（不阻塞等待）。
func (m *Manager) CancelUpload(transferID string) error {
	m.mu.Lock()
	entry, ok := m.jobs[transferID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("transfer.Manager: transferID %q not found", transferID)
	}
	entry.cancel()
	return nil
}

// ListTransfers 返回所有 jobs（active + 已结束）的快照。
//
// 排序：按 StartedAt 倒序（最新在前）。
func (m *Manager) ListTransfers() []JobInfo {
	m.mu.RLock()
	out := make([]JobInfo, 0, len(m.jobs))
	for _, e := range m.jobs {
		out = append(out, e.Info)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// GetTransfer 按 ID 取一个 job 快照。
//
// 不存在 → (zero, false)。
func (m *Manager) GetTransfer(transferID string) (JobInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.jobs[transferID]
	if !ok {
		return JobInfo{}, false
	}
	return entry.Info, true
}

// Cleanup 清理已完成的 jobs（remove from map；保留活跃）。
//
// 完成的判定：State ∈ {Completed, Failed, Canceled}。
// 调用方一般定期调用（v0.5.10 不暴露给前端，wailsbinding 不接）。
// v0.5.10 行为：等 doneCh 关闭后调一次（goroutine 退出 = 内存可释放）。
func (m *Manager) Cleanup() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for id, e := range m.jobs {
		if e.Info.State == StateCompleted || e.Info.State == StateFailed || e.Info.State == StateCanceled {
			delete(m.jobs, id)
			removed++
		}
	}
	return removed
}

// -----------------------------------------------------------------------------
// sessionID ctx 注入
// -----------------------------------------------------------------------------

type sessionIDKey struct{}

// WithSessionID 把 sessionID 注入 ctx（wailsbinding.StartUpload 用）。
func WithSessionID(ctx context.Context, sid session.ID) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, string(sid))
}

func sessionIDFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(sessionIDKey{}).(string); ok {
		return v
	}
	return ""
}
