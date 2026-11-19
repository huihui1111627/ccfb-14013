package st

import "fmt"

// TypeKind 类型种类。
type TypeKind int

const (
	TBool TypeKind = iota
	TInt          // 有符号整数族（SINT/INT/DINT/LINT/INT别名）
	TUint         // 无符号整数族（BYTE/WORD/DWORD/LWORD/USINT...）
	TReal         // REAL/LREAL
	TString
	TTime // TIME（纳秒精度）
	TEnum
	TStruct
	TArray
	TFuncBlock // 功能块实例类型
)

// Type 是加载完成后的类型描述。
type Type struct {
	Kind    TypeKind
	Name    string // 声明名（基本类型为规范名）
	Bits    int    // 整数位宽：8/16/32/64；REAL=32, LREAL=64
	Unsigned bool
	// 枚举
	EnumValues map[string]int64
	// 结构体
	Fields []StructField
	// 数组
	ElemType *Type
	Dims     []Dim
	// 功能块
	FB *POU
}

// StructField 结构体字段。
type StructField struct {
	Name string
	Type *Type
}

// Dim 数组维（解析后的常量上下界）。
type Dim struct {
	Low  int64
	High int64
}

func (t *Type) String() string { return t.Name }

// Size 返回该类型一个实例占用的值槽位数（结构体/数组递归展开）。
func (t *Type) Size() int {
	switch t.Kind {
	case TStruct:
		n := 0
		for i := range t.Fields {
			n += t.Fields[i].Type.Size()
		}
		return n
	case TArray:
		n := int64(1)
		for _, d := range t.Dims {
			n *= d.High - d.Low + 1
		}
		return int(n) * t.ElemType.Size()
	default:
		return 1
	}
}

// FieldOffset 返回字段在结构体值槽中的偏移。
func (t *Type) FieldOffset(name string) (int, *Type, error) {
	off := 0
	for i := range t.Fields {
		if t.Fields[i].Name == name {
			return off, t.Fields[i].Type, nil
		}
		off += t.Fields[i].Type.Size()
	}
	return 0, nil, fmt.Errorf("结构体 %s 没有字段 %s", t.Name, name)
}

// DimLength 第 n 维长度。
func (t *Type) DimLength(n int) int64 {
	return t.Dims[n].High - t.Dims[n].Low + 1
}

// IsNumeric 是否数值（整数/实数）。
func (t *Type) IsNumeric() bool {
	return t.Kind == TInt || t.Kind == TUint || t.Kind == TReal
}

// IsIntegral 是否整数。
func (t *Type) IsIntegral() bool { return t.Kind == TInt || t.Kind == TUint }

// AssignableTo 判断源类型能否赋值给目标类型（IEC 隐式转换规则的实用子集）。
func AssignableTo(src, dst *Type) bool {
	if src == dst {
		return true
	}
	if src.Kind == TEnum && dst.Kind == TEnum {
		return src.Name == dst.Name
	}
	if src.Kind == TEnum || dst.Kind == TEnum {
		return false
	}
	if src.Kind == TStruct || dst.Kind == TStruct {
		return src.Name == dst.Name
	}
	if src.Kind == TArray || dst.Kind == TArray {
		return src.Name == dst.Name
	}
	if src.Kind == TFuncBlock || dst.Kind == TFuncBlock {
		return false
	}
	if src.Kind == TTime || dst.Kind == TTime {
		return src.Kind == TTime && dst.Kind == TTime
	}
	if src.Kind == TString || dst.Kind == TString {
		return src.Kind == TString && dst.Kind == TString
	}
	if src.Kind == TBool || dst.Kind == TBool {
		return src.Kind == dst.Kind
	}
	// 数值之间放宽：整数族互转、整数→实数、实数→实数
	return src.IsNumeric() && dst.IsNumeric()
}

// 基本类型表。
var primitiveTypes = map[string]*Type{}

func init() {
	mkInt := func(name string, bits int, unsigned bool) {
		k := TInt
		if unsigned {
			k = TUint
		}
		primitiveTypes[name] = &Type{Kind: k, Name: name, Bits: bits, Unsigned: unsigned}
	}
	mkInt("SINT", 8, false)
	mkInt("INT", 16, false)
	mkInt("DINT", 32, false)
	mkInt("LINT", 64, false)
	mkInt("USINT", 8, true)
	mkInt("UINT", 16, true)
	mkInt("UDINT", 32, true)
	mkInt("ULINT", 64, true)
	mkInt("BYTE", 8, true)
	mkInt("WORD", 16, true)
	mkInt("DWORD", 32, true)
	mkInt("LWORD", 64, true)
	primitiveTypes["REAL"] = &Type{Kind: TReal, Name: "REAL", Bits: 32}
	primitiveTypes["LREAL"] = &Type{Kind: TReal, Name: "LREAL", Bits: 64}
	primitiveTypes["BOOL"] = &Type{Kind: TBool, Name: "BOOL", Bits: 1}
	primitiveTypes["STRING"] = &Type{Kind: TString, Name: "STRING", Bits: 0}
	primitiveTypes["TIME"] = &Type{Kind: TTime, Name: "TIME", Bits: 64}
	primitiveTypes["LTIME"] = &Type{Kind: TTime, Name: "LTIME", Bits: 64}
}
