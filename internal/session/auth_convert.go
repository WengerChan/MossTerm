// auth_convert.go 是 session.AuthSpec 的转换器。
//
// 这一层很薄：把字段透传给 connect.AuthMethodFromSpec，
// 避免在 connect 包里反向 import session 形成循环依赖。
package session

import (
	"github.com/mossterm/mossterm/internal/connect"
)

// ToAuthMethod 把当前 AuthSpec 转换成 connect.AuthMethod。
//
// kind / password / keyID / passphrase 四个字段直接传给
// connect.AuthMethodFromSpec；具体校验逻辑在该函数中。
//
// 返回值：
//   - nil + nil error：当前 spec 不需要 auth（v0.1 暂不会出现）
//   - nil + error：spec 无效或 kind 未实现
//   - 非 nil：可用于 connector.Dial(params.Auth)
func (s AuthSpec) ToAuthMethod() (connect.AuthMethod, error) {
	return connect.AuthMethodFromSpec(s.Kind, s.Password, s.KeyID, s.Passphrase)
}
