/**
 * PreviewPanel —— SFTP 文件预览面板（v0.5.9 新增）
 * --------------------------------------------------------------------
 * 在 SFTP 浏览器双击文件时弹出，替代 v0.5.1 的简单 TextViewer。
 *
 * 流程：
 *   1. 打开 → App.SftpGetFileMetadata(sessionID, path) 拿 {size, mime, kind, ...}
 *   2. 按 kind 分发：
 *      - image  → App.SftpReadFileChunk(0, min(size, 5MB)) → base64 data URL → <img>
 *      - pdf    → App.SftpReadFileChunk(0, min(size, 2MB)) → extractPdfTextSnippet 片段
 *      - text   → App.SftpReadFileChunk(0, min(size, 256KB)) → TextDecoder → <pre>
 *      - binary → 仅元信息 + "下载"按钮（v0.6+ 接入）
 *      - toolarge (>50MB) → 仅元信息
 *   3. 文本分支额外提供 "text / hex" 切换：hex 模式走 hexDump()
 *
 * 设计要点：
 *   - **不替换** SftpBrowser 列表：渲染在 SftpBrowserContent 之上（绝对覆盖）
 *   - Esc 关闭
 *   - backdrop 点击关闭
 *   - 大文件保护：后端 PreviewMaxBytes = 50 MiB hard cap；前端也做同样的兜底
 *   - 文本阈值：> 5 MiB 不按文本渲染（kind 自动为 binary）
 *   - "下载"按钮：v0.5.9 仅复制路径到剪贴板 + toast 提示，v0.6+ 接 saveAs
 *
 * 依赖：
 *   - App（wails binding）
 *   - useUIStore（pushToast）
 *   - preview.ts 的 pure helpers（routePreviewKind / hexDump / extractPdfTextSnippet / uint8ArrayToBase64）
 *
 * 测试：
 *   - preview.ts 的 pure 函数由 preview.test.ts 覆盖
 *   - 本组件本身通过 SftpBrowserContent 集成测试覆盖（手动 / 后续 vitest）
 */
import {
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  AlertTriangle,
  Copy,
  Download,
  Eye,
  FileText,
  Image as ImageIcon,
  Loader2,
  X,
} from "lucide-react";
import clsx from "clsx";
import { App } from "@wails/go/main/App";
import { useUIStore } from "@stores/uiStore";
import { formatBytes } from "@utils/format";
import { logger } from "@utils/logger";
import type { SessionID } from "@/types/session";
import {
  PREVIEW_IMAGE_MAX_BYTES,
  PREVIEW_PDF_MAX_BYTES,
  PREVIEW_TEXT_RENDER_MAX,
  extractPdfTextSnippet,
  hexDump,
  routePreviewKind,
  uint8ArrayToBase64,
  type PreviewKind,
  type PreviewMetadata,
} from "./preview";

export interface PreviewPanelProps {
  sessionID: SessionID | null;
  /** 远端绝对路径 */
  path: string;
  /** 关闭回调（用户点 X / Esc / backdrop） */
  onClose: () => void;
}

type LoadState =
  | { phase: "loading" }
  | { phase: "ready"; kind: PreviewKind; meta: PreviewMetadata }
  | { phase: "error"; message: string };

type TextMode = "text" | "hex";

export function PreviewPanel({
  sessionID,
  path,
  onClose,
}: PreviewPanelProps): JSX.Element | null {
  const pushToast = useUIStore((s) => s.pushToast);
  const [load, setLoad] = useState<LoadState>({ phase: "loading" });
  const [imageDataUrl, setImageDataUrl] = useState<string | null>(null);
  const [textContent, setTextContent] = useState<string | null>(null);
  const [textRaw, setTextRaw] = useState<Uint8Array | null>(null);
  const [textMode, setTextMode] = useState<TextMode>("text");
  const [pdfInfo, setPdfInfo] = useState<ReturnType<typeof extractPdfTextSnippet> | null>(null);
  const hasFetchedRef = useRef<string>("");

  // 加载预览：先 metadata → 再按 kind 加载内容
  useEffect(() => {
    if (!sessionID) return;
    const key = `${sessionID}:${path}`;
    if (hasFetchedRef.current === key) return;
    hasFetchedRef.current = key;

    void (async (): Promise<void> => {
      setLoad({ phase: "loading" });
      setImageDataUrl(null);
      setTextContent(null);
      setTextRaw(null);
      setTextMode("text");
      setPdfInfo(null);

      try {
        const meta = await App.SftpGetFileMetadata(sessionID, path);
        const kind = routePreviewKind(meta);
        setLoad({ phase: "ready", kind, meta });

        if (kind === "image") {
          const size = Math.min(meta.size, PREVIEW_IMAGE_MAX_BYTES);
          const data = await App.SftpReadFileChunk(sessionID, path, 0, size);
          const mime = meta.mimeType && meta.mimeType !== "application/octet-stream"
            ? meta.mimeType
            : "image/png"; // 兜底；后端 magic 已识别为 image
          setImageDataUrl(`data:${mime};base64,${uint8ArrayToBase64(data)}`);
        } else if (kind === "text") {
          const size = Math.min(meta.size, PREVIEW_TEXT_RENDER_MAX);
          const data = await App.SftpReadFileChunk(sessionID, path, 0, size);
          setTextRaw(data);
          setTextContent(new TextDecoder("utf-8").decode(data));
        } else if (kind === "pdf") {
          const size = Math.min(meta.size, PREVIEW_PDF_MAX_BYTES);
          const data = await App.SftpReadFileChunk(sessionID, path, 0, size);
          // PDF 内部字节流是 latin1（PDF spec）
          const raw = new TextDecoder("latin1").decode(data);
          setPdfInfo(extractPdfTextSnippet(raw));
        }
        // binary / toolarge：仅元信息，不读字节
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        logger.error(`[PreviewPanel] load ${path} failed: ${msg}`);
        setLoad({ phase: "error", message: msg });
      }
    })();
  }, [sessionID, path]);

  // Esc 关闭
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const onCopyPath = useCallback((): void => {
    void navigator.clipboard.writeText(path).then(
      () => pushToast({ level: "success", message: "路径已复制到剪贴板", durationMs: 1500 }),
      () => pushToast({ level: "error", message: "复制失败", durationMs: 2000 }),
    );
  }, [path, pushToast]);

  if (!sessionID) return null;

  return (
    <div
      role="dialog"
      aria-modal
      aria-labelledby="preview-panel-title"
      className="absolute inset-0 z-20 flex items-center justify-center bg-black/50 backdrop-blur-sm"
      onClick={onClose}
      data-testid="preview-panel"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="flex h-[88%] w-[88%] max-w-[1200px] flex-col overflow-hidden rounded-lg border border-moss-border bg-moss-surface shadow-2xl"
        data-testid="preview-panel-frame"
      >
        {/* ===== Header ===== */}
        <div className="flex shrink-0 items-center justify-between gap-2 border-b border-moss-border bg-moss-bg px-4 py-2">
          <div className="flex min-w-0 items-center gap-2 text-xs">
            <KindIcon kind={load.phase === "ready" ? load.kind : null} />
            <span id="preview-panel-title" className="truncate font-mono text-ink">
              {load.phase === "ready" ? load.meta.name : path.split("/").pop() ?? path}
            </span>
            {load.phase === "ready" && (
              <span className="shrink-0 text-ink-muted">
                · {formatBytes(load.meta.size)}
                {load.meta.mimeType && (
                  <span className="ml-1 text-ink-subtle">· {load.meta.mimeType}</span>
                )}
              </span>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <button
              onClick={onCopyPath}
              className="inline-flex items-center gap-1 rounded border border-moss-border bg-moss-surface px-2 py-1 text-[11px] text-ink-muted hover:bg-moss-hover hover:text-ink"
              title="复制远端路径"
            >
              <Copy size={11} aria-hidden />
              复制路径
            </button>
            <DownloadButton
              meta={load.phase === "ready" ? load.meta : null}
              onToast={pushToast}
            />
            <button
              onClick={onClose}
              className="rounded p-1 text-ink-muted hover:bg-moss-hover hover:text-ink"
              title="关闭（Esc）"
              aria-label="关闭预览"
            >
              <X size={14} />
            </button>
          </div>
        </div>

        {/* ===== Body ===== */}
        <div className="flex-1 min-h-0 overflow-auto bg-moss-bg" data-testid="preview-panel-body">
          {load.phase === "loading" && <LoadingView />}
          {load.phase === "error" && <ErrorView message={load.message} />}
          {load.phase === "ready" && load.kind === "image" && (
            <ImageView dataUrl={imageDataUrl} meta={load.meta} />
          )}
          {load.phase === "ready" && load.kind === "text" && (
            <TextView
              text={textContent}
              raw={textRaw}
              mode={textMode}
              onModeChange={setTextMode}
              meta={load.meta}
            />
          )}
          {load.phase === "ready" && load.kind === "pdf" && (
            <PdfView info={pdfInfo} meta={load.meta} />
          )}
          {load.phase === "ready" && load.kind === "binary" && (
            <BinaryView meta={load.meta} />
          )}
          {load.phase === "ready" && load.kind === "toolarge" && (
            <TooLargeView meta={load.meta} />
          )}
        </div>
      </div>
    </div>
  );
}

// =====================================================================
// Sub-components（private）
// =====================================================================

function KindIcon({ kind }: { kind: PreviewKind | null }): JSX.Element {
  if (kind === "image") return <ImageIcon size={14} className="text-accent" aria-hidden />;
  if (kind === "pdf") return <FileText size={14} className="text-state-err" aria-hidden />;
  if (kind === "text") return <FileText size={14} className="text-accent" aria-hidden />;
  if (kind === "binary") return <Eye size={14} className="text-ink-muted" aria-hidden />;
  if (kind === "toolarge") return <AlertTriangle size={14} className="text-state-warn" aria-hidden />;
  return <Loader2 size={14} className="animate-spin text-ink-muted" aria-hidden />;
}

function LoadingView(): JSX.Element {
  return (
    <div className="flex h-full items-center justify-center text-ink-muted">
      <div className="flex flex-col items-center gap-2">
        <Loader2 size={20} className="animate-spin text-accent" aria-hidden />
        <span className="text-[11px]">加载中…</span>
      </div>
    </div>
  );
}

function ErrorView({ message }: { message: string }): JSX.Element {
  return (
    <div className="flex h-full items-center justify-center p-6 text-ink-muted">
      <div className="max-w-md rounded border border-state-err/40 bg-state-err/10 p-4 text-center">
        <AlertTriangle size={20} className="mx-auto mb-2 text-state-err" aria-hidden />
        <p className="text-xs text-state-err">加载失败</p>
        <p className="mt-2 break-all font-mono text-[10px] text-ink-muted">{message}</p>
      </div>
    </div>
  );
}

function ImageView({ dataUrl, meta }: { dataUrl: string | null; meta: PreviewMetadata }): JSX.Element {
  if (!dataUrl) {
    return (
      <div className="flex h-full items-center justify-center text-ink-muted">
        <Loader2 size={20} className="animate-spin" aria-hidden />
      </div>
    );
  }
  const wasTruncated = meta.size > PREVIEW_IMAGE_MAX_BYTES;
  return (
    <div className="flex h-full flex-col items-center gap-2 p-4">
      <div className="flex flex-1 items-center justify-center overflow-auto">
        <img
          src={dataUrl}
          alt={meta.name}
          className="max-h-full max-w-full object-contain"
          data-testid="preview-image"
        />
      </div>
      {wasTruncated && (
        <p className="text-[10px] text-ink-subtle">
          原文件 {formatBytes(meta.size)} · 已截断到 {formatBytes(PREVIEW_IMAGE_MAX_BYTES)} 渲染
        </p>
      )}
    </div>
  );
}

function TextView({
  text,
  raw,
  mode,
  onModeChange,
  meta,
}: {
  text: string | null;
  raw: Uint8Array | null;
  mode: TextMode;
  onModeChange: (m: TextMode) => void;
  meta: PreviewMetadata;
}): JSX.Element {
  if (text === null || raw === null) {
    return (
      <div className="flex h-full items-center justify-center text-ink-muted">
        <Loader2 size={20} className="animate-spin" aria-hidden />
      </div>
    );
  }
  const wasTruncated = meta.size > PREVIEW_TEXT_RENDER_MAX;
  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 items-center gap-1 border-b border-moss-border bg-moss-bg px-3 py-1.5 text-[11px]">
        <ModeTab active={mode === "text"} onClick={() => onModeChange("text")}>文本</ModeTab>
        <ModeTab active={mode === "hex"} onClick={() => onModeChange("hex")}>Hex</ModeTab>
        <span className="ml-auto text-ink-muted">
          {wasTruncated
            ? `已截断到 ${formatBytes(PREVIEW_TEXT_RENDER_MAX)} / 原 ${formatBytes(meta.size)}`
            : formatBytes(meta.size)}
        </span>
      </div>
      <div className="flex-1 min-h-0 overflow-auto p-3">
        {mode === "text" ? (
          <pre
            className="whitespace-pre-wrap break-all font-mono text-[11px] text-ink"
            data-testid="preview-text"
          >
            {text}
            {wasTruncated && (
              <span className="block pt-2 text-[10px] text-ink-subtle">
                （已截断；原文件 {formatBytes(meta.size)}）
              </span>
            )}
          </pre>
        ) : (
          <pre
            className="whitespace-pre font-mono text-[11px] text-ink"
            data-testid="preview-hex"
          >
            {hexDump(raw)}
          </pre>
        )}
      </div>
    </div>
  );
}

function ModeTab({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}): JSX.Element {
  return (
    <button
      onClick={onClick}
      className={clsx(
        "rounded px-2 py-0.5 text-[11px]",
        active
          ? "bg-accent/15 text-ink"
          : "text-ink-muted hover:bg-moss-hover hover:text-ink",
      )}
    >
      {children}
    </button>
  );
}

function PdfView({
  info,
  meta,
}: {
  info: ReturnType<typeof extractPdfTextSnippet> | null;
  meta: PreviewMetadata;
}): JSX.Element {
  if (!info) {
    return (
      <div className="flex h-full items-center justify-center text-ink-muted">
        <Loader2 size={20} className="animate-spin" aria-hidden />
      </div>
    );
  }
  const fragments = info.textFragments;
  return (
    <div className="flex h-full flex-col gap-3 p-4 text-[12px] text-ink" data-testid="preview-pdf">
      <div className="rounded border border-moss-border bg-moss-surface p-3 text-[11px]">
        <p className="text-ink-muted">
          v0.5.9 PDF 预览为 best-effort 文本片段提取（不引 pdf.js）。
          完整渲染留 v0.6+。
        </p>
        <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[11px]">
          {info.header && (
            <>
              <dt className="text-ink-muted">Header</dt>
              <dd className="font-mono">{info.header}</dd>
            </>
          )}
          {info.pageCount && (
            <>
              <dt className="text-ink-muted">Pages</dt>
              <dd className="font-mono">{info.pageCount}</dd>
            </>
          )}
          {info.title && (
            <>
              <dt className="text-ink-muted">Title</dt>
              <dd>{info.title}</dd>
            </>
          )}
          {info.author && (
            <>
              <dt className="text-ink-muted">Author</dt>
              <dd>{info.author}</dd>
            </>
          )}
          {info.subject && (
            <>
              <dt className="text-ink-muted">Subject</dt>
              <dd>{info.subject}</dd>
            </>
          )}
          <dt className="text-ink-muted">Size</dt>
          <dd className="font-mono">{formatBytes(meta.size)}</dd>
        </dl>
      </div>
      {fragments.length === 0 ? (
        <p className="text-[11px] text-ink-subtle">
          （未在头部 {formatBytes(PREVIEW_PDF_MAX_BYTES)} 中提取到文本片段）
        </p>
      ) : (
        <div className="rounded border border-moss-border bg-moss-surface p-3">
          <p className="mb-2 text-[10px] uppercase tracking-wider text-ink-subtle">
            提取的文本片段（{fragments.length}）
          </p>
          <ul className="space-y-1 text-[12px] leading-relaxed text-ink">
            {fragments.map((f, i) => (
              <li key={i} className="border-l-2 border-moss-border pl-2 font-mono">
                {f}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function BinaryView({ meta }: { meta: PreviewMetadata }): JSX.Element {
  return (
    <div className="flex h-full items-center justify-center p-6 text-ink-muted">
      <div className="max-w-md rounded border border-moss-border bg-moss-surface p-6 text-center">
        <FileText size={32} className="mx-auto mb-3 text-ink-subtle" aria-hidden />
        <p className="text-sm text-ink">二进制文件</p>
        <p className="mt-1 text-[11px] text-ink-muted">
          {meta.mimeType || meta.ext || "application/octet-stream"} · {formatBytes(meta.size)}
        </p>
        <p className="mt-3 text-[10px] text-ink-subtle">
          v0.5.9 不内联预览二进制。下载功能留 v0.6+ 接入。
        </p>
      </div>
    </div>
  );
}

function TooLargeView({ meta }: { meta: PreviewMetadata }): JSX.Element {
  return (
    <div className="flex h-full items-center justify-center p-6 text-ink-muted">
      <div className="max-w-md rounded border border-state-warn/40 bg-state-warn/10 p-6 text-center">
        <AlertTriangle size={32} className="mx-auto mb-3 text-state-warn" aria-hidden />
        <p className="text-sm text-state-warn">文件过大，禁止预览</p>
        <p className="mt-1 font-mono text-[11px] text-ink-muted">
          {meta.name} · {formatBytes(meta.size)}
        </p>
        <p className="mt-3 text-[10px] text-ink-subtle">
          v0.5.9 预览上限 {formatBytes(50 * 1024 * 1024)}（{meta.mimeType || "?"}）。
          完整传输 / 大文件预览留 v0.6+ streaming 接入。
        </p>
      </div>
    </div>
  );
}

function DownloadButton({
  meta,
  onToast,
}: {
  meta: PreviewMetadata | null;
  onToast: (t: { level: "info" | "warn"; message: string; durationMs: number }) => void;
}): JSX.Element {
  const onClick = useCallback((): void => {
    if (!meta) return;
    if (meta.size > 1 * 1024 * 1024) {
      onToast({ level: "warn", message: "下载功能 v0.6+ 接入（> 1 MiB 需要 streaming）", durationMs: 3000 });
      return;
    }
    onToast({ level: "info", message: "v0.5.9 仅支持复制路径 + 后续 SftpRead；v0.6+ 接 saveAs", durationMs: 3000 });
  }, [meta, onToast]);
  return (
    <button
      onClick={onClick}
      disabled={!meta}
      className="inline-flex items-center gap-1 rounded border border-moss-border bg-moss-surface px-2 py-1 text-[11px] text-ink-muted hover:bg-moss-hover hover:text-ink disabled:opacity-40"
      title="下载（v0.6+ 接入）"
    >
      <Download size={11} aria-hidden />
      下载
    </button>
  );
}
