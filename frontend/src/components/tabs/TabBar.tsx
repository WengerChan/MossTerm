/**
 * TabBar —— 多标签栏
 * --------------------------------------------------------------------
 * 横向排列的 tab，支持：
 *   - 点击切换活跃
 *   - 中键 / close 按钮关闭
 *   - 右侧 + 按钮新建空 tab
 *   - 右侧 SFTP 按钮：v0.5.8 起调 `addSftpPane` 在当前 tab 嵌入 SFTP pane
 *     （之前 v0.5.1 是开 modal；v0.5.8 改为 pane 树主路径，modal 仍保留）
 *   - Ctrl/Cmd+T 新建；Ctrl/Cmd+W 关闭当前
 *
 * v0.5.0 B：始终渲染（即便 tabs 为空也显示 +），方便用户随时开新会话。
 * v0.5.1   ：加 SFTP 浏览器按钮（FolderOpen 图标）。
 * v0.5.8   ：SFTP 按钮改调 `addSftpPane(activeTabId, tab.sessionId)`；
 *            tab title 后缀加 pane kind 统计（"term" / "+1 sftp"）。
 */
import { Plus, FolderOpen } from "lucide-react";
import { useTabsStore } from "./tabsStore";
import { collectLeaves, treeHasLeafOfKind } from "./paneTree";
import { Tab } from "./Tab";
import { useUIStore } from "@stores/uiStore";
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
  const addSftpPane = useTabsStore((s) => s.addSftpPane);
  const openPalette = useUIStore((s) => s.openCommandPalette);
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

  // v0.5.8：在当前 active tab 嵌入 SFTP pane。
  //   - 拿不到 active tab / tab 没绑 session：弹 toast 提示，不静默失败
  //   - tab 已含 SFTP leaf：仍允许加（"再开一个"，每次都是独立 path 状态；
  //     关闭其中一个不影响其他，符合 v0.5.1 用户对 "SFTP 浏览器" 的预期）
  const onAddSftpPane = (): void => {
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
    if (!tab) return;
    // 拿 session id：tab.sessionId 是后端 session；用之绑新 SFTP leaf
    addSftpPane(id, tab.sessionId);
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
        v0.5.8 SFTP 按钮：在当前 active tab 内追加 SFTP leaf（不再弹 modal）。
        始终渲染；点击时校验 session 是否就绪（tab.sessionId == null 时
        addSftpPane 传 null，pane 渲染 EmptyLeafHint 引导用户先连 SSH）。
       */}
      <button
        onClick={onAddSftpPane}
        className="ml-0.5 flex h-9 w-9 shrink-0 items-center justify-center text-ink-muted hover:bg-moss-hover hover:text-accent"
        title="在当前 tab 加 SFTP pane（v0.5.8：嵌入 Pane 树而非 modal）"
        aria-label="Add SFTP pane"
        data-testid="sftp-pane-button"
      >
        <FolderOpen size={14} />
      </button>
    </div>
  );
}

// =====================================================================
// helpers —— Tab 内部用
// =====================================================================

/** 计算 tab 的 pane 概要：terminal / sftp 数量。 */
function summarizePanes(tab: import("./tabsStore").Tab): { term: number; sftp: number } {
  const leaves = collectLeaves(tab.panes);
  return {
    term: leaves.filter((p) => p.kind === "terminal").length,
    sftp: leaves.filter((p) => p.kind === "sftp").length,
  };
}

/** 树里是否含 SFTP leaf（v0.5.8 Tab title 后缀用）。 */
function hasSftpLeaf(tab: import("./tabsStore").Tab): boolean {
  return treeHasLeafOfKind(tab.panes, "sftp");
}

// 保留给 Tab 用（v0.5.8 Tab title 显示）
export { summarizePanes, hasSftpLeaf };
