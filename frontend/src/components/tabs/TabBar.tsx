/**
 * TabBar —— 多标签栏（v0.2+ 启用，v0.1 仅占位）
 * --------------------------------------------------------------------
 * 横向排列的 tab，支持：
 *   - 点击切换活跃
 *   - 中键 / close 按钮关闭
 *   - 拖拽重排（v0.2 接入 dnd-kit）
 */
import { Plus } from "lucide-react";
import { useTabsStore } from "./tabsStore";
import { Tab } from "./Tab";
import { useUIStore } from "@stores/uiStore";
import { useShortcut } from "@hooks/useShortcut";
import clsx from "clsx";

export interface TabBarProps {
  className?: string;
}

export function TabBar({ className }: TabBarProps): JSX.Element | null {
  const tabs        = useTabsStore((s) => s.tabs);
  const activeId    = useTabsStore((s) => s.activeTabId);
  const setActive   = useTabsStore((s) => s.setActive);
  const addTab      = useTabsStore((s) => s.addTab);
  const openPalette = useUIStore((s) => s.openCommandPalette);

  // Ctrl+T 新建 tab
  useShortcut({ key: "cmdorctrl+t", handler: () => {
    // TODO: 弹出新建会话向导
    addTab({ sessionId: "stub", title: "New Tab", closable: true });
  }});

  // Ctrl+W 关闭当前 tab
  useShortcut({ key: "cmdorctrl+w", handler: () => {
    if (activeId) useTabsStore.getState().removeTab(activeId);
  }});

  if (tabs.length === 0) return null;

  return (
    <div
      className={clsx(
        "flex h-9 items-end border-b border-moss-border bg-moss-surface",
        className,
      )}
    >
      {tabs.map((t) => (
        <Tab
          key={t.id}
          tab={t}
          active={t.id === activeId}
          onActivate={() => setActive(t.id)}
          onClose={() => useTabsStore.getState().removeTab(t.id)}
        />
      ))}

      <button
        onClick={openPalette}
        className="m-1 ml-0.5 rounded p-1 text-ink-muted hover:bg-moss-hover hover:text-ink"
        title="新建 tab / 打开命令面板"
      >
        <Plus size={14} />
      </button>
    </div>
  );
}
