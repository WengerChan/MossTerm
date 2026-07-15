// base.go 提供各 mode 共享的状态机 / spec 持有 / 状态写辅助。
//
// 设计：Local / Remote / Dynamic 三个 runner 都内嵌 *baseTunnel 做状态
// 转移；state 字段为 atomic.Int32（无锁读、比较-写用 CAS 转移）。
//
// 状态转移：
//
//	New → Active  (Start 成功)
//	New → Failed  (Start 失败 / nil provider)
//	Active → Stopped (Stop 调用)
//	任何 → Failed (运行中错误？v0.6 不引入，listener 错只丢单 conn)
//
// 注意：v0.6 不引入 "状态机非法转移守卫" —— baseTunnel 只记录
// setState 的结果。runner 自己决定何时调。
package tunnel

import "sync/atomic"

// baseTunnel 持有 spec + state，被具体 runner 组合。
type baseTunnel struct {
	spec  Spec
	state atomic.Int32
}

func newBaseTunnel(spec Spec) *baseTunnel {
	b := &baseTunnel{spec: spec}
	b.state.Store(int32(TunnelStateNew))
	return b
}

func (b *baseTunnel) Spec() Spec { return b.spec }

// State 返回当前状态。
func (b *baseTunnel) State() TunnelState {
	return TunnelState(b.state.Load())
}

// setState 写新状态（runner 内部用）。
func (b *baseTunnel) setState(s TunnelState) {
	b.state.Store(int32(s))
}
