// Package mossterm 是 MossTerm 项目的公共 API 入口。
//
// 该包向外部 Go 程序（如自动化脚本、CI 工具、第三方插件）暴露稳定
// 的 schema / 事件名 / 版本号。所有依赖 internal/* 的复杂实现都被隐藏。
//
// 兼容性承诺：本包遵循严格 semver；任何 breaking change 必须升级 major。
package mossterm

import _ "embed"

// Version 是 MossTerm 的当前版本。
//
// 与 git tag 保持一致；release 时由 CI 注入 ldflags 覆盖。
const Version = "0.1.0"

// MagicByte 是 MossTerm 协议帧的 magic 字节（ASCII 'M'）。
//
// 预留用于未来进程间通信 / 录制回放格式。
const MagicByte byte = 0x4D

// Scheme 标识一种协议。
type Scheme string

const (
	// SchemeSSH 是 SSH 协议。
	SchemeSSH Scheme = "ssh"
	// SchemeSFTP 是 SFTP 协议。
	SchemeSFTP Scheme = "sftp"
	// SchemeTelnet 是 Telnet 协议（v0.3+）。
	SchemeTelnet Scheme = "telnet"
	// SchemeSerial 是串口协议（v0.3+）。
	SchemeSerial Scheme = "serial"
)

// UserAgent 标识客户端身份，写入 SSH banner / User-Agent 头。
const UserAgent = "MossTerm/" + Version

// event topic 常量（与 internal/.../event 镜像）。
//
// 前端 lib/events.ts 必须与本表保持一致；CI 中会做对账检查。
const (
	// EventSessionData 在 PTY 输出时发送；payload 是 { id, data: Uint8Array }。
	EventSessionData = "session:data"
	// EventSessionState 在 Session 状态变化时发送。
	EventSessionState = "session:state"
	// EventSessionExit 在 Session 关闭时发送。
	EventSessionExit = "session:exit"
	// EventSessionOverflow 在背压丢帧时发送。
	EventSessionOverflow = "session:overflow"
	// EventTransferProgress 在传输进度更新时发送。
	EventTransferProgress = "transfer:progress"
	// EventTransferDone 在传输完成时发送。
	EventTransferDone = "transfer:done"
	// EventAIResponse 在 AI 调用返回时发送。
	EventAIResponse = "ai:response"
	// EventLogLine 在日志面板打开时按行推送。
	EventLogLine = "log:line"
	// EventAppReady 在 webview DOM ready 后由后端主动发送。
	EventAppReady = "app:ready"
)

// 编译期断言：所有 topic 都是非空字符串。
var _ = []string{
	EventSessionData, EventSessionState, EventSessionExit,
	EventSessionOverflow, EventTransferProgress, EventTransferDone,
	EventAIResponse, EventLogLine, EventAppReady,
}
