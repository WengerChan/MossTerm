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
// 参数说明：
//   - kind: 业务层识别的 auth 方式字符串
//   - password: 明文密码（仅 password 时使用）
//   - keyID: 私钥在 secret.Store 中的 ID（仅 publickey 时使用）
//   - passphrase: 私钥 passphrase（仅 publickey 时使用，可选）
//
// v0.1.2 起 publickey 真实接通：
//   1. publickey 路径返回 PublicKeyAuth{KeyID, Passphrase}，Signer 留空
//   2. sshclient 收到这个 AuthMethod 后用 connector.secrets 拉私钥 bytes
//   3. 解析为 ssh.Signer 后用于 ssh.PublicKeys
//
// 该分工让 connect 包保持"协议无关 + 不依赖具体实现"，secret 拉取和
// PEM 解析在 sshclient 内部完成。
func AuthMethodFromSpec(kind AuthKind, password, keyID, passphrase string) (AuthMethod, error) {
	switch kind {
	case AuthKindPassword:
		if password == "" {
			return nil, errors.New("connect.AuthMethodFromSpec: empty password")
		}
		return PasswordAuth(password), nil

	case AuthKindPublicKey:
		if keyID == "" {
			return nil, errors.New("connect.AuthMethodFromSpec: publickey: empty keyID")
		}
		// 把 KeyID 透传给 sshclient，让它在拿到 connector.secrets 后拉私钥
		// 解析。如果 connector.secrets == nil，sshclient 会返回明确错误。
		return PublicKeyAuth{KeyID: keyID, Passphrase: passphrase}, nil

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
