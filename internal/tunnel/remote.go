// remote.go 实现 -R（Remote forward）：远端端口 → 本地目标。
//
// 流程：
//  1. client.Listen("tcp", "host:port") 让 SSH server 在远端监听
//     （v0.31 ssh/tcpip.go::Client.Listen 内部发 "tcpip-forward" 请求，
//     远端 accept 后通过 "forwarded-tcpip" 通道回连 → client 端 Accept 返回）
//  2. 每个 Accept 拿到 conn（server 端 accept 后由 SSH 转发的连接）
//  3. net.Dial("tcp", TargetHost:TargetPort) 连本地目标
//  4. io.Copy 双向 pipe
//
// 错误：
//   - Listen 失败（远端拒绝 / 端口已占用）→ Start 返回 error + State → Failed
//   - net.Dial 失败 → 仅丢这条 conn
//   - Stop → CancelTunnelListener + 关本地 conn
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
)

// remoteTunnel 是 Remote mode 的实现。
type remoteTunnel struct {
	spec     Spec
	provider ClientProvider

	listener net.Listener // ssh client 端的 net.Listener
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	bytesIn    atomic.Int64
	bytesOut   atomic.Int64
	activeConn atomic.Int64
	startedAt  atomic.Int64

	base *baseTunnel
}

func newRemoteTunnel(spec Spec, p ClientProvider) *remoteTunnel {
	return &remoteTunnel{
		spec:     spec,
		provider: p,
		stopCh:   make(chan struct{}),
		base:     newBaseTunnel(spec),
	}
}

// Start 通过 SSH server 在远端监听端口。
//
// 注意：x/crypto v0.31 的 Client.Listen 内部 c.handleForwards 启动一个
// handler goroutine 接收 "forwarded-tcpip" channel；Client.Close 时
// 会调 c.forwards.Close 把 listener 全部关掉。所以 Stop 不需要手动
// cancel-tcpip-forward（v0.31 handleForwards 内部做）。
func (t *remoteTunnel) Start(ctx context.Context) error {
	if t.provider == nil {
		t.base.setState(TunnelStateFailed)
		return errors.New("tunnel.remote: nil ClientProvider")
	}
	sshClient, ok := t.provider.Client(t.spec.SessionID)
	if !ok || sshClient == nil {
		t.base.setState(TunnelStateFailed)
		return fmt.Errorf("tunnel.remote.Start: no ssh client for session %q", t.spec.SessionID)
	}

	// SSH server 端监听地址 = BindHost:BindPort
	addr := net.JoinHostPort(t.spec.BindHost, fmt.Sprintf("%d", t.spec.BindPort))
	ln, err := sshClient.Listen("tcp", addr)
	if err != nil {
		t.base.setState(TunnelStateFailed)
		return fmt.Errorf("tunnel.remote.Start: ssh.Listen %s: %w", addr, err)
	}
	t.listener = ln
	t.startedAt.Store(time.Now().Unix())
	t.base.setState(TunnelStateActive)

	t.wg.Add(1)
	go t.acceptLoop(ctx)

	return nil
}

func (t *remoteTunnel) acceptLoop(ctx context.Context) {
	defer t.wg.Done()

	for {
		conn, err := t.listener.Accept()
		if err != nil {
			select {
			case <-t.stopCh:
				return
			default:
				return
			}
		}

		t.wg.Add(1)
		t.activeConn.Add(1)
		go func(c net.Conn) {
			defer t.wg.Done()
			defer t.activeConn.Add(-1)
			t.handleConn(ctx, c)
		}(conn)
	}
}

// handleConn 处理远端 → 远端 conn：拿到 conn 后 net.Dial 到本地 TargetHost:TargetPort。
func (t *remoteTunnel) handleConn(ctx context.Context, remoteConn net.Conn) {
	defer remoteConn.Close()

	if ctx != nil {
		go func() {
			<-ctx.Done()
			_ = remoteConn.Close()
		}()
	}

	target := net.JoinHostPort(t.spec.TargetHost, fmt.Sprintf("%d", t.spec.TargetPort))
	localConn, err := net.Dial("tcp", target)
	if err != nil {
		// 本地目标不可达：丢这条 conn，listener 不停
		return
	}
	defer localConn.Close()

	var copyWg sync.WaitGroup
	buf := make([]byte, 32*1024)
	copyWg.Add(2)
	// 任一 io.Copy 返回 → 关两端（unblock 对端 goroutine；避免依赖 SSH EOF 传播）
	closeOnce := &sync.Once{}
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = remoteConn.Close()
			_ = localConn.Close()
		})
	}
	go func() {
		defer copyWg.Done()
		n, _ := io.CopyBuffer(localConn, remoteConn, buf)
		t.bytesIn.Add(n)
		if cw, ok := localConn.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		closeBoth()
	}()
	go func() {
		defer copyWg.Done()
		n, _ := io.CopyBuffer(remoteConn, localConn, buf)
		t.bytesOut.Add(n)
		if cw, ok := remoteConn.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		closeBoth()
	}()
	copyWg.Wait()
}

// Stop 关闭 ssh listener + 等待 goroutine。
func (t *remoteTunnel) Stop() error {
	var firstErr error
	t.stopOnce.Do(func() {
		close(t.stopCh)
		if t.listener != nil {
			// ssh.Listener.Close 内部会 cancel-tcpip-forward + 停 accept
			if err := t.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				firstErr = err
			}
		}
		t.base.setState(TunnelStateStopped)
		// 等所有 handleConn goroutine 退出
		t.wg.Wait()
	})
	return firstErr
}

func (t *remoteTunnel) Spec() Spec         { return t.spec }
func (t *remoteTunnel) State() TunnelState { return t.base.State() }

func (t *remoteTunnel) Stats() Stats {
	return Stats{
		BytesIn:     t.bytesIn.Load(),
		BytesOut:    t.bytesOut.Load(),
		ActiveConns: int(t.activeConn.Load()),
		StartedAt:   t.startedAt.Load(),
	}
}
