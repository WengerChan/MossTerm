/**
 * Tab —— 单个标签
 * --------------------------------------------------------------------
 * 视觉：
 *   - 选中：底部 accent 高亮条 + 主背景
 *   - 未选中：surface 背景，hover 略亮
 *   - 左侧圆点反映 tab 状态（idle / connecting / established / failed / closed）
 *   - close 按钮：选中时常显，未选中时仅 hover 显示
 *   - 中键关闭
 *   - **v0.5.8**：title 后缀显示 pane 概要 —— "term ×2 / sftp ×1"，
 *     方便用户一眼看出 tab 内 pane 树组成
 */
import { FolderOpen, X } from "lucide-react";
import clsx from "clsx";
import type { Tab as TabType, TabState } from "./tabsStore";
import { summarizePanes } from "./TabBar";

export interface TabProps {
  tab: TabType;
  active: boolean;
  onClick: () => void;
  onClose: () => void;
}

/** 状态色映射（与 tailwind.config.js 的 state.* / accent 一致） */
const STATE_DOT: Record<TabState, string> = {
  idle:            "bg-ink-subtle",
  connecting:      "bg-state-warn",
  authenticating:  "bg-state-warn",
  established:     "bg-accent",
  closed:          "bg-ink-subtle",
  failed:          "bg-state-err",
};

const STATE_LABEL: Record<TabState, string> = {
  idle:            "Idle",
  connecting:      "Connecting…",
  authenticating:  "Authenticating…",
  established:     "Connected",
  closed:          "Closed",
  failed:          "Failed",
};

export function Tab({ tab, active, onClick, onClose }: TabProps): JSX.Element {
  const summary = summarizePanes(tab);

  return (
    <div
      onClick={onClick}
      onAuxClick={(e) => {
        // 中键关闭
        if (e.button === 1) onClose();
      }}
      title={`${tab.title} — ${STATE_LABEL[tab.state]}`}
      className={clsx(
        "group relative flex h-9 min-w-[140px] max-w-[240px] cursor-pointer items-center gap-2 border-r border-moss-border px-3 text-sm",
        active
          ? "border-b-2 border-b-accent bg-moss-bg text-ink -mb-px"
          : "border-b-2 border-b-transparent bg-moss-surface text-ink-muted hover:bg-moss-hover hover:text-ink",
      )}
    >
      <span
        className={clsx("h-2 w-2 shrink-0 rounded-full", STATE_DOT[tab.state])}
        aria-label={STATE_LABEL[tab.state]}
      />
      <span className="flex-1 truncate">{tab.title}</span>

      {/* v0.5.8：pane 概要后缀（多个 terminal / 多个 sftp 时显示数量） */}
      <PaneSummaryBadge term={summary.term} sftp={summary.sftp} />

      <button
        onClick={(e) => {
          e.stopPropagation();
          onClose();
        }}
        className={clsx(
          "rounded p-0.5 text-ink-muted hover:bg-moss-border hover:text-ink",
          active ? "opacity-70 hover:opacity-100" : "opacity-0 group-hover:opacity-70",
        )}
        title="Close"
        aria-label={`Close ${tab.title}`}
      >
        <X size={12} />
      </button>
    </div>
  );
}

// =====================================================================
// PaneSummaryBadge —— tab title 旁的小徽标
// =====================================================================
interface PaneSummaryBadgeProps {
  term: number;
  sftp: number;
}

/**
 * 当 tab 有多个 pane 或含 SFTP 时显示：
 *   - sftp 存在 → 显示 SFTP 图标（始终显示）
 *   - terminal 数量 > 1 → 显示 "×N" 后缀
 * 这样用户在 tab bar 上一眼能看到"这个 tab 有几个 terminal / 几个 sftp"。
 */
function PaneSummaryBadge({ term, sftp }: PaneSummaryBadgeProps): JSX.Element | null {
  if (sftp === 0 && term <= 1) return null;
  return (
    <span className="flex shrink-0 items-center gap-1 text-[10px] text-ink-muted">
      {sftp > 0 && (
        <span
          className="inline-flex items-center gap-0.5 rounded bg-accent/15 px-1 py-0.5 text-accent"
          title={`${sftp} 个 SFTP pane`}
        >
          <FolderOpen size={10} aria-hidden />
          {sftp > 1 && <span>×{sftp}</span>}
        </span>
      )}
      {term > 1 && (
        <span
          className="rounded bg-moss-border px-1 py-0.5 text-ink-muted"
          title={`${term} 个 terminal pane`}
        >
          term ×{term}
        </span>
      )}
    </span>
  );
}
