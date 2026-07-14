// integration_test.go 端到端覆盖 internal/sshclient.Connector 的
// 全链路：Dial → OpenSession → sftp.NewClient → 真 SFTP IO。
//
// 目的（v0.5.2 spec）：
//  1. 守住 v0.1 的隐 bug：OpenSession 把 StdinPipe/StdoutPipe 写在 Shell() 之后
//     （x/crypto v0.22.0 的 Session.StdinPipe 在 started==true 时返回
//     "StdinPipe after process started" 错误）。v0.5.1 已经修好；本文件
//     写一个真 SSH server 把它钉死。
//  2. 验证 Connector.Dial → OpenSession → RawClient → sftp.NewClient
//     → 真 SFTP Mkdir/Write/Read/ReadDir 端到端联通。
//
// 测试基础设施：
//   - sshIntegrationServer：in-process SSH server，绑 127.0.0.1:0，
//     ed25519 临时 host key，接受任何用户名/密码
//   - sftp subsystem 走 github.com/pkg/sftp 的 sftp.InMemHandler()
//     （package sftp 的导出 example FS —— 完整 read/write/mkdir/rmdir/
//     rename 支持，全在内存不落盘）
//   - 真 *sshclient.Connector —— 无 mock、无 factory 注入
//
// 设计参考：
//   - 借鉴 internal/app/sftp_test.go::sshTestServer (v0.5.1) 的 in-process
//     SSH server 骨架（ed25519 key + accept any password + 全局请求回复成功）
//   - 在 session handler 上扩展支持 "subsystem" 请求，name="sftp" 时
//     把 channel 交给 sftp.NewRequestServer 跑 InMemHandler
//
// 不覆盖（v0.5.2 spec 明确不做）：
//   - 真 shell 行为（PTY 数据流、命令 echo 循环）—— v0.1-v0.5.x 单测已 mock
//   - keepalive 失败路径（Connector 资源回收）—— keepalive_test.go 单测
//   - first-use trust / known_hosts —— knownhosts/ 单测
package sshclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/mossterm/mossterm/internal/connect"
	"github.com/mossterm/mossterm/internal/sftpclient"
)

// -----------------------------------------------------------------------------
// 桩：in-process SSH server（扩展自 v0.5.1 的 sshTestServer）
// -----------------------------------------------------------------------------

// sshIntegrationServer 是绑在 127.0.0.1:0 上的最小化 SSH server + SFTP subsystem。
//
// 接受任何用户名/密码；
// 接受 "session" 类型的 channel open；
// 在 session 内的请求分发：
//   - "pty-req" / "env" / "window-change" / "exec" → 回复 true（不动）
//   - "shell" → 回复 true + 启动 serveShell goroutine（写 "shell: OK\n" +
//     排空 stdin 直到 channel 关闭）
//   - "subsystem"，name="sftp" → 回复 true + 启动 serveSFTP goroutine
//     （sftp.NewRequestServer + sftp.InMemHandler）
//   - 其他 subsystem → 回复 false
//
// 清理：t.Cleanup 触发 s.Close()：
//  1. cancel(ctx) → 所有 handleConn 收到信号，sconn.Close() → sconn.Wait() 返回
//  2. listener.Close() → acceptLoop 退出
//  3. wg.Wait() 等所有 goroutine 退出
type sshIntegrationServer struct {
	listener  net.Listener
	serverCfg *ssh.ServerConfig

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once

	// sftpHandlers 是 v0.5.3 共享的 InMemFS Handlers（4 个 method 共享一个 root）。
	//
	// 原 v0.5.2 实现每次 SFTP channel 调 sftp.InMemHandler() —— 内部 root + files map
	// 是新建的，导致 test 调"第二个 sftp.NewClient"读 server 端文件时拿不到
	// "第一个 client 写入的内容"（file does not exist）。
	//
	// 现在 sftp.InMemHandler() 只在 server 启动时调一次，所有 SFTP channel 共享
	// 同一组 Handlers（内部 4 个 method 引用同一个 root），跨 sftp.NewClient
	// 的 read/write 一致。
	sftpHandlers sftp.Handlers

	// t 用于把 SFTP server 错误通过 t.Log 报告（不通过 slog，避免污染 stderr）
	t *testing.T
}

// newSSHIntegrationServer 构造一个临时 SSH server 并注册 t.Cleanup。
func newSSHIntegrationServer(t *testing.T) *sshIntegrationServer {
	t.Helper()

	// 1. 生成临时 ed25519 host key
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}

	// 2. server config —— 任何用户名/密码
	serverCfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	serverCfg.AddHostKey(signer)

	// 3. 绑 127.0.0.1:0（OS 分配空闲端口）
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	// 4. 启动 acceptLoop
	ctx, cancel := context.WithCancel(context.Background())

	// 5. v0.5.3：构造共享 InMemFS Handlers（4 个 method 引用同一个 root，
	// 跨 sftp.NewClient 的 read/write 一致）
	sharedHandlers := sftp.InMemHandler()

	s := &sshIntegrationServer{
		listener:     l,
		serverCfg:    serverCfg,
		ctx:          ctx,
		cancel:       cancel,
		sftpHandlers: sharedHandlers,
		t:            t,
	}
	s.wg.Add(1)
	go s.acceptLoop()

	t.Cleanup(s.Close)
	return s
}

func (s *sshIntegrationServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.handleConn(c)
		}(conn)
	}
}

// handleConn 处理一个 SSH 连接：全局请求 + session 通道。
// ctx 取消时主动 sconn.Close()，让 sconn.Wait() 返回。
func (s *sshIntegrationServer) handleConn(c net.Conn) {
	defer c.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(c, s.serverCfg)
	if err != nil {
		return
	}
	defer sconn.Close()

	var connWg sync.WaitGroup
	defer connWg.Wait()

	// ctx 取消 → sconn.Close() → sconn.Wait() 返回
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		<-s.ctx.Done()
		sconn.Close()
	}()

	// 全局请求（keepalive 等）：全部回复成功
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		for req := range reqs {
			if req.WantReply {
				req.Reply(true, nil)
			}
		}
	}()

	// session 通道：接受 "session" 类型 + 分发请求
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

// handleSession 是单个 session channel 的请求分发循环。
//
// 设计：
//   - 本 goroutine 是 requests channel 的**唯一**读取者（无数据竞争）
//   - "shell" / "subsystem" 收到后回复 true，把 channel 交给独立
//     goroutine 持续服务（serveShell / serveSFTP），本 goroutine 继续
//     处理可能的后续 "window-change" / "env" 请求
//   - requests channel 关闭（客户端关闭 SSH session）时，defer ch.Close()
//     关闭 channel，让 serveShell / serveSFTP 退出
//
// 与 v0.5.1 区别：v0.5.1 不区分 shell / subsystem（全部 reply true），
// 本版本需要把 channel 真的交给 sftp.InMemHandler 跑，所以要分派。
func (s *sshIntegrationServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request, connWg *sync.WaitGroup) {
	defer ch.Close()

	for req := range requests {
		switch req.Type {
		case "shell":
			if req.WantReply {
				req.Reply(true, nil)
			}
			// 启动 shell 服务的独立 goroutine。
			// 本 goroutine 继续读 requests 处理可能的 window-change。
			connWg.Add(1)
			go func() {
				defer connWg.Done()
				s.serveShell(ch)
			}()

		case "subsystem":
			// subsystem 名的 payload 格式：[4B length][name bytes]
			// 长度字段是大端 uint32（SSH string 编码）。如果 length > payload 长度
			// 说明 payload 不规范，但 InMemHandler 之外的 subsystem 都会被拒绝。
			name := subsystemName(req.Payload)

			switch name {
			case "sftp":
				if req.WantReply {
					req.Reply(true, nil)
				}
				connWg.Add(1)
				go func() {
					defer connWg.Done()
					s.serveSFTP(ch)
				}()
			default:
				// 未知 subsystem —— 拒绝
				if req.WantReply {
					req.Reply(false, nil)
				}
			}

		case "pty-req", "env", "window-change", "exec":
			// 设置类请求 —— 全部接受，exec 暂不真执行
			if req.WantReply {
				req.Reply(true, nil)
			}

		default:
			// 未知请求 —— 接受（保守策略，避免客户端卡死）
			if req.WantReply {
				req.Reply(true, nil)
			}
		}
	}
}

// subsystemName 从 subsystem 请求 payload 解析 subsystem 名。
//
// SSH string 编码：[4B big-endian length][name bytes]
// x/crypto 提供 ssh.Unmarshal 但那需要 *Request；这里我们直接字节切。
//
// 防护：
//   - payload 长度 < 4 → 返回 ""（让调用方拒绝）
//   - length > len(payload)-4 → 返回 ""（malformed）
func subsystemName(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
	if int(n) > len(payload)-4 {
		return ""
	}
	return string(payload[4 : 4+n])
}

// serveShell 模拟一个最小 shell：
//  1. 写 "shell: OK\n" 到 channel（让 client 的 sess.Read 能拿到字节，
//     证明 stdout pipe 工作）
//  2. 排空 channel 读直到 EOF（client 关闭 SSH session 时触发）
//     —— 不需要真的 echo，sess.Write 写到 stdin 的数据 sink 掉即可
//
// 不需要 ch.Close()：上层 handleSession 的 defer 会负责。
func (s *sshIntegrationServer) serveShell(ch ssh.Channel) {
	if _, err := ch.Write([]byte("shell: OK\n")); err != nil {
		// channel 已经被对端关闭 —— 直接退出
		return
	}
	// 排空 stdin 直到 client 关闭。io.Copy 在 channel 关闭时返回
	// （io.EOF 或 net 错误），不需要区分。
	_, _ = io.Copy(io.Discard, ch)
}

// serveSFTP 把 channel 交给 sftp.NewRequestServer + 共享的 InMemHandler。
//
// v0.5.3 关键改动：复用 server.sftpHandlers（4 个 method 引用同一个 root）。
// 原 v0.5.2 实现每次 channel 调 sftp.InMemHandler() —— 内部 root + files map
// 是新建的，导致 test 调"第二个 sftp.NewClient"读 server 端文件时拿不到
// "第一个 client 写入的内容"（file does not exist）。
func (s *sshIntegrationServer) serveSFTP(ch ssh.Channel) {
	rs := sftp.NewRequestServer(ch, s.sftpHandlers)
	err := rs.Serve()
	if err != nil && err != io.EOF {
		// 用 t.Log 报告（不污染 stderr），不影响 test pass/fail
		s.t.Logf("sftp server Serve returned: %v", err)
	}
	_ = rs.Close()
}

func (s *sshIntegrationServer) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.listener.Close()
		s.wg.Wait()
	})
}

// hostPort 返回 "host:port" 给 *Connector.Dial 用。
func (s *sshIntegrationServer) hostPort() string {
	return s.listener.Addr().String()
}

// hostAndPort 拆分成 host 和 numeric port，喂 connect.DialParams。
func (s *sshIntegrationServer) hostAndPort(t *testing.T) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.hostPort())
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", s.hostPort(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", portStr, err)
	}
	return host, port
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// newTestDeps 构造一个 connect.Deps 适用于本地 in-process SSH server。
//
//   - DialTimeout 5s（短超时，快速暴露问题）
//   - KeepAlive -1 显式禁用（client.go 在 KeepAlive==0 时会兜底为 30s 并
//     slog.Info；负数是唯一彻底禁用的方式，触发 if c.keepAlive > 0 失败
//     → 不启动 runKeepAlive 协程 → 不打 keepalive 日志）
//   - HostKeyCb InsecureIgnoreHostKey（test 是本地临时 server，
//     不验 host key；known_hosts 路径走单测）
//   - BannerCb noop 抑制 banner 噪声
func newTestDeps() connect.Deps {
	return connect.Deps{
		DialTimeout: 5 * time.Second,
		KeepAlive:   -1, // 显式禁用：client.go 的 > 0 判断会跳过 keepalive
		HostKeyCb:   ssh.InsecureIgnoreHostKey(),
		BannerCb:    func(_ string) error { return nil },
	}
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

// TestConnector_OpenSession_FullSFTPPath 是 v0.5.2 spec 的主回归测试。
//
// 流程（守住 v0.1 OpenSession 隐 bug）：
//  1. sshclient.New(deps) 构造 Connector
//  2. Connector.Dial() 拿到 net.Conn
//  3. Connector.OpenSession() 拿到 connect.Session
//     —— **关键回归点**：这一步如果 StdinPipe/StdoutPipe 写在 Shell() 之后，
//     x/crypto v0.22.0 会返回 "StdinPipe after process started"。
//     OpenSession 成功 = 顺序正确 = bug 不在。
//  4. sess.Read() / sess.Write() 各调一次：
//     - Read 期望拿到 server 写的 "shell: OK\n"（验证 stdout pipe 通）
//     - Write 不期望 error（验证 stdin pipe 通；server 排空 stdin）
//  5. sftp.NewClient(Connector.RawClient()) 拿 *sftp.Client
//  6. 端到端 SFTP IO：Mkdir / Write / Read / ReadDir
//     —— 通过独立的 "subsystem" channel（SFTP），与 shell session 独立
func TestConnector_OpenSession_FullSFTPPath(t *testing.T) {
	server := newSSHIntegrationServer(t)
	host, port := server.hostAndPort(t)

	// 1. 构造 Connector
	conn, err := New(newTestDeps())
	if err != nil {
		t.Fatalf("sshclient.New: %v", err)
	}
	defer conn.Close()

	// 回归守卫（间接）：Dial 之前 RawClient 必须是 nil
	if rc := conn.RawClient(); rc != nil {
		t.Errorf("RawClient() before Dial = %p, want nil", rc)
	}

	// 2. Dial —— SSH 握手（TCP + 协议层）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nc, err := conn.Dial(ctx, connect.DialParams{
		Host: host,
		Port: port,
		User: "test",
		Auth: connect.PasswordAuth("hunter2"),
	})
	if err != nil {
		t.Fatalf("Connector.Dial: %v", err)
	}
	defer nc.Close()

	// Dial 后 RawClient 必须非 nil
	if rc := conn.RawClient(); rc == nil {
		t.Fatal("RawClient() after Dial = nil, want non-nil *ssh.Client")
	}

	// 3. OpenSession —— 关键回归点
	sess, err := conn.OpenSession(ctx, nc, connect.SessionOpts{
		Term: "xterm-256color",
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		// 最可能原因：StdinPipe/StdoutPipe/Shell 顺序写反
		// x/crypto v0.22.0 错误信息："StdinPipe after process started"
		t.Fatalf("Connector.OpenSession: %v\nREGRESSION: StdinPipe/StdoutPipe 顺序写反了？参考 client.go OpenSession 顶部注释。", err)
	}
	defer sess.Close()

	// 4a. sess.Read —— server 写 "shell: OK\n"
	// 用 goroutine + 3s 超时做死锁守卫（如果 server 没写 / pipe 没接好）
	readBuf := make([]byte, 64)
	type readResult struct {
		n   int
		err error
	}
	readCh := make(chan readResult, 1)
	go func() {
		n, rerr := sess.Read(readBuf)
		readCh <- readResult{n: n, err: rerr}
	}()
	var rr readResult
	select {
	case rr = <-readCh:
	case <-time.After(3 * time.Second):
		t.Fatal("sess.Read did not return within 3s (server should write 'shell: OK\\n' immediately)")
	}
	// accept nil err (data) or io.EOF (server closed before we read);
	// 但 v0.5.2 spec 要求 server 写 "shell: OK\n" 后保持 channel 开放，
	// 所以 n > 0 + 无 err 是预期
	if rr.err != nil && rr.err != io.EOF {
		t.Errorf("sess.Read err: %v (want nil or io.EOF)", rr.err)
	}
	if rr.n == 0 {
		t.Errorf("sess.Read returned 0 bytes; server should have written 'shell: OK\\n'")
	} else if !strings.Contains(string(readBuf[:rr.n]), "shell: OK") {
		t.Errorf("sess.Read = %q, want substring 'shell: OK'", string(readBuf[:rr.n]))
	}

	// 4b. sess.Write —— 验证 stdin pipe 通
	// server 的 serveShell 排空 stdin，Write 不会立即失败
	const writePayload = "echo regression\n" // 16 字节
	if n, werr := sess.Write([]byte(writePayload)); werr != nil {
		t.Errorf("sess.Write err: %v (want nil)", werr)
	} else if n != len(writePayload) {
		t.Errorf("sess.Write n = %d, want %d", n, len(writePayload))
	}

	// 5. SFTP —— 通过独立 subsystem channel（不开 shell channel）
	rawClient := conn.RawClient()
	if rawClient == nil {
		t.Fatal("RawClient() returned nil after Dial+OpenSession (regression?)")
	}
	sftpCli, err := sftp.NewClient(rawClient)
	if err != nil {
		t.Fatalf("sftp.NewClient(RawClient()): %v", err)
	}
	defer sftpCli.Close()

	// 6. 端到端 SFTP IO
	const testDir = "/mossterm-it"
	const testFile = testDir + "/hello.txt"
	const testContent = "hello, sftp regression guard\n"

	// 6a. Mkdir
	if err := sftpCli.Mkdir(testDir); err != nil {
		t.Fatalf("sftpCli.Mkdir(%q): %v", testDir, err)
	}
	t.Cleanup(func() { _ = sftpCli.RemoveAll(testDir) })

	// 6b. Write
	wf, err := sftpCli.OpenFile(testFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		t.Fatalf("sftpCli.OpenFile (write) %q: %v", testFile, err)
	}
	if _, err := wf.Write([]byte(testContent)); err != nil {
		t.Errorf("wf.Write: %v", err)
	}
	if err := wf.Close(); err != nil {
		t.Errorf("wf.Close: %v", err)
	}

	// 6c. Read back
	rf, err := sftpCli.OpenFile(testFile, os.O_RDONLY)
	if err != nil {
		t.Fatalf("sftpCli.OpenFile (read) %q: %v", testFile, err)
	}
	got, err := io.ReadAll(rf)
	if err != nil {
		t.Errorf("io.ReadAll: %v", err)
	}
	if err := rf.Close(); err != nil {
		t.Errorf("rf.Close: %v", err)
	}
	if string(got) != testContent {
		t.Errorf("read content mismatch:\n got:  %q\n want: %q", got, testContent)
	}

	// 6d. ReadDir
	entries, err := sftpCli.ReadDir(testDir)
	if err != nil {
		t.Fatalf("sftpCli.ReadDir(%q): %v", testDir, err)
	}
	found := false
	for _, e := range entries {
		if e.Name() == "hello.txt" {
			found = true
			if e.Size() != int64(len(testContent)) {
				t.Errorf("hello.txt size = %d, want %d", e.Size(), len(testContent))
			}
			break
		}
	}
	if !found {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("ReadDir(%q) did not contain hello.txt; got %v", testDir, names)
	}
}

// TestConnector_OpenSession_StdinPipeOrderRegression 单独守住 StdinPipe/Shell
// 顺序这条路径。
//
// 做法：与 TestConnector_OpenSession_FullSFTPPath 同样的 in-process SSH server，
// 但测试 body 更小、更聚焦：
//  1. Dial
//  2. OpenSession
//  3. 明确断言 err == nil 且 err.Error() 不含 "StdinPipe after process started"
//  4. sess.Read + sess.Write 各一次
//
// 与主测试互补：主测试覆盖完整业务流，本测试只盯 OpenSession 这一步。
// 万一未来有人在 OpenSession 里加新逻辑引入了 race / panic，也能被这个
// 小测试单独定位。
func TestConnector_OpenSession_StdinPipeOrderRegression(t *testing.T) {
	server := newSSHIntegrationServer(t)
	host, port := server.hostAndPort(t)

	conn, err := New(newTestDeps())
	if err != nil {
		t.Fatalf("sshclient.New: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nc, err := conn.Dial(ctx, connect.DialParams{
		Host: host,
		Port: port,
		User: "test",
		Auth: connect.PasswordAuth("hunter2"),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer nc.Close()

	sess, err := conn.OpenSession(ctx, nc, connect.SessionOpts{
		Term: "xterm-256color",
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		// 关键回归断言：err 字符串里**不应该**出现
		// "StdinPipe after process started"（x/crypto v0.22.0 在
		// started==true 时调 StdinPipe/StdoutPipe 返回的错误）。
		if strings.Contains(err.Error(), "StdinPipe after process started") {
			t.Fatalf("OpenSession returned x/crypto 'StdinPipe after process started': %v\n"+
				"这意味着 StdinPipe/StdoutPipe 在 Shell() 之后被调了。\n"+
				"参考 internal/sshclient/client.go OpenSession 顶部注释。", err)
		}
		t.Fatalf("OpenSession: %v", err)
	}
	defer sess.Close()

	// 至少一次 Read + Write
	const writePayload = "ping\n" // 5 字节
	if n, err := sess.Write([]byte(writePayload)); err != nil || n != len(writePayload) {
		t.Errorf("sess.Write: n=%d, err=%v", n, err)
	}
	buf := make([]byte, 64)
	readCh := make(chan struct {
		n   int
		err error
	}, 1)
	go func() {
		n, rerr := sess.Read(buf)
		readCh <- struct {
			n   int
			err error
		}{n, rerr}
	}()
	select {
	case res := <-readCh:
		if res.err != nil && res.err != io.EOF {
			t.Errorf("sess.Read: err=%v (want nil or io.EOF)", res.err)
		}
		if res.n == 0 {
			t.Error("sess.Read returned 0 bytes")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sess.Read did not return within 3s")
	}
}

// TestConnector_RawClient_Lifecycle 单独守住 RawClient() 的生命周期。
//
// v0.5.2 spec 没显式要求，但与主测试互补：
//   - Dial 前 RawClient() == nil
//   - Dial 后 RawClient() != nil
//   - 关闭 *ssh.Client 后 RawClient() 的引用（注意 Connector 不 Close *ssh.Client，
//     Connector.Close 只关 keepalive）—— 这一条没法在测试里直接验证，
//     留给真生命周期测试（v0.6+）
func TestConnector_RawClient_Lifecycle(t *testing.T) {
	server := newSSHIntegrationServer(t)
	host, port := server.hostAndPort(t)

	conn, err := New(newTestDeps())
	if err != nil {
		t.Fatalf("sshclient.New: %v", err)
	}
	defer conn.Close()

	if rc := conn.RawClient(); rc != nil {
		t.Errorf("RawClient() before Dial = %p, want nil", rc)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nc, err := conn.Dial(ctx, connect.DialParams{
		Host: host,
		Port: port,
		User: "test",
		Auth: connect.PasswordAuth("hunter2"),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer nc.Close()

	rc := conn.RawClient()
	if rc == nil {
		t.Fatal("RawClient() after Dial = nil, want non-nil")
	}

	// 简单 sanity check：拿 RawClient 调 Close 不应让 Connector panic
	// (Connector 不持有 *ssh.Client 的所有权，Close 是 caller 责任)
	_ = rc.Close()

	// Connector 仍可调 RawClient（拿到的是已 Close 的 *ssh.Client），
	// 这是 by design —— Connector 不知道 caller 何时关 client
	if rc2 := conn.RawClient(); rc2 == nil {
		t.Error("RawClient() after manual Close = nil (should still return cached pointer)")
	}
}

// -----------------------------------------------------------------------------
// sftpclient.Client v0.5.3 写路径集成测试
// -----------------------------------------------------------------------------
//
// 这两个测试通过 in-process SSH server 验证 v0.5.3 新加的 sftpclient.Client
// 公开方法（Write / UploadFile），用真 *sftp.Client + InMemHandler 跑端到端
// 流程：写字节 → 读回 → 校验内容一致。
//
// 不在 client_test.go 里跑的原因：Client 的 *sftp.Client 字段是
// 私有（c.sc），mock 不动；本测试不依赖私有字段，只用公开 API（sftpclient.Open
// 拿 Client → 调 Write/UploadFile），所以放在 sshclient/integration_test.go
// 共享 in-process server 设施。
//
// 不依赖前端 binding：Write / UploadFile 是 sftpclient 层 API，
// wailsbindings.SftpUploadFile 在另一个文件单独测（unit-level）。
//
// 故意不在主 SFTP 测试 (TestConnector_OpenSession_FullSFTPPath) 里加步骤：
// 那个测试守 v0.1 StdinPipe/Shell 顺序的历史 bug，混合新功能会让失败信号
// 不清。分两个小测试更易定位。

// TestSftpClient_Write_Integration 验证 sftpclient.Client.Write 端到端：
//
//  1. 准备一个测试目录 + 测试文件路径
//  2. Write 1 KiB 数据
//  3. 用 sftp.NewClient 直接打开同一个文件读回
//  4. 内容 + size 校验
func TestSftpClient_Write_Integration(t *testing.T) {
	server := newSSHIntegrationServer(t)
	host, port := server.hostAndPort(t)

	conn, err := New(newTestDeps())
	if err != nil {
		t.Fatalf("sshclient.New: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nc, err := conn.Dial(ctx, connect.DialParams{
		Host: host,
		Port: port,
		User: "test",
		Auth: connect.PasswordAuth("hunter2"),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer nc.Close()

	// 拿真 *ssh.Client → sftpclient.Client
	sshClient := conn.RawClient()
	if sshClient == nil {
		t.Fatal("RawClient() = nil after Dial")
	}
	sftpWrapper, err := sftpclient.Open(sshClient)
	if err != nil {
		t.Fatalf("sftpclient.Open: %v", err)
	}
	defer sftpWrapper.Close()

	const testDir = "/v053-write"
	const testFile = testDir + "/out.bin"

	// 准备：建目录
	if err := sftpWrapper.Mkdir(testDir); err != nil {
		t.Fatalf("Mkdir(%q): %v", testDir, err)
	}
	t.Cleanup(func() {
		_ = sftpWrapper.Remove(testFile)
		_ = sftpWrapper.Remove(testDir)
	})

	// 1 KiB 写入 payload
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte(i & 0xff)
	}

	n, err := sftpWrapper.Write(testFile, payload)
	if err != nil {
		t.Fatalf("sftpclient.Client.Write: %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write n = %d, want %d", n, len(payload))
	}

	// 用 sftp.NewClient 直接读回验证（绕开 sftpclient.Client 自身，
	// 避免"自己写自己读"的同源循环）
	rawSftp, err := sftp.NewClient(sshClient)
	if err != nil {
		t.Fatalf("sftp.NewClient: %v", err)
	}
	defer rawSftp.Close()

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

// TestSftpClient_Write_Overwrite_Integration 验证 Write 覆盖写语义。
//
// 写一次 256B → 写一次 1024B → 读回应该是 1024B 新内容，不是 1280B 拼接。
// OpenFile flags = O_WRONLY|O_CREATE|O_TRUNC 是覆盖写语义的关键。
func TestSftpClient_Write_Overwrite_Integration(t *testing.T) {
	server := newSSHIntegrationServer(t)
	host, port := server.hostAndPort(t)

	conn, err := New(newTestDeps())
	if err != nil {
		t.Fatalf("sshclient.New: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nc, err := conn.Dial(ctx, connect.DialParams{
		Host: host,
		Port: port,
		User: "test",
		Auth: connect.PasswordAuth("hunter2"),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer nc.Close()

	sftpWrapper, err := sftpclient.Open(conn.RawClient())
	if err != nil {
		t.Fatalf("sftpclient.Open: %v", err)
	}
	defer sftpWrapper.Close()

	const testFile = "/v053-overwrite.txt"
	t.Cleanup(func() { _ = sftpWrapper.Remove(testFile) })

	// 第一次写：256B
	first := bytes.Repeat([]byte("A"), 256)
	if n, err := sftpWrapper.Write(testFile, first); err != nil || n != len(first) {
		t.Fatalf("first Write: n=%d, err=%v", n, err)
	}

	// 第二次写：1024B（覆盖）
	second := bytes.Repeat([]byte("B"), 1024)
	if n, err := sftpWrapper.Write(testFile, second); err != nil || n != len(second) {
		t.Fatalf("second Write: n=%d, err=%v", n, err)
	}

	// 读回：必须 1024B 全是 'B'，不是 1280B 拼接
	rawSftp, err := sftp.NewClient(conn.RawClient())
	if err != nil {
		t.Fatalf("sftp.NewClient: %v", err)
	}
	defer rawSftp.Close()

	rf, err := rawSftp.OpenFile(testFile, os.O_RDONLY)
	if err != nil {
		t.Fatalf("OpenFile(read) %q: %v", testFile, err)
	}
	got, err := io.ReadAll(rf)
	_ = rf.Close()
	if err != nil {
		t.Fatalf("io.ReadAll: %v", err)
	}
	if len(got) != len(second) {
		t.Fatalf("readback size = %d, want %d (覆盖写应该只保留第二次内容)", len(got), len(second))
	}
	for i := range second {
		if got[i] != 'B' {
			t.Errorf("readback byte %d = %d, want 'B' (=%d)", i, got[i], 'B')
			break
		}
	}
}

// TestSftpClient_UploadFile_Integration 验证 UploadFile 本地文件 → 远端
// 分片上传。
//
//  1. 准备 256 KiB 本地文件 (内容 = 循环字节)
//  2. UploadFile 走默认 64 KiB chunkSize → 期望进度回调至少 4 次
//     (256K / 64K = 4)，最后一次 total == 文件大小
//  3. 远端读回 → 字节级比对
func TestSftpClient_UploadFile_Integration(t *testing.T) {
	server := newSSHIntegrationServer(t)
	host, port := server.hostAndPort(t)

	conn, err := New(newTestDeps())
	if err != nil {
		t.Fatalf("sshclient.New: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nc, err := conn.Dial(ctx, connect.DialParams{
		Host: host,
		Port: port,
		User: "test",
		Auth: connect.PasswordAuth("hunter2"),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer nc.Close()

	sftpWrapper, err := sftpclient.Open(conn.RawClient())
	if err != nil {
		t.Fatalf("sftpclient.Open: %v", err)
	}
	defer sftpWrapper.Close()

	// 1. 准备本地文件
	localPath := filepath.Join(t.TempDir(), "upload.bin")
	const fileSize = 256 * 1024 // 256 KiB → 默认 64 KiB chunk = 4 chunks
	payload := make([]byte, fileSize)
	for i := range payload {
		payload[i] = byte((i * 7) & 0xff) // 伪随机可识别
	}
	if err := os.WriteFile(localPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile local: %v", err)
	}

	// 2. UploadFile + 进度回调
	const testFile = "/v053-upload.bin"
	t.Cleanup(func() { _ = sftpWrapper.Remove(testFile) })

	var progressCalls int
	var lastReported int64
	progress := func(written int64) error {
		progressCalls++
		if written < lastReported {
			t.Errorf("progress went backwards: prev=%d, now=%d", lastReported, written)
		}
		lastReported = written
		return nil
	}

	if err := sftpWrapper.UploadFile(localPath, testFile, 0, progress); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	// 进度回调至少 4 次（256K / 64K = 4；最后 partial chunk 也算）
	if progressCalls < 4 {
		t.Errorf("progress calls = %d, want >= 4 (256 KiB / 64 KiB chunk)", progressCalls)
	}
	// 最后一次进度必须等于文件大小
	if lastReported != int64(fileSize) {
		t.Errorf("last progress = %d, want %d", lastReported, fileSize)
	}

	// 3. 远端读回
	rawSftp, err := sftp.NewClient(conn.RawClient())
	if err != nil {
		t.Fatalf("sftp.NewClient: %v", err)
	}
	defer rawSftp.Close()

	rf, err := rawSftp.OpenFile(testFile, os.O_RDONLY)
	if err != nil {
		t.Fatalf("OpenFile(read) %q: %v", testFile, err)
	}
	got, err := io.ReadAll(rf)
	_ = rf.Close()
	if err != nil {
		t.Fatalf("io.ReadAll: %v", err)
	}
	if len(got) != fileSize {
		t.Fatalf("uploaded file size = %d, want %d", len(got), fileSize)
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Errorf("uploaded byte %d = %d, want %d", i, got[i], payload[i])
			break
		}
	}
}

// -----------------------------------------------------------------------------
// v0.5.3 — sftpclient.Client.List 真实分页（客户端分页协议）
// -----------------------------------------------------------------------------

// TestSftpClient_List_Pagination 端到端验证 v0.5.3 客户端分页。
//
// 流程：
//  1. 在 InMemFS 上建 25 个 file（file-00 .. file-24）
//  2. sftpclient.Client.List(path, 10, "") → 10 entries + token1
//  3. sftpclient.Client.List(path, 10, token1) → 10 entries + token2
//  4. sftpclient.Client.List(path, 10, token2) → 5 entries + token=""（最后一页）
//  5. 验证：
//     - 三页共 25 个 entries，无重复无缺失
//     - 第三页的 NextToken 为空（无更多）
//     - entries 名字连续（拼接起来 == 期望的 25 个 file 名集合）
//  6. 额外：用 token1 调 path 不同的目录 → error
//  7. 额外：用乱码 token → error
//
// 设计要点：
//   - 复用 sshIntegrationServer + InMemHandler（v0.5.2 写的）
//   - 走真 sftpclient.Client.List（不是 sftp.ReadDir）—— 是本测试的核心价值
//   - file 数 25 = 不整除 10：3 页 = 10+10+5，校验最后一页 partial + next token 为空
func TestSftpClient_List_Pagination(t *testing.T) {
	server := newSSHIntegrationServer(t)
	host, port := server.hostAndPort(t)

	conn, err := New(newTestDeps())
	if err != nil {
		t.Fatalf("sshclient.New: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nc, err := conn.Dial(ctx, connect.DialParams{
		Host: host,
		Port: port,
		User: "test",
		Auth: connect.PasswordAuth("hunter2"),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer nc.Close()

	sftpWrapper, err := sftpclient.Open(conn.RawClient())
	if err != nil {
		t.Fatalf("sftpclient.Open: %v", err)
	}
	defer sftpWrapper.Close()

	const testDir = "/v053-paged"
	const total = 25
	const pageSize = 10

	// 1. 准备 25 个 file
	if err := sftpWrapper.Mkdir(testDir); err != nil {
		t.Fatalf("Mkdir(%q): %v", testDir, err)
	}
	t.Cleanup(func() { _ = sftpWrapper.Remove(testDir) })

	// 用 sftp.NewClient 写文件（sftpclient.Client 没有 Write ... 等下，v0.5.3 有了）
	expectedNames := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		name := "file-" + pad2(i) // file-00 .. file-24
		expectedNames[name] = true
		// 直接用 sftpclient.Client.Write 写（小文件分页测试不需要 UploadFile）
		if _, err := sftpWrapper.Write(testDir+"/"+name, []byte(name)); err != nil {
			t.Fatalf("Write %q: %v", name, err)
		}
	}

	// 2. page 1
	page1, err := sftpWrapper.List(ctx, testDir, pageSize, "")
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(page1.Entries) != pageSize {
		t.Errorf("page1 size = %d, want %d", len(page1.Entries), pageSize)
	}
	if page1.NextToken == "" {
		t.Error("page1 NextToken is empty, want non-empty (还有更多)")
	}
	seen := make(map[string]bool, total)
	for _, e := range page1.Entries {
		seen[e.Name] = true
	}

	// 3. page 2
	page2, err := sftpWrapper.List(ctx, testDir, pageSize, page1.NextToken)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2.Entries) != pageSize {
		t.Errorf("page2 size = %d, want %d", len(page2.Entries), pageSize)
	}
	if page2.NextToken == "" {
		t.Error("page2 NextToken is empty, want non-empty (还有最后一页)")
	}
	for _, e := range page2.Entries {
		if seen[e.Name] {
			t.Errorf("page2 entry %q already seen in page1 (overlap)", e.Name)
		}
		seen[e.Name] = true
	}

	// 4. page 3（最后一页，partial）
	page3, err := sftpWrapper.List(ctx, testDir, pageSize, page2.NextToken)
	if err != nil {
		t.Fatalf("List page3: %v", err)
	}
	const expectedLastPage = total - 2*pageSize // 25 - 20 = 5
	if len(page3.Entries) != expectedLastPage {
		t.Errorf("page3 size = %d, want %d", len(page3.Entries), expectedLastPage)
	}
	if page3.NextToken != "" {
		t.Errorf("page3 NextToken = %q, want empty (no more pages)", page3.NextToken)
	}
	for _, e := range page3.Entries {
		if seen[e.Name] {
			t.Errorf("page3 entry %q already seen in earlier page (overlap)", e.Name)
		}
		seen[e.Name] = true
	}

	// 5. 验证三页合并 == 25 个 expected names（无重复无缺失）
	if len(seen) != total {
		t.Errorf("total unique names = %d, want %d (missing or extra)", len(seen), total)
	}
	for name := range expectedNames {
		if !seen[name] {
			t.Errorf("expected name %q not in any page", name)
		}
	}

	// 6. token 跨路径：page1.NextToken 是 path testDir 的，调 path testDir+"/other" 必须 error
	//    先建 /v053-paged/other 目录
	otherDir := testDir + "/other"
	if err := sftpWrapper.Mkdir(otherDir); err != nil {
		t.Fatalf("Mkdir(%q): %v", otherDir, err)
	}
	if _, err := sftpWrapper.List(ctx, otherDir, pageSize, page1.NextToken); err == nil {
		t.Error("List with token from different path: expected error, got nil")
	}

	// 7. 乱码 token 必须 error
	if _, err := sftpWrapper.List(ctx, testDir, pageSize, "this-is-not-a-valid-token!!!"); err == nil {
		t.Error("List with garbage token: expected error, got nil")
	}
}

// TestSftpClient_List_PageSizeZeroAndLarge 覆盖 pageSize 边界：
//   - pageSize=0 → 用 defaultPageSize (200)
//   - pageSize > maxPageSize (1000) → 截断到 1000
func TestSftpClient_List_PageSizeZeroAndLarge(t *testing.T) {
	server := newSSHIntegrationServer(t)
	host, port := server.hostAndPort(t)

	conn, err := New(newTestDeps())
	if err != nil {
		t.Fatalf("sshclient.New: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nc, err := conn.Dial(ctx, connect.DialParams{
		Host: host,
		Port: port,
		User: "test",
		Auth: connect.PasswordAuth("hunter2"),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer nc.Close()

	sftpWrapper, err := sftpclient.Open(conn.RawClient())
	if err != nil {
		t.Fatalf("sftpclient.Open: %v", err)
	}
	defer sftpWrapper.Close()

	const testDir = "/v053-pagesize"
	if err := sftpWrapper.Mkdir(testDir); err != nil {
		t.Fatalf("Mkdir(%q): %v", testDir, err)
	}
	t.Cleanup(func() { _ = sftpWrapper.Remove(testDir) })

	// 3 个 file
	for i := 0; i < 3; i++ {
		name := "f-" + pad2(i)
		if _, err := sftpWrapper.Write(testDir+"/"+name, []byte(name)); err != nil {
			t.Fatalf("Write %q: %v", name, err)
		}
	}

	// pageSize=0：应当用 default (200)，3 个 entries 一次拿完，token=""
	page, err := sftpWrapper.List(ctx, testDir, 0, "")
	if err != nil {
		t.Fatalf("List pageSize=0: %v", err)
	}
	if len(page.Entries) != 3 {
		t.Errorf("pageSize=0: size = %d, want 3 (default 200 > 3 entries)", len(page.Entries))
	}
	if page.NextToken != "" {
		t.Errorf("pageSize=0: NextToken = %q, want empty (all in one page)", page.NextToken)
	}

	// pageSize=99999：截断到 1000，3 entries 一次拿完
	page, err = sftpWrapper.List(ctx, testDir, 99999, "")
	if err != nil {
		t.Fatalf("List pageSize=99999: %v", err)
	}
	if len(page.Entries) != 3 {
		t.Errorf("pageSize=99999: size = %d, want 3 (truncated to 1000)", len(page.Entries))
	}
	if page.NextToken != "" {
		t.Errorf("pageSize=99999: NextToken = %q, want empty", page.NextToken)
	}
}

// TestSftpClient_List_WithPageSizeOption 验证 WithPageSize option 真的把
// defaultPageSize 改了。
//
// 1. Open(... WithPageSize(50))
// 2. List(pageSize=0) → 应当用 50 作为 pageSize，不是默认 200
//   - 用 30 个 entries 验证：page 1 = 30 个一次拿完（30 < 50），token=""
func TestSftpClient_List_WithPageSizeOption(t *testing.T) {
	server := newSSHIntegrationServer(t)
	host, port := server.hostAndPort(t)

	conn, err := New(newTestDeps())
	if err != nil {
		t.Fatalf("sshclient.New: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nc, err := conn.Dial(ctx, connect.DialParams{
		Host: host,
		Port: port,
		User: "test",
		Auth: connect.PasswordAuth("hunter2"),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer nc.Close()

	// 关键：注入 WithPageSize(50)
	sftpWrapper, err := sftpclient.Open(conn.RawClient(), sftpclient.WithPageSize(50))
	if err != nil {
		t.Fatalf("sftpclient.Open with WithPageSize(50): %v", err)
	}
	defer sftpWrapper.Close()

	const testDir = "/v053-withpagesize"
	if err := sftpWrapper.Mkdir(testDir); err != nil {
		t.Fatalf("Mkdir(%q): %v", testDir, err)
	}
	t.Cleanup(func() { _ = sftpWrapper.Remove(testDir) })

	// 写 30 个 file（30 > 50 ? 不，30 < 50，所以 1 页拿完）—— 改成 60
	const total = 60
	for i := 0; i < total; i++ {
		name := "x-" + pad2(i)
		if _, err := sftpWrapper.Write(testDir+"/"+name, []byte(name)); err != nil {
			t.Fatalf("Write %q: %v", name, err)
		}
	}

	// List(pageSize=0) → 应当用 50：60 entries 拆 50+10 → page1=50, page2=10
	page1, err := sftpWrapper.List(ctx, testDir, 0, "")
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(page1.Entries) != 50 {
		t.Errorf("WithPageSize(50) + pageSize=0: page1 size = %d, want 50 (证明 defaultPageSize 真的改了)", len(page1.Entries))
	}
	if page1.NextToken == "" {
		t.Error("page1 NextToken is empty, want non-empty (60 > 50)")
	}
	page2, err := sftpWrapper.List(ctx, testDir, 0, page1.NextToken)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2.Entries) != 10 {
		t.Errorf("page2 size = %d, want 10", len(page2.Entries))
	}
	if page2.NextToken != "" {
		t.Errorf("page2 NextToken = %q, want empty", page2.NextToken)
	}
}

// pad2 把 i 补零到 2 位（testDir/file-00 .. file-24）。
func pad2(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
