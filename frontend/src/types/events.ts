/**
 * 事件总线类型
 * --------------------------------------------------------------------
 * 与 `pkg/event/` 的 Go 常量镜像。
 *
 * 设计要点：
 *  - Wails emit 的 payload 全部为 snake_case JSON；
 *  - 前端订阅时通过 `WailsRuntime.EventsOn(topic, cb)` 拿到 raw payload，
 *    在 hook 层做 camelCase 转换。
 *  - PTY 数据流 (`session:data`) 的 `data` 字段是 `Uint8Array`，
 *    通过 Wails 二进制事件机制传递，跳过 base64。
 */

import type { SessionID, SessionState } from "./session";
import type { TransferProgressEvent } from "./sftp";

// =====================================================================
// 事件名常量
// =====================================================================
export const EventTopic = {
  SessionData:       "session:data",
  SessionState:      "session:state",
  SessionExit:       "session:exit",
  TransferProgress:  "transfer:progress",
  TransferDone:      "transfer:done",
  AIResponse:        "ai:response",
  LogLine:           "log:line",
  /** 背压溢出通知 */
  SessionOverflow:   "session:overflow",

  // ===== UI 意图事件（v0.5.0 B+） =====
  /** 用户新建 tab（用于埋点 / 日志 / 未来后端同步） */
  TabOpen:           "tab:open",
  /** 用户关闭 tab */
  TabClose:          "tab:close",
  /** 用户拆分 pane */
  PaneSplit:         "pane:split",

  // ===== Known Hosts（v0.5.0 C） =====
  /** 首次信任请求：未知 host key → 推 modal 给用户。 */
  KnownHostsTrustRequest: "knownhosts:trust-request",
} as const;

export type EventTopicName = typeof EventTopic[keyof typeof EventTopic];

// =====================================================================
// 事件 payload（前端内部使用的强类型）
// =====================================================================
export interface SessionDataEvent {
  id: SessionID;
  /** 二进制终端输出 */
  data: Uint8Array;
}

export interface SessionStateEvent {
  id: SessionID;
  state: SessionState;
}

export interface SessionExitEvent {
  id: SessionID;
  code: number;
  msg: string;
}

export interface SessionOverflowEvent {
  id: SessionID;
  dropped: number;  // 丢弃的字节数
}

export interface TransferDoneEvent {
  jobId: string;
  error?: string;
}

export interface AIResponseEvent {
  requestId: string;
  text: string;
  error?: string;
}

export interface LogLineEvent {
  level: "trace" | "debug" | "info" | "warn" | "error";
  msg: string;
  ts: number;
}

/**
 * 首次信任请求（v0.5.0 C）：后端 known_hosts 遇到未知 host key 时推给前端。
 *
 * 前端 TrustRequestModal 收到后展示 host / keyType / fingerprint / fullKey，
 * 用户点 trust/reject → 调 App.TrustHost(id, action) 通知后端。
 *
 * 字段名与后端 knownhosts.TrustRequest 的 json tag 对齐；Wails 默认按
 * JSON 字段名传输（PascalCase → PascalCase / camelCase 取决于 Wails 版本），
 * 这里显式写 camelCase，与 Go json tag 一致，避免转换开销。
 */
export interface TrustRequestEvent {
  /** 唯一请求 ID，前端回传时原样给回 App.TrustHost */
  id: string;
  /** 远端 host（含端口，如 "example.com:2222"） */
  host: string;
  /** SSH key 类型："ssh-ed25519" / "ssh-rsa" / ... */
  keyType: string;
  /** 完整 base64 key 的前 16 字符 + "..."（视觉截断） */
  fingerprint: string;
  /** 完整 base64 编码的 key（供"展开"展示 + 复制） */
  fullKey: string;
}

// =====================================================================
// 联合类型（事件总线入口处用）
// =====================================================================
export type AppEvent =
  | { topic: typeof EventTopic.SessionData;      payload: SessionDataEvent }
  | { topic: typeof EventTopic.SessionState;     payload: SessionStateEvent }
  | { topic: typeof EventTopic.SessionExit;      payload: SessionExitEvent }
  | { topic: typeof EventTopic.SessionOverflow;  payload: SessionOverflowEvent }
  | { topic: typeof EventTopic.TransferProgress; payload: TransferProgressEvent }
  | { topic: typeof EventTopic.TransferDone;     payload: TransferDoneEvent }
  | { topic: typeof EventTopic.AIResponse;       payload: AIResponseEvent }
  | { topic: typeof EventTopic.LogLine;          payload: LogLineEvent }
  | { topic: typeof EventTopic.KnownHostsTrustRequest; payload: TrustRequestEvent };

// =====================================================================
// 原始 payload（来自 Wails 的 snake_case JSON），用于类型守卫
// =====================================================================
export interface RawSessionData {
  id: string;
  data: number[];  // Wails 把 []byte 编码为 number[]
}
export interface RawSessionState {
  id: string;
  state: string;
}
export interface RawSessionExit {
  id: string;
  code: number;
  msg: string;
}
