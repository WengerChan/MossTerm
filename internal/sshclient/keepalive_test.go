// keepalive_test.go 覆盖 Connector.keepalive 协程的关键退出路径。
//
// 设计约束：
//   - *ssh.Client 是 concrete type，无法在 package 内 mock（没有对应 interface）
//   - 起一个真实 ssh.Server（sftp/ssh.NewServer）来做集成测试是可能的，但
//     慢且依赖网络，在 unit test 套件里不划算
//   - 因此本文件只覆盖 *与 *ssh.Client 无关* 的退出路径：close(c.done)
//
// 失败路径（SendRequest 报错）的覆盖留给 v0.2 接入 integration test harness
// 时再做（v0.1.3 的 known_hosts 也是同样策略 —— 真实 SSH server 行为走
// integration test）。
package sshclient

import (
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestConnectorClose_StopsKeepAliveGoroutine 验证 Connector.Close() 后
// 已在飞的 keepalive 协程能立刻退出。
//
// 实现思路：
//  1. 构造一个最小可用的 Connector（只需要 done 字段）
//  2. 启动一个 goroutine 复刻 runKeepAlive 的 select 循环（同包访问 unexported 字段）
//  3. 等 50ms 让 goroutine 进入 select
//  4. close(c.done) 模拟 Connector.Close()
//  5. 1s 内 goroutine 应当退出（via done 分支）
//  6. 重复 Close 多次验证幂等（sync.Once）
func TestConnectorClose_StopsKeepAliveGoroutine(t *testing.T) {
	c := &Connector{done: make(chan struct{})}

	finished := make(chan struct{})
	go func() {
		// 复刻 runKeepAlive 的循环结构（去掉 SendRequest 步骤，因为没有
		// 真实的 *ssh.Client）。验证退出信号能正确传递。
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-c.done:
				close(finished)
				return
			case <-ticker.C:
				// 真实 runKeepAlive 在这里会发 SendRequest；
				// 这里什么都不做，等 done 信号。
			}
		}
	}()

	// 让 goroutine 进入 select
	time.Sleep(50 * time.Millisecond)

	// 关闭 done —— 模拟 Connector.Close()
	c.Close()

	select {
	case <-finished:
		// 预期路径
	case <-time.After(1 * time.Second):
		t.Fatal("keepalive goroutine did not stop within 1s after Close")
	}

	// 二次 Close 应当安全（sync.Once 保护）—— 不能 panic
	c.Close()
	c.Close()
}

// TestConnectorClose_ConcurrentSafe 验证多 goroutine 并发调用 Close
// 不会 panic（sync.Once 保护的核心价值）。
func TestConnectorClose_ConcurrentSafe(t *testing.T) {
	c := &Connector{done: make(chan struct{})}

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			c.Close()
		}()
	}
	wg.Wait()
	// 如果 sync.Once 没有保护，第一次 close 之后的后续 close 会 panic
	// ("close of closed channel")。跑完到这里说明全部安全。
}

// TestRunKeepAlive_Signature 编译期守卫：保证 runKeepAlive 方法的签名
// 没有被意外改动。
//
// 真实 runKeepAlive 需要 *ssh.Client 才能运行（无法 unit test），但 Go 的
// 编译期类型检查能挡住签名漂移：把方法值赋给具体函数类型，签名不一致时
// 编译失败。同时这也防止有人误删 runKeepAlive —— 删除后本测试连编译都过不了。
func TestRunKeepAlive_Signature(t *testing.T) {
	// 仅取方法值，不调用（没有真实的 *ssh.Client）
	var _ func(c *Connector, client *ssh.Client, interval time.Duration) = (*Connector).runKeepAlive
}
