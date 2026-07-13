/**
 * SFTP 相关 DTO
 * --------------------------------------------------------------------
 * 与 `internal/sftpclient/` 的 Entry / ListPage 镜像。
 * v0.1 阶段前端仅占位，v0.2+ 启用 SFTP 面板时使用。
 */

export type SftpEntryType = "file" | "dir" | "symlink" | "other";

export interface SftpEntry {
  /** 文件名（不含路径） */
  name: string;
  /** 完整路径 */
  path: string;
  /** 字节数（目录为 0） */
  size: number;
  /** 文件权限（八进制，例如 0o755） */
  mode: number;
  /** 修改时间（Unix epoch ms） */
  modTime: number;
  /** 类型 */
  type: SftpEntryType;
  /** 符号链接目标（type==='symlink' 时有效） */
  linkTarget?: string;
}

export interface SftpListPage {
  entries: SftpEntry[];
  /** 下一页 token，空字符串代表到底 */
  nextToken: string;
}

export type TransferDirection = "upload" | "download";

export type TransferState =
  | "queued"
  | "running"
  | "paused"
  | "completed"
  | "failed"
  | "canceled";

export interface TransferJob {
  id: string;
  direction: TransferDirection;
  localPath: string;
  remotePath: string;
  size: number;
  transferred: number;
  speed: number;          // bytes/s
  state: TransferState;
  error?: string;
  startedAt: number;
  eta?: number;           // seconds
}

export interface TransferProgressEvent {
  jobId: string;
  transferred: number;
  speed: number;
  eta: number;
}
