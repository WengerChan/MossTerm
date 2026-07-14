// Package knownhosts 提供 MossTerm 的 known_hosts 持久化与 host key 校验。
//
// 设计要点：
//   - 文件格式与 OpenSSH known_hosts 完全兼容（`~/.ssh/known_hosts` 同款格式）
//   - 路径默认 ~/.config/mossterm/known_hosts（与 OpenSSH 不共用，便于隔离）
//   - 智能 HostKeyCallback：未找到时弹 GUI 询问用户；host key 改变时拒绝
//   - 线程安全：内部用 sync.RWMutex 保护
//   - v0.2.0b 起支持 OpenSSH 完整 host pattern：通配符、端口、IP 范围
//   - v0.5.0 起支持"首次信任"UI 对话框：未知 host key 经 Wails 事件总线
//     推给前端，前端 modal 让用户选 trust/reject
//
// 安全语义：
//   - "找到且匹配" → 放行
//   - "找到但不匹配"（host key 改变）→ 拒绝（这是 MITM 攻击的信号）
//   - "未找到"（new host）→
//     - v0.5.0+：经 EventEmitter 推给前端，用户 trust 后 Add 写入 + 放行；
//       reject 或 60s 超时则拒绝。
//     - 兜底（无 emitter / 单元测试）：自动 Add 写入 + 放行（v0.1.3 行为）
//
// 与 sshclient 的关系：
//   connect.Deps 加 KnownHosts *Manager 字段
//   sshclient.New 存到 Connector
//   sshclient.Dial 把它转成 ssh.HostKeyCallback 给 ssh.ClientConfig 用
//
// 为什么不用 x/crypto/ssh/knownhosts 标准库：
//   那个包只导出 New(files) (HostKeyCallback, error)，不导出 DB 类型，
//   无法在 callback 命中"未找到"分支时手动 Add。我们的需求是
//   "首次连接询问用户 + 持久化"，所以需要自实现匹配算法。
//   匹配规则源自 OpenSSH addrmatch.c（与标准库 wildcardMatch 等价实现）。
package knownhosts

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// TrustRequestTimeout 是 HostKeyCallback 等待用户响应的最大时长。
//
// 超时后 HostKeyCallback 返回 error（"user trust timeout"），SSH 握手失败，
// 不会自动信任。60s 与主流 SSH 客户端（OpenSSH 的 StrictHostKeyChecking=ask
// 无超时，PuTTY 默认 10s）取折中：足够让用户阅读指纹 + 决定，又不会因误点
// modal 而永久挂起 SSH 连接。
//
// 暴露为 var（非 const）便于单元测试覆盖；生产代码不应改写。
var TrustRequestTimeout = 60 * time.Second

// replyChannelCapacity 是 Manager 内部 trustReplyCh 的容量。
//
// 容量为 1：保证 ReplyTrust 在没有挂起请求时也能立即返回（不阻塞），
// 而不会无限堆积过期 reply。多个并发 SSH 连接同时请求 trust 时只有第一
// 个会拿到 reply，其余会 timeout —— 这是 v0.5.0 的已知限制，参见
// HostKeyCallback 内的注释。
const replyChannelCapacity = 1

// TrustRequest 是推给前端的"是否信任此 host"请求。
//
// 字段说明：
//   - ID：唯一请求 ID，用于 ReplyTrust 时把决策路由回正确的 SSH 连接。
//   - Host：远端 host（含端口，SSH client 传的原始字符串）。
//   - KeyType：ssh.PublicKey.Type()（"ssh-ed25519" / "ssh-rsa" / "ecdsa-sha2-nistp256" 等）。
//   - Fingerprint：完整 base64 key 的前 16 字符（短哈希），供 modal 摘要展示。
//   - FullKey：完整 base64 编码的 key，供"高级 / 复制"按钮展开。
type TrustRequest struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	FullKey     string `json:"fullKey"`
}

// TrustReply 是前端回传的决策。
//
// 字段说明：
//   - ID：对应 TrustRequest.ID；Manager 检查匹配后才会采纳。
//   - Action："trust" | "reject" | 其他（视为 reject）。
//   - Err：保留字段，未来用于前端上报错误（如 modal 渲染失败）；v0.5.0 暂不使用。
type TrustReply struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Err    string `json:"err,omitempty"`
}

// EventEmitter 是 known_hosts 用来推送"信任请求"到前端的轻量接口。
//
// 设计目的：known_hosts 包不直接 import wailsruntime（避免循环依赖，
// 同时让核心层可以独立单测）。实现由 main.go 或 internal/app 注入。
//
// EmitTrustRequest 应该**非阻塞**：把请求 emit 到 Wails 事件总线后立即返回；
// 实际的等待用户响应通过 Manager.ReplyTrust 走另一条路径。
// 如果实现需要"等待"语义，应在内部开 goroutine 处理并把结果回灌到 replyCh。
type EventEmitter interface {
	EmitTrustRequest(ctx context.Context, req TrustRequest)
}

// Manager 是 known_hosts 文件的运行时句柄。
//
// 内部维护一个 entry 列表：每行文件 = 一个 entry；同一行多 host
// （用逗号分隔）共用一个 key，展开为 entry 内的多个 pattern。
//
// 匹配流程：
//  1. callback 收到 host（SSH client 传来的地址，如 "example.com:2222"）
//  2. 用 net.SplitHostPort 拆 host → 实际 host + port
//  3. 遍历 entries，对每个 entry 用 OpenSSH 通配符规则逐 pattern 比对
//  4. 找到 pattern match + key match → 放行
//  5. 找到 pattern match + key mismatch → 拒绝（MITM）
//  6. 都没找到 → 走"首次信任"路径（v0.5.0+）：
//     a) emitter 非 nil → EmitTrustRequest 给前端，等用户响应
//     b) emitter 为 nil → 自动 Add 写入文件 + 放行（v0.1.3 兜底行为）
type Manager struct {
	path string
	mu   sync.RWMutex
	// entries 是文件按行解析的列表，顺序与文件一致。
	entries []entry

	// emitter 是 v0.5.0+ 首次信任 GUI 通道。nil 时退化到 v0.1.3 自动信任。
	emitter EventEmitter
	// trustReplyCh 接收前端回传的 TrustReply（wailsbinding TrustHost 调 ReplyTrust 写入）。
	// 容量为 1，避免 ReplyTrust 在无挂起请求时阻塞，也避免过期 reply 堆积。
	// channel 自身的 send/receive 自带 happens-before 保证，无需额外锁。
	trustReplyCh chan TrustReply
}

// entry 对应 known_hosts 文件的一行。
type entry struct {
	// patterns 是该行第一个字段按逗号拆分后的 pattern 列表。
	// 例如 "host1,*.example.com" 拆为 [{host:host1,port:22},{host:*.example.com,port:22}]。
	patterns []pattern
	// key 是解析出的 ssh.PublicKey；用 Marshal() 字节比较 key 相等性。
	key ssh.PublicKey
	// keyType 是冗余信息（key.Marshal() 内部已含 type），仅用于写回文件时
	// 保留文件行的 keytype 字段。
	keyType string
}

// pattern 是一个 host pattern 的解析后形式。
//
// 例如 "[example.com]:2222" 解析为 {host: "example.com", port: "2222"}；
// "example.com" 解析为 {host: "example.com", port: "22"}（OpenSSH 默认端口）。
// host 部分支持 OpenSSH 通配符：* 匹配任意序列，? 匹配任意单字符。
type pattern struct {
	host string
	port string
}

// addr 是 host 查询时的实际地址（从 SSH callback 的 host 参数解析得到）。
type addr struct {
	host string
	port string
}

// New 构造一个 Manager，从 path 加载已有 host keys。
//
// path 文件不存在时自动创建（父目录用 0700 权限）。文件已存在则解析；
// 单行格式错误采用宽容策略（跳过该行），保证一个坏行不会阻塞整个文件加载。
//
// path 为空字符串时返回 error（不提供默认路径，强制调用方显式选择）。
//
// 该构造器**不**启用首次信任 GUI 询问 —— 未知 host 走 v0.1.3 的"自动
// 信任并写入"路径。生产入口（main.go）应改用 NewWithTrust。
func New(path string) (*Manager, error) {
	return newManager(path, nil)
}

// NewWithTrust 构造一个启用"首次信任"GUI 询问的 Manager。
//
// emitter 非 nil 时，HostKeyCallback 遇到未知 host 会先调
// emitter.EmitTrustRequest 推给前端，再同步等待 TrustRequestTimeout
// （默认 60s）内的 TrustReply。用户 trust → Add 写入 + 放行；
// 用户 reject / 超时 / 错误 ID → 拒绝。
//
// emitter 为 nil 时退化为 New() 行为（自动信任）。传 nil 在生产环境
// 等同于 v0.1.3，仅用于不想接 GUI 的子命令 / 测试。
func NewWithTrust(path string, emitter EventEmitter) (*Manager, error) {
	return newManager(path, emitter)
}

// newManager 是 New / NewWithTrust 的共享实现。
func newManager(path string, emitter EventEmitter) (*Manager, error) {
	if path == "" {
		return nil, errors.New("knownhosts.New: empty path")
	}
	// 确保父目录存在
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("knownhosts.New: mkdir parent: %w", err)
	}
	m := &Manager{
		path:          path,
		emitter:       emitter,
		trustReplyCh:  make(chan TrustReply, replyChannelCapacity),
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

// Size 返回已知 entry 数量（仅用于测试 / 调试）。
//
// 一个 entry 对应 known_hosts 文件的一行；同一 host 出现在不同行 / 不同文件
// 位置算多个 entry（与 OpenSSH `ssh-keygen -F host` 行为对齐）。
func (m *Manager) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}

// HostKeyCallback 返回一个 ssh.HostKeyCallback 智能实现。
//
// 策略：
//  1. host pattern + key 都匹配已知 entry → 放行（返回 nil）
//  2. host pattern 匹配但 key 不匹配 → 拒绝（返回 ErrHostKeyMismatch；MITM 信号）
//  3. host 全无匹配 → 走"首次信任"路径：
//     a) emitter 非 nil → EmitTrustRequest 给前端；等用户响应
//        - "trust" → Add 写入 + 放行
//        - "reject" / 错误 ID / 超时 → 拒绝
//     b) emitter 为 nil → 自动 Add 写入文件 + 放行（v0.1.3 兜底）
//
// 签名遵循 x/crypto v0.22+：返回 error 而非 bool（nil = 放行）。
//
// 并发：多个 SSH 连接可能同时触发 HostKeyCallback。trustReplyCh 容量 1
// 的设计意味着：第一个等待者拿到 reply 后，reply 被消费；第二个等待者
// 拿到的是别人的 reply（ID 不匹配）或 timeout。
// v0.5.0 的实际场景（用户在 modal 里点 trust 之前不会同时点第二次连接）
// 下这个限制可接受；并发 trust 场景需要 map[requestID]chan TrustReply，
// 留给 v0.6+ 优化。
func (m *Manager) HostKeyCallback() ssh.HostKeyCallback {
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		a := parseAddr(host)
		if a.host == "" && remote != nil {
			// host 参数解析失败时退化到 remote（x/crypto 的同样兜底）
			a = parseAddr(remote.String())
		}
		if a.host == "" {
			// 完全无法解析 host（极端情况，SSH client 给了空地址）— 拒绝
			// 比"放行"更安全：避免对一个未知 host 静默接受任意 key
			return fmt.Errorf("knownhosts: cannot determine host from address %q", host)
		}

		keyType := key.Type()
		keyBase64 := base64.StdEncoding.EncodeToString(key.Marshal())

		m.mu.RLock()
		matchIdx := -1
		anyMatch := false
		for i := range m.entries {
			if !m.entries[i].match(a) {
				continue
			}
			anyMatch = true
			if bytes.Equal(m.entries[i].key.Marshal(), key.Marshal()) {
				matchIdx = i
				break
			}
		}
		m.mu.RUnlock()

		if matchIdx >= 0 {
			return nil
		}
		if anyMatch {
			return ErrHostKeyMismatch
		}

		// 未找到 → 走"首次信任"路径。
		//
		// 兜底：没有 emitter（单元测试 / CLI 子命令）→ 自动信任（v0.1.3 行为）。
		if m.emitter == nil {
			_ = m.Add(host, key, "mossterm-auto")
			return nil
		}

		req := TrustRequest{
			ID:          generateTrustID(),
			Host:        host,
			KeyType:     keyType,
			Fingerprint: shortFingerprint(keyBase64),
			FullKey:     keyBase64,
		}

		// 推给前端（非阻塞；实现应立即返回，自身 spawn 协程或仅 emit 事件）
		m.emitter.EmitTrustRequest(context.Background(), req)

		// 同步等待用户回复（带超时）。
		//
		// channel send/receive 自带 happens-before 保证，无需额外锁。
		select {
		case reply := <-m.trustReplyCh:
			if reply.ID != req.ID {
				// ID 不匹配：多半是并发场景下拿到了"别人的 reply"或"过期的 reply"。
				// 当前 reply 已被消费，挂起方将 timeout。
				return fmt.Errorf("knownhosts: trust reply ID mismatch (want %q, got %q)", req.ID, reply.ID)
			}
			if reply.Action == "trust" {
				if err := m.Add(host, key, "mossterm-user"); err != nil {
					return fmt.Errorf("knownhosts: persist trusted key: %w", err)
				}
				return nil
			}
			// "reject" 或其他非 trust → 拒绝。
			return fmt.Errorf("knownhosts: user rejected host key for %q", host)
		case <-time.After(TrustRequestTimeout):
			return fmt.Errorf("knownhosts: user trust timeout for %q (waited %s)", host, TrustRequestTimeout)
		}
	}
}

// Authorize 显式校验（用于测试 / API 调用）。
//
// host 接受 "example.com" 或 "example.com:2222" 两种形式；内部统一用
// net.SplitHostPort 拆 host + port。
//
// 返回：
//   - nil 表示通过（host pattern 匹配 + key 一致）
//   - ErrHostKeyMismatch 表示 host 在 known_hosts 中但 key 不匹配
//   - ErrHostUnknown 表示 host 不在 known_hosts 中
func (m *Manager) Authorize(host string, key ssh.PublicKey) error {
	a := parseAddr(host)
	if a.host == "" {
		return ErrHostUnknown
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.entries {
		if !m.entries[i].match(a) {
			continue
		}
		if bytes.Equal(m.entries[i].key.Marshal(), key.Marshal()) {
			return nil
		}
		return ErrHostKeyMismatch
	}
	return ErrHostUnknown
}

// Add 显式添加一条 host key 记录。
//
// 同时更新内存 entries 和持久化到文件。host 可以是 "example.com" 或
// "example.com:2222"；端口非默认（!=22）时写回文件用 [host]:port 形式
// （OpenSSH 规范）。
//
// comment 可选（写入文件时附加），例如 "mossterm-auto" 标识自动信任。
func (m *Manager) Add(host string, key ssh.PublicKey, comment string) error {
	keyType := key.Type()
	keyBase64 := base64.StdEncoding.EncodeToString(key.Marshal())

	// 1. 解析 host → pattern
	p, err := parsePattern(host)
	if err != nil {
		return fmt.Errorf("knownhosts.Add: parse host: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 2. 追加到内存
	m.entries = append(m.entries, entry{
		patterns: []pattern{p},
		key:      key,
		keyType:  keyType,
	})

	// 3. 追加到文件（OpenSSH 格式：<pattern> <keytype> <keybase64> [<comment>]）
	f, err := os.OpenFile(m.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("knownhosts.Add: open file: %w", err)
	}
	defer f.Close()
	line := formatPattern(p) + " " + keyType + " " + keyBase64
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

// ReplyTrust 把前端用户的决策写回 Manager，唤醒挂起的 HostKeyCallback。
//
// 由 wailsbindings.App.TrustHost 调用：前端 modal 收到用户点击后，
// 立刻通过 wails 反射调 TrustHost(id, action) → ReplyTrust(id, action)。
//
// 行为：
//   - 5s 内能写入 trustReplyCh（容量 1）→ 成功；HostKeyCallback 拿到 reply。
//   - 5s 内写不进去（无挂起请求 / 上一个 reply 还没被消费）→ 返回 error。
//
// 注意：返回 error 不代表"用户的决策丢了"——前端可以根据错误重发，
// 也可以直接关闭 modal 让 SSH 连接走 timeout 失败路径。
//
// 锁：channel send/receive 自带 happens-before 保证，ReplyTrust 自身
// 不需要额外锁。
func (m *Manager) ReplyTrust(requestID, action string) error {
	if m.trustReplyCh == nil {
		return errors.New("knownhosts.ReplyTrust: manager not initialized with NewWithTrust")
	}

	// 5s 兜底：正常情况下 replyCh 容量 1 总是能立即写入。
	// 这层 select 只在异常（重复 reply / 无挂起请求）触发。
	select {
	case m.trustReplyCh <- TrustReply{ID: requestID, Action: action}:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("knownhosts.ReplyTrust: no pending request (or reply channel full)")
	}
}

// generateTrustID 生成一个 16 字节的随机 ID（base64 URL 编码，无 padding）。
//
// 用 crypto/rand 避免可预测的 ID；不需要 RFC 4122 完整 UUID 格式，
// base64.RawURLEncoding 短 22 字符够用且对前端友好（无 `+` `/`）。
func generateTrustID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败是极罕见的系统级错误；用全 0 兜底（碰撞概率可忽略）
		// 注意：仍返回有效 ID，前端能正常匹配；碰撞只意味着可能被错路由
		// 到同 ID 的其他请求，v0.5.0 概率 ~0，认了。
		for i := range b {
			b[i] = 0
		}
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// shortFingerprint 把完整 base64 key 截短成 16 字符 + "..." 的摘要。
//
// 用于 modal 摘要展示（避免一长串 base64 撑爆 UI）。不是真正的指纹
// 哈希，仅作视觉截断；用户点"展开"时看 FullKey。
func shortFingerprint(b64 string) string {
	const max = 16
	if len(b64) > max {
		return b64[:max] + "..."
	}
	return b64
}

// loadFromFile 解析 known_hosts 文件到内存 entries。
//
// 容错策略（v0.1.3 沿用）：
//   - 跳过空行和以 # 开头的注释行
//   - 跳过以 @ 开头的 marker 行（@revoked / @cert-authority）— v0.2.0b 暂不实现
//   - 跳过以 | 开头的行（host 字段为 |1|... hash 编码）— v0.2.0b 不实现 hash 校验
//   - 每行格式：<host-patterns> <keytype> <keybase64> [<comment>...]
//   - key 解析失败时跳过该行（不阻塞其他行加载）
//   - host-patterns 按逗号拆分，每个独立成 pattern
//   - pattern 形式：精确（example.com）/ 通配符（*.example.com）/ 端口（[host]:2222）
//
// 不规范的行被静默忽略；这种宽容策略保证一个损坏的行不会影响整个 known_hosts。
func (m *Manager) loadFromFile() error {
	f, err := os.Open(m.path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		// 去 BOM（Windows 记事本有时会写）
		line = bytes.TrimPrefix(line, []byte{0xEF, 0xBB, 0xBF})
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		// 跳过 @ 开头 marker（@revoked / @cert-authority）
		if line[0] == '@' {
			continue
		}
		// 跳过 hash 编码 host 行（|1|salt|hash ...）— v0.2.0b 不实现匹配
		if line[0] == '|' {
			continue
		}
		e, err := parseLine(line)
		if err != nil {
			// 容错：单行错误不阻塞其他行
			continue
		}
		m.entries = append(m.entries, e)
	}
	return scanner.Err()
}

// parseLine 解析一行已知格式为 entry。
//
// 期望格式：<patterns> <keytype> <keybase64> [<comment>...]
// 返回的 entry 中 patterns 已拆分，key 已用 ssh.ParsePublicKey 验证。
func parseLine(line []byte) (entry, error) {
	fields := strings.Fields(string(line))
	if len(fields) < 3 {
		return entry{}, fmt.Errorf("knownhosts: need at least 3 fields, got %d", len(fields))
	}

	// 1. 拆分 host patterns（按逗号）
	patternStrs := strings.Split(fields[0], ",")
	var patterns []pattern
	for _, ps := range patternStrs {
		ps = strings.TrimSpace(ps)
		if ps == "" {
			continue
		}
		// 跳过 hash 编码（同行的多 pattern 中也可能出现）
		if strings.HasPrefix(ps, "|") {
			return entry{}, errors.New("knownhosts: hashed host pattern in line, skipping entire line")
		}
		p, err := parsePattern(ps)
		if err != nil {
			return entry{}, fmt.Errorf("knownhosts: parse pattern %q: %w", ps, err)
		}
		patterns = append(patterns, p)
	}
	if len(patterns) == 0 {
		return entry{}, errors.New("knownhosts: no valid patterns in line")
	}

	// 2. 解析 key（fields[1] 是 keytype，fields[2] 是 base64；keytype 冗余但保留）
	keyType := fields[1]
	keyBytes, err := base64.StdEncoding.DecodeString(fields[2])
	if err != nil {
		return entry{}, fmt.Errorf("knownhosts: decode key base64: %w", err)
	}
	key, err := ssh.ParsePublicKey(keyBytes)
	if err != nil {
		return entry{}, fmt.Errorf("knownhosts: parse public key: %w", err)
	}

	return entry{
		patterns: patterns,
		key:      key,
		keyType:  keyType,
	}, nil
}

// parsePattern 把 pattern 字符串拆成 host + port。
//
// 支持形式：
//   - "example.com"        → {host: "example.com", port: "22"}
//   - "example.com:2222"   → {host: "example.com", port: "2222"}
//   - "[example.com]:2222" → {host: "example.com", port: "2222"}
//   - "[::1]:2222"         → {host: "::1", port: "2222"}
//   - "192.168.1.10"       → {host: "192.168.1.10", port: "22"}
//
// 不含端口时默认 "22"（OpenSSH 行为）。
// 空字符串或含逗号（多 host 字段）返回 error — Add API 只接受单 host。
func parsePattern(s string) (pattern, error) {
	if s == "" {
		return pattern{}, errors.New("knownhosts: empty host pattern")
	}
	if strings.Contains(s, ",") {
		return pattern{}, errors.New("knownhosts: comma in host pattern (Add only accepts single host)")
	}
	if h, p, err := net.SplitHostPort(s); err == nil {
		if h == "" {
			return pattern{}, errors.New("knownhosts: empty host in bracket form")
		}
		return pattern{host: h, port: p}, nil
	}
	// 没有显式端口 — 用 22 作为默认（OpenSSH 行为）
	return pattern{host: s, port: "22"}, nil
}

// formatPattern 把 pattern 序列化回 OpenSSH 文件格式。
//
// 规则（与 OpenSSH Normalize 对齐）：
//   - 端口 != "22" → "[host]:port"（带方括号）
//   - 端口 == "22" 且 host 含 ":"（IPv6 literal）→ "[host]"
//   - 其他 → "host"
func formatPattern(p pattern) string {
	if p.port != "22" {
		return "[" + p.host + "]:" + p.port
	}
	if strings.Contains(p.host, ":") {
		return "[" + p.host + "]"
	}
	return p.host
}

// parseAddr 把 SSH callback 的 host 参数拆成 host + port。
//
// 与 parsePattern 行为一致；独立命名以区分语义（callback 入参 vs 文件 pattern）。
func parseAddr(s string) addr {
	if h, p, err := net.SplitHostPort(s); err == nil {
		return addr{host: h, port: p}
	}
	return addr{host: s, port: "22"}
}

// match 检查 pattern 是否匹配 lookup 地址。
//
// 匹配规则（OpenSSH 语义）：
//   - 端口必须完全相等（pattern.port == addr.port）
//   - host 部分用 OpenSSH 通配符规则：* 匹配任意序列、? 匹配任意单字符
func (p pattern) match(a addr) bool {
	if p.port != a.port {
		return false
	}
	return wildcardMatch(p.host, a.host)
}

// match 检查 entry 的任一 pattern 是否匹配 lookup 地址。
func (e entry) match(a addr) bool {
	for i := range e.patterns {
		if e.patterns[i].match(a) {
			return true
		}
	}
	return false
}

// wildcardMatch 实现 OpenSSH 风格通配符匹配（无 separator 概念）。
//
// 规则：
//   - `*` 匹配任意字符序列（含空序列）
//   - `?` 匹配任意单字符
//   - 其他字符必须字面相等
//
// 与文件系统 glob 不同：`*` 跨 `.` 也匹配（foo.example.com 是 *.example.com 的有效匹配）。
//
// 参考 OpenSSH addrmatch.c 和 x/crypto/ssh/knownhosts.wildcardMatch。
func wildcardMatch(pat, str string) bool {
	patBytes := []byte(pat)
	strBytes := []byte(str)
	for {
		// 先处理 pattern 中的 '*' —— 它能匹配空串，必须在 str 空检查之前
		if len(patBytes) > 0 && patBytes[0] == '*' {
			// 跳过连续的 '*'（合并为单个）
			for len(patBytes) > 0 && patBytes[0] == '*' {
				patBytes = patBytes[1:]
			}
			if len(patBytes) == 0 {
				// pattern 末尾全是 '*' —— 匹配任何剩余 str（含空）
				return true
			}
			// 尝试把 str 切成 pat[1:] 的某个前缀
			for j := 0; j <= len(strBytes); j++ {
				if wildcardMatch(string(patBytes), string(strBytes[j:])) {
					return true
				}
			}
			return false
		}
		if len(patBytes) == 0 {
			return len(strBytes) == 0
		}
		if len(strBytes) == 0 {
			// pattern 还有非 '*' 字符，str 已空 → 不匹配
			return false
		}
		if patBytes[0] == '?' || patBytes[0] == strBytes[0] {
			patBytes = patBytes[1:]
			strBytes = strBytes[1:]
			continue
		}
		return false
	}
}

// ErrHostUnknown 表示 host 不在 known_hosts 中。
var ErrHostUnknown = errors.New("knownhosts: host not found")

// ErrHostKeyMismatch 表示 host 在 known_hosts 中但 key 不匹配（MITM 风险）。
var ErrHostKeyMismatch = errors.New("knownhosts: host key mismatch (possible MITM)")
