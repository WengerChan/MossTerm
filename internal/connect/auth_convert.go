// auth_convert.go 提供从业务层字段（kind/password/keyID/passphrase）
// 到 connect.AuthMethod 的转换入口。
//
// 之所以做成"接收原始字段"的 free function 而非 `func (AuthSpec) ToAuthMethod()`，
// 是为了避免 `connect` 与 `session` 互相 import 形成循环依赖。
// session 包的 AuthSpec 提供同名方法作为薄包装。

package connect

import (
	"errors"
	"fmt"
)

// AuthKind 是业务层识别 auth 方式的字符串。
//
// 与 session.AuthSpec.Kind 取值保持一致。
type AuthKind = string

const (
	// AuthKindPassword 明文密码登录。
	AuthKindPassword AuthKind = "password"
	// AuthKindPublicKey 公私钥登录（私钥来自 secret.Store）。
	AuthKindPublicKey AuthKind = "publickey"
	// AuthKindAgent 使用本地 ssh-agent。
	AuthKindAgent AuthKind = "agent"
	// AuthKindKeyboardInteractive keyboard-interactive 登录。
	AuthKindKeyboardInteractive AuthKind = "keyboard-interactive"
)

// AuthMethodFromSpec 把业务层 auth 字段转成 connect.AuthMethod。
//
// kind 必须是 AuthKind* 之一；其他取值返回 error。
//
// 重要：v0.1 仅支持 password；其他类型返回 ErrAuthNotImplemented。
// 这与"默认禁用密码"的 settings 看似矛盾 —— 但 sshclient 在 New
// 阶段会再校验 settings.AllowPassword 并拒绝非密码尝试；本函数只做
// 形态转换，权限校验在更上层完成。
//
// 参数说明：
//   - kind: 业务层识别的 auth 方式字符串
//   - password: 明文密码（仅 password 时使用）
//   - keyID: 私钥在 secret.Store 中的 ID（仅 publickey 时使用，v0.1 暂未接通）
//   - passphrase: 私钥 passphrase（仅 publickey 时使用，可选）
func AuthMethodFromSpec(kind AuthKind, password, keyID, passphrase string) (AuthMethod, error) {
	switch kind {
	case AuthKindPassword:
		if password == "" {
			return nil, errors.New("connect.AuthMethodFromSpec: empty password")
		}
		return PasswordAuth(password), nil

	case AuthKindPublicKey:
		// v0.1 不接 secret.Store —— 让 caller 决定怎么处理。
		// 真实实现路径：
		//   1. secret.Store.Get(secret.ID(keyID)) -> bytes
		//   2. sshclient.loadSignerFromBytes(bytes, passphrase) -> ssh.Signer
		//   3. return PublicKeyAuth{Signer: signer, Passphrase: passphrase}, nil
		if keyID == "" {
			return nil, errors.New("connect.AuthMethodFromSpec: publickey: empty keyID")
		}
		// v0.1 直接返回一个"未就绪"的错误，避免调用方在没接通 secret
		// 之前误以为能登录。
		return nil, fmt.Errorf("connect.AuthMethodFromSpec: publickey via secret.Store not yet wired (keyID=%q)", keyID)

	case AuthKindAgent:
		return AgentAuth{}, nil

	case AuthKindKeyboardInteractive:
		return KeyboardInteractiveAuth{}, nil

	case "":
		return nil, errors.New("connect.AuthMethodFromSpec: empty kind")

	default:
		return nil, fmt.Errorf("connect.AuthMethodFromSpec: unknown auth kind %q", kind)
	}
}
