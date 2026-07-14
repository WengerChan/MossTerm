/**
 * paneTree.test.ts —— 纯函数 Pane 树算法单测（v0.5.8）
 * --------------------------------------------------------------------
 * 跑法：node --experimental-strip-types --test src/components/tabs/paneTree.test.ts
 *   （package.json scripts.test:tabs 已加）
 *
 * Node 22.6+ 内置 `--experimental-strip-types` 可直接跑 .ts（Node 26
 * 默认开），不依赖 vitest / tsx。
 *
 * 覆盖：
 *   - createLeaf / createSplit
 *   - findFirstLeafId / findFirstLeaf
 *   - treeHasLeafOfKind / collectLeaves
 *   - splitPaneAt（leaf → split；split 不可再 split；deep nested）
 *   - closePaneFromTree（唯一 leaf / 父 split collapse / split 节点 lift）
 *   - addPaneToRoot
 *   - 不变性（不修改输入）
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  addPaneToRoot,
  closePaneFromTree,
  collectLeaves,
  createLeaf,
  createSplit,
  findFirstLeaf,
  findFirstLeafId,
  splitPaneAt,
  treeHasLeafOfKind,
} from "./paneTree.ts";
import type { Pane } from "./tabsStore.ts";

// =====================================================================
// Helpers —— 注入 idFactory 保证测试可重复
// =====================================================================

let idSeq = 0;
const nextId = (): string => `id-${++idSeq}`;
const reset = (): void => { idSeq = 0; };

// =====================================================================
// createLeaf
// =====================================================================

test("createLeaf - terminal + sessionId", () => {
  reset();
  const p = createLeaf("terminal", "sid-1", nextId);
  assert.equal(p.id, "id-1");
  assert.equal(p.kind, "terminal");
  assert.equal(p.split, null);
  assert.deepEqual(p.children, []);
  assert.equal(p.size, 100);
  assert.equal(p.sessionId, "sid-1");
});

test("createLeaf - sftp + null sessionId", () => {
  reset();
  const p = createLeaf("sftp", null, nextId);
  assert.equal(p.kind, "sftp");
  assert.equal(p.sessionId, null);
});

// =====================================================================
// createSplit
// =====================================================================

test("createSplit - horizontal 包含两个 leaf", () => {
  reset();
  const left  = createLeaf("terminal", "sid-L", nextId);
  const right = createLeaf("sftp", "sid-R", nextId);
  const sp = createSplit("horizontal", left, right);
  assert.equal(sp.kind, "split");
  assert.equal(sp.split, "horizontal");
  assert.equal(sp.sessionId, null);
  assert.equal(sp.children.length, 2);
  assert.equal(sp.children[0], left);
  assert.equal(sp.children[1], right);
});

// =====================================================================
// findFirstLeafId / findFirstLeaf
// =====================================================================

test("findFirstLeafId - 深搜 split 树", () => {
  reset();
  const l1 = createLeaf("terminal", "s1", nextId);   // id-1
  const l2 = createLeaf("sftp", "s2", nextId);       // id-2
  const l3 = createLeaf("terminal", "s3", nextId);   // id-3
  const sp1 = createSplit("horizontal", l1, l2);     // id-1 (复用)
  const root = createSplit("vertical", sp1 as Pane, l3);
  // findFirstLeafId 应返回 l1.id
  assert.equal(findFirstLeafId(root), "id-1");
});

test("findFirstLeaf - 返回 leaf 节点本身", () => {
  reset();
  const l1 = createLeaf("terminal", "s1", nextId);
  const l2 = createLeaf("sftp", "s2", nextId);
  const root = createSplit("horizontal", l1, l2);
  const leaf = findFirstLeaf(root);
  assert.equal(leaf.id, l1.id);
  assert.equal(leaf.kind, "terminal");
});

// =====================================================================
// treeHasLeafOfKind
// =====================================================================

test("treeHasLeafOfKind - 命中 nested leaf", () => {
  reset();
  const l1 = createLeaf("terminal", "s1", nextId);
  const l2 = createLeaf("sftp", "s2", nextId);
  const l3 = createLeaf("terminal", "s3", nextId);
  const sp  = createSplit("horizontal", l1, l2);
  const root = createSplit("vertical", sp as Pane, l3);
  assert.equal(treeHasLeafOfKind([root], "sftp"), true);
  assert.equal(treeHasLeafOfKind([root], "terminal"), true);
});

test("treeHasLeafOfKind - 缺失", () => {
  reset();
  const l1 = createLeaf("terminal", "s1", nextId);
  const l2 = createLeaf("terminal", "s2", nextId);
  const root = createSplit("horizontal", l1, l2);
  assert.equal(treeHasLeafOfKind([root], "sftp"), false);
});

test("treeHasLeafOfKind - 空树", () => {
  assert.equal(treeHasLeafOfKind([], "sftp"), false);
});

// =====================================================================
// collectLeaves
// =====================================================================

test("collectLeaves - DFS 先序收集所有 leaf", () => {
  reset();
  const l1 = createLeaf("terminal", "s1", nextId);
  const l2 = createLeaf("sftp", "s2", nextId);
  const l3 = createLeaf("terminal", "s3", nextId);
  const sp  = createSplit("horizontal", l1, l2);
  const root = createSplit("vertical", sp as Pane, l3);
  const leaves = collectLeaves([root]);
  assert.equal(leaves.length, 3);
  assert.equal(leaves[0]!.id, l1.id);
  assert.equal(leaves[1]!.id, l2.id);
  assert.equal(leaves[2]!.id, l3.id);
});

// =====================================================================
// splitPaneAt
// =====================================================================

test("splitPaneAt - leaf 转 split，sessionId 保留在左 child", () => {
  reset();
  const leaf = createLeaf("terminal", "sid-A", nextId);   // id-1
  const out = splitPaneAt([leaf], "id-1", "horizontal", nextId);
  // out[0] = split 节点（新 id-2），children = [原 leaf (id-1), 新 leaf (id-3)]
  assert.equal(out.length, 1);
  const sp = out[0]!;
  assert.equal(sp.kind, "split");
  assert.equal(sp.split, "horizontal");
  assert.notEqual(sp.id, "id-1"); // split 节点是新 id
  assert.equal(sp.children.length, 2);
  assert.equal(sp.children[0]!.id, "id-1");
  assert.equal(sp.children[0]!.kind, "terminal");
  assert.equal(sp.children[0]!.sessionId, "sid-A");
  assert.equal(sp.children[1]!.kind, "terminal");
  assert.equal(sp.children[1]!.sessionId, null);
});

test("splitPaneAt - SFTP leaf 转 split 后左 child 仍为 SFTP", () => {
  reset();
  const leaf = createLeaf("sftp", "sid-A", nextId);
  const out = splitPaneAt([leaf], leaf.id, "vertical", nextId);
  const sp = out[0]!;
  assert.equal(sp.kind, "split");
  assert.equal(sp.children[0]!.kind, "sftp");
  assert.equal(sp.children[1]!.kind, "sftp");
});

test("splitPaneAt - split 节点不可再 split（no-op）", () => {
  reset();
  const leaf = createLeaf("terminal", "sid-A", nextId);   // id-1
  const l2   = createLeaf("terminal", null, nextId);       // id-2
  const sp1  = createSplit("horizontal", leaf, l2, nextId); // id-3
  // target = sp1.id (split 节点) → splitPaneAt 应原样返回
  const out = splitPaneAt([sp1], sp1.id, "vertical", nextId);
  assert.equal(out.length, 1);
  assert.equal(out[0], sp1);
});

test("splitPaneAt - 命中 nested leaf", () => {
  reset();
  const l1 = createLeaf("terminal", "s1", nextId);   // id-1
  const l2 = createLeaf("sftp", "s2", nextId);       // id-2
  const root = createSplit("horizontal", l1, l2);    // id-1
  // 对 id-2 (sftp leaf) 拆 vertical
  const out = splitPaneAt([root], "id-2", "vertical", nextId);
  // root.split = horizontal 保持
  // root.children[1] (原 sftp) 应变成 split 节点
  assert.equal(out[0]!.split, "horizontal");
  const newRight = out[0]!.children[1]!;
  assert.equal(newRight.kind, "split");
  assert.equal(newRight.split, "vertical");
  assert.equal(newRight.children[0]!.kind, "sftp");
  assert.equal(newRight.children[0]!.sessionId, "s2");
});

test("splitPaneAt - 不修改原数组（不可变）", () => {
  reset();
  const leaf = createLeaf("terminal", "sid-A", nextId);
  const origRef = leaf;
  const before = JSON.stringify(leaf);
  void splitPaneAt([leaf], leaf.id, "horizontal", nextId);
  assert.equal(JSON.stringify(leaf), before);
  assert.equal(leaf, origRef);
});

// =====================================================================
// closePaneFromTree
// =====================================================================

test("closePaneFromTree - 唯一 root leaf → null（外部关闭整个 tab）", () => {
  reset();
  const leaf = createLeaf("terminal", "sid-A", nextId);
  const out = closePaneFromTree([leaf], leaf.id);
  assert.equal(out, null);
});

test("closePaneFromTree - 关闭 split 的 child leaf → 父 collapse", () => {
  reset();
  const l1 = createLeaf("terminal", "s1", nextId);   // id-1
  const l2 = createLeaf("terminal", "s2", nextId);   // id-2
  const root = createSplit("horizontal", l1, l2);    // id-1
  // 关 l2 (id-2)
  const out = closePaneFromTree([root], "id-2");
  assert.ok(out);
  // 应只剩 l1（split collapse）
  assert.equal(out!.length, 1);
  assert.equal(out![0]!.id, "id-1");
  assert.equal(out![0]!.kind, "terminal");
});

test("closePaneFromTree - 关闭 split 节点 → children lift 到 root", () => {
  reset();
  const l1 = createLeaf("terminal", "s1", nextId);   // id-1
  const l2 = createLeaf("sftp", "s2", nextId);       // id-2
  const l3 = createLeaf("terminal", "s3", nextId);   // id-3
  // v0.5.8 createSplit 分配新 id（sp.id = id-4），不复用 left id
  const sp  = createSplit("horizontal", l1, l2, nextId);
  const root = createSplit("vertical", sp, l3, nextId);
  // 关 sp（不是 l1）→ 应 lift 出 l1 + l2 到 root 层
  // 但 root 自身也是 split，关 sp 后 root.children 变成 [l1, l2, l3]（3 个），
  // v0.5.8 修复：sub.length > 2 时统一平铺到 root 层，不再保留外层 split 容器
  const out = closePaneFromTree([root], sp.id);
  assert.ok(out);
  // out 应是 3 个 leaf 平铺
  assert.equal(out!.length, 3);
  assert.equal(out![0]!.id, l1.id);
  assert.equal(out![0]!.kind, "terminal");
  assert.equal(out![1]!.id, l2.id);
  assert.equal(out![1]!.kind, "sftp");
  assert.equal(out![2]!.id, l3.id);
  assert.equal(out![2]!.kind, "terminal");
});

test("closePaneFromTree - 父 split 关 1 child → collapse 到 leaf", () => {
  reset();
  const lA = createLeaf("terminal", "x", nextId);
  const lB = createLeaf("terminal", "y", nextId);
  const inner = createSplit("horizontal", lA, lB, nextId);
  // 关 lA → inner 剩 [lB] → collapse → out = [lB]
  const out1 = closePaneFromTree([inner], lA.id);
  assert.ok(out1);
  assert.equal(out1!.length, 1);
  assert.equal(out1![0]!.id, lB.id);
  assert.equal(out1![0]!.kind, "terminal");
});

test("closePaneFromTree - 命中不存在的 id（no-op）", () => {
  reset();
  const leaf = createLeaf("terminal", "s1", nextId);
  const out = closePaneFromTree([leaf], "nonexistent");
  // 唯一根且 id != target → 不变
  assert.ok(out);
  assert.equal(out!.length, 1);
  assert.equal(out![0]!.id, leaf.id);
});

test("closePaneFromTree - 命中不存在的 id（no-op）", () => {
  reset();
  const leaf = createLeaf("terminal", "s1", nextId);
  const out = closePaneFromTree([leaf], "nonexistent");
  // 唯一根且 id != target → 不变
  assert.ok(out);
  assert.equal(out!.length, 1);
  assert.equal(out![0]!.id, leaf.id);
});

// =====================================================================
// addPaneToRoot
// =====================================================================

test("addPaneToRoot - 追加 leaf 到 root（不变更原数组）", () => {
  reset();
  const orig = createLeaf("terminal", "s1", nextId);
  const newP = createLeaf("sftp", "s2", nextId);
  const before = JSON.stringify([orig]);
  const out = addPaneToRoot([orig], newP);
  // 不修改原数组
  assert.equal(JSON.stringify([orig]), before);
  // 返回新数组
  assert.equal(out.length, 2);
  assert.equal(out[0]!.id, orig.id);
  assert.equal(out[1]!.id, newP.id);
});

test("addPaneToRoot - 多次追加累积", () => {
  reset();
  const a = createLeaf("terminal", "s1", nextId);
  const b = createLeaf("sftp", "s2", nextId);
  const c = createLeaf("terminal", "s3", nextId);
  const out = addPaneToRoot(addPaneToRoot([a], b), c);
  assert.equal(out.length, 3);
  assert.equal(out[0]!.id, a.id);
  assert.equal(out[1]!.id, b.id);
  assert.equal(out[2]!.id, c.id);
});
