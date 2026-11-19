package st

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Checkpointer 由运行时调度器实现：解释器在语句边界与循环回填处调用，
// 用于优先级抢占、看门狗超时检测和在线换程序挂起。
type Checkpointer interface {
	Checkpoint(line int) error
}

// ExecEnv 是一次任务执行的完整环境。
type ExecEnv struct {
	Prog *Program
	Root *Frame
	CP   Checkpointer
	Now  time.Duration // 本拍虚拟时刻
	DT   time.Duration // 本任务周期
	// 运行时函数表（原生 FB 工厂由外部注入，避免 st 包依赖 fb 包）
	NewNative func(kind string) NativeFB
}// RuntimeError 是执行期错误（含行号）。
type RuntimeError struct {
	Line int
	Msg  string
}

func (e *RuntimeError) Error() string {
	return fmt.Sprintf("运行时错误 第%d行: %s", e.Line, e.Msg)
}

// ---------- 帧实例化 ----------

// Instantiate 为 POU 建帧：分配槽位、零值/初值、递归创建 FB 实例。
func (env *ExecEnv) Instantiate(pou *POU, parent *Frame) *Frame {
	layout := env.Prog.FrameLayout[pou]
	f := &Frame{
		POU:    pou,
		Slots:  make([]Value, layout.Size),
		Vars:   map[string]*VarBinding{},
		Parent: parent,
	}
	for name, b := range layout.Vars {
		nb := &VarBinding{Type: b.Type, Base: b.Base, IsFB: b.IsFB}
		f.Vars[name] = nb
	}
	env.fillSlots(pou, f)
	// 功能块实例递归
	for name, b := range f.Vars {
		if !b.IsFB {
			continue
		}
		b.FB = env.instantiateFB(b.Type, f, name)
	}
	return f
}

func (env *ExecEnv) instantiateFB(t *Type, parent *Frame, varName string) *Frame {
	if native, ok := env.Prog.Universe.NativeFBs[t.Name]; ok {
		_ = native
		nf := &Frame{Vars: map[string]*VarBinding{}, Parent: parent}
		nf.Native = env.NewNative(t.Name)
		return nf
	}
	pou := t.FB
	return env.Instantiate(pou, parent)
}

// fillSlots 填零值与声明初值。
func (env *ExecEnv) fillSlots(pou *POU, f *Frame) {
	layout := env.Prog.FrameLayout[pou]
	// 先全部零值
	for i := range f.Slots {
		f.Slots[i] = Value{}
	}
	// 按变量类型递归零填充
	for name, b := range layout.Vars {
		if b.IsFB {
			continue
		}
		env.zeroFill(f, b.Base, b.Type)
		_ = name
	}
	// 声明初值
	for _, sec := range pou.Sections {
		for _, d := range sec.Variables {
			t := env.Prog.Universe.Types[d.Type.Name]
			_ = t
			if d.Init == nil {
				continue
			}
			for _, name := range d.Names {
				b, ok := f.Vars[name]
				if !ok || b.IsFB {
					continue
				}
				if v, ok := env.Prog.ConstVal[d.Init]; ok {
					env.storeInit(f, b.Base, b.Type, v)
				}
			}
		}
	}
}

func (env *ExecEnv) zeroFill(f *Frame, base int, t *Type) {
	switch t.Kind {
	case TStruct:
		off := base
		for i := range t.Fields {
			env.zeroFill(f, off, t.Fields[i].Type)
			off += t.Fields[i].Type.Size()
		}
	case TArray:
		total := t.Size()
		for i := 0; i < total; i++ {
			env.zeroFill(f, base+i, t.ElemType)
		}
	default:
		f.Slots[base] = ZeroValue(t)
	}
}

// storeInit 把常量初值写入（支持标量；聚合初值按首槽标量处理，数组全量初值在运行时赋值）。
func (env *ExecEnv) storeInit(f *Frame, base int, t *Type, v Value) {
	if t.Kind == TArray || t.Kind == TStruct {
		// 标量常量初始化聚合的第一叶子
		cv, err := Convert(v, env.leafAt(t))
		if err == nil {
			f.Slots[base] = cv
		}
		return
	}
	cv, err := Convert(v, t)
	if err == nil {
		f.Slots[base] = cv
	}
}

func (env *ExecEnv) leafAt(t *Type) *Type {
	for t.Kind == TArray {
		t = t.ElemType
	}
	if t.Kind == TStruct {
		return env.leafAt(t.Fields[0].Type)
	}
	return t
}

// InstantiateGlobal 按全局布局建帧、填初值、创建全局 FB 实例。
func (env *ExecEnv) InstantiateGlobal() *Frame {
	layout := env.Prog.GlobalLayout
	f := &Frame{
		Slots: make([]Value, layout.Size),
		Vars:  map[string]*VarBinding{},
	}
	for name, b := range layout.Vars {
		nb := &VarBinding{Type: b.Type, Base: b.Base, IsFB: b.IsFB}
		f.Vars[name] = nb
	}
	for name, b := range f.Vars {
		if b.IsFB {
			b.FB = env.instantiateFB(b.Type, f, name)
			continue
		}
		env.zeroFill(f, b.Base, b.Type)
	}
	// 全局变量声明初值
	for _, sec := range env.Prog.Universe.GlobalSec {
		for _, d := range sec.Variables {
			if d.Init == nil {
				continue
			}
			v, ok := env.Prog.ConstVal[d.Init]
			if !ok {
				continue
			}
			for _, name := range d.Names {
				b := f.Vars[name]
				if b == nil || b.IsFB {
					continue
				}
				env.storeInit(f, b.Base, b.Type, v)
			}
		}
	}
	return f
}

// ---------- 左值解析 ----------

// resolveRoot 找变量绑定（本帧 -> 父链 -> root 全局帧）。
func (env *ExecEnv) resolveRoot(f *Frame, spec *LValueSpec) (*VarBinding, *Frame) {
	search := f
	for search != nil {
		if b, ok := search.Vars[spec.Root]; ok {
			return b, search
		}
		search = search.Parent
	}
	if b, ok := env.Root.Vars[spec.Root]; ok {
		return b, env.Root
	}
	return nil, nil
}

// lvalueRef 把绑定规格 + 访问路径解析成当前拍的槽引用。
func (env *ExecEnv) lvalueRef(f *Frame, e Expr) (*SlotRef, error) {
	spec := env.Prog.LValue[e]
	if spec == nil {
		return nil, &RuntimeError{Line: e.Pos(), Msg: "内部错误: 左值未绑定"}
	}
	b, frame := env.resolveRoot(f, spec)
	if b == nil {
		return nil, &RuntimeError{Line: e.Pos(), Msg: fmt.Sprintf("变量 %s 不存在", spec.Root)}
	}
	if spec.IsFB {
		// FB 字段访问：进入实例帧；下标等表达式仍在调用帧 f 求值
		inst := b.FB
		t := spec.RootTy
		slot, _, err := env.walkFBPath(inst, f, t, spec.Path)
		if err != nil {
			return nil, &RuntimeError{Line: e.Pos(), Msg: err.Error()}
		}
		return slot, nil
	}
	t := spec.RootTy
	idx := b.Base
	if spec.Path == nil {
		if t.Kind == TArray || t.Kind == TStruct {
			return nil, &RuntimeError{Line: e.Pos(), Msg: "聚合变量不能作为标量"}
		}
		return &SlotRef{Frame: frame, Index: idx, T: t}, nil
	}
	// 数据所在帧（可能是全局帧）；下标/嵌套表达式仍在调用方局部帧 f 求值
	slot, _, err := env.walkPath(frame, f, idx, t, spec.Path)
	if err != nil {
		return nil, &RuntimeError{Line: e.Pos(), Msg: err.Error()}
	}
	return slot, nil
}

func (env *ExecEnv) walkFBPath(inst, local *Frame, t *Type, path *AccessPath) (*SlotRef, *Type, error) {
	if path == nil {
		return nil, nil, fmt.Errorf("功能块实例不能作为标量")
	}
	switch path.Kind {
	case PathField:
		if t.Kind != TFuncBlock || t.FB == nil {
			return nil, nil, fmt.Errorf("非用户功能块不支持字段访问")
		}
		off, ft, err := env.memberOffset(t.FB, path.Field)
		if err != nil {
			return nil, nil, err
		}
		if path.Next == nil {
			if ft.Kind == TArray || ft.Kind == TStruct {
				return nil, nil, fmt.Errorf("聚合成员不能作为标量")
			}
			return &SlotRef{Frame: inst, Index: off, T: ft}, ft, nil
		}
		return env.walkPath(inst, local, off, ft, path.Next)
	default:
		return nil, nil, fmt.Errorf("功能块字段路径中出现数组下标")
	}
}

// memberOffset 取用户 FB 成员在实例帧中的槽偏移。
func (env *ExecEnv) memberOffset(pou *POU, name string) (int, *Type, error) {
	layout := env.Prog.FrameLayout[pou]
	if b, ok := layout.Vars[name]; ok {
		return b.Base, b.Type, nil
	}
	return 0, nil, fmt.Errorf("功能块没有成员 %s", name)
}

// walkPath 在普通聚合变量上走访问链。frame=数据帧（槽位所在），local=表达式求值帧。
func (env *ExecEnv) walkPath(frame, local *Frame, base int, t *Type, path *AccessPath) (*SlotRef, *Type, error) {
	switch path.Kind {
	case PathField:
		if t.Kind != TStruct {
			return nil, nil, fmt.Errorf("类型 %s 没有字段", t.Name)
		}
		off, ft, err := t.FieldOffset(path.Field)
		if err != nil {
			return nil, nil, err
		}
		if path.Next == nil {
			if ft.Kind == TArray || ft.Kind == TStruct {
				return nil, nil, fmt.Errorf("聚合字段不能作为标量")
			}
			return &SlotRef{Frame: frame, Index: base + off, T: ft}, ft, nil
		}
		return env.walkPath(frame, local, base+off, ft, path.Next)
	case PathIndex:
		if t.Kind != TArray {
			return nil, nil, fmt.Errorf("类型 %s 不可下标访问", t.Name)
		}
		if len(path.Index) != len(t.Dims) {
			return nil, nil, fmt.Errorf("下标维数不匹配")
		}
		stride := t.ElemType.Size()
		// 行主序：offset = (((i1)*n2 + i2)*n3 + ...)
		linear := int64(0)
		for d, ie := range path.Index {
			v, err := env.eval(ie, local)
			if err != nil {
				return nil, nil, err
			}
			iv := v.AsInt()
			dim := t.Dims[d]
			if iv < dim.Low || iv > dim.High {
				return nil, nil, fmt.Errorf("数组下标 %d 越界 [%d..%d]", iv, dim.Low, dim.High)
			}
			if d == 0 {
				linear = iv - dim.Low
			} else {
				linear = linear*t.DimLength(d) + (iv - dim.Low)
			}
		}
		off := linear * int64(stride)
		if path.Next == nil {
			et := t.ElemType
			if et.Kind == TArray || et.Kind == TStruct {
				return nil, nil, fmt.Errorf("数组元素是聚合类型，需继续访问")
			}
			return &SlotRef{Frame: frame, Index: base + int(off), T: et}, et, nil
		}
		return env.walkPath(frame, local, base+int(off), t.ElemType, path.Next)
	}
	return nil, nil, fmt.Errorf("未知访问路径")
}

// ---------- 语句执行 ----------

// Run 执行整个 POU 体。
func (env *ExecEnv) Run(f *Frame) error {
	return env.execList(f, f.POU.Body)
}

func (env *ExecEnv) execList(f *Frame, list []Stmt) error {
	for _, s := range list {
		if err := env.cp(s.Pos()); err != nil {
			return err
		}
		if err := env.execStmt(f, s); err != nil {
			return err
		}
	}
	return nil
}

func (env *ExecEnv) cp(line int) error {
	if env.CP != nil {
		return env.CP.Checkpoint(line)
	}
	return nil
}

func (env *ExecEnv) execStmt(f *Frame, s Stmt) error {
	switch n := s.(type) {
	case *AssignStmt:
		return env.execAssign(f, n)
	case *CallStmt:
		_, err := env.execCall(f, n.Call)
		return err
	case *IfStmt:
		return env.execIf(f, n)
	case *CaseStmt:
		return env.execCase(f, n)
	case *ForStmt:
		return env.execFor(f, n)
	case *WhileStmt:
		return env.execWhile(f, n)
	case *RepeatStmt:
		return env.execRepeat(f, n)
	case *ExitStmt:
		return errExit
	case *ReturnStmt:
		return errReturn
	}
	return &RuntimeError{Line: s.Pos(), Msg: "未知语句"}
}

var (
	errExit   = &RuntimeError{Line: 0, Msg: "__EXIT__"}
	errReturn = &RuntimeError{Line: 0, Msg: "__RETURN__"}
)

func (env *ExecEnv) execAssign(f *Frame, n *AssignStmt) error {
	ref, err := env.lvalueRef(f, n.Target)
	if err != nil {
		return err
	}
	v, err := env.eval(n.Value, f)
	if err != nil {
		return err
	}
	cv, err := Convert(v, ref.T)
	if err != nil {
		return &RuntimeError{Line: n.Line, Msg: err.Error()}
	}
	ref.Set(cv)
	return nil
}

func (env *ExecEnv) execIf(f *Frame, n *IfStmt) error {
	v, err := env.eval(n.Cond, f)
	if err != nil {
		return err
	}
	if v.AsBool() {
		return env.execList(f, n.Then)
	}
	for _, e := range n.Elsifs {
		v, err := env.eval(e.Cond, f)
		if err != nil {
			return err
		}
		if v.AsBool() {
			return env.execList(f, e.Body)
		}
	}
	return env.execList(f, n.Else)
}

func (env *ExecEnv) execCase(f *Frame, n *CaseStmt) error {
	sel, err := env.eval(n.Selector, f)
	if err != nil {
		return err
	}
	for _, br := range n.Cases {
		for _, lab := range br.Labels {
			if b, ok := lab.(*BinaryExpr); ok && b.Op == ".." {
				lo, err := env.eval(b.Left, f)
				if err != nil {
					return err
				}
				hi, err := env.eval(b.Right, f)
				if err != nil {
					return err
				}
				iv := sel.AsInt()
				if iv >= lo.AsInt() && iv <= hi.AsInt() {
					return env.execList(f, br.Body)
				}
				continue
			}
			lv, err := env.eval(lab, f)
			if err != nil {
				return err
			}
			if sel.Type.Kind == TEnum {
				if sel.AsInt() == lv.AsInt() {
					return env.execList(f, br.Body)
				}
			} else if valuesEqual(sel, lv) {
				return env.execList(f, br.Body)
			}
		}
	}
	return env.execList(f, n.Else)
}

func valuesEqual(a, b Value) bool {
	if a.Type.Kind == TString {
		return a.V.(string) == b.V.(string)
	}
	if a.Type.Kind == TTime {
		return a.V.(int64) == b.V.(int64)
	}
	if a.Type.IsNumeric() || b.Type.IsNumeric() {
		return a.AsReal() == b.AsReal()
	}
	return a.AsInt() == b.AsInt()
}

func (env *ExecEnv) execFor(f *Frame, n *ForStmt) error {
	from, err := env.eval(n.From, f)
	if err != nil {
		return err
	}
	to, err := env.eval(n.To, f)
	if err != nil {
		return err
	}
	step := int64(1)
	if n.Step != nil {
		sv, err := env.eval(n.Step, f)
		if err != nil {
			return err
		}
		step = sv.AsInt()
		if step == 0 {
			return &RuntimeError{Line: n.Line, Msg: "FOR 步长不能为 0"}
		}
	}
	ref, err := f.Ref(n.Counter)
	if err != nil {
		return &RuntimeError{Line: n.Line, Msg: err.Error()}
	}
	i := from.AsInt()
	end := to.AsInt()
	const maxIter = 1_000_000
	iters := 0
	for {
		if step > 0 && i > end || step < 0 && i < end {
			break
		}
		ref.Set(IntVal(ref.T, i))
		err := env.execList(f, n.Body)
		if err != nil {
			if err == errExit {
				break
			}
			if err == errReturn {
				return err
			}
			return err
		}
		i += step
		iters++
		if iters > maxIter {
			return &RuntimeError{Line: n.Line, Msg: "FOR 循环迭代次数超过上限 1000000"}
		}
		if err := env.cp(n.Line); err != nil {
			return err
		}
	}
	return nil
}

func (env *ExecEnv) execWhile(f *Frame, n *WhileStmt) error {
	iters := 0
	for {
		v, err := env.eval(n.Cond, f)
		if err != nil {
			return err
		}
		if !v.AsBool() {
			return nil
		}
		err = env.execList(f, n.Body)
		if err != nil {
			if err == errExit {
				return nil
			}
			if err == errReturn {
				return err
			}
			return err
		}
		iters++
		if iters > 1_000_000 {
			return &RuntimeError{Line: n.Line, Msg: "WHILE 循环迭代次数超过上限 1000000"}
		}
		if err := env.cp(n.Line); err != nil {
			return err
		}
	}
}

func (env *ExecEnv) execRepeat(f *Frame, n *RepeatStmt) error {
	iters := 0
	for {
		err := env.execList(f, n.Body)
		if err != nil {
			if err == errExit {
				return nil
			}
			if err == errReturn {
				return err
			}
			return err
		}
		v, err := env.eval(n.Until, f)
		if err != nil {
			return err
		}
		if v.AsBool() {
			return nil
		}
		iters++
		if iters > 1_000_000 {
			return &RuntimeError{Line: n.Line, Msg: "REPEAT 循环迭代次数超过上限 1000000"}
		}
		if err := env.cp(n.Line); err != nil {
			return err
		}
	}
}

// ---------- 表达式求值 ----------

func (env *ExecEnv) eval(e Expr, f *Frame) (Value, error) {
	// 编译期常量直接返回
	if v, ok := env.Prog.ConstVal[e]; ok {
		return v, nil
	}
	switch n := e.(type) {
	case *LiteralExpr:
		return evalLiteral(n)
	case *EnumExpr:
		return env.evalEnum(n)
	case *IdentExpr:
		// 枚举常量优先：它没有左值绑定
		if iv, ty, ok := env.findEnumConst(n.Name); ok {
			return Value{Type: ty, V: iv}, nil
		}
		ref, err := env.lvalueRef(f, e)
		if err != nil {
			return Value{}, err
		}
		return ref.Get(), nil
	case *IndexExpr, *FieldExpr:
		ref, err := env.lvalueRef(f, e)
		if err != nil {
			return Value{}, err
		}
		return ref.Get(), nil
	case *UnaryExpr:
		return env.evalUnary(n, f)
	case *BinaryExpr:
		return env.evalBinary(n, f)
	case *CallExpr:
		return env.execCall(f, n)
	}
	return Value{}, &RuntimeError{Line: e.Pos(), Msg: "未知表达式"}
}

func evalLiteral(n *LiteralExpr) (Value, error) {
	if v, ok := evalConstLiteral(n); ok {
		return v, nil
	}
	return Value{}, &RuntimeError{Line: n.Line, Msg: "字面量解析失败: " + n.Raw}
}

// findEnumConst 查裸枚举常量。
func (env *ExecEnv) findEnumConst(name string) (int64, *Type, bool) {
	// 变量表里有的名字不是枚举常量
	if _, ok := env.Root.Vars[name]; ok {
		return 0, nil, false
	}
	for _, ty := range env.Prog.Universe.Types {
		if ty.Kind == TEnum {
			if iv, ok := ty.EnumValues[name]; ok {
				return iv, ty, true
			}
		}
	}
	return 0, nil, false
}

// evalEnum 处理带类型限定的枚举表达式。
func (env *ExecEnv) evalEnum(n *EnumExpr) (Value, error) {
	for _, t := range env.Prog.Universe.Types {
		if t.Kind == TEnum {
			if iv, ok := t.EnumValues[n.Value]; ok {
				if n.EnumType != "" && t.Name != n.EnumType {
					continue
				}
				return Value{Type: t, V: iv}, nil
			}
		}
	}
	return Value{}, &RuntimeError{Line: n.Line, Msg: "未定义枚举值 " + n.Value}
}

func (env *ExecEnv) evalUnary(n *UnaryExpr, f *Frame) (Value, error) {
	v, err := env.eval(n.Expr, f)
	if err != nil {
		return Value{}, err
	}
	switch n.Op {
	case "NOT":
		if v.Type.Kind == TBool {
			return BoolVal(!v.AsBool()), nil
		}
		return Value{Type: v.Type, V: ^v.AsUint()}, nil
	case "-":
		if v.Type.Kind == TTime {
			return Value{Type: v.Type, V: -v.V.(int64)}, nil
		}
		if v.Type.Kind == TReal {
			return RealVal(v.Type, -v.AsReal()), nil
		}
		return IntVal(v.Type, -v.AsInt()), nil
	}
	return Value{}, &RuntimeError{Line: n.Line, Msg: "未知一元运算 " + n.Op}
}

func (env *ExecEnv) evalBinary(n *BinaryExpr, f *Frame) (Value, error) {
	a, err := env.eval(n.Left, f)
	if err != nil {
		return Value{}, err
	}
	b, err := env.eval(n.Right, f)
	if err != nil {
		return Value{}, err
	}
	return applyBinary(n.Op, a, b, n.Line)
}

// applyBinary 完整运行期运算语义。
func applyBinary(op string, a, b Value, line int) (Value, error) {
	boolT := primitiveTypes["BOOL"]
	switch op {
	case "AND", "&":
		if a.Type.Kind == TBool {
			return Value{Type: boolT, V: a.AsBool() && b.AsBool()}, nil
		}
		return Value{Type: a.Type, V: a.AsUint() & b.AsUint()}, nil
	case "OR":
		if a.Type.Kind == TBool {
			return Value{Type: boolT, V: a.AsBool() || b.AsBool()}, nil
		}
		return Value{Type: a.Type, V: a.AsUint() | b.AsUint()}, nil
	case "XOR":
		if a.Type.Kind == TBool {
			return Value{Type: boolT, V: a.AsBool() != b.AsBool()}, nil
		}
		return Value{Type: a.Type, V: a.AsUint() ^ b.AsUint()}, nil
	case "=":
		return BoolVal(valuesEqual(a, b)), nil
	case "<>":
		return BoolVal(!valuesEqual(a, b)), nil
	case "<", ">", "<=", ">=":
		c := compareOrdered(a, b)
		var r bool
		switch op {
		case "<":
			r = c < 0
		case ">":
			r = c > 0
		case "<=":
			r = c <= 0
		case ">=":
			r = c >= 0
		}
		return BoolVal(r), nil
	case "+":
		if a.Type.Kind == TTime {
			return Value{Type: a.Type, V: a.V.(int64) + b.AsInt()}, nil
		}
		if a.Type.Kind == TReal || b.Type.Kind == TReal {
			return RealVal(widenedReal(a, b), a.AsReal()+b.AsReal()), nil
		}
		return intResult(a, b, a.AsInt()+b.AsInt()), nil
	case "-":
		if a.Type.Kind == TTime {
			return Value{Type: a.Type, V: a.V.(int64) - b.AsInt()}, nil
		}
		if a.Type.Kind == TReal || b.Type.Kind == TReal {
			return RealVal(widenedReal(a, b), a.AsReal()-b.AsReal()), nil
		}
		return intResult(a, b, a.AsInt()-b.AsInt()), nil
	case "*":
		if a.Type.Kind == TReal || b.Type.Kind == TReal {
			return RealVal(widenedReal(a, b), a.AsReal()*b.AsReal()), nil
		}
		return intResult(a, b, a.AsInt()*b.AsInt()), nil
	case "/":
		if a.Type.Kind == TReal || b.Type.Kind == TReal {
			if b.AsReal() == 0 {
				return Value{}, &RuntimeError{Line: line, Msg: "实数除零"}
			}
			return RealVal(widenedReal(a, b), a.AsReal()/b.AsReal()), nil
		}
		if b.AsInt() == 0 {
			return Value{}, &RuntimeError{Line: line, Msg: "整数除零"}
		}
		return intResult(a, b, a.AsInt()/b.AsInt()), nil
	case "MOD":
		if b.AsInt() == 0 {
			return Value{}, &RuntimeError{Line: line, Msg: "MOD 除零"}
		}
		return intResult(a, b, a.AsInt()%b.AsInt()), nil
	case "**":
		return RealVal(primitiveTypes["LREAL"], math.Pow(a.AsReal(), b.AsReal())), nil
	}
	return Value{}, &RuntimeError{Line: line, Msg: "未知运算符 " + op}
}

func widenedReal(a, b Value) *Type {
	if a.Type.Kind == TReal {
		return a.Type
	}
	return b.Type
}

func intResult(a, b Value, v int64) Value {
	t := a.Type
	if b.Type.Bits > t.Bits {
		t = b.Type
	}
	if t.Unsigned {
		return Value{Type: t, V: uint64(v)}
	}
	return Value{Type: t, V: v}
}

// ---------- 调用 ----------

func (env *ExecEnv) execCall(f *Frame, n *CallExpr) (Value, error) {
	bind := env.Prog.CallInfo[n]
	if bind == nil {
		return Value{}, &RuntimeError{Line: n.Line, Msg: "调用未绑定: " + n.Callee}
	}
	switch {
	case bind.NativeFB:
		return Value{}, env.execNativeFB(f, n, bind)
	case bind.UserFB:
		return Value{}, env.execUserFB(f, n, bind)
	case bind.Func:
		return env.execUserFunc(f, n, bind)
	case bind.Native:
		return env.execNativeFunc(f, n, bind)
	}
	return Value{}, &RuntimeError{Line: n.Line, Msg: "未知调用类型"}
}

func (env *ExecEnv) collectArgs(f *Frame, n *CallExpr) (map[string]Value, map[string]*SlotRef, error) {
	vals := map[string]Value{}
	outs := map[string]*SlotRef{}
	for _, arg := range n.Args {
		name := arg.Name
		isOut := false
		if strings.HasPrefix(name, "OUT:") {
			name = strings.TrimPrefix(name, "OUT:")
			isOut = true
		}
		if isOut {
			ref, err := env.lvalueRef(f, arg.Expr)
			if err != nil {
				return nil, nil, err
			}
			outs[name] = ref
			vals[name] = ref.Get()
			continue
		}
		v, err := env.eval(arg.Expr, f)
		if err != nil {
			return nil, nil, err
		}
		vals[name] = v
	}
	return vals, outs, nil
}

func (env *ExecEnv) execNativeFB(f *Frame, n *CallExpr, bind *CallBinding) error {
	inst, err := f.FBInstance(n.Callee)
	if err != nil {
		// 可能在 root 全局帧
		b, ok := env.Root.Vars[n.Callee]
		if !ok || !b.IsFB {
			return &RuntimeError{Line: n.Line, Msg: err.Error()}
		}
		inst = b.FB
	}
	if inst.Native == nil {
		return &RuntimeError{Line: n.Line, Msg: n.Callee + " 不是原生功能块实例"}
	}
	in, outs, err := env.collectArgs(f, n)
	if err != nil {
		return err
	}
	// 缺省参数补零值
	for _, p := range bind.ParamOrder {
		if _, ok := in[p]; !ok {
			in[p] = ZeroValue(nativeFBParamType(inst.Native.Kind(), p))
		}
	}
	out := inst.Native.Execute(in, env.Now, env.DT)
	// 输出回写：显式 => 参数优先，否则写入实例的标准输出成员（原生实例无槽，
	// 通过输出参数回传给调用者；无输出参数时结果保留在原生对象内部供下拍使用）
	for name, ref := range outs {
		if v, ok := out[name]; ok {
			cv, err := Convert(v, ref.T)
			if err != nil {
				return &RuntimeError{Line: n.Line, Msg: err.Error()}
			}
			ref.Set(cv)
		}
	}
	// 无输出参数的裸调用（如 timer(IN:=...,PT:=...)）：结果存到调用帧同名约定变量
	// 约定：调用者声明 timerQ/timerET 不做强制；这里把结果挂在实例上供下一拍，
	// 用户需要输出时按 IEC 写法 timer(IN:=x, PT:=t, Q=>myQ, ET=>myET)。
	return nil
}

func nativeFBParamType(kind, param string) *Type {
	return nativeFBParams(kind)[param]
}

func (env *ExecEnv) execUserFB(f *Frame, n *CallExpr, bind *CallBinding) error {
	inst, err := f.FBInstance(n.Callee)
	if err != nil {
		b, ok := env.Root.Vars[n.Callee]
		if !ok || !b.IsFB {
			return &RuntimeError{Line: n.Line, Msg: err.Error()}
		}
		inst = b.FB
	}
	in, outs, err := env.collectArgs(f, n)
	if err != nil {
		return err
	}
	// 输入参数拷贝到实例帧
	pou := inst.POU
	for _, sec := range pou.Sections {
		if sec.Kind != VarInput && sec.Kind != VarInOut {
			continue
		}
		for _, d := range sec.Variables {
			for _, name := range d.Names {
				if v, ok := in[name]; ok {
					b := inst.Vars[name]
					cv, err := Convert(v, b.Type)
					if err != nil {
						return &RuntimeError{Line: n.Line, Msg: err.Error()}
					}
					if b.Type.Kind == TArray || b.Type.Kind == TStruct {
						copyAggregate(inst.Slots, b.Base, b.Type, cv)
					} else {
						inst.Slots[b.Base] = cv
					}
				}
			}
		}
	}
	// 执行
	if err := env.Run(inst); err != nil {
		if err == errReturn {
			err = nil
		}
		if err != nil {
			return err
		}
	}
	// 输出参数回写
	for name, ref := range outs {
		b := inst.Vars[name]
		if b == nil {
			continue
		}
		if b.Type.Kind == TArray || b.Type.Kind == TStruct {
			continue
		}
		cv, err := Convert(inst.Slots[b.Base], ref.T)
		if err != nil {
			return &RuntimeError{Line: n.Line, Msg: err.Error()}
		}
		ref.Set(cv)
	}
	return nil
}

func copyAggregate(slots []Value, base int, t *Type, v Value) {
	// 聚合 IN/OUT 目前只支持同类型整体拷贝；类型检查已保证同名同型。
	slots[base] = v
}

func (env *ExecEnv) execUserFunc(f *Frame, n *CallExpr, bind *CallBinding) (Value, error) {
	pou := env.Prog.Universe.POUs[n.Callee]
	cf := env.Instantiate(pou, f)
	in, outs, err := env.collectArgs(f, n)
	if err != nil {
		return Value{}, err
	}
	for _, sec := range pou.Sections {
		if sec.Kind != VarInput && sec.Kind != VarInOut {
			continue
		}
		for _, d := range sec.Variables {
			for _, name := range d.Names {
				if v, ok := in[name]; ok {
					b := cf.Vars[name]
					cv, err := Convert(v, b.Type)
					if err != nil {
						return Value{}, &RuntimeError{Line: n.Line, Msg: err.Error()}
					}
					cf.Slots[b.Base] = cv
				}
			}
		}
	}
	err = env.Run(cf)
	if err != nil && err != errReturn {
		return Value{}, err
	}
	for name, ref := range outs {
		b := cf.Vars[name]
		if b != nil {
			ref.Set(cf.Slots[b.Base])
		}
	}
	ret := cf.Vars[pou.Name]
	if ret == nil {
		return Value{}, &RuntimeError{Line: n.Line, Msg: "函数缺少返回值"}
	}
	return cf.Slots[ret.Base], nil
}

func (env *ExecEnv) execNativeFunc(f *Frame, n *CallExpr, bind *CallBinding) (Value, error) {
	impl := env.Prog.Universe.NativeImpl[n.Callee]
	args := make([]Value, len(n.Args))
	for i, a := range n.Args {
		v, err := env.eval(a.Expr, f)
		if err != nil {
			return Value{}, err
		}
		args[i] = v
	}
	v, err := impl(args)
	if err != nil {
		return Value{}, &RuntimeError{Line: n.Line, Msg: err.Error()}
	}
	return v, nil
}

// SnapshotSlots 导出帧全部槽位（迁移/页面用）。
func (env *ExecEnv) SnapshotSlots(f *Frame) []Value {
	out := make([]Value, len(f.Slots))
	copy(out, f.Slots)
	return out
}

// FormatValue 页面/日志用的值格式化。
func FormatValue(v Value) string {
	switch v.Type.Kind {
	case TBool:
		if v.AsBool() {
			return "TRUE"
		}
		return "FALSE"
	case TInt, TEnum:
		return fmt.Sprintf("%d", v.AsInt())
	case TUint:
		return fmt.Sprintf("%d", v.AsUint())
	case TReal:
		return fmt.Sprintf("%g", v.AsReal())
	case TString:
		return "'" + v.V.(string) + "'"
	case TTime:
		return formatDuration(time.Duration(v.V.(int64)))
	}
	return "?"
}

func formatDuration(d time.Duration) string {
	return "T#" + d.String()
}
