// client_provider.go 定义 tunnel 包如何拿到一个 session 对应的 *ssh.Client。
//
// 为什么用接口而不是直接 *session.Manager？
//   - tunnel 包与 session 包是正交的，tunnel 不应该反向依赖 session
//     （session 自己也不应该 import tunnel —— 避免循环）
//   - 测试希望自己注入 provider（in-process SSH server 已经起好，
//     直接拿 *ssh.Client 喂给 tunnel 即可）
//   - main.go 注入的 provider 是闭包：sessionID → 走 *App.sessions.Get
//     → sess.Connector() → type assert *sshclient.Connector → RawClient()
//
// 接口方法：Client(sessionID) 返回 *ssh.Client 和 ok。
// 错误/缺失时返回 nil + false；tunnel 内部把 false 当 Failed 处理。
//
// 并发：实现必须是并发安全的（多个 tunnel 同时调 Client 取 ssh client）。
package tunnel

import "golang.org/x/crypto/ssh"

// ClientProvider 把 sessionID 映射为底层 *ssh.Client。
//
// 典型实现（main.go 注入）：
//
//	provider := tunnel.ClientFunc(func(sessionID string) (*ssh.Client, bool) {
//	    sess, ok := app.Sessions().Get(session.ID(sessionID))
//	    if !ok { return nil, false }
//	    conn, ok := sess.Connector().(*sshclient.Connector)
//	    if !ok { return nil, false }
//	    c := conn.RawClient()
//	    return c, c != nil
//	})
//
// 返回 nil *ssh.Client 等价于"客户端还没就绪" —— 同样返回 false。
type ClientProvider interface {
	Client(sessionID string) (*ssh.Client, bool)
}

// ClientFunc 是 ClientProvider 的函数式适配器。
//
// 用法见 ClientProvider 注释。
type ClientFunc func(sessionID string) (*ssh.Client, bool)

// Client 实现 ClientProvider。
func (f ClientFunc) Client(sessionID string) (*ssh.Client, bool) {
	return f(sessionID)
}
