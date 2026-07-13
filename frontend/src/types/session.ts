/**
 * Session / Profile DTO 类型
 * --------------------------------------------------------------------
 * 与 `internal/session/` 和 `internal/config/` 的 Go struct 一一镜像。
 * 任何修改必须先在 ARCHITECTURE.md §6 提 PR。
 */

// =====================================================================
// 基础 ID 类型
// =====================================================================
export type SessionID = string;
export type ProfileID = string;
export type SecretID = string;

// =====================================================================
// 协议与状态
// =====================================================================
export type Protocol = "ssh" | "sftp" | "telnet" | "serial";

export type SessionState =
  | "connecting"
  | "authenticating"
  | "established"
  | "closing"
  | "closed"
  | "failed";

/** 渲染层用的"是否还在活动连接中"判定 */
export const ACTIVE_STATES: ReadonlySet<SessionState> = new Set([
  "connecting",
  "authenticating",
  "established",
]);

// =====================================================================
// 认证
// =====================================================================
export type AuthKind =
  | "password"
  | "publickey"
  | "agent"
  | "keyboard-interactive";

export interface AuthSpec {
  kind: AuthKind;
  password?: string;
  keyId?: string;
  passphrase?: string;
}

// =====================================================================
// Profile（持久化的连接配置）
// =====================================================================
export interface Profile {
  id: ProfileID;
  name: string;
  group?: string;
  host: string;
  port: number;
  user: string;
  protocol: Protocol;
  /** 关联的凭据 ID（凭据内容存储在 secret store） */
  auth: {
    kind: AuthKind;
    keyId?: string;
    username?: string;
    command?: string;
  };
  env?: Record<string, string>;
  jumpVia?: string[];          // ProfileID[]
  tags?: string[];
  color?: string;
  icon?: string;
  createdAt: number;
  updatedAt: number;
}

// =====================================================================
// 运行时会话信息（来自后端 session.Info）
// =====================================================================
export interface SessionInfo {
  id: SessionID;
  name: string;
  host: string;
  port: number;
  user: string;
  protocol: Protocol;
  state: SessionState;
  createdAt: number;
  cols: number;
  rows: number;
}

// =====================================================================
// 打开会话请求（对应 session.OpenRequest）
// =====================================================================
export interface JumpHop {
  profileId: string;
}

export interface OpenRequest {
  profileId?: string;
  host: string;
  port: number;
  user: string;
  auth: AuthSpec;
  cols: number;
  rows: number;
  env?: Record<string, string>;
  jumpVia?: JumpHop[];
}

// =====================================================================
// PTY 几何
// =====================================================================
export interface PTYSize {
  cols: number;
  rows: number;
}
