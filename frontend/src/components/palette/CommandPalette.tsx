/**
 * CommandPalette —— 命令面板
 * --------------------------------------------------------------------
 * 唤起：`Ctrl+Shift+P`
 * 数据源：
 *   - 内置命令（BUILTIN_COMMANDS）
 *   - profile 列表（动态注入）
 *   - 插件贡献（v0.3+）
 * 搜索：纯字符串匹配（v0.2 引入 fuse.js 做模糊匹配）
 */
import { useEffect, useMemo, useRef, useState } from "react";
import { Search, ChevronRight, Hash } from "lucide-react";
import clsx from "clsx";
import { useUIStore } from "@stores/uiStore";
import { useConnectionStore } from "@stores/connectionStore";
import { useShortcut } from "@hooks/useShortcut";
import type { ReactNode } from "react";

export interface CommandItem {
  id: string;
  title: string;
  description?: string;
  group: string;
  icon?: ReactNode;
  shortcut?: string;
  /** 关键词，用于搜索权重 */
  keywords?: string[];
  /** 实际执行 */
  run: () => void | Promise<void>;
}

export function CommandPalette(): JSX.Element {
  const close     = useUIStore((s) => s.closeCommandPalette);
  const toggleSb  = useUIStore((s) => s.toggleSidebar);
  const toggleSftp = useUIStore((s) => s.toggleSftpPanel);
  const toggleLog = useUIStore((s) => s.toggleLogPanel);
  const openModal = useUIStore((s) => s.openModal);
  const profiles  = useConnectionStore((s) => s.profiles);
  const order     = useConnectionStore((s) => s.profileOrder);
  const openSession = useConnectionStore((s) => s.openSession);
  const setActive   = useConnectionStore((s) => s.setActiveSession);

  const [query, setQuery]     = useState("");
  const [activeIdx, setActiveIdx] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const listRef  = useRef<HTMLUListElement | null>(null);

  // 构建命令列表
  const items = useMemo<CommandItem[]>(() => {
    const builtin: CommandItem[] = [
      {
        id: "ui.toggleSidebar",
        title: "切换侧栏",
        group: "视图",
        shortcut: "Ctrl+`",
        run: () => toggleSb(),
      },
      {
        id: "ui.toggleSftp",
        title: "切换 SFTP 面板",
        group: "视图",
        shortcut: "Ctrl+Alt+S",
        run: () => toggleSftp(),
      },
      {
        id: "ui.toggleLog",
        title: "切换日志面板",
        group: "视图",
        shortcut: "F1",
        run: () => toggleLog(),
      },
      {
        id: "settings.open",
        title: "打开设置",
        group: "应用",
        shortcut: "Ctrl+,",
        run: () => openModal({ id: "settings", title: "设置", componentKey: "Settings" }),
      },
      {
        id: "session.new",
        title: "新建 Profile",
        group: "会话",
        run: () => openModal({ id: "session-form", title: "新建 Profile", componentKey: "SessionForm" }),
      },
    ];

    const profileItems: CommandItem[] = order
      .map((id) => profiles[id])
      .filter((p): p is NonNullable<typeof p> => !!p)
      .map<CommandItem>((p) => ({
        id: `profile.open.${p.id}`,
        title: `连接: ${p.name}`,
        description: `${p.user}@${p.host}:${p.port}`,
        group: "Profiles",
        keywords: [p.host, p.user, ...(p.tags ?? [])],
        run: () => {
          void openSession(p.id).then((sid) => sid && setActive(sid));
        },
      }));

    return [...builtin, ...profileItems];
  }, [profiles, order, toggleSb, toggleSftp, toggleLog, openModal, openSession, setActive]);

  // 过滤
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return items;
    return items.filter((it) => {
      if (it.title.toLowerCase().includes(q)) return true;
      if (it.description?.toLowerCase().includes(q)) return true;
      if (it.keywords?.some((k) => k.toLowerCase().includes(q))) return true;
      return false;
    });
  }, [items, query]);

  // 自动 focus & 选中第一条
  useEffect(() => {
    inputRef.current?.focus();
    setActiveIdx(0);
  }, []);

  // Esc 关闭
  useShortcut({
    key: "escape",
    handler: () => close(),
    requireFocus: false,
    ignoreEditable: false,
  });

  const execute = async (item: CommandItem): Promise<void> => {
    try {
      await item.run();
    } finally {
      close();
    }
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>): void => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActiveIdx((i) => Math.min(i + 1, filtered.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActiveIdx((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const it = filtered[activeIdx];
      if (it) void execute(it);
    }
  };

  // 把 active 项滚到可视区
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-idx="${activeIdx}"]`);
    el?.scrollIntoView({ block: "nearest" });
  }, [activeIdx]);

  return (
    <div
      onClick={close}
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 pt-24 backdrop-blur-sm"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-[560px] max-w-[90vw] overflow-hidden rounded-lg border border-moss-border bg-moss-surface shadow-2xl"
      >
        {/* 搜索框 */}
        <div className="flex items-center gap-2 border-b border-moss-border px-3 py-2">
          <Search size={14} className="text-ink-muted" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder="输入命令或 profile 名…"
            className="flex-1 bg-transparent text-sm text-ink placeholder-ink-subtle focus:outline-none"
          />
          <kbd className="rounded border border-moss-border bg-moss-bg px-1.5 py-0.5 text-[10px] text-ink-muted">
            Esc
          </kbd>
        </div>

        {/* 列表 */}
        <ul ref={listRef} className="max-h-80 overflow-y-auto py-1 text-sm">
          {filtered.length === 0 ? (
            <li className="px-3 py-6 text-center text-xs text-ink-muted">无匹配命令</li>
          ) : (
            filtered.map((it, idx) => (
              <li
                key={it.id}
                data-idx={idx}
                onMouseEnter={() => setActiveIdx(idx)}
                onClick={() => void execute(it)}
                className={clsx(
                  "flex cursor-pointer items-center gap-2 px-3 py-1.5",
                  idx === activeIdx ? "bg-accent/15 text-ink" : "text-ink-muted hover:bg-moss-hover",
                )}
              >
                <Hash size={12} className="opacity-50" />
                <span className="flex-1 truncate">
                  <span className="text-ink">{it.title}</span>
                  {it.description && (
                    <span className="ml-2 text-[11px] text-ink-muted">{it.description}</span>
                  )}
                </span>
                {it.shortcut && (
                  <kbd className="rounded border border-moss-border bg-moss-bg px-1.5 py-0.5 text-[10px] text-ink-muted">
                    {it.shortcut}
                  </kbd>
                )}
                {idx === activeIdx && <ChevronRight size={12} className="text-accent" />}
              </li>
            ))
          )}
        </ul>

        {/* 底栏 */}
        <div className="flex items-center justify-between border-t border-moss-border px-3 py-1.5 text-[10px] text-ink-subtle">
          <span>↑↓ 选择 · ⏎ 执行</span>
          <span>{filtered.length} 条结果</span>
        </div>
      </div>
    </div>
  );
}
