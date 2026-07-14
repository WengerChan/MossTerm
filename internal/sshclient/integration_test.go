// integration_test.go 端到端覆盖 internal/sshclient.Connector 的
// 全链路：Dial → OpenSession → sftp.NewClient → 真 SFTP IO。
//
// 目的（v0.5.2 spec）：
//   1. 守住 v0.1 的隐 bug：OpenSession 把 StdinPipe/StdoutPipe 写在 Shell() 之后
//      （x/crypto v0.22.0 的 Session.StdinPipe 在 started==true 时返回
//      "StdinPipe after process started" 错误）。v0.5.1 已经修好；本文件
//      写一个真 SSH server 把它钉死。
//   2. 验证 Connector.Dial → OpenSession → RawClient → sftp.NewClient
//      → 真 SFTP Mkdir/Write/Read/ReadDir 端到端联通。
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
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/mossterm/mossterm/internal/connect"
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
//   1. cancel(ctx) → 所有 handleConn 收到信号，sconn.Close() → sconn.Wait() 返回
//   2. listener.Close() → acceptLoop 退出
//   3. wg.Wait() 等所有 goroutine 退出
type sshIntegrationServer struct {
	listener  net.Listener
	serverCfg *ssh.ServerConfig

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once

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
	s := &sshIntegrationServer{
		listener:  l,
		serverCfg: serverCfg,
		ctx:       ctx,
		cancel:    cancel,
		t:         t,
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

// serveSFTP 把 channel 交给 sftp.NewRequestServer + sftp.InMemHandler。
//
// InMemHandler 是 github.com/pkg/sftp request-example.go 的导出 FS：
// 全内存的 key-value 文件系统，支持 read / write / mkdir / rmdir / remove /
// rename / symlink / readlink / lstat / stat。test 足够用。
func (s *sshIntegrationServer) serveSFTP(ch ssh.Channel) {
	rs := sftp.NewRequestServer(ch, sftp.InMemHandler())
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
//   1. Dial
//   2. OpenSession
//   3. 明确断言 err == nil 且 err.Error() 不含 "StdinPipe after process started"
//   4. sess.Read + sess.Write 各一次
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
			t.Fatalf("OpenSession returned x/crypto 'StdinPipe after process started': %v\n" +
				"这意味着 StdinPipe/StdoutPipe 在 Shell() 之后被调了。\n" +
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
