/**
 * SftpPaneView —— 嵌入 Pane 树的 SFTP 浏览器 leaf（v0.5.8 引入）
 * --------------------------------------------------------------------
 * 设计要点：
 *   - 复用 SftpBrowserContent（v0.5.8 抽出的 list + toolbar + dialog + viewer）
 *   - 顶部 mini header 显示 session 标识（SFTP · user@host）+ 状态信息
 *   - leaf 容器负责：focus 高亮（isActive ring）/ EmptyLeafHint 兜底
 *     （sessionId 还没绑时）
 *   - **不**做 modal 关闭按钮（pane 关闭走 SplitPane 工具栏 / TabBar）
 *   - Esc 行为：关闭内部的 viewer/dialog（与 SftpBrowserContent 一致）；
 *     不关 pane（pane 关闭走 SplitPane / Tab close）
 *
 * 与 SftpBrowser 的关系：
 *   - SftpBrowser = 独立 modal 包装（v0.5.1 兼容路径）
 *   - SftpPaneView = pane 包装（v0.5.8 主路径）
 *   - 两者共享 SftpBrowserContent（业务逻辑）
 *
 * 与 PaneView 的关系：
 *   - PaneView.pane.kind === 'sftp' → 渲染 <SftpPaneView />
 *   - PaneView.pane.kind === 'terminal' → 渲染 <Terminal />
 *   - PaneView.pane.kind === 'split' → 递归 children
 */
import { useEffect, useRef } from "react";
import { FolderOpen, HardDrive } from "lucide-react";
import clsx from "clsx";
import { SftpBrowserContent } from "./SftpBrowserContent";
import { useConnectionStore } from "@stores/connectionStore";
import { useUIStore } from "@stores/uiStore";
import type { SessionID } from "@/types/session";

export interface SftpPaneViewProps {
  /** leaf pane id（用于 setActivePane 上抛） */
  paneId: string;
  sessionId: SessionID | null;
  isActive: boolean;
  onActivate: (paneId: string) => void;
}

export function SftpPaneView({
  paneId,
  sessionId,
  isActive,
  onActivate,
}: SftpPaneViewProps): JSX.Element {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const openPalette = useUIStore((s) => s.openCommandPalette);
  const session = useConnectionStore((s) =>
    sessionId ? s.sessions[sessionId] : undefined,
  );

  // 切到当前 leaf 时把焦点给列表区（不是 xterm 的 textarea，content
  // 自身没有 hidden textarea 概念，触发 click 即可让 dialog/viewer
  // 的 Esc 等键盘事件被本节点接收）
  useEffect(() => {
    if (!isActive) return;
    const root = containerRef.current;
    if (!root) return;
    const id = requestAnimationFrame(() => {
      // 让 content 内的工具栏按钮可获焦 —— 直接 focus content 容器
      root.focus();
    });
    return () => cancelAnimationFrame(id);
  }, [isActive, sessionId]);

  // sessionId 缺失 → EmptyLeafHint 兜底（与 Terminal 一样）
  if (sessionId === null) {
    return (
      <div
        ref={containerRef}
        onMouseDown={() => onActivate(paneId)}
        className={clsx(
          "group relative h-full w-full min-h-0 min-w-0",
          isActive ? "ring-1 ring-accent" : "ring-1 ring-transparent",
        )}
        data-pane-id={paneId}
        data-active={isActive ? "true" : "false"}
      >
        <EmptySftpHint onOpenPalette={openPalette} />
      </div>
    );
  }

  const hostLabel = session
    ? `${session.user}@${session.host}:${session.port}`
    : sessionId;

  return (
    <div
      ref={containerRef}
      tabIndex={-1}
      onMouseDown={() => onActivate(paneId)}
      className={clsx(
        "group relative flex h-full w-full min-h-0 min-w-0 flex-col overflow-hidden bg-moss-surface outline-none",
        isActive ? "ring-1 ring-accent" : "ring-1 ring-transparent",
      )}
      data-pane-id={paneId}
      data-pane-kind="sftp"
      data-active={isActive ? "true" : "false"}
    >
      {/* mini header：会话标识 + 当前无 session 的提示 */}
      <div className="flex shrink-0 items-center gap-2 border-b border-moss-border bg-moss-bg px-3 py-1.5 text-[11px]">
        <FolderOpen size={12} className="text-accent" aria-hidden />
        <span className="font-semibold text-ink">SFTP</span>
        <span className="text-ink-muted">·</span>
        <code className="font-mono text-ink-muted">{hostLabel}</code>
        {session && session.state !== "established" && (
          <span className="ml-auto text-state-warn">
            等待 session 就绪（{session.state}）
          </span>
        )}
      </div>

      {/* 核心 list + toolbar + dialog + viewer */}
      <SftpBrowserContent sessionID={sessionId} className="flex-1 min-h-0" />
    </div>
  );
}

function EmptySftpHint({ onOpenPalette }: { onOpenPalette: () => void }): JSX.Element {
  return (
    <div className="flex h-full w-full items-center justify-center bg-moss-bg">
      <button
        onClick={onOpenPalette}
        className="flex max-w-xs flex-col items-center gap-3 rounded border border-moss-border bg-moss-surface px-6 py-5 text-center text-ink-muted transition-colors hover:border-accent hover:text-ink"
        title="Open command palette (Ctrl+Shift+P)"
      >
        <HardDrive size={24} className="text-accent" />
        <div className="text-sm">SFTP pane 已就位</div>
        <div className="text-[11px] text-ink-subtle">
          等 session 连上后会自动激活；当前可先去命令面板连 SSH
        </div>
      </button>
    </div>
  );
}
