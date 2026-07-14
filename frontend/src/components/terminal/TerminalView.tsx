/**
 * TerminalView
 * --------------------------------------------------------------------
 * 顶层"终端区"包装：
 *   - 监听 activeSessionId 决定渲染哪个 Terminal
 *   - 没有活跃 session 时显示空状态（引导用户新建/打开）
 *   - 监听 session:state 事件更新 connectionStore
 */
import { useEffect } from "react";
import { Terminal } from "./Terminal";
import { useConnectionStore } from "@stores/connectionStore";
import { useUIStore } from "@stores/uiStore";
import { useWailsEvent } from "@hooks/useWailsEvent";
import { Button } from "@components/common/Button";
import { Terminal as TerminalIcon, Plus, FolderOpen } from "lucide-react";
import {
  EventTopic,
  type SessionStateEvent,
  type SessionExitEvent,
} from "@/types/events";
import type { SessionState } from "@/types/session";
import { ACTIVE_STATES } from "@/types/session";

const RAW_TO_STATE: Record<string, SessionState> = {
  connecting:      "connecting",
  authenticating:  "authenticating",
  established:     "established",
  closing:         "closing",
  closed:          "closed",
  failed:          "failed",
};

function normalizeState(raw: string): SessionState {
  return RAW_TO_STATE[raw] ?? "closed";
}

export function TerminalView(): JSX.Element {
  const activeSid   = useConnectionStore((s) => s.activeSessionId);
  const setActive   = useConnectionStore((s) => s.setActiveSession);
  const upsert      = useConnectionStore((s) => s.upsertSession);
  const remove      = useConnectionStore((s) => s.removeSession);
  const openPalette = useUIStore((s) => s.openCommandPalette);
  const pushToast   = useUIStore((s) => s.pushToast);

  // 同步 session:state 到 store
  useWailsEvent<SessionStateEvent>(
    EventTopic.SessionState,
    (ev) => {
      const cur = useConnectionStore.getState().sessions[ev.id];
      if (cur) upsert({ ...cur, state: ev.state });
      // 失败时弹 Toast
      if (ev.state === "failed" && cur) {
        pushToast({
          level: "error",
          message: `Session ${cur.name} failed`,
          durationMs: 5000,
        });
      }
    },
    (raw: unknown): SessionStateEvent => {
      const r = raw as { id: string; state: string };
      return { id: r.id, state: normalizeState(r.state) };
    },
  );

  // session:exit → 从 store 移除
  useWailsEvent<SessionExitEvent>(
    EventTopic.SessionExit,
    (ev) => {
      remove(ev.id);
      if (useConnectionStore.getState().activeSessionId === ev.id) {
        setActive(null);
      }
      if (ev.code !== 0) {
        pushToast({
          level: "warn",
          message: `Session exited (code ${ev.code}): ${ev.msg}`,
          durationMs: 5000,
        });
      }
    },
  );

  // v0.1 单 session：挂载时若无 active，提示去命令面板
  useEffect(() => {
    if (!activeSid) {
      // 可选：自动聚焦命令面板
    }
  }, [activeSid]);

  if (!activeSid) {
    return <EmptyState onOpenPalette={openPalette} />;
  }

  return (
    <div className="h-full w-full p-2">
      <Terminal sessionId={activeSid} autoFocus />
    </div>
  );
}

interface EmptyStateProps {
  onOpenPalette: () => void;
}

function EmptyState({ onOpenPalette }: EmptyStateProps): JSX.Element {
  return (
    <div className="flex h-full w-full items-center justify-center bg-moss-bg">
      <div className="flex max-w-md flex-col items-center gap-4 text-center">
        <div className="rounded-full border border-moss-border bg-moss-surface p-4">
          <TerminalIcon size={32} className="text-accent" />
        </div>
        <div>
          <h2 className="text-lg font-semibold text-ink">还没有活跃的会话</h2>
          <p className="mt-1 text-sm text-ink-muted">
            从左侧选一个 profile 打开，或在命令面板里快速连接。
          </p>
        </div>
        <div className="flex gap-2">
          <Button
            variant="primary"
            icon={<Plus size={14} />}
            onClick={onOpenPalette}
          >
            打开命令面板
            <span className="ml-1 text-[10px] opacity-60">Ctrl+Shift+P</span>
          </Button>
          <Button icon={<FolderOpen size={14} />}>从 profile 开始</Button>
        </div>
        <p className="mt-2 text-[11px] text-ink-subtle">
          v0.1 支持单 session；v0.2+ 启用多 tab 与 SFTP 面板
        </p>
      </div>
    </div>
  );
}

// re-export 给外部
export { ACTIVE_STATES };
