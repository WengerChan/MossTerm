// local.go 实现 -L（Local forward）：本地端口 → 远端目标。
//
// 流程：
//  1. net.Listen("tcp", BindHost:BindPort) 拿本地 listener
//  2. 每个 conn 拿 session 的 *ssh.Client → client.Dial("tcp", TargetHost:TargetPort)
//     （x/crypto v0.31：内部走 direct-tcpip channel；返回的 net.Conn 是
//     穿透 SSH 连接到远端 TCP 的连接）
//  3. io.Copy 双向 pipe
//  4. 任意一端关闭 → 关闭另一端 → conn 计 -1
//
// 错误路径：
//   - net.Listen 失败 → Start 返回 error，State → Failed
//   - client.Dial 失败 → 仅丢弃这条 conn（不影响 listener；记 Stats.Bytes）
//   - ctx 取消 → Stop 优雅关 listener
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// localTunnel 是 Local mode 的实现。
type localTunnel struct {
	spec     Spec
	provider ClientProvider

	listener net.Listener
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// stats 由 atomic 操作（无锁读多写少）
	bytesIn    atomic.Int64
	bytesOut   atomic.Int64
	activeConn atomic.Int64
	startedAt  atomic.Int64

	// state 是 baseTunnel 内部用，caller 通过 State() 读
	// 留指针避免 copy
	base *baseTunnel
}

// newLocalTunnel 构造一个 local 模式的 tunnel。**不**启动。
func newLocalTunnel(spec Spec, p ClientProvider) *localTunnel {
	return &localTunnel{
		spec:     spec,
		provider: p,
		stopCh:   make(chan struct{}),
		base:     newBaseTunnel(spec),
	}
}

// Start 启动 listener。
//
// 错误：BindHost:BindPort 被占用 / 无权限 → 返回 error，State → Failed。
// 成功：listener 就绪 + 1 个 acceptLoop goroutine 在跑；State → Active。
func (t *localTunnel) Start(ctx context.Context) error {
	if t.provider == nil {
		t.base.setState(TunnelStateFailed)
		return errors.New("tunnel.local: nil ClientProvider (call MemoryManager.WithClientProvider first)")
	}

	addr := net.JoinHostPort(t.spec.BindHost, fmt.Sprintf("%d", t.spec.BindPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.base.setState(TunnelStateFailed)
		return fmt.Errorf("tunnel.local.Start: listen %s: %w", addr, err)
	}
	t.listener = ln
	t.startedAt.Store(time.Now().Unix())
	t.base.setState(TunnelStateActive)

	t.wg.Add(1)
	go t.acceptLoop(ctx)

	return nil
}

// acceptLoop 不断接受 conn，每个 conn 跑一对 io.Copy。
func (t *localTunnel) acceptLoop(ctx context.Context) {
	defer t.wg.Done()

	for {
		conn, err := t.listener.Accept()
		if err != nil {
			// listener 关闭（Stop 触发）→ 正常退出
			select {
			case <-t.stopCh:
				return
			default:
				// 其他原因（罕见：fd 被外部关）→ 退出
				return
			}
		}

		// 拿 *ssh.Client
		sshClient, ok := t.provider.Client(t.spec.SessionID)
		if !ok || sshClient == nil {
			_ = conn.Close()
			// provider 暂时没有 client：稍后重试（业务层应保证 session Established 时再 Open）
			// 当前实现：丢弃这条 conn；State 仍 Active
			continue
		}

		// 后台处理 conn（避免阻塞 acceptLoop）
		t.wg.Add(1)
		t.activeConn.Add(1)
		go func(c net.Conn) {
			defer t.wg.Done()
			defer t.activeConn.Add(-1)
			t.handleConn(ctx, c, sshClient)
		}(conn)
	}
}

// handleConn 处理单个 conn：SSH Dial 到 TargetHost:TargetPort + 双向 io.Copy。
func (t *localTunnel) handleConn(ctx context.Context, localConn net.Conn, sshClient *ssh.Client) {
	defer localConn.Close()

	target := net.JoinHostPort(t.spec.TargetHost, fmt.Sprintf("%d", t.spec.TargetPort))

	// 取消传播：ctx 取消时关 local conn（让 io.Copy 返回）。
	//
	// 注意：context.Background().Done() 返回 nil chan，<-nil chan 永远阻塞，
	// 所以必须在 select 里同时监听 t.stopCh —— Stop 触发时也能回收这个 watcher。
	// v0.6.1 修：之前用裸 if ctx != nil 启动 watcher，ctx==Background 时永久泄漏 goroutine。
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = localConn.Close()
			case <-t.stopCh:
				_ = localConn.Close()
			}
		}()
	}

	// client.Dial 内部走 direct-tcpip channel（x/crypto v0.31 tcpip.go）
	remoteConn, err := sshClient.Dial("tcp", target)
	if err != nil {
		// 远端不可达 / SSH channel 拒绝 → 丢弃这条 conn；State 不变
		// （不是 listener 整体失败）
		return
	}
	defer remoteConn.Close()

	// 双向 io.Copy + 任一端返回 → close 另一端（避免 SSH EOF 传播延迟卡 cleanup）。
	// 用 32KB 显式 buffer 避免 io.Copy 走 TCPConn.WriteTo 优化
	// （c.ReadFrom(c) 会无限等自己；这里 src/dst 不同，但保险起见
	// 强制走通用 copyBuffer 路径）。
	var copyWg sync.WaitGroup
	buf := make([]byte, 32*1024)
	copyWg.Add(2)
	closeOnce := &sync.Once{}
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = remoteConn.Close()
			_ = localConn.Close()
		})
	}
	go func() {
		defer copyWg.Done()
		n, _ := io.CopyBuffer(remoteConn, localConn, buf)
		t.bytesOut.Add(n)
		if cw, ok := remoteConn.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		closeBoth()
	}()
	go func() {
		defer copyWg.Done()
		n, _ := io.CopyBuffer(localConn, remoteConn, buf)
		t.bytesIn.Add(n)
		if cw, ok := localConn.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		closeBoth()
	}()

	copyWg.Wait()
}

// Stop 关闭 listener + 等待所有 conn goroutine 退出。
func (t *localTunnel) Stop() error {
	var firstErr error
	t.stopOnce.Do(func() {
		close(t.stopCh)
		if t.listener != nil {
			if err := t.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				firstErr = err
			}
		}
		t.base.setState(TunnelStateStopped)
		// 等所有 handleConn goroutine 退出，避免 Stop 返回后还有 goroutine 在跑
		// （后台 io.Copy 仍可能挂住测试 cleanup）
		t.wg.Wait()
	})
	return firstErr
}

// Spec 返回当前 tunnel 的 spec。
func (t *localTunnel) Spec() Spec { return t.spec }

// State 返回当前状态。
func (t *localTunnel) State() TunnelState { return t.base.State() }

// Stats 返回当前累计统计。
func (t *localTunnel) Stats() Stats {
	return Stats{
		BytesIn:     t.bytesIn.Load(),
		BytesOut:    t.bytesOut.Load(),
		ActiveConns: int(t.activeConn.Load()),
		StartedAt:   t.startedAt.Load(),
	}
}

// closeWriter 是 tcpConn 实现的接口（用于半关）。
// *net.TCPConn 实现了 CloseWrite；定义成 interface 让我们可以 type-assert。
type closeWriter interface {
	CloseWrite() error
}
