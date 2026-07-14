/**
 * 前端 Logger
 * --------------------------------------------------------------------
 * - 在浏览器里 fallback 到 console
 * - 在 Wails webview 里同步把日志通过 runtime.Log* 转发到后端
 *   （后端 slog 统一收集，方便 issue 反馈 / 排错）
 *
 * v0.1 阶段只做 console；v0.2 启用 Wails 转发。
 */

type LogLevel = "trace" | "debug" | "info" | "warn" | "error";

interface LogPayload {
  level: LogLevel;
  msg: string;
  ts: number;
}

let inWails = false;
try {
  inWails = typeof window !== "undefined" && !!window.runtime;
} catch {
  inWails = false;
}

/**
 * 内部：调用 console.*，按 level 路由
 */
function emitToConsole(payload: LogPayload): void {
  const { level, msg, ts } = payload;
  const prefix = `[${new Date(ts).toISOString()}]`;
  switch (level) {
    case "trace":
    case "debug":
      // eslint-disable-next-line no-console
      console.debug(prefix, msg);
      break;
    case "info":
      // eslint-disable-next-line no-console
      console.info(prefix, msg);
      break;
    case "warn":
      // eslint-disable-next-line no-console
      console.warn(prefix, msg);
      break;
    case "error":
      // eslint-disable-next-line no-console
      console.error(prefix, msg);
      break;
  }
}

/**
 * 内部：转发到 Wails runtime（v0.2+ 启用）
 */
function emitToWails(_payload: LogPayload): void {
  if (!inWails) return;
  // TODO: 真实启用（v0.6+）
  // const r = window.runtime;
  // switch (_payload.level) {
  //   case "trace": r.LogTrace(_payload.msg); break;
  //   case "debug": r.LogDebug(_payload.msg); break;
  //   case "info":  r.LogInfo(_payload.msg);  break;
  //   case "warn":  r.LogWarning(_payload.msg); break;
  //   case "error": r.LogError(_payload.msg); break;
  // }
}

function log(level: LogLevel, msg: string): void {
  const payload: LogPayload = { level, msg, ts: Date.now() };
  emitToConsole(payload);
  emitToWails(payload);
}

export const logger = {
  trace: (msg: string) => log("trace", msg),
  debug: (msg: string) => log("debug", msg),
  info:  (msg: string) => log("info",  msg),
  warn:  (msg: string) => log("warn",  msg),
  error: (msg: string) => log("error", msg),
};

export type { LogLevel };
