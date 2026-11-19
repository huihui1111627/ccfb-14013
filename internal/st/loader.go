package st

import (
	"fmt"
	"sort"
	"strings"
)

// Universe 是一次加载形成的类型/POU/全局变量符号宇宙。
type Universe struct {
	Types     map[string]*Type
	POUs      map[string]*POU
	Globals   []*VarDecl
	GlobalSec []*VarSection
	// 原生功能块名集合：TON/TOF/TP/R_TRIG/F_TRIG
	NativeFBs map[string]*Type
	// 原生函数签名：参数类型 nil 表示可变参数且按实参推断
	NativeFuncs map[string]NativeFuncSig
	// 原生函数实现（解释器调用）
	NativeImpl map[string]NativeFuncImpl
}

// NativeFuncSig 原生函数签名信息（用于类型检查）。
type NativeFuncSig struct {
	Variadic bool
	// 返回类型推导：nil 表示按首个数值实参类型
}

// Program 是加载完成、可执行的 ST 程序。
type Program struct {
	Universe *Universe
	Root     *POU
	// 绑定结果（类型检查期填充，解释器读取）
	ExprType map[Expr]*Type
	ExprRef  map[Expr]*RefSpec
	LValue   map[Expr]*LValueSpec
	CallInfo map[*CallExpr]*CallBinding
	ConstVal map[Expr]Value
	// 变量在帧中的槽位布局
	FrameLayout map[*POU]*FrameLayout
	// 全局帧布局
	GlobalLayout *FrameLayout
}

// FrameLayout 描述一个 POU 帧/全局帧的槽位布局。
type FrameLayout struct {
	Size int
	Vars map[string]*VarBinding
}

// RefSpec 描述标量左值的定位方式（绑定期解析，运行期零查表）。
type RefSpec struct {
	Kind   RefKind
	Name   string // Local/Global：变量名
	Base   int    // 聚合变量基槽
	Type   *Type  // 最终叶子类型
	// 字段链（静态偏移在运行期沿类型计算）
	Fields []string
}

type RefKind int

const (
	RefLocal RefKind = iota
	RefGlobal
)

// LValueSpec 是赋值目标的绑定信息。
type LValueSpec struct {
	Root    string
	Global  bool
	IsFB    bool
	Base    int
	RootTy  *Type
	Path    *AccessPath
}

// AccessPath 描述从聚合根到叶子的访问链。
type AccessPath struct {
	Kind   PathKind
	Field  string  // 字段名
	Index  []Expr  // 数组下标表达式（运行期求值）
	Next   *AccessPath
}

type PathKind int

const (
	PathField PathKind = iota
	PathIndex
)

// CallBinding 是调用点的绑定结果。
type CallBinding struct {
	NativeFB bool
	UserFB   bool
	Func     bool
	Native   bool
	Callee   string
	// 命名/位置参数 -> 形参名（大写）；输出参数带 OUT: 前缀已在语法层标记
	ParamOrder []string
	IsOutput   map[string]bool
}

// LoadError 聚合加载期全部诊断。
type LoadError struct {
	Errs []error
}

func (e *LoadError) Error() string {
	ss := make([]string, len(e.Errs))
	for i, x := range e.Errs {
		ss[i] = x.Error()
	}
	return fmt.Sprintf("加载失败，共 %d 个错误:\n  %s", len(e.Errs), strings.Join(ss, "\n  "))
}

type Loader struct {
	units   []*CompilationUnit
	errs    []error
	types   map[string]*Type
	pous    map[string]*POU
	nativeFB map[string]*Type
	anon    int

	exprType map[Expr]*Type
	exprRef  map[Expr]*RefSpec
	lvalue   map[Expr]*LValueSpec
	callInfo map[*CallExpr]*CallBinding
	constVal map[Expr]Value
	layout   map[*POU]*FrameLayout
	globalLayout *FrameLayout
}

// Load 解析并检查若干 ST 源文件，产出可执行 Program（以 rootName 指定的 PROGRAM 为根）。
func Load(rootName string, files map[string]string) (*Program, error) {
	l := &Loader{
		types:    map[string]*Type{},
		pous:     map[string]*POU{},
		nativeFB: map[string]*Type{},
		exprType: map[Expr]*Type{},
		exprRef:  map[Expr]*RefSpec{},
		lvalue:   map[Expr]*LValueSpec{},
		callInfo: map[*CallExpr]*CallBinding{},
		constVal: map[Expr]Value{},
		layout:   map[*POU]*FrameLayout{},
	}
	for name, src := range files {
		cu, err := Parse(name, src)
		if err != nil {
			return nil, err
		}
		l.units = append(l.units, cu)
	}
	l.registerNative()
	l.registerNames()
	l.resolveTypesPhase()
	l.checkGlobalsAndPOUs()
	if len(l.errs) > 0 {
		return nil, &LoadError{Errs: l.errs}
	}
	u := &Universe{
		Types:       l.types,
		POUs:        l.pous,
		NativeFBs:   l.nativeFB,
		NativeFuncs: nativeFuncSigs,
		NativeImpl:  nativeFuncImpls,
	}
	for _, cu := range l.units {
		u.GlobalSec = append(u.GlobalSec, cu.GlobalVars...)
		for _, sec := range cu.GlobalVars {
			u.Globals = append(u.Globals, sec.Variables...)
		}
	}
	root, ok := l.pous[rootName]
	if !ok {
		return nil, fmt.Errorf("找不到根 PROGRAM %q", rootName)
	}
	if root.Kind != KProgram {
		return nil, fmt.Errorf("%q 不是 PROGRAM", rootName)
	}
	gl := l.globalLayout
	return &Program{
		Universe:    u,
		Root:        root,
		ExprType:    l.exprType,
		ExprRef:     l.exprRef,
		LValue:      l.lvalue,
		CallInfo:    l.callInfo,
		ConstVal:    l.constVal,
		FrameLayout: l.layout,
		GlobalLayout: gl,
	}, nil
}

func (l *Loader) errf(line int, format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf("第%d行: %s", line, fmt.Sprintf(format, args...)))
}

func (l *Loader) nodeErr(n Node, format string, args ...any) {
	l.errf(n.Pos(), format, args...)
}

func (l *Loader) registerNative() {
	for _, name := range []string{"TON", "TOF", "TP"} {
		l.nativeFB[name] = &Type{Kind: TFuncBlock, Name: name}
	}
	for _, name := range []string{"R_TRIG", "F_TRIG"} {
		l.nativeFB[name] = &Type{Kind: TFuncBlock, Name: name}
	}
}

// registerNames 先登记所有类型名和 POU 名，支持前向引用。
func (l *Loader) registerNames() {
	for _, cu := range l.units {
		for _, td := range cu.Types {
			if _, dup := l.types[td.Name]; dup {
				l.nodeErr(td, "类型 %s 重复定义", td.Name)
			}
			// 占位，具体内容在 resolve 阶段填
			l.types[td.Name] = nil
		}
		for _, pou := range cu.POUs {
			if _, dup := l.pous[pou.Name]; dup {
				l.nodeErr(pou, "POU %s 重复定义", pou.Name)
			}
			l.pous[pou.Name] = pou
		}
	}
}

func (l *Loader) resolveTypesPhase() {
	// 自定义类型可能互相引用，递归解析。
	visiting := map[string]bool{}
	var resolve func(ref *TypeRef) *Type
	resolve = func(ref *TypeRef) *Type {
		if ref.ArrayOf != nil {
			elem := resolve(ref.ArrayOf)
			if elem == nil {
				return nil
			}
			dims := make([]Dim, len(ref.Dims))
			for i, r := range ref.Dims {
				lo, ok1 := l.constInt(r.Low)
				hi, ok2 := l.constInt(r.High)
				if !ok1 || !ok2 {
					l.errf(ref.Line, "数组上下界必须是常量表达式")
					return nil
				}
				if hi < lo {
					l.errf(ref.Line, "数组上界 %d 小于下界 %d", hi, lo)
					return nil
				}
				dims[i] = Dim{Low: lo, High: hi}
			}
			// 匿名数组类型名：ARRAY<...>
			return &Type{Kind: TArray, Name: arrayTypeName(elem, dims), ElemType: elem, Dims: dims}
		}
		// 内联枚举
		if strings.HasPrefix(ref.Name, "__ANON_ENUM__:") {
			l.anon++
			t := &Type{Kind: TEnum, Name: fmt.Sprintf("__ANON_%d__", l.anon), EnumValues: map[string]int64{}}
			vals := strings.Split(strings.TrimPrefix(ref.Name, "__ANON_ENUM__:"), ":")
			for i, v := range vals {
				if v == "" {
					continue
				}
				t.EnumValues[v] = int64(i)
			}
			return t
		}
		// 内联结构体：字段直接挂在 TypeRef.Struct 上
		if ref.Struct != nil {
			return l.resolveStructRef(ref, resolve)
		}
		if pt, ok := primitiveTypes[ref.Name]; ok {
			return pt
		}
		if t, ok := l.types[ref.Name]; ok && t != nil {
			return t
		}
		var decl *TypeDecl
		for _, cu := range l.units {
			for _, td := range cu.Types {
				if td.Name == ref.Name {
					decl = td
				}
			}
		}
		if decl == nil {
			l.errf(ref.Line, "未知类型 %q", ref.Name)
			return nil
		}
		if visiting[decl.Name] {
			l.errf(ref.Line, "类型 %s 存在循环定义", decl.Name)
			return nil
		}
		visiting[decl.Name] = true
		t := resolve(&decl.Type)
		delete(visiting, decl.Name)
		if t != nil {
			// 声明类型保留声明名，数组别名也一样
			named := *t
			named.Name = decl.Name
			t = &named
			l.types[decl.Name] = t
		}
		return t
	}
	for _, cu := range l.units {
		for _, td := range cu.Types {
			if l.types[td.Name] != nil {
				continue // 已解析（递归引用时提前完成）
			}
			visiting[td.Name] = true
			t := resolve(&td.Type)
			delete(visiting, td.Name)
			if t != nil {
				named := *t
				named.Name = td.Name
				l.types[td.Name] = &named
			}
		}
	}
}

func arrayTypeName(elem *Type, dims []Dim) string {
	var b strings.Builder
	b.WriteString("ARRAY[")
	for i, d := range dims {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%d..%d", d.Low, d.High)
	}
	b.WriteString("]OF_")
	b.WriteString(elem.Name)
	return b.String()
}

// resolveStructRef 从 TypeRef.Struct 构建结构体类型，递归解析每个字段类型
// （数组、嵌套结构体都通过完整 TypeRef 解析，不再走字符串编码）。
func (l *Loader) resolveStructRef(ref *TypeRef, resolve func(*TypeRef) *Type) *Type {
	l.anon++
	t := &Type{Kind: TStruct, Name: fmt.Sprintf("__STRUCT_%d__", l.anon)}
	for _, d := range ref.Struct {
		ft := resolve(&d.Type)
		if ft == nil {
			return nil
		}
		for _, fn := range d.Names {
			t.Fields = append(t.Fields, StructField{Name: fn, Type: ft})
		}
	}
	return t
}

// checkGlobalsAndPOUs 做声明级检查、类型检查与帧布局。
func (l *Loader) checkGlobalsAndPOUs() {
	chk := newChecker(l)
	// 把所有枚举值登记到全局作用域，支持裸枚举值引用（GREEN）
	for _, ty := range l.types {
		if ty == nil || ty.Kind != TEnum {
			continue
		}
		for val := range ty.EnumValues {
			if _, dup := chk.scopes[0][val]; !dup {
				chk.scopes[0][val] = &scopeEntry{Type: ty}
			}
		}
	}
	// 全局变量检查 + 直接分配全局帧布局
	globalLayout := &FrameLayout{Vars: map[string]*VarBinding{}}
	names := map[string]bool{}
	for _, cu := range l.units {
		for _, sec := range cu.GlobalVars {
			for _, d := range sec.Variables {
				for _, n := range d.Names {
					if names[n] {
						l.nodeErr(d, "全局变量 %s 重复声明", n)
					}
					names[n] = true
				}
				chk.checkDecl(d, true, globalLayout)
			}
		}
	}
	l.globalLayout = globalLayout
	// POU 声明与函数体
	for _, pou := range l.pous {
		chk.checkPOU(pou)
	}
}

// constInt 求常量整数表达式（数组界、CASE 标签等）。
func (l *Loader) constInt(e Expr) (int64, bool) {
	v, ok := l.foldConst(e)
	if !ok {
		return 0, false
	}
	switch v.Type.Kind {
	case TInt, TUint, TEnum:
		return v.AsInt(), true
	}
	return 0, false
}

// sortedUnits 仅供调试输出使用。
func sortedUnits(m map[string]*POU) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
