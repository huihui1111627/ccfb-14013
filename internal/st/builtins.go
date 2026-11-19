package st

import (
	"fmt"
	"math"
)

func init() {
	registerNativeFuncs()
}

func registerNativeFuncs() {
	sig := NativeFuncSig{}
	for _, name := range []string{
		"ABS", "SQRT", "LN", "LOG", "EXP", "SIN", "COS", "TAN",
		"ASIN", "ACOS", "ATAN", "MIN", "MAX", "LIMIT", "SEL",
		"MUX", "TRUNC", "INT_TO_REAL", "REAL_TO_INT", "BOOL_TO_INT",
		"INT_TO_BOOL", "TIME_TO_INT", "INT_TO_TIME", "LEN", "CONCAT",
	} {
		nativeFuncSigs[name] = sig
	}
	nativeFuncImpls["ABS"] = func(a []Value) (Value, error) {
		if a[0].Type.Kind == TReal {
			return RealVal(a[0].Type, math.Abs(a[0].AsReal())), nil
		}
		v := a[0].AsInt()
		if v < 0 {
			v = -v
		}
		return IntVal(a[0].Type, v), nil
	}
	nativeFuncImpls["SQRT"] = unaryReal(math.Sqrt)
	nativeFuncImpls["LN"] = unaryReal(math.Log)
	nativeFuncImpls["LOG"] = unaryReal(math.Log10)
	nativeFuncImpls["EXP"] = unaryReal(math.Exp)
	nativeFuncImpls["SIN"] = unaryReal(math.Sin)
	nativeFuncImpls["COS"] = unaryReal(math.Cos)
	nativeFuncImpls["TAN"] = unaryReal(math.Tan)
	nativeFuncImpls["ASIN"] = unaryReal(math.Asin)
	nativeFuncImpls["ACOS"] = unaryReal(math.Acos)
	nativeFuncImpls["ATAN"] = unaryReal(math.Atan)

	nativeFuncImpls["MIN"] = func(a []Value) (Value, error) {
		m := a[0]
		for _, x := range a[1:] {
			if compareOrdered(x, m) < 0 {
				m = x
			}
		}
		return m, nil
	}
	nativeFuncImpls["MAX"] = func(a []Value) (Value, error) {
		m := a[0]
		for _, x := range a[1:] {
			if compareOrdered(x, m) > 0 {
				m = x
			}
		}
		return m, nil
	}
	// LIMIT(MN, IN, MX)：把 IN 限制在 [MN, MX]
	nativeFuncImpls["LIMIT"] = func(a []Value) (Value, error) {
		mn, in, mx := a[0], a[1], a[2]
		if compareOrdered(in, mn) < 0 {
			return mn, nil
		}
		if compareOrdered(in, mx) > 0 {
			return mx, nil
		}
		return in, nil
	}
	// SEL(G, IN0, IN1)：G=TRUE 选 IN1
	nativeFuncImpls["SEL"] = func(a []Value) (Value, error) {
		if a[0].AsBool() {
			return a[2], nil
		}
		return a[1], nil
	}
	// MUX(K, IN0, IN1, ...)
	nativeFuncImpls["MUX"] = func(a []Value) (Value, error) {
		k := int(a[0].AsInt())
		idx := k + 1
		if idx < 1 || idx >= len(a) {
			return Value{}, fmt.Errorf("MUX 下标 %d 越界", k)
		}
		return a[idx], nil
	}
	nativeFuncImpls["TRUNC"] = func(a []Value) (Value, error) {
		return IntVal(primitiveTypes["DINT"], int64(math.Trunc(a[0].AsReal()))), nil
	}
	nativeFuncImpls["INT_TO_REAL"] = func(a []Value) (Value, error) {
		return RealVal(primitiveTypes["LREAL"], a[0].AsReal()), nil
	}
	nativeFuncImpls["REAL_TO_INT"] = func(a []Value) (Value, error) {
		return IntVal(primitiveTypes["DINT"], int64(math.Round(a[0].AsReal()))), nil
	}
	nativeFuncImpls["BOOL_TO_INT"] = func(a []Value) (Value, error) {
		if a[0].AsBool() {
			return IntVal(primitiveTypes["INT"], 1), nil
		}
		return IntVal(primitiveTypes["INT"], 0), nil
	}
	nativeFuncImpls["INT_TO_BOOL"] = func(a []Value) (Value, error) {
		return BoolVal(a[0].AsInt() != 0), nil
	}
	nativeFuncImpls["TIME_TO_INT"] = func(a []Value) (Value, error) {
		return IntVal(primitiveTypes["LINT"], a[0].V.(int64)), nil
	}
	nativeFuncImpls["INT_TO_TIME"] = func(a []Value) (Value, error) {
		return Value{Type: primitiveTypes["TIME"], V: a[0].AsInt()}, nil
	}
	nativeFuncImpls["LEN"] = func(a []Value) (Value, error) {
		return IntVal(primitiveTypes["DINT"], int64(len(a[0].V.(string)))), nil
	}
	nativeFuncImpls["CONCAT"] = func(a []Value) (Value, error) {
		s := ""
		for _, v := range a {
			s += v.V.(string)
		}
		return StrVal(s), nil
	}
}

func unaryReal(f func(float64) float64) NativeFuncImpl {
	return func(a []Value) (Value, error) {
		return RealVal(primitiveTypes["LREAL"], f(a[0].AsReal())), nil
	}
}

// compareOrdered 返回 -1/0/1；支持数值与 TIME。
func compareOrdered(a, b Value) int {
	if a.Type.Kind == TTime {
		x, y := a.V.(int64), b.V.(int64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		default:
			return 0
		}
	}
	if a.Type.Kind == TString {
		switch {
		case a.V.(string) < b.V.(string):
			return -1
		case a.V.(string) > b.V.(string):
			return 1
		default:
			return 0
		}
	}
	x, y := a.AsReal(), b.AsReal()
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	default:
		return 0
	}
}

// nativeFuncReturnType 检查期推导返回类型。
func nativeFuncReturnType(name string, args []*Type) *Type {
	if len(args) == 0 {
		return nil
	}
	switch name {
	case "TRUNC":
		return primitiveTypes["DINT"]
	case "INT_TO_REAL":
		return primitiveTypes["LREAL"]
	case "REAL_TO_INT":
		return primitiveTypes["DINT"]
	case "BOOL_TO_INT":
		return primitiveTypes["INT"]
	case "INT_TO_BOOL":
		return primitiveTypes["BOOL"]
	case "TIME_TO_INT":
		return primitiveTypes["LINT"]
	case "INT_TO_TIME":
		return primitiveTypes["TIME"]
	case "LEN":
		return primitiveTypes["DINT"]
	case "CONCAT":
		return primitiveTypes["STRING"]
	case "MIN", "MAX", "LIMIT", "SEL", "MUX":
		return args[0]
	case "ABS":
		return args[0]
	default:
		// 数学函数统一 LREAL
		return primitiveTypes["LREAL"]
	}
}
