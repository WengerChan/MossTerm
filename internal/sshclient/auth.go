// auth.go 把 connect.AuthMethod（sum-type）转换为 ssh.AuthMethod 列表。
//
// v0.6 起转换逻辑下移到 connect.ToSSHAuthMethods，本文件变成薄包装：
//   - c.loadSigner 解析 publickey 私钥并缓存（per-KeyID LRU）
//   - c.authMethods 调 connect.ToSSHAuthMethods + 注入缓存好的 Signer
//
// 缓存目的：避免每次 Dial 都重新打开 secret.Store + 解析 PEM。
// 注：loadSigner 仍保留 sshclient 私有（依赖 c.signerCache LRU）；
// agent 包的 Dialer 走自己的路径（不享 LRU；按需调 secret.Store 即可）。
package sshclient

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"

	"github.com/mossterm/mossterm/internal/connect"
)

// authMethods 把 connect.AuthMethod 转换为 ssh 库所需的 []ssh.AuthMethod。
//
// 流程：
//  1. 如果是 PublicKeyAuth{KeyID, ...}：先 c.loadSigner 走 LRU，
//     命中直接用；miss 时从 secret.Store 拉私钥 bytes → 解析 → 写 LRU
//  2. 把解析好的 Signer 注入 PublicKeyAuth{Signer}，交给 connect.ToSSHAuthMethods
//  3. 返回 []ssh.AuthMethod
//
// 为什么不让 connect.ToSSHAuthMethods 直接拿 secrets？
//   - sshclient 有 per-KeyID LRU 缓存，命中时省一次 secret.Store IO + PEM 解析
//   - agent 包的 Dialer 不需要这个缓存（hop 数量少 + 一次性）
//   - 两套入口并行：sshclient 走 LRU，agent 走 secret.Store 直拉
func (c *Connector) authMethods(am connect.AuthMethod) ([]ssh.AuthMethod, error) {
	if am == nil {
		return nil, errors.New("sshclient.authMethods: nil auth method")
	}
	// publickey 走 LRU 预热 signer，再交给 connect.ToSSHAuthMethods
	if pka, ok := am.(connect.PublicKeyAuth); ok && pka.Signer == nil && pka.KeyID != "" {
		loaded, err := c.loadSigner(pka.KeyID, pka.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("sshclient.authMethods: load signer for keyID=%q: %w", pka.KeyID, err)
		}
		am = connect.PublicKeyAuth{
			Signer:     loaded,
			KeyID:      pka.KeyID,
			Passphrase: pka.Passphrase,
		}
	}
	return connect.ToSSHAuthMethods(am, c.secrets)
}

// keyboardInteractiveChallenge 是 ssh.KeyboardInteractive 的默认 challenge 处理。
//
// v0.1 桩实现：
//   - 把所有 question 的 answer 留空
//   - 多数服务器在收到空 answer 后会回退到下一个 auth method（通常是 password）
//
// 真实实现需要在 user / instruction / questions 之间建立映射并通过 UI
// 提示用户输入；v0.1 暂未提供 UI 通道。
//
// 仍然保留本函数以维持旧调用方（v0.1-v0.5 期间的 ssh.KeyboardInteractive
// 引用），但 v0.6 起的 connect.ToSSHAuthMethods 已经改用包内私有的
// sshKeyboardInteractiveStub；本函数已不在生产路径上。
// 保留它便于未来扩展（比如接 Wails prompt 通道时在这里改）。
func keyboardInteractiveChallenge(user, instruction string, questions []string, echos []bool) ([]string, error) {
	_ = user
	_ = instruction
	_ = echos
	answers := make([]string, len(questions))
	return answers, nil
}
