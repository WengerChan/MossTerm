// Package sftpclient —— v0.5.9 文件预览支持
//
// 本文件提供：
//   - DetectMagic  从前几字节判断"实际类型"（PNG/JPEG/GIF/WebP/PDF magic）
//   - ClassifyPreview  size + mime + magic + ext 综合分类 → PreviewKind
//   - Client.ReadFileChunk  任意偏移读字节（受 PreviewMaxBytes hard cap 限制）
//   - Client.BuildPreviewMetadata  复合：Stat + 读前 16 字节 + mime 探测 + 分类
//   - PreviewMetadata  前端路由用的结构（wailsbinding 序列化）
//
// 设计要点：
//   - magic detection 优先级高于扩展名（用户把 .jpg 改成 .bin 仍能识别）
//   - mime 探测走 net/http.DetectContentType（前 512 字节），不引新依赖
//   - hard cap 50 MiB（PreviewMaxBytes）防恶意大文件读
//   - 文本限制 5 MiB（PreviewTextMaxBytes）—— 超过按 binary 处理
//   - looksLikeText 兜底无扩展名 + 无 mime 的纯文本（无 NUL 字节）
package sftpclient

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Preview 阈值常量（与 frontend/src/components/sftp/preview.ts 对齐）。
const (
	// PreviewMaxBytes 硬上限：超过此值拒绝读字节，仅返回元信息。
	// 50 MiB 是经验值：SFTP 协议 ReadFile 单次 RPC 推荐 < 64 KiB × 800 ≈ 50 MiB。
	// 真正传输走 v0.6+ streaming，本 binding 仅供 preview。
	PreviewMaxBytes = 50 << 20

	// PreviewTextMaxBytes 文本渲染上限：超过按 binary 处理（不读字节）。
	// 5 MiB 是浏览器可接受的内联文本（256 KiB-1 MiB 是最佳，5 MiB 兜底）。
	PreviewTextMaxBytes = 5 << 20

	// PreviewMagicBytes magic 探测最小字节数。
	PreviewMagicBytes = 16

	// PreviewMimeProbeBytes http.DetectContentType 推荐 512 字节。
	PreviewMimeProbeBytes = 512

	// PreviewTextProbeBytes 文本探测字节数（looksLikeText 范围）。
	PreviewTextProbeBytes = 4096
)

// PreviewKind 是前端的路由 kind 联合（与 frontend PreviewKind union 镜像）。
const (
	PreviewKindImage    = "image"    // jpg / png / gif / webp / svg
	PreviewKindPDF      = "pdf"      // application/pdf
	PreviewKindText     = "text"     // UTF-8 文本
	PreviewKindBinary   = "binary"   // 其他二进制 / 超文本大小限制
	PreviewKindTooLarge = "toolarge" // > PreviewMaxBytes
)

// MagicKind 是 DetectMagic 的返回值。
//
// 当前只区分 "image" / "pdf" / ""（无法识别）。
// "" 表示 magic 不匹配 —— 调用方应继续走 mime / ext 路径。
const (
	MagicKindImage = "image"
	MagicKindPDF   = "pdf"
)

// PreviewMetadata 是一次预览调用的复合返回（wailsbinding 用）。
//
// 字段全部导出（Wails 反射需要），时间序列化为 RFC3339 字符串（前端可解析）。
// IsImage/IsPDF/IsText 是 Kind 字段的冗余便利字段，节省前端 switch。
type PreviewMetadata struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Mode     uint32 `json:"mode"`
	ModTime  string `json:"modTime"`
	MimeType string `json:"mimeType"`
	// Kind 前端路由 kind：image / pdf / text / binary / toolarge
	Kind string `json:"kind"`
	// Ext 文件扩展名（小写，不含 .）；前端可做兜底判断
	Ext string `json:"ext"`
	// IsImage / IsPDF / IsText 便利字段（Kind === ...）
	IsImage bool `json:"isImage"`
	IsPDF   bool `json:"isPDF"`
	IsText  bool `json:"isText"`
}

// DetectMagic 从 head 字节判断"实际类型"。
//
// 仅识别"高置信度"magic（PNG/JPEG/GIF/WebP/PDF）。SVG 走 mime 探测 +
// 扩展名兜底（无固定 magic）。返回 MagicKind 字符串或 ""。
//
// 输入 head 长度 < 4 时返回 ""（数据不足以判断）。
func DetectMagic(head []byte) string {
	if len(head) < 4 {
		return ""
	}
	// PNG: 89 50 4E 47 0D 0A 1A 0A（8 字节完整 magic）
	if len(head) >= 8 && bytes.Equal(head[0:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) {
		return MagicKindImage
	}
	// JPEG: FF D8 FF
	if head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF {
		return MagicKindImage
	}
	// GIF: 47 49 46 38 (7|9)a
	if len(head) >= 6 && bytes.Equal(head[0:4], []byte{'G', 'I', 'F', '8'}) &&
		(head[4] == '7' || head[4] == '9') && head[5] == 'a' {
		return MagicKindImage
	}
	// WebP: 52 49 46 46 ?? ?? ?? ?? 57 45 42 50（RIFF....WEBP）
	if len(head) >= 12 && bytes.Equal(head[0:4], []byte{'R', 'I', 'F', 'F'}) &&
		bytes.Equal(head[8:12], []byte{'W', 'E', 'B', 'P'}) {
		return MagicKindImage
	}
	// PDF: 25 50 44 46 (%PDF)
	if len(head) >= 4 && bytes.Equal(head[0:4], []byte{'%', 'P', 'D', 'F'}) {
		return MagicKindPDF
	}
	return ""
}

// ClassifyPreview 把 (size, mime, magic, ext) 综合成 PreviewKind 字符串。
//
// 优先级：
//  1. size > PreviewMaxBytes → "toolarge"（hard cap，不读字节）
//  2. magic == "image" → "image"
//  3. magic == "pdf" → "pdf"
//  4. mime 以 "image/" 开头 → "image"
//  5. mime == "application/pdf" → "pdf"
//  6. mime 以 "text/" 开头 → size>5MB?"binary":"text"
//  7. mime == "application/json" → size>5MB?"binary":"text"
//  8. ext 在文本白名单内 → size>5MB?"binary":"text"
//  9. 兜底 "binary"
//
// 注意：本函数是 pure function，方便单测覆盖所有分支。
func ClassifyPreview(size int64, mime string, magicKind string, ext string) string {
	if size > PreviewMaxBytes {
		return PreviewKindTooLarge
	}
	switch magicKind {
	case MagicKindImage:
		return PreviewKindImage
	case MagicKindPDF:
		return PreviewKindPDF
	}
	if strings.HasPrefix(mime, "image/") {
		return PreviewKindImage
	}
	if mime == "application/pdf" {
		return PreviewKindPDF
	}
	if strings.HasPrefix(mime, "text/") {
		if size > PreviewTextMaxBytes {
			return PreviewKindBinary
		}
		return PreviewKindText
	}
	if mime == "application/json" {
		if size > PreviewTextMaxBytes {
			return PreviewKindBinary
		}
		return PreviewKindText
	}
	// 扩展名兜底（仅含常见代码/数据/配置扩展名）
	if isTextExt(ext) {
		if size > PreviewTextMaxBytes {
			return PreviewKindBinary
		}
		return PreviewKindText
	}
	return PreviewKindBinary
}

// isTextExt 判断 ext（小写，不含 .）是否在文本白名单。
//
// 白名单选自 GitHub linguist 的 "Text" 类别常见后缀；
// SVG 单独走 image/* mime 路径；本白名单仅覆盖"明确文本"后缀。
func isTextExt(ext string) bool {
	switch ext {
	case "md", "txt", "log", "csv", "tsv", "xml", "html", "htm",
		"css", "js", "jsx", "ts", "tsx", "mjs", "cjs",
		"json", "yaml", "yml", "toml", "ini", "conf", "cfg",
		"sh", "bash", "zsh", "fish", "ps1",
		"py", "rb", "php", "pl", "lua", "r",
		"go", "rs", "java", "kt", "scala", "groovy",
		"c", "h", "cpp", "cc", "cxx", "hpp", "hxx",
		"m", "mm", "swift", "dart", "cs", "fs",
		"sql", "graphql", "proto", "thrift",
		"env", "gitignore", "dockerignore", "editorconfig",
		"rst", "adoc", "tex", "bib":
		return true
	}
	return false
}

// looksLikeTextBytes 判断 head 字节是否像纯文本。
//
// 启发式：前 PreviewTextProbeBytes 字节内无 NUL（0x00）即视为文本。
// 更严格的 printable ratio 检查由前端 TextDecoder 兜底（避免重复）。
func looksLikeTextBytes(head []byte) bool {
	probe := head
	if len(probe) > PreviewTextProbeBytes {
		probe = probe[:PreviewTextProbeBytes]
	}
	for _, b := range probe {
		if b == 0 {
			return false
		}
	}
	return true
}

// ReadFileChunk 读取 [offset, offset+size) 字节。
//
// size > 0：精确 size 字节（截到 PreviewMaxBytes）
// size <= 0：读到 EOF（截到 PreviewMaxBytes 防御）
// offset < 0 → 视作 0
// offset > 文件实际 size → 返回空 slice
//
// 内部先 OpenFile + Stat 拿真实 size，再 Seek + ReadFull。
//
// 错误：
//   - client closed
//   - 打开远端文件失败（路径不存在 / 权限）
//   - Seek / Read 失败
func (c *Client) ReadFileChunk(p string, offset, size int64) ([]byte, error) {
	if c.sc == nil {
		return nil, errors.New("sftpclient.ReadFileChunk: client closed")
	}
	if offset < 0 {
		offset = 0
	}
	effectiveSize := size
	if effectiveSize > PreviewMaxBytes {
		effectiveSize = PreviewMaxBytes
	}
	f, err := c.sc.OpenFile(p, os.O_RDONLY)
	if err != nil {
		return nil, fmt.Errorf("sftpclient.ReadFileChunk: open %q: %w", p, err)
	}
	defer f.Close()

	// Stat 拿真实 size 供截断；OpenFile 后 stat 是额外的 RPC 但语义清晰
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("sftpclient.ReadFileChunk: stat %q: %w", p, err)
	}
	fileSize := fi.Size()
	if offset >= fileSize {
		return []byte{}, nil
	}

	// 计算实际可读字节数
	remaining := fileSize - offset
	if effectiveSize <= 0 || effectiveSize > remaining {
		effectiveSize = remaining
	}

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, fmt.Errorf("sftpclient.ReadFileChunk: seek %q to %d: %w", p, offset, err)
		}
	}
	if effectiveSize <= 0 {
		return []byte{}, nil
	}

	buf := make([]byte, effectiveSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("sftpclient.ReadFileChunk: read %q: %w", p, err)
	}
	return buf[:n], nil
}

// BuildPreviewMetadata 复合：Stat + 读前 16 字节 magic + mime 探测 + 分类。
//
// 调用栈（v0.5.9）：
//  1. 前端 PreviewPanel 调用 App.SftpGetFileMetadata(sessionID, path)
//  2. wailsbinding 调 Client.BuildPreviewMetadata
//  3. Stat + 读前 512 字节（一次 RPC）+ http.DetectContentType + ClassifyPreview
//  4. 返回 PreviewMetadata
//
// 注意事项：
//   - 即使 size > PreviewMaxBytes 也允许 —— 仍 Stat + 读 16 字节（够小）
//   - 文件不存在 / 权限错误 → 透传
//   - "lite" 版本（仅 size+mime，kind 留空）走 BuildPreviewMetadataLite
func (c *Client) BuildPreviewMetadata(p string) (PreviewMetadata, error) {
	return c.buildPreviewMetadataImpl(p, true)
}

// BuildPreviewMetadataLite 返回 size + mime（不做 magic / classify）。
//
// 用于 wailsbinding.SftpStatFile —— 前端在不需要 full classification 时
// （比如 tooltip 展示 mime）可走这个 lighter 入口，少一次 ClassifyPreview 调用。
func (c *Client) BuildPreviewMetadataLite(p string) (PreviewMetadata, error) {
	return c.buildPreviewMetadataImpl(p, false)
}

// buildPreviewMetadataImpl 是 BuildPreviewMetadata / BuildPreviewMetadataLite 的共享实现。
//
// classify=true：返回完整 kind；classify=false：kind 留空，前端按 mime 自行判断。
func (c *Client) buildPreviewMetadataImpl(p string, classify bool) (PreviewMetadata, error) {
	if c.sc == nil {
		return PreviewMetadata{}, errors.New("sftpclient.BuildPreviewMetadata: client closed")
	}
	entry, err := c.Stat(p)
	if err != nil {
		return PreviewMetadata{}, fmt.Errorf("sftpclient.BuildPreviewMetadata: stat %q: %w", p, err)
	}

	// 读前 PreviewMimeProbeBytes 字节（mime 用；magic 只用前 16 字节）
	headSize := int64(PreviewMimeProbeBytes)
	if entry.Size < headSize {
		headSize = entry.Size
	}
	head, err := c.ReadFileChunk(p, 0, headSize)
	if err != nil {
		return PreviewMetadata{}, fmt.Errorf("sftpclient.BuildPreviewMetadata: read head %q: %w", p, err)
	}

	// mime detection
	var mime string
	if len(head) > 0 {
		mime = http.DetectContentType(head)
	}

	// 扩展名
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(entry.Name)), ".")

	pm := PreviewMetadata{
		Path:     entry.Path,
		Name:     entry.Name,
		Size:     entry.Size,
		Mode:     uint32(entry.Mode),
		ModTime:  entry.ModTime.UTC().Format(time.RFC3339),
		MimeType: mime,
		Ext:      ext,
	}

	if !classify {
		// lite 模式：Kind / IsImage/IsPDF/IsText 留空
		return pm, nil
	}

	// full 模式：magic + classify
	magicKind := DetectMagic(head)
	kind := ClassifyPreview(entry.Size, mime, magicKind, ext)

	// 兜底：kind == binary 但 head 像文本 + 无 ext + 无 mime → 升级为 text
	if kind == PreviewKindBinary && ext == "" && mime == "" && looksLikeTextBytes(head) &&
		entry.Size <= PreviewTextMaxBytes {
		kind = PreviewKindText
	}

	pm.Kind = kind
	pm.IsImage = kind == PreviewKindImage
	pm.IsPDF = kind == PreviewKindPDF
	pm.IsText = kind == PreviewKindText
	return pm, nil
}
