// =====================================================================
// Wails 自动生成占位文件
// ---------------------------------------------------------------------
// 该文件在运行 `wails dev` / `wails build` 时由 Wails CLI 重新生成。
// 不要手改；当前为骨架阶段，提供合理的类型让前端 import 不报错。
// =====================================================================

/**
 * Wails 注入的全局 `App` 类
 *
 * 真实情况下 Wails 会扫描 `internal/ui/wailsbindings/App` 上所有
 * 导出的方法，并生成同名函数。本占位声明只暴露 v0.1 用得到的最小集。
 */
export class App {
  // 会话管理
  static ListSessions(): Promise<import("@/types/session").SessionInfo[]>;
  static OpenSession(req: import("@/types/session").OpenRequest): Promise<import("@/types/session").SessionID>;
  static CloseSession(id: import("@/types/session").SessionID, force?: boolean): Promise<void>;
  static SendInput(id: import("@/types/session").SessionID, data: Uint8Array): Promise<void>;
  static ResizePTY(id: import("@/types/session").SessionID, cols: number, rows: number): Promise<void>;

  // 配置
  static GetConfig(): Promise<unknown>;
  static UpdateConfig(mutate: (cfg: unknown) => void): Promise<void>;

  // v0.5.5 Profile CRUD —— 接通 v0.1 留的后端 ListProfiles/SaveProfile/DeleteProfile。
  // 后端实现：internal/ui/wailsbindings/api.go: ListProfiles/SaveProfile/DeleteProfile。
  // 类型镜像 internal/config/manager.go 的 Profile 结构（前端 @types/session.ts）。
  // SaveProfile 走 ID 区分 add/update：p.id 空 → AddProfile；非空 → UpdateProfile。
  // deleteProfile 无返回值，错误经 try/catch 进 error state。
  static ListProfiles(): Promise<import("@/types/session").Profile[]>;
  static SaveProfile(p: import("@/types/session").Profile): Promise<void>;
  static DeleteProfile(id: import("@/types/session").ProfileID): Promise<void>;

  // 凭据
  static ListSecrets(): Promise<unknown[]>;
  static GetSecretContent(id: string): Promise<string>;
  static SaveSecret(name: string, kind: string, content: string): Promise<string>;
  static DeleteSecret(id: string): Promise<void>;

  // v0.5.0 First-Use Trust：前端弹窗收到 "knownhosts:trust-request" 事件后，
  // 用户点 trust/reject → 调 TrustHost(id, action) 通知后端。
  // action: "trust" | "reject" | 其他（视作 reject）。
  static TrustHost(requestID: string, action: string): Promise<void>;

  // v0.5.1 SFTP 浏览器：wailsbindings 暴露给前端的 SFTP 操作。
  // 后端 SftpList 当前一次性返回全量（pkg/sftp 的 ReadDir 不分页）；
  // pageSize / pageToken 参数保留，v0.5.1+ 接真实分页时签名不变。
  //
  // 类型镜像 internal/sftpclient/{Entry,ListPage}：
  //   - time.Time 经 Wails 序列化为 RFC3339 字符串
  //   - os.FileMode (uint32) 经 Wails 序列化为 number
  //   - []byte 经 Wails 序列化为 Uint8Array
  static SftpList(sessionID: string, path: string, pageSize: number, pageToken: string): Promise<ListPage>;
  static SftpStat(sessionID: string, path: string): Promise<Entry>;
  static SftpMkdir(sessionID: string, path: string): Promise<void>;
  static SftpRemove(sessionID: string, path: string): Promise<void>;
  static SftpRename(sessionID: string, oldPath: string, newPath: string): Promise<void>;
  static SftpRead(sessionID: string, path: string): Promise<Uint8Array>;
  static SftpWrite(sessionID: string, path: string, data: Uint8Array): Promise<void>;

  // v0.5.3 SFTP drag-drop 上传：把前端 Uint8Array 一次性写到远端。
  // 与 SftpWrite 的区别：返回写入字节数（前端可以做更详细的反馈），
  // 专属 path 给 SftpBrowser 的 drag-drop handler 用。
  // 限制：v0.5.3 不分片，前端应先校验大小（推荐 ≤ 100 MiB），
  // 否则一次性 readAsArrayBuffer 会卡 UI。
  static SftpUploadFile(sessionID: string, remotePath: string, content: Uint8Array): Promise<number>;

  // v0.5.9 SFTP 文件预览：图片 / PDF / 文本 / 二进制 / 超大文件分支。
  // 后端硬上限 PreviewMaxBytes = 50 MiB（前端 PreviewPanel 用作 belt-and-suspenders 校验）。
  // 与既有 SftpStat/SftpRead 互补：SftpRead 1 MiB hard cap + 恒 offset 0；本组是"任意 offset + 50 MiB"。
  // 类型镜像 internal/sftpclient/preview.go::PreviewMetadata struct。
  static SftpReadFileChunk(sessionID: string, path: string, offset: number, size: number): Promise<Uint8Array>;
  static SftpStatFile(sessionID: string, path: string): Promise<PreviewMetadata>;
  static SftpGetFileMetadata(sessionID: string, path: string): Promise<PreviewMetadata>;

  // v0.5.10 SFTP streaming upload：分片 + 进度 + 断点续传。
  // 后端实现：internal/transfer/{streaming,manifest,manager}.go + internal/ui/wailsbindings/api.go。
  // 4 个 binding：StartUpload (启动后台 goroutine，返回 transferID) / CancelUpload (ctx cancel)
  // / ListTransfers (看全部 active + 已结束) / GetTransfer (单个详情)。
  // 进度通过 Wails runtime EventsEmit 推 3 个事件（前端 EventsOn 订阅）：
  //   - "transfer:progress" → UploadProgress payload（节流 200ms）
  //   - "transfer:done"     → UploadJobInfo payload（State=Completed）
  //   - "transfer:error"    → UploadJobInfo payload（State=Failed / Canceled）
  // 大文件保护：> 10 GiB 拒绝（OOM + 远端磁盘风险）；不缓冲整文件（io.SectionReader + WriteAt）。
  // sessionID 通过 Wails ctx 注入（req 字段不携带 sessionID）。
  static StartUpload(req: UploadRequest): Promise<string>;
  static CancelUpload(transferID: string): Promise<void>;
  static ListTransfers(): Promise<UploadJobInfo[]>;
  static GetTransfer(transferID: string): Promise<UploadJobInfo | null>;
}

/** v0.5.10 SFTP streaming upload 请求（与 internal/transfer/streaming.go::UploadRequest 镜像）。 */
export interface UploadRequest {
  /** transfer ID（可空 → 自动生成）；Resume 模式必须传原 ID 续传 */
  transferID: string;
  /** v0.5.10 必传：sftp 连接的 session ID；wailsbinding 注入 ctx */
  sessionID: string;
  /** 本地文件绝对路径 */
  localPath: string;
  /** 远端绝对路径（含文件名） */
  remotePath: string;
  /** 分片字节数；0 = 4 MiB；范围 [1 MiB, 16 MiB] */
  chunkSize?: number;
  /** 并发 worker 数；0 = 2；范围 [1, 4] */
  concurrency?: number;
  /** true = 接续 manifest；false = 忽略旧 manifest 重新传 */
  resume: boolean;
}

/** v0.5.10 SFTP streaming upload 进度（与 internal/transfer/streaming.go::Progress 镜像）。 */
export interface UploadProgress {
  transferID: string;
  bytesSent: number;
  totalBytes: number;
  /** 瞬时速度 B/s（从 Upload 起到当前） */
  speedBps: number;
  /** 剩余秒数；-1 表示速度未知 */
  etaSec: number;
  /** 当前正在上传的 chunk 索引；-1 = 不属于某片（节流 emit 的进度） */
  chunkIndex: number;
  /** 总 chunk 数 */
  totalChunks: number;
}

/** v0.5.10 SFTP streaming upload 任务信息（与 internal/transfer/manager.go::JobInfo 镜像）。 */
export interface UploadJobInfo {
  transferID: string;
  localPath: string;
  remotePath: string;
  totalBytes: number;
  bytesSent: number;
  state: "running" | "completed" | "failed" | "canceled";
  error?: string;
  chunkSize: number;
  concurrency: number;
  startedAt: string;
  updatedAt: string;
  /** 传输完成后的 SHA-256（"sha256:<hex>"）；运行中为空 */
  checksum?: string;
}

/** v0.5.9 SFTP 文件预览元信息（与 internal/sftpclient/preview.go::PreviewMetadata 镜像）。 */
export interface PreviewMetadata {
  /** 远端绝对路径 */
  path: string;
  /** 文件名（不带路径） */
  name: string;
  /** 字节数（目录无意义） */
  size: number;
  /** 文件权限位（unix mode，uint32 序列化为 number） */
  mode: number;
  /** 修改时间 RFC3339 字符串（由 Go time.Time 序列化） */
  modTime: string;
  /** 由 net/http.DetectContentType 探测（前 512 字节 magic 库） */
  mimeType: string;
  /**
   * 前端路由 kind：
   *   "image"    jpg/png/gif/webp/svg（magic 或 image/* mime）
   *   "pdf"      application/pdf（magic 或 mime）
   *   "text"     text/* 或 application/json 或 文本扩展名白名单
   *   "binary"   其他二进制 / 超 5 MiB 文本阈值
   *   "toolarge" > 50 MiB hard cap（禁止读字节）
   *   ""         SftpStatFile lite 入口不分类
   */
  kind: "image" | "pdf" | "text" | "binary" | "toolarge" | "";
  /** 扩展名（小写，不含 .） */
  ext: string;
  /** 便利字段（Kind === "image"） */
  isImage: boolean;
  /** 便利字段（Kind === "pdf"） */
  isPDF: boolean;
  /** 便利字段（Kind === "text"） */
  isText: boolean;
}

// =====================================================================
// SFTP DTO（v0.5.1 前端类型）
// ---------------------------------------------------------------------
// 与 internal/sftpclient/client.go 的 Entry / ListPage 镜像。
// v0.5.1 不与 @types/sftp.ts（旧 stub）混用，避免与既有的 SftpEntry
// 类型（带 type: 'file'|'dir'|'symlink' 联合）混淆。
// =====================================================================

/** 远端文件/目录条目。 */
export interface Entry {
  /** 文件名（不含路径） */
  name: string;
  /** 完整路径 */
  path: string;
  /** 字节数（目录时无意义） */
  size: number;
  /** 文件权限位（unix mode，例如 0o755） */
  mode: number;
  /** 修改时间，RFC3339 字符串（由 Go time.Time 序列化） */
  modTime: string;
  /** 是否为目录 */
  isDir: boolean;
  /** 是否为符号链接 */
  isSymlink: boolean;
  /** 符号链接目标（v0.5.0 暂未填充，保持空串） */
  link: string;
}

/** 大目录分页结果。 */
export interface ListPage {
  entries: Entry[];
  /** 下一页 token；空串 = 已到底（v0.5.1 一次性返回时恒为空） */
  nextToken: string;
}

export default App;
