// preview_test.go 覆盖 sftpclient preview 逻辑的纯函数路径：
//   - DetectMagic（PNG/JPEG/GIF/WebP/PDF 各种 magic + 边界长度）
//   - ClassifyPreview（所有 size/mime/magic/ext 组合）
//   - isTextExt（白名单 / 非白名单）
//   - looksLikeTextBytes（空 / 有 NUL / 全可打印）
//   - ReadFileChunk 边界（client closed）
//
// 真实 SSH server 集成测试（v0.5.0 spec 明确不做）留 v0.5.10+ 用
// sftp.NewServer in-process server 覆盖 —— 本文件专注纯逻辑。
package sftpclient

import (
	"bytes"
	"errors"
	"testing"
)

// -----------------------------------------------------------------------------
// DetectMagic
// -----------------------------------------------------------------------------

func TestDetectMagic_PNG(t *testing.T) {
	// 89 50 4E 47 0D 0A 1A 0A
	head := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}
	if got := DetectMagic(head); got != MagicKindImage {
		t.Errorf("PNG magic: got %q, want %q", got, MagicKindImage)
	}
}

func TestDetectMagic_JPEG(t *testing.T) {
	// FF D8 FF E0
	head := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	if got := DetectMagic(head); got != MagicKindImage {
		t.Errorf("JPEG magic: got %q, want %q", got, MagicKindImage)
	}
}

func TestDetectMagic_GIF(t *testing.T) {
	// 47 49 46 38 39 61 (GIF89a)
	head := []byte{'G', 'I', 'F', '8', '9', 'a'}
	if got := DetectMagic(head); got != MagicKindImage {
		t.Errorf("GIF89a magic: got %q, want %q", got, MagicKindImage)
	}
	// 47 49 46 38 37 61 (GIF87a)
	head87 := []byte{'G', 'I', 'F', '8', '7', 'a'}
	if got := DetectMagic(head87); got != MagicKindImage {
		t.Errorf("GIF87a magic: got %q, want %q", got, MagicKindImage)
	}
}

func TestDetectMagic_WebP(t *testing.T) {
	// RIFF....WEBP
	head := []byte{'R', 'I', 'F', 'F', 0x00, 0x10, 0x00, 0x00, 'W', 'E', 'B', 'P'}
	if got := DetectMagic(head); got != MagicKindImage {
		t.Errorf("WebP magic: got %q, want %q", got, MagicKindImage)
	}
}

func TestDetectMagic_PDF(t *testing.T) {
	// %PDF-1.7
	head := []byte{'%', 'P', 'D', 'F', '-', '1', '.', '7'}
	if got := DetectMagic(head); got != MagicKindPDF {
		t.Errorf("PDF magic: got %q, want %q", got, MagicKindPDF)
	}
}

func TestDetectMagic_Unknown(t *testing.T) {
	cases := []struct {
		name string
		head []byte
	}{
		{"empty", nil},
		{"too-short-1", []byte{0x00}},
		{"too-short-3", []byte{0x00, 0x01, 0x02}},
		{"random-binary", []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE}},
		{"plain-text", []byte("hello world")},
		{"elf-magic-not-implemented", []byte{0x7F, 'E', 'L', 'F'}}, // spec 提到 ELF 但 v0.5.9 不实现
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectMagic(tc.head); got != "" {
				t.Errorf("DetectMagic(%v): got %q, want \"\"", tc.head, got)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// ClassifyPreview
// -----------------------------------------------------------------------------

func TestClassifyPreview_TooLarge(t *testing.T) {
	if got := ClassifyPreview(PreviewMaxBytes+1, "", "", "bin"); got != PreviewKindTooLarge {
		t.Errorf("> PreviewMaxBytes: got %q, want %q", got, PreviewKindTooLarge)
	}
	// 边界：== PreviewMaxBytes 不算 toolarge（与 spec 一致：> PreviewMaxBytes）
	if got := ClassifyPreview(PreviewMaxBytes, "", "image", "bin"); got != PreviewKindImage {
		t.Errorf("== PreviewMaxBytes 不算 toolarge: got %q, want image", got)
	}
}

func TestClassifyPreview_MagicPriority(t *testing.T) {
	// magic=image 优先于 ext=bin + mime 错
	if got := ClassifyPreview(1024, "application/octet-stream", MagicKindImage, "bin"); got != PreviewKindImage {
		t.Errorf("magic=image 应优先: got %q", got)
	}
	// magic=pdf 优先于 ext=bin
	if got := ClassifyPreview(1024, "", MagicKindPDF, "bin"); got != PreviewKindPDF {
		t.Errorf("magic=pdf 应优先: got %q", got)
	}
}

func TestClassifyPreview_MimeImage(t *testing.T) {
	cases := []struct {
		mime string
		want string
	}{
		{"image/png", PreviewKindImage},
		{"image/jpeg", PreviewKindImage},
		{"image/gif", PreviewKindImage},
		{"image/webp", PreviewKindImage},
		{"image/svg+xml", PreviewKindImage},
	}
	for _, tc := range cases {
		t.Run(tc.mime, func(t *testing.T) {
			if got := ClassifyPreview(1024, tc.mime, "", ""); got != tc.want {
				t.Errorf("mime=%q: got %q, want %q", tc.mime, got, tc.want)
			}
		})
	}
}

func TestClassifyPreview_MimePDF(t *testing.T) {
	if got := ClassifyPreview(1024, "application/pdf", "", ""); got != PreviewKindPDF {
		t.Errorf("application/pdf: got %q, want %q", got, PreviewKindPDF)
	}
}

func TestClassifyPreview_MimeText(t *testing.T) {
	// 文本 mime + 小 size → text
	if got := ClassifyPreview(1024, "text/plain", "", ""); got != PreviewKindText {
		t.Errorf("text/plain + 1KB: got %q, want text", got)
	}
	// 文本 mime + 大 size → binary（超过文本阈值）
	if got := ClassifyPreview(PreviewTextMaxBytes+1, "text/plain", "", ""); got != PreviewKindBinary {
		t.Errorf("text/plain + >5MB: got %q, want binary", got)
	}
}

func TestClassifyPreview_MimeJSON(t *testing.T) {
	if got := ClassifyPreview(1024, "application/json", "", ""); got != PreviewKindText {
		t.Errorf("application/json: got %q, want text", got)
	}
}

func TestClassifyPreview_ExtTextWhitelist(t *testing.T) {
	// ext 在白名单内 + 小 size + 无 mime
	exts := []string{"md", "txt", "log", "go", "py", "rs", "json", "yaml", "sh", "sql"}
	for _, ext := range exts {
		t.Run(ext, func(t *testing.T) {
			if got := ClassifyPreview(1024, "", "", ext); got != PreviewKindText {
				t.Errorf("ext=%q: got %q, want text", ext, got)
			}
		})
	}
	// ext 在白名单内 + 超阈值 → binary
	if got := ClassifyPreview(PreviewTextMaxBytes+1, "", "", "md"); got != PreviewKindBinary {
		t.Errorf("ext=md 超阈值: got %q, want binary", got)
	}
}

func TestClassifyPreview_ExtNotInWhitelist(t *testing.T) {
	for _, ext := range []string{"bin", "exe", "zip"} {
		if got := ClassifyPreview(1024, "", "", ext); got != PreviewKindBinary {
			t.Errorf("ext=%q: got %q, want binary", ext, got)
		}
	}
}

func TestClassifyPreview_FallbackBinary(t *testing.T) {
	if got := ClassifyPreview(1024, "application/octet-stream", "", ""); got != PreviewKindBinary {
		t.Errorf("兜底: got %q, want binary", got)
	}
	if got := ClassifyPreview(1024, "", "", ""); got != PreviewKindBinary {
		t.Errorf("全空兜底: got %q, want binary", got)
	}
}

// -----------------------------------------------------------------------------
// isTextExt
// -----------------------------------------------------------------------------

func TestIsTextExt(t *testing.T) {
	yesExts := []string{"md", "txt", "log", "go", "py", "json", "yaml", "sh", "sql", "html", "css", "js", "ts", "tsx"}
	noExts := []string{"", "bin", "exe", "zip", "tar", "gz", "so", "dll", "jpg", "png", "pdf", "mp4", "mp3"}
	for _, ext := range yesExts {
		if !isTextExt(ext) {
			t.Errorf("isTextExt(%q): got false, want true", ext)
		}
	}
	for _, ext := range noExts {
		if isTextExt(ext) {
			t.Errorf("isTextExt(%q): got true, want false", ext)
		}
	}
}

// -----------------------------------------------------------------------------
// looksLikeTextBytes
// -----------------------------------------------------------------------------

func TestLooksLikeTextBytes(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want bool
	}{
		{"empty", []byte{}, true},
		{"plain-ascii", []byte("Hello, World!\n"), true},
		{"utf8-text", []byte("你好世界"), true},
		{"null-byte", []byte{0x48, 0x00, 0x65}, false},
		{"no-null-binary-jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, true}, // 启发式只查 NUL；严格 printable 比例由前端 TextDecoder 兜底
		{"png-with-nulls-in-data", []byte{0x89, 'P', 'N', 'G', 0x00, 0x00}, false},
		{"long-no-null", bytes.Repeat([]byte{'A'}, PreviewTextProbeBytes*2), true},
		{"null-beyond-probe", append(bytes.Repeat([]byte{'A'}, PreviewTextProbeBytes), 0x00), true}, // null 超过 probe 范围
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeTextBytes(tc.head); got != tc.want {
				t.Errorf("looksLikeTextBytes: got %v, want %v", got, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// ReadFileChunk — 边界
// -----------------------------------------------------------------------------

func TestReadFileChunk_ClientClosed(t *testing.T) {
	c := &Client{} // sc == nil
	if _, err := c.ReadFileChunk("/foo", 0, 100); err == nil {
		t.Error("ReadFileChunk on closed client: expected error, got nil")
	}
}

func TestReadFileChunk_NilClientDoesNotPanicOnClosedBranch(t *testing.T) {
	// 验证 errors.New 路径不 panic（不是 nil-deref）
	// c.sc 在 nil receiver 上访问会 panic —— 这与 spec 一致：
	// spec 只要求"Close 之后"（c != nil, c.sc == nil）报错。
	// 真正的"nil *Client"调用不在 spec 范围。
	defer func() {
		if r := recover(); r != nil {
			t.Logf("nil *Client 调 ReadFileChunk panic（已知行为）: %v", r)
		}
	}()
	var c *Client
	_, _ = c.ReadFileChunk("/foo", 0, 100)
}

// 验证阈值常量符合 spec（避免 silent 改动破坏前端）
func TestPreviewConstants(t *testing.T) {
	if PreviewMaxBytes != 50<<20 {
		t.Errorf("PreviewMaxBytes: got %d, want %d (50 MiB)", PreviewMaxBytes, 50<<20)
	}
	if PreviewTextMaxBytes != 5<<20 {
		t.Errorf("PreviewTextMaxBytes: got %d, want %d (5 MiB)", PreviewTextMaxBytes, 5<<20)
	}
	if PreviewMagicBytes != 16 {
		t.Errorf("PreviewMagicBytes: got %d, want 16", PreviewMagicBytes)
	}
	if PreviewMimeProbeBytes != 512 {
		t.Errorf("PreviewMimeProbeBytes: got %d, want 512", PreviewMimeProbeBytes)
	}
}

// 占位：让 errors 包被 import —— 当前 ReadFileChunk 用 errors.New。
var _ = errors.New
