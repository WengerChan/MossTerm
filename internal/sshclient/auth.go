// auth.go 把 connect.AuthMethod（sum-type）转换为 ssh.AuthMethod 列表。
//
// 每种 connect.AuthMethod 对应一个 / 多个 ssh.AuthMethod 实现：
//   - PasswordAuth            → ssh.Password
//   - PublicKeyAuth           → ssh.PublicKeys (使用外部注入的 Signer，或从 secret.Store 拉 KeyID)
//   - AgentAuth               → ssh.PublicKeysCallback (走本地 ssh-agent)
//   - KeyboardInteractiveAuth → ssh.KeyboardInteractive (v0.1 仅占位)
package sshclient

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/mossterm/mossterm/internal/connect"
)

// authMethods 把 connect.AuthMethod 转换为 ssh 库所需的 []ssh.AuthMethod。
//
// 作为 Connector 的方法，publickey 路径可以在需要时通过 c.loadSigner
// 从 secret.Store 拉私钥解析 signer。
//
// 该方法不持有网络/IO 状态：每次调用都会重新解析（agent 情况除外 ——
// agent 每次回调都会重新连接 $SSH_AUTH_SOCK）。signerCache 提供 per-KeyID
// 缓存，避免重复解析。
func (c *Connector) authMethods(am connect.AuthMethod) ([]ssh.AuthMethod, error) {
	if am == nil {
		return nil, errors.New("sshclient.authMethods: nil auth method")
	}
	switch a := am.(type) {
	case connect.PasswordAuth:
		return []ssh.AuthMethod{ssh.Password(string(a))}, nil

	case connect.PublicKeyAuth:
		// 两种使用方式：
		//   1. Signer != nil —— 调用方已解析好，直接用
		//   2. KeyID != "" —— 从 secret.Store 拉私钥后解析
		signer := a.Signer
		if signer == nil {
			if a.KeyID == "" {
				return nil, errors.New("sshclient.authMethods: PublicKeyAuth: both Signer and KeyID are empty")
			}
			loaded, err := c.loadSigner(a.KeyID, a.Passphrase)
			if err != nil {
				return nil, fmt.Errorf("sshclient.authMethods: load signer for keyID=%q: %w", a.KeyID, err)
			}
			signer = loaded
		}
		methods := []ssh.AuthMethod{ssh.PublicKeys(signer)}
		// 如果带 passphrase，可补充 keyboard-interactive 作为兜底
		// (某些服务器在公钥失败时会回退询问口令)
		if a.Passphrase != "" {
			methods = append(methods, ssh.KeyboardInteractive(keyboardInteractiveChallenge))
		}
		return methods, nil

	case connect.AgentAuth:
		return []ssh.AuthMethod{
			ssh.PublicKeysCallback(agentSignersCallback),
		}, nil

	case connect.KeyboardInteractiveAuth:
		return []ssh.AuthMethod{
			ssh.KeyboardInteractive(keyboardInteractiveChallenge),
		}, nil

	default:
		return nil, fmt.Errorf("sshclient.authMethods: unknown auth method type %T", am)
	}
}

// agentSignersCallback 是 ssh.PublicKeysCallback 的实现。
//
// v0.1 简化：每次 SSH 协议层请求 Signer 时，都重新连接 $SSH_AUTH_SOCK。
// 这样可以正确处理 agent 端新增 / 删除 key 的情况，但每次连接握手
// 会多一次 unix socket 往返。生产环境可改为长连接 + 缓存。
func agentSignersCallback() ([]ssh.Signer, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("sshclient: SSH_AUTH_SOCK not set")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("sshclient: dial agent socket %q: %w", sock, err)
	}
	defer conn.Close()

	ag := agent.NewClient(conn)
	return ag.Signers()
}

// keyboardInteractiveChallenge 是 ssh.KeyboardInteractive 的默认 challenge 处理。
//
// v0.1 桩实现：
//   - 把所有 question 的 answer 留空
//   - 多数服务器在收到空 answer 后会回退到下一个 auth method（通常是 password）
//
// 真实实现需要在 user / instruction / questions 之间建立映射并通过 UI
// 提示用户输入；v0.1 暂未提供 UI 通道。
func keyboardInteractiveChallenge(user, instruction string, questions []string, echos []bool) ([]string, error) {
	// TODO(secret): 接通 secret.Store 拉取密码 / 私钥 passphrase
	// TODO(ui): 接通 Wails 事件总线把 prompt 推给前端
	_ = user
	_ = instruction
	_ = echos
	answers := make([]string, len(questions))
	return answers, nil
}
