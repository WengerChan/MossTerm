//go:build !windows
// +build !windows

// pty_unix.go 提供 macOS / Linux / *BSD 上的 PTY 实现（基于 creack/pty）。
//
// 该文件仅在非 windows 平台编译。
package pty

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func init() {
	// 在包初始化阶段把 defaultFactory 绑定到 unix 实现，
	// 这样 pty.Default() 总是能返回当前平台的工厂。
	defaultFactory = unixFactory{}
}

// unixFactory 在 unix 平台上启动一个 PTY。
type unixFactory struct{}

// Start 在 unix 平台启动一个 PTY 并把 cmd 与 slave fd 关联。
//
// 参数约束：
//   - cmd 不能为 nil（如果是 SSH 协议层，请直接用 connect.Session，
//     它已经通过 SSH RequestPty 拿到了远端 pty 语义）
//   - cmd 不能已经启动（creack/pty 会自己 fork）
//   - opts.Cols / opts.Rows <= 0 时使用 80x24 兜底
//
// 环境变量处理：opts.Env 会被 append 到 cmd.Environ()（保留系统 env），
// 并把 TERM 替换 / 设置为 opts.Term（默认 xterm-256color）。
func (unixFactory) Start(ctx context.Context, cmd *exec.Cmd, opts Options) (PTY, error) {
	if cmd == nil {
		return nil, errors.New("pty.unixFactory.Start: cmd is nil (use connect.Session for SSH)")
	}
	if ctx == nil {
		return nil, errors.New("pty.unixFactory.Start: nil ctx")
	}
	if cmd.Process != nil {
		return nil, errors.New("pty.unixFactory.Start: cmd already started")
	}

	// 默认 TERM
	term := opts.Term
	if term == "" {
		term = "xterm-256color"
	}

	// 合并 env：先取系统 env，再追加 opts.Env，最后强制覆盖 TERM
	if cmd.Env == nil {
		cmd.Env = cmd.Environ()
	}
	cmd.Env = appendEnv(cmd.Env, opts.Env)
	cmd.Env = setEnv(cmd.Env, "TERM", term)

	// 默认窗口大小
	cols := opts.Cols
	rows := opts.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	// 启动 PTY
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(rows),
		Cols: uint16(cols),
	})
	if err != nil {
		return nil, fmt.Errorf("pty.unixFactory.Start: pty.StartWithSize: %w", err)
	}

	return &unixPTY{
		tty: tty,
		cmd: cmd,
	}, nil
}

// unixPTY 是 PTY 接口的 unix 实现。
//
// 它把 creack/pty 返回的 *os.File 当作主端 fd，自己只持有引用；
// 所有 Read / Write / Close / Resize 都直接转发给这个 fd。
type unixPTY struct {
	tty *os.File
	cmd *exec.Cmd
}

// Read 转发到底层 PTY fd。
func (p *unixPTY) Read(b []byte) (int, error) {
	return p.tty.Read(b)
}

// Write 转发到底层 PTY fd。
func (p *unixPTY) Write(b []byte) (int, error) {
	return p.tty.Write(b)
}

// Close 关闭底层 PTY fd。
func (p *unixPTY) Close() error {
	return p.tty.Close()
}

// Resize 调 creack/pty.Setsize 改变远端 winsize。
func (p *unixPTY) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("pty.unixPTY.Resize: invalid size %dx%d", cols, rows)
	}
	return pty.Setsize(p.tty, &pty.Winsize{
		Rows: uint16(rows),
		Cols: uint16(cols),
	})
}

// PID 返回子进程 PID。
func (p *unixPTY) PID() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// TTYName 返回底层设备路径（如 /dev/ttys001）。
func (p *unixPTY) TTYName() string {
	if p.tty == nil {
		return ""
	}
	return p.tty.Name()
}

// -----------------------------------------------------------------------------
// 辅助函数
// -----------------------------------------------------------------------------

// appendEnv 把 extra 中的 key=value 追加到 env，extra 为空时直接返回 env。
// 不做重复 key 检查（ssh Setenv 风格：后写覆盖前写）。
func appendEnv(env, extra []string) []string {
	if len(extra) == 0 {
		return env
	}
	out := make([]string, 0, len(env)+len(extra))
	out = append(out, env...)
	out = append(out, extra...)
	return out
}

// setEnv 在 env 中查找 "key=..." 并替换为 "key=value"；找不到则追加。
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
