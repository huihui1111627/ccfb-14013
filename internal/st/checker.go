package st

import "fmt"

// scopeEntry 作用域中的一个符号。
type scopeEntry struct {
	Type *Type
	Decl *VarDecl
	FB   bool
}

type checker struct {
	l       *Loader
	scopes  []map[string]*scopeEntry
	curPOU  *POU
	curFB   bool // 当前 POU 是否 FUNCTION_BLOCK
	curFunc bool // 当前 POU 是否 FUNCTION
}

func newChecker(l *Loader) *checker {
	c := &checker{l: l}
	c.push() // 常驻全局作用域
	return c
}

func (c *checker) push() { c.scopes = append(c.scopes, map[string]*scopeEntry{}) }
func (c *checker) pop()  { c.scopes = c.scopes[:len(c.scopes)-1] }

func (c *checker) declare(name string, e *scopeEntry) bool {
	top := c.scopes[len(c.scopes)-1]
	if _, dup := top[name]; dup {
		return false
	}
	top[name] = e
	return true
}

// lookup 沿词法作用域查找，找不到再找全局。
func (c *checker) lookup(name string) (*scopeEntry, bool) {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if e, ok := c.scopes[i][name]; ok {
			return e, true
		}
	}
	return c.lookupGlobal(name)
}

func (c *checker) lookupGlobal(name string) (*scopeEntry, bool) {
	for _, cu := range c.l.units {
		for _, sec := range cu.GlobalVars {
			for _, d := range sec.Variables {
				for _, n := range d.Names {
					if n == name {
						t := c.l.resolveVarType(&d.Type)
						return &scopeEntry{Type: t, Decl: d, FB: t != nil && t.Kind == TFuncBlock}, true
					}
				}
			}
		}
	}
	return nil, false
}

func (l *Loader) resolveVarType(ref *TypeRef) *Type {
	if ref.ArrayOf != nil {
		elem := l.resolveVarType(ref.ArrayOf)
		if elem == nil {
			return nil
		}
		dims := make([]Dim, len(ref.Dims))
		for i, r := range ref.Dims {
			lo, _ := l.constInt(r.Low)
			hi, _ := l.constInt(r.High)
			dims[i] = Dim{Low: lo, High: hi}
		}
		return &Type{Kind: TArray, Name: arrayTypeName(elem, dims), ElemType: elem, Dims: dims}
	}
	// 内联结构体
	if ref.Struct != nil {
		return l.resolveStructRef(ref, l.resolveVarType)
	}
	if t, ok := l.types[ref.Name]; ok {
		return t
	}
	if t, ok := l.nativeFB[ref.Name]; ok {
		return t
	}
	if pou, ok := l.pous[ref.Name]; ok {
		return &Type{Kind: TFuncBlock, Name: ref.Name, FB: pou}
	}
	if pt, ok := primitiveTypes[ref.Name]; ok {
		return pt
	}
	return nil
}

// checkDecl 检查一条变量声明（global 表示全局区），并在全局布局中占位。
func (c *checker) checkDecl(d *VarDecl, global bool, layout *FrameLayout) {
	t := c.l.resolveVarType(&d.Type)
	if t == nil {
		c.l.nodeErr(d, "变量 %s 使用了未知类型", joinNames(d.Names))
		return
	}
	for _, n := range d.Names {
		if !c.declare(n, &scopeEntry{Type: t, Decl: d, FB: t.Kind == TFuncBlock}) {
			c.l.nodeErr(d, "变量 %s 重复声明", n)
			continue
		}
		if layout != nil {
			c.alloc(layout, n, t)
		}
	}
	if d.Init != nil {
		vt := c.exprTypeOf(d.Init)
		if vt != nil && !AssignableTo(vt, t) {
			c.l.nodeErr(d, "变量 %s 初值类型 %s 与声明类型 %s 不兼容",
				joinNames(d.Names), vt.Name, t.Name)
		}
		if global || c.curPOU != nil {
			// 初值若能常量折叠则记录，解释器建帧时使用；非常量初值（含 FB 输出参数）按零值处理并在运行时每拍执行
			if v, ok := c.l.foldConst(d.Init); ok {
				c.l.constVal[d.Init] = v
			}
		}
	}
}

func (c *checker) alloc(layout *FrameLayout, name string, t *Type) {
	b := &VarBinding{Type: t, Base: layout.Size}
	layout.Size += t.Size()
	if t.Kind == TFuncBlock {
		b.IsFB = true
	}
	layout.Vars[name] = b
}

func joinNames(ns []string) string {
	out := ""
	for i, n := range ns {
		if i > 0 {
			out += ","
		}
		out += n
	}
	return out
}

// checkPOU 检查整个 POU：声明、布局、函数体。
func (c *checker) checkPOU(pou *POU) {
	c.curPOU = pou
	c.curFB = pou.Kind == KFunctionBlock
	c.curFunc = pou.Kind == KFunction
	c.push()
	layout := &FrameLayout{Vars: map[string]*VarBinding{}}
	// 形参与局部变量
	for _, sec := range pou.Sections {
		for _, d := range sec.Variables {
			c.checkDecl(d, false, layout)
		}
	}
	if c.curFunc {
		// 隐式返回变量，与 POU 同名，既进帧布局也进作用域
		rt := c.l.resolveVarType(pou.ReturnType)
		if rt == nil {
			c.l.nodeErr(pou, "FUNCTION %s 返回类型未知", pou.Name)
		} else {
			c.alloc(layout, pou.Name, rt)
			c.declare(pou.Name, &scopeEntry{Type: rt})
		}
	}
	c.l.layout[pou] = layout
	for _, stm := range pou.Body {
		c.stmt(stm)
	}
	c.pop()
	c.curPOU = nil
}

// BuildGlobalLayout 构建独立的全局帧布局（全局变量不并入任何 POU 帧）。
func (l *Loader) BuildGlobalLayout() *FrameLayout {
	layout := &FrameLayout{Vars: map[string]*VarBinding{}}
	for _, cu := range l.units {
		for _, sec := range cu.GlobalVars {
			for _, d := range sec.Variables {
				t := l.resolveVarType(&d.Type)
				if t == nil {
					continue
				}
				for _, n := range d.Names {
					if _, exists := layout.Vars[n]; exists {
						continue
					}
					b := &VarBinding{Type: t, Base: layout.Size}
					layout.Size += t.Size()
					if t.Kind == TFuncBlock {
						b.IsFB = true
					}
					layout.Vars[n] = b
				}
			}
		}
	}
	return layout
}

// ---------- 语句检查 ----------

func (c *checker) stmt(s Stmt) {
	switch n := s.(type) {
	case *AssignStmt:
		lt := c.lvalueType(n.Target)
		rt := c.exprTypeOf(n.Value)
		if lt == nil || rt == nil {
			return
		}
		if !AssignableTo(rt, lt) {
			c.l.nodeErr(n, "无法把 %s 赋值给 %s（类型 %s -> %s）",
				textOf(n.Value), textOf(n.Target), rt.Name, lt.Name)
		}
	case *CallStmt:
		c.checkCall(n.Call)
	case *IfStmt:
		c.condType(n.Cond, n)
		for _, st := range n.Then {
			c.stmt(st)
		}
		for _, e := range n.Elsifs {
			c.condType(e.Cond, n)
			for _, st := range e.Body {
				c.stmt(st)
			}
		}
		for _, st := range n.Else {
			c.stmt(st)
		}
	case *CaseStmt:
		st := c.exprTypeOf(n.Selector)
		if st != nil && !(st.IsIntegral() || st.Kind == TEnum) {
			c.l.nodeErr(n, "CASE 选择表达式必须是整数或枚举类型，实际为 %s", st.Name)
		}
		for _, br := range n.Cases {
			for _, lab := range br.Labels {
				lt := c.exprTypeOf(lab)
				if lt != nil && st != nil && !AssignableTo(lt, st) && !AssignableTo(st, lt) {
					c.l.nodeErr(n, "CASE 标签类型 %s 与选择类型 %s 不匹配", lt.Name, st.Name)
				}
			}
			for _, st2 := range br.Body {
				c.stmt(st2)
			}
		}
		for _, st2 := range n.Else {
			c.stmt(st2)
		}
	case *ForStmt:
		entry, ok := c.lookup(n.Counter)
		if !ok {
			c.l.nodeErr(n, "FOR 计数器 %s 未声明", n.Counter)
		} else if !entry.Type.IsIntegral() {
			c.l.nodeErr(n, "FOR 计数器必须是整数类型")
		}
		for _, e := range []Expr{n.From, n.To} {
			t := c.exprTypeOf(e)
			if t != nil && !t.IsIntegral() {
				c.l.nodeErr(n, "FOR 上下界必须是整数")
			}
		}
		if n.Step != nil {
			t := c.exprTypeOf(n.Step)
			if t != nil && !t.IsIntegral() {
				c.l.nodeErr(n, "FOR 步长必须是整数")
			}
		}
		for _, st := range n.Body {
			c.stmt(st)
		}
	case *WhileStmt:
		c.condType(n.Cond, n)
		for _, st := range n.Body {
			c.stmt(st)
		}
	case *RepeatStmt:
		c.condType(n.Until, n)
		for _, st := range n.Body {
			c.stmt(st)
		}
	case *ExitStmt, *ReturnStmt:
		// 语义合法性（循环内 EXIT）在解释器层也有保护，这里不额外限制
	}
}

func (c *checker) condType(e Expr, n Node) {
	t := c.exprTypeOf(e)
	if t != nil && t.Kind != TBool {
		c.l.nodeErr(n, "条件表达式必须是 BOOL，实际为 %s", t.Name)
	}
}

// lvalueType 检查左值表达式并记录 LValue 绑定。
func (c *checker) lvalueType(e Expr) *Type {
	t := c.exprTypeOf(e)
	if t != nil && !isLValue(e) {
		c.l.nodeErr(e, "赋值目标必须是可写左值")
		return nil
	}
	return t
}

func textOf(e Expr) string {
	switch n := e.(type) {
	case *IdentExpr:
		return n.Name
	case *FieldExpr:
		return textOf(n.Object) + "." + n.Field
	case *IndexExpr:
		return textOf(n.Array) + "[...]"
	}
	return "?"
}

var _ = fmt.Sprintf
