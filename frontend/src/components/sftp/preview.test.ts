/**
 * preview.test.ts —— preview.ts pure helpers 的单测（v0.5.9）
 * --------------------------------------------------------------------
 * 跑法：node --experimental-strip-types --test src/components/sftp/preview.test.ts
 *
 * Node 22.6+ 内置 --experimental-strip-types 可直接跑 .ts（Node 26 GA），
 * 不依赖 vitest / tsx。与 v0.5.8 paneTree.test.ts 一致。
 *
 * 覆盖：
 *   - routePreviewKind：所有 kind 路径 + size 兜底
 *   - uint8ArrayToBase64：典型输入 + round-trip
 *   - hexDump：长度 / 截断 / ascii 不可打印字符
 *   - extractPdfTextSnippet：title / author / page count / Tj 文本片段
 *   - 阈值常量稳定性（防止 silent 改动破坏后端契约）
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  PREVIEW_MAX_BYTES,
  PREVIEW_TEXT_MAX_BYTES,
  extractPdfTextSnippet,
  hexDump,
  routePreviewKind,
  uint8ArrayToBase64,
  type PreviewMetadata,
} from "./preview.ts";

// =====================================================================
// routePreviewKind
// =====================================================================

function makeMeta(overrides: Partial<PreviewMetadata> = {}): PreviewMetadata {
  return {
    path: "/foo",
    name: "foo",
    size: 1024,
    mode: 0o644,
    modTime: "2025-01-01T00:00:00Z",
    mimeType: "",
    kind: "",
    ext: "",
    isImage: false,
    isPDF: false,
    isText: false,
    ...overrides,
  };
}

test("routePreviewKind: image kind routes to image", () => {
  const m = makeMeta({ kind: "image" });
  assert.equal(routePreviewKind(m), "image");
});

test("routePreviewKind: pdf kind routes to pdf", () => {
  const m = makeMeta({ kind: "pdf" });
  assert.equal(routePreviewKind(m), "pdf");
});

test("routePreviewKind: text kind routes to text", () => {
  const m = makeMeta({ kind: "text" });
  assert.equal(routePreviewKind(m), "text");
});

test("routePreviewKind: binary kind routes to binary", () => {
  const m = makeMeta({ kind: "binary" });
  assert.equal(routePreviewKind(m), "binary");
});

test("routePreviewKind: toolarge kind routes to toolarge", () => {
  const m = makeMeta({ kind: "toolarge" });
  assert.equal(routePreviewKind(m), "toolarge");
});

test("routePreviewKind: empty kind falls back to IsImage/IsPDF/IsText", () => {
  assert.equal(routePreviewKind(makeMeta({ isImage: true })), "image");
  assert.equal(routePreviewKind(makeMeta({ isPDF: true })), "pdf");
  assert.equal(routePreviewKind(makeMeta({ isText: true })), "text");
});

test("routePreviewKind: empty kind + no flags → binary", () => {
  assert.equal(routePreviewKind(makeMeta()), "binary");
});

test("routePreviewKind: size > 50 MiB always routes to toolarge", () => {
  const m = makeMeta({ size: 60 * 1024 * 1024, kind: "image" });
  assert.equal(routePreviewKind(m), "toolarge");
});

test("routePreviewKind: size == 50 MiB is not toolarge (boundary)", () => {
  const m = makeMeta({ size: 50 * 1024 * 1024, kind: "image" });
  assert.equal(routePreviewKind(m), "image");
});

// =====================================================================
// uint8ArrayToBase64
// =====================================================================

test("uint8ArrayToBase64: empty bytes returns empty string", () => {
  assert.equal(uint8ArrayToBase64(new Uint8Array(0)), "");
});

test("uint8ArrayToBase64: round-trips through atob", () => {
  // "Hello, World!" → 13 字节
  const input = new TextEncoder().encode("Hello, World!");
  const b64 = uint8ArrayToBase64(input);
  const decoded = atob(b64);
  assert.equal(decoded, "Hello, World!");
});

test("uint8ArrayToBase64: handles non-ASCII bytes", () => {
  // 0xC3 0xA9 = é (UTF-8 编码)
  const input = new Uint8Array([0xC3, 0xA9]);
  const b64 = uint8ArrayToBase64(input);
  // base64 of 0xC3A9 is "w6k="
  assert.equal(b64, "w6k=");
});

test("uint8ArrayToBase64: handles 32 KiB+ chunks (CHUNK boundary)", () => {
  // 构造 100 KiB 数据
  const size = 100 * 1024;
  const input = new Uint8Array(size);
  for (let i = 0; i < size; i++) input[i] = i & 0xff;
  const b64 = uint8ArrayToBase64(input);
  // 100 KiB = 102400 bytes → ceil(102400/3)*4 = 136536 chars
  // 不严格校验长度，只验证 round-trip
  const decoded = atob(b64);
  assert.equal(decoded.length, size);
  for (let i = 0; i < size; i++) {
    assert.equal(decoded.charCodeAt(i), input[i] as number, `byte ${i}`);
  }
});

// =====================================================================
// hexDump
// =====================================================================

test("hexDump: empty bytes returns empty string", () => {
  assert.equal(hexDump(new Uint8Array(0)), "");
});

test("hexDump: single line (16 bytes)", () => {
  // 0x00 0x01 0x02 ... 0x0F
  const input = new Uint8Array(16);
  for (let i = 0; i < 16; i++) input[i] = i;
  const out = hexDump(input);
  // 格式：每字节 xx + 单空格，8 字节处分隔多一个空格（产生 "  "）
  assert.equal(out, "00000000: 00 01 02 03 04 05 06 07  08 09 0a 0b 0c 0d 0e 0f  ................");
});

test("hexDump: second line starts at offset 0x10", () => {
  // 17 字节 → 第一行 16 字节 + 第二行 1 字节
  const input = new Uint8Array(17);
  input[16] = 0xFF;
  const out = hexDump(input);
  const lines = out.split("\n");
  assert.equal(lines.length, 2);
  assert.ok(lines[1]!.startsWith("00000010: "));
  assert.ok(lines[1]!.endsWith(" ."));
});

test("hexDump: ASCII printable characters shown as-is", () => {
  const input = new TextEncoder().encode("Hello");
  const out = hexDump(input);
  // 末尾 ASCII 段应包含 "Hello"
  assert.ok(out.includes("Hello"));
});

test("hexDump: non-printable bytes shown as '.'", () => {
  // 0xFF 0x00 0x01 → 都不可打印（0x00 是 NUL）
  const input = new Uint8Array([0xFF, 0x00, 0x01]);
  const out = hexDump(input);
  // ascii 段 "..."
  assert.ok(out.endsWith("..."));
});

test("hexDump: truncates at maxBytes with marker", () => {
  // 100 字节 + maxBytes=16 → 应截断 + "... (truncated, total 100 bytes)"
  const input = new Uint8Array(100);
  const out = hexDump(input, 16);
  assert.ok(out.includes("... (truncated, total 100 bytes)"));
});

test("hexDump: maxBytes larger than data does not truncate", () => {
  const input = new Uint8Array(10);
  const out = hexDump(input, 100);
  assert.ok(!out.includes("truncated"));
});

// =====================================================================
// extractPdfTextSnippet
// =====================================================================

test("extractPdfTextSnippet: extracts PDF header", () => {
  const raw = "%PDF-1.7\n%¥±ë\n1 0 obj\n<<\n/Type /Catalog\n>>\nendobj\n";
  const info = extractPdfTextSnippet(raw);
  assert.equal(info.header, "%PDF-1.7");
});

test("extractPdfTextSnippet: extracts page count", () => {
  const raw = "%PDF-1.7\n<< /Type /Pages /Count 42 /Kids [ ] >>\n";
  const info = extractPdfTextSnippet(raw);
  assert.equal(info.pageCount, "42");
});

test("extractPdfTextSnippet: extracts ASCII Title", () => {
  const raw = "%PDF-1.7\n<< /Title (Hello World) /Author (Alice) >>\n";
  const info = extractPdfTextSnippet(raw);
  assert.equal(info.title, "Hello World");
  assert.equal(info.author, "Alice");
});

test("extractPdfTextSnippet: extracts content stream Tj text fragments", () => {
  const raw = `
%PDF-1.7
BT
/F1 12 Tf
(Hello) Tj
(World) Tj
ET
`;
  const info = extractPdfTextSnippet(raw);
  assert.deepEqual(info.textFragments, ["Hello", "World"]);
});

test("extractPdfTextSnippet: unescapes PDF string escapes", () => {
  // \( = literal (，\\ = literal \
  const raw = `BT (foo\\(bar) Tj (back\\\\slash) Tj ET`;
  const info = extractPdfTextSnippet(raw);
  assert.equal(info.textFragments[0], "foo(bar");
  assert.equal(info.textFragments[1], "back\\slash");
});

test("extractPdfTextSnippet: empty PDF returns empty fragments", () => {
  const raw = "%PDF-1.7\n";
  const info = extractPdfTextSnippet(raw);
  assert.equal(info.header, "%PDF-1.7");
  assert.equal(info.textFragments.length, 0);
});

test("extractPdfTextSnippet: limits fragments to 30", () => {
  let raw = "%PDF-1.7\nBT\n";
  for (let i = 0; i < 50; i++) raw += `(t${i}) Tj `;
  raw += "ET\n";
  const info = extractPdfTextSnippet(raw);
  assert.equal(info.textFragments.length, 30);
});

// =====================================================================
// 阈值常量稳定性（防止 silent 改动破坏后端契约）
// =====================================================================

test("preview constants match backend contract", () => {
  // 与 internal/sftpclient/preview.go 对齐；任何改动需要同步两边
  assert.equal(PREVIEW_MAX_BYTES, 50 * 1024 * 1024, "PREVIEW_MAX_BYTES");
  assert.equal(PREVIEW_TEXT_MAX_BYTES, 5 * 1024 * 1024, "PREVIEW_TEXT_MAX_BYTES");
});
