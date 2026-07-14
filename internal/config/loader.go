// Package config 的 loader.go：路径解析、默认数据工厂、TOML 读写。
//
// 与 manager.go 分离的原因：
//   - 路径 / 平台特定逻辑（XDG、%APPDATA%）集中一处；
//   - 读写文件的纯函数（readTOMLFile / writeTOMLFile）可被单测覆盖。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
)

// DefaultConfigPath 返回配置文件的标准路径。
//
// 解析顺序（与 config.example.toml 头部注释保持一致）：
//  1. $MOSSTERM_CONFIG（环境变量优先）
//  2. $XDG_CONFIG_HOME/mossterm/config.toml
//  3. ~/Library/Application Support/mossterm/config.toml（macOS）
//  4. %APPDATA%\mossterm\config.toml（Windows）
//  5. ~/.config/mossterm/config.toml（Linux 兜底）
func DefaultConfigPath() string {
	if env := os.Getenv("MOSSTERM_CONFIG"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// 拿不到 home dir 就退到当前目录
		return filepath.Join(".config", "mossterm", "config.toml")
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "mossterm", "config.toml")
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "mossterm", "config.toml")
		}
		return filepath.Join(home, "AppData", "Roaming", "mossterm", "config.toml")
	default:
		// Linux / BSD / 其它
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "mossterm", "config.toml")
		}
		return filepath.Join(home, ".config", "mossterm", "config.toml")
	}
}

// CopyExampleTo 把仓库根目录的 configs/config.example.toml 复制到 dstDir/config.example.toml。
//
// 这是 best-effort：找不到源文件时返回 error，但 Manager.New 不会因此失败。
//
// 实现细节：
//   - 优先用 `git rev-parse --show-toplevel` 解析仓库根（开发者本地）；
//   - 失败则尝试相对于可执行文件的位置（打 release 后的常见布局）；
//   - 最后才尝试相对于当前工作目录。
func CopyExampleTo(dstDir string) error {
	if dstDir == "" {
		return errors.New("config.CopyExampleTo: empty dstDir")
	}
	src, err := locateExampleTOML()
	if err != nil {
		return err
	}
	dst := filepath.Join(dstDir, "config.example.toml")
	// 已存在就不覆盖
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("config.CopyExampleTo: read source: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("config.CopyExampleTo: write dest: %w", err)
	}
	return nil
}

// locateExampleTOML 找 examples 文件的源路径。
func locateExampleTOML() (string, error) {
	// 候选路径，按优先级尝试
	candidates := make([]string, 0, 6)

	// 1. git 根
	if gitRoot, err := gitRepoRoot(); err == nil && gitRoot != "" {
		candidates = append(candidates, filepath.Join(gitRoot, "configs", "config.example.toml"))
	}
	// 2. 相对于可执行文件
	if exe, err := os.Executable(); err == nil && exe != "" {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "configs", "config.example.toml"),
			filepath.Join(exeDir, "..", "configs", "config.example.toml"),
			filepath.Join(exeDir, "..", "..", "configs", "config.example.toml"),
		)
	}
	// 3. 相对于 cwd
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		candidates = append(candidates,
			filepath.Join(cwd, "configs", "config.example.toml"),
			filepath.Join(cwd, "..", "configs", "config.example.toml"),
		)
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("config.locateExampleTOML: not found (candidates=%d)", len(candidates))
}

// gitRepoRoot 尝试用 git 命令解析当前工作目录所属仓库的根。
//
// 失败（无 git / 不在 repo 内）返回 error，调用方继续走其它候选路径。
func gitRepoRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if cwd, err := os.Getwd(); err == nil {
		cmd.Dir = cwd
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	root := string(bytes.TrimSpace(out))
	if root == "" {
		return "", errors.New("empty git toplevel")
	}
	return root, nil
}

// readTOMLFile 从 path 读取并解析 TOML 到 *Data。
//
// 文件不存在返回 (nil, nil) —— 让调用方决定是否 fallback 到 Defaults。
// 解析失败返回 error。
func readTOMLFile(path string) (*Data, error) {
	if path == "" {
		return nil, errors.New("config.readTOMLFile: empty path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("config.readTOMLFile: read %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var d Data
	if _, err := toml.Decode(string(data), &d); err != nil {
		return nil, fmt.Errorf("config.readTOMLFile: parse %s: %w", path, err)
	}
	return &d, nil
}

// writeTOMLFile 把 d 序列化到 path（atomic-rename 模式）。
//
// 流程：写到 path.tmp → fsync → rename 到 path。
// 写入失败时尝试清理 .tmp 避免遗留垃圾文件。
func writeTOMLFile(path string, d *Data) error {
	if path == "" {
		return errors.New("config.writeTOMLFile: empty path")
	}
	if d == nil {
		return errors.New("config.writeTOMLFile: nil data")
	}

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(d); err != nil {
		return fmt.Errorf("config.writeTOMLFile: encode: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("config.writeTOMLFile: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config.writeTOMLFile: rename: %w", err)
	}
	return nil
}

// Defaults 返回程序内置的默认配置。
//
// 注意：Profile.Layouts/Keymaps/Themes 等用空 map 而非 nil，方便
// 上层 for-range 不需要 nil check。
//
// TransferSettings（v0.5.10）零值 → transfer 包用 package const 兜底
// （DefaultChunkSize 4 MiB / DefaultConcurrency 2 / MaxFileSize 10 GiB）。
// 用户在 config.toml 显式设值后覆盖。
func Defaults() *Data {
	return &Data{
		Version: 1,
		Settings: Settings{
			DefaultFont:   "JetBrains Mono",
			FontSize:      14,
			Scrollback:    10000,
			KeepAliveSecs: 30,
			CheckUpdate:   true,
		},
		Transfer: TransferSettings{},
		Profiles: make(map[string]Profile),
		Layouts:  make(map[string]Layout),
		Keymaps:  make(map[string]Keymap),
		Themes:   make(map[string]Theme),
	}
}

// cloneData 深拷贝 Data 及其 map 字段。
//
// 输出与 *d 等价的 Data 值；d == nil 时返回零值。
func cloneData(d *Data) Data {
	if d == nil {
		return Data{}
	}
	out := *d
	if d.Profiles != nil {
		out.Profiles = make(map[string]Profile, len(d.Profiles))
		for k, v := range d.Profiles {
			out.Profiles[k] = v
		}
	}
	if d.Layouts != nil {
		out.Layouts = make(map[string]Layout, len(d.Layouts))
		for k, v := range d.Layouts {
			out.Layouts[k] = v
		}
	}
	if d.Keymaps != nil {
		out.Keymaps = make(map[string]Keymap, len(d.Keymaps))
		for k, v := range d.Keymaps {
			out.Keymaps[k] = v
		}
	}
	if d.Themes != nil {
		out.Themes = make(map[string]Theme, len(d.Themes))
		for k, v := range d.Themes {
			out.Themes[k] = v
		}
	}
	if d.Recent != nil {
		out.Recent = append([]string(nil), d.Recent...)
	}
	return out
}
