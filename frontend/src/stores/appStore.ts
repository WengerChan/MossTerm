/**
 * 全局 App 状态
 * --------------------------------------------------------------------
 * 应用级数据：版本、平台、主题、初始化状态、致命错误等。
 * 与具体业务（session/tab/sftp）解耦。
 */
import { create } from "zustand";
import { logger } from "@utils/logger";

export type Theme = "dark" | "light" | "system";
export type Platform = "darwin" | "win32" | "linux" | "other";

export interface AppState {
  // ===== state =====
  initialized: boolean;
  version: string;
  platform: Platform;
  theme: Theme;
  /** 后端同步过来的全局错误（弹 Modal 用） */
  fatalError: string | null;

  // ===== actions =====
  init: () => Promise<void>;
  setTheme: (theme: Theme) => void;
  setFatalError: (msg: string | null) => void;
}

export const useAppStore = create<AppState>((set) => ({
  initialized: false,
  version: "0.1.0",
  platform: "other",
  theme: "dark",
  fatalError: null,

  init: async () => {
    if (useAppStore.getState().initialized) return;
    try {
      // TODO: 调用 wails backend 拉取环境信息
      // const env = await window.runtime.Environment();
      // set({ platform: env.platform as Platform });
      logger.info("[appStore] init done");
      set({ initialized: true });
    } catch (err: unknown) {
      logger.error(`[appStore] init failed: ${String(err)}`);
      set({ fatalError: String(err) });
    }
  },

  setTheme: (theme) => {
    // TODO: 同步到后端配置（持久化）
    set({ theme });
  },

  setFatalError: (msg) => set({ fatalError: msg }),
}));
