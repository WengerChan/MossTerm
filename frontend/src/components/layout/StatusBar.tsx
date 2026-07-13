/**
 * StatusBar
 * --------------------------------------------------------------------
 * 底部状态栏：
 *   - 左侧：连接状态 / 协议 / 主机
 *   - 中部：PTY 几何 cols × rows
 *   - 右侧：通知 / 主题 / 快捷键提示
 */
import { Activity, CircleAlert, Keyboard, Sun, Moon } from "lucide-react";
import { useConnectionStore } from "@stores/connectionStore";
import { useUIStore } from "@stores/uiStore";
import { useAppStore } from "@stores/appStore";
import { useTerminalStore } from "@components/terminal/terminalStore";
import clsx from "clsx";

export interface StatusBarProps {
  className?: string;
}

export function StatusBar({ className }: StatusBarProps): JSX.Element {
  const activeSid   = useConnectionStore((s) => s.activeSessionId);
  const session     = useConnectionStore((s) =>
    activeSid ? s.sessions[activeSid] : undefined,
  );
  const meta        = useTerminalStore((s) =>
    activeSid ? s.metas[activeSid] : undefined,
  );
  const theme       = useAppStore((s) => s.theme);
  const setTheme    = useAppStore((s) => s.setTheme);
  const toggleLog   = useUIStore((s) => s.toggleLogPanel);

  const stateColor: Record<string, string> = {
    connecting:     "text-state-warn",
    authenticating: "text-state-warn",
    established:    "text-state-ok",
    closing:        "text-ink-muted",
    closed:         "text-ink-muted",
    failed:         "text-state-err",
  };

  return (
    <footer
      className={clsx(
        "h-6 flex items-center justify-between border-t border-moss-border bg-moss-surface px-3 text-[11px] text-ink-muted",
        className,
      )}
    >
      {/* 左 */}
      <div className="flex items-center gap-3">
        <span className={clsx("flex items-center gap-1", session ? stateColor[session.state] : "")}>
          {session?.state === "failed" ? <CircleAlert size={11} /> : <Activity size={11} />}
          {session ? session.state : "idle"}
        </span>
        {session && (
          <>
            <span>{session.protocol.toUpperCase()}</span>
            <span>
              {session.user}@{session.host}:{session.port}
            </span>
          </>
        )}
      </div>

      {/* 中 */}
      <div className="flex items-center gap-3">
        {meta && (
          <span className="font-mono">
            {meta.size.cols} × {meta.size.rows}
          </span>
        )}
      </div>

      {/* 右 */}
      <div className="flex items-center gap-2">
        <button
          onClick={toggleLog}
          className="rounded p-0.5 hover:bg-moss-hover hover:text-ink"
          title="Toggle Log Panel (F1)"
        >
          <Keyboard size={12} />
        </button>
        <button
          onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
          className="rounded p-0.5 hover:bg-moss-hover hover:text-ink"
          title="Toggle theme"
        >
          {theme === "dark" ? <Moon size={12} /> : <Sun size={12} />}
        </button>
      </div>
    </footer>
  );
}
