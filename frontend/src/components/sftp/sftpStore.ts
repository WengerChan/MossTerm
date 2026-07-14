/**
 * SFTP store（v0.2+ 启用）
 * --------------------------------------------------------------------
 * 占位实现，定义 list / upload / download / jobs 的 state shape。
 */
import { create } from "zustand";
import type { SessionID } from "@/types/session";
import type { SftpEntry, TransferJob } from "@/types/sftp";

export interface SftpPanelState {
  /** session 维度：每个 session 一棵"目录树"缓存 */
  entriesByPath: Record<SessionID, Record<string, SftpEntry[]>>;
  /** 每个 path 的下一页 token，用于分页 */
  nextTokenByPath: Record<SessionID, Record<string, string>>;
  /** 当前浏览的路径（per session） */
  currentPath: Record<SessionID, string>;
  /** 传输任务列表 */
  jobs: TransferJob[];
  /** 正在上传/下载的 path（用于 UI 高亮） */
  busyPaths: Record<SessionID, Set<string>>;
}

interface SftpState extends SftpPanelState {
  // ===== actions =====
  listDir: (sid: SessionID, path: string, reset?: boolean) => Promise<void>;
  cd: (sid: SessionID, path: string) => void;
  upload: (sid: SessionID, local: string, remote: string) => Promise<void>;
  download: (sid: SessionID, remote: string, local: string) => Promise<void>;
  cancelJob: (jobId: string) => Promise<void>;
  pauseJob:  (jobId: string) => Promise<void>;
  resumeJob: (jobId: string) => Promise<void>;
}

const initial: SftpPanelState = {
  entriesByPath: {},
  nextTokenByPath: {},
  currentPath: {},
  jobs: [],
  busyPaths: {},
};

export const useSftpStore = create<SftpState>((set) => ({
  ...initial,

  listDir: async (_sid, _path, _reset) => {
    // TODO: const page: SftpListPage = await Api.ListSftp(sid, path, 200, token);
    // set((s) => {
    //   const cur = s.entriesByPath[sid] ?? {};
    //   const next = reset ? page.entries : [...(cur[path] ?? []), ...page.entries];
    //   return {
    //     entriesByPath:  { ...s.entriesByPath,  [sid]: { ...cur, [path]: next } },
    //     nextTokenByPath:{ ...s.nextTokenByPath,[sid]: { ...(s.nextTokenByPath[sid] ?? {}), [path]: page.nextToken } },
    //   };
    // });
  },

  cd: (sid, path) =>
    set((s) => ({ currentPath: { ...s.currentPath, [sid]: path } })),

  upload: async (_sid, _local, _remote) => {
    // TODO: const jobId = await Api.EnqueueTransfer({ direction: "upload", localPath: local, remotePath: remote });
    // 订阅 transfer:progress 事件更新 jobs
  },

  download: async (_sid, _remote, _local) => {
    // TODO: 同上，direction = "download"
  },

  cancelJob: async (_jobId) => {
    // TODO: await Api.CancelTransfer(jobId);
  },
  pauseJob: async (_jobId) => {
    // TODO: await Api.PauseTransfer(jobId);
  },
  resumeJob: async (_jobId) => {
    // TODO: await Api.ResumeTransfer(jobId);
  },
}));

// 工具：判断某 path 是否有进行中的传输
export function isPathBusy(state: SftpPanelState, sid: SessionID, _path: string): boolean {
  const set = state.busyPaths[sid];
  return !!set && set.size > 0;
}
