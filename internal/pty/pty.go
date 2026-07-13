// Package pty 提供跨平台 PTY 抽象。
//
// macOS / Linux 使用 creack/pty；Windows 未来使用 ConPTY。
// 本包只负责"打开 pty 设备并暴露 fd"；
// 命令启动由 SSH 协议完成（RequestPty + StartShell）。
// 本地 shell 模式（v0.5+）才会用 exec.Cmd。
package pty

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

// PTY 是一个伪终端实例。
//
// 它既是 io.ReadWriteCloser（与远端 shell 交互），
// 又能调整窗口大小并查询远端 PID。
type PTY interface {
	io.ReadWriteCloser
	// Resize 通知对端窗口尺寸变化。
	Resize(cols, rows int) error
	// PID 返回对端进程 PID；本地 shell 模式下为本地 shell PID。
	PID() int
	// TTYName 返回底层设备路径（如 /dev/ttys001）；用于调试。
	TTYName() string
}

// Factory 创建 PTY 实例。
//
// 平台相关实现由 build tag 区分（pty_unix.go / pty_windows.go）。
// 进程启动时通过 Default() 选定合适的工厂。
type Factory interface {
	Start(ctx context.Context, cmd *exec.Cmd, opts Options) (PTY, error)
}

// Options 描述一次 PTY 启动所需的参数。
type Options struct {
	Cols int
	Rows int
	Term string
	Env  []string
}

// Default 返回当前平台的默认 Factory。
//
// 进程启动时由 init() 选择具体实现。
func Default() Factory {
	return defaultFactory
}

// defaultFactory 在平台实现文件中赋值。
//
// 占位实现：当没有平台文件被 link 时（理论上不应该发生，
// 因为 *_unix.go / *_windows.go 是必备的），返回一个返回错误的工厂。
var defaultFactory Factory = errorFactory{}

// errorFactory 在未提供平台实现时给出明确报错。
type errorFactory struct{}

// Start 总是返回 "not implemented"。
func (errorFactory) Start(_ context.Context, _ *exec.Cmd, _ Options) (PTY, error) {
	return nil, fmt.Errorf("pty: no platform implementation registered (build tag missing?)")
}

// Size 表示一个 PTY 窗口尺寸。
type Size struct {
	Cols int
	Rows int
}

// String 返回 "ColsxRows" 形式。
func (s Size) String() string {
	return fmt.Sprintf("%dx%d", s.Cols, s.Rows)
}
