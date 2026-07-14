/**
 * MainLayout
 * --------------------------------------------------------------------
 * 应用主框架（v0.5.0 B：多 tab + split pane）：
 *
 *   ┌────────────────────────────────────────────────────────┐
 *   │                       TitleBar                          │
 *   ├──────────┬─────────────────────────────────────────────┤
 *   │          │   TabBar                                     │
 *   │ Sidebar  ├─────────────────────────────────────────────┤
 *   │          │                                              │
 *   │          │   Pane tree (递归)        │   SftpPanel (可 │
 *   │          │   ┌──────┬──────┐         │   选)            │
 *   │          │   │ Term │ Term │         │                  │
 *   │          │   └──────┴──────┘         │                  │
 *   │          │                                              │
 *   ├──────────┴─────────────────────────────────────────────┤
 *   │                       StatusBar                         │
 *   └────────────────────────────────────────────────────────┘
 *
 * Pane tree 渲染：tabsStore.activeTab.panes[0] 是 root。
 *   - root 是 leaf：单个 PaneView
 *   - root 是 split：SplitPane 嵌套 PaneView
 */
import { useEffect } from "react";
import clsx from "clsx";
import { TitleBar } from "./TitleBar";
import { StatusBar } from "./StatusBar";
import { Sidebar } from "./Sidebar";
import { TabBar } from "@components/tabs/TabBar";
import { PaneView } from "@components/tabs/PaneView";
import { useTabsStore } from "@components/tabs/tabsStore";
import { SftpPanel } from "@components/sftp/SftpPanel";
import { useUIStore } from "@stores/uiStore";
import { useConnectionStore } from "@stores/connectionStore";
import { useShortcut } from "@hooks/useShortcut";
import { logger } from "@utils/logger";

export function MainLayout(): JSX.Element {
  const sidebarVisible   = useUIStore((s) => s.sidebarVisible);
  const sftpPanelVisible = useUIStore((s) => s.sftpPanelVisible);
  const toggleSidebar    = useUIStore((s) => s.toggleSidebar);
  const toggleSftpPanel  = useUIStore((s) => s.toggleSftpPanel);
  const openPalette      = useUIStore((s) => s.openCommandPalette);
  const refreshSessions  = useConnectionStore((s) => s.refreshSessions);

  // 顶部 tabs 树
  const tabs         = useTabsStore((s) => s.tabs);
  const activeTabId  = useTabsStore((s) => s.activeTabId);
  const splitPane    = useTabsStore((s) => s.splitPane);
  const closePane    = useTabsStore((s) => s.closePane);
  const setActivePane = useTabsStore((s) => s.setActivePane);

  const activeTab = tabs.find((t) => t.id === activeTabId) ?? null;
  const rootPanes = activeTab?.panes ?? [];

  // 启动时拉取一次会话列表
  useEffect(() => {
    void refreshSessions();
  }, [refreshSessions]);

  // 全局快捷键
  useShortcut({ key: "cmdorctrl+shift+p", handler: () => openPalette() });
  useShortcut({ key: "cmdorctrl+`",       handler: () => toggleSidebar() });
  useShortcut({ key: "cmdorctrl+alt+s",   handler: () => toggleSftpPanel() });

  useEffect(() => {
    logger.info("[MainLayout] mounted");
  }, []);

  return (
    <div className="flex h-full w-full flex-col bg-moss-bg text-ink">
      {/* 标题栏 */}
      <TitleBar />

      {/* 主体 */}
      <div className="flex min-h-0 flex-1">
        {/* 侧栏 */}
        <aside
          className={clsx(
            "flex flex-col border-r border-moss-border bg-moss-surface transition-all duration-150",
            sidebarVisible ? "w-60" : "w-0 overflow-hidden",
          )}
        >
          <Sidebar />
        </aside>

        {/* 中部：Tab + Pane tree + Sftp */}
        <main className="flex min-w-0 flex-1 flex-col">
          <TabBar />

          <div className="flex min-h-0 flex-1">
            <section className="relative min-w-0 flex-1">
              {activeTab && rootPanes.length > 0 ? (
                <PaneTree
                  tabId={activeTab.id}
                  rootPanes={rootPanes}
                  activePaneId={activeTab.activePaneId}
                  onSplit={splitPane}
                  onClose={closePane}
                  onActivate={setActivePane}
                />
              ) : (
                <EmptyState onOpenPalette={openPalette} />
              )}
            </section>

            {sftpPanelVisible && (
              <section className="w-80 border-l border-moss-border">
                <SftpPanel />
              </section>
            )}
          </div>
        </main>
      </div>

      {/* 状态栏 */}
      <StatusBar />
    </div>
  );
}

// =====================================================================
// PaneTree —— 把 root panes 渲染成 PaneView 列表（一般 length=1）
// =====================================================================
interface PaneTreeProps {
  tabId: string;
  rootPanes: import("@components/tabs/tabsStore").Pane[];
  activePaneId: string;
  onSplit: (
    tabId: string,
    paneId: string,
    dir: import("@components/tabs/tabsStore").PaneSplitDirection,
  ) => void;
  onClose: (tabId: string, paneId: string) => void;
  onActivate: (tabId: string, paneId: string) => void;
}

function PaneTree({
  tabId,
  rootPanes,
  activePaneId,
  onSplit,
  onClose,
  onActivate,
}: PaneTreeProps): JSX.Element {
  // 极少数情况下 rootPanes.length > 1：关闭 split 节点会 lift 它的 children
  // 到 root 层。用 flex 容器让多个 root 横向均分。
  return (
    <div className="flex h-full w-full min-h-0 min-w-0 flex-row">
      {rootPanes.map((pane) => (
        <PaneView
          key={pane.id}
          pane={pane}
          isActive={pane.id === activePaneId}
          onActivate={(paneId) => onActivate(tabId, paneId)}
          onSplit={(paneId, dir) => onSplit(tabId, paneId, dir)}
          onClose={(paneId) => onClose(tabId, paneId)}
        />
      ))}
    </div>
  );
}

// =====================================================================
// EmptyState —— 没有 tab 时的引导
// =====================================================================
interface EmptyStateProps {
  onOpenPalette: () => void;
}

function EmptyState({ onOpenPalette }: EmptyStateProps): JSX.Element {
  return (
    <div className="flex h-full w-full items-center justify-center bg-moss-bg">
      <div className="flex max-w-md flex-col items-center gap-4 text-center">
        <div className="rounded-full border border-moss-border bg-moss-surface p-4 text-accent">
          {/* 装饰：模仿 WindTerm 的方块 logo */}
          <div className="grid grid-cols-2 gap-1">
            <div className="h-3 w-3 rounded-sm bg-accent" />
            <div className="h-3 w-3 rounded-sm bg-state-warn" />
            <div className="h-3 w-3 rounded-sm bg-state-info" />
            <div className="h-3 w-3 rounded-sm bg-state-err" />
          </div>
        </div>
        <div>
          <h2 className="text-lg font-semibold text-ink">No tabs open</h2>
          <p className="mt-1 text-sm text-ink-muted">
            Press <kbd className="rounded border border-moss-border bg-moss-surface px-1.5 py-0.5 text-[11px]">Ctrl+T</kbd>{" "}
            to open a new tab, or
          </p>
        </div>
        <button
          onClick={onOpenPalette}
          className="rounded border border-accent bg-accent/10 px-3 py-1.5 text-sm text-accent hover:bg-accent/20"
        >
          Open command palette (Ctrl+Shift+P)
        </button>
      </div>
    </div>
  );
}
