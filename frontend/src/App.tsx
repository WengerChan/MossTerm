/**
 * 顶层 App 组件
 * --------------------------------------------------------------------
 * 负责把全局 layout、stores 初始化、命令面板等横切关注点串起来。
 * 当前 v0.1 阶段仅做骨架。
 */
import { useEffect } from "react";
import { MainLayout } from "@components/layout/MainLayout";
import { CommandPalette } from "@components/palette/CommandPalette";
import { TrustRequestModal } from "@components/knownhosts/TrustRequestModal";
import { SftpBrowser } from "@components/sftp/SftpBrowser";
import { useSftpBrowserStore } from "@components/sftp/sftpBrowserStore";
import { useAppStore } from "@stores/appStore";
import { useUIStore } from "@stores/uiStore";

export default function App(): JSX.Element {
  // 应用启动时拉取一次后端状态
  const initApp = useAppStore((s) => s.init);
  const paletteOpen = useUIStore((s) => s.commandPaletteOpen);

  // v0.5.1：SFTP 浏览器开关（受 sftpBrowserStore 全局控制）
  const sftpOpen   = useSftpBrowserStore((s) => s.isOpen);
  const sftpSid    = useSftpBrowserStore((s) => s.sessionID);
  const sftpClose  = useSftpBrowserStore((s) => s.close);

  useEffect(() => {
    void initApp();
  }, [initApp]);

  return (
    <div className="h-screen w-screen overflow-hidden bg-moss-bg text-ink font-sans">
      {/* 主框架：标题栏 + 侧栏 + 终端区 + 状态栏 */}
      <MainLayout />

      {/* 命令面板：覆盖在最上层 */}
      {paletteOpen && <CommandPalette />}

      {/* v0.5.0 C：首次连接信任弹窗（监听后端 knownhosts:trust-request） */}
      {/*
       * 始终挂在树里；组件内部根据是否有 request 决定是否渲染。
       * 挂在外层（不在 MainLayout 里）确保 z-index 凌驾于普通 UI 之上，
       * 与 CommandPalette 一致。v0.5.0 单并发：同一时刻只会显示一个 modal。
       */}
      <TrustRequestModal />

      {/*
       * v0.5.1 SFTP 浏览器：受 sftpBrowserStore 控制；
       * open / sessionID / onClose 三件套都从 store 拿。
       * 挂在 App 顶层（不在 MainLayout 里），与其他 modal 同级。
       */}
      <SftpBrowser
        open={sftpOpen}
        sessionID={sftpSid}
        onClose={sftpClose}
      />
    </div>
  );
}
