/**
 * 连接 / Profile 状态
 * --------------------------------------------------------------------
 * Profile CRUD + 活跃会话元数据。
 * 不持有 PTY 字节流（热路径数据流由 xterm + Wails 事件直接处理）。
 */
import { create } from "zustand";
import { subscribeWithSelector } from "zustand/middleware";
import type { Profile, ProfileID, SessionID, SessionInfo } from "@types/session";
import { ACTIVE_STATES } from "@types/session";
import { logger } from "@utils/logger";

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
        // TODO: 调用 wails backend 拉取 profile 列表
        // const list = await Api.ListProfiles();
        // const profiles: Record<ProfileID, Profile> = {};
        // const profileOrder: ProfileID[] = [];
        // for (const p of list) { profiles[p.id] = p; profileOrder.push(p.id); }
        // set({ profiles, profileOrder, loading: false });
        logger.debug("[connectionStore] refreshProfiles called");
        set({ loading: false });
      } catch (err: unknown) {
        logger.error(`[connectionStore] refreshProfiles: ${String(err)}`);
        set({ error: String(err), loading: false });
      }
    },

    saveProfile: async (p) => {
      try {
        // TODO: await Api.SaveProfile(p);
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
        // TODO: await Api.DeleteProfile(id);
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
        // TODO: const list = await Api.ListSessions();
        // const sessions: Record<SessionID, SessionInfo> = {};
        // for (const s of list) sessions[s.id] = s;
        // set({ sessions });
        logger.debug("[connectionStore] refreshSessions called");
      } catch (err: unknown) {
        logger.error(`[connectionStore] refreshSessions: ${String(err)}`);
        set({ error: String(err) });
      }
    },

    openSession: async (_profileId) => {
      try {
        // TODO: const id = await Api.OpenSession({ ... });
        // await get().refreshSessions();
        // return id;
        const fakeId: SessionID = "stub-session-id";
        set({ activeSessionId: fakeId });
        return fakeId;
      } catch (err: unknown) {
        logger.error(`[connectionStore] openSession: ${String(err)}`);
        set({ error: String(err) });
        return null;
      }
    },

    closeSession: async (id, force = false) => {
      try {
        // TODO: await Api.CloseSession(id, force);
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
