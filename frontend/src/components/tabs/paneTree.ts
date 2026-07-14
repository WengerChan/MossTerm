/**
 * Pane 树算法（pure functions）
 * --------------------------------------------------------------------
 * 把 v0.5.0 B 写在 tabsStore.ts 里的 splitPaneInTree / closePaneInTree /
 * findFirstLeafId 抽出来，作为无副作用的 pure module。
 *
 * 抽离原因（v0.5.8）：
 *   1. Pane 类型扩 `kind: 'terminal' | 'sftp' | 'split'` 后，算法边界
 *      不变（仍是 split/leaf 两种状态机），但 ops 多了 ——
 *      addPaneToRoot、hasKind、createLeaf 都需要测试覆盖
 *   2. tabsStore 体积膨胀（v0.5.0 B 已有 ~280 行，v0.5.8 再加 SFTP ops
 *      会破 350 行），拆出算法层让 store 只剩 zustand wrapper
 *   3. 抽到 .ts 模块后，可用 node:test 直接跑（不依赖 React/zustand
 *      运行时），不需要起 vitest
 *
 * 设计约束：
 *   - 所有函数 PURE：输入 Pane[] → 输出 Pane[]，无外部状态依赖
 *   - paneId 生成依赖 crypto.randomUUID() —— 在测试里可注入 idFactory
 *     避免依赖 globalThis.crypto（在 Node 18 之前不挂载）
 *   - 不可变：所有变换都返回新对象，原 Pane[] 不修改
 */
import type { Pane, PaneSplitDirection } from "./tabsStore";

// =====================================================================
// Factory 注入（测试用）
// =====================================================================

/** 生成新 pane id。默认 crypto.randomUUID()，测试可注入固定序列。 */
export type PaneIdFactory = () => string;

const defaultIdFactory: PaneIdFactory = () => crypto.randomUUID();

/** 算法层一律接受 idFactory 入参；tabsStore 调用时传 undefined 走默认。 */
export type IdFactoryArg = PaneIdFactory | undefined;

// =====================================================================
// 构造器
// =====================================================================

/** 创建一个 leaf pane（kind 决定渲染 Terminal / SftpPaneView）。 */
export function createLeaf(
  kind: "terminal" | "sftp",
  sessionId: string | null,
  newId: IdFactoryArg = defaultIdFactory,
): Pane {
  return {
    id: newId!(),
    kind,
    split: null,
    children: [],
    size: 100,
    sessionId,
  };
}

/** 创建一个 split 节点（direction 决定 flex-row/col）。 */
export function createSplit(
  direction: PaneSplitDirection,
  left: Pane,
  right: Pane,
  newId: IdFactoryArg = defaultIdFactory,
): Pane {
  return {
    // v0.5.8 修复：split 节点不再复用 left id；始终分配新 id。
    // 原因：复用 id 在嵌套 split 时会让 closePaneFromTree 误判
    // "唯一根 = target"（panes.length === 1 && panes[0].id === targetId）。
    // splitPaneAt 已经自己分配 id；createSplit 之前沿用 v0.5.0 B 的
    // "调用方负责"约定，但实际无 caller，制造陷阱。
    id: (newId ?? defaultIdFactory)(),
    kind: "split",
    split: direction,
    children: [left, right],
    size: 100,
    sessionId: null,
  };
}

// =====================================================================
// 遍历
// =====================================================================

/** 深搜：找到第一个 leaf 的 id（split 节点的 id 跳过）。 */
export function findFirstLeafId(p: Pane): string {
  if (p.kind === "split") {
    const first = p.children[0];
    if (!first) return p.id; // 防御：split 必有两个 child
    return findFirstLeafId(first);
  }
  return p.id;
}

/** 深搜：找到任意一个 leaf（测试用 + 可能给 "切换下一个 pane" 用）。 */
export function findFirstLeaf(p: Pane): Pane {
  if (p.kind === "split") {
    const first = p.children[0];
    if (!first) return p;
    return findFirstLeaf(first);
  }
  return p;
}

/** 树中是否含指定 kind 的 leaf（v0.5.8 用于：tab 内是否已有 SFTP pane）。 */
export function treeHasLeafOfKind(panes: Pane[], kind: "terminal" | "sftp"): boolean {
  for (const p of panes) {
    if (p.kind === kind) return true;
    if (p.kind === "split" && treeHasLeafOfKind(p.children, kind)) return true;
  }
  return false;
}

/** 收集树中所有 leaf（DFS 先序）。 */
export function collectLeaves(panes: Pane[]): Pane[] {
  const out: Pane[] = [];
  const walk = (p: Pane): void => {
    if (p.kind === "split") {
      for (const c of p.children) walk(c);
    } else {
      out.push(p);
    }
  };
  for (const p of panes) walk(p);
  return out;
}

// =====================================================================
// 变换
// =====================================================================

/**
 * 把 panes 树中 targetId 对应的 leaf 转成 split 节点。
 * 第一个 child 继承原 sessionId（同 pane 类型 + sessionId）；第二个 child
 * 是与原 leaf 同 kind 的空 pane（sessionId=null）—— SFTP split SFTP /
 * Terminal split Terminal，最常见的场景都是这个。
 *
 * 注意：split 节点不可再 split（hit 已为 split 时 no-op）。
 */
export function splitPaneAt(
  panes: Pane[],
  targetId: string,
  direction: PaneSplitDirection,
  newId: IdFactoryArg = defaultIdFactory,
): Pane[] {
  const factory = newId ?? defaultIdFactory;
  return panes.map((p) => {
    if (p.id === targetId) {
      if (p.kind !== "terminal" && p.kind !== "sftp") return p; // split 不可再 split
      const newLeaf: Pane = {
        id: factory(),
        kind: p.kind, // 同 kind（terminal split terminal, sftp split sftp）
        split: null,
        children: [],
        size: 50,
        sessionId: null, // 新 leaf 等用户后续绑定
      };
      return {
        id: factory(), // split 节点用新 id
        kind: "split",
        split: direction,
        children: [
          { ...p, size: 50 }, // 原 leaf 保留（id / kind / sessionId 都不变）
          newLeaf,
        ],
        size: 100,
        sessionId: null,
      };
    }
    if (p.kind === "split") {
      return { ...p, children: splitPaneAt(p.children, targetId, direction, factory) };
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
 *   - **v0.5.8 修正**：lift 出来的 children 数量可能 > 2（嵌套 split
 *     关闭时合并 3+ 个 leaf），不再用 split 容器装，统一平铺到 root
 *
 * 冒泡语义：
 *   之前 `if (sub === null) return null` 是 dead code —— 只有
 *   panes.length === 1 && panes[0].id === targetId 才能 return null
 *   （即 tab 唯一根 = target），外层 split 不会触发"内部空 → 冒泡"
 *   （因 sub.length === 0 时 continue 不 push 即可）。保留 null 出口
 *   给上层使用。
 */
export function closePaneFromTree(panes: Pane[], targetId: string): Pane[] | null {
  // 特殊：tab 唯一根 pane 即 target → 整个 tab 关闭
  if (panes.length === 1 && panes[0]!.id === targetId) {
    return null;
  }

  const result: Pane[] = [];
  for (const p of panes) {
    if (p.id === targetId) {
      // 命中 target
      if (p.kind === "split") {
        // split 节点：把 children lift 到上一层（root 层）
        for (const child of p.children) {
          result.push(child);
        }
      }
      // leaf：直接丢弃（result 不 push）
      continue;
    }
    if (p.kind === "split") {
      const sub = closePaneFromTree(p.children, targetId);
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
      if (sub.length === 2) {
        // 标准 split：2 child 保留
        result.push({ ...p, children: sub });
        continue;
      }
      // sub.length >= 3：lift 到 root 层（v0.5.8 修正 —— 嵌套 split
      // 关闭时合并出来的 leaf 数量可能 > 2，不能再用 split 容器装）
      for (const s of sub) result.push(s);
      continue;
    }
    result.push(p);
  }
  return result;
}

/**
 * 在 root 追加一个新 leaf。语义：
 *   - 用于 addSftpPane / addTerminalPane —— 用户从 TabBar 点 "SFTP"
 *     就在 tab 内并列出一个新 pane
 *   - 若 root 已有 split，平铺到 root（与 closePane 提升 children 一致）
 *   - 若 root 唯一 leaf 且就是当前 active —— 仍平铺（不做自动 wrap），
 *     保持简单；用户后续可手动调整
 *
 * 注：调用方负责在 add 后更新 activePaneId 指向新 leaf。
 */
export function addPaneToRoot(panes: Pane[], leaf: Pane): Pane[] {
  return [...panes, leaf];
}
