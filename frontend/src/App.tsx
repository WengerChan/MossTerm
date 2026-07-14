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
import { ProfileEditModal } from "@components/session/ProfileEditModal";
import { ConfirmDeleteProfile } from "@components/session/ConfirmDeleteProfile";
import { useAppStore } from "@stores/appStore";
import { useUIStore } from "@stores/uiStore";

export default function App(): JSX.Element {
  // 应用启动时拉取一次后端状态
  const initApp = useAppStore((s) => s.init);
  const paletteOpen = useUIStore((s) => s.commandPaletteOpen);

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
       * v0.5.8 SFTP 集成到 Pane 树：SFTP 浏览器不再以独立 modal 形式
       * 弹在 App 顶层 —— 改为 `tabsStore.addSftpPane` 在当前 active tab
       * 内追加 SFTP leaf（与 SSH terminal 并列）。
       *
       * 旧 SftpBrowser modal 包装仍 export 保留（@components/sftp/SftpBrowser）
       * —— 未来 v0.6+ "SFTP 预览" / "命令面板调起独立面板" 仍可挂载，
       * 但 v0.5.8 默认不在 App.tsx 挂。
       */}

      {/*
       * v0.5.6 Profile 编辑 modal（包 SessionForm）+ 删除确认 modal。
       * 都用 useUIStore.modal.id 匹配 + 自己判断是否渲染。
       * 触发链：Sidebar "+" / SessionTree hover 编辑/删除按钮 → sessionStore.startCreate / startEdit / openModal。
       */}
      <ProfileEditModal />
      <ConfirmDeleteProfile />
    </div>
  );
}
