// Package app 的 upload_adapter.go：sftpclient.Client → transfer.Uploader 适配。
//
// 设计要点：
//   - transfer.Uploader 接口只暴露 Open / Stat / Truncate 三个方法
//   - sftpclient.Client 已实现这三个（Open / Stat + v0.5.10 新增的 Truncate）
//   - 适配器是 ~10 行 wrapper；不开新文件完全可以放在 app.go 内
//   - 单开文件便于 v0.6+ 下载/其它协议接入 transfer.Uploader 时复用
package app

import (
	"github.com/mossterm/mossterm/internal/sftpclient"
	"github.com/mossterm/mossterm/internal/transfer"
)

// sftpUploader 把 *sftpclient.Client 适配成 transfer.Uploader。
//
// 零值不可用；必须通过 &sftpUploader{Client: c} 构造。
// 线程安全：依赖 *sftpclient.Client 的内部锁（Open/Stat/Truncate 都是
// pkg/sftp 并发安全 API）。
type sftpUploader struct {
	*sftpclient.Client
}

// Compile-time assertion: *sftpUploader 实现 transfer.Uploader。
var _ transfer.Uploader = (*sftpUploader)(nil)

// Truncate 是 sftpclient.Client 的新方法（v0.5.10 加），把远端文件截到 size。
//
// pkg/sftp 的 *Client.Truncate 在 path 不存在时会返回 error，
// streaming.Upload 在 Truncate 之前已经 OpenFile 创建过文件，路径必然存在。
func (u *sftpUploader) Truncate(path string, size int64) error {
	return u.Client.Truncate(path, size)
}

// MkdirAll 是 sftpclient.Client 的新方法（v0.5.10 加），递归创建远端目录。
//
// streaming.Upload 在 OpenFile 之前调这个保证父目录存在。
// pkg/sftp 提供 MkdirAll（client 内部拆多层 mkdir）。
func (u *sftpUploader) MkdirAll(path string) error {
	return u.Client.MkdirAll(path)
}
