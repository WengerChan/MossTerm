/**
 * 顶层 App 组件
 * --------------------------------------------------------------------
 * 负责把全局 layout、stores 初始化、命令面板等横切关注点串起来。
 * 当前 v0.1 阶段仅做骨架。
 */
import { useEffect } from "react";
import { MainLayout } from "@components/layout/MainLayout";
import { CommandPalette } from "@components/palette/CommandPalette";
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
    </div>
  );
}
