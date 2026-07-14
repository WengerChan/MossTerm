/**
 * SftpBrowser —— 独立 Modal 形式的 SFTP 浏览器
 * --------------------------------------------------------------------
 * v0.5.1 范围：
 *   - 列目录（List）+ 面包屑导航
 *   - 双击目录进入
 *   - 双击文本文件查看（utf-8 decode；二进制显示 "binary file, N bytes"）
 *   - 新建文件夹（Mkdir）
 *   - 删除（Remove，弹确认）
 *   - 重命名（Rename，弹输入）
 *   - 上一级 / 刷新
 *
 * 不做（v0.6+）：拖拽上传、大文件分片 / 进度条、搜索 / 过滤、多选、
 * 真实分页（后端 v0.5.1 仍一次性返回）。
 *
 * 集成方式：
 *   - 挂在 App 顶层（与 TrustRequestModal 同级）
 *   - 通过 sftpBrowserStore 控制开/关
 *   - 接收当前 sessionID（来自 store）；session 关闭后 store 清空，
 *     浏览器自动隐藏。
 *
 * 与既有 SftpPanel 的关系：
 *   - SftpPanel 是 v0.1 写的右侧抽屉 stub（带虚拟 jobs 等），
 *     仍保留代码但当前不挂载
 *   - SftpBrowser 是 v0.5.1 真正的"文件管理"UI，独立 Modal
 *   - 二者通过 sftpStore 解耦（共享 list 缓存等未来可能复用）
 *
 * 设计要点：
 *   - 不依赖 uiStore.modal —— 子对话框（mkdir/rename/delete/查看）直接
 *     渲染在 SftpBrowser 内部，避免 uiStore 单 modal slot 的状态冲突
 *   - 错误用 toast 提示（短），关键阻塞用 inline 提示（带 spinner 关闭）
 *   - 关闭浏览器 = Esc / backdrop click / 显式 X —— 跟 TrustRequestModal
 *     不同，这里允许 backdrop 关闭
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
  X,
  FolderOpen,
  ChevronRight,
} from "lucide-react";
import clsx from "clsx";
import { App, type Entry, type ListPage } from "@wails/go/main/App";
import { useUIStore } from "@stores/uiStore";
import { useConnectionStore } from "@stores/connectionStore";
import { formatBytes } from "@utils/format";
import { logger } from "@utils/logger";
import type { SessionID } from "@types/session";

// =====================================================================
// Constants
// =====================================================================

/** List 调用时的分页参数。v0.5.1 后端不分页，固定一个较大值够用。 */
const LIST_PAGE_SIZE = 500;

/** 文本判定的最大探针字节数（避免大文件 decode 浪费时间）。 */
const TEXT_PROBE_BYTES = 4096;

/** 文本可打印字符比例阈值。 */
const TEXT_PRINTABLE_THRESHOLD = 0.85;

/** 最大文本预览字节数（避免超长文件卡死 UI）。 */
const TEXT_PREVIEW_MAX_BYTES = 256 * 1024; // 256 KiB

// =====================================================================
// Types
// =====================================================================

/** 子对话框类型：'mkdir' / 'rename' / 'delete'。null = 不显示。 */
type DialogKind = "mkdir" | "rename" | "delete" | null;

interface DialogState {
  kind: Exclude<DialogKind, null>;
  /** 'rename' 时是当前 entry，'delete' 时是要删的 entry。 */
  target?: Entry;
  /** 'mkdir' 时是当前路径；'rename' 时是当前 entry 的 dir。 */
  contextPath: string;
}

type SortKey = "name" | "size" | "modTime";
type SortDir = "asc" | "desc";

// =====================================================================
// Path helpers
// =====================================================================

/** 规范化路径：去尾部斜杠（保留根的 "/"）。 */
function normalizePath(p: string): string {
  if (!p) return "/";
  if (p === "/") return "/";
  return p.replace(/\/+$/, "") || "/";
}

/** 拼接 parent + name（处理 "/" 根 + 重复斜杠）。 */
function joinPath(parent: string, name: string): string {
  const p = normalizePath(parent);
  if (p === "/") return `/${name}`;
  return `${p}/${name}`;
}

/** 父路径；已在根则返回 null。 */
function parentPath(p: string): string | null {
  const n = normalizePath(p);
  if (n === "/") return null;
  const idx = n.lastIndexOf("/");
  if (idx <= 0) return "/";
  return n.slice(0, idx);
}

/** 把路径切成段（含根 "/"，最末段是当前 dir）。用于面包屑。 */
function splitSegments(p: string): string[] {
  const n = normalizePath(p);
  if (n === "/") return ["/"];
  return ["/", ...n.slice(1).split("/").filter(Boolean)];
}

// =====================================================================
// Text detection
// =====================================================================

/**
 * 启发式判断 Uint8Array 是否像文本（utf-8 可解码 + 可打印比例达标）。
 * 不做 BOM 嗅探 / 编码猜测，简化即可。
 */
function looksLikeText(bytes: Uint8Array): boolean {
  const probe = bytes.subarray(0, Math.min(bytes.length, TEXT_PROBE_BYTES));
  // 出现 NUL 字节 → 几乎肯定是二进制
  for (let i = 0; i < probe.length; i++) {
    if (probe[i] === 0) return false;
  }
  // 解码
  let decoded: string;
  try {
    decoded = new TextDecoder("utf-8", { fatal: true }).decode(probe);
  } catch {
    return false;
  }
  if (decoded.length === 0) return true;
  let printable = 0;
  for (let i = 0; i < decoded.length; i++) {
    const c = decoded.charCodeAt(i);
    // ASCII 可见 + 常见控制（\t \n \r \f \v）
    if (
      (c >= 0x20 && c < 0x7f) ||
      c === 0x09 || c === 0x0a || c === 0x0d || c === 0x0c || c === 0x0b ||
      c >= 0x80 // 多字节字符也算"可读"
    ) {
      printable += 1;
    }
  }
  return printable / decoded.length >= TEXT_PRINTABLE_THRESHOLD;
}

// =====================================================================
// SftpBrowser
// =====================================================================

export interface SftpBrowserProps {
  /** 由父组件传入，控制可见性。 */
  open: boolean;
  /** 关闭回调。 */
  onClose: () => void;
  /** 浏览器绑定的 session id。null 时浏览器不渲染内容（也无意义）。 */
  sessionID: SessionID | null;
}

export function SftpBrowser({ open, onClose, sessionID }: SftpBrowserProps): JSX.Element | null {
  const pushToast = useUIStore((s) => s.pushToast);
  const session   = useConnectionStore((s) =>
    sessionID ? s.sessions[sessionID] : undefined,
  );

  // 目录状态
  const [path, setPath] = useState<string>("/");
  const [entries, setEntries] = useState<Entry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selectedPath, setSelectedPath] = useState<string | null>(null);

  // 排序（目录始终优先；同类型内排序）
  const [sortKey, setSortKey] = useState<SortKey>("name");
  const [sortDir, setSortDir] = useState<SortDir>("asc");

  // 子对话框
  const [dialog, setDialog] = useState<DialogState | null>(null);

  // 文本查看器
  const [viewer, setViewer] = useState<{ name: string; content: string; binary: boolean; size: number } | null>(null);

  // 用于防止 stale 响应：每次 cd/refresh 分配一个 token，回调时校验。
  const reqTokenRef = useRef(0);

  // ---------- Effects ----------

  // open 变化：关闭时清空状态（不保留上次浏览的位置）
  useEffect(() => {
    if (!open) {
      setPath("/");
      setEntries([]);
      setSelectedPath(null);
      setError(null);
      setDialog(null);
      setViewer(null);
    }
  }, [open]);

  // sessionID 变化：重置
  useEffect(() => {
    setPath("/");
    setEntries([]);
    setSelectedPath(null);
    setError(null);
    setDialog(null);
    setViewer(null);
  }, [sessionID]);

  // 初始 / 路径变化时拉取
  const listDir = useCallback(
    async (target: string) => {
      if (!sessionID) return;
      const token = ++reqTokenRef.current;
      setLoading(true);
      setError(null);
      try {
        const page: ListPage = await App.SftpList(sessionID, target, LIST_PAGE_SIZE, "");
        if (token !== reqTokenRef.current) return; // stale
        setEntries(page.entries ?? []);
        setSelectedPath(null);
      } catch (err: unknown) {
        if (token !== reqTokenRef.current) return;
        const msg = err instanceof Error ? err.message : String(err);
        logger.error(`[SftpBrowser] List ${target} failed: ${msg}`);
        setError(msg);
        setEntries([]);
      } finally {
        if (token === reqTokenRef.current) setLoading(false);
      }
    },
    [sessionID],
  );

  useEffect(() => {
    if (open && sessionID) {
      void listDir(path);
    }
  }, [open, sessionID, path, listDir]);

  // Esc 关浏览器（子对话框打开时不关浏览器，而是关子对话框）
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key !== "Escape") return;
      if (dialog) {
        setDialog(null);
        e.preventDefault();
        return;
      }
      if (viewer) {
        setViewer(null);
        e.preventDefault();
        return;
      }
      onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, dialog, viewer, onClose]);

  // ---------- Navigation ----------

  const cd = useCallback((next: string) => {
    setPath(normalizePath(next));
  }, []);

  const cdUp = useCallback(() => {
    const p = parentPath(path);
    if (p !== null) cd(p);
  }, [path, cd]);

  const onBreadcrumbClick = useCallback((segIndex: number) => {
    // segIndex 0 = "/"
    const segs = splitSegments(path);
    if (segs.length === 0) return;
    if (segIndex === 0) {
      cd("/");
      return;
    }
    // 重新拼接到 segIndex 那层
    const next = "/" + segs.slice(1, segIndex + 1).join("/");
    cd(next);
  }, [path, cd]);

  // ---------- Entry interactions ----------

  const onEntryClick = useCallback((e: Entry) => {
    setSelectedPath(e.path);
  }, []);

  const onEntryDouble = useCallback(async (e: Entry) => {
    if (e.isDir) {
      cd(e.path);
      return;
    }
    // 文件 → 读取 + 查看
    if (!sessionID) return;
    try {
      const data = await App.SftpRead(sessionID, e.path);
      if (looksLikeText(data)) {
        // 截断预览
        const slice = data.length > TEXT_PREVIEW_MAX_BYTES
          ? data.subarray(0, TEXT_PREVIEW_MAX_BYTES)
          : data;
        const content = new TextDecoder("utf-8").decode(slice);
        setViewer({
          name: e.name,
          content,
          binary: false,
          size: e.size,
        });
      } else {
        setViewer({
          name: e.name,
          content: "",
          binary: true,
          size: e.size,
        });
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      pushToast({ level: "error", message: `读取 ${e.name} 失败：${msg}`, durationMs: 5000 });
    }
  }, [sessionID, cd, pushToast]);

  // ---------- Actions ----------

  const refresh = useCallback(() => {
    void listDir(path);
  }, [path, listDir]);

  const promptMkdir = useCallback(() => {
    setDialog({ kind: "mkdir", contextPath: path });
  }, [path]);

  const promptRename = useCallback((e: Entry) => {
    setDialog({ kind: "rename", target: e, contextPath: e.path });
  }, []);

  const promptDelete = useCallback((e: Entry) => {
    setDialog({ kind: "delete", target: e, contextPath: path });
  }, [path]);

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

  // ---------- Derived ----------

  const sorted = useMemo(() => {
    const dirs = entries.filter((e) => e.isDir);
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

  if (!open) return null;
  if (!sessionID) {
    // store 给到 sessionID 但被父组件 null 覆盖，理论不会发生 —— 兜底
    return null;
  }

  const hostLabel = session
    ? `${session.user}@${session.host}:${session.port}`
    : sessionID;

  // ---------- Render ----------

  return (
    <div
      role="dialog"
      aria-modal
      aria-labelledby="sftp-browser-title"
      className="fixed inset-0 z-40 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      onClick={onClose}
      data-testid="sftp-browser"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="relative flex h-[640px] max-h-[90vh] w-[920px] max-w-[95vw] flex-col overflow-hidden rounded-lg border border-moss-border bg-moss-surface shadow-2xl"
      >
        {/* ===== Header ===== */}
        <div className="flex items-center justify-between border-b border-moss-border px-4 py-2.5">
          <div className="flex items-center gap-2 text-sm">
            <FolderOpen size={16} className="text-accent" aria-hidden />
            <h2 id="sftp-browser-title" className="font-semibold text-ink">
              SFTP 浏览器
            </h2>
            <span className="text-ink-muted">·</span>
            <code className="font-mono text-[11px] text-ink-muted">{hostLabel}</code>
          </div>
          <button
            onClick={onClose}
            className="rounded p-1 text-ink-muted hover:bg-moss-hover hover:text-ink"
            title="关闭（Esc）"
            aria-label="关闭"
          >
            <X size={14} />
          </button>
        </div>

        {/* ===== Toolbar: 上 / 刷 / 新建 / 面包屑 ===== */}
        <div className="flex items-center gap-2 border-b border-moss-border bg-moss-bg px-3 py-2 text-xs">
          <button
            onClick={cdUp}
            disabled={loading || parentPath(path) === null}
            className="rounded border border-moss-border bg-moss-surface p-1 text-ink-muted hover:bg-moss-hover hover:text-ink disabled:opacity-40"
            title="上一级"
            aria-label="上一级"
          >
            <ChevronUp size={14} />
          </button>
          <button
            onClick={refresh}
            disabled={loading}
            className="rounded border border-moss-border bg-moss-surface p-1 text-ink-muted hover:bg-moss-hover hover:text-ink disabled:opacity-40"
            title="刷新"
            aria-label="刷新"
          >
            <RefreshCw size={14} className={loading ? "animate-spin" : ""} />
          </button>
          <button
            onClick={promptMkdir}
            disabled={loading}
            className="inline-flex items-center gap-1 rounded border border-moss-border bg-moss-surface px-2 py-1 text-ink-muted hover:bg-moss-hover hover:text-ink disabled:opacity-40"
            title="新建文件夹"
          >
            <FolderPlus size={12} aria-hidden />
            新建文件夹
          </button>

          {/* 面包屑 */}
          <Breadcrumb path={path} onJump={onBreadcrumbClick} />

          {/* 错误提示（如果有） */}
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

        {/* ===== Sub-dialogs (rendered as overlay inside SftpBrowser) ===== */}
        {dialog && (
          <SubDialog
            kind={dialog.kind}
            target={dialog.target}
            onCancel={() => setDialog(null)}
            onSubmit={(v) => void onDialogSubmit(v)}
          />
        )}

        {/* ===== Text viewer ===== */}
        {viewer && (
          <TextViewer viewer={viewer} onClose={() => setViewer(null)} />
        )}
      </div>
    </div>
  );
}

// =====================================================================
// Sub-components
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
          {i > 0 && (
            <ChevronRight size={10} className="text-ink-subtle" aria-hidden />
          )}
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

/**
 * 子对话框（覆盖在 SftpBrowser 内容上）。
 *  - mkdir: 输入名称
 *  - rename: 输入新名称（预填旧名）
 *  - delete: 确认（无输入框）
 */
function SubDialog({ kind, target, onCancel, onSubmit }: SubDialogProps): JSX.Element {
  const [value, setValue] = useState<string>(
    kind === "rename" && target ? target.name : "",
  );
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (kind === "delete") return; // 无 input
    // 聚焦 + 选中全部
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
    kind === "rename" ? "重命名" :
    "删除";

  const inputLabel =
    kind === "mkdir" ? "文件夹名" :
    kind === "rename" ? "新名称" :
    "";

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

interface TextViewerProps {
  viewer: { name: string; content: string; binary: boolean; size: number };
  onClose: () => void;
}

function TextViewer({ viewer, onClose }: TextViewerProps): JSX.Element {
  return (
    <div
      className="absolute inset-0 z-10 flex items-center justify-center bg-black/40 backdrop-blur-sm"
      onClick={onClose}
      role="dialog"
      aria-modal
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="flex h-[80%] w-[80%] max-w-[95vw] flex-col overflow-hidden rounded-lg border border-moss-border bg-moss-surface shadow-2xl"
      >
        <div className="flex items-center justify-between border-b border-moss-border px-4 py-2">
          <div className="flex items-center gap-2 text-xs">
            <Eye size={12} className="text-accent" aria-hidden />
            <span className="font-mono text-ink">{viewer.name}</span>
            <span className="text-ink-muted">· {formatBytes(viewer.size)}</span>
          </div>
          <button
            onClick={onClose}
            className="rounded p-1 text-ink-muted hover:bg-moss-hover hover:text-ink"
            title="关闭（Esc）"
          >
            <X size={14} />
          </button>
        </div>
        <div className="flex-1 min-h-0 overflow-auto bg-moss-bg p-3 text-[11px]">
          {viewer.binary ? (
            <div className="flex h-full items-center justify-center text-ink-muted">
              <div className="text-center">
                <File size={32} className="mx-auto mb-2 text-ink-subtle" aria-hidden />
                <p>
                  binary file, {formatBytes(viewer.size)}
                </p>
                <p className="mt-1 text-[10px] text-ink-subtle">
                  v0.5.1 不支持二进制预览 / 下载
                </p>
              </div>
            </div>
          ) : (
            <pre className="whitespace-pre-wrap break-all font-mono text-ink">
              {viewer.content}
              {viewer.size > TEXT_PREVIEW_MAX_BYTES && (
                <span className="block pt-2 text-[10px] text-ink-subtle">
                  （已截断到 {formatBytes(TEXT_PREVIEW_MAX_BYTES)}；原文件 {formatBytes(viewer.size)}）
                </span>
              )}
            </pre>
          )}
        </div>
      </div>
    </div>
  );
}

// =====================================================================
// Local formatters
// =====================================================================

/** RFC3339 → "YYYY-MM-DD HH:mm"（与 formatAbsoluteTime 一致，但输入是字符串）。 */
function formatModTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const pad = (n: number): string => n.toString().padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
         `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** 数字 mode (0755) → "rwxr-xr-x"（前导类型位省略，只展示 9 位）。 */
function formatPerms(mode: number): string {
  const perms = ["---", "--x", "-w-", "-wx", "r--", "r-x", "rw-", "rwx"];
  const m = mode & 0o7777;
  return (
    perms[(m >> 6) & 0b111] +
    perms[(m >> 3) & 0b111] +
    perms[m & 0b111]
  );
}
