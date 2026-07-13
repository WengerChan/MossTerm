/**
 * useShortcut
 * --------------------------------------------------------------------
 * 绑定全局键盘快捷键。
 * - 支持 `cmdorctrl` 跨平台修饰键
 * - 自动在 component unmount 时清理
 * - 提供 preventDefault 选项
 */
import { useEffect, useRef } from "react";
import { parseKeyboardEvent } from "@utils/keymap";

export interface ShortcutOptions {
  /** 全局快捷键字符串，如 "cmdorctrl+shift+p" */
  key: string;
  /** 触发回调 */
  handler: (e: KeyboardEvent) => void;
  /** 是否调用 e.preventDefault()（默认 true） */
  preventDefault?: boolean;
  /** 是否忽略 input/textarea/contenteditable 中触发的按键（默认 true） */
  ignoreEditable?: boolean;
  /** 是否仅在 webview 焦点状态下生效（默认 true） */
  requireFocus?: boolean;
}

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName.toLowerCase();
  return tag === "input" || tag === "textarea" || tag === "select";
}

function isMacPlatform(): boolean {
  if (typeof navigator === "undefined") return false;
  return /Mac|iPhone|iPad/i.test(navigator.platform);
}

/**
 * 将 "cmdorctrl" 替换为当前平台实际修饰符
 */
function expandShortcut(key: string, isMac: boolean): string {
  const targetMod = isMac ? "meta" : "ctrl";
  return key
    .split("+")
    .map((p) => (p === "cmdorctrl" ? targetMod : p))
    .join("+");
}

export function useShortcut(opts: ShortcutOptions): void {
  const {
    key,
    handler,
    preventDefault = true,
    ignoreEditable = true,
    requireFocus = true,
  } = opts;

  const handlerRef = useRef(handler);
  handlerRef.current = handler;

  useEffect(() => {
    const targetKey = expandShortcut(key, isMacPlatform());

    const onKeyDown = (e: KeyboardEvent): void => {
      if (requireFocus && !(e.target instanceof Element)) return;
      if (ignoreEditable && isEditableTarget(e.target)) return;
      const parsed = parseKeyboardEvent(e);
      if (parsed === targetKey) {
        if (preventDefault) e.preventDefault();
        handlerRef.current(e);
      }
    };

    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [key, preventDefault, ignoreEditable, requireFocus]);
}
