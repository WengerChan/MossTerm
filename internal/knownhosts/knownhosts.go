// Package knownhosts 提供 MossTerm 的 known_hosts 持久化与 host key 校验。
//
// 设计要点：
//   - 文件格式与 OpenSSH known_hosts 兼容（`~/.ssh/known_hosts` 同款格式）
//   - 路径默认 ~/.config/mossterm/known_hosts（与 OpenSSH 不共用，便于隔离）
//   - 智能 HostKeyCallback：未找到时自动信任并写入；host key 改变时拒绝
//   - 线程安全：内部用 sync.Mutex 保护
//
// 安全语义：
//   - "未找到"（new host）→ 自动 Add 写入文件 + 放行（v0.1.3 简化策略）
//   - "找到且匹配" → 放行
//   - "找到但不匹配"（host key 改变）→ 拒绝（这是 MITM 攻击的信号）
//   - v0.2+ 计划加"首次信任"UI 对话框，让用户在 GUI 确认
//
// 与 sshclient 的关系：
//   connect.Deps 加 KnownHosts *Manager 字段
//   sshclient.New 存到 Connector
//   sshclient.Dial 把它转成 ssh.HostKeyCallback 给 ssh.ClientConfig 用
//
// 为什么不直接用 x/crypto/ssh/knownhosts：
//   那个包只提供 `New(path) (HostKeyCallback, error)`，不导出 DB 类型，
//   因此无法在 callback 命中"未找到"分支时自动 Add。我们的需求是
//   首次连接自动信任 + 持久化，所以需要自己实现简化版。
package knownhosts

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Manager 是 known_hosts 文件的运行时句柄。
//
// 内部维护 host → keytype → keybase64 的二级 map。
// 每次 Add / Authorize 都加锁，callback 无锁读（快照）。
type Manager struct {
	path string
	mu   sync.RWMutex
	// keys[host][keyType] = keyBase64
	keys map[string]map[string]string
}

// New 构造一个 Manager，从 path 加载已有 host keys。
//
// path 文件不存在时自动创建（父目录用 0700 权限）。文件已存在则解析。
//
// path 为空字符串时返回 error（不提供默认路径，强制调用方显式选择）。
func New(path string) (*Manager, error) {
	if path == "" {
		return nil, errors.New("knownhosts.New: empty path")
	}
	// 确保父目录存在
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("knownhosts.New: mkdir parent: %w", err)
	}
	m := &Manager{
		path: path,
		keys: make(map[string]map[string]string),
	}
	// 文件不存在 → 创建空文件（首次运行）
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("knownhosts.New: create file: %w", err)
		}
		_ = f.Close()
		return m, nil
	}
	// 文件存在 → 解析
	if err := m.loadFromFile(); err != nil {
		return nil, fmt.Errorf("knownhosts.New: load: %w", err)
	}
	return m, nil
}

// Path 返回 known_hosts 文件绝对路径。
func (m *Manager) Path() string { return m.path }

// Size 返回已知 host 数量（仅用于测试 / 调试）。
func (m *Manager) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.keys)
}

// HostKeyCallback 返回一个 ssh.HostKeyCallback 智能实现。
//
// 策略：
//  1. host key 匹配已知条目 → 放行（返回 nil）
//  2. host 在 known_hosts 中但 key 不匹配 → 拒绝（返回 ErrHostKeyMismatch）
//  3. host 不在 known_hosts 中 → 自动 Add 写入文件 + 放行（返回 nil）
//
// v0.1.3 简化：自动信任未知 host。v0.2 接入"首次信任"UI 对话框。
//
// 签名遵循 x/crypto v0.22+：返回 error 而非 bool（nil = 放行）。
func (m *Manager) HostKeyCallback() ssh.HostKeyCallback {
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		keyType := key.Type()
		keyBase64 := base64.StdEncoding.EncodeToString(key.Marshal())

		m.mu.RLock()
		known, exists := m.keys[host]
		var matched bool
		if exists {
			matched = known[keyType] == keyBase64
		}
		m.mu.RUnlock()

		if exists && matched {
			return nil // 1. 匹配 → 放行
		}
		if exists && !matched {
			// 2. host 在但 key 不匹配 → MITM
			return ErrHostKeyMismatch
		}
		// 3. 未找到 → 自动 Add
		if err := m.Add(host, key, "mossterm-auto"); err != nil {
			// Add 失败仍放行（避免网络问题锁死整个 SSH 流程）
			// v0.2 应该把错误推到 Wails 事件总线给前端显示
			return nil
		}
		return nil
	}
}

// Authorize 显式校验（用于测试 / API 调用）。
//
// 返回 nil 表示通过；返回 ErrHostKeyMismatch 表示 host key 不匹配。
func (m *Manager) Authorize(host string, key ssh.PublicKey) error {
	keyType := key.Type()
	keyBase64 := base64.StdEncoding.EncodeToString(key.Marshal())

	m.mu.RLock()
	defer m.mu.RUnlock()
	known, exists := m.keys[host]
	if !exists {
		return ErrHostUnknown
	}
	if known[keyType] != keyBase64 {
		return ErrHostKeyMismatch
	}
	return nil
}

// Add 显式添加一条 host key 记录。
//
// 同时更新内存 map 和持久化到文件。comment 可选（写入文件时附加）。
func (m *Manager) Add(host string, key ssh.PublicKey, comment string) error {
	keyType := key.Type()
	keyBase64 := base64.StdEncoding.EncodeToString(key.Marshal())

	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. 更新内存
	if m.keys[host] == nil {
		m.keys[host] = make(map[string]string)
	}
	m.keys[host][keyType] = keyBase64

	// 2. 追加到文件（OpenSSH 格式：<host> <keytype> <keybase64> [<comment>]）
	f, err := os.OpenFile(m.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("knownhosts.Add: open file: %w", err)
	}
	defer f.Close()
	line := fmt.Sprintf("%s %s %s", host, keyType, keyBase64)
	if comment != "" {
		line += " " + comment
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("knownhosts.Add: write: %w", err)
	}
	return nil
}

// Close 释放资源。Manager 内部不持有持久句柄，Close 是 no-op。
func (m *Manager) Close() error {
	return nil
}

// loadFromFile 解析 known_hosts 文件到内存 map。
//
// 简化解析：
//   - 跳过空行和以 # 开头的注释行
//   - 每行格式：<host> <keytype> <keybase64> [<comment>...]
//   - host 不能含空格（不支持通配符 / 端口 / IP 范围）
//   - 多 host 用逗号分隔时只取第一个
//
// 不规范的行被忽略（v0.1.3 宽容策略；v0.2 可以加警告日志）。
func (m *Manager) loadFromFile() error {
	f, err := os.Open(m.path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 跳过 @ 开头的 marker（@revoked / @cert-authority）
		if strings.HasPrefix(line, "@") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue // 格式不对，跳过
		}
		host := strings.SplitN(fields[0], ",", 2)[0] // 多 host 取第一个
		keyType := fields[1]
		keyBase64 := fields[2]
		if m.keys[host] == nil {
			m.keys[host] = make(map[string]string)
		}
		m.keys[host][keyType] = keyBase64
	}
	return scanner.Err()
}

// ErrHostUnknown 表示 host 不在 known_hosts 中。
var ErrHostUnknown = errors.New("knownhosts: host not found")

// ErrHostKeyMismatch 表示 host 在 known_hosts 中但 key 不匹配（MITM 风险）。
var ErrHostKeyMismatch = errors.New("knownhosts: host key mismatch (possible MITM)")
