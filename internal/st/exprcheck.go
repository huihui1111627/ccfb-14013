package st

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// exprTypeOf 求表达式类型，同时把结果记入 ExprType 并生成左值绑定。
func (c *checker) exprTypeOf(e Expr) *Type {
	if t, ok := c.l.exprType[e]; ok {
		return t
	}
	var t *Type
	switch n := e.(type) {
	case *LiteralExpr:
		t = c.litType(n)
	case *EnumExpr:
		t = c.enumType(n)
	case *IdentExpr:
		t = c.identType(e, n)
	case *UnaryExpr:
		t = c.unaryType(n)
	case *BinaryExpr:
		t = c.binaryType(n)
	case *IndexExpr:
		t = c.indexType(n)
	case *FieldExpr:
		t = c.fieldType(n)
	case *CallExpr:
		t = c.checkCall(n)
	default:
		c.l.nodeErr(e, "未知表达式节点")
	}
	if t != nil {
		c.l.exprType[e] = t
	}
	return t
}

func (c *checker) litType(n *LiteralExpr) *Type {
	switch n.Kind {
	case LitBool:
		return primitiveTypes["BOOL"]
	case LitString:
		return primitiveTypes["STRING"]
	case LitInt:
		// 基数字 16#/2#/8#：按位宽选 WORD 族
		if strings.Contains(n.Raw, "#") {
			return c.bitStringType(n)
		}
		if strings.ContainsAny(n.Raw, ".eE") {
			return primitiveTypes["LREAL"]
		}
		// 十进制常量按取值范围选最小有符号类型，表达式需要时再放宽
		v, err := strconv.ParseInt(n.Raw, 10, 64)
		if err != nil {
			c.l.nodeErr(n, "整数字面量越界: %s", n.Raw)
			return primitiveTypes["DINT"]
		}
		return intLiteralType(v)
	case LitReal:
		return primitiveTypes["LREAL"]
	case LitTime:
		return primitiveTypes["TIME"]
	}
	return nil
}

func intLiteralType(v int64) *Type {
	switch {
	case v >= math.MinInt16 && v <= math.MaxInt16:
		return primitiveTypes["INT"]
	case v >= math.MinInt32 && v <= math.MaxInt32:
		return primitiveTypes["DINT"]
	default:
		return primitiveTypes["LINT"]
	}
}

func (c *checker) bitStringType(n *LiteralExpr) *Type {
	parts := strings.SplitN(n.Raw, "#", 2)
	digits := strings.ReplaceAll(parts[1], "_", "")
	bitsPerChar := map[string]int{"2": 1, "8": 3, "16": 4}[parts[0]]
	bits := len(digits) * bitsPerChar
	switch {
	case bits <= 8:
		return primitiveTypes["BYTE"]
	case bits <= 16:
		return primitiveTypes["WORD"]
	case bits <= 32:
		return primitiveTypes["DWORD"]
	default:
		return primitiveTypes["LWORD"]
	}
}

func (c *checker) enumType(n *EnumExpr) *Type {
	if n.EnumType != "" {
		t, ok := c.l.types[n.EnumType]
		if !ok || t.Kind != TEnum {
			c.l.nodeErr(n, "%s 不是枚举类型", n.EnumType)
			return nil
		}
		if _, ok := t.EnumValues[n.Value]; !ok {
			c.l.nodeErr(n, "枚举 %s 没有值 %s", n.EnumType, n.Value)
		}
		return t
	}
	// 裸枚举值：在所有枚举类型中查找
	for _, t := range c.l.types {
		if t.Kind == TEnum {
			if _, ok := t.EnumValues[n.Value]; ok {
				return t
			}
		}
	}
	c.l.nodeErr(n, "未定义的枚举值 %s", n.Value)
	return nil
}

func (c *checker) identType(e Expr, n *IdentExpr) *Type {
	entry, ok := c.lookup(n.Name)
	if !ok || entry == nil || entry.Type == nil {
		c.l.nodeErr(e, "未声明的标识符 %s", n.Name)
		return nil
	}
	// 枚举常量（无 Decl）：只给类型，不建左值绑定
	if entry.Decl == nil && entry.Type.Kind == TEnum {
		return entry.Type
	}
	// 绑定左值规格
	isGlobal := false
	if _, local := c.scopeLookup(n.Name); !local {
		isGlobal = true
	}
	if entry.Type.Kind != TFuncBlock {
		c.bindLValue(e, n.Name, isGlobal, entry.Type, nil)
	}
	return entry.Type
}

func (c *checker) scopeLookup(name string) (*scopeEntry, bool) {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if e, ok := c.scopes[i][name]; ok {
			return e, true
		}
	}
	return nil, false
}

func (c *checker) unaryType(n *UnaryExpr) *Type {
	inner := c.exprTypeOf(n.Expr)
	if inner == nil {
		return nil
	}
	switch n.Op {
	case "NOT":
		if inner.Kind != TBool && !inner.IsIntegral() {
			c.l.nodeErr(n, "NOT 只能作用于 BOOL 或位串类型")
			return nil
		}
		return inner
	case "-":
		if !inner.IsNumeric() && inner.Kind != TTime {
			c.l.nodeErr(n, "一元负号要求数值或 TIME 类型")
			return nil
		}
		return inner
	}
	return nil
}

func (c *checker) binaryType(n *BinaryExpr) *Type {
	lt := c.exprTypeOf(n.Left)
	rt := c.exprTypeOf(n.Right)
	if lt == nil || rt == nil {
		return nil
	}
	switch n.Op {
	case "AND", "OR", "XOR":
		if lt.Kind == TBool && rt.Kind == TBool {
			return primitiveTypes["BOOL"]
		}
		if lt.IsIntegral() && rt.IsIntegral() {
			return c.widenInt(lt, rt)
		}
		c.l.nodeErr(n, "%s 运算要求 BOOL 或整数位串操作数", n.Op)
		return nil
	case "=", "<>":
		if !comparable(lt, rt) {
			c.l.nodeErr(n, "不能比较类型 %s 与 %s", lt.Name, rt.Name)
			return nil
		}
		return primitiveTypes["BOOL"]
	case "<", ">", "<=", ">=":
		if lt.Kind == TString && rt.Kind == TString {
			return primitiveTypes["BOOL"]
		}
		if !(lt.IsNumeric() && rt.IsNumeric()) && !(lt.Kind == TTime && rt.Kind == TTime) &&
			!(lt.Kind == TEnum && lt == rt) {
			c.l.nodeErr(n, "大小比较要求数值/TIME/同枚举操作数")
			return nil
		}
		return primitiveTypes["BOOL"]
	case "+", "-", "*", "/":
		if lt.Kind == TTime && rt.Kind == TTime {
			return primitiveTypes["TIME"]
		}
		if lt.Kind == TTime && rt.IsNumeric() {
			return primitiveTypes["TIME"]
		}
		if lt.IsNumeric() && rt.IsNumeric() {
			if lt.Kind == TReal || rt.Kind == TReal {
				return c.widenReal(lt, rt)
			}
			return c.widenInt(lt, rt)
		}
		c.l.nodeErr(n, "运算 %s 不支持类型 %s 与 %s", n.Op, lt.Name, rt.Name)
		return nil
	case "MOD":
		if !(lt.IsIntegral() && rt.IsIntegral()) {
			c.l.nodeErr(n, "MOD 要求整数操作数")
			return nil
		}
		return c.widenInt(lt, rt)
	case "**":
		if !(lt.IsNumeric() && rt.IsNumeric()) {
			c.l.nodeErr(n, "** 要求数值操作数")
			return nil
		}
		return c.widenReal(lt, rt)
	case "..":
		// CASE 区间标签，类型取左侧
		return lt
	}
	c.l.nodeErr(n, "未知运算符 %s", n.Op)
	return nil
}

func comparable(a, b *Type) bool {
	if a == b {
		return true
	}
	if a.Kind == TEnum || b.Kind == TEnum {
		return a == b
	}
	if a.Kind == TString || b.Kind == TString {
		return a.Kind == b.Kind
	}
	if a.Kind == TTime || b.Kind == TTime {
		return a.Kind == b.Kind
	}
	return a.IsNumeric() && b.IsNumeric() || a.Kind == TBool && b.Kind == TBool
}

func (c *checker) widenInt(a, b *Type) *Type {
	if a.Bits > b.Bits {
		return a
	}
	return b
}

func (c *checker) widenReal(a, b *Type) *Type {
	if a.Kind == TReal || b.Kind == TReal {
		if a.Bits == 64 || b.Bits == 64 {
			return primitiveTypes["LREAL"]
		}
		return primitiveTypes["REAL"]
	}
	return primitiveTypes["LREAL"]
}

func (c *checker) indexType(n *IndexExpr) *Type {
	at := c.exprTypeOf(n.Array)
	if at == nil {
		return nil
	}
	if at.Kind != TArray {
		c.l.nodeErr(n, "只能对数组做下标访问，实际类型 %s", at.Name)
		return nil
	}
	if len(n.Index) != len(at.Dims) {
		c.l.nodeErr(n, "数组维数 %d 与下标个数 %d 不一致", len(at.Dims), len(n.Index))
	}
	for _, ie := range n.Index {
		it := c.exprTypeOf(ie)
		if it != nil && !it.IsIntegral() {
			c.l.nodeErr(n, "数组下标必须是整数")
		}
	}
	// 左值绑定：沿已有根规格追加索引路径
	c.extendAggregate(n, n.Array, &AccessPath{Kind: PathIndex, Index: n.Index})
	return at.ElemType
}

func (c *checker) fieldType(n *FieldExpr) *Type {
	ot := c.exprTypeOf(n.Object)
	if ot == nil {
		return nil
	}
	// 功能块实例字段访问
	if ot.Kind == TFuncBlock {
		// 原生 FB 的 IN/OUT 由调用参数处理，字段访问只允许用户 FB
		if ot.FB == nil {
			c.l.nodeErr(n, "原生功能块 %s 的成员只能通过调用参数访问", ot.Name)
			return nil
		}
		if ft := c.findMemberType(ot.FB, n.Field); ft != nil {
			c.extendAggregate(n, n.Object, &AccessPath{Kind: PathField, Field: n.Field})
			return ft
		}
		c.l.nodeErr(n, "功能块 %s 没有成员 %s", ot.Name, n.Field)
		return nil
	}
	if ot.Kind != TStruct {
		c.l.nodeErr(n, "类型 %s 没有字段", ot.Name)
		return nil
	}
	for _, f := range ot.Fields {
		if f.Name == n.Field {
			c.extendAggregate(n, n.Object, &AccessPath{Kind: PathField, Field: n.Field})
			return f.Type
		}
	}
	c.l.nodeErr(n, "结构体 %s 没有字段 %s", ot.Name, n.Field)
	return nil
}

func (c *checker) findMemberType(pou *POU, name string) *Type {
	for _, sec := range pou.Sections {
		for _, d := range sec.Variables {
			for _, vn := range d.Names {
				if vn == name {
					return c.l.resolveVarType(&d.Type)
				}
			}
		}
	}
	return nil
}

// orderedMemberNames 按声明顺序返回 POU 的形参/成员名。
func orderedMemberNames(pou *POU) []string {
	var out []string
	for _, sec := range pou.Sections {
		for _, d := range sec.Variables {
			out = append(out, d.Names...)
		}
	}
	return out
}

// orderedNativeParams 返回原生功能块标准参数的固定顺序。
func orderedNativeParams(kind string) []string {
	switch kind {
	case "TON", "TOF", "TP":
		return []string{"IN", "PT", "Q", "ET"}
	case "R_TRIG", "F_TRIG":
		return []string{"CLK", "Q"}
	}
	return nil
}

// ---------- 左值绑定 ----------

func (c *checker) bindLValue(e Expr, root string, global bool, t *Type, path *AccessPath) {
	spec := &LValueSpec{Root: root, Global: global, RootTy: t, Path: path}
	c.l.lvalue[e] = spec
}

// extendAggregate 把数组/字段访问接到内层表达式的左值规格上。
func (c *checker) extendAggregate(e Expr, inner Expr, step *AccessPath) {
	base, ok := c.l.lvalue[inner]
	if !ok {
		// 内层可能是 FB 实例的 IdentExpr（不建普通左值）
		if id, ok2 := inner.(*IdentExpr); ok2 {
			entry, _ := c.lookup(id.Name)
			if entry != nil && entry.Type.Kind == TFuncBlock {
				_, isLocal := c.scopeLookup(id.Name)
				spec := &LValueSpec{
					Root: id.Name, Global: !isLocal, IsFB: true,
					RootTy: entry.Type, Path: step,
				}
				c.l.lvalue[e] = spec
				return
			}
		}
		c.l.nodeErr(e, "聚合访问的根不是左值")
		return
	}
	// 深拷贝内层访问链，再在尾部挂上新 step，避免污染内层表达式的规格
	var path *AccessPath
	if base.Path != nil {
		path = clonePath(base.Path)
		tail := path
		for tail.Next != nil {
			tail = tail.Next
		}
		tail.Next = step
	} else {
		path = step
	}
	c.l.lvalue[e] = &LValueSpec{
		Root: base.Root, Global: base.Global, IsFB: base.IsFB,
		RootTy: base.RootTy, Path: path,
	}
}

// ---------- 调用检查与绑定 ----------

func (c *checker) checkCall(n *CallExpr) *Type {
	// 原生功能块实例调用：timer(IN:=..., PT:=...)
	if entry, ok := c.lookup(n.Callee); ok && entry.Type.Kind == TFuncBlock {
		return c.checkFBCall(n, entry)
	}
	// 原生函数
	if sig, ok := nativeFuncSigs[n.Callee]; ok {
		return c.checkNativeFunc(n, sig)
	}
	// 用户 FUNCTION
	if pou, ok := c.l.pous[n.Callee]; ok && pou.Kind == KFunction {
		return c.checkUserFunc(n, pou)
	}
	// 用户 FUNCTION_BLOCK 不能直接当函数调，必须先实例化
	if pou, ok := c.l.pous[n.Callee]; ok && pou.Kind == KFunctionBlock {
		c.l.nodeErr(n, "功能块 %s 必须先声明实例再调用", n.Callee)
		return nil
	}
	c.l.nodeErr(n, "未定义的函数或功能块 %s", n.Callee)
	return nil
}

func (c *checker) checkFBCall(n *CallExpr, entry *scopeEntry) *Type {
	bind := &CallBinding{Callee: n.Callee, IsOutput: map[string]bool{}}
	if _, ok := c.l.nativeFB[entry.Type.Name]; ok {
		bind.NativeFB = true
	} else {
		bind.UserFB = true
	}
	// 实例本身是左值根；解释器按 Callee 名字直接查实例帧
	_, isLocal := c.scopeLookup(n.Callee)
	bind.ParamOrder, bind.IsOutput = c.bindArgs(n, entry.Type, isLocal)
	c.l.callInfo[n] = bind
	// FB 调用语句无返回值；作为表达式时取其 Q（仅 TON/TOF/TP/R_TRIG/F_TRIG）
	return primitiveTypes["BOOL"]
}

func (c *checker) bindArgs(n *CallExpr, fbTy *Type, _ bool) ([]string, map[string]bool) {
	order := []string{}
	out := map[string]bool{}
	defined := map[string]bool{}
	// 形参表：原生 FB 用标准成员表，用户 FB 从 POU 声明取
	var params map[string]*Type
	var paramNames []string
	if fbTy.FB != nil {
		params = map[string]*Type{}
		paramNames = orderedMemberNames(fbTy.FB)
		for _, sec := range fbTy.FB.Sections {
			for _, d := range sec.Variables {
				t := c.l.resolveVarType(&d.Type)
				for _, vn := range d.Names {
					params[vn] = t
				}
			}
		}
	} else {
		params = nativeFBParams(fbTy.Name)
		paramNames = orderedNativeParams(fbTy.Name)
	}
	positional := true
	posIdx := 0
	for i, arg := range n.Args {
		name := arg.Name
		isOut := false
		if strings.HasPrefix(name, "OUT:") {
			name = strings.TrimPrefix(name, "OUT:")
			isOut = true
		}
		if name != "" {
			positional = false
		} else if !positional {
			c.l.nodeErr(n, "位置参数不能出现在命名参数之后")
			continue
		} else {
			// 位置参数按形参声明顺序匹配
			if posIdx >= len(paramNames) {
				c.l.nodeErr(n, "功能块 %s 位置参数过多", fbTy.Name)
				continue
			}
			name = paramNames[posIdx]
			posIdx++
		}
		pt, ok := params[name]
		if !ok {
			c.l.nodeErr(n, "功能块 %s 没有参数 %s", fbTy.Name, name)
			continue
		}
		if defined[name] {
			c.l.nodeErr(n, "参数 %s 重复给出", name)
		}
		defined[name] = true
		at := c.exprTypeOf(arg.Expr)
		if at != nil && !isOut && !AssignableTo(at, pt) {
			c.l.nodeErr(n, "参数 %s 类型不匹配: 传入 %s，要求 %s", name, at.Name, pt.Name)
		}
		if isOut {
			if !isLValue(arg.Expr) {
				c.l.nodeErr(n, "输出参数 %s 必须接左值", name)
			}
			out[name] = true
		}
		order = append(order, name)
		// 把实参名回写到 AST 供解释器使用
		n.Args[i].Name = name
		if isOut {
			n.Args[i].Name = "OUT:" + name
		}
	}
	return order, out
}

// nativeFBParams 返回原生功能块的标准参数类型表。
func nativeFBParams(kind string) map[string]*Type {
	boolT := primitiveTypes["BOOL"]
	timeT := primitiveTypes["TIME"]
	switch kind {
	case "TON", "TOF", "TP":
		return map[string]*Type{"IN": boolT, "PT": timeT, "Q": boolT, "ET": timeT}
	case "R_TRIG", "F_TRIG":
		return map[string]*Type{"CLK": boolT, "Q": boolT}
	}
	return map[string]*Type{}
}

func (c *checker) checkNativeFunc(n *CallExpr, sig NativeFuncSig) *Type {
	var argTypes []*Type
	for _, a := range n.Args {
		t := c.exprTypeOf(a.Expr)
		if t != nil {
			argTypes = append(argTypes, t)
		}
	}
	rt := nativeFuncReturnType(n.Callee, argTypes)
	if rt == nil {
		c.l.nodeErr(n, "函数 %s 参数类型不合法", n.Callee)
		return primitiveTypes["DINT"]
	}
	c.l.callInfo[n] = &CallBinding{Callee: n.Callee, Native: true}
	return rt
}

func (c *checker) checkUserFunc(n *CallExpr, pou *POU) *Type {
	bind := &CallBinding{Callee: n.Callee, Func: true, IsOutput: map[string]bool{}}
	params := map[string]*Type{}
	var paramNames []string
	for _, sec := range pou.Sections {
		if sec.Kind != VarInput && sec.Kind != VarInOut && sec.Kind != VarOutput {
			continue
		}
		for _, d := range sec.Variables {
			t := c.l.resolveVarType(&d.Type)
			for _, vn := range d.Names {
				params[vn] = t
				paramNames = append(paramNames, vn)
			}
		}
	}
	positional := true
	defined := map[string]bool{}
	posIdx := 0
	for i, arg := range n.Args {
		name := arg.Name
		if strings.HasPrefix(name, "OUT:") {
			name = strings.TrimPrefix(name, "OUT:")
			bind.IsOutput[name] = true
		}
		if name != "" {
			positional = false
		} else if !positional {
			c.l.nodeErr(n, "位置参数不能出现在命名参数之后")
		} else {
			if posIdx >= len(paramNames) {
				c.l.nodeErr(n, "函数 %s 位置参数过多", pou.Name)
				continue
			}
			name = paramNames[posIdx]
			posIdx++
		}
		pt, ok := params[name]
		if !ok {
			c.l.nodeErr(n, "函数 %s 没有参数 %s", pou.Name, name)
			continue
		}
		if defined[name] {
			c.l.nodeErr(n, "参数 %s 重复", name)
		}
		defined[name] = true
		at := c.exprTypeOf(arg.Expr)
		if at != nil && pt != nil && !AssignableTo(at, pt) {
			c.l.nodeErr(n, "参数 %s 类型不匹配: %s -> %s", name, at.Name, pt.Name)
		}
		bind.ParamOrder = append(bind.ParamOrder, name)
		n.Args[i].Name = name
	}
	c.l.callInfo[n] = bind
	rt := c.l.resolveVarType(pou.ReturnType)
	return rt
}

// ---------- 常量折叠 ----------

// foldConst 求编译期常量；返回 ok=false 表示非常量。
func (l *Loader) foldConst(e Expr) (Value, bool) {
	if v, ok := l.constVal[e]; ok {
		return v, true
	}
	switch n := e.(type) {
	case *LiteralExpr:
		v, ok := evalConstLiteral(n)
		if ok {
			l.constVal[e] = v
		}
		return v, ok
	case *IdentExpr:
		// 裸枚举常量
		for _, t := range l.types {
			if t != nil && t.Kind == TEnum {
				if iv, ok := t.EnumValues[n.Name]; ok {
					v := Value{Type: t, V: iv}
					l.constVal[e] = v
					return v, true
				}
			}
		}
		return Value{}, false
	case *EnumExpr:
		for _, t := range l.types {
			if t.Kind == TEnum {
				if iv, ok := t.EnumValues[n.Value]; ok {
					if n.EnumType != "" && t.Name != n.EnumType {
						continue
					}
					v := Value{Type: t, V: iv}
					l.constVal[e] = v
					return v, true
				}
			}
		}
		return Value{}, false
	case *UnaryExpr:
		v, ok := l.foldConst(n.Expr)
		if !ok {
			return Value{}, false
		}
		switch n.Op {
		case "-":
			if v.Type.Kind == TTime {
				v.V = -v.V.(int64)
			} else if v.Type.IsIntegral() {
				v.V = -v.AsInt()
			} else {
				v.V = -v.AsReal()
			}
		case "NOT":
			if v.Type.Kind == TBool {
				v.V = !v.AsBool()
			} else {
				v.V = ^v.AsUint()
			}
		}
		l.constVal[e] = v
		return v, true
	case *BinaryExpr:
		a, ok1 := l.foldConst(n.Left)
		b, ok2 := l.foldConst(n.Right)
		if !ok1 || !ok2 {
			return Value{}, false
		}
		v, ok := foldBinary(n.Op, a, b)
		if ok {
			l.constVal[e] = v
		}
		return v, ok
	}
	return Value{}, false
}

func evalConstLiteral(n *LiteralExpr) (Value, bool) {
	switch n.Kind {
	case LitBool:
		return BoolVal(n.Raw == "TRUE"), true
	case LitString:
		s, _ := decodeString(n.Raw)
		return StrVal(s), true
	case LitTime:
		d, err := ParseTimeLiteral(n.Raw)
		if err != nil {
			return Value{}, false
		}
		return Value{Type: primitiveTypes["TIME"], V: int64(d)}, true
	case LitInt:
		if strings.Contains(n.Raw, "#") {
			v, err := parseBitString(n.Raw)
			if err != nil {
				return Value{}, false
			}
			return Value{Type: bitStringTypeStatic(n.Raw), V: v}, true
		}
		if strings.ContainsAny(n.Raw, ".eE") {
			f, err := strconv.ParseFloat(n.Raw, 64)
			if err != nil {
				return Value{}, false
			}
			return RealVal(primitiveTypes["LREAL"], f), true
		}
		iv, err := strconv.ParseInt(n.Raw, 10, 64)
		if err != nil {
			return Value{}, false
		}
		return IntVal(intLiteralType(iv), iv), true
	}
	return Value{}, false
}

func foldBinary(op string, a, b Value) (Value, bool) {
	// 只覆盖常量折叠需要的算术/逻辑子集，运行期完整语义在 interp.go。
	switch op {
	case "+":
		if a.Type.Kind == TTime {
			return Value{Type: a.Type, V: a.V.(int64) + b.AsInt()}, true
		}
		if a.Type.Kind == TReal || b.Type.Kind == TReal {
			return RealVal(primitiveTypes["LREAL"], a.AsReal()+b.AsReal()), true
		}
		return IntVal(primitiveTypes["DINT"], a.AsInt()+b.AsInt()), true
	case "-":
		if a.Type.Kind == TTime {
			return Value{Type: a.Type, V: a.V.(int64) - b.AsInt()}, true
		}
		if a.Type.Kind == TReal || b.Type.Kind == TReal {
			return RealVal(primitiveTypes["LREAL"], a.AsReal()-b.AsReal()), true
		}
		return IntVal(primitiveTypes["DINT"], a.AsInt()-b.AsInt()), true
	case "*":
		if a.Type.Kind == TReal || b.Type.Kind == TReal {
			return RealVal(primitiveTypes["LREAL"], a.AsReal()*b.AsReal()), true
		}
		return IntVal(primitiveTypes["DINT"], a.AsInt()*b.AsInt()), true
	case "AND":
		if a.Type.Kind == TBool {
			return BoolVal(a.AsBool() && b.AsBool()), true
		}
		return Value{Type: a.Type, V: a.AsUint() & b.AsUint()}, true
	case "OR":
		if a.Type.Kind == TBool {
			return BoolVal(a.AsBool() || b.AsBool()), true
		}
		return Value{Type: a.Type, V: a.AsUint() | b.AsUint()}, true
	case "XOR":
		if a.Type.Kind == TBool {
			return BoolVal(a.AsBool() != b.AsBool()), true
		}
		return Value{Type: a.Type, V: a.AsUint() ^ b.AsUint()}, true
	}
	return Value{}, false
}

// ParseTimeLiteral 解析 T#1d2h3m4s5ms / TIME#...（纳秒精度）。
func ParseTimeLiteral(raw string) (time.Duration, error) {
	s := strings.ToUpper(raw)
	s = strings.TrimPrefix(s, "LTIME#")
	s = strings.TrimPrefix(s, "TIME#")
	s = strings.TrimPrefix(s, "T#")
	s = strings.ReplaceAll(s, "_", "")
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("空时间字面量 %q", raw)
	}
	var total time.Duration
	i := 0
	for i < len(s) {
		// 数字段（允许小数）
		j := i
		for j < len(s) && (s[j] == '.' || (s[j] >= '0' && s[j] <= '9')) {
			j++
		}
		if j == i {
			return 0, fmt.Errorf("时间字面量格式错误: %q", raw)
		}
		num, err := strconv.ParseFloat(s[i:j], 64)
		if err != nil {
			return 0, fmt.Errorf("时间字面量格式错误: %q", raw)
		}
		// 单位段
		k := j
		for k < len(s) && s[k] >= 'A' && s[k] <= 'Z' {
			k++
		}
		unit := s[j:k]
		if unit == "" {
			return 0, fmt.Errorf("时间字面量缺少单位: %q", raw)
		}
		mult, ok := timeUnits[unit]
		if !ok {
			return 0, fmt.Errorf("未知时间单位 %q in %q", unit, raw)
		}
		total += time.Duration(num * float64(mult))
		i = k
	}
	if neg {
		total = -total
	}
	return total, nil
}

var timeUnits = map[string]time.Duration{
	"D":  24 * time.Hour,
	"H":  time.Hour,
	"M":  time.Minute,
	"S":  time.Second,
	"MS": time.Millisecond,
	"US": time.Microsecond,
	"NS": time.Nanosecond,
}

func parseBitString(raw string) (uint64, error) {
	parts := strings.SplitN(raw, "#", 2)
	base := map[string]int{"2": 2, "8": 8, "16": 16}[parts[0]]
	if base == 0 {
		return 0, fmt.Errorf("非法基数: %s", raw)
	}
	digits := strings.ReplaceAll(parts[1], "_", "")
	v, err := strconv.ParseUint(digits, base, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func bitStringTypeStatic(raw string) *Type {
	parts := strings.SplitN(raw, "#", 2)
	digits := strings.ReplaceAll(parts[1], "_", "")
	bitsPerChar := map[string]int{"2": 1, "8": 3, "16": 4}[parts[0]]
	bits := len(digits) * bitsPerChar
	switch {
	case bits <= 8:
		return primitiveTypes["BYTE"]
	case bits <= 16:
		return primitiveTypes["WORD"]
	case bits <= 32:
		return primitiveTypes["DWORD"]
	default:
		return primitiveTypes["LWORD"]
	}
}

// decodeString 处理 IEC 字符串转义（$' $" $$ $R $N $T 等）。
func decodeString(raw string) (string, error) {
	s := strings.Trim(raw, "'")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			break
		}
		switch s[i] {
		case '$':
			b.WriteByte('$')
		case '\'':
			b.WriteByte('\'')
		case 'R', 'r':
			b.WriteByte('\r')
		case 'N', 'n':
			b.WriteByte('\n')
		case 'T', 't':
			b.WriteByte('\t')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), nil
}

// clonePath 深拷贝访问链（下标表达式节点可共享，因为只读）。
func clonePath(p *AccessPath) *AccessPath {
	if p == nil {
		return nil
	}
	return &AccessPath{
		Kind:  p.Kind,
		Field: p.Field,
		Index: p.Index,
		Next:  clonePath(p.Next),
	}
}
