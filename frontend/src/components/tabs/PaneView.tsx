/**
 * PaneView —— 递归渲染 Pane 树
 * --------------------------------------------------------------------
 * - Pane.split === null → leaf，挂载 <Terminal />（sessionId 必填）
 *   若 sessionId 为 null（空 tab / 刚 split）→ 渲染 EmptyPane 引导用户连会话
 * - Pane.split !== null → split 节点，递归 children 用 <SplitPane>
 *
 * 交互：
 *   - 点击 leaf → setActivePane（store）+ focus 该 pane 的 xterm
 *   - split 工具栏点击 → onSplit / onClose 上抛到 MainLayout 处理
 *
 * xterm.js focus 技巧：
 *   xterm 内部挂了一个隐藏的 `<textarea class="xterm-helper-textarea">`
 *   用来接收键盘事件。聚焦它即等同于 term.focus()，无需 ref 穿透 Terminal。
 */
import { useEffect, useRef } from "react";
import { Terminal as TerminalIcon, Plus } from "lucide-react";
import clsx from "clsx";
import { Terminal } from "../terminal/Terminal";
import { SplitPane } from "./SplitPane";
import type { Pane, PaneSplitDirection } from "./tabsStore";
import { useUIStore } from "@stores/uiStore";

export interface PaneViewProps {
  pane: Pane;
  isActive: boolean;
  onActivate: (paneId: string) => void;
  onSplit: (paneId: string, direction: PaneSplitDirection) => void;
  onClose: (paneId: string) => void;
}

export function PaneView({
  pane,
  isActive,
  onActivate,
  onSplit,
  onClose,
}: PaneViewProps): JSX.Element {
  // ============ split node：递归 ============
  if (pane.split !== null) {
    return (
      <SplitPane
        direction={pane.split}
        onSplit={(dir) => onSplit(pane.id, dir)}
        onClose={() => onClose(pane.id)}
      >
        {pane.children.map((child) => (
          <PaneView
            key={child.id}
            pane={child}
            isActive={false /* 视觉上 leaf 自己负责高亮 */}
            onActivate={onActivate}
            onSplit={onSplit}
            onClose={onClose}
          />
        ))}
      </SplitPane>
    );
  }

  // ============ leaf：Terminal 或占位 ============
  return (
    <LeafPane
      pane={pane}
      isActive={isActive}
      onActivate={onActivate}
    />
  );
}

// =====================================================================
// LeafPane —— leaf 节点的容器（点击激活 + 视觉高亮）
// =====================================================================
interface LeafPaneProps {
  pane: Pane;
  isActive: boolean;
  onActivate: (paneId: string) => void;
}

function LeafPane({ pane, isActive, onActivate }: LeafPaneProps): JSX.Element {
  const containerRef = useRef<HTMLDivElement | null>(null);

  // 切到当前 leaf 时，把焦点给 xterm 的 hidden textarea
  useEffect(() => {
    if (!isActive) return;
    const root = containerRef.current;
    if (!root) return;
    // 等一帧，确保 Terminal 内的 xterm DOM 已就绪
    const id = requestAnimationFrame(() => {
      const ta = root.querySelector<HTMLTextAreaElement>(
        ".xterm-helper-textarea",
      );
      ta?.focus();
    });
    return () => cancelAnimationFrame(id);
  }, [isActive, pane.sessionId]);

  return (
    <div
      ref={containerRef}
      onMouseDown={() => onActivate(pane.id)}
      className={clsx(
        "group relative h-full w-full min-h-0 min-w-0",
        isActive ? "ring-1 ring-accent" : "ring-1 ring-transparent",
      )}
      data-pane-id={pane.id}
      data-active={isActive ? "true" : "false"}
    >
      {pane.sessionId !== null ? (
        <Terminal sessionId={pane.sessionId} />
      ) : (
        <EmptyLeafHint />
      )}
    </div>
  );
}

// =====================================================================
// EmptyLeafHint —— 空 leaf 的引导（sessionId=null）
// =====================================================================
function EmptyLeafHint(): JSX.Element {
  const openPalette = useUIStore((s) => s.openCommandPalette);
  return (
    <div className="flex h-full w-full items-center justify-center bg-moss-bg">
      <button
        onClick={openPalette}
        className="flex max-w-xs flex-col items-center gap-3 rounded border border-moss-border bg-moss-surface px-6 py-5 text-center text-ink-muted transition-colors hover:border-accent hover:text-ink"
        title="Open command palette (Ctrl+Shift+P)"
      >
        <TerminalIcon size={24} className="text-accent" />
        <div className="text-sm">No active session</div>
        <div className="flex items-center gap-1 text-[11px] text-ink-subtle">
          <Plus size={10} />
          Click to connect
        </div>
      </button>
    </div>
  );
}
