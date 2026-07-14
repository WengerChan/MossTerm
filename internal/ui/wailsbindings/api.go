// Package wailsbindings 是"哪些方法暴露给前端"的白名单层。
//
// 该层组合 *app.App 并转发方法，避免内部重构破坏前端契约。
// 任何对前端可见的 API 变化必须先改本文件再改前端。
//
// Wails 反射规则（影响本文件所有公开方法签名）：
//   - 必须导出（首字母大写）
//   - context.Context 是合法参数类型（Wails 特殊处理）且必须是第一个参数
//   - 参数 / 返回值必须是可导出类型或基本类型
//   - []byte 在事件 payload 中会被转成前端 Uint8Array
package wailsbindings

import (
	"context"
	"errors"
	"fmt"

	"github.com/mossterm/mossterm/internal/app"
	"github.com/mossterm/mossterm/internal/config"
	"github.com/mossterm/mossterm/internal/secret"
	"github.com/mossterm/mossterm/internal/session"
)

// App 是 Wails 绑定入口。
//
// 由 wails.Run 通过 Bind(ctx, api) 注入到 webview 运行时；
// 前端通过 window.go.app.App.<Method>() 调用。
type App struct {
	core *app.App
}

// New 用内部 *app.App 构造一个绑定层。
func New(core *app.App) *App {
	return &App{core: core}
}

// -----------------------------------------------------------------------------
// Session 相关
// -----------------------------------------------------------------------------

// ListSessions 返回全部活跃 Session 的 Info。
func (a *App) ListSessions(ctx context.Context) []session.Info {
	return a.core.Sessions().List()
}

// OpenSession 打开一个新会话。
//
// 返回 session ID（UUID v4 字符串）；错误以 Go error 形式返回，
// Wails 会序列化成 { error: "..." } 给前端。
func (a *App) OpenSession(ctx context.Context, req session.OpenRequest) (string, error) {
	s, err := a.core.Sessions().Open(ctx, req)
	if err != nil {
		return "", fmt.Errorf("wailsbindings.OpenSession: %w", err)
	}
	return string(s.Info().ID), nil
}

// CloseSession 关闭一个会话。
func (a *App) CloseSession(ctx context.Context, id string, force bool) error {
	if err := a.core.Sessions().Close(session.ID(id), force); err != nil {
		return fmt.Errorf("wailsbindings.CloseSession: %w", err)
	}
	return nil
}

// SendInput 把键盘输入发送到指定 session。
func (a *App) SendInput(ctx context.Context, id string, data []byte) error {
	s, ok := a.core.Sessions().Get(session.ID(id))
	if !ok {
		return fmt.Errorf("wailsbindings.SendInput: session %q not found", id)
	}
	if err := s.Input(data); err != nil {
		return fmt.Errorf("wailsbindings.SendInput: %w", err)
	}
	return nil
}

// ResizePTY 通知指定 session 调整 PTY 大小。
func (a *App) ResizePTY(ctx context.Context, id string, cols, rows int) error {
	s, ok := a.core.Sessions().Get(session.ID(id))
	if !ok {
		return fmt.Errorf("wailsbindings.ResizePTY: session %q not found", id)
	}
	if err := s.Resize(cols, rows); err != nil {
		return fmt.Errorf("wailsbindings.ResizePTY: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Profile CRUD
// -----------------------------------------------------------------------------

// ListProfiles 返回全部 Profile。
func (a *App) ListProfiles(ctx context.Context) []config.Profile {
	return a.core.Cfg().ListProfiles()
}

// SaveProfile 保存一个 Profile（新增或更新）。
//
// p.ID 为空时走 AddProfile，非空走 UpdateProfile。
// 这让前端可以无差别调用同一个入口。
func (a *App) SaveProfile(ctx context.Context, p config.Profile) error {
	cfg := a.core.Cfg()
	if p.ID == "" {
		return fmt.Errorf("wailsbindings.SaveProfile: empty profile ID")
	}
	_, exists := cfg.GetProfile(p.ID)
	if exists {
		if err := cfg.UpdateProfile(p); err != nil {
			return fmt.Errorf("wailsbindings.SaveProfile: update: %w", err)
		}
		return nil
	}
	if err := cfg.AddProfile(p); err != nil {
		return fmt.Errorf("wailsbindings.SaveProfile: add: %w", err)
	}
	return nil
}

// DeleteProfile 按 ID 删除一个 Profile。
func (a *App) DeleteProfile(ctx context.Context, id string) error {
	if err := a.core.Cfg().DeleteProfile(id); err != nil {
		return fmt.Errorf("wailsbindings.DeleteProfile: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Secret 元数据（不含内容）
// -----------------------------------------------------------------------------

// ListSecretsItems 列出全部凭据条目（仅元数据）。
func (a *App) ListSecretsItems(ctx context.Context) []secret.Item {
	items, err := a.core.Secret().List()
	if err != nil {
		// 列表失败返回空 slice + 记 log；不冒泡 error 避免前端阻塞
		a.core.Log().Warn("wailsbindings.ListSecretsItems failed", "err", err)
		return []secret.Item{}
	}
	return items
}

// SaveSecret 把凭据写入安全存储。
//
// name / kind / content 来自前端表单；ID 由 secret.Store 生成。
// 真正的写入走 secret.Store.Set；本层只做编排 + 错误包装。
func (a *App) SaveSecret(ctx context.Context, name, kind, content string) (string, error) {
	id, err := a.core.Secret().Set(name, secret.Kind(kind), []byte(content), nil)
	if err != nil {
		return "", fmt.Errorf("wailsbindings.SaveSecret: %w", err)
	}
	return string(id), nil
}

// GetSecretContent 取出凭据内容。
//
// 前端调用栈应：用户已输入主密码 → GetSecretContent → 用完清零引用。
// 不得把返回值存入 Zustand（避免 DevTools 泄露）。
func (a *App) GetSecretContent(ctx context.Context, id string) (string, error) {
	data, err := a.core.Secret().Get(secret.ID(id))
	if err != nil {
		return "", fmt.Errorf("wailsbindings.GetSecretContent: %w", err)
	}
	return string(data), nil
}

// -----------------------------------------------------------------------------
// Known Hosts（v0.5.0 First-Use Trust）
// -----------------------------------------------------------------------------

// TrustHost 把前端的"信任决策"回传给 known_hosts.Manager。
//
// 调用栈（v0.5.0）：
//  1. 用户在 modal 点 trust/reject
//  2. 前端 TrustRequestModal 调 App.TrustHost(requestID, action)
//  3. 本方法调 known_hosts.Manager.ReplyTrust(requestID, action)
//  4. Manager 把 reply 写入内部 trustReplyCh，唤醒挂起的 HostKeyCallback
//  5. SSH 握手继续 / 中断
//
// 参数约定：
//   - requestID：必须与 TrustRequestModal 收到的事件 ID 完全一致
//     （Manager 校验 ID 不匹配会返回 ID mismatch 错误并把当前 reply 丢弃）。
//   - action："trust" | "reject" | 其他（视作 reject）。
//
// 错误：known_hosts 未初始化时返回 "known_hosts not initialized"。
// 该方法本身不返回 reply 同步结果——前端调完即关闭 modal，
// reply 是否被采纳由 Manager 内部异步处理。
func (a *App) TrustHost(ctx context.Context, requestID string, action string) error {
	kh := a.core.KnownHosts()
	if kh == nil {
		return errors.New("wailsbindings.TrustHost: known_hosts not initialized")
	}
	if err := kh.ReplyTrust(requestID, action); err != nil {
		return fmt.Errorf("wailsbindings.TrustHost: %w", err)
	}
	return nil
}
