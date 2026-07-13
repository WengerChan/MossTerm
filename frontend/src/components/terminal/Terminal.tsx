/**
 * Terminal 组件 —— xterm.js 的薄封装
 * --------------------------------------------------------------------
 * 职责：
 *   1. 创建 / 销毁 xterm.Terminal 实例
 *   2. 加载 FitAddon / WebLinksAddon / 可选 WebGLAddon
 *   3. 把 xterm.onData 桥接到后端 App.SendInput
 *   4. 把 xterm.onResize 桥接到后端 App.ResizePTY
 *   5. 监听 session:data 事件 → term.write
 *   6. 应用 Moss 主题
 *   7. 处理容器 ResizeObserver 触发 fit
 *
 * 关键设计：
 *   - xterm 实例不进入 React 状态（避免 re-render 抖动）
 *   - 所有事件订阅在 useEffect 中按 sessionId 绑定
 *   - 组件卸载时严格 dispose 并解绑
 */
import { useEffect, useRef } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
// import { WebglAddon } from "@xterm/addon-webgl"; // 可选，性能更好
import "@xterm/xterm/css/xterm.css";

import { useTerminalStore } from "@components/terminal/terminalStore";
import { useWailsEvent } from "@hooks/useWailsEvent";
import { EventTopic, type SessionDataEvent } from "@types/events";
import type { SessionID, PTYSize } from "@types/session";
import { logger } from "@utils/logger";

/**
 * Moss 暗色主题 —— 与 tailwind.config.js 同步
 * xterm.js 接受 ITheme 类型的对象。
 */
export const MOSS_DARK_THEME = {
  background:    "#1a1d23",
  foreground:    "#e4e6eb",
  cursor:        "#7CB342",
  cursorAccent:  "#1a1d23",
  selectionBackground: "rgba(124, 179, 66, 0.35)",

  black:         "#1a1d23",
  red:           "#e5484d",
  green:         "#7CB342",
  yellow:        "#f0a830",
  blue:          "#5aa9e6",
  magenta:       "#c678dd",
  cyan:          "#56b6c2",
  white:         "#e4e6eb",

  brightBlack:   "#5b6168",
  brightRed:     "#ff6b72",
  brightGreen:   "#a8d878",
  brightYellow:  "#ffb454",
  brightBlue:    "#79b8ff",
  brightMagenta: "#e288fa",
  brightCyan:    "#7fdbca",
  brightWhite:   "#ffffff",
} as const;

export interface TerminalProps {
  sessionId: SessionID;
  /** 容器 className（用于控制尺寸/边距） */
  className?: string;
  /** 是否在挂载后立即 focus */
  autoFocus?: boolean;
  /** 是否启用 WebGL（性能更好但需要 GPU） */
  enableWebGL?: boolean;
}

export function Terminal({
  sessionId,
  className,
  autoFocus = true,
  enableWebGL = false,
}: TerminalProps): JSX.Element {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const xtermRef     = useRef<XTerm | null>(null);
  const fitRef       = useRef<FitAddon | null>(null);

  const config = useTerminalStore((s) => s.configs[sessionId]);
  const ensureConfig = useTerminalStore((s) => s.ensureConfig);
  const setSize      = useTerminalStore((s) => s.setSize);
  const markAttached = useTerminalStore((s) => s.markAttached);

  // session:data 事件 → term.write
  useWailsEvent<SessionDataEvent>(
    EventTopic.SessionData,
    (ev) => {
      if (ev.id !== sessionId) return;
      if (!xtermRef.current) return;
      xtermRef.current.write(ev.data);
    },
    (raw: unknown): SessionDataEvent => {
      const r = raw as { id: string; data: number[] | Uint8Array };
      const data =
        r.data instanceof Uint8Array ? r.data : new Uint8Array(r.data);
      return { id: r.id, data };
    },
  );

  // 第一次挂载：创建 xterm 实例
  useEffect(() => {
    const cfg = ensureConfig(sessionId);
    if (!containerRef.current) return;

    const term = new XTerm({
      fontSize:   cfg.fontSize,
      fontFamily: cfg.fontFamily,
      cursorBlink: cfg.cursorBlink,
      cursorStyle: cfg.cursorStyle,
      scrollback: cfg.scrollback,
      theme: MOSS_DARK_THEME,
      allowProposedApi: true,
      // macOS 触摸板自然滚动
      smoothScrollDuration: 0,
    });

    const fit = new FitAddon();
    term.loadAddon(fit);
    term.loadAddon(new WebLinksAddon());

    // 可选 WebGL 渲染（性能更好；失败时降级 canvas）
    if (enableWebGL) {
      try {
        // const webgl = new WebglAddon();
        // webgl.onContextLoss(() => webgl.dispose());
        // term.loadAddon(webgl);
      } catch (err: unknown) {
        logger.warn(`[Terminal] WebGL addon load failed, fallback to canvas: ${String(err)}`);
      }
    }

    term.open(containerRef.current);
    fit.fit();
    xtermRef.current = term;
    fitRef.current = fit;
    markAttached(sessionId, true);

    // 用户键入 → 发到后端
    const subData = term.onData((data) => {
      // TODO: App.SendInput(sessionId, new TextEncoder().encode(data))
      // const u8 = new TextEncoder().encode(data);
      // window.go.app.App.SendInput(sessionId, u8).catch((e) => console.error(e));
      logger.debug(`[Terminal] onData sid=${sessionId} len=${data.length}`);
    });

    // xterm 报告尺寸变化（fit / 字体变更触发）→ 同步到后端 PTY
    const subResize = term.onResize(({ cols, rows }) => {
      const size: PTYSize = { cols, rows };
      setSize(sessionId, size);
      // TODO: window.go.app.App.ResizePTY(sessionId, cols, rows);
    });

    if (autoFocus) {
      // 等一帧再 focus，避免容器尺寸未稳定
      requestAnimationFrame(() => term.focus());
    }

    // 容器尺寸监听 → 触发 fit
    const ro = new ResizeObserver(() => {
      // 节流：rAF 合并多次回调
      requestAnimationFrame(() => {
        try {
          fit.fit();
        } catch (err: unknown) {
          logger.warn(`[Terminal] fit failed: ${String(err)}`);
        }
      });
    });
    ro.observe(containerRef.current);

    // 清理
    return () => {
      subData.dispose();
      subResize.dispose();
      ro.disconnect();
      term.dispose();
      xtermRef.current = null;
      fitRef.current = null;
      markAttached(sessionId, false);
    };
    // 仅在 sessionId 变化时重建终端；config 变更走单独的 effect 热更新
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, enableWebGL]);

  // config 热更新（字体 / 字号 / 主题 / scrollback 等）
  useEffect(() => {
    const t = xtermRef.current;
    if (!t || !config) return;
    t.options.fontSize = config.fontSize;
    t.options.fontFamily = config.fontFamily;
    t.options.cursorBlink = config.cursorBlink;
    t.options.cursorStyle = config.cursorStyle;
    t.options.scrollback = config.scrollback;
    t.options.theme = MOSS_DARK_THEME;
    fitRef.current?.fit();
  }, [config]);

  return (
    <div
      ref={containerRef}
      className={`h-full w-full overflow-hidden bg-moss-bg ${className ?? ""}`}
      // xterm 自己管理内部 DOM 渲染
      data-session-id={sessionId}
    />
  );
}

// 兼容 TS 严格模式下的 default export
export default Terminal;
