/**
 * MainLayout
 * --------------------------------------------------------------------
 * 应用主框架：
 *
 *   ┌────────────────────────────────────────────────┐
 *   │                  TitleBar                        │
 *   ├──────────┬─────────────────────────────────────┤
 *   │          │  TabBar (v0.2+)                      │
 *   │ Sidebar  ├─────────────────────────────────────┤
 *   │          │                                       │
 *   │          │  TerminalView / SftpPanel / Settings │
 *   │          │                                       │
 *   ├──────────┴─────────────────────────────────────┤
 *   │                  StatusBar                      │
 *   └────────────────────────────────────────────────┘
 *
 * v0.1：单 session，无 TabBar。
 */
import { useEffect } from "react";
import clsx from "clsx";
import { TitleBar } from "./TitleBar";
import { StatusBar } from "./StatusBar";
import { Sidebar } from "./Sidebar";
import { TerminalView } from "@components/terminal/TerminalView";
import { SftpPanel } from "@components/sftp/SftpPanel";
import { useUIStore } from "@stores/uiStore";
import { useConnectionStore } from "@stores/connectionStore";
import { useShortcut } from "@hooks/useShortcut";
import { logger } from "@utils/logger";

export function MainLayout(): JSX.Element {
  const sidebarVisible    = useUIStore((s) => s.sidebarVisible);
  const sftpPanelVisible  = useUIStore((s) => s.sftpPanelVisible);
  const toggleSidebar     = useUIStore((s) => s.toggleSidebar);
  const toggleSftpPanel   = useUIStore((s) => s.toggleSftpPanel);
  const openPalette       = useUIStore((s) => s.openCommandPalette);
  const refreshSessions   = useConnectionStore((s) => s.refreshSessions);

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
    <div className="flex h-full w-full flex-col">
      {/* 标题栏 */}
      <TitleBar />

      {/* 主体 */}
      <div className="flex flex-1 min-h-0">
        {/* 侧栏 */}
        <aside
          className={clsx(
            "flex flex-col bg-moss-surface border-r border-moss-border transition-all duration-150",
            sidebarVisible ? "w-60" : "w-0 overflow-hidden",
          )}
        >
          <Sidebar />
        </aside>

        {/* 中部：终端 / SFTP */}
        <main className="flex flex-1 min-w-0 flex-col bg-moss-bg">
          {/* TODO: <TabBar /> v0.2+ */}

          <div className="flex flex-1 min-h-0">
            <section className="flex-1 min-w-0">
              <TerminalView />
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
