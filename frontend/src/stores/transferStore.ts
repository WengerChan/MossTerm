/**
 * Transfer store（v0.5.10 streaming upload）
 * --------------------------------------------------------------------
 * 集中管理 streaming upload 的任务状态 + 进度。
 *
 * 数据来源（两条路）：
 *   1. 主动调用：refreshList（拉后端 list）/ upsertJob（接 transfer:done
 *      / transfer:error 事件）/ applyProgress（接 transfer:progress 事件）
 *   2. UI 操作：clearFinished（清空已完成） / setSelected（选中）
 *
 * 设计要点：
 *   - 单一 Map<transferID, JobView>，按 StartedAt 倒序展示
 *   - 完成后保留在 map 里，UI 用 listActive / listFinished 区分
 *   - progress 事件节流 200ms 是后端做的；前端只负责 upsert
 *   - 不持久化：刷新页面清空（后端 Manager 也在内存）；前端 store 仅缓存
 *   - 不调 App.StartUpload：sessionID 在调用方上下文已知，由组件直接调
 *     binding；本 store 只做"事件 → state 投影"
 *
 * 复用：
 *   - SftpBrowserContent drag handler 调 App.StartUpload → upsertJob
 *   - UploadProgress 组件订阅 useTransferEvents → applyProgress / upsertJob
 *   - 全局：useTransferStore.getState() 直接读
 */
import { create } from "zustand";
import type { UploadJobInfo, UploadProgress } from "@wails/go/main/App";
import { App } from "@wails/go/main/App";
import { logger } from "@utils/logger";

/** JobView = 持久 JobInfo（最新一次 emit 的快照）。 */
export type JobView = UploadJobInfo & {
  /** 最近一次 progress 事件的速度 B/s（用于 UploadProgress 行内显示） */
  speedBps?: number;
  /** 最近一次 progress 事件的 ETA 秒（-1 = 未知） */
  etaSec?: number;
};

export interface TransferState {
  // ===== state =====
  /** 按 transferID 索引的 jobs */
  jobs: Record<string, JobView>;
  /** 选中态（UploadProgress 面板的"当前展示哪一个"） */
  selectedID: string | null;
  /** 错误兜底（refreshList / cancel 失败时给 UI） */
  lastError: string | null;

  // ===== selectors =====
  getJob: (id: string) => JobView | undefined;
  listJobs: () => JobView[];
  listActive: () => JobView[];
  listFinished: () => JobView[];

  // ===== actions =====
  /** 取消一个 transfer（调 App.CancelUpload）。 */
  cancelUpload: (id: string) => Promise<void>;
  /** 从后端拉一次最新 list（事件丢失兜底）。 */
  refreshList: () => Promise<void>;
  /** 内部使用：被 useTransferEvents() 钩子调用同步 JobInfo。 */
  upsertJob: (info: JobView) => void;
  applyProgress: (p: UploadProgress) => void;
  setSelected: (id: string | null) => void;
  setError: (msg: string | null) => void;
  /** 清空 finished jobs（仅 UI）。 */
  clearFinished: () => void;
}

/**
 * 把 Wails 推送的 raw payload 规范化成 JobView。
 *
 * v0.5.10 Wails 序列化按 Go struct 字段原 tag（camelCase），
 * 所以 transform 通常是 no-op。保留 transform 函数便于未来字段命名约定变化。
 */
function normalizeJobInfo(raw: unknown): JobView {
  const r = raw as Record<string, unknown>;
  return {
    transferID:  String(r.transferID ?? r.transfer_id ?? ""),
    localPath:   String(r.localPath  ?? r.local_path  ?? ""),
    remotePath:  String(r.remotePath ?? r.remote_path ?? ""),
    totalBytes:  Number(r.totalBytes  ?? r.total_bytes  ?? 0),
    bytesSent:   Number(r.bytesSent   ?? r.bytes_sent   ?? 0),
    state:       (r.state as JobView["state"]) ?? "running",
    error:       r.error ? String(r.error) : undefined,
    chunkSize:   Number(r.chunkSize   ?? r.chunk_size   ?? 0),
    concurrency: Number(r.concurrency ?? 0),
    startedAt:   String(r.startedAt   ?? r.started_at   ?? ""),
    updatedAt:   String(r.updatedAt   ?? r.updated_at   ?? ""),
    checksum:    r.checksum ? String(r.checksum) : undefined,
  };
}

function normalizeProgress(raw: unknown): UploadProgress {
  const r = raw as Record<string, unknown>;
  return {
    transferID:  String(r.transferID ?? r.transfer_id ?? ""),
    bytesSent:   Number(r.bytesSent   ?? r.bytes_sent   ?? 0),
    totalBytes:  Number(r.totalBytes  ?? r.total_bytes  ?? 0),
    speedBps:    Number(r.speedBps    ?? r.speed_bps    ?? 0),
    etaSec:      Number(r.etaSec      ?? r.eta_sec      ?? -1),
    chunkIndex:  Number(r.chunkIndex  ?? r.chunk_index  ?? -1),
    totalChunks: Number(r.totalChunks ?? r.total_chunks ?? 0),
  };
}

export const useTransferStore = create<TransferState>((set, get) => ({
  jobs: {},
  selectedID: null,
  lastError: null,

  // ----- selectors -----
  getJob: (id) => get().jobs[id],
  listJobs: () => {
    const arr = Object.values(get().jobs);
    return arr.sort((a, b) => (b.startedAt > a.startedAt ? 1 : -1));
  },
  listActive: () => get().listJobs().filter((j) => j.state === "running"),
  listFinished: () => get().listJobs().filter((j) => j.state !== "running"),

  // ----- actions -----
  cancelUpload: async (id) => {
    set({ lastError: null });
    try {
      await App.CancelUpload(id);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      logger.error(`[transferStore] cancelUpload ${id}: ${msg}`);
      set({ lastError: msg });
    }
  },

  refreshList: async () => {
    try {
      const list = await App.ListTransfers();
      const next: Record<string, JobView> = {};
      for (const item of list) {
        next[item.transferID] = normalizeJobInfo(item);
      }
      set((s) => ({ jobs: { ...s.jobs, ...next } }));
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      logger.error(`[transferStore] refreshList: ${msg}`);
    }
  },

  upsertJob: (info) =>
    set((s) => ({ jobs: { ...s.jobs, [info.transferID]: info } })),

  applyProgress: (p) =>
    set((s) => {
      const cur = s.jobs[p.transferID];
      if (!cur) return {}; // 还没 upsert，丢弃（防 race：upload 启动前的事件）
      return {
        jobs: {
          ...s.jobs,
          [p.transferID]: {
            ...cur,
            bytesSent:  p.bytesSent,
            totalBytes: p.totalBytes || cur.totalBytes,
            speedBps:   p.speedBps,
            etaSec:     p.etaSec,
            updatedAt:  new Date().toISOString(),
          },
        },
      };
    }),

  setSelected: (id) => set({ selectedID: id }),
  setError: (msg) => set({ lastError: msg }),

  clearFinished: () =>
    set((s) => {
      const next: Record<string, JobView> = {};
      for (const [k, v] of Object.entries(s.jobs)) {
        if (v.state === "running") next[k] = v;
      }
      return { jobs: next };
    }),
}));

// 导出 normalize helper 供 useTransferEvents 钩子复用
export { normalizeJobInfo, normalizeProgress };
