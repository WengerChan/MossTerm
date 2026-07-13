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
// v0.2.0a 重构：dial / OpenSession 改为后台 goroutine 异步执行，
// Open 立即返回 session 实例。状态机显式化：
//
//	Connecting (初始) → Authenticating (Dial 成功) → Established (OpenSession 成功)
//	                                                                  ↘
//	                                                                   Failed (任一步失败)
//
// 同步阶段（Open 内）：
//  1. 校验 req
//  2. 生成 UUID 作为 session ID
//  3. 解析 dialParams + sessionOpts
//  4. 从 registry 查 "ssh" factory；factory(deps) 构造 connector（struct 构造，毫秒级）
//  5. NewSessionImpl（初始 state=Connecting）+ 注册到 m.sessions
//  6. s.Start(ctx) 启动 readLoop / writeLoop / fanoutLoop
//     （readLoop / writeLoop 在 waitForSess 阻塞；fanoutLoop 立即可用，
//     保证后续 state 事件能立即 broadcast 给 subscriber）
//  7. 启动后台 dialInBackground goroutine
//  8. 返回 session
//
// 异步阶段（dialInBackground goroutine 内）：
//  9.  connector.Dial → 失败：state=Failed + signalDone
//  10. state=Authenticating + tryPublish
//  11. connector.OpenSession → 失败：conn.Close + state=Failed + signalDone
//  12. SetDialedSess(conn, sshSess) + state=Established + tryPublish
//
// 失败时 session 保留在 m.sessions（caller 仍可 Get + 查 Info），由 caller
// 决定是否调用 Close 清理 —— v0.2.0a 不自动 Close。
//
// 关闭中 dial 的处理：dialInBackground 在每次状态转换前检查 s.state，
// 若已被 Close 抢先（state=Closing/Closed），释放已分配资源后退出。
// 这保证 Close 在 dial 中途被调用时不会泄漏 conn。
//
// 边界：
//   - dial 自身的 ctx 是 caller 传入的；Close 不取消 ctx。
//     若要立即终止长时间 dial，caller 应传入带超时的 ctx 或自行 cancel。
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
	deps.Secrets = secrets       // 注入凭据存储（publickey 用）
	deps.KnownHosts = knownHosts // 注入 known_hosts（host key 校验）
	connector, err := factory(deps)
	if err != nil {
		return nil, fmt.Errorf("session.MemoryManager.Open: build ssh connector: %w", err)
	}

	// 5. 构造 sessionImpl，初始 state=Connecting
	now := time.Now().UnixMilli()
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
		State:     StateConnecting,
		CreatedAt: now,
		Cols:      sessionOpts.Cols,
		Rows:      sessionOpts.Rows,
	}
	s := NewSessionImpl(id, connector, dialParams, sessionOpts, info)

	// 6. 注册（在 Start 之前 —— Start 启动的 fanoutLoop 需要 session 已可见，
	//    否则 dial 期间 tryPublish 的 state 事件进 events 通道却没人接收）
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	// 7. 启动 3 个 loop。fanoutLoop 立即可用；readLoop/writeLoop 在
	//    waitForSess 阻塞直到 SetDialedSess。
	if err := s.Start(ctx); err != nil {
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		_ = s.Close(true)
		return nil, fmt.Errorf("session.MemoryManager.Open: start: %w", err)
	}

	// 8. 启动后台 dial goroutine
	go m.dialInBackground(ctx, s, dialParams, sessionOpts)

	slog.Default().Info("session open started",
		"id", string(id),
		"host", req.Host,
		"port", port,
		"user", req.User,
	)
	return s, nil
}

// dialInBackground 是 Open 启动的后台 goroutine，负责 dial + OpenSession。
//
// 状态转换：
//
//	Connecting → (Dial 成功) → Authenticating → (OpenSession 成功) → Established
//	任意阶段失败 → Failed
//
// 失败时 s.signalDone 关闭 s.done —— 让 readLoop / writeLoop / fanoutLoop 退出。
// 之前已注册的 subscriber 仍可通过 Info().State 看到 Failed（state 事件已 publish）。
//
// 关闭语义 + race 处理：
//   每一步状态转换用 setStateIf（CAS）做"check-and-set"——如果 Close 已经
//   抢先把 state 推到 Closing/Closed，CAS 失败，本 goroutine 释放已分配资源
//   （conn.Close / sshSess.Close）后退出，不覆盖 Close 写入的 Closed。
//
//   这避免了 v0.1.x 那种"A: isClosedOrClosing()=false / B: Close 把 state→Closed
//   / A: setState(Authenticating) 覆盖 Closed"的 race（subscriber 会看到
//   Connecting → Closed → Authenticating 的荒谬时序）。
//
// 边界：
//   - dial 自身的 ctx 是 caller 传入的；Close 不取消 ctx。
//     若需立即取消长时间 dial，caller 应传入带超时的 ctx 或自行 cancel。
func (m *MemoryManager) dialInBackground(
	ctx context.Context,
	s *sessionImpl,
	dialParams connect.DialParams,
	opts connect.SessionOpts,
) {
	// 1. Dial
	conn, err := s.conn.Dial(ctx, dialParams)
	if err != nil {
		// CAS(Connecting, Failed)：若 state 已被 Close 推到 Closing/Closed 则失败，
		// 直接退出（Close 会处理关闭流程）。
		if !s.setStateIf(StateConnecting, StateFailed) {
			return
		}
		s.tryPublish(newStateEvent(StateFailed))
		s.signalDone() // 唤醒 readLoop / writeLoop / fanoutLoop
		slog.Default().Warn("session dial failed",
			"id", string(s.id),
			"host", s.info.Load().Host,
			"err", err,
		)
		return
	}

	// 2. Connecting → Authenticating
	//    失败说明 Close 已抢先（state=Closing/Closed）；释放 conn 防止泄漏。
	if !s.setStateIf(StateConnecting, StateAuthenticating) {
		_ = conn.Close()
		return
	}
	s.tryPublish(newStateEvent(StateAuthenticating))

	// 3. OpenSession
	sshSess, err := s.conn.OpenSession(ctx, conn, opts)
	if err != nil {
		_ = conn.Close()
		// CAS(Authenticating, Failed)：state 已被 Close 推到 Closing/Closed 则失败。
		if !s.setStateIf(StateAuthenticating, StateFailed) {
			return
		}
		s.tryPublish(newStateEvent(StateFailed))
		s.signalDone()
		slog.Default().Warn("session open session failed",
			"id", string(s.id),
			"host", s.info.Load().Host,
			"err", err,
		)
		return
	}

	// 4. OpenSession 成功；先 SetDialedSess 激活 readLoop/writeLoop，
	//    再 CAS(Authenticating, Established)。
	//
	//    SetDialedSess 在 CAS 之前的好处：即使 Close 在中间抢跑（CAS 失败），
	//    conn/sshSess 已在 s.sess/s.dialed 中；Close 的 connMu.Lock 会
	//    拿走引用并 close，不泄漏。
	s.SetDialedSess(conn, sshSess)

	// CAS(Authenticating, Established)：Close 抢先则失败；conn/sess 已被
	// Close 取走并 close（见上），本 goroutine 直接退出。
	if !s.setStateIf(StateAuthenticating, StateEstablished) {
		return
	}
	s.tryPublish(newStateEvent(StateEstablished))

	slog.Default().Info("session established",
		"id", string(s.id),
		"host", s.info.Load().Host,
	)
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
