// Package ai 提供 AI 辅助能力。
//
// 隐私策略（架构基线）：
//   - 默认不开启。
//   - UI 弹"确认发送"对话框。
//   - 支持 Ollama 完全离线（数据不出本机）。
//   - 调用方负责把"敏感输出"过滤后发送（建议 UI 端在最后 1KB 内
//     删除常见密码字段）。
package ai

import (
	"context"
	"time"
)

// Provider 标识 AI 服务提供方。
type Provider string

const (
	OpenAI    Provider = "openai"
	Ollama    Provider = "ollama"
	Anthropic Provider = "anthropic"
)

// Client 是 AI 能力的抽象接口。
type Client interface {
	// Explain 用自然语言解释一条命令。
	Explain(ctx context.Context, cmd string) (string, error)
	// Summarize 总结一段日志 / 输出。
	// hint 是用户提示（如 "focus on errors"）。
	Summarize(ctx context.Context, log, hint string) (string, error)
	// Suggest 根据历史命令推荐下一步。
	Suggest(ctx context.Context, history []string) ([]string, error)
}

// Options 是 New 的入参。
type Options struct {
	Provider Provider
	Endpoint string
	// KeyID 是 secret.Store 中 API key 条目的 ID（不要把 key 明文传入）。
	KeyID string
	Model string
	// Timeout 默认 30s；零值使用默认。
	Timeout time.Duration
	// MaxTokens 默认 1024；零值使用默认。
	MaxTokens int
}

// New 构造一个 Client。
//
// 实现按 Provider 分发：OpenAI / Ollama / Anthropic 各一个 adapter。
func New(opts Options) (Client, error) {
	if opts.Provider == "" {
		return nil, errEmptyProvider
	}
	panic("ai.New: not implemented")
}

// errEmptyProvider 是 New 的必填校验。
var errEmptyProvider = &AIError{Code: "empty_provider", Message: "Provider is required"}

// AIError 是 AI 模块的错误类型。
//
// 未来 v0.2+ 用于把上游 API 错误标准化后展示给前端。
type AIError struct {
	Code    string
	Message string
	Err     error
}

// Error 实现 error 接口。
func (e *AIError) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Message + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Message
}

// Unwrap 支持 errors.Is / errors.As。
func (e *AIError) Unwrap() error { return e.Err }

// 占位：context 引用。
var _ = context.Background
