package st

import (
	"fmt"
	"math"
)

// Value 是 ST 运行时的叶子值。聚合类型不使用 Value，而是由 Frame 的连续槽位承载。
type Value struct {
	Type *Type
	// 标量值统一盒子：
	//   BOOL        -> bool
	//   整数族       -> int64 / uint64（按 Unsigned）
	//   REAL/LREAL  -> float64
	//   STRING      -> string
	//   TIME        -> time.Duration（纳秒）
	//   枚举         -> int64
	V any
}

func BoolVal(b bool) Value { return Value{Type: primitiveTypes["BOOL"], V: b} }

// PrimitiveType 按规范名返回基本类型（供其他包使用）。
func PrimitiveType(name string) *Type { return primitiveTypes[name] }
func IntVal(t *Type, v int64) Value { return Value{Type: t, V: v} }
func UintVal(t *Type, v uint64) Value { return Value{Type: t, V: v} }
func RealVal(t *Type, v float64) Value { return Value{Type: t, V: v} }
func StrVal(s string) Value { return Value{Type: primitiveTypes["STRING"], V: s} }

// AsBool 取布尔值。
func (v Value) AsBool() bool {
	if b, ok := v.V.(bool); ok {
		return b
	}
	return false
}

// AsInt 取有符号整数（枚举、位串都可转）。
func (v Value) AsInt() int64 {
	switch x := v.V.(type) {
	case int64:
		return x
	case uint64:
		return int64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return int64(x)
	}
	return 0
}

// AsUint 取无符号整数。
func (v Value) AsUint() uint64 {
	switch x := v.V.(type) {
	case uint64:
		return x
	case int64:
		return uint64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return uint64(x)
	}
	return 0
}

// AsReal 取浮点。
func (v Value) AsReal() float64 {
	switch x := v.V.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case uint64:
		return float64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	}
	return 0
}

// Ref 是一个可读写左值的定位接口。解释器绑定阶段把所有左值表达式编译成 Ref。
type Ref interface {
	Get() Value
	Set(Value)
	ValueType() *Type
}

// SlotRef 指向 Frame 内一个值槽。
type SlotRef struct {
	Frame *Frame
	Index int
	T     *Type
}

func (r *SlotRef) Get() Value          { return r.Frame.Slots[r.Index] }
func (r *SlotRef) Set(v Value)          { r.Frame.Slots[r.Index] = v }
func (r *SlotRef) ValueType() *Type     { return r.T }

// FBRef 指向某个功能块实例帧（用于 FB 字段访问与调用）。
type FBRef struct {
	Instance *Frame
}

// Frame 是一次 POU 实例化/调用的内存帧。
type Frame struct {
	POU    *POU          // 所属 POU（原生 FB 为 nil）
	Native NativeFB      // 非 nil 表示原生功能块实例（TON 等）
	Slots  []Value       // 全部标量槽（结构体/数组按 Size 展开）
	Vars   map[string]*VarBinding
	Parent *Frame        // 词法外层（FUNCTION 调用链）
}

// VarBinding 变量绑定：标量给槽引用，聚合给基槽与类型。
type VarBinding struct {
	Type    *Type
	Base    int    // 在 Slots 中的起始槽
	IsFB    bool
	FB      *Frame // 功能块实例帧
}

// Ref 返回该变量的左值引用（标量）。
func (f *Frame) Ref(name string) (*SlotRef, error) {
	b, ok := f.Vars[name]
	if !ok {
		if f.Parent != nil {
			return f.Parent.Ref(name)
		}
		return nil, fmt.Errorf("未声明的变量 %q", name)
	}
	if b.IsFB {
		return nil, fmt.Errorf("功能块实例 %q 不能作为标量使用", name)
	}
	return &SlotRef{Frame: f, Index: b.Base, T: leafType(b.Type)}, nil
}

// FBInstance 取功能块实例帧。
func (f *Frame) FBInstance(name string) (*Frame, error) {
	b, ok := f.Vars[name]
	if !ok {
		if f.Parent != nil {
			return f.Parent.FBInstance(name)
		}
		return nil, fmt.Errorf("未声明的功能块实例 %q", name)
	}
	if !b.IsFB {
		return nil, fmt.Errorf("%q 不是功能块实例", name)
	}
	return b.FB, nil
}

func leafType(t *Type) *Type {
	// 只有标量变量能直接建 SlotRef；聚合变量通过下标/字段访问。
	return t
}

// ZeroValue 生成类型的零值。
func ZeroValue(t *Type) Value {
	switch t.Kind {
	case TBool:
		return BoolVal(false)
	case TInt, TEnum:
		return IntVal(t, 0)
	case TUint:
		return UintVal(t, 0)
	case TReal:
		return RealVal(t, 0)
	case TString:
		return StrVal("")
	case TTime:
		return Value{Type: t, V: int64(0)}
	default:
		return Value{Type: t, V: int64(0)}
	}
}

// Convert 把值转换到目标类型（数值放宽/收窄、枚举、时间运算结果规整）。
func Convert(v Value, dst *Type) (Value, error) {
	src := v.Type
	if src == dst {
		return v, nil
	}
	switch dst.Kind {
	case TBool:
		if src.Kind != TBool {
			return Value{}, fmt.Errorf("不能把 %s 转成 BOOL", src.Name)
		}
		return v, nil
	case TInt, TEnum:
		switch src.Kind {
		case TInt, TUint, TEnum:
			iv := v.AsInt()
			if dst.Kind == TEnum && src != dst {
				return Value{}, fmt.Errorf("枚举类型 %s 与 %s 不兼容", src.Name, dst.Name)
			}
			iv = clampSigned(iv, dst.Bits)
			return Value{Type: dst, V: iv}, nil
		case TReal:
			return Value{Type: dst, V: clampSigned(int64(math.Round(v.AsReal())), dst.Bits)}, nil
		}
	case TUint:
		switch src.Kind {
		case TInt, TUint:
			return Value{Type: dst, V: clampUnsigned(v.AsUint(), dst.Bits)}, nil
		case TReal:
			return Value{Type: dst, V: clampUnsigned(uint64(math.Round(v.AsReal())), dst.Bits)}, nil
		}
	case TReal:
		if src.IsNumeric() {
			return Value{Type: dst, V: v.AsReal()}, nil
		}
	case TString:
		if src.Kind == TString {
			return Value{Type: dst, V: v.V}, nil
		}
	case TTime:
		if src.Kind == TTime {
			return Value{Type: dst, V: v.V}, nil
		}
	}
	return Value{}, fmt.Errorf("不支持的类型转换: %s -> %s", src.Name, dst.Name)
}

func clampSigned(v int64, bits int) int64 {
	if bits >= 64 {
		return v
	}
	shift := 64 - bits
	return (v << shift) >> shift
}

func clampUnsigned(v uint64, bits int) uint64 {
	if bits >= 64 {
		return v
	}
	return v & ((uint64(1) << bits) - 1)
}
