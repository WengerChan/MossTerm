// auth_ssh.go 提供从 connect.AuthMethod（sum-type）到
// []golang.org/x/crypto/ssh.AuthMethod 的公开转换入口。
//
// 背景：v0.1 起 sshclient.Connector.authMethods 是私有方法，
// 只被 Connector.Dial / OpenSession 内部使用。v0.6 agent 跳板链
// 接入后，agent 包的 Dialer 也要做同样的转换（为每跳构造 *ssh.ClientConfig），
// 共享同一份转换逻辑 + 共享对 secret.Store 的依赖。
//
// 把该函数放在 connect 包的原因：
//   - connect 是协议无关的契约层，AuthMethod 是它的核心 sum-type
//   - 让 sshclient 和 agent 都用同一份实现，避免 auth 路径分叉
//   - secrets 依赖已经在 connect/factory.go 里出现过（connect.Deps.Secrets），
//     引入 secret.Store 作为参数不增加新依赖
//
// 与 sshclient.authMethods 的关系：
//   - 本函数是公开版，签名稳定
//   - sshclient.Connector.authMethods 仍存在（作为私有方法），
//     转发到本函数 + 缓存 Signer，未来可统一收编
//
// 用法：
//
//	methods, err := connect.ToSSHAuthMethods(auth, secrets)
package connect

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/mossterm/mossterm/internal/secret"
)

// sshPublicKeyParser 把 PEM/DER 私钥字节解析成 ssh.Signer。
//
// v0.6 实现：与 sshclient.loadSignerFromBytes 行为一致 ——
// passphrase 为空按未加密处理，非空则解密。
//
// 这是函数变量，方便测试时替换为 mock（v0.6 agent 单测覆盖
// 多种 key format；后续可注入）。
var sshPublicKeyParser = func(keyBytes []byte, passphrase string) (ssh.Signer, error) {
	if len(keyBytes) == 0 {
		return nil, errors.New("sshPublicKeyParser: empty key bytes")
	}
	if passphrase == "" {
		return ssh.ParsePrivateKey(keyBytes)
	}
	return ssh.ParsePrivateKeyWithPassphrase(keyBytes, []byte(passphrase))
}

// sshAgentSignersFunc 从 $SSH_AUTH_SOCK 拉本地 agent 的 Signer 列表。
//
// 同上，作为函数变量方便测试替换。
var sshAgentSignersFunc = func() ([]ssh.Signer, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("connect.ToSSHAuthMethods: SSH_AUTH_SOCK not set")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("connect.ToSSHAuthMethods: dial agent socket %q: %w", sock, err)
	}
	defer conn.Close()
	ag := agent.NewClient(conn)
	return ag.Signers()
}

// sshKeyboardInteractiveStub 是 keyboard-interactive 的默认 challenge 处理。
//
// 跟 sshclient.keyboardInteractiveChallenge 行为一致：所有 question 留空，
// 让 server 回退到下一个 auth method。真实 UI prompt 走 v0.1 占位路径。
func sshKeyboardInteractiveStub(user, instruction string, questions []string, echos []bool) ([]string, error) {
	_ = user
	_ = instruction
	_ = echos
	answers := make([]string, len(questions))
	return answers, nil
}

// ToSSHAuthMethods 把 connect.AuthMethod 转换为 []ssh.AuthMethod。
//
// secrets 用于 publickey 路径：PublicKeyAuth.KeyID 非空时从 secret.Store
// 拉私钥 bytes 并解析为 ssh.Signer。secrets 为 nil 且需要 KeyID 拉私钥
// 时返回明确错误（与 sshclient 行为对齐）。
//
// 每种 AuthMethod 对应 1 个或多个 ssh.AuthMethod：
//   - PasswordAuth            → ssh.Password
//   - PublicKeyAuth           → ssh.PublicKeys（必要时补 KeyboardInteractive 兜底）
//   - AgentAuth               → ssh.PublicKeysCallback（连 $SSH_AUTH_SOCK）
//   - KeyboardInteractiveAuth → ssh.KeyboardInteractive
//
// 错误返回：AuthMethod 为 nil / 类型未知 / PublicKeyAuth 缺 Signer 且
// 缺 KeyID / KeyID 拉私钥失败 等。
func ToSSHAuthMethods(am AuthMethod, secrets secret.Store) ([]ssh.AuthMethod, error) {
	if am == nil {
		return nil, errors.New("connect.ToSSHAuthMethods: nil auth method")
	}
	switch a := am.(type) {
	case PasswordAuth:
		return []ssh.AuthMethod{ssh.Password(string(a))}, nil

	case PublicKeyAuth:
		signer := a.Signer
		if signer == nil {
			if a.KeyID == "" {
				return nil, errors.New("connect.ToSSHAuthMethods: PublicKeyAuth: both Signer and KeyID are empty")
			}
			if secrets == nil {
				return nil, errors.New("connect.ToSSHAuthMethods: PublicKeyAuth: secret.Store not initialized (need secrets to resolve KeyID)")
			}
			keyBytes, err := secrets.Get(secret.ID(a.KeyID))
			if err != nil {
				return nil, fmt.Errorf("connect.ToSSHAuthMethods: secrets.Get(%q): %w", a.KeyID, err)
			}
			if len(keyBytes) == 0 {
				return nil, fmt.Errorf("connect.ToSSHAuthMethods: empty key bytes for KeyID=%q", a.KeyID)
			}
			loaded, err := sshPublicKeyParser(keyBytes, a.Passphrase)
			if err != nil {
				return nil, fmt.Errorf("connect.ToSSHAuthMethods: parse key KeyID=%q: %w", a.KeyID, err)
			}
			signer = loaded
		}
		methods := []ssh.AuthMethod{ssh.PublicKeys(signer)}
		if a.Passphrase != "" {
			methods = append(methods, ssh.KeyboardInteractive(sshKeyboardInteractiveStub))
		}
		return methods, nil

	case AgentAuth:
		return []ssh.AuthMethod{
			ssh.PublicKeysCallback(sshAgentSignersFunc),
		}, nil

	case KeyboardInteractiveAuth:
		return []ssh.AuthMethod{
			ssh.KeyboardInteractive(sshKeyboardInteractiveStub),
		}, nil

	default:
		return nil, fmt.Errorf("connect.ToSSHAuthMethods: unknown auth method type %T", am)
	}
}
