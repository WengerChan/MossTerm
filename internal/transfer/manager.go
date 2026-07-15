// Package transfer 的 manager.go：v0.5.10 streaming upload + v0.6.0
// streaming download 共享的 Manager。
//
// 设计要点：
//   - Manager 持有 active + completed jobs 的内存 map（jobsMu 保护）
//   - StartUpload / StartDownload：spawn 后台 goroutine 跑 Upload / Download
//     函数；不阻塞 wailsbinding
//   - CancelUpload / CancelDownload：ctx cancel + 从 active 移除
//   - ListTransfers / GetTransfer：frontend 用 polling 兜底（事件丢失场景）
//   - ProgressCallback 把 transfer.Progress 转发到 Wails 事件总线
//     "transfer:progress" / "transfer:done" / "transfer:error"
//   - Manager 接受一个 Emitter 接口（与 app.EventEmitter 同款），
//     不直接 import wails —— main.go 注入 wailsEmitter 适配器
//
// v0.6.0 扩展：
//   - 加 DownloaderFactory：startDownload 通过 ctx 注入的 sessionID 拿 Downloader
//   - 加 StartDownload / CancelDownload（与 upload 对称）
//   - jobs map 用 transferID 作 key；同一 ID 的 upload/download 会冲突
//     （v0.6 行为：前者失败）—— v0.6 期望前端 ID 用 GenerateTransferID()
//     生成（独立空间），工程角度够用；v0.7+ 想要更严格可加 direction 前缀
//
// 为什么单独的 Manager 不复用 transfer.Engine 接口：
//   - Engine 接口（v0.1 占位）描述通用多任务队列（含 Pause/Resume 等
//     未来扩展），与 v0.5.10/v0.6.0 upload+download 焦点不直接对齐
//   - v0.5.10/v0.6.0 选择 focused Manager 简单实现；v0.7+ 再让 Engine 适配
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

// DownloaderFactory 根据 sessionID 返回 Downloader。
//
// v0.6.0 加：与 UploaderFactory 同款签名（factory 函数 + sessionID lookup），
// 区别只返回的接口是 Downloader。wailsbinding 把 *sftpclient.Client
// 适配成 Downloader（见 internal/app/download_adapter.go）。
type DownloaderFactory func(sessionID session.ID) (Downloader, error)

// Manager 是 v0.5.10/v0.6.0 streaming upload + download 的运行时 manager。
//
// 线程安全：所有公开方法都走 jobsMu 保护。
// 生命周期：进程内单例（wailsbinding 持有），与 Wails App 同寿命。
type Manager struct {
	mu                sync.RWMutex
	jobs              map[string]*jobEntry
	manifestDir       string
	uploaderFactory   UploaderFactory
	downloaderFactory DownloaderFactory
	emitter           Emitter
	log               *slog.Logger
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
//
// v0.6.0 起：downloaderFactory 仍走 SetDownloaderFactory 注入（与
// UploaderFactory 风格一致；保留 NewManager 签名向后兼容）。
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

// SetDownloaderFactory 设置 downloader factory（v0.6.0 新增）。
//
// 走 Setter 而非 NewManager 第 5 参：保持 v0.5.10 NewManager 签名稳定
// （main.go / app.New / 测试都不需要改）。DownloaderFactory 可选 ——
// 不设置时调 StartDownload 会返回 "downloader factory not configured"。
func (m *Manager) SetDownloaderFactory(f DownloaderFactory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.downloaderFactory = f
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

// jobRunner 是 streaming upload / download 共享的 goroutine 入口。
//
// v0.6.0 重构：原本 StartUpload + runJob / StartDownload + runDownloadJob
// 是 4 段几乎一致的代码（dupl lint 命中 4 次）。提取为：
//   - startJob：参数校验 + jobs map 登记 + ctx 派生 + 启动 goroutine
//   - jobRunner：factory 调用 + 进度回调 + 错误分类 + finalizeJob
//
// startJob 和 jobRunner 之间用 closure 串接（runner func 由调用方闭包构造，
// 携带方向 + 协议特定的 Upload/Download 调用）。
type jobRunner func(ctx context.Context, entry *jobEntry, manifestDir string, emitter Emitter)

// StartUpload 启动一次 streaming upload（后台 goroutine）。
//
// 返回 transferID（用 req.TransferID 若非空，否则生成）。
// 后台 goroutine 跑 Upload()，通过 emitter 推 Wails 事件。
//
// 同一 transferID 重复 StartUpload 会被 reject（防覆盖）。
// sessionID 通过 parentCtx 携带（用 WithSessionID 注入）；
// 立即取出来做 uploader factory 早失败检查（不 spawn 死 goroutine）。
func (m *Manager) StartUpload(parentCtx context.Context, req UploadRequest) (string, error) {
	if err := req.Validate(); err != nil {
		return "", err
	}
	runner := func(ctx context.Context, entry *jobEntry, manifestDir string, emitter Emitter) {
		m.runUpload(ctx, entry, req, manifestDir, emitter)
	}
	return m.startJob(parentCtx, req.TransferID, DirectionUpload, req.LocalPath, req.RemotePath, req.ChunkSize, req.Concurrency, runner, func() (any, error) {
		m.mu.RLock()
		f := m.uploaderFactory
		m.mu.RUnlock()
		if f == nil {
			return nil, errors.New("transfer.Manager: uploader factory not configured")
		}
		return f, nil
	})
}

// StartDownload 启动一次 streaming download（后台 goroutine）。
//
// 与 StartUpload 对称：
//   - 返回 transferID（用 req.TransferID 若非空，否则生成）
//   - 同一 transferID 重复启动会被 reject（防覆盖；upload/download 共享 jobs map）
//   - sessionID 通过 parentCtx 携带（用 WithSessionID 注入）
//   - 失败 / 取消走同 finalizeJob 路径
//
// v0.6.0 行为：downloader factory 没配置时直接 reject（不 spawn 死 goroutine）。
//
//nolint:gocritic // 88B DownloadRequest 值传与 StartUpload 保持对称；hugeParam 延后到 v0.7
func (m *Manager) StartDownload(parentCtx context.Context, req DownloadRequest) (string, error) {
	if err := req.Validate(); err != nil {
		return "", err
	}
	runner := func(ctx context.Context, entry *jobEntry, manifestDir string, emitter Emitter) {
		m.runDownload(ctx, entry, req, manifestDir, emitter)
	}
	return m.startJob(parentCtx, req.TransferID, DirectionDownload, req.LocalPath, req.RemotePath, req.ChunkSize, req.Concurrency, runner, func() (any, error) {
		m.mu.RLock()
		f := m.downloaderFactory
		m.mu.RUnlock()
		if f == nil {
			return nil, errors.New("transfer.Manager: downloader factory not configured")
		}
		return f, nil
	})
}

// startJob 是 StartUpload / StartDownload 共享的"参数校验 + 入队 + 启动 goroutine"骨架。
//
// 行为：
//  1. factory 早失败检查（factoryGetter 取工厂；nil → 返回 error，不 spawn 死 goroutine）
//  2. 校验 sessionID 注入 ctx
//  3. 分配 transferID（reqTransferID 非空就用它，否则 GenerateTransferID）
//  4. jobs map 登记（同 transferID 重复 → reject）
//  5. 派生 jobCtx（带 cancel + sessionID）
//  6. 启动 runner goroutine
//
// 返回最终 transferID。工厂类型用 any（具体 UploaderFactory / DownloaderFactory
// 差异在 runner 闭包里处理）；这里只关心"工厂是否已配置"。
func (m *Manager) startJob(
	parentCtx context.Context,
	reqTransferID string,
	direction Direction,
	localPath, remotePath string,
	chunkSize, concurrency int,
	runner jobRunner,
	factoryGetter func() (any, error),
) (string, error) {
	if _, err := factoryGetter(); err != nil {
		return "", err
	}

	sid := sessionIDFromCtx(parentCtx)
	if sid == "" {
		return "", errors.New("transfer.Manager: sessionID missing in ctx")
	}

	id := reqTransferID
	if id == "" {
		id = GenerateTransferID()
	}

	m.mu.RLock()
	emitter := m.emitter
	manifestDir := m.manifestDir
	m.mu.RUnlock()

	m.mu.Lock()
	if _, exists := m.jobs[id]; exists {
		m.mu.Unlock()
		return "", fmt.Errorf("transfer.Manager: transferID %q already running", id)
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	jobCtx = WithSessionID(jobCtx, session.ID(sid))
	entry := &jobEntry{
		Info: JobInfo{
			TransferID:  id,
			Direction:   direction,
			LocalPath:   localPath,
			RemotePath:  remotePath,
			ChunkSize:   normalizeChunkSize(chunkSize),
			Concurrency: normalizeConcurrency(concurrency),
			State:       StateRunning,
			StartedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
		cancel: cancel,
		doneCh: make(chan struct{}),
	}
	m.jobs[id] = entry
	m.mu.Unlock()

	//nolint:contextcheck // jobCtx 已通过 context.WithCancel + WithSessionID 派生；走具名函数
	go m.runJobInheritCtx(jobCtx, entry, manifestDir, emitter, runner)

	return id, nil
}

// runJobInheritCtx 是 go runner(...) 的薄包装：golangci-lint contextcheck
// 要求"go func()" 启动 goroutine 走"继承自参数 ctx"的具名函数。
// 这里 jobCtx 已经用 WithCancel + WithSessionID 派生（startJob 内部），
// 把它当 ctx 传给 runner 是"继承"语义；具名函数让 lint 识别为继承。
func (m *Manager) runJobInheritCtx(jobCtx context.Context, entry *jobEntry, manifestDir string, emitter Emitter, runner jobRunner) {
	runner(jobCtx, entry, manifestDir, emitter)
}

// runUpload 是 streaming upload 的 goroutine 入口。
//
// 串接：factory 拿 uploader → 调 Upload → emit 进度事件 → emit done/error。
// ctx 取消时 Upload() 内部 worker 检测到，Upload 返回 ErrUploadFailed，
// 我们 emit error 事件 + 把 state 改 Failed。
//
//nolint:gocritic // 88B UploadRequest 值传与 v0.5.10 StartUpload 保持一致；hugeParam 延后到 v0.7
func (m *Manager) runUpload(ctx context.Context, entry *jobEntry, req UploadRequest, manifestDir string, emitter Emitter) {
	defer close(entry.doneCh)
	m.executeJob(ctx, entry, manifestDir, emitter, ErrUploadFailed, func() error {
		m.mu.RLock()
		factory := m.uploaderFactory
		m.mu.RUnlock()
		if factory == nil {
			return errors.New("uploader factory not configured")
		}
		sid := sessionIDFromCtx(ctx)
		uploader, err := factory(session.ID(sid))
		if err != nil {
			return fmt.Errorf("uploader factory: %v", err)
		}
		return Upload(ctx, uploader, req, manifestDir, m.makeProgressFn(ctx, entry, emitter))
	})
}

// runDownload 是 streaming download 的 goroutine 入口。
//
// 与 runUpload 同结构：
//   - factory 拿 downloader → 调 Download → emit 进度事件 → emit done/error
//   - ctx 取消时 Download() 内部 worker 检测到，Download 返回 ErrDownloadFailed
//
//nolint:gocritic // 88B DownloadRequest 值传与 runUpload 保持对称；hugeParam 延后到 v0.7
func (m *Manager) runDownload(ctx context.Context, entry *jobEntry, req DownloadRequest, manifestDir string, emitter Emitter) {
	defer close(entry.doneCh)
	m.executeJob(ctx, entry, manifestDir, emitter, ErrDownloadFailed, func() error {
		m.mu.RLock()
		factory := m.downloaderFactory
		m.mu.RUnlock()
		if factory == nil {
			return errors.New("downloader factory not configured")
		}
		sid := sessionIDFromCtx(ctx)
		downloader, err := factory(session.ID(sid))
		if err != nil {
			return fmt.Errorf("downloader factory: %v", err)
		}
		return Download(ctx, downloader, req, manifestDir, m.makeProgressFn(ctx, entry, emitter))
	})
}

// executeJob 是 runUpload / runDownload 共享的"调 work 闭包 + cancel vs fail 分类 + finalizeJob"骨架。
//
// wrappedErr 区分 upload / download（ErrUploadFailed / ErrDownloadFailed）；
// 分类规则（与 v0.5.10 一致）：
//   - context.Canceled → StateCanceled
//   - wrappedErr + ctx.Err() != nil → StateCanceled
//   - 其他 → StateFailed
//   - nil err → StateCompleted
func (m *Manager) executeJob(ctx context.Context, entry *jobEntry, _ string, emitter Emitter, wrappedErr error, work func() error) {
	runErr := work()
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) {
			m.finalizeJob(ctx, entry, StateCanceled, "", runErr.Error(), emitter)
			return
		}
		if errors.Is(runErr, wrappedErr) && ctx.Err() != nil {
			m.finalizeJob(ctx, entry, StateCanceled, "", ctx.Err().Error(), emitter)
			return
		}
		m.finalizeJob(ctx, entry, StateFailed, "", runErr.Error(), emitter)
		return
	}
	m.finalizeJob(ctx, entry, StateCompleted, "", "", emitter)
}

// makeProgressFn 返回统一的 Progress 回调：写 entry.Info + emit Wails 事件。
func (m *Manager) makeProgressFn(ctx context.Context, entry *jobEntry, emitter Emitter) func(Progress) {
	return func(p Progress) {
		m.mu.Lock()
		entry.Info.BytesSent = p.BytesSent
		entry.Info.TotalBytes = p.TotalBytes
		entry.Info.UpdatedAt = time.Now()
		m.mu.Unlock()
		if emitter != nil {
			emitter.Emit(ctx, EventProgress, p)
		}
	}
}

// finalizeJob 把 jobEntry 状态写到 JobInfo + emit Wails 事件。
//
// _ context.Context 是 v0.6.0 引入（满足 contextcheck lint 要求"ctx 不丢"）；
// 当前实现 emit 用 background ctx（事件 emit 不挂 ctx），保留 ctx 形参是
// 为未来"用 ctx 控制 emit 超时"留口子（v0.7+ 真要 cancel-aware emit 时不
// 改签名）。
func (m *Manager) finalizeJob(_ context.Context, entry *jobEntry, state JobState, checksum, errMsg string, emitter Emitter) {
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
	return m.cancelJob(transferID)
}

// CancelDownload 取消一个 download transfer（与 CancelUpload 对称）。
//
// 内部走同一 cancelJob 路径（jobs map 共享；cancel 与方向无关）。
// v0.6.0 保留为独立 API 是为前端调用语义清晰；底层不重复实现。
func (m *Manager) CancelDownload(transferID string) error {
	return m.cancelJob(transferID)
}

// cancelJob 是 CancelUpload / CancelDownload 共享的内部实现。
func (m *Manager) cancelJob(transferID string) error {
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
