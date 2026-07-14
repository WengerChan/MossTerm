/**
 * SftpBrowser —— 独立 Modal 形式的 SFTP 浏览器（v0.5.1 保留为兼容）
 * --------------------------------------------------------------------
 * v0.5.8 改动：
 *   - 业务逻辑（list/cd/refresh/upload/mkdir/rename/remove/view）抽到
 *     `SftpBrowserContent`；本组件只剩 modal 包装（backdrop / title / X）
 *   - 仍受 `sftpBrowserStore` 全局控制；v0.5.8 主路径是 `addSftpPane`
 *     集成到 Pane 树，本组件保留作兼容与未来"预览/详细面板"扩展位
 *
 * 历史：v0.5.1 第一次实现完整 list/cd/refresh/dialog/viewer + v0.5.3
 * drag-drop 上传；v0.5.7 修过 stale request token；v0.5.8 拆 wrapper。
 */
import { useEffect } from "react";
import { FolderOpen, X } from "lucide-react";
import clsx from "clsx";
import { SftpBrowserContent } from "./SftpBrowserContent";
import { useConnectionStore } from "@stores/connectionStore";
import type { SessionID } from "@/types/session";

export interface SftpBrowserProps {
  open: boolean;
  onClose: () => void;
  sessionID: SessionID | null;
}

export function SftpBrowser({ open, onClose, sessionID }: SftpBrowserProps): JSX.Element | null {
  const session = useConnectionStore((s) =>
    sessionID ? s.sessions[sessionID] : undefined,
  );

  // Esc 关闭：与 v0.5.1 行为一致（dialog/viewer 在 content 内部处理）
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;
  if (!sessionID) return null;

  const hostLabel = session
    ? `${session.user}@${session.host}:${session.port}`
    : sessionID;

  return (
    <div
      role="dialog"
      aria-modal
      aria-labelledby="sftp-browser-title"
      className="fixed inset-0 z-40 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      onClick={onClose}
      data-testid="sftp-browser"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className={clsx(
          "relative flex h-[640px] max-h-[90vh] w-[920px] max-w-[95vw] flex-col overflow-hidden rounded-lg border bg-moss-surface shadow-2xl",
          "border-moss-border",
        )}
        data-testid="sftp-browser-panel"
      >
        {/* ===== Header ===== */}
        <div className="flex items-center justify-between border-b border-moss-border px-4 py-2.5">
          <div className="flex items-center gap-2 text-sm">
            <FolderOpen size={16} className="text-accent" aria-hidden />
            <h2 id="sftp-browser-title" className="font-semibold text-ink">
              SFTP 浏览器
            </h2>
            <span className="text-ink-muted">·</span>
            <code className="font-mono text-[11px] text-ink-muted">{hostLabel}</code>
          </div>
          <button
            onClick={onClose}
            className="rounded p-1 text-ink-muted hover:bg-moss-hover hover:text-ink"
            title="关闭（Esc）"
            aria-label="关闭"
          >
            <X size={14} />
          </button>
        </div>

        <SftpBrowserContent sessionID={sessionID} className="flex-1 min-h-0" />
      </div>
    </div>
  );
}
