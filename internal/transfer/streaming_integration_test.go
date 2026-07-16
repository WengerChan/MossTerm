// streaming_integration_test.go 覆盖 streaming upload 的端到端：
//   - 真实 SSH server（in-process，复用 v0.5.3 sftpUploadSSHServer 模式）
//   - 真实 *sftp.Client（走 transfer.Uploader → sftpclient.Client.Open）
//   - 上传 100MB+ 文件 + 字节级比对
//   - 验证并发 WriteAt 不破坏文件
//   - 验证 cancel + resume
//
// v0.5.3 的 sftpUploadSSHServer（internal/ui/wailsbindings/sftp_upload_test.go）
// 跑通完整 happy path；本文件复刻 server 结构（in-process SSH + SFTP subsystem
// + sftp.InMemHandler 共享 FS）+ 客户端构造，避免重新发明轮子。
package transfer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/mossterm/mossterm/internal/sftpclient"
	"github.com/mossterm/mossterm/internal/testutil/sftpd"
)

// -----------------------------------------------------------------------------
// 桩：in-process SSH server（精简自 v0.5.3 sftp_upload_test.go）
// -----------------------------------------------------------------------------

type streamingSFTPTestServer struct {
	listener     net.Listener
	serverCfg    *ssh.ServerConfig
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	closeOnce    sync.Once
	sftpHandlers sftp.Handlers
}

func newStreamingSFTPTestServer(t *testing.T) *streamingSFTPTestServer {
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
	s := &streamingSFTPTestServer{
		listener:     l,
		serverCfg:    serverCfg,
		ctx:          ctx,
		cancel:       cancel,
		sftpHandlers: sftp.InMemHandler(),
	}
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

func (s *streamingSFTPTestServer) acceptLoop() {
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

func (s *streamingSFTPTestServer) handleConn(c net.Conn) {
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

func (s *streamingSFTPTestServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request, connWg *sync.WaitGroup) {
	defer ch.Close()
	for req := range requests {
		switch req.Type {
		case "subsystem":
			name := sftpSubsystemName(req.Payload)
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
			if req.WantReply {
				req.Reply(true, nil)
			}
		}
	}
}

func sftpSubsystemName(payload []byte) string {
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

func (s *streamingSFTPTestServer) serveSFTP(ch ssh.Channel) {
	rs := sftp.NewRequestServer(ch, s.sftpHandlers)
	_ = rs.Serve()
	_ = rs.Close()
}

func (s *streamingSFTPTestServer) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.listener.Close()
		s.wg.Wait()
	})
}

func (s *streamingSFTPTestServer) hostPort(t *testing.T) (string, int) {
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
// 桩：构造 *sftpclient.Client
// -----------------------------------------------------------------------------

func newSFTPClientForTest(t *testing.T, server *streamingSFTPTestServer) *sftpclient.Client {
	t.Helper()
	host, port := server.hostPort(t)
	clientCfg := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("x")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	sshClient, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		t.Fatalf("ssh.Dial: %v", err)
	}
	t.Cleanup(func() { _ = sshClient.Close() })

	cli, err := sftpclient.Open(sshClient)
	if err != nil {
		t.Fatalf("sftpclient.Open: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func writeRandomFile(t *testing.T, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	path := t.TempDir() + "/src.bin"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path, data
}

// readRemoteFile 用**独立** ssh connection 读回（避免读写抢同一 SFTP channel）。
//
// 写完显式关原 client → dial 新 SSH conn → 开新 SFTP client → 读。
func readRemoteFile(t *testing.T, server *streamingSFTPTestServer, path string) []byte {
	t.Helper()
	cli := newSFTPClientForTest(t, server)
	f, err := cli.Open(path, os.O_RDONLY)
	if err != nil {
		t.Fatalf("open remote %s: %v", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return data
}

// -----------------------------------------------------------------------------
// 集成测试
// -----------------------------------------------------------------------------

// TestUpload_Integration_SFTPSmoke 最小化 SFTP 端到端：
// 1MB 文件走 transfer.Upload → 字节级比对。
func TestUpload_Integration_SFTPSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in short mode")
	}
	const size = 1 * 1024 * 1024 // 1 MiB（最小可测的"分片"size）

	server := newStreamingSFTPTestServer(t)
	cli := newSFTPClientForTest(t, server)
	manifestDir := t.TempDir()

	localPath, srcData := writeRandomFile(t, size)
	remotePath := "/smoke.bin"

	req := UploadRequest{
		TransferID:  "tx-smoke",
		LocalPath:   localPath,
		RemotePath:  remotePath,
		ChunkSize:   1 * 1024 * 1024, // 1 MiB chunk（最小合法值）
		Concurrency: 2,
	}
	if err := Upload(context.Background(), cli, req, manifestDir, nil); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	// 关 client 让 server 端 SFTP RequestServer 退出（避免在 read 新 client 时死锁）
	_ = cli.Close()

	got := readRemoteFile(t, server, remotePath)
	if len(got) != size {
		t.Errorf("size: got %d, want %d", len(got), size)
	}
	if !bytes.Equal(got, srcData) {
		t.Errorf("content mismatch")
	}

	// SHA-256 验证
	gh := sha256.Sum256(got)
	sh := sha256.Sum256(srcData)
	if hex.EncodeToString(gh[:]) != hex.EncodeToString(sh[:]) {
		t.Errorf("SHA-256 mismatch")
	}
}

// TestUpload_Integration_50MB 跑 50 MiB（v0.5.10 spec 要求 100MB+；50MB
// 作为 CI 友好的中位档位；100MB 单独跑 + race detector 走手动）。
func TestUpload_Integration_50MB(t *testing.T) {
	if testing.Short() {
		t.Skip("skip 50MB in short mode")
	}
	const size = 50 * 1024 * 1024

	server := newStreamingSFTPTestServer(t)
	cli := newSFTPClientForTest(t, server)
	manifestDir := t.TempDir()

	localPath, srcData := writeRandomFile(t, size)
	remotePath := "/big50.bin"

	req := UploadRequest{
		TransferID:  "tx-50mb",
		LocalPath:   localPath,
		RemotePath:  remotePath,
		ChunkSize:   4 * 1024 * 1024,
		Concurrency: 2,
	}
	if err := Upload(context.Background(), cli, req, manifestDir, nil); err != nil {
		t.Fatalf("Upload 50MB: %v", err)
	}
	_ = cli.Close()

	got := readRemoteFile(t, server, remotePath)
	if len(got) != size {
		t.Errorf("size: got %d, want %d", len(got), size)
	}
	if !bytes.Equal(got, srcData) {
		t.Errorf("content mismatch (50MB)")
	}
	gh := sha256.Sum256(got)
	sh := sha256.Sum256(srcData)
	if hex.EncodeToString(gh[:]) != hex.EncodeToString(sh[:]) {
		t.Errorf("SHA-256 mismatch (50MB)")
	}
}

// TestUpload_Integration_100MB 跑 100 MiB（v0.5.10 spec 硬要求）。
// 不在 -short 跑；CI 跑 -short 跳过这个，full mode 才跑。
func TestUpload_Integration_100MB(t *testing.T) {
	if testing.Short() {
		t.Skip("skip 100MB in short mode (run with: go test -count=1 ./internal/transfer/...)")
	}
	const size = 100 * 1024 * 1024

	server := newStreamingSFTPTestServer(t)
	cli := newSFTPClientForTest(t, server)
	manifestDir := t.TempDir()

	localPath, srcData := writeRandomFile(t, size)
	remotePath := "/big100.bin"

	req := UploadRequest{
		TransferID:  "tx-100mb",
		LocalPath:   localPath,
		RemotePath:  remotePath,
		ChunkSize:   4 * 1024 * 1024,
		Concurrency: 4, // 4 路并发
	}
	if err := Upload(context.Background(), cli, req, manifestDir, nil); err != nil {
		t.Fatalf("Upload 100MB: %v", err)
	}
	_ = cli.Close()

	got := readRemoteFile(t, server, remotePath)
	if len(got) != size {
		t.Errorf("size: got %d, want %d", len(got), size)
	}
	if !bytes.Equal(got, srcData) {
		t.Errorf("content mismatch (100MB)")
	}
	gh := sha256.Sum256(got)
	sh := sha256.Sum256(srcData)
	if hex.EncodeToString(gh[:]) != hex.EncodeToString(sh[:]) {
		t.Errorf("SHA-256 mismatch (100MB)")
	}
}

// TestUpload_Integration_ConcurrentOrdering 验证并发 WriteAt 各写到
// 自己 offset 区间（不重叠），最终字节哈希等于源文件。
func TestUpload_Integration_ConcurrentOrdering(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in short mode")
	}
	const size = 8 * 1024 * 1024 // 8 MiB / 1 MiB chunk = 8 chunks

	server := newStreamingSFTPTestServer(t)
	cli := newSFTPClientForTest(t, server)
	manifestDir := t.TempDir()

	localPath, srcData := writeRandomFile(t, size)
	remotePath := "/concurrent.bin"

	req := UploadRequest{
		TransferID:  "tx-conc",
		LocalPath:   localPath,
		RemotePath:  remotePath,
		ChunkSize:   1 * 1024 * 1024,
		Concurrency: 4, // 4 路并发验证
	}
	if err := Upload(context.Background(), cli, req, manifestDir, nil); err != nil {
		t.Fatalf("Upload concurrent: %v", err)
	}
	_ = cli.Close()

	got := readRemoteFile(t, server, remotePath)
	if !bytes.Equal(got, srcData) {
		t.Fatalf("concurrent WriteAt produced wrong content")
	}
}

// TestUpload_Integration_Resume 中断后 resume 续传验证最终字节完整。
func TestUpload_Integration_Resume(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in short mode")
	}
	const size = 32 * 1024 * 1024

	server := newStreamingSFTPTestServer(t)
	cli := newSFTPClientForTest(t, server)
	manifestDir := t.TempDir()

	localPath, srcData := writeRandomFile(t, size)
	remotePath := "/resume.bin"

	// 第一次：1ms 后 cancel
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(1 * time.Millisecond)
		cancel()
	}()
	req := UploadRequest{
		TransferID:  "tx-resume",
		LocalPath:   localPath,
		RemotePath:  remotePath,
		ChunkSize:   4 * 1024 * 1024,
		Concurrency: 2,
	}
	_ = Upload(ctx, cli, req, manifestDir, nil)
	// 失败或成功都行；只看 manifest 是否带 uploadedChunks

	m, err := LoadManifest(manifestDir, "tx-resume")
	if err != nil || m == nil || len(m.UploadedChunks) == 0 {
		chunks := -1
		if m != nil {
			chunks = len(m.UploadedChunks)
		}
		t.Skipf("cancel 太早无 manifest 续传点（err=%v, chunks=%d）", err, chunks)
	}
	t.Logf("partial upload: %d chunks done, resuming", len(m.UploadedChunks))
	_ = cli.Close()

	// 重新开 client + resume
	cli2 := newSFTPClientForTest(t, server)
	req2 := UploadRequest{
		TransferID:  "tx-resume",
		LocalPath:   localPath,
		RemotePath:  remotePath,
		ChunkSize:   4 * 1024 * 1024,
		Concurrency: 2,
		Resume:      true,
	}
	if err := Upload(context.Background(), cli2, req2, manifestDir, nil); err != nil {
		t.Fatalf("Resume Upload: %v", err)
	}
	_ = cli2.Close()

	got := readRemoteFile(t, server, remotePath)
	if !bytes.Equal(got, srcData) {
		t.Errorf("after resume: content mismatch")
	}
	gh := sha256.Sum256(got)
	sh := sha256.Sum256(srcData)
	if hex.EncodeToString(gh[:]) != hex.EncodeToString(sh[:]) {
		t.Errorf("SHA-256 mismatch after resume")
	}
}

// -----------------------------------------------------------------------------
// v0.6.0 Download 集成测试
// -----------------------------------------------------------------------------
//
// 与 upload 集成测试同结构：复用 streamingSFTPTestServer +
// newSFTPClientForTest。差异：
//   - 先用普通方式"上传"（sftpclient.Client.Write）把数据写到远端
//   - 然后用 transfer.Download 拉回本地 → 字节级比对
//   - 关原 client + dial 新 client 避免读写抢同一 SFTP channel
//
// 不开 100MB 下载测试（与 upload 50MB 同档位；download 100MB 主要
// 测的是 network/IO 而不是 streaming 逻辑）。

// seedRemote 写"远端"文件内容。
//
// v0.6.0 集成测试用：直接走 cli.Write（不走 transfer.Upload）；
// download 测试需要先有远端数据。
func seedRemoteData(t *testing.T, cli *sftpclient.Client, path string, data []byte) {
	t.Helper()
	if _, err := cli.Write(path, data); err != nil {
		t.Fatalf("seed remote: %v", err)
	}
}

// TestDownload_Integration_SFTPSmoke 最小化下载端到端：1 MiB 远端数据
// → 本地 → 字节级比对 + SHA-256 验证。
func TestDownload_Integration_SFTPSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in short mode")
	}
	const size = 1 * 1024 * 1024 // 1 MiB

	server := newStreamingSFTPTestServer(t)
	cli := newSFTPClientForTest(t, server)
	manifestDir := t.TempDir()

	// seed 远端
	srcData := make([]byte, size)
	if _, err := rand.Read(srcData); err != nil {
		t.Fatalf("rand: %v", err)
	}
	remotePath := "/dl-smoke.bin"
	seedRemoteData(t, cli, remotePath, srcData)
	// 关 client 让 server 端 SFTP RequestServer 退出（避免在 download 新 client 时死锁）
	_ = cli.Close()

	// 用新 client 下载
	cli2 := newSFTPClientForTest(t, server)
	localPath := t.TempDir() + "/dl-smoke.bin"
	req := DownloadRequest{
		TransferID:  "tx-d-smoke",
		RemotePath:  remotePath,
		LocalPath:   localPath,
		ChunkSize:   1 * 1024 * 1024,
		Concurrency: 2,
	}
	if err := Download(context.Background(), cli2, req, manifestDir, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	_ = cli2.Close()

	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != size {
		t.Errorf("size: got %d, want %d", len(got), size)
	}
	if !bytes.Equal(got, srcData) {
		t.Errorf("content mismatch")
	}
	gh := sha256.Sum256(got)
	sh := sha256.Sum256(srcData)
	if hex.EncodeToString(gh[:]) != hex.EncodeToString(sh[:]) {
		t.Errorf("SHA-256 mismatch")
	}

	// 成功完成后 manifest 应被删除
	if _, err := LoadManifest(manifestDir, "tx-d-smoke"); !errors.Is(err, ErrManifestNotFound) {
		t.Errorf("manifest after success: got err=%v, want ErrManifestNotFound", err)
	}
}

// TestDownload_Integration_50MB 跑 50 MiB（与 upload 50MB 同档位）。
func TestDownload_Integration_50MB(t *testing.T) {
	if testing.Short() {
		t.Skip("skip 50MB in short mode")
	}
	const size = 50 * 1024 * 1024

	server := newStreamingSFTPTestServer(t)
	cli := newSFTPClientForTest(t, server)
	manifestDir := t.TempDir()

	srcData := make([]byte, size)
	if _, err := rand.Read(srcData); err != nil {
		t.Fatalf("rand: %v", err)
	}
	remotePath := "/dl-big50.bin"
	seedRemoteData(t, cli, remotePath, srcData)
	_ = cli.Close()

	cli2 := newSFTPClientForTest(t, server)
	localPath := t.TempDir() + "/dl-big50.bin"
	req := DownloadRequest{
		TransferID:  "tx-d-50mb",
		RemotePath:  remotePath,
		LocalPath:   localPath,
		ChunkSize:   4 * 1024 * 1024,
		Concurrency: 2,
	}
	if err := Download(context.Background(), cli2, req, manifestDir, nil); err != nil {
		t.Fatalf("Download 50MB: %v", err)
	}
	_ = cli2.Close()

	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != size {
		t.Errorf("size: got %d, want %d", len(got), size)
	}
	if !bytes.Equal(got, srcData) {
		t.Errorf("content mismatch (50MB)")
	}
	gh := sha256.Sum256(got)
	sh := sha256.Sum256(srcData)
	if hex.EncodeToString(gh[:]) != hex.EncodeToString(sh[:]) {
		t.Errorf("SHA-256 mismatch (50MB)")
	}
}

// TestDownload_Integration_ConcurrentOrdering 验证并发 ReadAt 各读
// 自己 offset 区间（不重叠），最终字节哈希等于源文件。
func TestDownload_Integration_ConcurrentOrdering(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in short mode")
	}
	const size = 8 * 1024 * 1024

	server := newStreamingSFTPTestServer(t)
	cli := newSFTPClientForTest(t, server)
	manifestDir := t.TempDir()

	srcData := make([]byte, size)
	if _, err := rand.Read(srcData); err != nil {
		t.Fatalf("rand: %v", err)
	}
	remotePath := "/dl-concurrent.bin"
	seedRemoteData(t, cli, remotePath, srcData)
	_ = cli.Close()

	cli2 := newSFTPClientForTest(t, server)
	localPath := t.TempDir() + "/dl-concurrent.bin"
	req := DownloadRequest{
		TransferID:  "tx-d-conc",
		RemotePath:  remotePath,
		LocalPath:   localPath,
		ChunkSize:   1 * 1024 * 1024,
		Concurrency: 4, // 4 路并发
	}
	if err := Download(context.Background(), cli2, req, manifestDir, nil); err != nil {
		t.Fatalf("Download concurrent: %v", err)
	}
	_ = cli2.Close()

	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, srcData) {
		t.Fatalf("concurrent ReadAt produced wrong content")
	}
}

// TestDownload_Integration_Resume 中断后 resume 续传验证最终字节完整。
func TestDownload_Integration_Resume(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in short mode")
	}
	const size = 32 * 1024 * 1024

	server := newStreamingSFTPTestServer(t)
	cli := newSFTPClientForTest(t, server)
	manifestDir := t.TempDir()

	srcData := make([]byte, size)
	if _, err := rand.Read(srcData); err != nil {
		t.Fatalf("rand: %v", err)
	}
	remotePath := "/dl-resume.bin"
	seedRemoteData(t, cli, remotePath, srcData)
	_ = cli.Close()

	localPath := t.TempDir() + "/dl-resume.bin"
	// 第一次：1ms 后 cancel
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(1 * time.Millisecond)
		cancel()
	}()
	cli2 := newSFTPClientForTest(t, server)
	req := DownloadRequest{
		TransferID:  "tx-d-resume",
		RemotePath:  remotePath,
		LocalPath:   localPath,
		ChunkSize:   4 * 1024 * 1024,
		Concurrency: 2,
	}
	_ = Download(ctx, cli2, req, manifestDir, nil)
	_ = cli2.Close()

	m, err := LoadManifest(manifestDir, "tx-d-resume")
	if err != nil || m == nil || len(m.UploadedChunks) == 0 {
		chunks := -1
		if m != nil {
			chunks = len(m.UploadedChunks)
		}
		t.Skipf("cancel too early no manifest resume point (err=%v, chunks=%d)", err, chunks)
	}
	t.Logf("partial download: %d chunks done, resuming", len(m.UploadedChunks))

	// 重新开 client + resume
	cli3 := newSFTPClientForTest(t, server)
	req2 := DownloadRequest{
		TransferID:  "tx-d-resume",
		RemotePath:  remotePath,
		LocalPath:   localPath,
		ChunkSize:   4 * 1024 * 1024,
		Concurrency: 2,
		Resume:      true,
	}
	if dlErr := Download(context.Background(), cli3, req2, manifestDir, nil); dlErr != nil {
		t.Fatalf("Resume Download: %v", dlErr)
	}
	_ = cli3.Close()

	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, srcData) {
		t.Errorf("after resume: content mismatch")
	}
	gh := sha256.Sum256(got)
	sh := sha256.Sum256(srcData)
	if hex.EncodeToString(gh[:]) != hex.EncodeToString(sh[:]) {
		t.Errorf("SHA-256 mismatch after resume")
	}
}

// -----------------------------------------------------------------------------
// v0.6.3：真实 OpenSSH sftp-server binary 集成测试
// -----------------------------------------------------------------------------
//
// 区别于本文件前 5 个测试（用 sftp.InMemHandler 走 pkg/sftp 自带 in-process
// SFTP server，覆盖"协议层"），本测试用 internal/testutil/sftpd 起真实系统
// sftp-server binary 走 stdin/stdout，覆盖"真实系统权限 / 真实 disk IO 边界
// / chdir 行为"。
//
// sftpd 内部已处理 windows（没 sftp-server binary 时 t.Skip）；此处不再写
// build tag。
//
// 路径用**相对路径**（sftpd 用 sftp-server -d 做 chdir 不 chroot，绝对路径
// 会落 WorkDir 外）。WorkDir 默认是 t.TempDir()（sftpd 自动 cleanup）。
func TestUpload_Integration_RealSFTPServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in short mode")
	}
	const size = 1 * 1024 * 1024 // 1 MiB（轻量；只验"真实 binary 跑通"，不分大文件）

	server := sftpd.Start(t, sftpd.Options{}) // sftpd 内部 t.Skip 如果 binary 不存在
	manifestDir := t.TempDir()

	// 客户端 dial 跟 streamingSFTPTestServer 那套一致；sftpd 暴露 HostPort / User / Password
	host, port := server.HostPort()
	clientCfg := &ssh.ClientConfig{
		User:            server.User(),
		Auth:            []ssh.AuthMethod{ssh.Password(server.Password())},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	sshClient, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		t.Fatalf("ssh.Dial: %v", err)
	}
	// sftpd 提示：t.Cleanup 注册顺序影响退出 LIFO。先注册 ssh 后注册 sftp，
	// 测试结束 sftp 先 Close（sftp-server 收到 stdin EOF 退出）→ ssh 后 Close。
	t.Cleanup(func() { _ = sshClient.Close() })

	cli, err := sftpclient.Open(sshClient)
	if err != nil {
		t.Fatalf("sftpclient.Open: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	// 相对路径！sftp-server 落 WorkDir
	localPath, srcData := writeRandomFile(t, size)
	remotePath := "realsftp-smoke.bin"

	req := UploadRequest{
		TransferID:  "tx-realsftp",
		LocalPath:   localPath,
		RemotePath:  remotePath,
		ChunkSize:   1 * 1024 * 1024,
		Concurrency: 2,
	}
	if err := Upload(context.Background(), cli, req, manifestDir, nil); err != nil {
		t.Fatalf("Upload via real sftp-server: %v", err)
	}
	// 关 client 让 sftp-server 进程退出（避免读时占着 SFTP channel）
	_ = cli.Close()

	// 独立新连接读回（sftpd 提示：先注册 ssh 后注册 sftp 的 Cleanup 顺序）
	host2, port2 := server.HostPort()
	addr2 := net.JoinHostPort(host2, strconv.Itoa(port2))
	sshClient2, err := ssh.Dial("tcp", addr2, clientCfg)
	if err != nil {
		t.Fatalf("ssh.Dial (read): %v", err)
	}
	t.Cleanup(func() { _ = sshClient2.Close() })
	cli2, err := sftpclient.Open(sshClient2)
	if err != nil {
		t.Fatalf("sftpclient.Open (read): %v", err)
	}
	t.Cleanup(func() { _ = cli2.Close() })

	f, err := cli2.Open(remotePath, os.O_RDONLY)
	if err != nil {
		t.Fatalf("open remote %s: %v", remotePath, err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != size {
		t.Errorf("size: got %d, want %d", len(got), size)
	}
	if !bytes.Equal(got, srcData) {
		t.Errorf("content mismatch (real sftp-server roundtrip)")
	}
	gh := sha256.Sum256(got)
	sh := sha256.Sum256(srcData)
	if hex.EncodeToString(gh[:]) != hex.EncodeToString(sh[:]) {
		t.Errorf("SHA-256 mismatch (real sftp-server roundtrip)")
	}
}
