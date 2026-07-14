/**
 * SFTP 浏览器 —— 全局开关
 * --------------------------------------------------------------------
 * v0.5.1 范围：单实例 SFTP 浏览器。
 *
 * 设计要点：
 *   - 用 Zustand 全局 store 而非 component state —— 工具栏（TabBar）
 *     和浏览器本身在不同子树，store 是最轻量的桥梁
 *   - v0.5.1 **不**支持多开（一个 session 一个浏览器）
 *     多次 open(sid) 会覆盖当前 sessionID；用户体感 = "切到那个 session 的目录"
 *   - 关闭时清空 sessionID —— 防止"关掉之后再开一个错 session"的脏状态
 *
 * 与既有 store 风格对齐：
 *   - `tabsStore` (create + subscribeWithSelector)
 *   - `sftpStore` (create only)
 *   - 这里不需要 subscribeWithSelector —— SftpBrowser 自己消费就够
 *     不需要外部 selector 跨 store 联动
 */
import { create } from "zustand";
import type { SessionID } from "@/types/session";

export interface SftpBrowserState {
  isOpen: boolean;
  sessionID: SessionID | null;

  /** 打开浏览器，绑定到指定 session。重复调用会覆盖（v0.5.1 单实例）。 */
  open: (sessionID: SessionID) => void;
  /** 关闭浏览器并清空 sessionID。 */
  close: () => void;
}

export const useSftpBrowserStore = create<SftpBrowserState>((set) => ({
  isOpen: false,
  sessionID: null,

  open: (sessionID) => set({ isOpen: true, sessionID }),
  close: () => set({ isOpen: false, sessionID: null }),
}));
