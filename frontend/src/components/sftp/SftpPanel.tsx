/**
 * SftpPanel —— 右侧 SFTP 面板
 * --------------------------------------------------------------------
 * v0.1 占位：显示当前 session 的远端工作目录 + 文件列表 + 传输任务进度条。
 * v0.2 启用：真正的 list / upload / download。
 * v0.6.1 启用：顶部 Download 按钮 = 批量下载当前目录所有文件；双击文件
 *   = 单文件下载（FileList 内部调 onDownload）。两者共用 startDownload 闭包。
 */
import { useCallback, useEffect } from "react";
import { FolderUp, Upload, Download, RefreshCw, X } from "lucide-react";
import { FileList } from "./FileList";
import { useSftpStore } from "./sftpStore";
import { useConnectionStore } from "@stores/connectionStore";
import { useUIStore } from "@stores/uiStore";
import { useTransferStore } from "@stores/transferStore";
import { Button } from "@components/common/Button";
import { formatBytes, formatRate, formatPercent } from "@utils/format";
import { App } from "@/../wailsjs/go/main/App";
import { logger } from "@utils/logger";
import type { SessionID } from "@/types/session";

export function SftpPanel(): JSX.Element {
  const activeSid   = useConnectionStore((s) => s.activeSessionId);
  const currentPath = useSftpStore((s) =>
    activeSid ? s.currentPath[activeSid] : undefined,
  );
  const entriesMap  = useSftpStore((s) =>
    activeSid && currentPath ? s.entriesByPath[activeSid]?.[currentPath] ?? [] : [],
  );
  const listDir     = useSftpStore((s) => s.listDir);
  const cd          = useSftpStore((s) => s.cd);
  const jobsRecord  = useTransferStore((s) => s.jobs);
  const jobs        = Object.values(jobsRecord);
  const pushToast   = useUIStore((s) => s.pushToast);
  const togglePanel = useUIStore((s) => s.toggleSftpPanel);

  useEffect(() => {
    if (activeSid && !currentPath) {
      void listDir(activeSid, "/", true);
    }
  }, [activeSid, currentPath, listDir]);

  // v0.6.1：双击文件 / 顶部 Download 按钮 共用的下载入口。
  //
  // 调 App.StartDownload（v0.6 streaming download wailsbinding）→ 后端
  // 启动后台 goroutine 跑 transfer.Download；前端立刻 upsertJob 占位，
  // 后续 transfer:progress 事件由 useTransferEvents 接管。
  const startDownload = useCallback(
    async (sessionID: SessionID, localPath: string, remotePath: string) => {
      try {
        const transferID = await App.StartDownload({
          transferID: "",
          sessionID,
          localPath,
          remotePath,
          chunkSize: 0,
          concurrency: 0,
          resume: false,
        });
        useTransferStore.getState().upsertJob({
          transferID,
          direction: "download",
          localPath,
          remotePath,
          totalBytes: 0,
          bytesSent: 0,
          state: "running",
          chunkSize: 0,
          concurrency: 0,
          startedAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        });
        pushToast({
          level: "info",
          message: `下载开始: ${remotePath}`,
          durationMs: 2000,
        });
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        logger.error(`[SftpPanel] StartDownload ${remotePath} failed: ${msg}`);
        pushToast({
          level: "error",
          message: `下载失败: ${msg}`,
          durationMs: 4000,
        });
      }
    },
    [pushToast],
  );

  // 顶部 Download 按钮：批量下载当前目录所有文件（跳过目录）
  const downloadAll = useCallback(() => {
    if (!activeSid || !currentPath) return;
    const files = entriesMap.filter((e) => e.type !== "dir");
    if (files.length === 0) {
      pushToast({ level: "info", message: "当前目录没有文件", durationMs: 2000 });
      return;
    }
    for (const f of files) {
      const localPath = `~/Downloads/${f.name}`;
      void startDownload(activeSid, localPath, f.path);
    }
  }, [activeSid, currentPath, entriesMap, startDownload, pushToast]);

  return (
    <div className="flex h-full flex-col bg-moss-surface">
      {/* 顶部：当前路径 + 操作 */}
      <div className="flex items-center gap-1 border-b border-moss-border p-2 text-xs">
        <Button
          size="sm"
          icon={<FolderUp size={12} />}
          onClick={() => {
            if (!activeSid || !currentPath) return;
            const parent = currentPath.split("/").slice(0, -1).join("/") || "/";
            cd(activeSid, parent);
            void listDir(activeSid, parent, true);
          }}
          title="上一级"
        />
        <code className="flex-1 truncate rounded bg-moss-bg px-2 py-1 font-mono text-[11px] text-ink-muted">
          {currentPath ?? "/"}
        </code>
        <Button
          size="sm"
          icon={<Download size={12} />}
          onClick={downloadAll}
          title="下载当前目录所有文件到 ~/Downloads"
          disabled={!activeSid || !currentPath}
        />
        <Button
          size="sm"
          icon={<RefreshCw size={12} />}
          onClick={() => activeSid && currentPath && listDir(activeSid, currentPath, true)}
          title="刷新"
        />
        <Button
          size="sm"
          icon={<X size={12} />}
          onClick={togglePanel}
          title="关闭面板"
        />
      </div>

      {/* 文件列表 */}
      <div className="flex-1 min-h-0 overflow-hidden">
        {activeSid && currentPath ? (
          <FileList sessionId={activeSid} path={currentPath} onDownload={startDownload} />
        ) : (
          <div className="flex h-full items-center justify-center text-[11px] text-ink-muted">
            先打开一个会话
          </div>
        )}
      </div>

      {/* 底部：传输任务 */}
      {jobs.length > 0 && (
        <div className="border-t border-moss-border">
          <div className="px-2 py-1 text-[10px] uppercase tracking-wider text-ink-subtle">
            传输任务 · {jobs.length}
          </div>
          <ul className="max-h-32 overflow-y-auto text-[11px]">
            {jobs.map((j) => (
              <li key={j.transferID} className="flex items-center gap-2 px-2 py-1">
                {j.direction === "upload" ? <Upload size={10} /> : <Download size={10} />}
                <span className="flex-1 truncate">{j.remotePath}</span>
                <span className="text-ink-muted">
                  {formatPercent(j.bytesSent, j.totalBytes)} · {formatRate(j.speedBps ?? 0)}
                </span>
                <span className="font-mono text-ink-muted">{formatBytes(j.bytesSent)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
