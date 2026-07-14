/**
 * FileList —— 文件列表
 * --------------------------------------------------------------------
 * v0.1 渲染缓存中的 entries（无虚拟滚动），v0.2 引入虚拟列表。
 */
import { useEffect, useMemo } from "react";
import { Folder, File, ChevronUp, ChevronDown } from "lucide-react";
import clsx from "clsx";
import { useSftpStore } from "./sftpStore";
import { useUIStore } from "@stores/uiStore";
import { formatBytes, formatAbsoluteTime } from "@utils/format";
import type { SftpEntry as Entry } from "@/types/sftp";
import type { SessionID } from "@/types/session";

export interface FileListProps {
  sessionId: SessionID;
  path: string;
}

type SortKey = "name" | "size" | "modTime";
type SortDir = "asc" | "desc";

export function FileList({ sessionId, path }: FileListProps): JSX.Element {
  const entriesMap  = useSftpStore((s) => s.entriesByPath[sessionId] ?? {});
  const tokenMap    = useSftpStore((s) => s.nextTokenByPath[sessionId] ?? {});
  const listDir     = useSftpStore((s) => s.listDir);
  const cd          = useSftpStore((s) => s.cd);
  const pushToast   = useUIStore((s) => s.pushToast);

  // 把 `?? []` 放进 useMemo 避免每次 render 返回新数组触发 exhaustive-deps 警告
  const rawEntries: ReadonlyArray<Entry> = useMemo(
    () => entriesMap[path] ?? [],
    [entriesMap, path],
  );
  const nextToken: string = tokenMap[path] ?? "";

  // 排序
  const sortKey: SortKey = "name";
  const sortDir: SortDir = "asc";
  const sorted = useMemo(() => {
    const dirs = rawEntries.filter((e) => e.type === "dir");
    const files = rawEntries.filter((e) => e.type !== "dir");
    const cmp = (a: Entry, b: Entry): number => {
      const av = a[sortKey] as number | string;
      const bv = b[sortKey] as number | string;
      const r = av < bv ? -1 : av > bv ? 1 : 0;
      return sortDir === "asc" ? r : -r;
    };
    return [...dirs.sort(cmp), ...files.sort(cmp)];
  }, [rawEntries, sortKey, sortDir]);

  // 首次加载
  useEffect(() => {
    if (rawEntries.length === 0 && !nextToken) {
      void listDir(sessionId, path, true);
    }
  }, [sessionId, path, rawEntries.length, nextToken, listDir]);

  if (rawEntries.length === 0) {
    return (
      <div className="flex h-full items-center justify-center text-[11px] text-ink-muted">
        空目录 / 加载中
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col text-[11px]">
      {/* 表头 */}
      <div className="flex items-center gap-1 border-b border-moss-border bg-moss-bg px-2 py-1 text-[10px] uppercase tracking-wider text-ink-subtle">
        <span className="w-4" />
        <span className="flex-1">名称</span>
        <span className="w-16 text-right">大小</span>
        <span className="w-32 text-right">修改时间</span>
      </div>

      {/* 列表 */}
      <ul className="flex-1 min-h-0 overflow-y-auto">
        {sorted.map((e) => (
          <FileRow
            key={e.path}
            entry={e}
            onActivate={() => {
              if (e.type === "dir") {
                cd(sessionId, e.path);
                void listDir(sessionId, e.path, true);
              } else {
                pushToast({ level: "info", message: `双击下载: ${e.name}（v0.2 实现）`, durationMs: 2000 });
              }
            }}
          />
        ))}
      </ul>

      {/* 加载更多 */}
      {nextToken && (
        <button
          onClick={() => listDir(sessionId, path, false)}
          className="border-t border-moss-border py-1 text-ink-muted hover:bg-moss-hover hover:text-ink"
        >
          加载更多…
        </button>
      )}
    </div>
  );
}

interface FileRowProps {
  entry: Entry;
  onActivate: () => void;
}
function FileRow({ entry, onActivate }: FileRowProps): JSX.Element {
  const isDir = entry.type === "dir";
  return (
    <li
      onDoubleClick={onActivate}
      className="group flex cursor-pointer items-center gap-1 px-2 py-1 hover:bg-moss-hover"
    >
      <span className="w-4 text-ink-muted">
        {isDir ? <Folder size={12} className="text-accent" /> : <File size={12} />}
      </span>
      <span className={clsx("flex-1 truncate", isDir && "text-ink")}>{entry.name}</span>
      <span className="w-16 text-right text-ink-muted">
        {isDir ? "—" : formatBytes(entry.size)}
      </span>
      <span className="w-32 text-right text-ink-muted">
        {formatAbsoluteTime(entry.modTime)}
      </span>
    </li>
  );
}

// 为避免未使用变量警告
export const _internal = { ChevronUp, ChevronDown };
