// Package app 的 download_adapter.go：sftpclient.Client → transfer.Downloader 适配。
//
// v0.6.0 加（与 upload_adapter.go 镜像）：
//   - transfer.Downloader 接口只暴露 Open / Stat 两个方法
//   - sftpclient.Client 已实现这两个（Open flags=O_RDONLY + Stat）
//   - 适配器是 0 行 wrapper：直接拿 *sftpclient.Client 当 Downloader
//   - 单开文件便于 v0.6+ 其它协议接入 transfer.Downloader 时复用
//     （保持 upload / download 各自一个 adapter 文件的对称）
package app

import (
	"github.com/mossterm/mossterm/internal/sftpclient"
	"github.com/mossterm/mossterm/internal/transfer"
)

// sftpDownloader 把 *sftpclient.Client 适配成 transfer.Downloader。
//
// 零值不可用；必须通过 &sftpDownloader{Client: c} 构造。
// 线程安全：依赖 *sftpclient.Client 的内部锁（Open/Stat 都是 pkg/sftp
// 并发安全 API）。
//
// v0.6.0 设计选择：不开任何新方法 —— *sftpclient.Client 直接满足
// transfer.Downloader 接口（Open 返回 ReadWriteCloser 含 io.ReaderAt，
// 满足 streaming.Download 走 ReadAt 并发分片）。适配器存在的唯一理由
// 是**编译期类型断言**（var _ transfer.Downloader = ...）防止
// sftpclient.Client 接口偷偷漂移时悄然破坏 download 路径。
type sftpDownloader struct {
	*sftpclient.Client
}

// Compile-time assertion: *sftpDownloader 实现 transfer.Downloader。
var _ transfer.Downloader = (*sftpDownloader)(nil)
