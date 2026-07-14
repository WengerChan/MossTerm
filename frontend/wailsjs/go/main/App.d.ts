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
  static ListSessions(): Promise<import("@types/session").SessionInfo[]>;
  static OpenSession(req: import("@types/session").OpenRequest): Promise<import("@types/session").SessionID>;
  static CloseSession(id: import("@types/session").SessionID, force?: boolean): Promise<void>;
  static SendInput(id: import("@types/session").SessionID, data: Uint8Array): Promise<void>;
  static ResizePTY(id: import("@types/session").SessionID, cols: number, rows: number): Promise<void>;

  // 配置
  static GetConfig(): Promise<unknown>;
  static UpdateConfig(mutate: (cfg: unknown) => void): Promise<void>;

  // 凭据
  static ListSecrets(): Promise<unknown[]>;
  static GetSecretContent(id: string): Promise<string>;
  static SaveSecret(name: string, kind: string, content: string): Promise<string>;
  static DeleteSecret(id: string): Promise<void>;

  // v0.5.0 First-Use Trust：前端弹窗收到 "knownhosts:trust-request" 事件后，
  // 用户点 trust/reject → 调 TrustHost(id, action) 通知后端。
  // action: "trust" | "reject" | 其他（视作 reject）。
  static TrustHost(requestID: string, action: string): Promise<void>;
}

export default App;
