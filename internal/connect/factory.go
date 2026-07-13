package connect

import (
	"time"

	"golang.org/x/crypto/ssh"
)

// Deps 是构造 Connector 所需的依赖集合。
//
// 保持小而集中：仅传递工厂函数真正需要的句柄，
// 避免在 internal/connect 中反向引用具体实现包。
type Deps struct {
	// HostKeyCb 会在 SSH 协议握手阶段被回调，用于实现 known_hosts 校验。
	// 当前仅 sshclient 关心；其它协议可置 nil。
	HostKeyCb HostKeyCallback
	// BannerCb 用于接收 SSH banner（连接前由服务端推送的提示信息）。
	BannerCb BannerCallback
	// DialTimeout 与 KeepAlive 是默认值；调用方可在 DialParams 中覆盖。
	DialTimeout time.Duration
	KeepAlive   time.Duration
}

// HostKeyCallback 兼容 golang.org/x/crypto/ssh.HostKeyCallback 签名。
//
// 实现位于 internal/sshclient（基于 known_hosts 文件）。
//
// 直接 alias 到 ssh.HostKeyCallback，避免 Go 1.26 严格类型系统
// 在 named func type 之间拒绝隐式转换。
type HostKeyCallback = ssh.HostKeyCallback

// BannerCallback 接收服务端 banner 文本。
//
// 实现负责把 banner 推送到 Wails 事件总线（log:line 等）。
type BannerCallback func(message string) error

// Factory 根据 Deps 构造一个 Connector 实例。
//
// 每个协议实现（sshclient / 未来的 telnetclient / serialclient）都提供自己的 Factory。
// 工厂在进程启动时被注册到 Registry。
type Factory func(deps Deps) (Connector, error)

// StdDeps 提供一组默认的 Deps 值。
//
// 仅在测试或非 Wails 入口（如单元测试 / 子命令）使用。
func StdDeps() Deps {
	return Deps{
		DialTimeout: 15 * time.Second,
		KeepAlive:   30 * time.Second,
	}
}
