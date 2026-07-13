/**
 * UI 状态
 * --------------------------------------------------------------------
 * 命令面板可见性、侧栏 / 状态栏折叠、模态框、Toast 等。
 * 与具体业务解耦。
 */
import { create } from "zustand";

export interface ToastItem {
  id: string;
  level: "info" | "success" | "warn" | "error";
  message: string;
  /** 自动消失毫秒数，0 = 不自动消失 */
  durationMs: number;
  createdAt: number;
}

export interface ModalSpec {
  id: string;
  title: string;
  /** 内容交给具体组件渲染，store 只持有元信息 */
  componentKey: string;
  /** 透传给组件的参数 */
  props?: Record<string, unknown>;
}

export interface UIState {
  // ===== 面板 =====
  sidebarVisible: boolean;
  statusBarVisible: boolean;
  logPanelVisible: boolean;
  sftpPanelVisible: boolean;
  commandPaletteOpen: boolean;

  // ===== 模态与提示 =====
  modal: ModalSpec | null;
  toasts: ToastItem[];

  // ===== actions =====
  toggleSidebar: () => void;
  toggleStatusBar: () => void;
  toggleLogPanel: () => void;
  toggleSftpPanel: () => void;
  openCommandPalette: () => void;
  closeCommandPalette: () => void;

  openModal: (spec: ModalSpec) => void;
  closeModal: () => void;

  pushToast: (toast: Omit<ToastItem, "id" | "createdAt">) => string;
  dismissToast: (id: string) => void;
}

let toastSeq = 0;
const nextToastId = (): string => {
  toastSeq += 1;
  return `toast-${Date.now().toString(36)}-${toastSeq}`;
};

export const useUIStore = create<UIState>((set, get) => ({
  sidebarVisible: true,
  statusBarVisible: true,
  logPanelVisible: false,
  sftpPanelVisible: false,
  commandPaletteOpen: false,

  modal: null,
  toasts: [],

  toggleSidebar:     () => set((s) => ({ sidebarVisible: !s.sidebarVisible })),
  toggleStatusBar:   () => set((s) => ({ statusBarVisible: !s.statusBarVisible })),
  toggleLogPanel:    () => set((s) => ({ logPanelVisible: !s.logPanelVisible })),
  toggleSftpPanel:   () => set((s) => ({ sftpPanelVisible: !s.sftpPanelVisible })),
  openCommandPalette:  () => set({ commandPaletteOpen: true }),
  closeCommandPalette: () => set({ commandPaletteOpen: false }),

  openModal: (spec) => set({ modal: spec }),
  closeModal: () => set({ modal: null }),

  pushToast: (toast) => {
    const id = nextToastId();
    const item: ToastItem = {
      ...toast,
      id,
      createdAt: Date.now(),
    };
    set((s) => ({ toasts: [...s.toasts, item] }));
    if (item.durationMs > 0) {
      setTimeout(() => {
        // 重新读 state，避免提前 dismiss
        if (get().toasts.find((t) => t.id === id)) {
          get().dismissToast(id);
        }
      }, item.durationMs);
    }
    return id;
  },

  dismissToast: (id) =>
    set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),
}));
