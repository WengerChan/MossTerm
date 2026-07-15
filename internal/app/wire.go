// wire.go 是 app 包的"协议装配点"。
//
// 这里集中 import 所有 internal/ 下的具体实现包
// （sshclient / 未来的 telnetclient / sftpclient 等），
// 把它们的 Factory 注册到 connect.Registry。
//
// 这样 app 包的其他文件不直接依赖具体实现，
// 符合架构文档里 "APP → SESS → CONN → SSH" 的单向依赖方向。
package app

import (
	"fmt"
	"strings"

	"github.com/mossterm/mossterm/internal/agent"
	"github.com/mossterm/mossterm/internal/connect"
	"github.com/mossterm/mossterm/internal/sshclient"
)

// WireDefaultConnectors 把 MossTerm v0.1 支持的全部协议 Factory 注册到 r。
//
// 当前仅注册 "ssh"。未来在此处追加 telnet / serial / 等。
//
// 注册冲突（scheme 已被注册）会被忽略 —— 这允许调用方在 New 之前
// 预注册自定义 Factory 来覆盖默认实现。
func WireDefaultConnectors(r connect.Registry) error {
	if r == nil {
		return fmt.Errorf("app.WireDefaultConnectors: nil registry")
	}

	// sshclient.Factory
	if err := r.Register("ssh", sshClientFactory); err != nil {
		if !isAlreadyRegisteredErr(err) {
			return fmt.Errorf("app.WireDefaultConnectors: register ssh: %w", err)
		}
	}

	// 未来：r.Register("telnet", telnetFactory) 等
	return nil
}

// WireDefaultAgentStrategies 把 MossTerm v0.6 起的跳板策略注册到 r。
//
// 注册的策略：
//   - "direct"     —— 一跳直连
//   - "single-jump" —— Local ← Hop1 ← Target
//   - "multi-hop"  —— 任意跳数（v0.6 spec 显式列出）
//
// dialer 是 agent.Dialer（典型为 *app.SSHDialer），由调用方提供。
// dialer == nil 时所有三个策略都注册（direct 不强制要求 dialer 也
// 能注册，Build 时由 BuildOptions.Dialer 兜底；调用方应负责注入）。
//
// 注册冲突（策略名已被注册）会被忽略 —— 与 WireDefaultConnectors 语义对齐。
func WireDefaultAgentStrategies(r agent.Registry, dialer agent.Dialer) error {
	if r == nil {
		return fmt.Errorf("app.WireDefaultAgentStrategies: nil registry")
	}

	// "direct" 不强制要求 dialer（BuildFunc 内部校验 BuildOptions.Dialer）
	if err := r.Register(agent.StrategyDirect, agent.DirectBuildFunc); err != nil {
		if !isAlreadyRegisteredErr(err) {
			return fmt.Errorf("app.WireDefaultAgentStrategies: register %q: %w", agent.StrategyDirect, err)
		}
	}

	// "single-jump" / "multi-hop" 内部都要求 dialer，nil 时跳过注册
	// （允许业务方后续再 Register；不会因为缺 dialer 让整个 wire 失败）
	if dialer != nil {
		if err := r.Register(agent.StrategySingleJump, agent.SingleJumpBuildFunc); err != nil {
			if !isAlreadyRegisteredErr(err) {
				return fmt.Errorf("app.WireDefaultAgentStrategies: register %q: %w", agent.StrategySingleJump, err)
			}
		}
		if err := r.Register(agent.StrategyMultiHop, agent.MultiHopBuildFunc); err != nil {
			if !isAlreadyRegisteredErr(err) {
				return fmt.Errorf("app.WireDefaultAgentStrategies: register %q: %w", agent.StrategyMultiHop, err)
			}
		}
	}

	return nil
}

// sshClientFactory 是 sshclient.Connector 的 connect.Factory 适配器。
//
// 它把 connect.Deps 透传给 sshclient.New，让 sshclient 决定如何处理
// HostKeyCb / BannerCb / DialTimeout / KeepAlive 等。
func sshClientFactory(d connect.Deps) (connect.Connector, error) {
	return sshclient.New(d)
}

// isAlreadyRegisteredErr 检查 error 是否为 connect.MemoryRegistry 的
// "scheme already registered" 错误。
//
// 我们故意不强转 error 类型（避免 import 内部细节），而是字符串匹配；
// 该判断仅用于"已注册就跳过"语义，不会影响真正的失败路径。
func isAlreadyRegisteredErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "already registered")
}
