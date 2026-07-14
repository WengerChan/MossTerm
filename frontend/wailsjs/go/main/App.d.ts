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
