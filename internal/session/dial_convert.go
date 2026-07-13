// dial_convert.go 把 session.OpenRequest 转换成 connect 层的入参。
//
// 设计说明：本文件放在 session 包内，而非 connect 包。
// 因为 OpenRequest 类型属于 session 包，函数若放 connect 包就会
// 形成 `connect → session → connect` 的循环 import。
package session

import (
	"errors"
	"fmt"
	"time"

	"github.com/mossterm/mossterm/internal/connect"
)

// DialParamsFrom 把 session.OpenRequest 转换成 connect.DialParams。
//
// 转换规则：
//   - Host / User 必填，缺失返回 error
//   - Port == 0 时用 SSH 默认端口 22
//   - Auth 通过 AuthSpec.ToAuthMethod() 转换
//   - Timeout 默认 15s（与 connect.StdDeps 保持一致）
//   - KeepAlive 默认 30s
//   - 不复制 Env —— Env 走 SessionOptsFrom
//
// cols/rows 不在 DialParams 里；它们属于 SessionOpts。
func DialParamsFrom(req OpenRequest) (connect.DialParams, error) {
	if req.Host == "" {
		return connect.DialParams{}, errors.New("session.DialParamsFrom: empty host")
	}
	if req.User == "" {
		return connect.DialParams{}, errors.New("session.DialParamsFrom: empty user")
	}

	port := req.Port
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return connect.DialParams{}, fmt.Errorf("session.DialParamsFrom: invalid port %d", port)
	}

	auth, err := req.Auth.ToAuthMethod()
	if err != nil {
		return connect.DialParams{}, fmt.Errorf("session.DialParamsFrom: auth: %w", err)
	}

	return connect.DialParams{
		Host:      req.Host,
		Port:      port,
		User:      req.User,
		Auth:      auth,
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		// Extra 留作未来扩展（如 ProxyCommand、Compression 等）
	}, nil
}

// SessionOptsFrom 把 session.OpenRequest 转换成 connect.SessionOpts。
//
// 与 DialParamsFrom 分离的原因：cols/rows/env 只在 OpenSession 阶段
// 才有意义，dial 阶段不需要。
//
// 转换规则：
//   - Term 默认 xterm-256color
//   - Cols <= 0 时默认 80
//   - Rows <= 0 时默认 24
//   - Env 原样复制
func SessionOptsFrom(req OpenRequest) connect.SessionOpts {
	cols := req.Columns
	if cols <= 0 {
		cols = 80
	}
	rows := req.Rows
	if rows <= 0 {
		rows = 24
	}
	return connect.SessionOpts{
		Term: "xterm-256color",
		Cols: cols,
		Rows: rows,
		Env:  cloneEnv(req.Env),
	}
}

// cloneEnv 深拷贝 env map，防止 caller 后续修改影响 session 行为。
func cloneEnv(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
