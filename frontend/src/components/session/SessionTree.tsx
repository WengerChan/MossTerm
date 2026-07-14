/**
 * SessionTree —— 侧栏中的 profile 树
 * --------------------------------------------------------------------
 * 数据来源：useConnectionStore.profiles + profileOrder
 * 支持：
 *   - 按 group 分组（v0.2 启用）
 *   - 搜索过滤（侧栏顶部输入框）
 *   - 双击/回车打开会话
 *   - 右键菜单（编辑、复制、删除）
 *   - 拖拽排序（v0.2+）
 */
import { useMemo } from "react";
import { Server, ChevronRight, ChevronDown, Pencil, Trash2, Copy } from "lucide-react";
import clsx from "clsx";
import { useConnectionStore } from "@stores/connectionStore";
import { useSessionStore } from "./sessionStore";
import { useUIStore } from "@stores/uiStore";
import type { Profile } from "@/types/session";

export function SessionTree(): JSX.Element {
  const profiles    = useConnectionStore((s) => s.profiles);
  const order       = useConnectionStore((s) => s.profileOrder);
  const openSession = useConnectionStore((s) => s.openSession);
  const startEdit   = useSessionStore((s) => s.startEdit);
  const setActive   = useConnectionStore((s) => s.setActiveSession);
  const activeSid   = useConnectionStore((s) => s.activeSessionId);
  const keyword     = useSessionStore((s) => s.form.searchKeyword);
  const setGroups   = useSessionStore((s) => s.setGroups);
  const groups      = useSessionStore((s) => s.groups);
  const openModal   = useUIStore((s) => s.openModal);
  const pushToast   = useUIStore((s) => s.pushToast);

  // 把扁平 profiles 按 group 聚合
  useMemoGroupSync(profiles, order, setGroups);

  // 过滤（按 name / host / user / tags）
  const filtered = useMemo(() => {
    const kw = keyword.trim().toLowerCase();
    if (!kw) return order.map((id) => profiles[id]).filter((p): p is Profile => !!p);
    return order
      .map((id) => profiles[id])
      .filter((p): p is Profile => !!p)
      .filter((p) => {
        if (p.name.toLowerCase().includes(kw)) return true;
        if (p.host.toLowerCase().includes(kw)) return true;
        if (p.user.toLowerCase().includes(kw)) return true;
        if (p.tags?.some((t) => t.toLowerCase().includes(kw))) return true;
        return false;
      });
  }, [keyword, order, profiles]);

  if (filtered.length === 0) {
    return (
      <div className="flex h-full flex-col items-center justify-center px-4 py-6 text-center text-xs text-ink-muted">
        <Server size={20} className="mb-2 opacity-50" />
        <p>暂无 profile</p>
        <p className="mt-1 text-[10px] text-ink-subtle">
          点击右上角 <span className="text-accent">+</span> 新建
        </p>
      </div>
    );
  }

  return (
    <ul className="flex flex-col gap-0.5 p-1 text-xs">
      {groups.map((g) => {
        const inGroup = filtered.filter((p) => (p.group ?? "默认") === g.name);
        if (inGroup.length === 0) return null;
        return (
          <li key={g.name}>
            <GroupHeader name={g.name} count={inGroup.length} />
            <ul className="mt-0.5">
              {inGroup.map((p) => (
                <ProfileItem
                  key={p.id}
                  profile={p}
                  active={activeSid === p.id}
                  onOpen={() => {
                    void openSession(p.id).then((id) => id && setActive(id));
                  }}
                  onEdit={() => startEdit(p)}
                  onDelete={() =>
                    openModal({
                      // v0.5.6: 用单 modal 槽位 + 透传 profileId props
                      //（之前每次删都建新 id 会出现多个 delete-profile modal 槽位 bug）
                      id: "confirm-delete-profile",
                      title: `删除 profile "${p.name}"?`,
                      componentKey: "ConfirmDeleteProfile",
                      props: { profileId: p.id },
                    })
                  }
                  onDuplicate={() => {
                    pushToast({ level: "info", message: "复制功能 v0.2 启用", durationMs: 2000 });
                  }}
                />
              ))}
            </ul>
          </li>
        );
      })}
    </ul>
  );
}

interface GroupHeaderProps { name: string; count: number; }
function GroupHeader({ name, count }: GroupHeaderProps): JSX.Element {
  return (
    <div className="mt-1 flex items-center gap-1 px-2 py-0.5 text-[10px] uppercase tracking-wider text-ink-subtle">
      <ChevronDown size={10} />
      <span className="font-semibold">{name}</span>
      <span className="text-ink-subtle">({count})</span>
    </div>
  );
}

interface ProfileItemProps {
  profile: Profile;
  active: boolean;
  onOpen: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onDuplicate: () => void;
}
function ProfileItem({ profile, active, onOpen, onEdit, onDelete, onDuplicate }: ProfileItemProps): JSX.Element {
  return (
    <li
      onDoubleClick={onOpen}
      className={clsx(
        "group flex cursor-pointer items-center gap-2 rounded px-2 py-1",
        active ? "bg-accent/20 text-accent" : "text-ink hover:bg-moss-hover",
      )}
    >
      <span
        className="h-1.5 w-1.5 rounded-full"
        style={{ backgroundColor: profile.color ?? "#7CB342" }}
      />
      <div className="flex-1 min-w-0">
        <div className="truncate">{profile.name || "(未命名)"}</div>
        <div className="truncate text-[10px] text-ink-muted">
          {profile.user}@{profile.host}:{profile.port}
        </div>
      </div>

      <div className="hidden gap-0.5 group-hover:flex">
        <ItemAction onClick={onEdit} title="编辑"><Pencil size={10} /></ItemAction>
        <ItemAction onClick={onDuplicate} title="复制"><Copy size={10} /></ItemAction>
        <ItemAction onClick={onDelete} title="删除" danger><Trash2 size={10} /></ItemAction>
      </div>
    </li>
  );
}

interface ItemActionProps {
  onClick: () => void;
  title: string;
  children: React.ReactNode;
  danger?: boolean;
}
function ItemAction({ onClick, title, children, danger }: ItemActionProps): JSX.Element {
  return (
    <button
      onClick={(e) => { e.stopPropagation(); onClick(); }}
      title={title}
      className={clsx(
        "rounded p-0.5",
        danger ? "hover:bg-state-err/20 hover:text-state-err" : "hover:bg-moss-border hover:text-ink",
      )}
    >
      {children}
    </button>
  );
}

/**
 * 把扁平 profiles 同步成 group 列表
 * - 默认分组 "默认"
 * - 按字母排序 group
 */
function useMemoGroupSync(
  profiles: Record<string, Profile>,
  order: string[],
  setGroups: (g: { name: string; profileIds: string[] }[]) => void,
): void {
  useMemo(() => {
    const map = new Map<string, string[]>();
    for (const id of order) {
      const p = profiles[id];
      if (!p) continue;
      const g = p.group ?? "默认";
      if (!map.has(g)) map.set(g, []);
      map.get(g)!.push(id);
    }
    const arr = Array.from(map.entries())
      .map(([name, profileIds]) => ({ name, profileIds }))
      .sort((a, b) => a.name.localeCompare(b.name));
    setGroups(arr);
  }, [profiles, order, setGroups]);
}

// 避免未使用 import 警告
export const _unused = { ChevronRight };
