// Package sftpclient 在已有的 *ssh.Client 之上提供 SFTP 文件操作。
//
// 大目录强制分页（ReadDir 不行）；上传/下载走 WriteAt + 分片 + 断点续传。
//
// v0.5.0 范围：把 stub 替换为基于 github.com/pkg/sftp 的真实实现。
// 不在本版本范围：分页（pkg/sftp 的 ReadDir 不支持分页，v0.5.0 简化一次性返回）、
// 断点续传、并发安全（pkg/sftp 的 client 是 goroutine unsafe，加锁责任
// 落在调用方 / wailsbindings 层）。
package sftpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Client 包装一个 *sftp.Client 并提供 MossTerm 风格的 API。
type Client struct {
	sc        *sftp.Client
	sshClient *ssh.Client

	// pageCache 是 v0.5.3 客户端分页缓存（详见 pagecache.go）。
	// 懒构造：首次 List 时按需 put。
	// Close 时 clear。
	pageCache *pageCache

	// defaultPageSize 是 v0.5.3 WithPageSize 真正生效：覆盖 List 默认 200。
	// 0 表示用默认 200；>0 表示用 client 级默认值（pageSize 参数仍可覆盖）。
	defaultPageSize int
}

// Option 配置 Client 行为。
type Option func(*Client)

// WithPageSize 设置 List 的默认页大小（条目数）。
//
// v0.5.0 暂未实现真正的分页：List 一次性返回全量结果，本选项保留作为
// 后续 v0.5.1+ 接入分页协议时的占位。当前调用是安全的 no-op。
//
// v0.5.3 起真正生效：覆盖 List 的默认 200。List 调用时如果 pageSize <= 0
// 且 client 配了 WithPageSize，用 n 作为默认。pageSize > 0 仍以 pageSize 为准。
func WithPageSize(n int) Option {
	return func(c *Client) { c.defaultPageSize = n }
}

// Open 在已有 *ssh.Client 上打开 SFTP subsystem。
//
// opts 用于调整 Client 行为（当前仅 WithPageSize）。nil sshClient
// 直接返回错误，避免后续方法在 nil receiver 上 panic。
func Open(sshClient *ssh.Client, opts ...Option) (*Client, error) {
	if sshClient == nil {
		return nil, errors.New("sftpclient.Open: nil ssh client")
	}
	sftpCli, err := sftp.NewClient(sshClient)
	if err != nil {
		return nil, fmt.Errorf("sftpclient.Open: sftp.NewClient: %w", err)
	}
	c := &Client{
		sc:        sftpCli,
		sshClient: sshClient,
		pageCache: newPageCache(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// List 列出一个目录的分页结果（v0.5.3 客户端分页）。
//
// pageSize <= 0 用默认（client.WithPageSize 配置 > 0 用配置的，否则 200）。
// pageToken == "" 走第一次（ReadDir 全量 + 切第一页）。
// pageToken != "" 走后续（decode token → 切缓存）。
//
// 返回的 ListPage.NextToken：
//   - 还有更多页：encodeToken(path, nextOffset)
//   - 最后一页：""（空字符串）
//
// 错误：
//   - token decode 失败 / path 不匹配 / offset 越界 → fmt.Errorf("..." + ErrInvalidPageToken)
//   - ReadDir 失败 → fmt.Errorf("sftpclient.List: ReadDir %q: %w", ...)
//   - client closed → "sftpclient.List: client closed"
//
// ctx 当前未使用（SFTP 协议无 cancel 机制）；保留参数是为对齐
// 上层 ctx-aware 调用约定。
func (c *Client) List(ctx context.Context, pathArg string, pageSize int, pageToken string) (ListPage, error) {
	if c.sc == nil {
		return ListPage{}, errors.New("sftpclient.List: client closed")
	}
	_ = ctx

	if pageSize <= 0 {
		pageSize = c.defaultPageSize
		if pageSize <= 0 {
			pageSize = 200
		}
	}
	// 防御性上限：单页 > 1000 限制到 1000
	if pageSize > 1000 {
		pageSize = 1000
	}

	// 1. 解析 pageToken → 起始 offset
	offset := 0
	if pageToken != "" {
		tokPath, tokOffset, err := decodeToken(pageToken)
		if err != nil {
			return ListPage{}, fmt.Errorf("sftpclient.List: %w", err)
		}
		if tokPath != pathArg {
			return ListPage{}, fmt.Errorf("sftpclient.List: %w (token path %q != request path %q)", ErrInvalidPageToken, tokPath, pathArg)
		}
		offset = tokOffset
	}

	// 2. 拿全量 entries（cache 命中即用；miss 才 ReadDir）
	state, ok := c.pageCache.get(pathArg)
	if !ok {
		files, err := c.sc.ReadDir(pathArg)
		if err != nil {
			return ListPage{}, fmt.Errorf("sftpclient.List: ReadDir %q: %w", pathArg, err)
		}
		entries := make([]Entry, 0, len(files))
		for _, fi := range files {
			entries = append(entries, entryFromFileInfo(fi, pathArg))
		}
		state = pageState{entries: entries}
		c.pageCache.put(pathArg, state)
	}

	// 3. 切页
	page, nextOffset := slicePage(state.entries, offset, pageSize)

	// 4. 算 nextToken
	nextToken := ""
	if nextOffset >= 0 {
		nextToken = encodeToken(pathArg, nextOffset)
	}

	return ListPage{
		Entries:   page,
		NextToken: nextToken,
	}, nil
}

// Stat 返回一个文件或目录的元数据。
func (c *Client) Stat(p string) (Entry, error) {
	if c.sc == nil {
		return Entry{}, errors.New("sftpclient.Stat: client closed")
	}
	fi, err := c.sc.Stat(p)
	if err != nil {
		return Entry{}, fmt.Errorf("sftpclient.Stat: %q: %w", p, err)
	}
	return entryFromFileInfo(fi, path.Dir(p)), nil
}

// ReadDir 一次性读取整个目录（仅用于小目录）。
//
// 大目录必须使用 List 分页（v0.5.0 暂未实现分页；v0.5.1+ 起 List 会真正分页）。
func (c *Client) ReadDir(p string) ([]Entry, error) {
	if c.sc == nil {
		return nil, errors.New("sftpclient.ReadDir: client closed")
	}
	entries, err := c.sc.ReadDir(p)
	if err != nil {
		return nil, fmt.Errorf("sftpclient.ReadDir: %q: %w", p, err)
	}
	out := make([]Entry, 0, len(entries))
	for _, fi := range entries {
		out = append(out, entryFromFileInfo(fi, p))
	}
	return out, nil
}

// Open 打开远端文件用于读写。
//
// flags 是 os.O_RDONLY / O_WRONLY / O_RDWR / O_CREATE / O_TRUNC / O_APPEND 的组合。
//
// 返回的 ReadWriteCloser 实际是 *sftp.File，调用方用完必须 Close。
// *sftp.File 内部实现 Read/Write/WriteAt/ReadAt 等，可直接用于断点续传。
func (c *Client) Open(p string, flags int) (ReadWriteCloser, error) {
	if c.sc == nil {
		return nil, errors.New("sftpclient.Open: client closed")
	}
	f, err := c.sc.OpenFile(p, flags)
	if err != nil {
		return nil, fmt.Errorf("sftpclient.Open: OpenFile %q: %w", p, err)
	}
	return f, nil
}

// Mkdir 在远端创建目录（单层）。
//
// 如需递归创建，调用方应在 sshclient 层提供 MkdirAll 包装；sftp
// 原生有 MkdirAll（v0.5.0 不暴露给 Client 接口）。
func (c *Client) Mkdir(p string) error {
	if c.sc == nil {
		return errors.New("sftpclient.Mkdir: client closed")
	}
	if err := c.sc.Mkdir(p); err != nil {
		return fmt.Errorf("sftpclient.Mkdir: %q: %w", p, err)
	}
	return nil
}

// Remove 删除远端文件或空目录。
//
// 非空目录需要 RemoveAll —— 本接口暂不暴露，调用方自己实现或后续版本加入。
func (c *Client) Remove(p string) error {
	if c.sc == nil {
		return errors.New("sftpclient.Remove: client closed")
	}
	if err := c.sc.Remove(p); err != nil {
		return fmt.Errorf("sftpclient.Remove: %q: %w", p, err)
	}
	return nil
}

// Rename 重命名远端文件或目录。
func (c *Client) Rename(o, n string) error {
	if c.sc == nil {
		return errors.New("sftpclient.Rename: client closed")
	}
	if err := c.sc.Rename(o, n); err != nil {
		return fmt.Errorf("sftpclient.Rename: %q -> %q: %w", o, n, err)
	}
	return nil
}

// Truncate 把远端 path 的文件截到 size 字节。
//
// v0.5.10 新增：streaming upload 在并发 WriteAt 之前先 Truncate 到 totalSize
// 做预分配（让"磁盘满"等错误早暴露，避免在大量 chunk 都失败后才报错）。
//
// pkg/sftp 的 *Client.Truncate 在 path 不存在时返回 error；
// 调用方应保证 path 已经被 OpenFile(O_CREATE) 创建过。
func (c *Client) Truncate(p string, size int64) error {
	if c.sc == nil {
		return errors.New("sftpclient.Truncate: client closed")
	}
	if err := c.sc.Truncate(p, size); err != nil {
		return fmt.Errorf("sftpclient.Truncate: %q to %d: %w", p, size, err)
	}
	return nil
}

// MkdirAll 递归创建远端目录（v0.5.10 新增）。
//
// pkg/sftp 提供 MkdirAll（sftp protocol 没有递归 mkdir，需要 client 拆）。
// 用途：streaming.Upload 在 OpenFile 之前确保父目录存在
// （多数 SFTP server 不自动创建父目录）。
func (c *Client) MkdirAll(p string) error {
	if c.sc == nil {
		return errors.New("sftpclient.MkdirAll: client closed")
	}
	if err := c.sc.MkdirAll(p); err != nil {
		return fmt.Errorf("sftpclient.MkdirAll: %q: %w", p, err)
	}
	return nil
}

// Close 关闭底层 SFTP 连接。
//
// 幂等：多次调用安全。Close 之后所有其它方法都会返回 "client closed" 错误。
// v0.5.3 起 Close 同时清空 pageCache 释放内存。
func (c *Client) Close() error {
	if c.sc == nil {
		return nil
	}
	err := c.sc.Close()
	c.sc = nil
	if c.pageCache != nil {
		c.pageCache.clear()
	}
	return err
}

// Write 写字节到远端 path（覆盖写）。
//
// v0.5.3 新增：把 wailsbindings.SftpUploadFile 的需求在 sftpclient 层提供
// 公开方法，避免 wailsbindings 跨包访问 *sftp.Client 的私有字段。
//
// 返回写入字节数 + 错误。错误时已写入字节数仍返回（best-effort）。
func (c *Client) Write(path string, data []byte) (int, error) {
	if c.sc == nil {
		return 0, errors.New("sftpclient.Write: client closed")
	}
	rf, err := c.sc.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return 0, fmt.Errorf("sftpclient.Write: open %q: %w", path, err)
	}
	defer rf.Close()
	n, err := rf.Write(data)
	if err != nil {
		return n, fmt.Errorf("sftpclient.Write: write %q: %w", path, err)
	}
	return n, nil
}

// UploadFile 把本地文件分片上传到远端 path。
//
// chunkSize <= 0 用默认 64 KiB（x/crypto 推荐的 SFTP 分片大小）。
// progress 回调每完成一个 chunk 调一次（参数：已传字节数；返回非 nil error
// 可取消上传）。
//
// v0.5.3 简化：
//   - 单 goroutine 顺序分片，不并行
//   - 错误时已传部分**不**回滚（best-effort，调用方负责清理）
//   - 进度回调高频（每 64 KiB 一次），调用方负责节流
//
// 大文件（>100MB）+ 并发上传留给 v0.6+ streaming upload。
func (c *Client) UploadFile(localPath, remotePath string, chunkSize int, progress func(written int64) error) error {
	if c.sc == nil {
		return errors.New("sftpclient.UploadFile: client closed")
	}
	lf, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("sftpclient.UploadFile: open local: %w", err)
	}
	defer lf.Close()

	if chunkSize <= 0 {
		chunkSize = 64 * 1024
	}

	rf, err := c.sc.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("sftpclient.UploadFile: open remote: %w", err)
	}
	defer rf.Close()

	buf := make([]byte, chunkSize)
	var total int64
	for {
		n, rerr := lf.Read(buf)
		if n > 0 {
			if _, werr := rf.Write(buf[:n]); werr != nil {
				return fmt.Errorf("sftpclient.UploadFile: write: %w", werr)
			}
			total += int64(n)
			if progress != nil {
				if perr := progress(total); perr != nil {
					return perr
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("sftpclient.UploadFile: read local: %w", rerr)
		}
	}
	return nil
}

// entryFromFileInfo 把 sftp/os 的 os.FileInfo 转换成 sftpclient.Entry。
//
// 已知 v0.5.0 限制：Entry.Link（symlink 目标路径）不会被填充。
// os.FileInfo 接口本身不暴露 symlink 目标；需要单独发起 ReadLink RPC。
// 前端 UI 在 v0.5.0 不会展示 Link 字段（前端契约见 wailsbindings/api.go），
// 因此暂不实现。v0.5.1+ 如果前端需要展示，单独加一次 ReadLink 即可。
func entryFromFileInfo(fi os.FileInfo, parent string) Entry {
	full := path.Join(parent, fi.Name())
	return Entry{
		Name:      fi.Name(),
		Path:      full,
		Size:      fi.Size(),
		Mode:      fi.Mode(),
		ModTime:   fi.ModTime(),
		IsDir:     fi.IsDir(),
		IsSymlink: fi.Mode()&os.ModeSymlink != 0,
		// Link: 留空 —— 见函数 doc。
	}
}

// Entry 描述一个文件系统条目。
type Entry struct {
	Name      string
	Path      string
	Size      int64
	Mode      os.FileMode
	ModTime   time.Time
	IsDir     bool
	IsSymlink bool
	Link      string
}

// ListPage 是大目录分页结果。
type ListPage struct {
	Entries   []Entry
	NextToken string
}

// ReadWriteCloser 是 io.ReadWriteCloser 的本地别名。
//
// 之所以单独命名而不是直接嵌入 io.ReadWriteCloser，
// 是为了让 wailsbindings/api.go 的方法签名更稳定：
// 即使将来在 sftpclient 内部换成自定义接口也不破坏前端契约。
//
// v0.5.10 扩展加 io.WriterAt：streaming upload 走 WriteAt 并发分片
// （pkg/sftp 的 *sftp.File 已实现 WriteAt）。扩展不破坏既有 caller
// （SftpRead/Write/UploadFile 只用 Reader/Writer/Closer）。
type ReadWriteCloser interface {
	io.Reader
	io.Writer
	io.WriterAt
	io.Closer
}
