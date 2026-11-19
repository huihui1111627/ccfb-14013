package st

import "time"

// NativeFB 是原生（Go 实现的）功能块实例接口。
// 状态保留在实现体内部，随实例帧一起存活，在线换程序时由迁移器决定是否保留。
type NativeFB interface {
	// Kind 返回功能块型号名：TON/TOF/TP/R_TRIG/F_TRIG。
	Kind() string
	// Execute 按扫描拍执行。now 为本拍虚拟时刻，dt 为任务周期（TIME 用纳秒）。
	Execute(inputs map[string]Value, now time.Duration, dt time.Duration) map[string]Value
	// Snapshot/Restore 用于在线换程序时迁移内部状态。
	Snapshot() map[string]Value
	Restore(map[string]Value)
}

// NativeFuncImpl 是原生函数实现（无状态），返回值类型由检查期标注。
type NativeFuncImpl func(args []Value) (Value, error)

// nativeFuncSigs/nativeFuncImpls 在 native.go 中填充。
var nativeFuncSigs = map[string]NativeFuncSig{}
var nativeFuncImpls = map[string]NativeFuncImpl{}
