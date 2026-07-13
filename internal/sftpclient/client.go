// Package sftpclient 在已有的 *ssh.Client 之上提供 SFTP 文件操作。
//
// 大目录强制分页（ReadDir 不行）；上传/下载走 WriteAt + 分片 + 断点续传。
package sftpclient

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Client 包装一个 *sftp.Client 并提供 MossTerm 风格的 API。
type Client struct {
	sc       *sftp.Client
	sshClient *ssh.Client
}

// Option 配置 Client 行为。
type Option func(*Client)

// WithPageSize 设置大目录分页的默认页大小（条目数）。
func WithPageSize(n int) Option {
	return func(c *Client) { _ = n /* TODO: store */ }
}

// Open 在已有 *ssh.Client 上打开 SFTP subsystem。
func Open(sshClient *ssh.Client, opts ...Option) (*Client, error) {
	panic("sftpclient.Open: not implemented")
}

// List 列出一个目录的分页结果。
//
// pageSize <= 0 时使用默认值（200）。
// pageToken 由上一次 List 返回；首次传空。
func (c *Client) List(ctx context.Context, path string, pageSize int, pageToken string) (ListPage, error) {
	panic("sftpclient.Client.List: not implemented")
}

// Stat 返回一个文件或目录的元数据。
func (c *Client) Stat(p string) (Entry, error) {
	panic("sftpclient.Client.Stat: not implemented")
}

// ReadDir 一次性读取整个目录（仅用于小目录）。
//
// 大目录必须使用 List 分页。
func (c *Client) ReadDir(p string) ([]Entry, error) {
	panic("sftpclient.Client.ReadDir: not implemented")
}

// Open 打开远端文件用于读写。
//
// flags 是 os.O_RDONLY / O_WRONLY / O_RDWR / O_CREATE / O_TRUNC / O_APPEND 的组合。
func (c *Client) Open(p string, flags int) (ReadWriteCloser, error) {
	panic("sftpclient.Client.Open: not implemented")
}

// Mkdir 在远端创建目录。
func (c *Client) Mkdir(p string) error {
	panic("sftpclient.Client.Mkdir: not implemented")
}

// Remove 删除远端文件或空目录。
func (c *Client) Remove(p string) error {
	panic("sftpclient.Client.Remove: not implemented")
}

// Rename 重命名远端文件或目录。
func (c *Client) Rename(o, n string) error {
	panic("sftpclient.Client.Rename: not implemented")
}

// Close 关闭底层 SFTP 连接。
func (c *Client) Close() error {
	panic("sftpclient.Client.Close: not implemented")
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
