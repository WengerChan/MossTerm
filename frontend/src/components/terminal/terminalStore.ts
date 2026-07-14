/**
 * 终端 store
 * --------------------------------------------------------------------
 * 管理每个 session 对应的 xterm.js 配置 + 几何尺寸。
 * 注意：xterm.js 实例本身**不**放进 store（避免 React 频繁 re-render），
 * 它由 Terminal.tsx 用 ref 持有，store 只保存可序列化的元数据。
 */
import { create } from "zustand";
import type { SessionID, PTYSize } from "@/types/session";

export type TerminalTheme = "moss-dark" | "moss-light" | "solarized-dark";

export interface TerminalConfig {
  fontSize: number;
  fontFamily: string;
  cursorBlink: boolean;
  cursorStyle: "block" | "underline" | "bar";
  scrollback: number;
  theme: TerminalTheme;
  /** 鼠标选中自动复制 */
  copyOnSelect: boolean;
  /** 启用 WebGL 渲染 */
  webgl: boolean;
}

export interface TerminalMeta {
  size: PTYSize;
  /** 终端实例是否已 attach 到 DOM */
  attached: boolean;
  /** 最近一次清屏时间 */
  lastClearAt: number | null;
}

interface TerminalState {
  /** session -> config */
  configs: Record<SessionID, TerminalConfig>;
  /** session -> meta */
  metas:   Record<SessionID, TerminalMeta>;

  // ===== actions =====
  ensureConfig: (id: SessionID) => TerminalConfig;
  updateConfig:  (id: SessionID, patch: Partial<TerminalConfig>) => void;
  setSize:       (id: SessionID, size: PTYSize) => void;
  markAttached:  (id: SessionID, attached: boolean) => void;
  markCleared:   (id: SessionID) => void;
  forget:        (id: SessionID) => void;
}

const DEFAULT_CONFIG: TerminalConfig = {
  fontSize: 14,
  fontFamily: "JetBrains Mono, Menlo, Consolas, monospace",
  cursorBlink: true,
  cursorStyle: "block",
  scrollback: 10000,
  theme: "moss-dark",
  copyOnSelect: true,
  webgl: true,
};

const DEFAULT_META: TerminalMeta = {
  size: { cols: 80, rows: 24 },
  attached: false,
  lastClearAt: null,
};

export const useTerminalStore = create<TerminalState>((set) => ({
  configs: {},
  metas: {},

  ensureConfig: (id) => {
    // v0.5.7: 用 set 的 get() 闭包代替 useTerminalStore.getState()，避免
    // create<T>() 返回类型在自引用时推不出（TS7022 / TS7023）。
    let cfg: TerminalConfig | undefined;
    set((s) => {
      cfg = s.configs[id];
      if (!cfg) {
        cfg = DEFAULT_CONFIG;
        return { configs: { ...s.configs, [id]: cfg } };
      }
      return {};
    });
    return cfg!;
  },

  updateConfig: (id, patch) =>
    set((s) => {
      const cur = s.configs[id] ?? DEFAULT_CONFIG;
      return { configs: { ...s.configs, [id]: { ...cur, ...patch } } };
    }),

  setSize: (id, size) =>
    set((s) => {
      const cur = s.metas[id] ?? DEFAULT_META;
      return { metas: { ...s.metas, [id]: { ...cur, size } } };
    }),

  markAttached: (id, attached) =>
    set((s) => {
      const cur = s.metas[id] ?? DEFAULT_META;
      return { metas: { ...s.metas, [id]: { ...cur, attached } } };
    }),

  markCleared: (id) =>
    set((s) => {
      const cur = s.metas[id] ?? DEFAULT_META;
      return { metas: { ...s.metas, [id]: { ...cur, lastClearAt: Date.now() } } };
    }),

  forget: (id) =>
    set((s) => {
      const configs = { ...s.configs }; delete configs[id];
      const metas   = { ...s.metas   }; delete metas[id];
      return { configs, metas };
    }),
}));
