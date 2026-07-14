/**
 * Tabs + Split-Pane 状态
 * --------------------------------------------------------------------
 * v0.5.0 B 引入。每个 Tab 持有一棵 Pane 树；v0.5.8 扩展 Pane 类型支持
 * SFTP leaf。
 *
 *   Tab.panes: Pane[]                ─ 顶级 root（一般 1 个；保留数组为后续 detached pane 留口）
 *     └─ Pane: { kind: 'terminal' | 'sftp', split: null, ... }  ─ leaf，挂载 Terminal / SftpPaneView
 *     └─ Pane: { kind: 'split', split: 'h' | 'v', children: [Pane, Pane] } ─ split 节点
 *
 * 设计要点：
 *   - split 节点固定 2 个 child（size 50/50；v0.5 不做拖拽改 size）
 *   - 关闭一个 leaf：其父 split 自动 collapse 为剩余的 child
 *   - 关闭一个 split 节点：lift 它的 children 到上一层
 *   - 关闭 tab 内最后一个 leaf：整个 tab 被关闭
 *   - pane id 用 crypto.randomUUID() 生成
 *   - **v0.5.8**：leaf 用 `kind` 区分 terminal / sftp；split 节点
 *     `kind = 'split'`。`split === null` 也保留为"非 split 节点"判定。
 *
 * 算法层（paneTree.ts）已抽离为 pure module，本文件只剩 zustand wrapper
 * + ID 生成。store actions 调 paneTree.ts 即可。
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
import {
  addPaneToRoot,
  closePaneFromTree,
  createLeaf,
  findFirstLeafId,
  splitPaneAt,
  treeHasLeafOfKind,
} from "./paneTree";

// =====================================================================
// Pane 树类型
// =====================================================================
export type PaneSplitDirection = "horizontal" | "vertical";

/**
 * v0.5.8 扩展 Pane 类型：
 *   - `kind: 'split'` → split 节点，children.length === 2
 *   - `kind: 'terminal' | 'sftp'` → leaf，挂载对应 view
 *
 * 判 leaf 用 `pane.kind !== 'split'`（不再用 `split === null`），
 * 保持类型上的显式声明。
 */
export type PaneKind = "terminal" | "sftp" | "split";

export interface Pane {
  id: string;
  /**
   * pane 类型：
   *   - 'split' = 容器节点
   *   - 'terminal' = SSH terminal leaf
   *   - 'sftp' = SFTP browser leaf（v0.5.8 引入）
   */
  kind: PaneKind;
  /**
   * split 方向；null = leaf（'terminal' / 'sftp'）。
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
  /** pane 树根列表（v0.5 固定 length=1；v0.5.8 多 root 用于 SFTP 并列） */
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
   * 新建一个 tab，自动创建一个空 terminal leaf pane。
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
  /**
   * v0.5.8：在 tab 内追加一个新 leaf（用于 addSftpPane / addTerminalPane）。
   *   - 追加到 rootPanes 末尾（root 已有 split 时 leaf 平铺到 root）
   *   - 自动激活新 leaf（activePaneId 指向新 leaf）
   *   - caller 负责传入 leaf.kind + sessionId
   */
  addPaneToTab: (tabId: string, leaf: Pane) => void;
  /**
   * v0.5.8 便捷 API：在 tab 内追加 SFTP leaf。返回新 pane id（用于 caller
   * 决定是否 toast / focus）。
   *
   *   - 缺省 sessionId：tab 当前 sessionId（tab 创建好 session 后再调用）
   *   - 缺省 sessionId 且 tab 也没有：传 null（leaf 渲染 EmptyLeafHint）
   */
  addSftpPane: (tabId: string, sessionId?: SessionID | null) => string;
  /**
   * v0.5.8 便捷 API：在 tab 内追加 terminal leaf。
   *   - 缺省 sessionId：tab 当前 sessionId
   *   - 当前 v0.5.8 暂未挂到 TabBar（保持 SplitPane 工具栏是唯一入口），
   *     留作未来 "克隆当前 terminal 到新 pane" 之类 ops 的接入口
   */
  addTerminalPane: (tabId: string, sessionId?: SessionID | null) => string;
  /**
   * v0.5.8 内部工具：tab 是否已含 SFTP leaf（用于 TabBar 决定 SFTP 按钮
   * 文案 = "打开 SFTP 面板" vs "再开一个 SFTP"）。
   */
  tabHasSftpLeaf: (tabId: string) => boolean;
}

// =====================================================================
// store impl
// =====================================================================
const newPaneId = (): string => crypto.randomUUID();

export const useTabsStore = create<TabsState>()(
  subscribeWithSelector((set, get) => ({
    tabs: [],
    activeTabId: null,

    addTab: (tab) => {
      const tabId = newPaneId();
      const leaf = createLeaf("terminal", null);
      const newTab: Tab = {
        ...tab,
        id: tabId,
        panes: [leaf],
        activePaneId: leaf.id,
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
          return { ...t, panes: splitPaneAt(t.panes, paneId, direction) };
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
          const newPanes = closePaneFromTree(t.panes, paneId);
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

    addPaneToTab: (tabId, leaf) => {
      set((s) => ({
        tabs: s.tabs.map((t) => {
          if (t.id !== tabId) return t;
          return {
            ...t,
            panes: addPaneToRoot(t.panes, leaf),
            activePaneId: leaf.id, // 自动激活新 leaf
          };
        }),
      }));
    },

    addSftpPane: (tabId, sessionId) => {
      const t: Tab | undefined = get().tabs.find((x) => x.id === tabId);
      // sessionId 缺省 → 用 tab 自身的 sessionId（如果 tab 已有）
      const sid = sessionId === undefined ? t?.sessionId ?? null : sessionId;
      const leaf = createLeaf("sftp", sid);
      get().addPaneToTab(tabId, leaf);
      return leaf.id;
    },

    addTerminalPane: (tabId, sessionId) => {
      const t: Tab | undefined = get().tabs.find((x) => x.id === tabId);
      const sid = sessionId === undefined ? t?.sessionId ?? null : sessionId;
      const leaf = createLeaf("terminal", sid);
      get().addPaneToTab(tabId, leaf);
      return leaf.id;
    },

    tabHasSftpLeaf: (tabId) => {
      const t = get().tabs.find((x) => x.id === tabId);
      if (!t) return false;
      return treeHasLeafOfKind(t.panes, "sftp");
    },
  })),
);

// re-export 给外部
export { findFirstLeafId, createLeaf } from "./paneTree";
export { treeHasLeafOfKind, collectLeaves } from "./paneTree";
