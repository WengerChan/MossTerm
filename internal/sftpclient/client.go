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
}

// Option 配置 Client 行为。
type Option func(*Client)

// WithPageSize 设置大目录分页的默认页大小（条目数）。
//
// v0.5.0 暂未实现真正的分页：List 一次性返回全量结果，本选项保留作为
// 后续 v0.5.1+ 接入分页协议时的占位。当前调用是安全的 no-op。
func WithPageSize(n int) Option {
	return func(c *Client) { _ = n /* TODO(v0.5.1+): store and honor in List */ }
}

// Open 在已有 *ssh.Client 上打开 SFTP subsystem。
//
// opts 用于调整 Client 行为（当前仅 WithPageSize 占位）。nil sshClient
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
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// List 列出一个目录的分页结果。
//
// pageSize <= 0 时使用默认值（200）。
// pageToken 由上一次 List 返回；首次传空。
//
// v0.5.0 简化：pkg/sftp 的 ReadDir 不支持分页，List 一次性返回全量条目，
// pageSize / NextToken 仅作接口占位（NextToken 恒为空）。后续版本接入
// 真实分页协议时此方法签名保持不变，行为升级。
//
// ctx 当前未使用（SFTP 协议无 cancel 机制）；保留参数是为对齐
// 上层 ctx-aware 调用约定，方便 v0.5.1+ 接 ReadDir 分页时无需改签名。
func (c *Client) List(ctx context.Context, pathArg string, pageSize int, pageToken string) (ListPage, error) {
	if c.sc == nil {
		return ListPage{}, errors.New("sftpclient.List: client closed")
	}
	_ = ctx
	_ = pageToken
	if pageSize <= 0 {
		pageSize = 200
	}
	entries, err := c.sc.ReadDir(pathArg)
	if err != nil {
		return ListPage{}, fmt.Errorf("sftpclient.List: ReadDir %q: %w", pathArg, err)
	}
	out := make([]Entry, 0, len(entries))
	for _, fi := range entries {
		out = append(out, entryFromFileInfo(fi, pathArg))
	}
	return ListPage{
		Entries:   out,
		NextToken: "", // 简化：v0.5.0 一次性返回
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

// Close 关闭底层 SFTP 连接。
//
// 幂等：多次调用安全。Close 之后所有其它方法都会返回 "client closed" 错误。
func (c *Client) Close() error {
	if c.sc == nil {
		return nil
	}
	err := c.sc.Close()
	c.sc = nil
	return err
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
type ReadWriteCloser interface {
	io.Reader
	io.Writer
	io.Closer
}
