/**
 * Tab —— 单个标签
 * --------------------------------------------------------------------
 * 视觉：
 *   - 选中：底部高亮条 + 略亮的背景
 *   - 未选中：透明背景，hover 略亮
 *   - close 按钮仅在 hover / 选中时显示
 */
import { X } from "lucide-react";
import clsx from "clsx";
import type { TabItem } from "./tabsStore";

export interface TabProps {
  tab: TabItem;
  active: boolean;
  onActivate: () => void;
  onClose: () => void;
}

export function Tab({ tab, active, onActivate, onClose }: TabProps): JSX.Element {
  return (
    <div
      onClick={onActivate}
      onAuxClick={(e) => {
        if (e.button === 1 && tab.closable) onClose();
      }}
      className={clsx(
        "group flex h-8 min-w-[120px] max-w-[200px] cursor-pointer items-center gap-1.5 border-r border-moss-border px-3 text-xs",
        active
          ? "border-b-2 border-b-accent bg-moss-bg text-ink"
          : "border-b-2 border-b-transparent bg-moss-surface text-ink-muted hover:bg-moss-hover hover:text-ink",
      )}
      title={tab.title}
    >
      <span className="truncate">{tab.title}</span>
      {tab.closable && (
        <button
          onClick={(e) => {
            e.stopPropagation();
            onClose();
          }}
          className={clsx(
            "rounded p-0.5 hover:bg-moss-border",
            active ? "opacity-70 hover:opacity-100" : "opacity-0 group-hover:opacity-70",
          )}
          title="Close"
        >
          <X size={12} />
        </button>
      )}
    </div>
  );
}
