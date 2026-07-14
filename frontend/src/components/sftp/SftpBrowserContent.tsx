/**
 * SftpBrowserContent —— SFTP 浏览器的核心 list / toolbar / dialog / viewer
 * --------------------------------------------------------------------
 * v0.5.8 抽出：把 v0.5.1 SftpBrowser 里的"内容部分"独立成组件，由两个
 * 包装器共用：
 *   - SftpBrowser：modal 包装（fixed inset-0 + 标题 + 关闭按钮）
 *   - SftpPaneView：pane 包装（leaf focus 高亮 + 嵌入 PaneView 树）
 *
 * 抽出原因（v0.5.8）：
 *   - SFTP 集成到 Pane 树后，"打开方式"从全局 modal 变成 pane tree 节点
 *   - 但 list / upload / mkdir / rename / remove / view 逻辑完全相同
 *     —— 抽出来避免 ~500 行代码在两个文件里复制粘贴
 *   - 之前 v0.5.1 的 SftpBrowser 的"内容部分"没有自己的组件边界（被
 *     整个 modal 上下文绑死），现在显式拆开
 *
 * 设计要点：
 *   - **状态全本地**：path/entries/dialog/viewer/drag 状态都在 content 内部
 *     —— pane 关闭 → content 卸载 → 状态自然清空，无脏数据
 *   - **所有 helper 函数独立**：EntryRow/SortHeader/Breadcrumb/SubDialog/
 *     SubDialog 都是本文件内 private 子组件，不跨文件 export
 *   - **可自定义 className**：wrapper（modal/pane）通过 className 调整布局
 *   - **SftpBrowser** 仍挂载在 App 顶层，但 v0.5.8 后**不再默认渲染**
 *     —— App.tsx 改为只在 sftpBrowserStore.isOpen=true 时挂载（保留
 *     兼容触发路径，例如未来 "TabBar hover 按钮弹预览"）
 *
 * 依赖：
 *   - App（wails binding）—— 通过 @wails/go/main/App 调 SftpList/Read/...
 *   - useConnectionStore —— 取 sessionInfo 显示 user@host
 *   - useUIStore —— pushToast
 *   - sftpBrowserStore —— 暂不依赖（v0.5.8 content 完全自含）
 */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import {
  ChevronUp,
  Folder,
  File,
  RefreshCw,
  FolderPlus,
  Trash2,
  Edit3,
  Eye,
  ChevronRight,
} from "lucide-react";
import clsx from "clsx";
import { App, type Entry, type ListPage } from "@wails/go/main/App";
import { useUIStore } from "@stores/uiStore";
import { useTransferStore } from "@stores/transferStore";
import { useTransferEvents } from "@hooks/useTransferEvents";
import { formatBytes } from "@utils/format";
import { logger } from "@utils/logger";
import type { SessionID } from "@/types/session";
import { PreviewPanel } from "./PreviewPanel";
import { UploadProgress } from "./UploadProgress";

// =====================================================================
// Constants
// =====================================================================

const LIST_PAGE_SIZE = 500;
// v0.5.10 streaming upload：取消 100 MiB 限制（受后端 10 GiB 硬保护）。
// 用户拖入 GB 级文件时后端走分片并发 WriteAt，前端不卡 UI。

// =====================================================================
// Types
// =====================================================================

type DialogKind = "mkdir" | "rename" | "delete" | null;

interface DialogState {
  kind: Exclude<DialogKind, null>;
  target?: Entry;
  contextPath: string;
}

type SortKey = "name" | "size" | "modTime";
type SortDir = "asc" | "desc";

export interface SftpBrowserContentProps {
  sessionID: SessionID | null;
  /** wrapper 传入的 className（h-full w-full 之类布局由 wrapper 决定） */
  className?: string;
}

// =====================================================================
// Path helpers
// =====================================================================

function normalizePath(p: string): string {
  if (!p) return "/";
  if (p === "/") return "/";
  return p.replace(/\/+$/, "") || "/";
}

function joinPath(parent: string, name: string): string {
  const p = normalizePath(parent);
  if (p === "/") return `/${name}`;
  return `${p}/${name}`;
}

function parentPath(p: string): string | null {
  const n = normalizePath(p);
  if (n === "/") return null;
  const idx = n.lastIndexOf("/");
  if (idx <= 0) return "/";
  return n.slice(0, idx);
}

function splitSegments(p: string): string[] {
  const n = normalizePath(p);
  if (n === "/") return ["/"];
  return ["/", ...n.slice(1).split("/").filter(Boolean)];
}

// =====================================================================
// SftpBrowserContent
// =====================================================================

export function SftpBrowserContent({
  sessionID,
  className,
}: SftpBrowserContentProps): JSX.Element | null {
  const pushToast = useUIStore((s) => s.pushToast);
  // session 信息（user/host/port）由 wrapper 决定如何展示：
  //   - modal 版（SftpBrowser）放 title bar
  //   - pane 版（SftpPaneView）放 mini header
  // content 不读 session —— 避免重复渲染

  const [path, setPath] = useState<string>("/");
  const [entries, setEntries] = useState<Entry[]>([]);
  const [loading, setLoading] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [sortKey, setSortKey] = useState<SortKey>("name");
  const [sortDir, setSortDir] = useState<SortDir>("asc");
  const [dialog, setDialog] = useState<DialogState | null>(null);
  // v0.5.9 预览：仅存 path；具体元信息由 PreviewPanel 内部从后端拉
  const [previewPath, setPreviewPath] = useState<string | null>(null);
  const [isDragOver, setIsDragOver] = useState(false);

  const reqTokenRef = useRef(0);

  // sessionID 变化：重置（pane 重新挂载时也会自动重置；这里防止 session 切换）
  useEffect(() => {
    setPath("/");
    setEntries([]);
    setSelectedPath(null);
    setError(null);
    setDialog(null);
    setPreviewPath(null);
    setIsDragOver(false);
  }, [sessionID]);

  const listDir = useCallback(
    async (target: string) => {
      if (!sessionID) return;
      const token = ++reqTokenRef.current;
      setLoading("Loading...");
      setError(null);
      try {
        const page: ListPage = await App.SftpList(sessionID, target, LIST_PAGE_SIZE, "");
        if (token !== reqTokenRef.current) return;
        setEntries(page.entries ?? []);
        setSelectedPath(null);
      } catch (err: unknown) {
        if (token !== reqTokenRef.current) return;
        const msg = err instanceof Error ? err.message : String(err);
        logger.error(`[SftpBrowserContent] List ${target} failed: ${msg}`);
        setError(msg);
        setEntries([]);
      } finally {
        if (token === reqTokenRef.current) setLoading(null);
      }
    },
    [sessionID],
  );

  useEffect(() => {
    if (sessionID) {
      void listDir(path);
    }
  }, [sessionID, path, listDir]);

  // ---------- Navigation ----------

  const cd = useCallback((next: string) => setPath(normalizePath(next)), []);

  const cdUp = useCallback(() => {
    const p = parentPath(path);
    if (p !== null) cd(p);
  }, [path, cd]);

  const onBreadcrumbClick = useCallback((segIndex: number) => {
    const segs = splitSegments(path);
    if (segs.length === 0) return;
    if (segIndex === 0) {
      cd("/");
      return;
    }
    const next = "/" + segs.slice(1, segIndex + 1).join("/");
    cd(next);
  }, [path, cd]);

  // ---------- Entry interactions ----------

  const onEntryClick = useCallback((e: Entry) => setSelectedPath(e.path), []);

  const onEntryDouble = useCallback((e: Entry) => {
    if (e.isDir) {
      cd(e.path);
      return;
    }
    if (!sessionID) return;
    // v0.5.9：弹 PreviewPanel，元信息由 panel 内部从后端 SftpGetFileMetadata 拉
    setPreviewPath(e.path);
  }, [sessionID, cd]);

  // ---------- Actions ----------

  const refresh = useCallback(() => { void listDir(path); }, [path, listDir]);

  const promptMkdir  = useCallback(() => setDialog({ kind: "mkdir", contextPath: path }), [path]);
  const promptRename = useCallback((e: Entry) => setDialog({ kind: "rename", target: e, contextPath: e.path }), []);
  const promptDelete = useCallback((e: Entry) => setDialog({ kind: "delete", target: e, contextPath: path }), [path]);

  const onDialogSubmit = useCallback(async (input: string): Promise<void> => {
    if (!dialog || !sessionID) return;
    const { kind, target, contextPath } = dialog;
    try {
      if (kind === "mkdir") {
        const name = input.trim();
        if (!name) return;
        if (name.includes("/")) {
          pushToast({ level: "warn", message: "目录名不能包含 /", durationMs: 3000 });
          return;
        }
        await App.SftpMkdir(sessionID, joinPath(contextPath, name));
        pushToast({ level: "success", message: `已创建 ${name}`, durationMs: 2000 });
      } else if (kind === "rename" && target) {
        const newName = input.trim();
        if (!newName) return;
        if (newName.includes("/")) {
          pushToast({ level: "warn", message: "名称不能包含 /", durationMs: 3000 });
          return;
        }
        if (newName === target.name) {
          setDialog(null);
          return;
        }
        const newPath = joinPath(parentPath(target.path) ?? "/", newName);
        await App.SftpRename(sessionID, target.path, newPath);
        pushToast({ level: "success", message: `已重命名为 ${newName}`, durationMs: 2000 });
      } else if (kind === "delete" && target) {
        await App.SftpRemove(sessionID, target.path);
        pushToast({ level: "success", message: `已删除 ${target.name}`, durationMs: 2000 });
      }
      setDialog(null);
      void listDir(path);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      const action = kind === "mkdir" ? "新建目录" : kind === "rename" ? "重命名" : "删除";
      pushToast({ level: "error", message: `${action} 失败：${msg}`, durationMs: 5000 });
    }
  }, [dialog, sessionID, path, listDir, pushToast]);

  // ---------- Drag-Drop 上传 ----------

  const handleDragOver = useCallback((e: React.DragEvent) => {
    if (e.dataTransfer.types.includes("Files")) {
      e.preventDefault();
      e.stopPropagation();
      setIsDragOver(true);
    }
  }, []);

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    const panel = e.currentTarget as HTMLElement;
    const next = e.relatedTarget as Node | null;
    if (next && panel.contains(next)) return;
    setIsDragOver(false);
  }, []);

  const handleDrop = useCallback(
    async (e: React.DragEvent) => {
      e.preventDefault();
      e.stopPropagation();
      setIsDragOver(false);

      const files = Array.from(e.dataTransfer.files);
      if (files.length === 0) return;
      if (!sessionID) return;

      // v0.5.10 streaming upload：每个文件调 App.StartUpload（后台 goroutine）。
      // 不再走 SftpUploadFile（v0.5.3 一次性上传，100 MiB 限制）。
      // 大文件（≤ 10 GiB）受后端硬保护；前端不卡 UI。
      const t0 = performance.now();
      let queued = 0;
      for (const f of files) {
        try {
          // File.path 在 webkit 浏览器可用；Firefox 不行（""）。
          // 拿不到 path 时回退到 name（只能传文件名，wailsbinding 后端
          // 收到空 path 会拒绝，所以这里必须至少有 name）。
          // v0.5.10 实际行为：File.path 在 Wails webview（macOS WebKit /
          // Windows WebView2 / Linux WebKitGTK）下都可用，Firefox 不支持
          // 跨过 Wails 部署范围。
          // DOM lib 的 File 类型没声明 .path（webkit 扩展）；用类型断言。
          const fWithPath = f as File & { path?: string };
          const localPath = fWithPath.path || `/${f.name}`; // 兜底
          const remotePath = joinPath(path, f.name);
          const transferID = await App.StartUpload({
            transferID:  "",
            sessionID,
            localPath,
            remotePath,
            chunkSize:   0,
            concurrency: 0,
            resume:      false,
          });
          // 占位 JobView 写 store：让 progress 事件有归属
          useTransferStore.getState().upsertJob({
            transferID,
            localPath,
            remotePath,
            totalBytes:  f.size,
            bytesSent:   0,
            state:       "running",
            chunkSize:   0,
            concurrency: 0,
            startedAt:   new Date().toISOString(),
            updatedAt:   new Date().toISOString(),
          });
          queued++;
        } catch (err: unknown) {
          const msg = err instanceof Error ? err.message : String(err);
          logger.error(`[SftpBrowserContent] StartUpload ${f.name} failed: ${msg}`);
          setError(`Upload ${f.name} 启动失败：${msg}`);
        }
      }
      const ms = (performance.now() - t0).toFixed(0);
      logger.info(`[SftpBrowserContent] queued ${queued}/${files.length} uploads in ${ms}ms`);
      // 注：不在这里 setLoading(null) — UploadProgress 面板接管"上传中"反馈
      // setLoading(null) 在 listDir 完成后做
      void listDir(path);
    },
    [sessionID, path, listDir],
  );

  // 订阅 transfer:progress / done / error 事件 → 写 store
  useTransferEvents();

  // ---------- Derived ----------

  const sorted = useMemo(() => {
    const dirs  = entries.filter((e) => e.isDir);
    const files = entries.filter((e) => !e.isDir);
    const cmp = (a: Entry, b: Entry): number => {
      const av: number | string =
        sortKey === "modTime" ? Date.parse(a.modTime) || 0 : (a[sortKey] as number | string);
      const bv: number | string =
        sortKey === "modTime" ? Date.parse(b.modTime) || 0 : (b[sortKey] as number | string);
      const r = av < bv ? -1 : av > bv ? 1 : 0;
      return sortDir === "asc" ? r : -r;
    };
    return [...dirs.sort(cmp), ...files.sort(cmp)];
  }, [entries, sortKey, sortDir]);

  const selectedEntry = useMemo(
    () => entries.find((e) => e.path === selectedPath) ?? null,
    [entries, selectedPath],
  );

  // ---------- Render guards ----------

  if (!sessionID) {
    return null;
  }

  // ---------- Render ----------

  return (
    <div
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
      className={clsx(
        "flex h-full w-full flex-col overflow-hidden",
        isDragOver ? "ring-4 ring-accent/40" : "",
        className,
      )}
      data-testid="sftp-content"
    >
      {/* ===== Toolbar: 上 / 刷 / 新建 / 面包屑 ===== */}
      <div className="flex items-center gap-2 border-b border-moss-border bg-moss-bg px-3 py-2 text-xs">
        <button
          onClick={cdUp}
          disabled={!!loading || parentPath(path) === null}
          className="rounded border border-moss-border bg-moss-surface p-1 text-ink-muted hover:bg-moss-hover hover:text-ink disabled:opacity-40"
          title="上一级"
          aria-label="上一级"
        >
          <ChevronUp size={14} />
        </button>
        <button
          onClick={refresh}
          disabled={!!loading}
          className="rounded border border-moss-border bg-moss-surface p-1 text-ink-muted hover:bg-moss-hover hover:text-ink disabled:opacity-40"
          title="刷新"
          aria-label="刷新"
        >
          <RefreshCw size={14} className={loading ? "animate-spin" : ""} />
        </button>
        <button
          onClick={promptMkdir}
          disabled={!!loading}
          className="inline-flex items-center gap-1 rounded border border-moss-border bg-moss-surface px-2 py-1 text-ink-muted hover:bg-moss-hover hover:text-ink disabled:opacity-40"
          title="新建文件夹"
        >
          <FolderPlus size={12} aria-hidden />
          新建文件夹
        </button>

        <Breadcrumb path={path} onJump={onBreadcrumbClick} />

        {error && (
          <span
            className="ml-auto max-w-[280px] truncate rounded border border-state-err/40 bg-state-err/10 px-2 py-0.5 text-[11px] text-state-err"
            title={error}
          >
            {error}
          </span>
        )}
      </div>

      {/* ===== Column header ===== */}
      <div className="flex items-center gap-2 border-b border-moss-border bg-moss-bg px-3 py-1 text-[10px] uppercase tracking-wider text-ink-subtle">
        <span className="w-4" />
        <SortHeader
          label="名称"
          colKey="name"
          sortKey={sortKey}
          sortDir={sortDir}
          onSort={(k, d) => { setSortKey(k); setSortDir(d); }}
          className="flex-1"
        />
        <SortHeader
          label="大小"
          colKey="size"
          sortKey={sortKey}
          sortDir={sortDir}
          onSort={(k, d) => { setSortKey(k); setSortDir(d); }}
          className="w-20 text-right"
        />
        <SortHeader
          label="修改时间"
          colKey="modTime"
          sortKey={sortKey}
          sortDir={sortDir}
          onSort={(k, d) => { setSortKey(k); setSortDir(d); }}
          className="w-40 text-right"
        />
        <span className="w-24 text-right">权限</span>
      </div>

      {/* ===== Entry list ===== */}
      <div className="flex-1 min-h-0 overflow-y-auto" onClick={() => setSelectedPath(null)}>
        {loading && entries.length === 0 ? (
          <div className="flex h-full items-center justify-center text-[11px] text-ink-muted">
            加载中…
          </div>
        ) : sorted.length === 0 ? (
          <div className="flex h-full items-center justify-center text-[11px] text-ink-muted">
            空目录
          </div>
        ) : (
          <ul className="text-[12px]">
            {sorted.map((e) => (
              <EntryRow
                key={e.path}
                entry={e}
                selected={selectedPath === e.path}
                onClick={() => onEntryClick(e)}
                onDoubleClick={() => void onEntryDouble(e)}
              />
            ))}
          </ul>
        )}
      </div>

      {/* ===== Action bar (bottom) ===== */}
      <div className="flex items-center gap-2 border-t border-moss-border bg-moss-bg px-3 py-2 text-xs">
        <span className="min-w-0 flex-1 truncate text-ink-muted">
          {selectedEntry
            ? `已选中：${selectedEntry.name}`
            : `${sorted.length} 项${loading ? "（刷新中…）" : ""}`}
        </span>
        {selectedEntry && (
          <>
            <button
              onClick={() => void onEntryDouble(selectedEntry)}
              disabled={selectedEntry.isDir}
              className="inline-flex items-center gap-1 rounded border border-moss-border bg-moss-surface px-2 py-1 text-ink-muted hover:bg-moss-hover hover:text-ink disabled:opacity-40"
              title="查看（双击）"
            >
              <Eye size={12} aria-hidden />
              查看
            </button>
            <button
              onClick={() => promptRename(selectedEntry)}
              className="inline-flex items-center gap-1 rounded border border-moss-border bg-moss-surface px-2 py-1 text-ink-muted hover:bg-moss-hover hover:text-ink"
              title="重命名"
            >
              <Edit3 size={12} aria-hidden />
              重命名
            </button>
            <button
              onClick={() => promptDelete(selectedEntry)}
              className="inline-flex items-center gap-1 rounded border border-state-err/40 bg-state-err/10 px-2 py-1 text-state-err hover:bg-state-err/20"
              title="删除"
            >
              <Trash2 size={12} aria-hidden />
              删除
            </button>
          </>
        )}
      </div>

      {/* Sub-dialog（保留绝对覆盖语义） */}
      {dialog && (
        <SubDialog
          kind={dialog.kind}
          target={dialog.target}
          onCancel={() => setDialog(null)}
          onSubmit={(v) => void onDialogSubmit(v)}
        />
      )}

      {/* v0.5.9 PreviewPanel —— 双击文件弹出，覆盖在 content 之上 */}
      {previewPath && sessionID && (
        <PreviewPanel
          sessionID={sessionID}
          path={previewPath}
          onClose={() => setPreviewPath(null)}
        />
      )}

      {/* v0.5.10 streaming upload 进度面板 —— 固定在 content 底部 */}
      {sessionID && <UploadProgress sessionID={sessionID} />}
    </div>
  );
}

// =====================================================================
// Sub-components（private — 不 export）
// =====================================================================

interface BreadcrumbProps {
  path: string;
  onJump: (segIndex: number) => void;
}

function Breadcrumb({ path, onJump }: BreadcrumbProps): JSX.Element {
  const segs = splitSegments(path);
  return (
    <nav
      aria-label="路径"
      className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto rounded border border-moss-border bg-moss-surface px-2 py-1 font-mono text-[11px]"
    >
      {segs.map((seg, i) => (
        <span key={i} className="flex shrink-0 items-center gap-1">
          {i > 0 && <ChevronRight size={10} className="text-ink-subtle" aria-hidden />}
          <button
            onClick={() => onJump(i)}
            className={clsx(
              "rounded px-1 py-0.5 hover:bg-moss-hover hover:text-ink",
              i === segs.length - 1 ? "text-ink" : "text-ink-muted",
            )}
            title={i === 0 ? "根目录" : seg}
          >
            {seg}
          </button>
        </span>
      ))}
    </nav>
  );
}

interface SortHeaderProps {
  label: string;
  colKey: SortKey;
  sortKey: SortKey;
  sortDir: SortDir;
  onSort: (k: SortKey, d: SortDir) => void;
  className?: string;
}

function SortHeader({ label, colKey, sortKey, sortDir, onSort, className }: SortHeaderProps): JSX.Element {
  const active = sortKey === colKey;
  return (
    <button
      onClick={() => onSort(colKey, active && sortDir === "asc" ? "desc" : "asc")}
      className={clsx(
        "hover:text-ink",
        active ? "text-ink" : "text-ink-subtle",
        className,
      )}
      title={`按${label}排序`}
    >
      {label}
      {active ? (sortDir === "asc" ? " ▲" : " ▼") : ""}
    </button>
  );
}

interface EntryRowProps {
  entry: Entry;
  selected: boolean;
  onClick: () => void;
  onDoubleClick: () => void;
}

function EntryRow({ entry, selected, onClick, onDoubleClick }: EntryRowProps): JSX.Element {
  const isDir = entry.isDir;
  const isSym = entry.isSymlink;
  return (
    <li
      onClick={(e) => { e.stopPropagation(); onClick(); }}
      onDoubleClick={(e) => { e.stopPropagation(); onDoubleClick(); }}
      className={clsx(
        "flex cursor-pointer items-center gap-2 px-3 py-1",
        selected ? "bg-accent/15 text-ink" : "hover:bg-moss-hover text-ink",
      )}
      title={entry.path}
    >
      <span className="w-4 shrink-0 text-ink-muted">
        {isDir
          ? <Folder size={12} className="text-accent" aria-hidden />
          : <File size={12} className="text-ink-muted" aria-hidden />}
      </span>
      <span className={clsx("flex-1 truncate", isDir && "font-medium")}>
        {entry.name}
        {isSym && <span className="ml-1 text-[10px] text-ink-subtle">↪</span>}
      </span>
      <span className="w-20 shrink-0 text-right text-ink-muted">
        {isDir ? "—" : formatBytes(entry.size)}
      </span>
      <span className="w-40 shrink-0 text-right text-[11px] text-ink-muted">
        {entry.modTime ? formatModTime(entry.modTime) : "—"}
      </span>
      <span className="w-24 shrink-0 text-right font-mono text-[10px] text-ink-muted">
        {formatPerms(entry.mode)}
      </span>
    </li>
  );
}

interface SubDialogProps {
  kind: "mkdir" | "rename" | "delete";
  target?: Entry;
  onCancel: () => void;
  onSubmit: (value: string) => void;
}

function SubDialog({ kind, target, onCancel, onSubmit }: SubDialogProps): JSX.Element {
  const [value, setValue] = useState<string>(
    kind === "rename" && target ? target.name : "",
  );
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (kind === "delete") return;
    const t = window.setTimeout(() => {
      const el = inputRef.current;
      if (el) {
        el.focus();
        if (kind === "rename") el.select();
      }
    }, 0);
    return () => window.clearTimeout(t);
  }, [kind]);

  const onKey = (e: ReactKeyboardEvent<HTMLInputElement>): void => {
    if (e.key === "Enter") {
      e.preventDefault();
      onSubmit(value);
    } else if (e.key === "Escape") {
      e.preventDefault();
      onCancel();
    }
  };

  const title =
    kind === "mkdir" ? "新建文件夹" :
    kind === "rename" ? "重命名" : "删除";

  const inputLabel =
    kind === "mkdir" ? "文件夹名" :
    kind === "rename" ? "新名称" : "";

  return (
    <div
      className="absolute inset-0 z-10 flex items-center justify-center bg-black/40 backdrop-blur-sm"
      onClick={onCancel}
      role="dialog"
      aria-modal
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-[400px] max-w-[90vw] rounded-lg border border-moss-border bg-moss-surface shadow-2xl"
      >
        <div className="border-b border-moss-border px-4 py-2.5 text-sm font-semibold text-ink">
          {title}
        </div>
        <div className="space-y-2 px-4 py-3 text-xs">
          {kind === "delete" ? (
            <p className="text-ink-muted">
              确认删除 <code className="font-mono text-state-err">{target?.name}</code> ？
              <br />
              <span className="text-[11px] text-ink-subtle">此操作不可撤销（sftp 协议层无回收站）。</span>
            </p>
          ) : (
            <>
              <label className="block text-ink-muted">{inputLabel}</label>
              <input
                ref={inputRef}
                type="text"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                onKeyDown={onKey}
                className="w-full rounded border border-moss-border bg-moss-bg px-2 py-1 font-mono text-xs text-ink outline-none focus:border-accent"
                spellCheck={false}
                autoComplete="off"
              />
            </>
          )}
        </div>
        <div className="flex items-center justify-end gap-2 border-t border-moss-border bg-moss-bg px-4 py-2.5">
          <button
            onClick={onCancel}
            className="rounded border border-moss-border bg-moss-surface px-3 py-1 text-xs text-ink-muted hover:bg-moss-hover hover:text-ink"
          >
            取消
          </button>
          {kind === "delete" ? (
            <button
              onClick={() => onSubmit("")}
              className="inline-flex items-center gap-1 rounded border border-state-err/40 bg-state-err/15 px-3 py-1 text-xs text-state-err hover:bg-state-err/25"
            >
              <Trash2 size={12} aria-hidden />
              确认删除
            </button>
          ) : (
            <button
              onClick={() => onSubmit(value)}
              className="rounded bg-accent px-3 py-1 text-xs font-medium text-moss-bg hover:bg-accent-600"
            >
              确定
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

// =====================================================================
// Local formatters
// =====================================================================

function formatModTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const pad = (n: number): string => n.toString().padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
         `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function formatPerms(mode: number): string {
  const perms = ["---", "--x", "-w-", "-wx", "r--", "r-x", "rw-", "rwx"];
  const m = mode & 0o7777;
  return (
    perms[(m >> 6) & 0b111] +
    perms[(m >> 3) & 0b111] +
    perms[m & 0b111]
  );
}
