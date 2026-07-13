// keepalive.go 实现 SSH keepalive 协程。
//
// 目的：
//  1. 防止中间设备（NAT / 防火墙 / 负载均衡）单方面断开长时间 idle 的 TCP 连接
//  2. 早期发现对端已崩溃（network partition），让 session 尽快感知到
//
// 协议细节：
//   - 使用 OpenSSH 标准的 "keepalive@openssh.com" 全局请求
//   - 走 ssh.Client.SendRequest（wantReply=true）而不是 TCP-level 探活
//   - 因为 SSH 协议层是双层的：TCP 关了 SSH 自然挂，但 TCP 还在但 SSH server
//     已死的话，TCP keepalive 探不出来 —— 必须走 SSH 协议层
//
// 退出路径（任意一个）：
//   1. 正常：Connector.Close() → close(c.done) → select 命中 done 分支 return
//   2. 异常：下次 SendRequest 因 conn 已关而失败 → 记录 warn + return
//
// v0.1.4 范围（重要）：
//   协程不主动关闭 *ssh.Client —— "关 client" 会同时挂掉所有走该 client
//   的 session / channel，影响面超出 keepalive 自身职责。session 关闭走
//   正常的 sess.Close() → conn.Close() 链路；下次 SendRequest 失败时本
//   协程自然通过路径 2 退出。
//
// v0.22.0 API 兼容性：
//   x/crypto v0.22.0 的 *ssh.Client 没有自己的 SendRequest 方法，通过嵌入
//   的 Conn interface 暴露：
//     SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
//   没有 context 入参（context 形式是 v0.33+ 引入的）。
//   因此本文件的超时通过 goroutine + select 模式实现，不是 ctx.WithTimeout。
package sshclient

import (
	"log/slog"
	"time"

	"golang.org/x/crypto/ssh"
)

// keepaliveRequest 是 OpenSSH 标准的 keepalive 全局请求名。
//
// RFC 4254 没有规定 keepalive 协议名，所以各家实现各自为政；OpenSSH
// 选 "keepalive@openssh.com"，Dropbear / libssh 等也兼容这个 name。
const keepaliveRequest = "keepalive@openssh.com"

// keepaliveTimeout 是单次 SendRequest 的硬超时。
//
// 30s 的心跳间隔下，3s 超时足够覆盖单次 RTT + server 响应 + 本地调度
// 延迟，不会因为网络抖动误判为"对端失联"。
const keepaliveTimeout = 3 * time.Second

// runKeepAlive 启动一个长循环，每 interval 发一次 SSH keepalive 全局请求。
//
// 该方法设计为在 Dial 成功后由 Connector 启动为独立 goroutine：
//
//	go c.runKeepAlive(client, c.keepAlive)
//
// 退出条件（任意一个）：
//   - 收到 c.done 信号（Connector 整体关闭）→ 立即 return
//   - SendRequest 出错（conn 已关 / server 无响应）→ slog.Warn + return
//   - SendRequest 超时（v0.22.0 没有 ctx 入参，用 goroutine + select 实现）→ return
//   - ticker 自然 tick → 发 keepalive，继续循环
//
// 失败处理：仅 slog.Warn + return，不主动关闭 client。理由见文件头注释。
func (c *Connector) runKeepAlive(client *ssh.Client, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Debug("ssh keepalive goroutine started",
		"interval", interval,
		"timeout", keepaliveTimeout,
	)
	defer slog.Debug("ssh keepalive goroutine exited")

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			// v0.22.0 的 *ssh.Client.SendRequest 不接受 context，要
			// 强制超时只能在 goroutine 里跑 + select 监听超时 channel。
			// 副作用：超时发生时该 goroutine 会"泄漏"到 SendRequest 完
			// 成（最坏 keepaliveTimeout + conn 实际响应时间），不影响
			// 整体正确性。
			type result struct {
				ok  bool
				err error
			}
			resCh := make(chan result, 1)
			go func() {
				ok, _, err := client.SendRequest(keepaliveRequest, true, nil)
				resCh <- result{ok: ok, err: err}
			}()

			timer := time.NewTimer(keepaliveTimeout)
			select {
			case <-c.done:
				timer.Stop()
				return
			case res := <-resCh:
				timer.Stop()
				if res.err != nil {
					// 连接已关 / server 失联 —— 退出协程，session 通过
					// 自身 Read 返回 io.EOF 感知对端失联。
					slog.Warn("ssh keepalive failed; goroutine exiting",
						"err", res.err,
						"interval", interval,
					)
					return
				}
			case <-timer.C:
				// 超时 —— server 无响应。和失败一样退出协程；
				// session 那边会在 Read 上感知到对端失联。
				slog.Warn("ssh keepalive timed out; goroutine exiting",
					"timeout", keepaliveTimeout,
					"interval", interval,
				)
				return
			}
		}
	}
}
