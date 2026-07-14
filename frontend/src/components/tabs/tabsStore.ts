/**
 * Tabs + Split-Pane 状态
 * --------------------------------------------------------------------
 * v0.5.0 B 引入。每个 Tab 持有一棵 Pane 树：
 *
 *   Tab.panes: Pane[]                ─ 顶级 root（一般 1 个；保留数组为后续 detached pane 留口）
 *     └─ Pane: { split: null, ... }  ─ leaf，挂载 Terminal
 *     └─ Pane: { split: 'h' | 'v', children: [Pane, Pane] } ─ split 节点
 *
 * 设计要点：
 *   - split 节点固定 2 个 child（size 50/50；v0.5 不做拖拽改 size）
 *   - 关闭一个 leaf：其父 split 自动 collapse 为剩余的 child
 *   - 关闭一个 split 节点：lift 它的 children 到上一层
 *   - 关闭 tab 内最后一个 leaf：整个 tab 被关闭
 *   - pane id 用 crypto.randomUUID() 生成；split 后子节点重新生成
 *
 * 与 SessionStore（connectionStore）的关系：
 *   - tabsStore 管 UI 树（layout/state）
 *   - connectionStore 管真实 session 生命周期（profiles/activeSessionId）
 *   - 两者通过 pane.sessionId 关联；session 关闭时由 TerminalView/SessionTree
 *     负责把对应 pane 标 closed
 */
import { create } from "zustand";
import { subscribeWithSelector } from "zustand/middleware";
import type { SessionID, ProfileID } from "@/types/session";

// =====================================================================
// Pane 树类型
// =====================================================================
export type PaneSplitDirection = "horizontal" | "vertical";

export interface Pane {
  id: string;
  /**
   * split 方向；null = leaf（挂载 Terminal）。
   * leaf 时 sessionId 才有意义；split 节点的 sessionId 始终为 null。
   */
  split: PaneSplitDirection | null;
  /** split 节点的子 pane 列表（固定 2 个） */
  children: Pane[];
  /**
   * 0-100 百分比。当前 v0.5 固定 50/50，保留字段为拖拽 resize 留口。
   */
  size: number;
  /** leaf pane 关联的 session id；split 节点为 null */
  sessionId: SessionID | null;
}

// =====================================================================
// Tab 类型
// =====================================================================
export type TabState =
  | "idle"
  | "connecting"
  | "authenticating"
  | "established"
  | "closed"
  | "failed";

export interface Tab {
  id: string;
  title: string;
  /** 后端 session id（Open 后填充） */
  sessionId: SessionID | null;
  profileId: ProfileID | null;
  host: string;
  state: TabState;
  /** pane 树根列表（v0.5 固定 length=1） */
  panes: Pane[];
  /** 当前激活的 leaf pane */
  activePaneId: string;
}

// =====================================================================
// Store
// =====================================================================
interface TabsState {
  tabs: Tab[];
  activeTabId: string | null;

  // ===== Tab actions =====
  /**
   * 新建一个 tab，自动创建一个空 leaf pane。
   * 返回新 tab id（方便 caller 立即设为 active / 关联 profile）。
   */
  addTab: (tab: Omit<Tab, "id" | "panes" | "activePaneId">) => string;
  removeTab: (id: string) => void;
  setActiveTab: (id: string) => void;
  updateTab: (id: string, patch: Partial<Tab>) => void;

  // ===== Pane actions =====
  /**
   * 把 paneId 对应的 leaf 拆成 split 方向。原始 session 保留在第一个 child。
   * 若目标不是 leaf（已为 split 节点）则 no-op。
   */
  splitPane: (
    tabId: string,
    paneId: string,
    direction: PaneSplitDirection,
  ) => void;
  /**
   * 关闭 paneId：
   *   - 若是 tab 内唯一 leaf：整个 tab 关闭
   *   - 若是 split 节点的子 leaf：父 split 自动 collapse
   *   - 若是 split 节点：lift 它的 children
   */
  closePane: (tabId: string, paneId: string) => void;
  setActivePane: (tabId: string, paneId: string) => void;
}

// =====================================================================
// helpers
// =====================================================================
const newPaneId = (): string => crypto.randomUUID();

/**
 * 在 panes 树中把 targetId 对应的 leaf 转成 split 节点。
 * 第一个 child 继承原 sessionId；第二个 child sessionId 为 null。
 */
function splitPaneInTree(
  panes: Pane[],
  targetId: string,
  direction: PaneSplitDirection,
): Pane[] {
  return panes.map((p) => {
    if (p.id === targetId) {
      // 已为 split 节点：no-op（split 不可再 split）
      if (p.split !== null) return p;
      return {
        id: p.id, // split 节点复用 leaf id；新的两个 child 重新分配
        split: direction,
        children: [
          {
            id: newPaneId(),
            split: null,
            children: [],
            size: 50,
            sessionId: p.sessionId,
          },
          {
            id: newPaneId(),
            split: null,
            children: [],
            size: 50,
            sessionId: null,
          },
        ],
        size: 100,
        sessionId: null,
      };
    }
    if (p.split !== null) {
      return { ...p, children: splitPaneInTree(p.children, targetId, direction) };
    }
    return p;
  });
}

/**
 * 关闭一个 pane 并保持树形约束：
 *   - target 是 root 且是唯一 leaf → return null（外部关闭整个 tab）
 *   - target 是 leaf 且其父 split 剩 1 个 child → 父 split collapse 为该 child
 *   - target 是 split 节点 → lift children 到上一层
 *   - 任何清空后的 split 节点被丢弃
 */
function closePaneInTree(panes: Pane[], targetId: string): Pane[] | null {
  // 特殊：tab 唯一根 pane 即 target → 整个 tab 关闭
  if (panes.length === 1 && panes[0]!.id === targetId) {
    return null;
  }

  const result: Pane[] = [];
  for (const p of panes) {
    if (p.id === targetId) {
      // 命中 target
      if (p.split !== null) {
        // split 节点：把 children lift 到上一层
        for (const child of p.children) {
          result.push(child);
        }
      }
      // leaf：直接丢弃（result 不 push）
      continue;
    }
    if (p.split !== null) {
      const sub = closePaneInTree(p.children, targetId);
      if (sub === null) {
        // 向上冒泡：通知调用方关闭整个 tab
        return null;
      }
      if (sub.length === 0) {
        // children 全空 → 丢弃这个 split 节点
        continue;
      }
      if (sub.length === 1) {
        // 父 split 剩 1 个 child → collapse（避免悬空 split 节点）
        result.push(sub[0]!);
        continue;
      }
      result.push({ ...p, children: sub });
      continue;
    }
    result.push(p);
  }
  return result;
}

// =====================================================================
// store impl
// =====================================================================
export const useTabsStore = create<TabsState>()(
  subscribeWithSelector((set) => ({
    tabs: [],
    activeTabId: null,

    addTab: (tab) => {
      const tabId = newPaneId();
      const paneId = newPaneId();
      const newTab: Tab = {
        ...tab,
        id: tabId,
        panes: [
          { id: paneId, split: null, children: [], size: 100, sessionId: null },
        ],
        activePaneId: paneId,
      };
      set((s) => ({ tabs: [...s.tabs, newTab], activeTabId: tabId }));
      return tabId;
    },

    removeTab: (id) => {
      set((s) => {
        const newTabs = s.tabs.filter((t) => t.id !== id);
        const newActive =
          s.activeTabId === id ? (newTabs[0]?.id ?? null) : s.activeTabId;
        return { tabs: newTabs, activeTabId: newActive };
      });
    },

    setActiveTab: (id) => set({ activeTabId: id }),

    updateTab: (id, patch) =>
      set((s) => ({
        tabs: s.tabs.map((t) => (t.id === id ? { ...t, ...patch } : t)),
      })),

    splitPane: (tabId, paneId, direction) => {
      set((s) => ({
        tabs: s.tabs.map((t) => {
          if (t.id !== tabId) return t;
          return {
            ...t,
            panes: splitPaneInTree(t.panes, paneId, direction),
          };
        }),
      }));
    },

    closePane: (tabId, paneId) => {
      set((s) => {
        const newTabs: Tab[] = [];
        for (const t of s.tabs) {
          if (t.id !== tabId) {
            newTabs.push(t);
            continue;
          }
          const newPanes = closePaneInTree(t.panes, paneId);
          if (newPanes === null) {
            // 整个 tab 关闭：不 push
            continue;
          }
          // active pane 兜底：若关闭的是 activePaneId，切到剩余的 root leaf
          let activePaneId = t.activePaneId;
          if (paneId === activePaneId) {
            const fallback = newPanes[0];
            if (fallback) {
              activePaneId = findFirstLeafId(fallback);
            }
          }
          newTabs.push({ ...t, panes: newPanes, activePaneId });
        }
        // 兜底 activeTabId
        const newActive =
          s.activeTabId === tabId
            ? (newTabs[0]?.id ?? null)
            : s.activeTabId;
        return { tabs: newTabs, activeTabId: newActive };
      });
    },

    setActivePane: (tabId, paneId) =>
      set((s) => ({
        tabs: s.tabs.map((t) =>
          t.id === tabId ? { ...t, activePaneId: paneId } : t,
        ),
      })),
  })),
);

/**
 * 在 pane 树中找出第一个 leaf 的 id（深度优先）。
 * 用于 activePane 被关闭后兜底选中。
 */
function findFirstLeafId(p: Pane): string {
  if (p.split === null) return p.id;
  const first = p.children[0];
  if (!first) return p.id; // 不应发生
  return findFirstLeafId(first);
}
