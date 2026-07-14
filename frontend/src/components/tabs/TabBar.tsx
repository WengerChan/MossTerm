/**
 * TabBar —— 多标签栏
 * --------------------------------------------------------------------
 * 横向排列的 tab，支持：
 *   - 点击切换活跃
 *   - 中键 / close 按钮关闭
 *   - 右侧 + 按钮新建空 tab
 *   - 右侧 SFTP 按钮打开当前 session 的 SFTP 浏览器（v0.5.1）
 *   - Ctrl/Cmd+T 新建；Ctrl/Cmd+W 关闭当前
 *
 * v0.5.0 B：始终渲染（即便 tabs 为空也显示 +），方便用户随时开新会话。
 * v0.5.1   ：加 SFTP 浏览器按钮（FolderOpen 图标）。
 */
import { Plus, FolderOpen } from "lucide-react";
import { useTabsStore } from "./tabsStore";
import { Tab } from "./Tab";
import { useUIStore } from "@stores/uiStore";
import { useSftpBrowserStore } from "@components/sftp/sftpBrowserStore";
import { useShortcut } from "@hooks/useShortcut";
import clsx from "clsx";

export interface TabBarProps {
  className?: string;
}

export function TabBar({ className }: TabBarProps): JSX.Element {
  const tabs       = useTabsStore((s) => s.tabs);
  const activeId   = useTabsStore((s) => s.activeTabId);
  const setActive  = useTabsStore((s) => s.setActiveTab);
  const addTab     = useTabsStore((s) => s.addTab);
  const removeTab  = useTabsStore((s) => s.removeTab);
  const openPalette = useUIStore((s) => s.openCommandPalette);
  const openSftp    = useSftpBrowserStore((s) => s.open);
  const pushToast   = useUIStore((s) => s.pushToast);

  // 新建一个空 tab；title 暂时显示 "New Tab"，连上 session 后由 openSession
  // 的回调 updateTab 覆盖。
  const newEmptyTab = (): string => {
    return addTab({
      title: "New Tab",
      sessionId: null,
      profileId: null,
      host: "",
      state: "idle",
    });
  };

  // Ctrl/Cmd+T 新建
  useShortcut({
    key: "cmdorctrl+t",
    handler: () => {
      newEmptyTab();
    },
  });

  // Ctrl/Cmd+W 关闭当前
  useShortcut({
    key: "cmdorctrl+w",
    handler: () => {
      const id = useTabsStore.getState().activeTabId;
      if (id) removeTab(id);
    },
  });

  // 打开 SFTP 浏览器 —— 取当前 active tab 的 sessionId。
  // 没有 active tab / tab 没绑 session：弹 toast 提示，不静默失败。
  const onOpenSftp = (): void => {
    const id = useTabsStore.getState().activeTabId;
    if (!id) {
      pushToast({
        level: "warn",
        message: "没有活跃 tab —— 先连一个 SSH 会话",
        durationMs: 2500,
      });
      return;
    }
    const tab = useTabsStore.getState().tabs.find((t) => t.id === id);
    const sid = tab?.sessionId ?? null;
    if (!sid) {
      pushToast({
        level: "warn",
        message: "当前 tab 还没绑 session —— 等待 SSH 连上",
        durationMs: 2500,
      });
      return;
    }
    openSftp(sid);
  };

  return (
    <div
      className={clsx(
        "flex h-9 items-center overflow-x-auto border-b border-moss-border bg-moss-surface",
        className,
      )}
      role="tablist"
    >
      {tabs.map((t) => (
        <Tab
          key={t.id}
          tab={t}
          active={t.id === activeId}
          onClick={() => setActive(t.id)}
          onClose={() => removeTab(t.id)}
        />
      ))}

      <button
        onClick={openPalette}
        className="ml-0.5 flex h-9 w-9 shrink-0 items-center justify-center text-ink-muted hover:bg-moss-hover hover:text-ink"
        title="新建 tab（Ctrl+T）/ 打开命令面板（Ctrl+Shift+P）"
        aria-label="New tab"
      >
        <Plus size={14} />
      </button>

      {/*
        v0.5.1 SFTP 浏览器按钮：开当前 active tab 的 session。
        始终渲染；点击时校验 session 是否就绪。
       */}
      <button
        onClick={onOpenSftp}
        className="ml-0.5 flex h-9 w-9 shrink-0 items-center justify-center text-ink-muted hover:bg-moss-hover hover:text-accent"
        title="SFTP 浏览器"
        aria-label="SFTP 浏览器"
        data-testid="sftp-browser-button"
      >
        <FolderOpen size={14} />
      </button>
    </div>
  );
}
