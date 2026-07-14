/**
 * 连接 / Profile 状态
 * --------------------------------------------------------------------
 * Profile CRUD + 活跃会话元数据。
 * 不持有 PTY 字节流（热路径数据流由 xterm + Wails 事件直接处理）。
 */
import { create } from "zustand";
import { subscribeWithSelector } from "zustand/middleware";
import type { Profile, ProfileID, SessionID, SessionInfo } from "@/types/session";
import { ACTIVE_STATES } from "@/types/session";
import { logger } from "@utils/logger";
// v0.5.5: 接入 wails 真实 binding —— App.d.ts 在 wailsjs 目录下 stub ，
// 真实运行时由 wails CLI 注入。这里 import 仅供 typecheck 通过 + 编译期守卫。
// eslint-disable-next-line @typescript-eslint/no-unused-vars
import { App } from "@wails/go/main/App";

export interface ConnectionState {
  // ===== state =====
  profiles: Record<ProfileID, Profile>;
  /** 保持插入顺序用于侧栏显示 */
  profileOrder: ProfileID[];
  sessions: Record<SessionID, SessionInfo>;
  /** 当前正在使用的会话（被 TerminalView 订阅） */
  activeSessionId: SessionID | null;
  loading: boolean;
  error: string | null;

  // ===== selectors =====
  getProfile: (id: ProfileID) => Profile | undefined;
  getSession: (id: SessionID) => SessionInfo | undefined;
  listActiveSessions: () => SessionInfo[];

  // ===== actions =====
  refreshProfiles: () => Promise<void>;
  saveProfile: (p: Profile) => Promise<void>;
  deleteProfile: (id: ProfileID) => Promise<void>;
  refreshSessions: () => Promise<void>;
  openSession: (profileId: ProfileID) => Promise<SessionID | null>;
  closeSession: (id: SessionID, force?: boolean) => Promise<void>;
  setActiveSession: (id: SessionID | null) => void;
  /** 内部使用：被 events 钩子调用以同步 SessionInfo */
  upsertSession: (info: SessionInfo) => void;
  removeSession: (id: SessionID) => void;
}

export const useConnectionStore = create<ConnectionState>()(
  subscribeWithSelector((set, get) => ({
    profiles: {},
    profileOrder: [],
    sessions: {},
    activeSessionId: null,
    loading: false,
    error: null,

    // ----- selectors -----
    getProfile: (id) => get().profiles[id],
    getSession: (id) => get().sessions[id],
    listActiveSessions: () =>
      Object.values(get().sessions).filter((s) => ACTIVE_STATES.has(s.state)),

    // ----- actions -----
    refreshProfiles: async () => {
      set({ loading: true, error: null });
      try {
        // v0.5.5: 真实调 wails binding
        const list = await App.ListProfiles();
        const profiles: Record<ProfileID, Profile> = {};
        const profileOrder: ProfileID[] = [];
        for (const p of list) { profiles[p.id] = p; profileOrder.push(p.id); }
        set({ profiles, profileOrder, loading: false });
      } catch (err: unknown) {
        logger.error(`[connectionStore] refreshProfiles: ${String(err)}`);
        set({ error: String(err), loading: false });
      }
    },

    saveProfile: async (p) => {
      try {
        // v0.5.5: 持久化到后端 config
        await App.SaveProfile(p);
        set((s) => {
          const profiles = { ...s.profiles, [p.id]: p };
          const order = s.profileOrder.includes(p.id)
            ? s.profileOrder
            : [...s.profileOrder, p.id];
          return { profiles, profileOrder: order };
        });
      } catch (err: unknown) {
        logger.error(`[connectionStore] saveProfile: ${String(err)}`);
        set({ error: String(err) });
      }
    },

    deleteProfile: async (id) => {
      try {
        // v0.5.5: 后端删 + 本地 state 同步
        await App.DeleteProfile(id);
        set((s) => {
          const profiles = { ...s.profiles };
          delete profiles[id];
          return {
            profiles,
            profileOrder: s.profileOrder.filter((x) => x !== id),
          };
        });
      } catch (err: unknown) {
        logger.error(`[connectionStore] deleteProfile: ${String(err)}`);
        set({ error: String(err) });
      }
    },

    refreshSessions: async () => {
      try {
        // v0.5.5: 从 wailsbinding 拉 session 列表
        const list = await App.ListSessions();
        const sessions: Record<SessionID, SessionInfo> = {};
        for (const s of list) sessions[s.id] = s;
        set({ sessions });
      } catch (err: unknown) {
        logger.error(`[connectionStore] refreshSessions: ${String(err)}`);
        set({ error: String(err) });
      }
    },

    openSession: async (profileId) => {
      try {
        // v0.5.5: 用 profile 调真实 OpenSession
        const p = get().profiles[profileId];
        if (!p) {
          throw new Error(`[connectionStore] openSession: profile not found: ${profileId}`);
        }
        const id = await App.OpenSession({
          profileId: p.id,
          host: p.host,
          port: p.port,
          user: p.user,
          auth: p.auth,
          cols: 80,
          rows: 24,
        });
        await get().refreshSessions();
        set({ activeSessionId: id });
        return id;
      } catch (err: unknown) {
        logger.error(`[connectionStore] openSession: ${String(err)}`);
        set({ error: String(err) });
        return null;
      }
    },

    closeSession: async (id, force = false) => {
      try {
        // v0.5.5: 真实调 CloseSession
        await App.CloseSession(id, force);
        get().removeSession(id);
        if (get().activeSessionId === id) {
          set({ activeSessionId: null });
        }
      } catch (err: unknown) {
        logger.error(`[connectionStore] closeSession: ${String(err)}`);
        set({ error: String(err) });
      }
    },

    setActiveSession: (id) => set({ activeSessionId: id }),

    upsertSession: (info) =>
      set((s) => ({ sessions: { ...s.sessions, [info.id]: info } })),

    removeSession: (id) =>
      set((s) => {
        const next = { ...s.sessions };
        delete next[id];
        return { sessions: next };
      }),
  })),
);
