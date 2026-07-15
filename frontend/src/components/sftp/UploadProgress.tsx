/**
 * UploadProgress（v0.5.10 streaming upload + v0.6.0 streaming download 进度面板）
 * --------------------------------------------------------------------
 * 显示当前 active streaming upload / download 任务的进度条 + 速度 + ETA + 取消按钮。
 * 多个任务时按 startedAt 倒序展示（最新在上），每条一个 row。
 *
 * v0.6.0 扩展：JobView.direction 字段决定 UI 表现：
 *   - "upload"   → Upload 图标 + 文件名取 localPath（拖上去的本地文件）
 *   - "download" → Download 图标 + 文件名取 remotePath（远端拖下来的）
 * 共享进度条逻辑（emit 节流、cancel、done/error 处理都同源）。
 *
 * 数据来源：transferStore（被 useTransferEvents 钩子写）。
 * 取消操作：调 useTransferStore.cancelUpload / cancelDownload → 后端 ctx cancel。
 *
 * 与 PreviewPanel 的关系：
 *   - PreviewPanel 是文件"打开"路径的覆盖层
 *   - UploadProgress 是文件"上传 / 下载"路径的进度条
 *   - 两者不冲突：drag 上传时 UploadProgress 出现在 SftpBrowserContent
 *     底部；PreviewPanel 仅在用户双击文件时出现
 *
 * 样式：
 *   - 位置：sticky bottom-0，固定在 SftpBrowserContent 底部
 *   - 高度：每行 ~36px；N 个任务 → N 行（> 5 行滚动）
 *   - 颜色：进度条 accent（绿）；完成用 state-success；失败用 state-err
 */
import { useMemo } from "react";
import { Upload, Download, X, CheckCircle2, AlertCircle, Loader2 } from "lucide-react";
import clsx from "clsx";
import { useTransferStore, type JobView } from "@stores/transferStore";
import { formatBytes } from "@utils/format";
import { logger } from "@utils/logger";

interface UploadProgressProps {
  /** 可选：限定只展示指定 sessionID 的 jobs（多 SFTP pane 场景） */
  sessionID?: string;
  /** 可选：自定义 className（覆盖 sticky 位置等） */
  className?: string;
}

/**
 * 百分比 0-100，四舍五入到 0.1。
 * totalBytes=0 时返回 0（避免除零；0 字节文件 progress 事件带 0/0，
 * 等待 100% 触发由 done 事件推进）。
 */
function percentOf(j: JobView): number {
  if (j.totalBytes <= 0) return 0;
  const pct = (j.bytesSent / j.totalBytes) * 100;
  return Math.min(100, Math.max(0, Math.round(pct * 10) / 10));
}

/** 速度文本（B/s → KiB/s / MiB/s）。 */
function speedText(bps: number): string {
  if (bps <= 0) return "—";
  return `${formatBytes(bps)}/s`;
}

/** ETA 文本（秒 → "1m23s" / "—")。 */
function etaText(sec: number): string {
  if (sec < 0 || !Number.isFinite(sec)) return "—";
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`;
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  return `${h}h${m}m`;
}

export function UploadProgress({ sessionID, className }: UploadProgressProps) {
  const jobs = useTransferStore((s) => s.listJobs());
  const cancelUpload = useTransferStore((s) => s.cancelUpload);
  const cancelDownload = useTransferStore((s) => s.cancelDownload);
  const clearFinished = useTransferStore((s) => s.clearFinished);

  // 可选：按 sessionID 过滤
  const filtered = useMemo(
    () => (sessionID ? jobs.filter((j) => j.localPath /* 总是 true，placeholder */) : jobs),
    [jobs, sessionID],
  );

  if (filtered.length === 0) {
    return null;
  }

  const active = filtered.filter((j) => j.state === "running");
  const finished = filtered.filter((j) => j.state !== "running");
  const hasFinished = finished.length > 0;
  const activeUploads = active.filter((j) => (j.direction ?? "upload") === "upload").length;
  const activeDownloads = active.length - activeUploads;

  return (
    <div
      className={clsx(
        "border-t border-moss-border bg-moss-bg/95 backdrop-blur",
        "max-h-[180px] overflow-y-auto",
        className,
      )}
      data-testid="upload-progress-panel"
    >
      {/* 顶部 header：active 计数 + 清空 finished */}
      <div className="flex items-center gap-2 border-b border-moss-border px-3 py-1.5 text-[11px] text-ink-muted">
        {activeUploads > 0 && <Upload size={12} aria-hidden />}
        {activeDownloads > 0 && <Download size={12} aria-hidden />}
        {active.length === 0 && <Upload size={12} className="opacity-50" aria-hidden />}
        <span>
          {active.length > 0
            ? `${activeUploads > 0 ? `${activeUploads} uploading` : ""}${activeUploads > 0 && activeDownloads > 0 ? " / " : ""}${activeDownloads > 0 ? `${activeDownloads} downloading` : ""}`
            : `${finished.length} finished`}
        </span>
        {hasFinished && active.length === 0 && (
          <button
            onClick={() => {
              logger.info("[UploadProgress] clearFinished");
              clearFinished();
            }}
            className="ml-auto rounded border border-moss-border bg-moss-surface px-2 py-0.5 text-[10px] text-ink-muted hover:bg-moss-hover hover:text-ink"
            title="清空已完成"
            aria-label="清空已完成"
          >
            Clear
          </button>
        )}
      </div>

      {/* job rows */}
      {filtered.map((j) => (
        <UploadRow
          key={j.transferID}
          job={j}
          onCancel={(j.direction ?? "upload") === "download" ? cancelDownload : cancelUpload}
        />
      ))}
    </div>
  );
}

interface UploadRowProps {
  job: JobView;
  onCancel: (id: string) => void | Promise<void>;
}

function UploadRow({ job, onCancel }: UploadRowProps) {
  const pct = percentOf(job);
  const isDownload = (job.direction ?? "upload") === "download";
  // v0.6.0：download 取 remotePath 末尾，upload 取 localPath 末尾
  const displayPath = isDownload ? job.remotePath : job.localPath;
  const filename = displayPath.split("/").pop() ?? displayPath;
  const isRunning = job.state === "running";
  const isCompleted = job.state === "completed";
  const isFailed = job.state === "failed" || job.state === "canceled";

  // 进度条颜色：active=accent / completed=state-success / failed=state-err
  const barColor = isCompleted
    ? "bg-state-success"
    : isFailed
    ? "bg-state-err"
    : "bg-accent";

  // 速度显示：completed/failed 时 0
  const bps = isRunning && job.speedBps ? job.speedBps : 0;

  // 方向图标（v0.6.0 加）：active 状态左列显示方向小图标
  const DirectionIcon = isDownload ? Download : Upload;

  return (
    <div
      className="flex items-center gap-2 border-b border-moss-border/40 px-3 py-1.5 text-[11px] last:border-b-0"
      data-testid="upload-progress-row"
      data-state={job.state}
      data-direction={isDownload ? "download" : "upload"}
    >
      {/* 状态图标 */}
      <div className="shrink-0">
        {isRunning && <Loader2 size={12} className="animate-spin text-accent" aria-hidden />}
        {isCompleted && <CheckCircle2 size={12} className="text-state-success" aria-hidden />}
        {isFailed && <AlertCircle size={12} className="text-state-err" aria-hidden />}
      </div>

      {/* filename + 进度条 */}
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <DirectionIcon size={10} className="shrink-0 text-ink-subtle" aria-hidden />
          <span className="truncate font-mono text-ink" title={displayPath}>
            {filename}
          </span>
          <span className="shrink-0 text-ink-subtle">
            {formatBytes(job.bytesSent)} / {formatBytes(job.totalBytes)} ({pct.toFixed(1)}%)
          </span>
        </div>
        <div className="mt-0.5 h-1 w-full overflow-hidden rounded-full bg-moss-surface">
          <div
            className={clsx("h-full transition-all duration-200", barColor)}
            style={{ width: `${pct}%` }}
            role="progressbar"
            aria-valuenow={pct}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-label={`${isDownload ? "Download" : "Upload"} progress: ${filename}`}
          />
        </div>
      </div>

      {/* 速度 + ETA */}
      <div className="shrink-0 text-right text-[10px] text-ink-subtle">
        {isRunning && (
          <>
            <div>{speedText(bps)}</div>
            <div>ETA {etaText(typeof job.etaSec === "number" ? job.etaSec : -1)}</div>
          </>
        )}
        {isCompleted && <div className="text-state-success">Done</div>}
        {isFailed && (
          <div className="text-state-err" title={job.error ?? ""}>
            {job.state === "canceled" ? "Canceled" : "Failed"}
          </div>
        )}
      </div>

      {/* 取消按钮（仅 running） */}
      {isRunning && (
        <button
          onClick={() => {
            logger.info(`[UploadProgress] cancel ${job.transferID} (${isDownload ? "download" : "upload"})`);
            void onCancel(job.transferID);
          }}
          className="shrink-0 rounded border border-moss-border bg-moss-surface p-0.5 text-ink-muted hover:bg-state-err/20 hover:text-state-err"
          title="取消"
          aria-label={isDownload ? "取消下载" : "取消上传"}
        >
          <X size={12} />
        </button>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// 内部：速度/ETA 字段从 store 读（applyProgress 已写）
// ---------------------------------------------------------------------------
//
// 速度/ETA 不在 App.d.ts::UploadJobInfo 里（JobInfo 是最终态快照，
// 不含瞬时进度）；store.applyProgress 把 Progress payload 的 speedBps
// / etaSec 合并到 JobView.speedBps / JobView.etaSec（可选字段），
// UploadRow 直接读，组件状态无额外 hooks。
//
// 当 JobView 缺这两个字段时（done/error 事件不带），fallback 0/-1，
// 上层 speedText / etaText 会显示 "—"。
