// Package connect 定义协议无关的连接抽象。
//
// MossTerm 通过 Connector 接口支持多种协议（SSH / Telnet / Serial 等）。
// 本包只提供契约与共享类型，具体实现由 internal/sshclient 等子包提供，
// 并通过 Registry 注册到全局注册中心。
package connect

import (
	"context"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// Connector 是任意协议适配器必须实现的契约。
//
// 生命周期：
//  1. session.Manager 调用 Dial 建立到目标主机的协议层连接；
//  2. 在 conn 之上调用 OpenSession 获取一个交互通道；
//  3. pty 包以这个通道作为 slave 端，叠加 PTY 语义；
//  4. 使用方调用 Close 释放资源。
//
// 实现必须是并发安全的：Connector 本身可以被多次调用 Dial / OpenSession。
type Connector interface {
	// Dial 建立到目标主机的协议层连接。
	//
	// host 可以是域名 / IPv4 / IPv6 literal。port=0 时使用协议默认端口。
	// 返回的 net.Conn 语义上是"已协商好协议的连接"，
	// 可被 sftp / tunnel 等上层直接复用。
	// ctx.Done() 返回时必须立刻返回 ctx.Err()。
	Dial(ctx context.Context, params DialParams) (net.Conn, error)

	// OpenSession 在已 Dial 的连接上开启一个交互会话。
	//
	// 返回的 Session 必须实现 io.ReadWriteCloser + Resize + ShellPID。
	// 远端进程退出时 Read 返回 io.EOF。
	OpenSession(ctx context.Context, conn net.Conn, opts SessionOpts) (Session, error)
}

// Session 是协议层会话（区别于 MossTerm 业务层 session.Session）。
//
// 它是一个交互式通道：可以读远端输出、写本地输入、调整窗口大小。
// 协议层无需关心 PTY 设备如何打开（由 internal/pty 负责）。
type Session interface {
	io.ReadWriteCloser
	// Resize 通知远端终端窗口尺寸变化（pty 协议层）。
	Resize(cols, rows int) error
	// ShellPID 返回远端 shell 进程 PID（如果协议支持；不支持则返回 0）。
	ShellPID() int
}

// DialParams 描述一次拨号所需的全部参数。
type DialParams struct {
	Host      string            `json:"host"`
	Port      int               `json:"port"`
	User      string            `json:"user"`
	Auth      AuthMethod        `json:"auth"`
	Timeout   time.Duration     `json:"timeout"`
	KeepAlive time.Duration     `json:"keepAlive"`
	Extra     map[string]string `json:"extra,omitempty"`
}

// SessionOpts 描述一个交互会话的初始参数。
type SessionOpts struct {
	Term string            `json:"term"`
	Cols int               `json:"cols"`
	Rows int               `json:"rows"`
	Env  map[string]string `json:"env,omitempty"`
}

// AuthMethod 是 sum-type（sealed）。
//
// 任何实现了私有方法 authMethod() 的类型都可以作为合法 AuthMethod。
// 这种"私有方法"模式用于在包外阻止构造新变体。
type AuthMethod interface{ authMethod() }

// PasswordAuth 表示明文密码登录。
//
// 注意：MossTerm 默认禁用密码登录（仅 SSH 协议）。
// 此类型仍保留以满足 SSH 协议规范。
type PasswordAuth string

// PublicKeyAuth 表示公私钥登录。
//
// 三种使用方式：
//  1. Signer != nil：调用方已解析好 ssh.Signer（最直接，sshclient 不会再去拉私钥）
//  2. KeyID != ""：sshclient 从 secret.Store 拉私钥 bytes 并解析
//  3. Signer == nil && KeyID == ""：构造不完整，sshclient 报错
//
// Passphrase 为空表示私钥未加密；非空则用其解密。
type PublicKeyAuth struct {
	Signer     ssh.Signer
	KeyID      string
	Passphrase string
}

// AgentAuth 表示使用本地 SSH agent 提供的签名器。
type AgentAuth struct{}

// KeyboardInteractiveAuth 表示 keyboard-interactive 登录。
type KeyboardInteractiveAuth struct{}

// 编译期断言：以下类型实现了 AuthMethod。
func (PasswordAuth) authMethod()            {}
func (PublicKeyAuth) authMethod()           {}
func (AgentAuth) authMethod()               {}
func (KeyboardInteractiveAuth) authMethod() {}
