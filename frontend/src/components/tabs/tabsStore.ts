/**
 * Tab 状态
 * --------------------------------------------------------------------
 * v0.1 不启用多 tab，但 store/UI 框架先就位（参考 ARCHITECTURE 7.2）。
 */
import { create } from "zustand";
import type { SessionID } from "@types/session";

export interface TabItem {
  id: string;              // tab 自身 id
  sessionId: SessionID;
  title: string;
  closable: boolean;
  /** 自定义图标（lucide-react 名） */
  icon?: string;
}

interface TabsState {
  tabs: TabItem[];
  activeTabId: string | null;

  // ===== actions =====
  addTab: (item: Omit<TabItem, "id">) => string;
  removeTab: (id: string) => void;
  setActive: (id: string) => void;
  updateTitle: (id: string, title: string) => void;
  reorder: (fromId: string, toId: string) => void;
}

let tabSeq = 0;
const nextTabId = (): string => {
  tabSeq += 1;
  return `tab-${Date.now().toString(36)}-${tabSeq}`;
};

export const useTabsStore = create<TabsState>((set, get) => ({
  tabs: [],
  activeTabId: null,

  addTab: (item) => {
    const id = nextTabId();
    set((s) => ({ tabs: [...s.tabs, { ...item, id }], activeTabId: id }));
    return id;
  },

  removeTab: (id) => {
    set((s) => {
      const tabs = s.tabs.filter((t) => t.id !== id);
      let activeTabId = s.activeTabId;
      if (activeTabId === id) {
        activeTabId = tabs.length > 0 ? tabs[tabs.length - 1]!.id : null;
      }
      return { tabs, activeTabId };
    });
  },

  setActive: (id) => set({ activeTabId: id }),

  updateTitle: (id, title) =>
    set((s) => ({
      tabs: s.tabs.map((t) => (t.id === id ? { ...t, title } : t)),
    })),

  reorder: (fromId, toId) => {
    const tabs = [...get().tabs];
    const fromIdx = tabs.findIndex((t) => t.id === fromId);
    const toIdx   = tabs.findIndex((t) => t.id === toId);
    if (fromIdx < 0 || toIdx < 0) return;
    const [moved] = tabs.splice(fromIdx, 1);
    if (moved) tabs.splice(toIdx, 0, moved);
    set({ tabs });
  },
}));
