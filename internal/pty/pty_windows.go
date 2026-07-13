//go:build windows
// +build windows

// pty_windows.go 提供 Windows 平台的 PTY 桩实现。
//
// v0.1 暂不支持 Windows 上的本地 PTY（ConPTY / winpty）。
// 任何调用 Start 都会返回 error，但包级 defaultFactory 仍然被赋值，
// 保证 pty.Default() 不会 panic。
package pty

import (
	"context"
	"errors"
	"os/exec"
)

func init() {
	defaultFactory = windowsFactory{}
}

// windowsFactory 是 Windows 平台的 stub 实现。
type windowsFactory struct{}

// Start 在 Windows 平台总是返回 "not implemented" 错误。
//
// v0.1 不实现 ConPTY 的原因：
//  1. ConPTY 需要 cgo + Windows SDK，CI 镜像较重
//  2. MossTerm v0.1 优先支持 macOS / Linux 桌面端
//  3. SSH 会话本身已通过 RequestPty 拿到 pty 语义，本地 PTY 仅在
//     "本地 shell" 模式（v0.5+）才需要
func (windowsFactory) Start(_ context.Context, _ *exec.Cmd, _ Options) (PTY, error) {
	return nil, errors.New("pty: Windows not implemented yet (v0.1 only supports macOS/Linux for local PTY)")
}
