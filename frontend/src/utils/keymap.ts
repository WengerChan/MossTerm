/**
 * 键位映射工具
 * --------------------------------------------------------------------
 * 统一管理 MossTerm 的快捷键。
 * - 用 `CmdOrCtrl` 代替 Ctrl/⌘ 切换
 * - 用 `Shift+` `Alt+` 修饰
 * - 注册到 useShortcut hook
 *
 * 当前仅做配置 + 解析；具体的 keydown 绑定由 hook 完成。
 */

export type Modifier = "Ctrl" | "Alt" | "Shift" | "Meta" | "CmdOrCtrl";

/** 规范化后的快捷键字符串（按字典序排列修饰键，键名小写） */
export type ShortcutKey = string;

export interface ShortcutBinding {
  /** 显示给用户的标签，如 "Ctrl+Shift+P" */
  label: string;
  /** 规范化后用于匹配的键名 */
  key: ShortcutKey;
  /** 命令 id，关联到命令面板/菜单 */
  command: string;
  description?: string;
}

/**
 * 解析浏览器 KeyboardEvent → 规范化键名
 * 例：Ctrl+Shift+P → "ctrl+shift+p"
 */
export function parseKeyboardEvent(e: KeyboardEvent): ShortcutKey {
  const parts: string[] = [];
  if (e.ctrlKey)  parts.push("ctrl");
  if (e.metaKey)  parts.push("meta");
  if (e.altKey)   parts.push("alt");
  if (e.shiftKey) parts.push("shift");

  // 忽略单独的修饰键
  const k = e.key.toLowerCase();
  if (!["control", "shift", "alt", "meta"].includes(k)) {
    parts.push(k);
  }
  return parts.join("+");
}

/**
 * 将快捷键字符串格式化用于显示（首字母大写）
 *  - "ctrl+shift+p" → "Ctrl+Shift+P"
 *  - "ctrl+,"       → "Ctrl+,"
 */
export function formatShortcut(key: ShortcutKey): string {
  return key
    .split("+")
    .map((p) => {
      if (p.length === 1) return p.toUpperCase();
      return p.charAt(0).toUpperCase() + p.slice(1);
    })
    .join("+");
}

/**
 * 平台相关快捷键显示（macOS 用 ⌘，其它用 Ctrl）
 */
export function shortcutForDisplay(key: ShortcutKey, isMac: boolean): string {
  return key
    .split("+")
    .map((p) => {
      if (p === "cmdorctrl") return isMac ? "⌘" : "Ctrl";
      if (p.length === 1) return p.toUpperCase();
      return p.charAt(0).toUpperCase() + p.slice(1);
    })
    .join(isMac ? "" : "+");
}

/**
 * 内置快捷键清单
 * - key 使用 `cmdorctrl` 跨平台
 * - command 与命令面板的 id 对齐
 */
export const BUILTIN_SHORTCUTS: ReadonlyArray<ShortcutBinding> = [
  { key: "cmdorctrl+shift+p", command: "palette.open",   label: "Ctrl+Shift+P", description: "打开命令面板" },
  { key: "cmdorctrl+t",       command: "tab.new",        label: "Ctrl+T",       description: "新建终端 tab" },
  { key: "cmdorctrl+w",       command: "tab.close",      label: "Ctrl+W",       description: "关闭当前 tab" },
  { key: "cmdorctrl+shift+w", command: "session.close",  label: "Ctrl+Shift+W", description: "断开当前会话" },
  { key: "cmdorctrl+shift+c", command: "terminal.copy",  label: "Ctrl+Shift+C", description: "复制选中内容" },
  { key: "cmdorctrl+shift+v", command: "terminal.paste", label: "Ctrl+Shift+V", description: "粘贴剪贴板" },
  { key: "cmdorctrl+`",       command: "ui.toggleSidebar", label: "Ctrl+`",     description: "切换侧栏" },
  { key: "cmdorctrl+,",       command: "settings.open",  label: "Ctrl+,",       description: "打开设置" },
  { key: "f1",                command: "log.toggle",     label: "F1",           description: "显示 / 隐藏日志面板" },
];

export function findShortcut(command: string): ShortcutBinding | undefined {
  return BUILTIN_SHORTCUTS.find((b) => b.command === command);
}
