// =====================================================================
// 后端 bindings 占位
// ---------------------------------------------------------------------
// 真实情况：Wails 扫描 `internal/ui/wailsbindings/*.go` 中 Bind() 注册的
// 所有类型，并按包路径生成 wailsjs/go/<pkg>/<Type>.d.ts。
// 这里放一个聚合入口，约定项目统一通过 `import { Api } from "@wails/..."`。
// =====================================================================

export type { SessionInfo, OpenRequest, SessionID, AuthSpec, AuthKind } from "@types/session";
export type { SftpEntry, SftpListPage } from "@types/sftp";

/**
 * 预留的 Api 命名空间 —— 真实生成后会被同名类替换。
 * 现阶段为类型占位，方便 IDE 自动补全和编译通过。
 */
export class Api {
  // TODO: wails generate 后会填充方法签名
}
export default Api;
