/**
 * preview.ts —— SFTP 文件预览的纯函数 helpers（v0.5.9）
 * --------------------------------------------------------------------
 * 与 PreviewPanel.tsx 配对，抽出 pure functions 方便单测：
 *   - PreviewKind union 类型
 *   - PreviewMetadata 类型（镜像 backend PreviewMetadata struct）
 *   - routePreviewKind(meta)         — 根据 size/kind 走哪个分支
 *   - uint8ArrayToBase64(bytes)      — Uint8Array → base64（data URL 用）
 *   - hexDump(bytes, maxBytes)       — xxd 风格 16 字节/行 hex+ascii
 *   - extractPdfTextSnippet(raw)     — best-effort PDF 文本片段提取
 *
 * 与 PreviewPanel.tsx 的关系：
 *   - PreviewPanel 调这些函数实现各分支的 UI
 *   - 测试（preview.test.ts）直接 import 这些函数，不依赖 React
 */

// =====================================================================
// Types
// =====================================================================

/** 后端 PreviewKind union —— 与 internal/sftpclient/preview.go 对齐 */
export type PreviewKind =
  | "image"
  | "pdf"
  | "text"
  | "binary"
  | "toolarge";

/** 后端 PreviewMetadata 镜像 —— Wails 序列化的 JSON shape */
export interface PreviewMetadata {
  path: string;
  name: string;
  size: number;
  mode: number;
  modTime: string;
  mimeType: string;
  kind: PreviewKind | "";
  ext: string;
  isImage: boolean;
  isPDF: boolean;
  isText: boolean;
}

// =====================================================================
// Constants（与 backend sftpclient preview.go 对齐）
// =====================================================================

/** 后端硬上限：超过此值禁止读字节 */
export const PREVIEW_MAX_BYTES = 50 * 1024 * 1024;

/** 文本渲染上限（与后端 PreviewTextMaxBytes 对齐） */
export const PREVIEW_TEXT_MAX_BYTES = 5 * 1024 * 1024;

/** 图片 base64 编码上限：超过此值不内联显示，仅元信息 */
export const PREVIEW_IMAGE_MAX_BYTES = 5 * 1024 * 1024;

/** 文本预览加载字节数：超过截断显示 */
export const PREVIEW_TEXT_RENDER_MAX = 256 * 1024;

/** PDF 头部预览字节数：仅用于 best-effort 文本提取（v0.5.9 留存） */
export const PREVIEW_PDF_MAX_BYTES = 2 * 1024 * 1024;

/** PDF 真实渲染加载字节数：v0.6.4 起前端用 pdfjs-dist 完整渲染；超过此值走 toolarge 兜底。 */
export const PREVIEW_PDF_MAX_FULL_BYTES = 50 * 1024 * 1024;

/** hex dump 渲染最大字节数：超过截断 */
export const PREVIEW_HEX_DUMP_MAX = 16 * 1024;

// =====================================================================
// routePreviewKind —— 路由分发
// =====================================================================

/**
 * 根据 PreviewMetadata 决定走哪个渲染分支。
 *
 * 后端已经分类（meta.kind），前端再做一次 size 校验作为 belt-and-suspenders：
 * 防止后端版本不一致 / meta.kind 字段缺失时前端仍能兜底。
 */
export function routePreviewKind(meta: Pick<PreviewMetadata, "size" | "kind" | "isImage" | "isPDF" | "isText">): PreviewKind {
  if (meta.size > PREVIEW_MAX_BYTES) return "toolarge";
  switch (meta.kind) {
    case "image":
      return "image";
    case "pdf":
      return "pdf";
    case "text":
      return "text";
    case "binary":
      return "binary";
    case "toolarge":
      return "toolarge";
  }
  // kind 字段缺失时按便利字段兜底（与后端 IsImage/IsPDF/IsText 对齐）
  if (meta.isImage) return "image";
  if (meta.isPDF) return "pdf";
  if (meta.isText) return "text";
  return "binary";
}

// =====================================================================
// uint8ArrayToBase64 —— Uint8Array → base64 string
// =====================================================================

/**
 * Uint8Array → base64 字符串（用于 data URL）。
 *
 * 实现：循环 + String.fromCharCode 拼成 binary string → btoa() 编码。
 * 不使用 FileReader.readAsDataURL 是为了支持 Uint8Array 而非 Blob。
 *
 * 性能：5 MB 大约 50ms（V8 实测），够 preview 用。
 */
export function uint8ArrayToBase64(bytes: Uint8Array): string {
  let binary = "";
  // chunked 避免 String 长度爆炸（V8 内部单 string 限制 ~512MB 没问题，
  // 但循环 + concat 对小数组更快）
  const CHUNK = 0x8000; // 32 KiB
  for (let i = 0; i < bytes.length; i += CHUNK) {
    const slice = bytes.subarray(i, Math.min(i + CHUNK, bytes.length));
    let s = "";
    for (let j = 0; j < slice.length; j++) {
      s += String.fromCharCode(slice[j] as number);
    }
    binary += s;
  }
  return btoa(binary);
}

// =====================================================================
// hexDump —— xxd 风格 hex+ascii
// =====================================================================

/**
 * 把 bytes 渲染成 xxd 风格 hex dump。
 *
 * 格式：每行 16 字节
 *   offset  | hex (8 组 × 2 hex + 1 空格) | ascii
 *   00000000: 8950 4e47 0d0a 1a0a 0000 000d 4948 4452  .PNG........IHDR
 *
 * maxBytes：超过此值截断 + 末尾标注 "... (truncated, total N bytes)"。
 * 默认 PREVIEW_HEX_DUMP_MAX = 16 KiB。
 */
export function hexDump(bytes: Uint8Array, maxBytes: number = PREVIEW_HEX_DUMP_MAX): string {
  const limit = Math.min(bytes.length, maxBytes);
  const lines: string[] = [];
  for (let off = 0; off < limit; off += 16) {
    const end = Math.min(off + 16, limit);
    const slice = bytes.subarray(off, end);
    // hex part: 8 组 × "xx "（最后一组可能只有 1-8 字节）
    const hexParts: string[] = [];
    for (let i = 0; i < 16; i++) {
      if (i < slice.length) {
        hexParts.push((slice[i] as number).toString(16).padStart(2, "0"));
      } else {
        hexParts.push("  ");
      }
      if (i === 7) hexParts.push(""); // 8 字节处分隔空格
    }
    const hexStr = hexParts.join(" ");
    // ascii part
    let ascii = "";
    for (let i = 0; i < slice.length; i++) {
      const b = slice[i] as number;
      ascii += b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : ".";
    }
    lines.push(`${off.toString(16).padStart(8, "0")}: ${hexStr}  ${ascii}`);
  }
  if (bytes.length > maxBytes) {
    lines.push(`... (truncated, total ${bytes.length} bytes)`);
  }
  return lines.join("\n");
}

// =====================================================================
// extractPdfTextSnippet —— best-effort PDF 文本片段
// =====================================================================

/**
 * 从 PDF 头部字节提取可读文本片段。
 *
 * 限制：v0.5.9 不引 pdf.js 库（spec 明确）；这里用正则 best-effort
 * 提取 metadata + content stream 文本。
 *
 * 提取内容：
 *  1. /Title (...) /Author (...) /Subject (...) —— metadata dictionary
 *  2. /Pages N 或 /Count N —— 页数（部分 PDF 用 /Count）
 *  3. ((...)) Tj 文本操作符的字符串（content stream 内的真实文字）
 *
 * 输入：latin1 编码的字符串（PDF 内部字节流是 latin1 + 偶尔 UTF-16 BE BOM）
 */
export function extractPdfTextSnippet(raw: string): {
  title: string;
  author: string;
  subject: string;
  pageCount: string;
  textFragments: string[];
  header: string;
} {
  const result = {
    title: "",
    author: "",
    subject: "",
    pageCount: "",
    textFragments: [] as string[],
    header: "",
  };

  // 1. metadata dictionary
  const m1 = raw.match(/\/Title\s*<([^>]+)>/);
  if (m1 && m1[1]) {
    // Hex-encoded UTF-16 BE
    try {
      result.title = decodeHexUtf16BE(m1[1]);
    } catch {
      result.title = m1[1];
    }
  } else {
    const m2 = raw.match(/\/Title\s*\(([^)]*)\)/);
    if (m2 && m2[1]) result.title = m2[1];
  }
  const mAuthor = raw.match(/\/Author\s*\(([^)]*)\)/);
  if (mAuthor && mAuthor[1]) result.author = mAuthor[1];
  const mSubject = raw.match(/\/Subject\s*\(([^)]*)\)/);
  if (mSubject && mSubject[1]) result.subject = mSubject[1];

  // 2. page count (从 /Type /Pages 字典中取 /Count)
  const mCount = raw.match(/\/Count\s+(\d+)/);
  if (mCount && mCount[1]) result.pageCount = mCount[1];

  // 3. content stream 内的 Tj 文本（best-effort，正则全局匹配）
  // 匹配 ((...)) Tj 或 <hex> Tj；这里只处理括号版本（最常见）
  const tjRegex = /\(((?:\\.|[^()\\])*)\)\s*Tj/g;
  let tm: RegExpExecArray | null;
  let count = 0;
  while ((tm = tjRegex.exec(raw)) !== null && count < 30) {
    const t = tm[1];
    if (t) {
      const unescaped = t
        .replace(/\\n/g, "\n")
        .replace(/\\r/g, "\r")
        .replace(/\\t/g, "\t")
        .replace(/\\\(/g, "(")
        .replace(/\\\)/g, ")")
        .replace(/\\\\/g, "\\");
      if (unescaped.trim().length > 0) {
        result.textFragments.push(unescaped);
        count += 1;
      }
    }
  }

  // 4. %PDF 头部（用于显示"已识别为 PDF"）
  const mHeader = raw.match(/(%PDF-\d+\.\d+)/);
  if (mHeader && mHeader[1]) result.header = mHeader[1];

  return result;
}

/**
 * 把 hex 字符串解码为 UTF-16 BE 字符串。
 *
 * PDF metadata 在 title/author 字段里有时用 <FEFF...> hex UTF-16 编码
 * （FEFF 是 BOM），对应 String.fromCharCode 逐字节 pair 解码。
 */
function decodeHexUtf16BE(hex: string): string {
  const clean = hex.replace(/\s+/g, "");
  if (clean.length % 4 !== 0) return hex;
  let out = "";
  for (let i = 0; i < clean.length; i += 4) {
    const code = parseInt(clean.substring(i, i + 4), 16);
    if (Number.isNaN(code)) return hex;
    out += String.fromCharCode(code);
  }
  return out;
}
