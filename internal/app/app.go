// Package app 是 MossTerm 的应用层：依赖注入 + Wails 生命周期钩子。
//
// 核心职责：
//  1. 加载配置（config.Manager）。
//  2. 初始化凭据存储（secret.Store）。
//  3. 构造 session.Manager / transfer.Engine / tunnel.Manager / agent.Registry / plugin.Host / ai.Client。
//  4. 把这些依赖组合到 *App 上，供 ui/wailsbindings 暴露给前端。
//  5. 把 Wails 的事件总线抽象成 EventEmitter 接口，避免 app 包直接 import Wails。
package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/mossterm/mossterm/internal/agent"
	"github.com/mossterm/mossterm/internal/ai"
	"github.com/mossterm/mossterm/internal/config"
	"github.com/mossterm/mossterm/internal/connect"
	"github.com/mossterm/mossterm/internal/knownhosts"
	"github.com/mossterm/mossterm/internal/plugin"
	"github.com/mossterm/mossterm/internal/secret"
	"github.com/mossterm/mossterm/internal/session"
	"github.com/mossterm/mossterm/internal/sftpclient"
	"github.com/mossterm/mossterm/internal/sshclient"
	"github.com/mossterm/mossterm/internal/transfer"
	"github.com/mossterm/mossterm/internal/tunnel"
)

// EventEmitter 是 Wails 事件总线的薄抽象。
//
// 设计目的：app 包不直接 import wails 包（避免传递性循环依赖，
// 同时让核心层可以独立单测）。实现由 main.go 注入（见 cmd/mossterm）。
//
// 行为对齐 Wails 的 runtime.EventsEmit：ctx 必须非 nil；
// optionalData 是任意可被 JSON 序列化的值。
type EventEmitter interface {
	Emit(ctx context.Context, event string, data ...interface{})
}

// App 是 MossTerm 后端的"根对象"。
//
// 所有跨模块协作都以 *App 为中心。
// Wails 运行时通过反射读取公开方法（OnStartup / OnDomReady / OnShutdown）
// 并自动调用。
type App struct {
	ctx context.Context

	cfg        *config.Manager
	secret     secret.Store
	sessions   session.Manager
	transfers  transfer.Engine
	tunnels    tunnel.Manager
	agents     agent.Registry
	plugins    plugin.Host
	ai         ai.Client
	knownHosts *knownhosts.Manager

	// connectors 是 connect.Connector 的注册表，被 sessions.Manager 共享。
	// v0.1 默认注册 "ssh" scheme → sshclient.Factory。
	connectors connect.Registry

	// emitter 是 Wails 事件总线的薄抽象。
	// nil 时 Emit 退化为 log（避免 nil 指针 panic）。
	emitter EventEmitter

	// sftpClients 按 sessionID 缓存 SFTP 客户端（v0.5.1+）。
	//
	// 懒加载：第一次 SftpXxx 调用时通过 sftpFor() 打开 SFTP subsystem，
	// 之后复用同一实例（sftp pkg 的 client 是 goroutine unsafe，
	// 复用避免每个请求都开/关 subsystem）。
	//
	// 生命周期：绑到 session 状态机 —— session 关 → lazy eviction
	// （下次 sftpFor 看到 state=Closed 就删 map 条目），
	// App.Close 时全部关闭 + 清空。
	//
	// sftpMu 保护整个 map 的并发读写；sftpFor 持 sftpMu 时**不**再持
	// session / connector 的内部锁（避免死锁，参见 sftpFor 注释）。
	sftpMu      sync.Mutex
	sftpClients map[session.ID]*sftpclient.Client

	// openSftp 是打开 SFTP subsystem 的可注入函数（v0.5.1+ 测试钩子）。
	//
	// 默认为包装 sftpclient.Open；测试可以替换为 mock，跳过真实 SFTP
	// subsystem 协商（v0.5.1 测试不依赖真实 SFTP server）。
	//
	// 设计动机：sftpclient.Open 内部要 call *ssh.Client.NewSession() +
	// RequestSubsystem("sftp")，需要一个真支持 SFTP subsystem 的 server。
	// 单元测试只关心 app 层的 map 生命周期（懒加载 / 复用 / lazy evict /
	// App.Close 清理），不应该为这个而启动一个 sftp.NewServer。
	openSftp func(*ssh.Client) (*sftpclient.Client, error)

	log *slog.Logger
}

// Deps 是 New 的入参。
//
// 由 cmd/mossterm 在 main 中构造并注入；
// 单元测试可以只填部分字段。
type Deps struct {
	Cfg       *config.Manager
	Secret    secret.Store
	Sessions  session.Manager
	Transfers transfer.Engine
	Tunnels   tunnel.Manager
	Agents    agent.Registry
	Plugins   plugin.Host
	AI        ai.Client

	// KnownHosts 是 known_hosts 文件管理器（v0.1.3+），同时被 session.Manager
	// 注入到 sshclient（用于 HostKeyCallback）和 wailsbindings 引用
	// （用于 TrustHost → ReplyTrust）。
	//
	// 可选；nil 时 sshclient 兜底为 InsecureIgnoreHostKey + 自动信任，
	// wailsbindings.TrustHost 返回 "known_hosts not initialized"。
	KnownHosts *knownhosts.Manager

	// Connectors 是外部传入的 connect.Registry；为 nil 时 New 自己创建
	// 一个空 registry，然后通过 WireDefaultConnectors 注册 sshclient。
	Connectors connect.Registry

	// Emitter 是 Wails 事件总线适配器（main.go 注入）。
	// 可选；nil 时 Emit 退化为 log。
	Emitter EventEmitter

	Log *slog.Logger
}

// New 用 Deps 构造一个 *App。
//
// Deps 中任何 nil 字段都用一个"无操作 / 报错"的默认实现兜底，
// 保证 *App 永远不会因为 nil 依赖 panic。
func New(deps Deps) *App {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Sessions == nil {
		deps.Sessions = session.NewMemoryManager()
	}
	if deps.Transfers == nil {
		deps.Transfers = transfer.New()
	}
	if deps.Tunnels == nil {
		deps.Tunnels = tunnel.NewMemoryManager()
	}
	if deps.Agents == nil {
		deps.Agents = agent.NewMemoryRegistry()
	}
	if deps.Plugins == nil {
		deps.Plugins = plugin.NewMemoryHost()
	}

	// connector registry 必须在 sessions 之前准备好；
	// 这样如果 deps.Sessions 是 *session.MemoryManager，可以直接挂上。
	registry := deps.Connectors
	if registry == nil {
		registry = connect.NewMemoryRegistry()
	}

	// 注册默认协议 factory（v0.1：仅 ssh）。
	// WireDefaultConnectors 内部使用 _ = err 模式忽略"已注册"错误，
	// 这样调用方如果在 Deps.Connectors 里预注册了同 scheme，会被忽略。
	if err := WireDefaultConnectors(registry); err != nil {
		deps.Log.Warn("app.New: wire default connectors failed", "err", err)
	}

	// 把 registry 注入到 *session.MemoryManager（如果是它）。
	if mm, ok := deps.Sessions.(*session.MemoryManager); ok {
		mm.WithConnectors(registry)
	}

	a := &App{
		cfg:         deps.Cfg,
		secret:      deps.Secret,
		sessions:    deps.Sessions,
		transfers:   deps.Transfers,
		tunnels:     deps.Tunnels,
		agents:      deps.Agents,
		plugins:     deps.Plugins,
		ai:          deps.AI,
		knownHosts:  deps.KnownHosts,
		connectors:  registry,
		emitter:     deps.Emitter,
		sftpClients: make(map[session.ID]*sftpclient.Client),
		log:         deps.Log,
	}

	// 默认 openSftp = 包装 sftpclient.Open。
	// 单元测试可以直接覆盖 a.openSftp，跳过真实 SFTP subsystem 协商。
	a.openSftp = func(c *ssh.Client) (*sftpclient.Client, error) {
		return sftpclient.Open(c)
	}

	return a
}

// OnStartup 是 Wails 生命周期钩子（webview 启动时调用一次）。
//
// 方法名首字母大写：Wails 反射绑定。
func (a *App) OnStartup(ctx context.Context) {
	a.ctx = ctx
	a.log.Info("app: OnStartup", "ctx", ctx != nil)
	// v0.1 留 hook 给后续 milestone 注入：
	//   - transfer 队列 worker
	//   - host key 数据库加载
	//   - agent client 启动
}

// OnDomReady 是 Wails 生命周期钩子（webview DOM ready 时调用一次）。
//
// 此时前端已可接收事件；通过 emitter 推 "app:ready" 让前端知道后端就绪。
func (a *App) OnDomReady(ctx context.Context) {
	a.log.Info("app: OnDomReady")
	a.Emit("app:ready", map[string]any{
		"version": "0.1.0",
		"ready":   true,
	})
}

// OnShutdown 是 Wails 生命周期钩子（窗口关闭前调用）。
//
// 必须：关闭所有 Session → flush 配置 → close secret store → close SFTP 客户端。
func (a *App) OnShutdown(ctx context.Context) {
	a.log.Info("app: OnShutdown")
	if a.sessions != nil {
		_ = a.sessions.CloseAll(ctx)
	}
	// 关闭 SFTP 客户端：sessions.CloseAll 之后 *ssh.Client 本身还活着
	// （keepalive 协程未退出），但 channel 关闭使 SFTP 操作自然失败；
	// 这里显式 Close 把 sftp subsystem 资源释放掉。
	a.closeSftpClients()
	if a.cfg != nil {
		_ = a.cfg.Save()
	}
	if a.secret != nil {
		_ = a.secret.Close()
	}
}

// Emit 是对 EventEmitter.Emit 的薄包装。
//
// nil emitter 时退化为 log（不阻塞业务路径）。
func (a *App) Emit(event string, data ...interface{}) {
	if a == nil {
		return
	}
	if a.emitter == nil {
		a.log.Debug("app: emit (no emitter)", "event", event)
		return
	}
	if a.ctx == nil {
		// 极端情况：OnStartup 未调用；用 background ctx 兜底
		a.emitter.Emit(context.Background(), event, data...)
		return
	}
	a.emitter.Emit(a.ctx, event, data...)
}

// SetEmitter 在 OnStartup 之后被允许替换 emitter（测试用）。
func (a *App) SetEmitter(e EventEmitter) {
	a.emitter = e
}

// -----------------------------------------------------------------------------
// 私有方法（不暴露给前端）
// -----------------------------------------------------------------------------

// Ctx 返回 Wails 注入的 ctx，session.Manager 等模块用它派生请求 ctx。
func (a *App) Ctx() context.Context { return a.ctx }

// Cfg 返回 config.Manager 引用。
func (a *App) Cfg() *config.Manager { return a.cfg }

// Secret 返回 secret.Store 引用。
func (a *App) Secret() secret.Store { return a.secret }

// Sessions 返回 session.Manager 引用。
func (a *App) Sessions() session.Manager { return a.sessions }

// Connectors 返回 connect.Registry 引用。
func (a *App) Connectors() connect.Registry { return a.connectors }

// KnownHosts 返回 known_hosts 管理器引用。
//
// 主要供 wailsbindings.TrustHost 调用 ReplyTrust；
// 业务模块（sshclient）通过 connect.Deps 拿到同一份引用。
//
// 返回 nil 时说明 main.go 没注入 known_hosts（极少见，仅单元测试），
// 调用方应自行兜底为"功能不可用"。
func (a *App) KnownHosts() *knownhosts.Manager { return a.knownHosts }

// Log 返回结构化 logger。
func (a *App) Log() *slog.Logger { return a.log }

// -----------------------------------------------------------------------------
// SFTP 客户端生命周期（v0.5.1+）
// -----------------------------------------------------------------------------

// SftpClient 是 sftpFor 的公开包装，供 wailsbindings 调用。
//
// 公开原因：wailsbindings 在 internal/ui/wailsbindings 包，
// 无法访问 *App.sftpFor（私有方法）；这是 Wails 反射的边界 ——
// sftpFor 是 *App 内部实现，SftpClient 是 stable Go API。
//
// 本身不暴露给前端（Wails 反射的 *sftpclient.Client 含未导出字段，
// 不会被识别为可绑定类型；前端的 SFTP 操作走 wailsbindings 的 8 个 binding）。
func (a *App) SftpClient(sessID session.ID) (*sftpclient.Client, error) {
	return a.sftpFor(sessID)
}

// sftpFor 返回 session 对应的 SFTP client（懒加载）。
//
// 第一次调用时从 session 拿 *ssh.Client，开 SFTP subsystem。
// 后续调用复用 map 里的同一实例。
//
// 错误返回：
//   - sessionID 在 Manager 中不存在（从未 Open 或已 Close）
//   - session.state != Established（dial 失败 / 正在连 / 已关）
//   - 拿不到 *sshclient.Connector（type assert 失败，理论上不会
//     因为 wire.go 总是注册 ssh client factory）
//   - sshclient.Connector.RawClient() 返回 nil（Dial 未成功；
//     理论上 state==Established 一定 Dial 成功过）
//   - openSftp 失败（subsystem 协商失败 —— 旧 server 不支持 sftp 等）
//
// 死锁防御：sftpFor 持 sftpMu 时**不**再调用任何会拿 session / connector
// 内部锁的方法。访问 session 用 Manager.Get（独立 RLock，不持 sftpMu）。
// 访问 sshclient.Connector.RawClient 只读字段，无锁。
//
// Lazy eviction：session 状态走到 Closed / Failed 时，下一次 sftpFor
// 调用会从 map 删条目 + 返回 error，调用方（wailsbindings）拿到 error
// 后通常会让前端关闭文件浏览器并提示重新连接。
func (a *App) sftpFor(sessID session.ID) (*sftpclient.Client, error) {
	if a == nil {
		return nil, fmt.Errorf("app.sftpFor: nil app")
	}

	// 1. 命中：sftpMu 内直接返回。
	//
	// 验证缓存有效：client != nil 且 session state 仍 Established。
	// state 不再 Established（关 / 失败）→ lazy evict。
	a.sftpMu.Lock()
	cached, ok := a.sftpClients[sessID]
	a.sftpMu.Unlock()
	if ok && cached != nil {
		// 拿 session 验证状态（**不**持 sftpMu 时调用 Get，避免死锁）
		sess, exists := a.sessions.Get(sessID)
		if !exists {
			// session 已从 Manager 移除 → 缓存无效
			a.sftpMu.Lock()
			delete(a.sftpClients, sessID)
			a.sftpMu.Unlock()
			return nil, fmt.Errorf("app.sftpFor: session %q not found", sessID)
		}
		if st := sess.State(); st != session.StateEstablished {
			// session 状态不健康 → 缓存失效
			a.sftpMu.Lock()
			delete(a.sftpClients, sessID)
			// 拿到 client 引用，释放锁后再 Close
			toClose := cached
			a.sftpMu.Unlock()
			_ = toClose.Close()
			return nil, fmt.Errorf("app.sftpFor: session %q not established (state=%s)", sessID, st)
		}
		return cached, nil
	}

	// 2. miss：拿 session
	sess, exists := a.sessions.Get(sessID)
	if !exists {
		return nil, fmt.Errorf("app.sftpFor: session %q not found", sessID)
	}

	// 3. 验状态
	if st := sess.State(); st != session.StateEstablished {
		return nil, fmt.Errorf("app.sftpFor: session %q not established (state=%s)", sessID, st)
	}

	// 4. 拿 connector（v0.5.1 新增的 Session.Connector() 访问器）
	connector := sess.Connector()
	if connector == nil {
		return nil, fmt.Errorf("app.sftpFor: session %q has nil connector", sessID)
	}

	// 5. type assert 回 *sshclient.Connector
	//
	// v0.5.1+ 范围：只有 sshclient 支持 SFTP subsystem。如果未来加
	// telnetclient 等新协议且不支持 sftp，type assert 失败 → 返回
	// 明确错误（"SFTP not supported on this protocol"）。
	sshConn, ok := connector.(*sshclient.Connector)
	if !ok {
		return nil, fmt.Errorf("app.sftpFor: session %q connector is %T, want *sshclient.Connector (SFTP only supported on SSH)", sessID, connector)
	}

	// 6. 拿底层 *ssh.Client
	sshClient := sshConn.RawClient()
	if sshClient == nil {
		// 理论上 state==Established 一定 Dial 成功过；
		// 若走到这，多半是 Connector 被复用但从未 Dial，理论上不会。
		return nil, fmt.Errorf("app.sftpFor: session %q ssh client is nil (Dial succeeded but client not set?)", sessID)
	}

	// 7. 开 SFTP subsystem（通过 openSftp 钩子，测试可替换为 mock）
	client, err := a.openSftp(sshClient)
	if err != nil {
		return nil, fmt.Errorf("app.sftpFor: open sftp subsystem: %w", err)
	}

	// 8. 写回 map
	//
	// 关键：sftpMu 内**只做 map 写**，不动 client 引用本身（client 由调用方持有）。
	a.sftpMu.Lock()
	// 二次检查：并发场景下可能另一个 goroutine 已经开过并写入
	if existing, ok := a.sftpClients[sessID]; ok && existing != nil {
		a.sftpMu.Unlock()
		// 关闭新开的，复用已存在的（first-write-wins 语义）
		_ = client.Close()
		return existing, nil
	}
	a.sftpClients[sessID] = client
	a.sftpMu.Unlock()
	return client, nil
}

// closeSftpClients 关闭并清空所有缓存的 SFTP 客户端。
//
// 调用场景：App.OnShutdown / Wails 关闭前 / 测试 App.Close()。
// 不返回 error：内部 _ = Close 吞掉（"尽力关闭"，剩余连接会随进程退出释放）。
// 不持锁调用 Close（避免 sftp client 内部阻塞反向影响其它清理路径）。
func (a *App) closeSftpClients() {
	if a == nil {
		return
	}
	a.sftpMu.Lock()
	clients := a.sftpClients
	a.sftpClients = make(map[session.ID]*sftpclient.Client)
	a.sftpMu.Unlock()
	for id, c := range clients {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil {
			a.log.Debug("app.closeSftpClients: close failed", "id", string(id), "err", err)
		}
	}
}

// Close 关闭 App 持有的所有 SFTP 客户端。
//
// 这是一个公开方法（不暴露给前端）让外部（如 wailsbindings 或测试）能
// 在合适时机清理。当前 v0.5.1 由 OnShutdown 内部调用 closeSftpClients。
//
// 设计：与 sessions.CloseAll 平行 —— sessions.CloseAll 释放 SSH 连接
// 资源（connMu / conn / sess），closeSftpClients 释放 SFTP subsystem 资源。
// 两者顺序无关（SFTP subsystem 依赖 *ssh.Client 的 channel，而 *ssh.Client
// 在 sessions.CloseAll 之后仍存活但 channel 已关；SFTP 调用会自然失败）。
func (a *App) Close() {
	a.closeSftpClients()
}
