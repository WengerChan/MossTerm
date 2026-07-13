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
  | { topic: typeof EventTopic.LogLine;          payload: LogLineEvent };

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
