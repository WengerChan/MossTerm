/**
 * Tab —— 单个标签
 * --------------------------------------------------------------------
 * 视觉：
 *   - 选中：底部 accent 高亮条 + 主背景
 *   - 未选中：surface 背景，hover 略亮
 *   - 左侧圆点反映 tab 状态（idle / connecting / established / failed / closed）
 *   - close 按钮：选中时常显，未选中时仅 hover 显示
 *   - 中键关闭
 */
import { X } from "lucide-react";
import clsx from "clsx";
import type { Tab as TabType, TabState } from "./tabsStore";

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
  return (
    <div
      onClick={onClick}
      onAuxClick={(e) => {
        // 中键关闭
        if (e.button === 1) onClose();
      }}
      title={`${tab.title} — ${STATE_LABEL[tab.state]}`}
      className={clsx(
        "group relative flex h-9 min-w-[120px] max-w-[200px] cursor-pointer items-center gap-2 border-r border-moss-border px-3 text-sm",
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
