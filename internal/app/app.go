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
	"log/slog"

	"github.com/mossterm/mossterm/internal/agent"
	"github.com/mossterm/mossterm/internal/ai"
	"github.com/mossterm/mossterm/internal/config"
	"github.com/mossterm/mossterm/internal/connect"
	"github.com/mossterm/mossterm/internal/knownhosts"
	"github.com/mossterm/mossterm/internal/plugin"
	"github.com/mossterm/mossterm/internal/secret"
	"github.com/mossterm/mossterm/internal/session"
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

	return &App{
		cfg:        deps.Cfg,
		secret:     deps.Secret,
		sessions:   deps.Sessions,
		transfers:  deps.Transfers,
		tunnels:    deps.Tunnels,
		agents:     deps.Agents,
		plugins:    deps.Plugins,
		ai:         deps.AI,
		knownHosts: deps.KnownHosts,
		connectors: registry,
		emitter:    deps.Emitter,
		log:        deps.Log,
	}
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
// 必须：关闭所有 Session → flush 配置 → close secret store。
func (a *App) OnShutdown(ctx context.Context) {
	a.log.Info("app: OnShutdown")
	if a.sessions != nil {
		_ = a.sessions.CloseAll(ctx)
	}
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
