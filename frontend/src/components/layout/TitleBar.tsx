/**
 * TitleBar
 * --------------------------------------------------------------------
 * 顶部标题栏：
 *   - 左侧：应用名 / 当前会话名
 *   - 中部：可拖拽区（`app-drag`）
 *   - 右侧：窗口控制按钮（macOS 不显示）
 *
 * 注意：Wails 的 webview 默认带原生标题栏；当前 v0.1 保留双层结构以便后续
 * 完全自定义。
 */
import { useEffect } from "react";
import { Minus, Square, X } from "lucide-react";
import { useAppStore, type Platform } from "@stores/appStore";
import { useConnectionStore } from "@stores/connectionStore";

export interface TitleBarProps {
  /** 自定义标题（默认 "MossTerm"） */
  title?: string;
}

export function TitleBar({ title = "MossTerm" }: TitleBarProps): JSX.Element {
  const platform    = useAppStore((s) => s.platform);
  const version     = useAppStore((s) => s.version);
  const activeSid   = useConnectionStore((s) => s.activeSessionId);
  const session     = useConnectionStore((s) =>
    activeSid ? s.sessions[activeSid] : undefined,
  );

  // 通知 Wails 同步窗口标题（v0.5.7: 暂未接通 Wails runtime，TODO 留到 v0.6）
  useEffect(() => {
    // const t = session ? `${session.name} — ${title}` : title;
    // window.runtime?.WindowSetTitle(t);
  }, [title, session]);

  return (
    <header className="app-drag h-9 flex items-center justify-between border-b border-moss-border bg-moss-surface px-3 select-none">
      {/* 左侧：app 名 + 当前 session */}
      <div className="flex items-center gap-2 text-xs">
        <span className="font-semibold tracking-wide text-accent">{title}</span>
        <span className="text-ink-muted">v{version}</span>
        {session && (
          <>
            <span className="text-ink-subtle">·</span>
            <span className="text-ink">{session.name}</span>
            <span className="text-ink-muted">
              {session.user}@{session.host}:{session.port}
            </span>
          </>
        )}
      </div>

      {/* 右侧：窗口控制（仅 Windows / Linux 显示，macOS 走 webview 原生） */}
      {platform !== "darwin" && <WindowControls />}
    </header>
  );
}

function WindowControls(): JSX.Element {
  const handleMin   = () => window.runtime?.WindowMinimise();
  const handleMax   = () => {
    void window.runtime?.WindowIsMaximised().then((max) => {
      if (max) window.runtime?.WindowUnmaximise();
      else      window.runtime?.WindowMaximise();
    });
  };
  const handleClose = () => window.runtime?.Quit();

  return (
    <div className="no-drag flex items-center gap-1">
      <button
        onClick={handleMin}
        className="rounded p-1.5 text-ink-muted hover:bg-moss-hover hover:text-ink"
        title="Minimize"
      >
        <Minus size={14} />
      </button>
      <button
        onClick={handleMax}
        className="rounded p-1.5 text-ink-muted hover:bg-moss-hover hover:text-ink"
        title="Maximize"
      >
        <Square size={12} />
      </button>
      <button
        onClick={handleClose}
        className="rounded p-1.5 text-ink-muted hover:bg-state-err/20 hover:text-state-err"
        title="Close"
      >
        <X size={14} />
      </button>
    </div>
  );
}

// 重新导出 Platform 以便外部按需引用
export type { Platform };
