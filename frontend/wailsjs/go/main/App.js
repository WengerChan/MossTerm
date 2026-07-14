// =====================================================================
// Wails App binding —— 浏览器侧 wrapper
// ---------------------------------------------------------------------
// v0.5.7：手写 wailsbindings bridge。
//
// 背景：v0.5.0 之前 wailsjs/go/main/App.d.ts 是手写占位（`App` 是
// class with static methods 的类型），源里所有地方都用 `App.ListProfiles()`
// 调用。`App.js` 从来没真正存在 —— `npm run build`（vite）一直因找不到
// `App.js` 失败，但 `wails build` 不走 vite，所以 7.64 MB 二进制没影响。
//
// v0.5.7 决定让 CI 真正绿，方案：
//   - 保留手写 `App.d.ts`（`App` class with static methods）+ `WailsRuntime`
//   - 写本文件 `App.js`，把每个 `App.Xxx(...)` 调用转发到 wails runtime：
//       window.go.wailsbindings.App.Xxx(arg1, arg2, ...)
//   - 不再依赖 `wails generate module`（v0.5.6 试过，输出路径/风格变了，
//     要全量迁移前端，工作量 ≫ v0.5.7 范围）
//   - 不影响 `wails build`（build job 不走 vite 也不走本文件）
//
// 设计：保持 `App.d.ts` 的静态方法签名（无 `context.Context` 显式参数），
// 桥接到 runtime 时直接把调用方传的 args 透传给 wails 暴露的函数。
// wails v2.12.0 runtime 会自动注入 context（与 `wailsbindings/bindings.go`
// 里 `func (a *App) Xxx(ctx context.Context, ...)` 的 ctx 匹配）。
// =====================================================================

/**
 * 取得 wails runtime 暴露的真正 Go 绑定对象。
 * - 开发模式（vite dev）：window.go 不存在，返回 null，调用方应 try/catch
 * - wails build 后：window.go.wailsbindings.App 是 Go 端的 *wailsbindings.App
 */
function getGoApp() {
  if (typeof window === "undefined") return null;
  const go = window.go;
  if (!go || !go.wailsbindings || !go.wailsbindings.App) return null;
  return go.wailsbindings.App;
}

export class App {
  // —— 会话管理 ——
  static ListSessions() {
    const a = getGoApp();
    if (!a) return Promise.resolve([]);
    return a.ListSessions();
  }

  static OpenSession(req) {
    const a = getGoApp();
    if (!a) return Promise.reject(new Error("Wails runtime not available"));
    return a.OpenSession(req);
  }

  static CloseSession(id, force) {
    const a = getGoApp();
    if (!a) return Promise.resolve();
    return a.CloseSession(id, force ?? false);
  }

  static SendInput(id, data) {
    const a = getGoApp();
    if (!a) return Promise.resolve();
    return a.SendInput(id, data);
  }

  static ResizePTY(id, cols, rows) {
    const a = getGoApp();
    if (!a) return Promise.resolve();
    return a.ResizePTY(id, cols, rows);
  }

  // —— 配置 ——
  static GetConfig() {
    const a = getGoApp();
    if (!a) return Promise.resolve({});
    return a.GetConfig();
  }

  static UpdateConfig(mutate) {
    const a = getGoApp();
    if (!a) return Promise.resolve();
    return a.UpdateConfig(mutate);
  }

  // —— v0.5.5 Profile CRUD ——
  static ListProfiles() {
    const a = getGoApp();
    if (!a) return Promise.resolve([]);
    return a.ListProfiles();
  }

  static SaveProfile(p) {
    const a = getGoApp();
    if (!a) return Promise.reject(new Error("Wails runtime not available"));
    return a.SaveProfile(p);
  }

  static DeleteProfile(id) {
    const a = getGoApp();
    if (!a) return Promise.resolve();
    return a.DeleteProfile(id);
  }

  // —— 凭据 ——
  static ListSecrets() {
    const a = getGoApp();
    if (!a) return Promise.resolve([]);
    return a.ListSecrets();
  }

  static GetSecretContent(id) {
    const a = getGoApp();
    if (!a) return Promise.reject(new Error("Wails runtime not available"));
    return a.GetSecretContent(id);
  }

  static SaveSecret(name, kind, content) {
    const a = getGoApp();
    if (!a) return Promise.reject(new Error("Wails runtime not available"));
    return a.SaveSecret(name, kind, content);
  }

  static DeleteSecret(id) {
    const a = getGoApp();
    if (!a) return Promise.resolve();
    return a.DeleteSecret(id);
  }

  // —— v0.5.0 First-Use Trust ——
  static TrustHost(requestID, action) {
    const a = getGoApp();
    if (!a) return Promise.resolve();
    return a.TrustHost(requestID, action);
  }

  // —— v0.5.1 SFTP 浏览器 ——
  static SftpList(sessionID, path, pageSize, pageToken) {
    const a = getGoApp();
    if (!a) return Promise.resolve({ entries: [], nextToken: "" });
    return a.SftpList(sessionID, path, pageSize, pageToken);
  }

  static SftpStat(sessionID, path) {
    const a = getGoApp();
    if (!a) return Promise.reject(new Error("Wails runtime not available"));
    return a.SftpStat(sessionID, path);
  }

  static SftpMkdir(sessionID, path) {
    const a = getGoApp();
    if (!a) return Promise.resolve();
    return a.SftpMkdir(sessionID, path);
  }

  static SftpRemove(sessionID, path) {
    const a = getGoApp();
    if (!a) return Promise.resolve();
    return a.SftpRemove(sessionID, path);
  }

  static SftpRename(sessionID, oldPath, newPath) {
    const a = getGoApp();
    if (!a) return Promise.resolve();
    return a.SftpRename(sessionID, oldPath, newPath);
  }

  static SftpRead(sessionID, path) {
    const a = getGoApp();
    if (!a) return Promise.reject(new Error("Wails runtime not available"));
    return a.SftpRead(sessionID, path);
  }

  static SftpWrite(sessionID, path, data) {
    const a = getGoApp();
    if (!a) return Promise.resolve();
    return a.SftpWrite(sessionID, path, data);
  }

  // —— v0.5.3 SFTP drag-drop 上传 ——
  static SftpUploadFile(sessionID, remotePath, content) {
    const a = getGoApp();
    if (!a) return Promise.reject(new Error("Wails runtime not available"));
    return a.SftpUploadFile(sessionID, remotePath, content);
  }
}

export default App;
