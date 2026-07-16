/**
 * PdfRenderer —— v0.6.4 真实 PDF 渲染
 * --------------------------------------------------------------------
 * 替代 v0.5.9 best-effort 文本片段提取：接 pdfjs-dist 6.x，渲染当前 page
 * 到 <canvas>，支持翻页 + zoom。
 *
 * 设计要点：
 *   - 接受 ArrayBuffer（来自 SFTP ReadFileChunk），不依赖 DOM File / URL.createObjectURL
 *   - workerSrc 用 new URL(... import.meta.url) 拼接，wails webview（assets:// 协议）下走相对路径
 *   - page change 取消 in-flight 渲染（防快速翻页 race）
 *   - 错误边界：load 失败显示错误（不 throw 到 React）
 *
 * 不在范围（v0.6.4 暂不做）：
 *   - 缩略图侧栏（v0.7+）
 *   - 文本层点击高亮 / 搜索（v0.7+）
 *   - 表单填写 / 注释（v0.8+）
 *   - 加密 PDF（v0.8+）
 */
import { useEffect, useRef, useState, useCallback } from "react";
import * as pdfjs from "pdfjs-dist";
// Vite 5 + ESM：把 worker 路径打包成独立 chunk；
// 运行时 new URL(... import.meta.url) 在 wails webview 下走 assets:// 相对路径。
import PdfWorker from "pdfjs-dist/build/pdf.worker.min.mjs?url";

// 全局一次性设置 workerSrc（pdfjs 推荐）
pdfjs.GlobalWorkerOptions.workerSrc = PdfWorker;

export interface PdfRendererProps {
  /** PDF 二进制（来自 SFTP read）；调用方负责 clone 避免 mutation */
  data: ArrayBuffer;
  /** 初始 page（1-indexed）；默认 1 */
  initialPage?: number;
  /** 缩放；默认 1.0 */
  initialScale?: number;
}

type LoadState =
  | { phase: "loading" }
  | { phase: "ready"; numPages: number }
  | { phase: "error"; message: string };

export function PdfRenderer({
  data,
  initialPage = 1,
  initialScale = 1.0,
}: PdfRendererProps): JSX.Element {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const [state, setState] = useState<LoadState>({ phase: "loading" });
  const [pageNum, setPageNum] = useState(initialPage);
  const [scale, setScale] = useState(initialScale);
  // 当前渲染任务的 abort flag（快速翻页时取消上一次的 page.render()）
  const renderTokenRef = useRef(0);
  // pdfjs 文档句柄（unmount 时 destroy）
  const docRef = useRef<pdfjs.PDFDocumentProxy | null>(null);

  // ----- 加载文档 -----
  //
  // v0.6.4 实证：pdfjs-dist 6.x 的 PDFDocumentProxy **没有 destroy() 方法**；
  // 文档句柄由 PDFDocumentLoadingTask 持有（getDocument 返回值），worker
  // 端资源在 promise 链 + GC 释放。卸载时只清 ref，依赖 browser GC。
  useEffect(() => {
    let cancelled = false;
    setState({ phase: "loading" });
    // pdfjs 6.x 接受 transferred ArrayBuffer；我们不 transfer 让 caller 自己决定
    pdfjs
      .getDocument({ data: new Uint8Array(data) })
      .promise.then((doc) => {
        if (cancelled) {
          return;
        }
        docRef.current = doc;
        setState({ phase: "ready", numPages: doc.numPages });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const msg = err instanceof Error ? err.message : String(err);
        setState({ phase: "error", message: msg });
      });
    return () => {
      cancelled = true;
      docRef.current = null;
    };
  }, [data]);

  // ----- 渲染当前 page -----
  const renderPage = useCallback(async () => {
    const doc = docRef.current;
    const canvas = canvasRef.current;
    if (!doc || !canvas) return;
    // 类型守卫：state 必须是 ready 才走 render
    if (state.phase !== "ready") return;
    const numPages: number = state.numPages;
    if (pageNum < 1 || pageNum > numPages) return;

    const token = ++renderTokenRef.current;
    try {
      const page = await doc.getPage(pageNum);
      if (token !== renderTokenRef.current) {
        await page.cleanup();
        return; // 被新的翻页抢断
      }
      const viewport = page.getViewport({ scale });
      // DPR 缩放让 canvas 物理像素跟 CSS 像素对齐（高 DPI 屏不糊）
      const dpr = window.devicePixelRatio || 1;
      canvas.width = Math.floor(viewport.width * dpr);
      canvas.height = Math.floor(viewport.height * dpr);
      canvas.style.width = `${Math.floor(viewport.width)}px`;
      canvas.style.height = `${Math.floor(viewport.height)}px`;
      const ctx = canvas.getContext("2d");
      if (!ctx) {
        setState({ phase: "error", message: "canvas 2d context unavailable" });
        return;
      }
      ctx.scale(dpr, dpr);
      const renderContext = {
        canvasContext: ctx,
        canvas,
        viewport,
      };
      await page.render(renderContext).promise;
      // render 完成后再 check token（防止 await 期间被新翻页抢断）
      if (token !== renderTokenRef.current) return;
      await page.cleanup();
    } catch (err) {
      if (token !== renderTokenRef.current) return;
      const msg = err instanceof Error ? err.message : String(err);
      setState({ phase: "error", message: msg });
    }
  }, [pageNum, scale, state]);

  useEffect(() => {
    void renderPage();
  }, [renderPage]);

  // ----- UI -----
  if (state.phase === "loading") {
    return (
      <div
        className="flex h-full items-center justify-center text-[11px] text-ink-muted"
        data-testid="pdf-renderer-loading"
      >
        PDF 加载中…
      </div>
    );
  }

  if (state.phase === "error") {
    return (
      <div
        className="flex h-full flex-col items-center justify-center gap-2 p-4 text-[12px] text-state-err"
        data-testid="pdf-renderer-error"
      >
        <p>PDF 渲染失败</p>
        <pre className="max-w-md whitespace-pre-wrap rounded bg-moss-surface p-2 font-mono text-[10px]">
          {state.message}
        </pre>
      </div>
    );
  }

  // ready
  return (
    <div
      className="flex h-full flex-col gap-2 overflow-hidden"
      data-testid="pdf-renderer"
    >
      {/* 工具栏：翻页 + 缩放 */}
      <div className="flex items-center gap-2 border-b border-moss-border px-2 py-1 text-[11px] text-ink-muted">
        <button
          type="button"
          disabled={pageNum <= 1}
          onClick={() => setPageNum((n) => Math.max(1, n - 1))}
          className="rounded border border-moss-border bg-moss-surface px-2 py-0.5 hover:bg-moss-hover disabled:opacity-40"
          title="上一页"
        >
          ‹ Prev
        </button>
        <span className="font-mono">
          {pageNum} / {state.numPages}
        </span>
        <button
          type="button"
          disabled={pageNum >= state.numPages}
          onClick={() => setPageNum((n) => Math.min(state.numPages, n + 1))}
          className="rounded border border-moss-border bg-moss-surface px-2 py-0.5 hover:bg-moss-hover disabled:opacity-40"
          title="下一页"
        >
          Next ›
        </button>
        <span className="mx-2 text-ink-subtle">|</span>
        <button
          type="button"
          onClick={() => setScale((s) => Math.max(0.25, +(s - 0.25).toFixed(2)))}
          className="rounded border border-moss-border bg-moss-surface px-2 py-0.5 hover:bg-moss-hover"
          title="缩小"
        >
          −
        </button>
        <span className="font-mono w-12 text-center">{Math.round(scale * 100)}%</span>
        <button
          type="button"
          onClick={() => setScale((s) => Math.min(4.0, +(s + 0.25).toFixed(2)))}
          className="rounded border border-moss-border bg-moss-surface px-2 py-0.5 hover:bg-moss-hover"
          title="放大"
        >
          ＋
        </button>
      </div>
      {/* canvas 区 */}
      <div className="flex-1 min-h-0 overflow-auto bg-moss-bg p-2">
        <canvas ref={canvasRef} data-testid="pdf-canvas" />
      </div>
    </div>
  );
}
