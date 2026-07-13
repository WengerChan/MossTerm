/**
 * useTerminal
 * --------------------------------------------------------------------
 * 封装"创建 / 销毁 / resize / write" 的核心逻辑，
 * 供 Terminal.tsx 与外部使用 hook 的场景共用。
 *
 * 注意：实际的 xterm.js 实例仍由 Terminal.tsx 用 ref 持有；
 * 本 hook 主要负责把 store 里的 config 应用到 Term、订阅事件、
 * 处理 fit / resize 的副作用。
 */
import { useCallback, useEffect, useRef } from "react";
import type { Terminal as XTerminal } from "@xterm/xterm";
import { useTerminalStore } from "@components/terminal/terminalStore";
import { useWailsEvent } from "@hooks/useWailsEvent";
import { EventTopic, type SessionDataEvent } from "@types/events";
import type { SessionID, PTYSize } from "@types/session";
import { logger } from "@utils/logger";

export interface UseTerminalOptions {
  sessionId: SessionID;
  /** 是否启用 WebGL addon */
  webgl?: boolean;
}

export interface UseTerminalApi {
  termRef: React.MutableRefObject<XTerminal | null>;
  containerRef: React.MutableRefObject<HTMLDivElement | null>;
  fit: () => void;
  write: (data: string | Uint8Array) => void;
  clear: () => void;
  focus: () => void;
  /** 应用当前 store 中的 config 到 xterm 实例 */
  applyConfig: () => void;
}

export function useTerminal(opts: UseTerminalOptions): UseTerminalApi {
  const { sessionId } = opts;
  const termRef     = useRef<XTerminal | null>(null);
  const containerRef = useRef<HTMLDivElement | null>(null);

  const ensureConfig = useTerminalStore((s) => s.ensureConfig);
  const setSize      = useTerminalStore((s) => s.setSize);
  const markAttached = useTerminalStore((s) => s.markAttached);

  // 启动时确保 config 存在
  useEffect(() => {
    ensureConfig(sessionId);
  }, [ensureConfig, sessionId]);

  // 订阅 session:data → term.write
  useWailsEvent<SessionDataEvent>(
    EventTopic.SessionData,
    (ev) => {
      if (ev.id !== sessionId) return;
      if (!termRef.current) return;
      // Uint8Array 直接 write；string 走默认
      termRef.current.write(ev.data);
    },
    (raw: unknown): SessionDataEvent => {
      // 把 snake_case raw 转换为强类型 payload
      const r = raw as { id: string; data: number[] | Uint8Array };
      const data =
        r.data instanceof Uint8Array ? r.data : new Uint8Array(r.data);
      return { id: r.id, data };
    },
  );

  // ===== 工具方法 =====
  const fit = useCallback((): void => {
    // TODO: 调用 fitAddon.fit()，节流（rAF）避免抖动
    // const fit = new FitAddon();
    // term.loadAddon(fit);
    // fit.fit();
    const cols = 80;
    const rows = 24;
    const size: PTYSize = { cols, rows };
    setSize(sessionId, size);
    logger.debug(`[useTerminal] fit sid=${sessionId} ${cols}x${rows}`);
  }, [sessionId, setSize]);

  const write = useCallback((data: string | Uint8Array): void => {
    termRef.current?.write(data);
  }, []);

  const clear = useCallback((): void => {
    termRef.current?.clear();
    markAttached(sessionId, true);
  }, [markAttached, sessionId]);

  const focus = useCallback((): void => {
    termRef.current?.focus();
  }, []);

  const applyConfig = useCallback((): void => {
    // TODO: 从 store 读 config，写入 term.options
  }, []);

  // 组件卸载时清理
  useEffect(() => {
    return () => {
      const t = termRef.current;
      if (t) {
        t.dispose();
        termRef.current = null;
        markAttached(sessionId, false);
      }
    };
  }, [sessionId, markAttached]);

  return { termRef, containerRef, fit, write, clear, focus, applyConfig };
}
