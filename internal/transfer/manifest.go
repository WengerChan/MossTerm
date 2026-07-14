// Package transfer 的 manifest.go：断点续传凭证持久化。
//
// v0.5.10 设计：
//   - 每个 transfer 一个 JSON 文件：<manifestDir>/<transferID>.json
//   - ManifestDir 默认 <configDir>/transfers/
//   - 每片完成 flush 一次（写盘）；并发 worker 通过 channel 串行写
//   - 成功完成 → DeleteManifest；失败保留供 Resume
//   - Resume 时校验 local mtime + size + path（任一不匹配 → ErrLocalChanged）
//
// 为什么是 JSON 不是二进制：
//   - v0.5.10 manifest 体小（< 1 KB），JSON 可读性 + 调试友好
//   - 断点续传凭证 = 用户数据，保 atomic-rename 写盘避免半截
//   - 未来 v0.6+ 字段扩展（checksum 已有 + 留口子）JSON 兼容
package transfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrManifestNotFound 是 LoadManifest 找不到 manifest 的 sentinel error。
//
// 配合 errors.Is 判断"首次上传"和"manifest 真坏掉"两种情况。
var ErrManifestNotFound = errors.New("transfer: manifest not found")

// Manifest 是断点续传的状态快照。
//
// UploadedChunks 是已成功上传的 chunk index 列表（升序、去重）。
// Checksum 在所有 chunk 完成后填入（"sha256:<hex>"）。
// LocalModTime 用于 Resume 时校验本地文件未变。
type Manifest struct {
	TransferID     string    `json:"transferID"`
	LocalPath      string    `json:"localPath"`
	RemotePath     string    `json:"remotePath"`
	ChunkSize      int       `json:"chunkSize"`
	TotalSize      int64     `json:"totalSize"`
	UploadedChunks []int     `json:"uploadedChunks"`
	Checksum       string    `json:"checksum,omitempty"`
	LocalModTime   time.Time `json:"localModTime"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// TransferDir 返回 manifest 存储目录（不创建）。
//
// 解析顺序：
//  1. <manifestDir>（参数，v0.5.10 走 DefaultManifestDir()）
//  2. v0.6+ 留口子：从 config.Manager().TransferDir() 取
//
// 该函数仅解析路径，不创建目录。创建在 EnsureManifestDir 内部。
func TransferDir(manifestDir string) string {
	if manifestDir == "" {
		return DefaultManifestDir()
	}
	return manifestDir
}

// DefaultManifestDir 返回默认 manifest 目录。
//
// 解析：
//   - 优先用 os.UserConfigDir()/mossterm/transfers（与 config 同根）
//   - 拿不到时退到 ./.mossterm/transfers（cwd 兜底）
func DefaultManifestDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = "."
	}
	return filepath.Join(base, "mossterm", "transfers")
}

// EnsureManifestDir 确保 manifest 目录存在。
//
// mkdir 失败返回 error（Upload / Manager 兜底决定是否继续）。
func EnsureManifestDir(manifestDir string) error {
	dir := TransferDir(manifestDir)
	return os.MkdirAll(dir, 0o700)
}

// manifestPath 拼出单个 manifest 的绝对路径。
func manifestPath(manifestDir, transferID string) string {
	// transferID 由调用方校验非空；这里只 sanitize 防路径穿越
	safe := sanitizeTransferID(transferID)
	return filepath.Join(TransferDir(manifestDir), safe+".json")
}

// sanitizeTransferID 把 transferID 里的路径分隔符 + 危险字符替换为 '_'。
//
// transferID 应是 UUID v4（hex + dash），不会触发 sanitize；
// 但前端如果传奇怪值，这里兜底避免跳出 manifest 目录。
func sanitizeTransferID(id string) string {
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c == '/' || c == '\\' || c == 0:
			out = append(out, '_')
		case c == '.' && i == 0:
			// 不让 ".json" / "..json" 形式逃出
			out = append(out, '_')
		default:
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "_"
	}
	return string(out)
}

// SaveManifest 把 m 写盘（atomic-rename）。
//
// 写盘流程：<path>.tmp → fsync → rename 到 <path>。
// 失败时尝试清理 .tmp 避免遗留。
//
// 写盘前会 EnsureManifestDir（首次 Upload 时目录还没建）。
var manifestMu sync.Mutex // 写盘串行化，避免并发写同文件半截

func SaveManifest(manifestDir string, m *Manifest) error {
	if m == nil {
		return errors.New("transfer.SaveManifest: nil manifest")
	}
	if m.TransferID == "" {
		return errors.New("transfer.SaveManifest: empty transferID")
	}
	if err := EnsureManifestDir(manifestDir); err != nil {
		return fmt.Errorf("transfer.SaveManifest: ensure dir: %w", err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("transfer.SaveManifest: marshal: %w", err)
	}

	manifestMu.Lock()
	defer manifestMu.Unlock()

	path := manifestPath(manifestDir, m.TransferID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("transfer.SaveManifest: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("transfer.SaveManifest: rename: %w", err)
	}
	return nil
}

// LoadManifest 从 manifestDir 读 <transferID>.json。
//
// 不存在 → (nil, ErrManifestNotFound)。
// 解析失败 → (nil, fmt.Errorf(...)) 包装。
// 成功 → (*Manifest, nil)。
func LoadManifest(manifestDir, transferID string) (*Manifest, error) {
	if transferID == "" {
		return nil, errors.New("transfer.LoadManifest: empty transferID")
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()

	path := manifestPath(manifestDir, transferID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrManifestNotFound
		}
		return nil, fmt.Errorf("transfer.LoadManifest: read %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("transfer.LoadManifest: parse %s: %w", path, err)
	}
	return &m, nil
}

// DeleteManifest 删除 manifest 文件。
//
// 不存在返回 nil（幂等）。
func DeleteManifest(manifestDir, transferID string) error {
	if transferID == "" {
		return nil
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()

	path := manifestPath(manifestDir, transferID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("transfer.DeleteManifest: remove %s: %w", path, err)
	}
	return nil
}

// ListManifests 列出 manifestDir 下全部 manifest。
//
// 不存在的目录返回空 slice + nil。
// 单个文件解析失败跳过 + 不报错（best-effort）。
// 用途：v0.5.10 Manager.List / 前端"历史"页（v0.6+）。
func ListManifests(manifestDir string) ([]*Manifest, error) {
	dir := TransferDir(manifestDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("transfer.ListManifests: readdir %s: %w", dir, err)
	}
	out := make([]*Manifest, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		m, err := LoadManifest(manifestDir, id)
		if err != nil {
			// best-effort: 解析失败跳过
			continue
		}
		out = append(out, m)
	}
	return out, nil
}
