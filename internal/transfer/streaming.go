// Package transfer 的 streaming.go：分片 + 并发 WriteAt + 进度 + 断点续传。
//
// v0.5.10 设计要点：
//   - 默认 4 MiB 分片（1-16 MiB 可配）；默认 2 路并发（1-4 可配）。
//   - 后端流式读本地 → 直接 SFTP WriteAt 到 remote（不经过 Go 堆缓冲整文件）。
//   - 每片完成触发 Progress 回调 + 写 manifest（断点续传凭证）。
//   - Resume 模式：读 manifest 跳过已传 chunk；启动时校验 local mtime+size，
//     不一致则拒绝续传，让用户重新传。
//   - context.Cancel 立即停（每片 Write 之前 + 之后检查 ctx）。
//   - 大文件保护：> 10 GiB 拒绝（OOM + SFTP server 端空间风险）。
//
// pkg/sftp 的 *sftp.Client 是并发安全的（"may be called concurrently from
// multiple Goroutines"），但 *sftp.File 内部 position 是共享的，**必须用
// WriteAt** 才能并发写到不同 offset，不能用 Seek + Write。
//
// 为什么不通过 sftpclient.Client.UploadFile 分片：
//   - sftpclient.Client.UploadFile 走 OpenFile(O_TRUNC) + 顺序 Write，
//     每次写都从 offset 0 开始，不支持并发也不支持断点续传。
//   - streaming.go 直接 OpenFile + WriteAt，是 v0.5.10 的新路径。
//
// 与 transfer.go Engine 接口的关系：
//   - Engine 接口（v0.1 占位）描述通用多任务队列；v0.5.10 不实现
//   - Manager（manager.go）描述 v0.5.10 streaming upload 的 jobs map
//     + 取消 + 事件推送，是 wailsbinding 实际用的对象
package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mossterm/mossterm/internal/sftpclient"
)

// -----------------------------------------------------------------------------
// 公开类型
// -----------------------------------------------------------------------------

// Default 调参常量。集中在此处便于文档化和未来调整。
const (
	// DefaultChunkSize 是默认分片大小（4 MiB）。
	// 4 MiB 是 SFTP 性能与延迟的甜点：太小 → 协议开销占比高；太大 → 大并发时
	// 内存占用多 + 出错时重试代价大。
	DefaultChunkSize = 4 * 1024 * 1024

	// DefaultConcurrency 是默认并发数（2 路）。
	// pkg/sftp 的 WriteAt 走 SFTP subsystem + SSH channel；2 路是经验值。
	// 太高反而被单 SSH channel 串行化抵消；4 路是上限。
	DefaultConcurrency = 2

	// MinChunkSize / MaxChunkSize 限制用户配置的合法范围。
	MinChunkSize = 1 * 1024 * 1024  // 1 MiB
	MaxChunkSize = 16 * 1024 * 1024 // 16 MiB

	// MinConcurrency / MaxConcurrency 限制合法范围。
	MinConcurrency = 1
	MaxConcurrency = 4

	// MaxFileSize 是 v0.5.10 大文件保护硬上限（10 GiB）。
	// 超过此值的文件拒绝上传（OOM + 远端磁盘空间风险）。
	// 调高需要先评估：本地 os.Open 没问题，但 manifest 内存 + 进度回调
	// 100MB+ 都没有问题；10 GiB 是 SFTP server 端空间也兜得住的合理值。
	MaxFileSize int64 = 10 << 30 // 10 GiB

	// ProgressEmitInterval 是 Progress 回调的最小间隔（200ms）。
	// 避免每 4 MiB 触发一次（默认 4 MiB 分片下，1 GiB 文件 = 256 次
	// 回调 → 前端事件风暴）。200ms 既能看到流畅进度条，又不刷屏。
	ProgressEmitInterval = 200 * time.Millisecond
)

// UploadRequest 描述一次 streaming upload 的输入参数。
//
// 字段语义：
//   - TransferID：调用方生成的唯一标识（manifest 文件名 + Wails 事件
//     携带）。v0.5.10 由 wailsbinding 用 crypto/rand 生成。
//   - SessionID：v0.5.10 加的字段 —— 用于 wailsbinding 内部注入 ctx
//     （transfer.WithSessionID）。Upload 本身不用（factory 在 caller
//     已经拿好 Uploader 注入 ctx），但 wailsbinding 收到 req 后
//     解析 sessionID 字段调 WithSessionID 包 ctx。
//     校验：v0.5.10 不强校验 sessionID 非空（Manager 拿不到会
//     返回 error）；前端必须传。
//   - LocalPath：本地待上传文件绝对路径。
//   - RemotePath：远端绝对路径（含文件名）。
//   - ChunkSize：0 = DefaultChunkSize；clamp 到 [MinChunkSize, MaxChunkSize]。
//   - Concurrency：0 = DefaultConcurrency；clamp 到 [MinConcurrency, MaxConcurrency]。
//   - Resume：true 时从 manifest 恢复；false 时忽略已有 manifest 重新传。
type UploadRequest struct {
	TransferID  string `json:"transferID"`
	SessionID   string `json:"sessionID,omitempty"`
	LocalPath   string `json:"localPath"`
	RemotePath  string `json:"remotePath"`
	ChunkSize   int    `json:"chunkSize,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
	Resume      bool   `json:"resume"`
}

// GetSessionID 是 Manager.StartUpload 注入的 ctx lookup helper。
//
// 实际定义在 manager.go（避免循环 import）。
// 暴露在 streaming.go 注释里方便调用方发现。

// Progress 是单次进度回调 payload。
//
// SpeedBps 是从 Upload 开始到当前的瞬时速度（不是全局平均）；调用方做
// UI 平滑（EMA / 滑动窗口）即可。EtaSec 是基于 SpeedBps 估算的剩余时间
// （SpeedBps==0 时给 -1 表示未知）。
type Progress struct {
	TransferID  string `json:"transferID"`
	BytesSent   int64  `json:"bytesSent"`
	TotalBytes  int64  `json:"totalBytes"`
	SpeedBps    int64  `json:"speedBps"`
	EtaSec      int64  `json:"etaSec"`
	ChunkIndex  int    `json:"chunkIndex"`
	TotalChunks int    `json:"totalChunks"`
}

// JobInfo 是 wailsbinding 返回给前端的"完整任务状态"。
//
// Progress 字段是"最新一次回调的快照"，方便前端用 polling 兜底
// （事件丢失时 ListTransfers/GetTransfer 仍能拿到进度）。
type JobInfo struct {
	TransferID  string    `json:"transferID"`
	LocalPath   string    `json:"localPath"`
	RemotePath  string    `json:"remotePath"`
	TotalBytes  int64     `json:"totalBytes"`
	BytesSent   int64     `json:"bytesSent"`
	State       JobState  `json:"state"`
	Error       string    `json:"error,omitempty"`
	ChunkSize   int       `json:"chunkSize"`
	Concurrency int       `json:"concurrency"`
	StartedAt   time.Time `json:"startedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Checksum    string    `json:"checksum,omitempty"`
}

// -----------------------------------------------------------------------------
// Uploader 接口
// -----------------------------------------------------------------------------

// Uploader 是 streaming upload 需要的 SFTP 写表面抽象。
//
// sftpclient.Client 通过 Open(path, flags) 返回 sftpclient.ReadWriteCloser
// （实际是 *sftp.File），实现这个接口无需新增 sftpclient 方法。
//
// Truncate 用于 v0.5.10 在并发 WriteAt 之前把远端文件撑到 totalSize。
// 多数 SFTP server 在 WriteAt 超出文件末尾时会自动扩展，但预先 Truncate
// 可以让"远端 server 端写入失败"被早发现（磁盘满等），也避免某些 server
// 在大量稀疏写入时表现奇怪。
//
// MkdirAll 用于 streaming.Upload 在打开文件前确保父目录存在
// （多数 SFTP server 不自动创建父目录；InMemHandler 也遵循）。
type Uploader interface {
	Open(path string, flags int) (sftpclient.ReadWriteCloser, error)
	Stat(path string) (sftpclient.Entry, error)
	Truncate(path string, size int64) error
	MkdirAll(path string) error
}

// -----------------------------------------------------------------------------
// 错误定义
// -----------------------------------------------------------------------------

var (
	// ErrFileTooLarge 文件超过 MaxFileSize。
	ErrFileTooLarge = errors.New("transfer: file too large (>10 GiB)")
	// ErrLocalNotFound 本地文件不存在或不是 regular file。
	ErrLocalNotFound = errors.New("transfer: local file not found or not regular")
	// ErrLocalChanged Resume 时本地文件 mtime/size 与 manifest 不一致。
	ErrLocalChanged = errors.New("transfer: local file changed since manifest (mtime/size mismatch)")
	// ErrInvalidChunkSize ChunkSize 越界。
	ErrInvalidChunkSize = errors.New("transfer: invalid chunk size")
	// ErrInvalidConcurrency Concurrency 越界。
	ErrInvalidConcurrency = errors.New("transfer: invalid concurrency")
	// ErrMissingTransferID TransferID 为空。
	ErrMissingTransferID = errors.New("transfer: empty transferID")
	// ErrMissingPaths LocalPath/RemotePath 为空。
	ErrMissingPaths = errors.New("transfer: empty local/remote path")
	// ErrUploadFailed 上传过程中出错（最常见：context 取消 / 网络中断）。
	// 错误链里带具体原因。
	ErrUploadFailed = errors.New("transfer: upload failed")
)

// -----------------------------------------------------------------------------
// 切分逻辑
// -----------------------------------------------------------------------------

// ChunkRange 描述一个分片的字节区间 [Start, End)。
//
// Start / End 是相对于 LocalPath 文件开头的字节偏移。
// End = Start + Size；最后一片的 End 可能 < TotalSize（如果 totalSize
// 不是 chunkSize 的整数倍）。
type ChunkRange struct {
	Index int   // 0-based
	Start int64
	End   int64 // 写到的字节 offset（exclusive）
}

// Plan 把 totalSize 按 chunkSize 切分成 ChunkRange 列表。
//
// chunkSize <= 0 用 DefaultChunkSize；clamp 到 [MinChunkSize, MaxChunkSize]。
// totalSize == 0 返回空 slice（0 字节文件不需要分片）。
//
// 排序：按 Index 升序（实际就是按 Start 升序）。
func Plan(totalSize int64, chunkSize int) []ChunkRange {
	if totalSize <= 0 {
		return nil
	}
	cs := int64(normalizeChunkSize(chunkSize))
	count := int((totalSize + cs - 1) / cs)
	out := make([]ChunkRange, 0, count)
	for i := 0; i < count; i++ {
		start := int64(i) * cs
		end := start + cs
		if end > totalSize {
			end = totalSize
		}
		out = append(out, ChunkRange{Index: i, Start: start, End: end})
	}
	return out
}

func normalizeChunkSize(n int) int {
	if n <= 0 {
		return DefaultChunkSize
	}
	if n < MinChunkSize {
		return MinChunkSize
	}
	if n > MaxChunkSize {
		return MaxChunkSize
	}
	return n
}

func normalizeConcurrency(n int) int {
	if n <= 0 {
		return DefaultConcurrency
	}
	if n < MinConcurrency {
		return MinConcurrency
	}
	if n > MaxConcurrency {
		return MaxConcurrency
	}
	return n
}

// Validate 校验 UploadRequest 的字段合法性（不涉及文件 IO）。
//
// 返回的 error 直接是 *ErrXxx，调用方可以做 errors.Is 判别。
func (r UploadRequest) Validate() error {
	if r.TransferID == "" {
		return ErrMissingTransferID
	}
	if r.LocalPath == "" || r.RemotePath == "" {
		return ErrMissingPaths
	}
	if r.ChunkSize != 0 {
		if r.ChunkSize < MinChunkSize || r.ChunkSize > MaxChunkSize {
			return fmt.Errorf("%w: got %d, want [%d, %d]",
				ErrInvalidChunkSize, r.ChunkSize, MinChunkSize, MaxChunkSize)
		}
	}
	if r.Concurrency != 0 {
		if r.Concurrency < MinConcurrency || r.Concurrency > MaxConcurrency {
			return fmt.Errorf("%w: got %d, want [%d, %d]",
				ErrInvalidConcurrency, r.Concurrency, MinConcurrency, MaxConcurrency)
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// 核心 Upload 函数
// -----------------------------------------------------------------------------

// chunkDone 是 worker → 主 goroutine 的"chunk 完成"事件。
//
// manifestCh 复用同一 channel（主 goroutine 处理 manifest + checksum + 进度）。
// 用 *chunkDone 而非 *Manifest 是为了**避免** worker 写共享 manifest 的 race。
// 主 goroutine 自己累加 UploadedChunks + 写盘，保证单写者。
type chunkDone struct {
	index   int
	written int64
}

// uploadState 持有 Upload 期间的运行时状态（atomic 字段供并发 worker 读写）。
type uploadState struct {
	bytesSent  atomic.Int64
	startTime  time.Time
	lastEmit   time.Time
	lastBytes  int64
	// manifestCh 是 worker 完成一片时往里写 *chunkDone 的通道；
	// 主 goroutine 串行累加 manifest.UploadedChunks + 写盘，保证单写者。
	manifestCh chan *chunkDone
	errCh      chan error
}

func newUploadState() *uploadState {
	now := time.Now()
	return &uploadState{
		startTime:  now,
		lastEmit:   now,
		manifestCh: make(chan *chunkDone, 64),
		errCh:      make(chan error, 1),
	}
}

// Upload 把 LocalPath 流式上传到 RemotePath（通过 Uploader）。
//
// 行为概要：
//  1. 校验 Request（Validate）
//  2. 打开本地文件 + Stat（拿 size + mtime）
//  3. 校验大小（> MaxFileSize 拒绝）
//  4. 若 Resume=true：尝试 LoadManifest，校验 local mtime/size 一致
//  5. 计算 Plan，切掉已上传 chunk
//  6. 打开远端文件（O_WRONLY|O_CREATE）→ Truncate(totalSize) → Close
//     （预分配 + 让磁盘满错误早暴露）
//  7. 重新打开远端文件（WriteAt 需要活跃句柄）
//  8. 启动 N 个 worker goroutine，按 chunk 索引分配任务
//  9. 主 goroutine：收集 manifest 更新（每片 flush）+ 限速 emit Progress
//  10. ctx 取消：worker 检查 ctx 立即退出；主 goroutine 收到错误后
//      保留 manifest（已传 chunk 记下来）返回 ErrUploadFailed
//
// progress 回调可能在任意 goroutine 调用；调用方负责线程安全。
// progress 为 nil 时不回调（测试 / 后台跑用）。
//
// Resume 行为：传 false 时忽略已有 manifest，从头传。
// Resume 行为：传 true 但 manifest 不存在 → 当作首次上传（不报错）。
// Resume 行为：传 true + manifest 存在 + local 不匹配 → ErrLocalChanged。
func Upload(ctx context.Context, uploader Uploader, req UploadRequest, manifestDir string, progress func(Progress)) error {
	// 1. Validate
	if err := req.Validate(); err != nil {
		return err
	}

	// 2. 打开本地文件
	lf, err := os.Open(req.LocalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrLocalNotFound, req.LocalPath)
		}
		return fmt.Errorf("transfer.Upload: open local: %w", err)
	}
	defer lf.Close()

	fi, err := lf.Stat()
	if err != nil {
		return fmt.Errorf("transfer.Upload: stat local: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is %s", ErrLocalNotFound, req.LocalPath, fi.Mode())
	}
	totalSize := fi.Size()
	localModTime := fi.ModTime()

	// 3. 大小保护
	if totalSize > MaxFileSize {
		return fmt.Errorf("%w: size=%d, max=%d", ErrFileTooLarge, totalSize, MaxFileSize)
	}

	// 4. Resume 校验
	chunkSize := normalizeChunkSize(req.ChunkSize)
	concurrency := normalizeConcurrency(req.Concurrency)

	manifest, err := LoadManifest(manifestDir, req.TransferID)
	if err != nil && !errors.Is(err, ErrManifestNotFound) {
		return fmt.Errorf("transfer.Upload: load manifest: %w", err)
	}
	if manifest != nil && req.Resume {
		if manifest.LocalPath != req.LocalPath || manifest.RemotePath != req.RemotePath {
			return fmt.Errorf("%w: manifest local=%q remote=%q, request local=%q remote=%q",
				ErrLocalChanged, manifest.LocalPath, manifest.RemotePath, req.LocalPath, req.RemotePath)
		}
		if manifest.TotalSize != totalSize {
			return fmt.Errorf("%w: manifest size=%d, current=%d",
				ErrLocalChanged, manifest.TotalSize, totalSize)
		}
		if !manifest.LocalModTime.Equal(localModTime) {
			return fmt.Errorf("%w: manifest mtime=%s, current=%s",
				ErrLocalChanged, manifest.LocalModTime, localModTime)
		}
	} else if manifest != nil && !req.Resume {
		// 显式 Resume=false：删除旧 manifest 重新开始
		_ = DeleteManifest(manifestDir, req.TransferID)
		manifest = nil
	}

	if manifest == nil {
		manifest = &Manifest{
			TransferID:   req.TransferID,
			LocalPath:    req.LocalPath,
			RemotePath:   req.RemotePath,
			ChunkSize:    chunkSize,
			TotalSize:    totalSize,
			LocalModTime: localModTime,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
	}

	// 5. 计算 Plan
	plan := Plan(totalSize, chunkSize)
	uploadedSet := make(map[int]bool, len(manifest.UploadedChunks))
	for _, idx := range manifest.UploadedChunks {
		uploadedSet[idx] = true
	}
	pending := make([]ChunkRange, 0, len(plan))
	for _, c := range plan {
		if !uploadedSet[c.Index] {
			pending = append(pending, c)
		}
	}

	// 全部已传完的兜底
	if len(pending) == 0 {
		// Emit done（让前端拿最终 100% 进度）
		if progress != nil {
			progress(Progress{
				TransferID:  req.TransferID,
				BytesSent:   totalSize,
				TotalBytes:  totalSize,
				SpeedBps:    0,
				EtaSec:      0,
				ChunkIndex:  len(plan),
				TotalChunks: len(plan),
			})
		}
		// 删除 manifest（v0.5.10 行为：完成后清理）
		_ = DeleteManifest(manifestDir, req.TransferID)
		return nil
	}

	// 6. 打开远端文件 + Truncate
	// 先 mkdir parent（多数 SFTP server 不自动建父目录）
	if parentDir := parentDirOf(req.RemotePath); parentDir != "" && parentDir != "/" && parentDir != "." {
		if mkErr := uploader.MkdirAll(parentDir); mkErr != nil {
			// best-effort：某些 SFTP server 已存在目录会返回 error，忽略
			if !errors.Is(mkErr, os.ErrExist) {
				return fmt.Errorf("transfer.Upload: mkdir parent %q: %w", parentDir, mkErr)
			}
		}
	}
	remoteFile, err := uploader.Open(req.RemotePath, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return fmt.Errorf("transfer.Upload: open remote: %w", err)
	}
	if err := uploader.Truncate(req.RemotePath, totalSize); err != nil {
		_ = remoteFile.Close()
		return fmt.Errorf("transfer.Upload: truncate remote to %d: %w", totalSize, err)
	}
	// Truncate 之后必须重新 Open，WriteAt 需要 seek 到 0 起始
	_ = remoteFile.Close()

	// 7. 重新打开（WriteAt 用）
	rf, err := uploader.Open(req.RemotePath, os.O_WRONLY)
	if err != nil {
		return fmt.Errorf("transfer.Upload: reopen remote for WriteAt: %w", err)
	}
	defer rf.Close()

	// 8. 启动 worker pool
	state := newUploadState()
	// 初始已传字节数（Resume 时非 0）
	initialSent := int64(0)
	for _, idx := range manifest.UploadedChunks {
		initialSent += plan[idx].End - plan[idx].Start
	}
	state.bytesSent.Store(initialSent)

	pendingCh := make(chan ChunkRange, len(pending))
	for _, c := range pending {
		pendingCh <- c
	}
	close(pendingCh)

	// SHA-256 校验：每 chunk 单独 hash（独立 sha256.New()），done 时
	// 合并所有 chunk hash 成最终 hash。**不**共享单个 hasher（race 风险）。
	//
	// chunkHashes 按 chunk.Index 索引，done 时按 Index 升序拼接 + sha256。
	chunkHashes := make([][]byte, len(plan))

	var wg sync.WaitGroup
	workerErrCh := make(chan error, concurrency)

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for chunk := range pendingCh {
				// ctx 取消检查
				if err := ctx.Err(); err != nil {
					workerErrCh <- fmt.Errorf("%w: %v", ErrUploadFailed, err)
					return
				}
				// 读本地 [Start, End)
				// SectionReader + Read 一次性读到 buf，不缓冲整文件。
				section := io.NewSectionReader(lf, chunk.Start, chunk.End-chunk.Start)
				buf := make([]byte, chunk.End-chunk.Start)
				readN, readErr := io.ReadFull(section, buf)
				if readErr != nil && readErr != io.ErrUnexpectedEOF {
					workerErrCh <- fmt.Errorf("%w: read chunk %d [%d,%d): %v",
						ErrUploadFailed, chunk.Index, chunk.Start, chunk.End, readErr)
					return
				}
				buf = buf[:readN]

				// 写远端 WriteAt
				written, werr := rf.WriteAt(buf, chunk.Start)
				if werr != nil {
					workerErrCh <- fmt.Errorf("%w: write chunk %d [%d,%d): %v",
						ErrUploadFailed, chunk.Index, chunk.Start, chunk.End, werr)
					return
				}
				if written != readN {
					workerErrCh <- fmt.Errorf("%w: short write chunk %d: want %d, got %d",
						ErrUploadFailed, chunk.Index, readN, written)
					return
				}

				// 累加字节
				state.bytesSent.Add(int64(written))
				// 单 chunk hash（per-worker 独立 hasher）
				h := sha256.New()
				h.Write(buf)
				chunkHashes[chunk.Index] = h.Sum(nil)

				// 通知主 goroutine：chunk 完成
				select {
				case state.manifestCh <- &chunkDone{index: chunk.Index, written: int64(written)}:
				case <-ctx.Done():
					workerErrCh <- fmt.Errorf("%w: %v", ErrUploadFailed, ctx.Err())
					return
				}
			}
		}(w)
	}

	// 9. 主 goroutine：累加 manifest + 刷盘 + 限速 emit progress
	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	// 持有 manifest 状态（主 goroutine 单写者）
	uploadedIdx := make([]int, len(manifest.UploadedChunks))
	copy(uploadedIdx, manifest.UploadedChunks)
	manifestMu := &sync.Mutex{} // 当前实现下主 goroutine 唯一访问，但保留以备未来扩展

	// ticker 驱动 progress + 处理 manifestCh
	var lastErr error
emitLoop:
	for {
		select {
		case cd, ok := <-state.manifestCh:
			if !ok {
				state.manifestCh = nil
				continue
			}
			// 主 goroutine 累加 UploadedChunks（单写者）
			manifestMu.Lock()
			uploadedIdx = append(uploadedIdx, cd.index)
			sort.Ints(uploadedIdx)
			updatedManifest := *manifest
			updatedManifest.UploadedChunks = append([]int(nil), uploadedIdx...)
			updatedManifest.UpdatedAt = time.Now()
			manifest = &updatedManifest
			// 写盘
			err := SaveManifest(manifestDir, &updatedManifest)
			manifestMu.Unlock()
			if err != nil {
				lastErr = fmt.Errorf("transfer.Upload: save manifest: %w", err)
				break emitLoop
			}
		case <-time.After(ProgressEmitInterval):
			// emit progress（限速）
			emitProgress(progress, req.TransferID, totalSize, state, len(plan))
		case <-ctx.Done():
			lastErr = fmt.Errorf("%w: %v", ErrUploadFailed, ctx.Err())
			break emitLoop
		case e := <-workerErrCh:
			lastErr = e
			break emitLoop
		case <-doneCh:
			// drain manifestCh（non-blocking：worker 没关 ch，可能还有 buffer 数据）
			for drained := false; !drained; {
				select {
				case cd := <-state.manifestCh:
					manifestMu.Lock()
					uploadedIdx = append(uploadedIdx, cd.index)
					manifestMu.Unlock()
				default:
					drained = true
				}
			}
			// 计算最终 checksum：把所有 chunk hash 按 Index 升序拼起来再 sha256
			manifestMu.Lock()
			sort.Ints(uploadedIdx)
			manifest.UploadedChunks = append([]int(nil), uploadedIdx...)
			manifest.UpdatedAt = time.Now()
			hasher := sha256.New()
			for _, idx := range uploadedIdx {
				if h := chunkHashes[idx]; h != nil {
					hasher.Write(h)
				}
			}
			manifest.Checksum = "sha256:" + hex.EncodeToString(hasher.Sum(nil))
			// 写最终 manifest（带 checksum）
			err := SaveManifest(manifestDir, manifest)
			manifestMu.Unlock()
			if err != nil {
				lastErr = fmt.Errorf("transfer.Upload: save final manifest: %w", err)
			}
			// emit final progress
			emitProgress(progress, req.TransferID, totalSize, state, len(plan))
			break emitLoop
		}
	}

	// 等待所有 worker 退出（避免 goroutine 泄漏）
	// 已经 break emitLoop；让 worker 自然退出
	go func() {
		<-doneCh
	}()

	if lastErr != nil {
		// 失败：保留 manifest（让 Resume 续传）
		return lastErr
	}

	// 成功：删除 manifest
	_ = DeleteManifest(manifestDir, req.TransferID)
	return nil
}

// parentDirOf 提取 path 的父目录。
//
// "/a/b/c" → "/a/b"；"/a" → "/"；"/" → ""。
// 简单字符串处理，不走 path 包（避免 Windows 路径分隔符的麻烦）。
func parentDirOf(path string) string {
	if path == "" {
		return ""
	}
	// 从右往左找最后一个 '/'
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return ""
}

// emitProgress 按 ProgressEmitInterval 节流地触发回调。
//
// 末次（bytesSent == totalSize）总是 emit 一次（确保 100% 进度送达）。
func emitProgress(progress func(Progress), transferID string, total int64, state *uploadState, totalChunks int) {
	if progress == nil {
		return
	}
	now := time.Now()
	sent := state.bytesSent.Load()
	if sent >= total && state.lastBytes == sent {
		return // 已经 emit 过 100%
	}
	elapsed := now.Sub(state.startTime).Seconds()
	var speed int64
	if elapsed > 0 {
		speed = int64(float64(sent) / elapsed)
	}
	var eta int64 = -1
	if speed > 0 && sent < total {
		eta = int64(float64(total-sent) / float64(speed))
	}
	progress(Progress{
		TransferID:  transferID,
		BytesSent:   sent,
		TotalBytes:  total,
		SpeedBps:    speed,
		EtaSec:      eta,
		ChunkIndex:  -1, // -1 = 不属于某片；wailsbinding 用 bytesSent / totalBytes 算百分比
		TotalChunks: totalChunks,
	})
	state.lastEmit = now
	state.lastBytes = sent
}
