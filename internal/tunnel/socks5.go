// socks5.go 实现 -D（Dynamic SOCKS5）：本地 SOCKS5 代理 → 通过 SSH 中继。
//
// RFC 1928 (SOCKS5) 极简实现，仅支持：
//   - 0x00 NO AUTHENTICATION
//   - 0x01 CONNECT
//   - 0x03 BIND / 0x02 ASSOCIATE → 拒绝（0x07 Command not supported）
//   - auth method != 0x00 → 0xFF 拒绝
//
// 流程：
//  1. net.Listen("tcp", BindHost:BindPort) 拿本地 SOCKS5 listener
//  2. 每个 conn：
//     a) 读 greeting：[ver=0x05][nmethods][methods...]
//     b) 选 method 0x00（不支持的 method 一律 0xFF 拒绝）→ 回 [ver=0x05][0x00]
//     c) 读 request：[ver=0x05][cmd=0x01][rsv=0x00][atyp][addr][port]
//     d) 解析 addr (atyp: 0x01 IPv4 / 0x03 domain / 0x04 IPv6)
//     e) 拿 *ssh.Client → client.Dial("tcp", host:port)
//     f) 回 [ver=0x05][0x00][rsv=0x00][atyp=0x01][0.0.0.0][0x00,0x00]
//     （响应里 BND.ADDR/PORT 多数客户端忽略，绑 0.0.0.0:0 即可）
//     g) io.Copy 双向 pipe
//  3. cmd != CONNECT / atyp 解析失败 / Dial 失败 → 回 [0x05][rep][rsv][atyp=0x01][0.0.0.0][0x00,0x00]
//
// 错误路径：
//   - net.Listen 失败 → Start 返回 error + State → Failed
//   - 单 conn 解析失败 → 0xFF/0x07 拒绝 + close（不影响 listener）
package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// SOCKS5 协议常量。
const (
	socks5Version = 0x05

	// auth method
	socks5AuthNone         = 0x00
	socks5AuthNoAcceptable = 0xFF

	// command
	socks5CmdConnect  = 0x01
	socks5CmdBind     = 0x02
	socks5CmdUDPAssoc = 0x03

	// address type
	socks5AtypIPv4   = 0x01
	socks5AtypDomain = 0x03
	socks5AtypIPv6   = 0x04

	// reply field
	socks5RepSuccess         = 0x00
	socks5RepGeneralFailure  = 0x01
	socks5RepConnRefused     = 0x05
	socks5RepAddrTypeNotSupp = 0x08
	socks5RepCmdNotSupported = 0x07
)

// dynamicTunnel 是 Dynamic (SOCKS5) 模式的实现。
type dynamicTunnel struct {
	spec     Spec
	provider ClientProvider

	listener net.Listener
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	bytesIn    atomic.Int64
	bytesOut   atomic.Int64
	activeConn atomic.Int64
	startedAt  atomic.Int64

	base *baseTunnel
}

func newDynamicTunnel(spec Spec, p ClientProvider) *dynamicTunnel {
	return &dynamicTunnel{
		spec:     spec,
		provider: p,
		stopCh:   make(chan struct{}),
		base:     newBaseTunnel(spec),
	}
}

// Start 启动 SOCKS5 listener。
func (t *dynamicTunnel) Start(ctx context.Context) error {
	addr := net.JoinHostPort(t.spec.BindHost, fmt.Sprintf("%d", t.spec.BindPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.base.setState(TunnelStateFailed)
		return fmt.Errorf("tunnel.dynamic.Start: listen %s: %w", addr, err)
	}
	t.listener = ln
	t.startedAt.Store(time.Now().Unix())
	t.base.setState(TunnelStateActive)

	t.wg.Add(1)
	go t.acceptLoop(ctx)
	return nil
}

func (t *dynamicTunnel) acceptLoop(ctx context.Context) {
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

// handleConn 处理单 SOCKS5 客户端连接。
func (t *dynamicTunnel) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	if ctx != nil {
		go func() {
			// v0.6.1 修：之前裸 <-ctx.Done() 在 ctx==Background 时永久泄漏 goroutine。
			// 加上 t.stopCh 后 Stop 触发也能回收 watcher。
			select {
			case <-ctx.Done():
				_ = conn.Close()
			case <-t.stopCh:
				_ = conn.Close()
			}
		}()
	}

	// 1. greeting
	host, err := t.handshakeAndResolve(conn)
	if err != nil {
		// 错误已写回 client；直接关
		return
	}

	// 2. 拿 *ssh.Client
	sshClient, ok := t.provider.Client(t.spec.SessionID)
	if !ok || sshClient == nil {
		_ = writeSocks5Reply(conn, socks5RepGeneralFailure)
		return
	}

	// 3. SSH Dial 到目标
	remoteConn, err := sshClient.Dial("tcp", host)
	if err != nil {
		// 简化：把任何 dial 失败映射为 "connection refused"
		_ = writeSocks5Reply(conn, socks5RepConnRefused)
		return
	}
	defer remoteConn.Close()

	// 4. 成功 reply
	if err := writeSocks5Reply(conn, socks5RepSuccess); err != nil {
		return
	}

	// 5. 双向 io.Copy
	var copyWg sync.WaitGroup
	buf := make([]byte, 32*1024)
	copyWg.Add(2)
	closeOnce := &sync.Once{}
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = remoteConn.Close()
			_ = conn.Close()
		})
	}
	go func() {
		defer copyWg.Done()
		n, _ := io.CopyBuffer(remoteConn, conn, buf)
		t.bytesOut.Add(n)
		if cw, ok := remoteConn.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		closeBoth()
	}()
	go func() {
		defer copyWg.Done()
		n, _ := io.CopyBuffer(conn, remoteConn, buf)
		t.bytesIn.Add(n)
		if cw, ok := conn.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		closeBoth()
	}()
	copyWg.Wait()
}

// handshakeAndResolve 处理 SOCKS5 握手 + 解析 target host:port。
//
// 成功返回 "host:port" 给后续 ssh.Dial。
// 失败时已向 client 写错误 reply；调用方直接 return。
func (t *dynamicTunnel) handshakeAndResolve(conn net.Conn) (string, error) {
	// 1. greeting: [ver][nmethods][methods...]
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return "", err
	}
	if hdr[0] != socks5Version {
		return "", fmt.Errorf("socks5: bad version %d", hdr[0])
	}
	nMethods := int(hdr[1])
	methods := make([]byte, nMethods)
	if nMethods > 0 {
		if _, err := io.ReadFull(conn, methods); err != nil {
			return "", err
		}
	}

	// 选 method：仅支持 NO AUTH (0x00)
	chosen := socks5AuthNoAcceptable
	for _, m := range methods {
		if m == socks5AuthNone {
			chosen = socks5AuthNone
			break
		}
	}
	// reply method selection
	if _, err := conn.Write([]byte{socks5Version, byte(chosen)}); err != nil {
		return "", err
	}
	if chosen == socks5AuthNoAcceptable {
		return "", errors.New("socks5: no acceptable auth method")
	}

	// 2. request: [ver][cmd][rsv][atyp][addr][port]
	reqHdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHdr); err != nil {
		return "", err
	}
	if reqHdr[0] != socks5Version {
		return "", fmt.Errorf("socks5: request bad version %d", reqHdr[0])
	}
	cmd := reqHdr[1]
	atyp := reqHdr[3]

	// 仅支持 CONNECT
	if cmd != socks5CmdConnect {
		_ = writeSocks5Reply(conn, socks5RepCmdNotSupported)
		return "", fmt.Errorf("socks5: unsupported command %d", cmd)
	}

	// 3. parse address
	host, err := readSocks5Addr(conn, atyp)
	if err != nil {
		_ = writeSocks5Reply(conn, socks5RepAddrTypeNotSupp)
		return "", err
	}

	// 4. parse port
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		_ = writeSocks5Reply(conn, socks5RepGeneralFailure)
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf)

	return net.JoinHostPort(host, fmt.Sprintf("%d", port)), nil
}

// readSocks5Addr 根据 atyp 解析 host。
//
// 0x01 IPv4 (4 bytes) / 0x03 domain (1B len + N bytes) / 0x04 IPv6 (16 bytes)
func readSocks5Addr(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case socks5AtypIPv4:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil

	case socks5AtypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return "", err
		}
		n := int(lenBuf[0])
		if n == 0 {
			return "", errors.New("socks5: empty domain")
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return string(buf), nil

	case socks5AtypIPv6:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil

	default:
		return "", fmt.Errorf("socks5: unknown atyp %d", atyp)
	}
}

// writeSocks5Reply 写固定格式 reply: [ver=0x05][rep][rsv=0x00][atyp=0x01][0.0.0.0][port=0]。
//
// 多数客户端忽略 BND.ADDR/PORT；绑 0.0.0.0:0 让实现简单。
func writeSocks5Reply(conn net.Conn, rep byte) error {
	// [ver][rep][rsv][atyp IPv4][4 bytes addr][2 bytes port]
	reply := []byte{socks5Version, rep, 0x00, socks5AtypIPv4, 0, 0, 0, 0, 0, 0}
	_, err := conn.Write(reply)
	return err
}

func (t *dynamicTunnel) Stop() error {
	var firstErr error
	t.stopOnce.Do(func() {
		close(t.stopCh)
		if t.listener != nil {
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

func (t *dynamicTunnel) Spec() Spec         { return t.spec }
func (t *dynamicTunnel) State() TunnelState { return t.base.State() }
func (t *dynamicTunnel) Stats() Stats {
	return Stats{
		BytesIn:     t.bytesIn.Load(),
		BytesOut:    t.bytesOut.Load(),
		ActiveConns: int(t.activeConn.Load()),
		StartedAt:   t.startedAt.Load(),
	}
}
