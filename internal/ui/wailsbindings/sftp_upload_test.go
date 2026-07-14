// sftp_upload_test.go 覆盖 wailsbindings.SftpUploadFile binding 的端到端 +
// 错误路径。
//
// 测试策略（v0.5.3-B spec）：
//  1. **Happy path 端到端**：用 in-process SSH server（带 SFTP subsystem +
//     sftp.InMemHandler 共享 FS）建真实 session，构造 *wailsbindings.App，
//     调 SftpUploadFile 写字节，再用 sftp.NewClient 直接读回验证内容一致。
//  2. **错误路径 session 不存在**：构造一个空 *app.App，验证
//     SftpUploadFile 把 "session not found" 包装成
//     "wailsbindings.SftpUploadFile: ..." 形式。
//  3. **边界**：空内容（零字节）不报错。
//  4. **覆盖写**：第二次写覆盖第一次（不拼接）。
//  5. **编译期守卫**：SftpUploadFile 签名稳定。
//
// 与现有测试的分工：
//   - sftpclient 端到端（sshclient/integration_test.go::TestSftpClient_Write_Integration）
//     覆盖 sftpclient.Client.Write 的数据流（无 wailsbindings 层）
//   - app.sftpFor 私有方法（app/sftp_test.go）覆盖 map 生命周期
//   - 本文件覆盖 wailsbindings 这一层（薄 wrapper）：拿 client → 调 Write →
//     错误包装。spec 显式要求在 wailsbindings 包测 Wails 反射契约
//     （ctx 第一参数、[]byte 序列化、int 返回值、error 包装）。
//
// 测试基础设施（自包含）：
//   - sftpUploadSSHServer：in-process SSH server + SFTP subsystem +
//     sftp.InMemHandler() 共享 FS。逻辑来自 sshclient/integration_test.go
//     的 sshIntegrationServer，但精简到只支持 sftp subsystem（不实现
//     shell —— SftpUploadFile 走 SFTP subsystem 而非 shell）。
//
// 不覆盖（spec 明确不做）：
//   - openSftp 失败时 wailsbindings 的错误包装：app.TestSftpFor_OpenSftpError
//     已经覆盖了 sftpFor 内部错误包装的代码路径（同一包装逻辑
//     "wailsbindings.<Method>: %w"）。再加 wailsbindings 层测试是重复。
//   - race detector：wailsbindings 是薄 wrapper，sftpclient + app 层
//     已用 -race 验证并发安全。wailsbindings 自身无新增并发原语。
package wailsbindings

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/mossterm/mossterm/internal/app"
	"github.com/mossterm/mossterm/internal/connect"
	"github.com/mossterm/mossterm/internal/session"
	"github.com/mossterm/mossterm/internal/sshclient"
)

// -----------------------------------------------------------------------------
// 桩：in-process SSH server（精简自 sshclient/integration_test.go）
// -----------------------------------------------------------------------------

// sftpUploadSSHServer 是绑在 127.0.0.1:0 上的最小化 SSH server + SFTP subsystem。
//
// 与 sshclient/integration_test.go::sshIntegrationServer 的差异：
//   - 不实现 shell handler（不关心 shell —— SftpUploadFile 走 SFTP subsystem）
//   - 不区分 pty-req / env / window-change 的细分逻辑（全部 reply true）
//   - 仍支持 ctx 取消时主动 sconn.Close()，让 sconn.Wait() 不卡死
//
// 共享 InMemFS Handlers（sftp.InMemHandler()）：v0.5.3 主理修过的
// 关键点 —— 每次 channel 都新建 InMemHandler 会导致跨 sftp.NewClient 的
// write→read 拿不到（root + files map 不一致）。本 server 在 New 时
// 调一次 sftp.InMemHandler()，所有 SFTP channel 共享同一组 Handlers。
type sftpUploadSSHServer struct {
	listener     net.Listener
	serverCfg    *ssh.ServerConfig
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	closeOnce    sync.Once
	sftpHandlers sftp.Handlers
}

func newSftpUploadSSHServer(t *testing.T) *sftpUploadSSHServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}
	serverCfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	serverCfg.AddHostKey(signer)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &sftpUploadSSHServer{
		listener:     l,
		serverCfg:    serverCfg,
		ctx:          ctx,
		cancel:       cancel,
		sftpHandlers: sftp.InMemHandler(), // 共享 FS（v0.5.3 主理修过的关键点）
	}
	s.wg.Add(1)
	go s.acceptLoop()

	t.Cleanup(s.Close)
	return s
}

func (s *sftpUploadSSHServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.handleConn(c)
		}(conn)
	}
}

func (s *sftpUploadSSHServer) handleConn(c net.Conn) {
	defer c.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(c, s.serverCfg)
	if err != nil {
		return
	}
	defer sconn.Close()

	var connWg sync.WaitGroup
	defer connWg.Wait()

	connWg.Add(1)
	go func() {
		defer connWg.Done()
		<-s.ctx.Done()
		sconn.Close()
	}()

	// 全局请求（keepalive 等）—— 全部回复成功
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		for req := range reqs {
			if req.WantReply {
				req.Reply(true, nil)
			}
		}
	}()

	// session 通道 —— 接受 "session" + 分发
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		for newChan := range chans {
			if newChan.ChannelType() != "session" {
				_ = newChan.Reject(ssh.UnknownChannelType, "only session supported")
				continue
			}
			ch, requests, err := newChan.Accept()
			if err != nil {
				continue
			}
			connWg.Add(1)
			go func() {
				defer connWg.Done()
				s.handleSession(ch, requests, &connWg)
			}()
		}
	}()

	_ = sconn.Wait()
}

func (s *sftpUploadSSHServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request, connWg *sync.WaitGroup) {
	defer ch.Close()
	for req := range requests {
		switch req.Type {
		case "subsystem":
			name := sftpUploadSubsystemName(req.Payload)
			if name == "sftp" {
				if req.WantReply {
					req.Reply(true, nil)
				}
				connWg.Add(1)
				go func() {
					defer connWg.Done()
					s.serveSFTP(ch)
				}()
			} else {
				if req.WantReply {
					req.Reply(false, nil)
				}
			}
		default:
			// pty-req / env / window-change / shell / exec —— 全部接受
			// （不真的执行：SftpUploadFile 不需要 shell）
			if req.WantReply {
				req.Reply(true, nil)
			}
		}
	}
}

// sftpUploadSubsystemName 从 subsystem 请求 payload 解析 subsystem 名。
// SSH string 编码：[4B big-endian length][name bytes]
func sftpUploadSubsystemName(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := uint32(payload[0])<<24 | uint32(payload[1])<<16 |
		uint32(payload[2])<<8 | uint32(payload[3])
	if int(n) > len(payload)-4 {
		return ""
	}
	return string(payload[4 : 4+n])
}

func (s *sftpUploadSSHServer) serveSFTP(ch ssh.Channel) {
	rs := sftp.NewRequestServer(ch, s.sftpHandlers)
	_ = rs.Serve()
	_ = rs.Close()
}

func (s *sftpUploadSSHServer) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.listener.Close()
		s.wg.Wait()
	})
}

func (s *sftpUploadSSHServer) hostAndPort(t *testing.T) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}
	return host, port
}

// -----------------------------------------------------------------------------
// 桩：构造 *wailsbindings.App（用真实 *app.App + 真 SSH server）
// -----------------------------------------------------------------------------

// newTestWBApp 构造一个 *wailsbindings.App，绑到真 SSH server。
//
// 流程：
//  1. 起 SSH server
//  2. 构造 *app.App（用真 sshclient.Connector + 默认 openSftp = sftpclient.Open）
//  3. 用 *app.App 的 Sessions() 打开一个 session 到 test server
//  4. 等 state=Established（2s 超时）
//  5. 拿 wailsbindings.New(realApp) 构造 *wailsbindings.App
//
// 返回 wailsbindingsApp + sessionID（供测试函数调 SftpUploadFile）。
func newTestWBApp(t *testing.T) (*App, session.ID) {
	t.Helper()

	server := newSftpUploadSSHServer(t)
	host, port := server.hostAndPort(t)

	mm := session.NewMemoryManager()
	registry := connect.NewMemoryRegistry()
	if err := registry.Register("ssh", func(deps connect.Deps) (connect.Connector, error) {
		return sshclient.New(deps)
	}); err != nil {
		t.Fatalf("register ssh factory: %v", err)
	}
	mm.WithConnectors(registry)

	realApp := app.New(app.Deps{
		Sessions:   mm,
		Connectors: registry,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	sess, err := mm.Open(context.Background(), session.OpenRequest{
		Host: host, Port: port, User: "test",
		Auth:    session.AuthSpec{Kind: "password", Password: "x"},
		Columns: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("mm.Open: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := sess.State(); st == session.StateEstablished {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := sess.State(); st != session.StateEstablished {
		t.Fatalf("session state = %s, want Established", st)
	}

	return New(realApp), sess.Info().ID
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// sftpClientFromSession 从 session 拿 *sftp.Client 用于读回验证。
//
// 流程：session.Connector() → *sshclient.Connector → RawClient() → sftp.NewClient
// 走独立 subsystem channel（与 SftpUploadFile 已开的 channel 并行）。
// pkg/sftp 的 sftp.NewClient 自身是 goroutine safe（每次 New 拿独立 session），
// 但底层 *ssh.Client 的 channel 复用是 SSH 协议层负责。
func sftpClientFromSession(t *testing.T, wb *App, sessID session.ID) *sftp.Client {
	t.Helper()
	realApp := wb.core
	sess, ok := realApp.Sessions().Get(sessID)
	if !ok {
		t.Fatal("session disappeared from manager")
	}
	connector := sess.Connector()
	if connector == nil {
		t.Fatal("session.Connector() == nil")
	}
	sshConn, ok := connector.(*sshclient.Connector)
	if !ok {
		t.Fatalf("connector type = %T, want *sshclient.Connector", connector)
	}
	sshClient := sshConn.RawClient()
	if sshClient == nil {
		t.Fatal("sshClient == nil after Established")
	}
	sc, err := sftp.NewClient(sshClient)
	if err != nil {
		t.Fatalf("sftp.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	return sc
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

// TestSftpUploadFile_HappyPath 端到端验证 wailsbindings.SftpUploadFile：
//
//  1. 通过 in-process SSH server 打开 session
//  2. 构造 *wailsbindings.App
//  3. 调 SftpUploadFile 写 1 KiB payload
//  4. 用 sftp.NewClient 直接打开同一文件读回，验证内容 + size
//
// 关键点：本测试在 wailsbindings 包（不是 app/sshclient），
// 是 Wails binding 层的契约验证：
//   - ctx 作为第一参数
//   - []byte 输入正确序列化
//   - int 返回值正确反序列化
//   - error 正确包装
func TestSftpUploadFile_HappyPath(t *testing.T) {
	wb, sessionID := newTestWBApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const testFile = "/v053-upload-test.txt"
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte((i * 13) & 0xff) // 伪随机可识别
	}

	n, err := wb.SftpUploadFile(ctx, string(sessionID), testFile, payload)
	if err != nil {
		t.Fatalf("SftpUploadFile: %v", err)
	}
	if n != len(payload) {
		t.Errorf("SftpUploadFile returned n = %d, want %d", n, len(payload))
	}

	// 读回 + 字节级比对（绕开 sftpclient.Client 自己）
	rawSftp := sftpClientFromSession(t, wb, sessionID)
	rf, err := rawSftp.OpenFile(testFile, os.O_RDONLY)
	if err != nil {
		t.Fatalf("sftp.OpenFile (read) %q: %v", testFile, err)
	}
	got, err := io.ReadAll(rf)
	_ = rf.Close()
	if err != nil {
		t.Fatalf("io.ReadAll: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("readback size = %d, want %d", len(got), len(payload))
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Errorf("readback byte %d = %d, want %d", i, got[i], payload[i])
			break
		}
	}
}

// TestSftpUploadFile_Overwrite 验证 SftpUploadFile 覆盖写语义：
//  1. 第一次写 256B 全 A
//  2. 第二次写 1024B 全 B
//  3. 读回应当是 1024B 全 B（覆盖写，不是拼接）
func TestSftpUploadFile_Overwrite(t *testing.T) {
	wb, sessionID := newTestWBApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const testFile = "/v053-overwrite.txt"
	first := make([]byte, 256)
	for i := range first {
		first[i] = 'A'
	}
	second := make([]byte, 1024)
	for i := range second {
		second[i] = 'B'
	}

	if n, err := wb.SftpUploadFile(ctx, string(sessionID), testFile, first); err != nil || n != len(first) {
		t.Fatalf("first upload: n=%d, err=%v", n, err)
	}
	if n, err := wb.SftpUploadFile(ctx, string(sessionID), testFile, second); err != nil || n != len(second) {
		t.Fatalf("second upload: n=%d, err=%v", n, err)
	}

	// 读回
	rawSftp := sftpClientFromSession(t, wb, sessionID)
	rf, err := rawSftp.OpenFile(testFile, os.O_RDONLY)
	if err != nil {
		t.Fatalf("OpenFile read: %v", err)
	}
	got, err := io.ReadAll(rf)
	_ = rf.Close()
	if err != nil {
		t.Fatalf("io.ReadAll: %v", err)
	}
	if len(got) != len(second) {
		t.Fatalf("readback size = %d, want %d (覆盖写应只保留第二次)", len(got), len(second))
	}
	for i := range second {
		if got[i] != 'B' {
			t.Errorf("readback byte %d = %d, want 'B'", i, got[i])
			break
		}
	}
}

// TestSftpUploadFile_SessionNotFound 验证 sessionID 不存在时 SftpUploadFile
// 返回 wrapped error（前缀必须是 "wailsbindings.SftpUploadFile: "）。
func TestSftpUploadFile_SessionNotFound(t *testing.T) {
	// 构造一个空 *app.App（没有 session）
	mm := session.NewMemoryManager()
	registry := connect.NewMemoryRegistry()
	realApp := app.New(app.Deps{
		Sessions:   mm,
		Connectors: registry,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	wb := New(realApp)

	ctx := context.Background()
	_, err := wb.SftpUploadFile(ctx, "nonexistent-id", "/foo.txt", []byte("hi"))
	if err == nil {
		t.Fatal("SftpUploadFile: want error, got nil")
	}
	const prefix = "wailsbindings.SftpUploadFile: "
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error message = %q, want prefix %q", err.Error(), prefix)
	}
}

// TestSftpUploadFile_EmptyContent 验证写空内容（零字节）不报错。
//
// 边界条件：sftpclient.Client.Write(path, []byte{}) → OpenFile 成功
// + rf.Write([]byte{}) 立即返回 (0, nil)。binding 应当返回 n=0, err=nil。
func TestSftpUploadFile_EmptyContent(t *testing.T) {
	wb, sessionID := newTestWBApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	n, err := wb.SftpUploadFile(ctx, string(sessionID), "/v053-empty.txt", []byte{})
	if err != nil {
		t.Fatalf("SftpUploadFile empty: %v", err)
	}
	if n != 0 {
		t.Errorf("SftpUploadFile empty: n = %d, want 0", n)
	}
}

// 编译期守卫：保证 SftpUploadFile 签名在 wailsbindings 侧稳定。
// Wails 反射对签名敏感（ctx 第一参数、[]byte 序列化为 Uint8Array、
// int 返回值）。任何破坏都会让本测试连编译都过不了。
func TestSftpUploadFile_Signature(t *testing.T) {
	var wb *App
	var _ func(context.Context, string, string, []byte) (int, error) = wb.SftpUploadFile
}
