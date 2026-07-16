// Package sftpd 提供 in-process SSH + 真实 OpenSSH sftp-server 的集成测试桩。
//
// v0.5 时代用 github.com/pkg/sftp.InMemHandler()（纯 Go 内存实现）做集成
// 测试，行为与真实 OpenSSH 略有差异：
//   - InMemHandler 不走真实文件系统，所以 size / mode / mtime 都是 Go 侧自填
//   - 不暴露 symlink / hardlink / rename(2) 真实行为
//   - 不覆盖 sftp-server 自身的协议边界（如 SSH_FXP_LSTAT vs STAT 差异）
//
// v0.6.3 切到 sftp-server 真 binary：用 os/exec 拉起 `/usr/libexec/sftp-server`
// （或 `/usr/lib/openssh/sftp-server`），通过 io.Copy 双向桥接 SSH channel
// 和 cmd stdin/stdout（与 v0.6.1 tunnel.local.go 同套 io.Copy 模式）。
//
// # WorkDir 的语义（重要）
//
// sftp-server 的 -d 标志**不**做 chroot，只 chdir。sftp-server 启动后：
//   - 相对路径（"foo.txt"）解析到 WorkDir
//   - 绝对路径（"/foo.txt"）走真实文件系统根（sftp-server 不会拒绝 ../ 越界）
//
// 这与 sshd 的 ChrootDirectory 指令行为不同（后者是真正的 chroot(2)），
// 但 x/crypto/ssh.ServerConfig 没暴露 ChrootDirectory，所以测试桩**只能**
// 拿到 chdir 级别的隔离。集成测试应统一用**相对路径**避免越界。
//
// 调用方（典型为 *_integration_test.go）：
//
//	srv := sftpd.Start(t, sftpd.Options{})
//	defer srv.Close()  // t.Cleanup 也已注册
//	host, port := srv.HostPort()
//	// ssh.Dial + sftpclient.Open + 用**相对路径**走 SFTP 协议
//
// windows 上 sftp-server binary 不存在 / OpenSSH 默认不带 sftp-server 子系统；
// 调用 Start 时直接 t.Skipf，让 windows runner 干净跳过（不 fail）。
package sftpd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Options 控制 sftp-server 启动参数。
type Options struct {
	// WorkDir 是 sftp-server 进程的 starting dir（OpenSSH -d 标志语义）。
	//
	// **注意**：sftp-server 的 -d 标志只 chdir 不 chroot。sftp-server 启动
	// 后 SFTP 客户端的**相对**路径（"foo.txt"）会落在 WorkDir 内；但
	// **绝对**路径（"/foo.txt"）仍走真实文件系统根，**不会**被隔离到
	// WorkDir。集成测试统一使用相对路径即可。
	// 为空时调 t.TempDir() 自动清理。
	WorkDir string

	// User 是 SSH 用户名；默认 "test"。
	User string

	// Password 是 SSH 密码；默认 "x"。
	Password string

	// ServerBinary 强制指定 sftp-server 路径；为空时按 runtime.GOOS
	// 自动选。测试专用。
	ServerBinary string

	// LogStderr 为 true 时把 sftp-server 的 stderr 转发到 t.Logf；
	// 默认 true（debug 时有用，CI 上噪音可控）。
	LogStderr *bool
}

// SFTPD 是 Start 返回的 in-process SSH + sftp-server 桩。
type SFTPD struct {
	Listener net.Listener
	// SSHConfig 是 server 端 *ssh.ServerConfig（含 host key），
	// 测试如需构造 client 用 Config 的 host key fingerprint 可拿到。
	SSHConfig *ssh.ServerConfig
	WorkDir   string
	// ServerBinary 是实际跑的 sftp-server binary 路径（debug 日志用）。
	ServerBinary string

	t        *testing.T
	user     string
	password string

	logStderr bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	closeOnce sync.Once
}

// Start 起一个 in-process SSH server + 真实 sftp-server binary。
//
// 行为：
//   - listen 127.0.0.1:0，t.Cleanup(s.Close) 已注册
//   - windows / 找不到 sftp-server 时 t.Skipf（**不 Fatal**）
//   - SSH auth：PasswordCallback，任何用户名密码都过（测试用）
//   - subsystem "sftp" → exec sftp-server -d WorkDir，bidirectional io.Copy
func Start(t *testing.T, opts Options) *SFTPD {
	t.Helper()

	if opts.ServerBinary == "" {
		bin, err := lookupServerBinary()
		if err != nil {
			t.Skipf("sftpd: %v (skipping integration test)", err)
			return nil // unreachable
		}
		opts.ServerBinary = bin
	}

	if opts.WorkDir == "" {
		opts.WorkDir = t.TempDir()
	}
	if opts.User == "" {
		opts.User = "test"
	}
	if opts.Password == "" {
		opts.Password = "x"
	}
	logStderr := true
	if opts.LogStderr != nil {
		logStderr = *opts.LogStderr
	}

	// SSH server config：ed25519 host key + PasswordCallback
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("sftpd: ed25519.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("sftpd: ssh.NewSignerFromKey: %v", err)
	}
	serverCfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	serverCfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("sftpd: net.Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &SFTPD{
		Listener:     ln,
		SSHConfig:    serverCfg,
		WorkDir:      opts.WorkDir,
		ServerBinary: opts.ServerBinary,
		t:            t,
		user:         opts.User,
		password:     opts.Password,
		logStderr:    logStderr,
		ctx:          ctx,
		cancel:       cancel,
	}
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

// HostPort 返回 server 监听的 host / port，给客户端 ssh.Dial 用。
func (s *SFTPD) HostPort() (string, int) {
	host, portStr, err := net.SplitHostPort(s.Listener.Addr().String())
	if err != nil {
		// Listener 还在；addr 必然有效；这里只是 panic guard
		panic(fmt.Sprintf("sftpd: SplitHostPort(%q): %v", s.Listener.Addr(), err))
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		panic(fmt.Sprintf("sftpd: Atoi(%q): %v", portStr, err))
	}
	return host, port
}

// User 返回 SSH 用户名。
func (s *SFTPD) User() string { return s.user }

// Password 返回 SSH 密码。
func (s *SFTPD) Password() string { return s.password }

// Close 停止 SSH listener + 取消 ctx + 等待所有 goroutine + kill 残留 sftp-server 进程。
// 幂等。
func (s *SFTPD) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		_ = s.Listener.Close()
		s.wg.Wait()
	})
}

// -----------------------------------------------------------------------------
// internal
// -----------------------------------------------------------------------------

// lookupServerBinary 按 runtime.GOOS 找 sftp-server binary。
//
// macOS 13+：/usr/libexec/sftp-server（系统自带）
// linux（Debian/Ubuntu）：/usr/lib/openssh/sftp-server
// linux（RHEL/Arch 等）：/usr/libexec/sftp-server
// windows：OpenSSH 不带 sftp-server 子系统 → 报错 → 调用方 t.Skip
func lookupServerBinary() (string, error) {
	candidates := sftpServerCandidates()
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			// x 位检查（让 chmod -x 后能跳过到下一个候选）
			if info.Mode()&0o111 != 0 {
				return p, nil
			}
		}
	}
	return "", errors.New("sftp-server binary not found in any of: " + joinComma(candidates))
}

func sftpServerCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		return nil
	case "darwin":
		return []string{"/usr/libexec/sftp-server"}
	default: // linux / 其他 unix
		return []string{
			"/usr/lib/openssh/sftp-server", // Debian / Ubuntu
			"/usr/libexec/sftp-server",     // RHEL / Arch / Alpine (edge)
		}
	}
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// acceptLoop 不断接受 conn，每条 conn 跑 handleConn。
func (s *SFTPD) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.Listener.Accept()
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

// handleConn 跑一次 SSH 握手 + 分发 session channel。
func (s *SFTPD) handleConn(c net.Conn) {
	defer c.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(c, s.SSHConfig)
	if err != nil {
		return
	}
	defer sconn.Close()

	var connWg sync.WaitGroup
	defer connWg.Wait()

	// ctx 取消时关 SSH conn（让 in-flight io.Copy 退出）
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		<-s.ctx.Done()
		sconn.Close()
	}()

	// 全局请求（keepalive 等）
	connWg.Add(1)
	go func() {
		defer connWg.Done()
		for req := range reqs {
			if req.WantReply {
				req.Reply(true, nil)
			}
		}
	}()

	// session channel 分发
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

// handleSession 处理 session 内的请求；只支持 "subsystem" + "sftp"。
func (s *SFTPD) handleSession(ch ssh.Channel, requests <-chan *ssh.Request, connWg *sync.WaitGroup) {
	defer ch.Close()
	for req := range requests {
		switch req.Type {
		case "subsystem":
			name := subsystemName(req.Payload)
			if name != "sftp" {
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
				continue
			}
			if req.WantReply {
				if err := req.Reply(true, nil); err != nil {
					// client 已关 channel：直接退出
					return
				}
			}
			// 跑 sftp-server + 双向 io.Copy
			connWg.Add(1)
			go func() {
				defer connWg.Done()
				s.serveSubsystem(ch)
			}()
		default:
			// shell / exec / env 等暂不支持
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		}
	}
}

// subsystemName 从 SSH subsystem request payload 解码子系统名。
//
// 协议格式：uint32 length + bytes（RFC 4254 §6.5）。
func subsystemName(payload []byte) string {
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

// serveSubsystem exec sftp-server + 双向 io.Copy 桥接 ssh.Channel 和 cmd。
func (s *SFTPD) serveSubsystem(ch ssh.Channel) {
	cmd := exec.CommandContext(s.ctx, s.ServerBinary, "-e", "-d", s.WorkDir)
	cmd.Dir = s.WorkDir

	// stdin/stdout = ssh.Channel（pipe 给 cmd 自己接管）
	cmd.Stdin = ch
	cmd.Stdout = ch

	// stderr 走 t.Logf（如果开启）
	if s.logStderr && s.t != nil {
		cmd.Stderr = sftpdLogWriter{t: s.t, prefix: "sftp-server"}
	} else {
		cmd.Stderr = io.Discard
	}

	if err := cmd.Start(); err != nil {
		s.t.Logf("sftpd: sftp-server Start: %v", err)
		return
	}

	// cmd.Wait 阻塞；任一端断开 → cmd 退出 → 整段 cleanup
	// （bidirectional 桥接在 exec.Command 已经把 Stdin/Stdout 双向接到 ch；
	// 这里只需等 cmd 退出，然后关 ch）
	if err := cmd.Wait(); err != nil {
		// SSH channel 关掉时 sftp-server 会被 SIGPIPE → 退出码非 0；
		// 这种是正常路径，不打 Logf（避免 CI 噪音）；如果 ctx 取消导致 kill 也跳过
		if !errors.Is(s.ctx.Err(), context.Canceled) {
			// 仅在非正常退出时记一行
			s.t.Logf("sftpd: sftp-server exited: %v", err)
		}
	}
}

// sftpdLogWriter 把 sftp-server stderr 拆行后转 t.Logf。
// 简单 io.Writer：累积一行，\n 触发 flush。
type sftpdLogWriter struct {
	t      *testing.T
	prefix string
	buf    []byte
}

func (w sftpdLogWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		idx := -1
		for i, b := range w.buf {
			if b == '\n' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		line := string(w.buf[:idx])
		w.t.Logf("%s: %s", w.prefix, line)
		// 切掉已 flush 的部分
		w.buf = append(w.buf[:0], w.buf[idx+1:]...)
	}
	return len(p), nil
}
