/**
 * SftpPanel —— 右侧 SFTP 面板
 * --------------------------------------------------------------------
 * v0.1 占位：显示当前 session 的远端工作目录 + 文件列表 + 传输任务进度条。
 * v0.2 启用：真正的 list / upload / download。
 */
import { useEffect } from "react";
import { FolderUp, Upload, Download, RefreshCw, X } from "lucide-react";
import { FileList } from "./FileList";
import { useSftpStore } from "./sftpStore";
import { useConnectionStore } from "@stores/connectionStore";
import { useUIStore } from "@stores/uiStore";
import { Button } from "@components/common/Button";
import { formatBytes, formatRate, formatPercent } from "@utils/format";

export function SftpPanel(): JSX.Element {
  const activeSid   = useConnectionStore((s) => s.activeSessionId);
  const currentPath = useSftpStore((s) =>
    activeSid ? s.currentPath[activeSid] : undefined,
  );
  const listDir     = useSftpStore((s) => s.listDir);
  const cd          = useSftpStore((s) => s.cd);
  const jobs        = useSftpStore((s) => s.jobs);
  const togglePanel = useUIStore((s) => s.toggleSftpPanel);

  useEffect(() => {
    if (activeSid && !currentPath) {
      void listDir(activeSid, "/", true);
    }
  }, [activeSid, currentPath, listDir]);

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
          <FileList sessionId={activeSid} path={currentPath} />
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
              <li key={j.id} className="flex items-center gap-2 px-2 py-1">
                {j.direction === "upload" ? <Upload size={10} /> : <Download size={10} />}
                <span className="flex-1 truncate">{j.remotePath}</span>
                <span className="text-ink-muted">
                  {formatPercent(j.transferred, j.size)} · {formatRate(j.speed)}
                </span>
                <span className="font-mono text-ink-muted">{formatBytes(j.transferred)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
