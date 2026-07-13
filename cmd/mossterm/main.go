// MossTerm 主入口。
//
// 解析 CLI flag，构造 *app.App，调用 wails.Run() 启动 webview。
// 处理 SIGINT / SIGTERM 优雅关闭。
//
// 使用方式：
//   mossterm                       # 正常启动
//   mossterm --debug               # 启用 devtools
//   mossterm --config=path.toml    # 指定配置文件
//   mossterm --no-gpu              # 关闭硬件加速
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/mossterm/mossterm/internal/agent"
	"github.com/mossterm/mossterm/internal/app"
	"github.com/mossterm/mossterm/internal/config"
	"github.com/mossterm/mossterm/internal/connect"
	"github.com/mossterm/mossterm/internal/knownhosts"
	"github.com/mossterm/mossterm/internal/secret"
	"github.com/mossterm/mossterm/internal/session"
	"github.com/mossterm/mossterm/internal/sshclient"
	"github.com/mossterm/mossterm/internal/ui/wailsbindings"
)

//go:embed frontend/dist
var assets embed.FS

// cliFlags 收集所有命令行选项。
type cliFlags struct {
	debug    bool
	config   string
	noGPU    bool
	logLevel string
}

func main() {
	flags := parseFlags()
	logger := buildLogger(flags.logLevel)
	slog.SetDefault(logger)

	if err := run(flags, logger); err != nil {
		logger.Error("mossterm exited with error", "err", err)
		os.Exit(1)
	}
}

// parseFlags 解析命令行 flag。
func parseFlags() *cliFlags {
	f := &cliFlags{}
	flag.BoolVar(&f.debug, "debug", false, "enable Wails devtools")
	flag.StringVar(&f.config, "config", "", "path to config.toml (default: ~/.config/mossterm/config.toml)")
	flag.BoolVar(&f.noGPU, "no-gpu", false, "disable hardware acceleration")
	flag.StringVar(&f.logLevel, "log-level", "info", "log level: debug|info|warn|error")
	flag.Parse()
	return f
}

// buildLogger 构造一个 slog.Logger。
func buildLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv})
	return slog.New(h)
}

// run 是真正的启动逻辑；分离出来方便测试。
//
// 启动流程（伪代码）：
//
//	cfg, _ := config.New(flags.config)               // 1. 加载配置
//	sec, _ := secret.New({UseSystemKeyring: true})   // 2. 凭据存储
//	ag  := agent.NewMemoryRegistry()                 // 3. 跳板 registry
//	reg := connect.NewMemoryRegistry()               // 4. 协议 registry
//	reg.Register("ssh", sshAdapter)                  //    注册 ssh factory
//	mm  := session.NewMemoryManager().
//	    WithConnectors(reg).
//	    WithSecrets(sec).
//	    WithKnownHosts(kh)                            // 5. session (publickey + host key)
//	app.New(app.Deps{...}) → *app.App                // 6. 装配 app
//	wailsbindings.New(app) → *wailsbindings.App      // 7. 绑定层
//	wails.Run(&options.App{Bind: []any{api}, ...})   // 8. 启动
func run(flags *cliFlags, logger *slog.Logger) error {
	// 1. 加载 / 初始化配置
	cfg, err := config.New(flags.config)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger.Info("config loaded", "path", cfg.Path())

	// 2. 凭据存储：优先系统 keyring，失败自动降级到加密文件
	sec, err := secret.New(secret.Config{
		UseSystemKeyring: true,
		// FallbackPath 由 secret.New 解析为 ~/.config/mossterm/secrets.enc
		Argon2Params: secret.DefaultArgon2Params,
	})
	if err != nil {
		return fmt.Errorf("init secret store: %w", err)
	}
	logger.Info("secret store ready")

	// 2.5 known_hosts：host key 持久化（v0.1.3+）
	//
	// 放在 config.toml 同目录下（默认 ~/.config/mossterm/known_hosts），
	// 与 OpenSSH 不共用，便于隔离与排查。
	knownHostsPath := filepath.Join(filepath.Dir(cfg.Path()), "known_hosts")
	kh, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return fmt.Errorf("init known_hosts at %s: %w", knownHostsPath, err)
	}
	logger.Info("known_hosts ready", "path", knownHostsPath, "entries", kh.Size())

	// 3. 跳板策略 registry
	//
	// v0.1：空注册表。session.Manager 在 v0.1 走 connect.Connector
	// 不走 agent.Build；agent 在 v0.5 接入 multi-hop 时再注入 "direct" 策略。
	ag := agent.NewMemoryRegistry()
	logger.Info("agent registry ready", "schemes", ag.Schemes())

	// 4. 协议 connector registry
	//
	// sshclient.New 的签名是 func(connect.Deps) (*sshclient.Connector, error)，
	// 不直接满足 connect.Factory（返回 connect.Connector 而非 *sshclient.Connector），
	// 所以用 sshAdapter 适配。
	reg := connect.NewMemoryRegistry()
	if err := reg.Register("ssh", sshAdapter(sshclient.New)); err != nil {
		// "already registered" 算 ok（重复启动测试时常见）
		if !isAlreadyRegistered(err) {
			return fmt.Errorf("register ssh connector: %w", err)
		}
	}
	logger.Info("connector registry ready", "schemes", reg.Schemes())

	// 5. 会话 manager（带 connectors + secrets + known_hosts 注入）
	mm := session.NewMemoryManager().
		WithConnectors(reg).
		WithSecrets(sec).
		WithKnownHosts(kh)

	// 6. 装配 app
	core := app.New(app.Deps{
		Cfg:        cfg,
		Secret:     sec,
		Sessions:   mm,
		Agents:     ag,
		Connectors: reg,
		Emitter:    wailsEmitter{},
		Log:        logger,
	})

	// 7. 绑定层
	api := wailsbindings.New(core)

	// 8. Wails 启动
	errCh := make(chan error, 1)
	go func() {
		errCh <- wails.Run(&options.App{
			Title:  "MossTerm",
			Width:  1280,
			Height: 800,
			AssetServer: &assetserver.Options{
				Assets: assets,
			},
			BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
			OnStartup:        core.OnStartup,
			OnDomReady:       core.OnDomReady,
			OnShutdown:       core.OnShutdown,
			Bind: []interface{}{
				api,
			},
		})
	}()

	// 信号处理：Ctrl+C / SIGTERM → 触发优雅关闭。
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("received signal, shutting down", "signal", sig.String())
		return nil
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("wails.Run: %w", err)
		}
		return nil
	}
}

// sshAdapter 把 sshclient.New 的签名（返回 *sshclient.Connector）转成
// connect.Factory 要求的签名（返回 connect.Connector）。
//
// *sshclient.Connector 已经实现 connect.Connector 接口，所以只需包一层
// 函数类型即可；这个 wrapper 是 Go 类型系统强制的，无法绕过。
func sshAdapter(newFn func(connect.Deps) (*sshclient.Connector, error)) connect.Factory {
	return func(d connect.Deps) (connect.Connector, error) {
		return newFn(d)
	}
}

// isAlreadyRegistered 检查 error 是否为 "already registered" 错误。
//
// 用字符串匹配避免跨包 import 内部错误类型；该判断仅用于"已注册就跳过"语义。
func isAlreadyRegistered(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "already registered")
}

// -----------------------------------------------------------------------------
// Wails 事件总线适配
// -----------------------------------------------------------------------------

// wailsEmitter 实现 app.EventEmitter，包装 wails runtime.EventsEmit。
//
// 行为：把 ctx + event + data 透传给 wails runtime。
// 实际推送效果：前端通过 EventsOn(event, handler) 收到回调。
type wailsEmitter struct{}

// Emit 把事件推送到 Wails 事件总线。
func (wailsEmitter) Emit(ctx context.Context, event string, data ...interface{}) {
	wailsruntime.EventsEmit(ctx, event, data...)
}
