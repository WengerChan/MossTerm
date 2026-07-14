/**
 * Session / Profile store（UI 视图层）
 * --------------------------------------------------------------------
 * 真正连接/凭据 CRUD 在 connectionStore；这里只管"表单编辑态"和"分组"，
 * 让 SessionForm 和 SessionTree 不直接耦合全局 store。
 */
import { create } from "zustand";
import type { Profile, ProfileID, AuthKind } from "@types/session";
import { useUIStore } from "@stores/uiStore";

// v0.5.6: startCreate / startEdit 现在会 open profile-edit modal
//（避免调用方还要再 openModal 一次）。字符串字面量避免循环依赖。
const PROFILE_EDIT_MODAL_ID = "profile-edit";

export interface ProfileGroup {
  name: string;
  /** 该分组下的 profile id 列表（按显示顺序） */
  profileIds: ProfileID[];
}

export interface SessionFormState {
  /** 正在编辑的 profile（新建时为 null） */
  draft: Profile;
  /** 表单是否 dirty */
  dirty: boolean;
  /** 当前选中的分组过滤器，null = 全部 */
  selectedGroup: string | null;
  /** 搜索关键字 */
  searchKeyword: string;
}

const emptyProfile = (): Profile => ({
  id: "",
  name: "",
  host: "127.0.0.1",
  port: 22,
  user: "root",
  protocol: "ssh",
  auth: { kind: "password" as AuthKind },
  createdAt: 0,
  updatedAt: 0,
});

interface SessionUIState {
  form: SessionFormState;
  groups: ProfileGroup[];

  // ===== actions =====
  startCreate: () => void;
  startEdit: (p: Profile) => void;
  updateDraft: (patch: Partial<Profile>) => void;
  updateAuth: (patch: Partial<Profile["auth"]>) => void;
  resetForm: () => void;
  setSelectedGroup: (g: string | null) => void;
  setSearchKeyword: (kw: string) => void;
  setGroups: (groups: ProfileGroup[]) => void;
}

export const useSessionStore = create<SessionUIState>((set) => ({
  form: {
    draft: emptyProfile(),
    dirty: false,
    selectedGroup: null,
    searchKeyword: "",
  },
  groups: [],

  startCreate: () => {
    // v0.5.6: 同时打开 profile-edit modal + init 表单
    //（避免调用方再 openModal 一次 —— 单一来源）
    set({
      form: {
        // id="" 触发后端 SaveProfile 走 AddProfile 路径
        draft: { ...emptyProfile(), id: "", createdAt: Date.now(), updatedAt: Date.now() },
        dirty: false,
        selectedGroup: null,
        searchKeyword: "",
      },
    });
    useUIStore.getState().openModal({
      id: PROFILE_EDIT_MODAL_ID,
      title: "新建 profile",
      componentKey: "ProfileEdit",
    });
  },

  startEdit: (p) => {
    set({
      form: {
        draft: { ...p },
        dirty: false,
        selectedGroup: null,
        searchKeyword: "",
      },
    });
    useUIStore.getState().openModal({
      id: PROFILE_EDIT_MODAL_ID,
      title: `编辑 profile：${p.name}`,
      componentKey: "ProfileEdit",
    });
  },

  updateDraft: (patch) =>
    set((s) => ({
      form: {
        ...s.form,
        draft: { ...s.form.draft, ...patch, updatedAt: Date.now() },
        dirty: true,
      },
    })),

  updateAuth: (patch) =>
    set((s) => ({
      form: {
        ...s.form,
        draft: {
          ...s.form.draft,
          auth: { ...s.form.draft.auth, ...patch },
          updatedAt: Date.now(),
        },
        dirty: true,
      },
    })),

  resetForm: () =>
    set({
      form: {
        draft: emptyProfile(),
        dirty: false,
        selectedGroup: null,
        searchKeyword: "",
      },
    }),

  setSelectedGroup: (g) =>
    set((s) => ({ form: { ...s.form, selectedGroup: g } })),

  setSearchKeyword: (kw) =>
    set((s) => ({ form: { ...s.form, searchKeyword: kw } })),

  setGroups: (groups) => set({ groups }),
}));
