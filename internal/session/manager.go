// Package session 内部文件：MemoryManager 真实实现。
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/mossterm/mossterm/internal/connect"
	"github.com/mossterm/mossterm/internal/knownhosts"
	"github.com/mossterm/mossterm/internal/secret"
)

// Manager 维护所有活跃 Session 的注册表与生命周期。
//
// Manager 是无状态的（除内部 map 之外）；多个实例之间不共享。
// App 持有一个 *Manager 即可，singleton 通过 DI 实现。
type Manager interface {
	// Open 创建一个新 Session 并开始握手。
	// 返回的 Session 已被 Start，但调用方需自行 Subscribe。
	Open(ctx context.Context, req OpenRequest) (Session, error)
	// Get 按 ID 取出一个 Session。
	Get(id ID) (Session, bool)
	// List 返回所有活跃 Session 的 Info 快照。
	List() []Info
	// Close 关闭指定 Session。force=true 时跳过优雅退出。
	Close(id ID, force bool) error
	// CloseAll 关闭全部 Session；用于应用退出阶段。
	CloseAll(ctx context.Context) error
}

// MemoryManager 是 Manager 的进程内实现。
//
// 线程安全：所有公开方法都用 RWMutex 保护。
type MemoryManager struct {
	mu       sync.RWMutex
	sessions map[ID]Session

	// connectors 是协议 connector 的注册表。
	//
	// v0.1 默认通过 NewMemoryManager 自带一个空 registry；
	// app/wire.go 会在启动时把 sshclient.Factory 注册进去。
	// 用户也可以通过 WithConnectors 注入自己的 registry。
	connectors connect.Registry

	// secrets 是凭据存储，用于 publickey auth 时从 secret.Store 拉私钥。
	// nil 时 publickey 路径会返回明确错误（提示用户未初始化凭据存储）。
	secrets secret.Store

	// knownHosts 是 known_hosts 文件管理器（v0.1.3+）。
	// nil 时回退到 InsecureIgnoreHostKey（v0.1 行为，⚠️ MITM 风险）。
	knownHosts *knownhosts.Manager
}

// NewMemoryManager 构造一个空的 Manager，自带一个空 connect.Registry。
//
// 调用方需要在使用 Open 之前把协议 factory 注册进 m.connectors。
// 直接使用本构造函数得到的 Manager，Open 总会返回 "no factory for scheme"。
func NewMemoryManager() *MemoryManager {
	return &MemoryManager{
		sessions:   make(map[ID]Session),
		connectors: connect.NewMemoryRegistry(),
	}
}

// WithConnectors 注入一个外部 connect.Registry（用于测试 / 多 Manager 共享）。
//
// 返回 m 自身，支持链式调用：
//
//	mm := session.NewMemoryManager().WithConnectors(reg)
func (m *MemoryManager) WithConnectors(r connect.Registry) *MemoryManager {
	if r == nil {
		return m
	}
	m.mu.Lock()
	m.connectors = r
	m.mu.Unlock()
	return m
}

// WithSecrets 注入一个 secret.Store（用于 publickey auth 时拉私钥）。
//
// 链式调用：
//
//	mm := session.NewMemoryManager().
//	    WithConnectors(reg).
//	    WithSecrets(sec)
//
// secrets 为 nil 时 publickey 路径会返回错误（保持向后兼容）。
func (m *MemoryManager) WithSecrets(s secret.Store) *MemoryManager {
	if s == nil {
		return m
	}
	m.mu.Lock()
	m.secrets = s
	m.mu.Unlock()
	return m
}

// WithKnownHosts 注入一个 knownhosts.Manager（用于 host key 校验）。
//
// 链式调用：
//
//	mm := session.NewMemoryManager().
//	    WithConnectors(reg).
//	    WithKnownHosts(kh)
//
// kh 为 nil 时 sshclient 兜底为 InsecureIgnoreHostKey（保持向后兼容）。
func (m *MemoryManager) WithKnownHosts(kh *knownhosts.Manager) *MemoryManager {
	if kh == nil {
		return m
	}
	m.mu.Lock()
	m.knownHosts = kh
	m.mu.Unlock()
	return m
}

// Open 实现 Manager.Open。
//
// 流程：
//  1. 校验 req
//  2. 生成 UUID 作为 session ID
//  3. 解析 dialParams + sessionOpts
//  4. 从 connectors registry 查 "ssh" scheme 的 factory
//  5. 用 factory(deps) 构造一个 sshclient.Connector
//  6. 同步执行 connector.Dial + connector.OpenSession
//     （任何一步失败都把 session 从 m.sessions 移除并返回 error）
//  7. 构造 sessionImpl，把 dialed/sess 注入
//  8. s.Start(ctx) 启动 read/write/fanout loop
//  9. 注册到 m.sessions
//  10. 返回
func (m *MemoryManager) Open(ctx context.Context, req OpenRequest) (Session, error) {
	// 1. 校验
	if err := validateOpenRequest(req); err != nil {
		return nil, err
	}

	// 2. UUID
	id := ID(uuid.NewString())

	// 3. 转换
	dialParams, err := DialParamsFrom(req)
	if err != nil {
		return nil, fmt.Errorf("session.MemoryManager.Open: %w", err)
	}
	sessionOpts := SessionOptsFrom(req)

	// 4. 解析 connector
	m.mu.RLock()
	registry := m.connectors
	secrets := m.secrets
	knownHosts := m.knownHosts
	m.mu.RUnlock()
	if registry == nil {
		return nil, errors.New("session.MemoryManager.Open: connector registry is nil")
	}
	factory, ok := registry.Get("ssh")
	if !ok {
		return nil, errors.New("session.MemoryManager.Open: no factory registered for scheme \"ssh\"")
	}
	deps := connect.StdDeps()
	deps.Secrets = secrets      // 注入凭据存储（publickey 用）
	deps.KnownHosts = knownHosts // 注入 known_hosts（host key 校验）
	connector, err := factory(deps)
	if err != nil {
		return nil, fmt.Errorf("session.MemoryManager.Open: build ssh connector: %w", err)
	}

	// 5. Dial（同步）
	conn, err := connector.Dial(ctx, dialParams)
	if err != nil {
		return nil, fmt.Errorf("session.MemoryManager.Open: dial: %w", err)
	}

	// 6. OpenSession（同步）—— 失败必须回滚 conn
	sess, err := connector.OpenSession(ctx, conn, sessionOpts)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("session.MemoryManager.Open: open session: %w", err)
	}

	// 7. 构造 sessionImpl
	now := time.Now().UnixMilli()
	initialState := StateEstablished // Dial + OpenSession 已成功
	port := req.Port
	if port == 0 {
		port = 22
	}
	info := Info{
		ID:        id,
		Name:      req.Host, // TODO: profile.Name
		Host:      req.Host,
		Port:      port,
		User:      req.User,
		Protocol:  "ssh",
		State:     initialState,
		CreatedAt: now,
		Cols:      sessionOpts.Cols,
		Rows:      sessionOpts.Rows,
	}
	s := NewSessionImpl(id, connector, dialParams, sessionOpts, info)
	s.SetDialedSess(conn, sess)

	// 8. 注册（在 Start 之前，避免 Start 启动的 goroutine 看到没注册的 session）
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	// 9. Start 启动 loop —— 失败则回滚
	if err := s.Start(ctx); err != nil {
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		_ = s.Close(true)
		return nil, fmt.Errorf("session.MemoryManager.Open: start: %w", err)
	}

	slog.Default().Info("session opened",
		"id", string(id),
		"host", req.Host,
		"port", port,
		"user", req.User,
	)
	return s, nil
}

// validateOpenRequest 校验 OpenRequest 的必填字段。
func validateOpenRequest(req OpenRequest) error {
	if req.Host == "" {
		return errors.New("empty host")
	}
	if req.User == "" {
		return errors.New("empty user")
	}
	if req.Port < 0 || req.Port > 65535 {
		return fmt.Errorf("invalid port %d", req.Port)
	}
	// 注意：req.Auth 即使为空（"password" + ""）也会在 DialParamsFrom
	// 阶段被拒 —— 这里不重复校验。
	return nil
}

// Get 实现 Manager.Get。
func (m *MemoryManager) Get(id ID) (Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// List 实现 Manager.List。
func (m *MemoryManager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Info, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.Info())
	}
	return out
}

// Close 实现 Manager.Close。
func (m *MemoryManager) Close(id ID, force bool) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session.Manager.Close: id %q not found", id)
	}
	delete(m.sessions, id)
	m.mu.Unlock()
	return s.Close(force)
}

// CloseAll 实现 Manager.CloseAll。
func (m *MemoryManager) CloseAll(ctx context.Context) error {
	m.mu.Lock()
	all := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.sessions = make(map[ID]Session)
	m.mu.Unlock()

	var firstErr error
	for _, s := range all {
		if err := s.Close(true); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// 编译期断言：*MemoryManager 满足 Manager 接口。
var _ Manager = (*MemoryManager)(nil)
