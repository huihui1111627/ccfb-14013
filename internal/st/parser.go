package st

import "fmt"

// ParseError 描述语法错误。
type ParseError struct {
	File string
	Line int
	Col  int
	Msg  string
}

func (e *ParseError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("语法错误 %s 第%d行: %s", e.File, e.Line, e.Msg)
	}
	return fmt.Sprintf("语法错误 第%d行: %s", e.Line, e.Msg)
}

// Parse 解析一个 ST 源文件。
func Parse(file, src string) (*CompilationUnit, error) {
	toks, err := Lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{file: file, toks: toks}
	cu := &CompilationUnit{File: file, Line: 1}
	for !p.is(TEOF) {
		switch p.peek().Text {
		case "TYPE":
			tds, err := p.parseTypeBlock()
			if err != nil {
				return nil, err
			}
			cu.Types = append(cu.Types, tds...)
		case "VAR_GLOBAL":
			sec, err := p.parseVarSection(VarGlobal)
			if err != nil {
				return nil, err
			}
			cu.GlobalVars = append(cu.GlobalVars, sec)
		case "PROGRAM", "FUNCTION_BLOCK", "FUNCTION":
			pou, err := p.parsePOU()
			if err != nil {
				return nil, err
			}
			cu.POUs = append(cu.POUs, pou)
		default:
			return nil, p.errf("顶层应为 TYPE / VAR_GLOBAL / PROGRAM / FUNCTION_BLOCK / FUNCTION，遇到 %s", p.peek().Text)
		}
	}
	return cu, nil
}

type parser struct {
	file string
	toks []Token
	pos  int
}

func (p *parser) peek() Token        { return p.toks[p.pos] }
func (p *parser) next() Token        { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser) is(k TokenKind) bool { return p.peek().Kind == k }

func (p *parser) isWord(w string) bool {
	t := p.peek()
	return t.Kind == TIDENT && t.Text == w
}

func (p *parser) acceptWord(w string) bool {
	if p.isWord(w) {
		p.next()
		return true
	}
	return false
}

func (p *parser) expectWord(w string) error {
	if !p.isWord(w) {
		return p.errf("期望关键字 %s，遇到 %s", w, p.peek().Text)
	}
	p.next()
	return nil
}

func (p *parser) accept(k TokenKind) (Token, bool) {
	if p.is(k) {
		return p.next(), true
	}
	return Token{}, false
}

func (p *parser) expect(k TokenKind, what string) (Token, error) {
	if !p.is(k) {
		return Token{}, p.errf("期望 %s，遇到 %s", what, p.peek().Text)
	}
	return p.next(), nil
}

func (p *parser) errf(format string, args ...any) error {
	t := p.peek()
	return &ParseError{File: p.file, Line: t.Line, Col: t.Col, Msg: fmt.Sprintf(format, args...)}
}

// ---------- 类型声明 ----------

func (p *parser) parseTypeBlock() ([]*TypeDecl, error) {
	if err := p.expectWord("TYPE"); err != nil {
		return nil, err
	}
	var decls []*TypeDecl
	for !p.isWord("END_TYPE") {
		if p.is(TEOF) {
			return nil, p.errf("TYPE 块缺少 END_TYPE")
		}
		nameTok, err := p.expect(TIDENT, "类型名")
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TCOLON, "':'"); err != nil {
			return nil, err
		}
		ref, err := p.parseTypeRef()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TSEMI, "';'"); err != nil {
			return nil, err
		}
		decls = append(decls, &TypeDecl{Name: nameTok.Text, Type: *ref, Line: nameTok.Line})
	}
	p.next() // END_TYPE
	return decls, nil
}

// parseStructFieldDecl 解析结构体字段：a, b : SomeType;
// 直接产出 VarDecl，完整保留数组/嵌套结构体的 TypeRef。
func (p *parser) parseStructFieldDecl() (*VarDecl, error) {
	decl := &VarDecl{Line: p.peek().Line}
	for {
		name, err := p.expect(TIDENT, "字段名")
		if err != nil {
			return nil, err
		}
		decl.Names = append(decl.Names, name.Text)
		if _, ok := p.accept(TCOMMA); !ok {
			break
		}
	}
	if _, err := p.expect(TCOLON, "':'"); err != nil {
		return nil, err
	}
	ref, err := p.parseTypeRef()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(TSEMI, "';'"); err != nil {
		return nil, err
	}
	decl.Type = *ref
	return decl, nil
}

// parseTypeRef 解析：
//
//	INT / MyStruct / (RED,GREEN) 枚举 / ARRAY[1..3,0..2] OF REAL
func (p *parser) parseTypeRef() (*TypeRef, error) {
	t := p.peek()
	if p.isWord("ARRAY") {
		p.next()
		if _, err := p.expect(TLBRACK, "'['"); err != nil {
			return nil, err
		}
		var dims []*Range
		for {
			low, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(TDOTDOT, "'..'"); err != nil {
				return nil, err
			}
			high, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			dims = append(dims, &Range{Low: low, High: high})
			if _, ok := p.accept(TCOMMA); !ok {
				break
			}
		}
		if _, err := p.expect(TRBRACK, "']'"); err != nil {
			return nil, err
		}
		if err := p.expectWord("OF"); err != nil {
			return nil, err
		}
		elem, err := p.parseTypeRef()
		if err != nil {
			return nil, err
		}
		return &TypeRef{ArrayOf: elem, Dims: dims, Line: t.Line}, nil
	}
	// 结构体类型：STRUCT 字段... END_STRUCT
	if p.isWord("STRUCT") {
		p.next()
		ref := &TypeRef{Name: "__ANON_STRUCT__", Line: t.Line}
		for !p.isWord("END_STRUCT") {
			if p.is(TEOF) {
				return nil, p.errf("结构体缺少 END_STRUCT")
			}
			// 字段复用变量声明语法（名字 + 类型引用，含数组/嵌套结构体）
			fd, err := p.parseStructFieldDecl()
			if err != nil {
				return nil, err
			}
			ref.Struct = append(ref.Struct, fd)
		}
		p.next() // END_STRUCT
		return ref, nil
	}
	// 内联枚举类型：(RED, GREEN, BLUE)
	if p.is(TLPAREN) {
		p.next()
		ref := &TypeRef{Name: "__ANON_ENUM__", Line: t.Line}
		for {
			v, err := p.expect(TIDENT, "枚举值")
			if err != nil {
				return nil, err
			}
			// 用 ArrayOf 不可能；复用 Dims 不合适。枚举值编码进名字：__ANON_ENUM__:RED,GREEN
			ref.Name += ":" + v.Text
			if _, ok := p.accept(TCOMMA); !ok {
				break
			}
		}
		if _, err := p.expect(TRPAREN, "')'"); err != nil {
			return nil, err
		}
		return ref, nil
	}
	name, err := p.expect(TIDENT, "类型名")
	if err != nil {
		return nil, err
	}
	return &TypeRef{Name: name.Text, Line: name.Line}, nil
}

// ---------- 变量声明 ----------

var varSectionKeywords = map[string]VarSectionKind{
	"VAR":         VarLocal,
	"VAR_INPUT":   VarInput,
	"VAR_OUTPUT":  VarOutput,
	"VAR_IN_OUT":  VarInOut,
	"VAR_GLOBAL":  VarGlobal,
	"VAR_TEMP":    VarTemp,
}

func (p *parser) parseVarSection(kind VarSectionKind) (*VarSection, error) {
	head := p.next() // VAR_xxx
	sec := &VarSection{Kind: kind, Line: head.Line}
	if p.acceptWord("CONSTANT") {
		sec.Constant = true
	}
	if p.acceptWord("RETAIN") {
		sec.Retain = true
	}
	for !p.isWord("END_VAR") {
		if p.is(TEOF) {
			return nil, p.errf("变量声明区缺少 END_VAR")
		}
		decl := &VarDecl{Line: p.peek().Line}
		for {
			name, err := p.expect(TIDENT, "变量名")
			if err != nil {
				return nil, err
			}
			decl.Names = append(decl.Names, name.Text)
			if _, ok := p.accept(TCOMMA); !ok {
				break
			}
		}
		if _, err := p.expect(TCOLON, "':'"); err != nil {
			return nil, err
		}
		ref, err := p.parseTypeRef()
		if err != nil {
			return nil, err
		}
		decl.Type = *ref
		if _, ok := p.accept(TASSIGN); ok {
			init, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			decl.Init = init
		}
		if _, err := p.expect(TSEMI, "';'"); err != nil {
			return nil, err
		}
		sec.Variables = append(sec.Variables, decl)
	}
	p.next() // END_VAR
	return sec, nil
}

// ---------- POU ----------

func (p *parser) parsePOU() (*POU, error) {
	head := p.next()
	kind := KProgram
	switch head.Text {
	case "PROGRAM":
		kind = KProgram
	case "FUNCTION_BLOCK":
		kind = KFunctionBlock
	case "FUNCTION":
		kind = KFunction
	}
	name, err := p.expect(TIDENT, "POU 名称")
	if err != nil {
		return nil, err
	}
	pou := &POU{Kind: kind, Name: name.Text, Line: head.Line}
	if kind == KFunction {
		if _, err := p.expect(TCOLON, "':'"); err != nil {
			return nil, err
		}
		ref, err := p.parseTypeRef()
		if err != nil {
			return nil, err
		}
		pou.ReturnType = ref
	}
	for {
		if p.peek().Kind == TIDENT {
			if k, ok := varSectionKeywords[p.peek().Text]; ok {
				sec, err := p.parseVarSection(k)
				if err != nil {
					return nil, err
				}
				pou.Sections = append(pou.Sections, sec)
				continue
			}
		}
		break
	}
	p.acceptWord("BEGIN") // IEC 风格 BEGIN 可省略
	body, err := p.parseStmtList(endWordsPou)
	if err != nil {
		return nil, err
	}
	pou.Body = body
	switch kind {
	case KProgram:
		if err := p.expectWord("END_PROGRAM"); err != nil {
			return nil, err
		}
	case KFunctionBlock:
		if err := p.expectWord("END_FUNCTION_BLOCK"); err != nil {
			return nil, err
		}
	case KFunction:
		if err := p.expectWord("END_FUNCTION"); err != nil {
			return nil, err
		}
	}
	return pou, nil
}

var endWordsPou = map[string]bool{
	"END_PROGRAM": true, "END_FUNCTION_BLOCK": true, "END_FUNCTION": true,
}

// ---------- 语句 ----------

var blockEndWords = map[string]bool{
	"END_IF": true, "END_CASE": true, "END_FOR": true, "END_WHILE": true,
	"END_REPEAT": true, "ELSE": true, "ELSIF": true, "UNTIL": true,
	"END_PROGRAM": true, "END_FUNCTION_BLOCK": true, "END_FUNCTION": true,
}

func (p *parser) parseStmtList(end map[string]bool) ([]Stmt, error) {
	var stmts []Stmt
	for !p.is(TEOF) {
		if p.peek().Kind == TIDENT && end[p.peek().Text] {
			return stmts, nil
		}
		s, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, s)
	}
	return stmts, p.errf("语句块未正常结束")
}

func (p *parser) parseStmt() (Stmt, error) {
	t := p.peek()
	switch t.Text {
	case "IF":
		return p.parseIf()
	case "CASE":
		return p.parseCase()
	case "FOR":
		return p.parseFor()
	case "WHILE":
		return p.parseWhile()
	case "REPEAT":
		return p.parseRepeat()
	case "EXIT":
		p.next()
		if _, err := p.expect(TSEMI, "';'"); err != nil {
			return nil, err
		}
		return &ExitStmt{Line: t.Line}, nil
	case "RETURN":
		p.next()
		if _, err := p.expect(TSEMI, "';'"); err != nil {
			return nil, err
		}
		return &ReturnStmt{Line: t.Line}, nil
	}
	// 赋值或调用：先解析一个后缀表达式，再看后面是 := 还是 ;
	expr, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, ok := p.accept(TASSIGN); ok {
		val, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TSEMI, "';'"); err != nil {
			return nil, err
		}
		if !isLValue(expr) {
			return nil, &ParseError{File: p.file, Line: t.Line, Msg: "赋值目标必须是变量、数组元素或结构体字段"}
		}
		return &AssignStmt{Target: expr, Value: val, Line: t.Line}, nil
	}
	call, ok := expr.(*CallExpr)
	if !ok {
		return nil, &ParseError{File: p.file, Line: t.Line, Msg: "语句只能是赋值或过程调用"}
	}
	if _, err := p.expect(TSEMI, "';'"); err != nil {
		return nil, err
	}
	return &CallStmt{Call: call, Line: t.Line}, nil
}

func isLValue(e Expr) bool {
	switch e.(type) {
	case *IdentExpr, *IndexExpr, *FieldExpr:
		return true
	}
	return false
}

func (p *parser) parseIf() (*IfStmt, error) {
	head := p.next() // IF
	cond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if err := p.expectWord("THEN"); err != nil {
		return nil, err
	}
	body, err := p.parseStmtList(blockEndWords)
	if err != nil {
		return nil, err
	}
	s := &IfStmt{Cond: cond, Then: body, Line: head.Line}
	for p.acceptWord("ELSIF") {
		c, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if err := p.expectWord("THEN"); err != nil {
			return nil, err
		}
		b, err := p.parseStmtList(blockEndWords)
		if err != nil {
			return nil, err
		}
		s.Elsifs = append(s.Elsifs, &Elsif{Cond: c, Body: b})
	}
	if p.acceptWord("ELSE") {
		b, err := p.parseStmtList(blockEndWords)
		if err != nil {
			return nil, err
		}
		s.Else = b
	}
	if err := p.expectWord("END_IF"); err != nil {
		return nil, err
	}
	if _, err := p.expect(TSEMI, "';'"); err != nil {
		// IEC 允许 END_IF 后无分号，宽松处理
	}
	return s, nil
}

func (p *parser) parseCase() (*CaseStmt, error) {
	head := p.next() // CASE
	sel, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if err := p.expectWord("OF"); err != nil {
		return nil, err
	}
	s := &CaseStmt{Selector: sel, Line: head.Line}
	for !p.isWord("END_CASE") {
		if p.is(TEOF) {
			return nil, p.errf("CASE 缺少 END_CASE")
		}
		if p.acceptWord("ELSE") {
			b, err := p.parseCaseBody()
			if err != nil {
				return nil, err
			}
			s.Else = b
			break
		}
		branch := &CaseBranch{}
		for {
			label, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, ok := p.accept(TDOTDOT); ok {
				hi, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				// 区间标签编码为 BinaryExpr ".."
				label = &BinaryExpr{Op: "..", Left: label, Right: hi, Line: label.Pos()}
			}
			branch.Labels = append(branch.Labels, label)
			if _, ok := p.accept(TCOMMA); !ok {
				break
			}
		}
		if _, err := p.expect(TCOLON, "':'"); err != nil {
			return nil, err
		}
		body, err := p.parseCaseBody()
		if err != nil {
			return nil, err
		}
		branch.Body = body
		s.Cases = append(s.Cases, branch)
	}
	p.next() // END_CASE
	p.accept(TSEMI)
	return s, nil
}

// parseCaseBody 解析 CASE 分支体，在遇到 END_CASE/ELSE 或下一个标签时停下。
// 下一个标签的判定：当前 token 能作为表达式开头，且向后扫描能在第一个分号前
// 遇到顶层 ':'（标签定义的冒号），而不是赋值 ':='。
func (p *parser) parseCaseBody() ([]Stmt, error) {
	var stmts []Stmt
	for {
		if p.is(TEOF) || p.isWord("END_CASE") || p.isWord("ELSE") {
			return stmts, nil
		}
		if p.looksLikeCaseLabel() {
			return stmts, nil
		}
		s, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, s)
	}
}

// looksLikeCaseLabel 前瞻判断当前位置是否是一个 CASE 标签（值列表后跟 ':'）。
func (p *parser) looksLikeCaseLabel() bool {
	save := p.pos
	depth := 0
	for p.pos < len(p.toks) {
		t := p.toks[p.pos]
		switch t.Kind {
		case TLPAREN, TLBRACK:
			depth++
		case TRPAREN, TRBRACK:
			depth--
		case TCOLON:
			if depth == 0 {
				p.pos = save
				return true
			}
		case TSEMI:
			// 语句结束还没遇到顶层冒号，说明是普通语句
			p.pos = save
			return false
		}
		p.pos++
	}
	p.pos = save
	return false
}

func (p *parser) parseFor() (*ForStmt, error) {
	head := p.next() // FOR
	name, err := p.expect(TIDENT, "循环计数器")
	if err != nil {
		return nil, err
	}
	if _, ok := p.accept(TASSIGN); !ok {
		return nil, p.errf("FOR 计数器后应为 :=")
	}
	from, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if err := p.expectWord("TO"); err != nil {
		return nil, err
	}
	to, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	s := &ForStmt{Counter: name.Text, From: from, To: to, Line: head.Line}
	if p.acceptWord("BY") {
		step, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		s.Step = step
	}
	if err := p.expectWord("DO"); err != nil {
		return nil, err
	}
	body, err := p.parseStmtList(blockEndWords)
	if err != nil {
		return nil, err
	}
	s.Body = body
	if err := p.expectWord("END_FOR"); err != nil {
		return nil, err
	}
	p.accept(TSEMI)
	return s, nil
}

func (p *parser) parseWhile() (*WhileStmt, error) {
	head := p.next()
	cond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if err := p.expectWord("DO"); err != nil {
		return nil, err
	}
	body, err := p.parseStmtList(blockEndWords)
	if err != nil {
		return nil, err
	}
	if err := p.expectWord("END_WHILE"); err != nil {
		return nil, err
	}
	p.accept(TSEMI)
	return &WhileStmt{Cond: cond, Body: body, Line: head.Line}, nil
}

func (p *parser) parseRepeat() (*RepeatStmt, error) {
	head := p.next() // REPEAT
	body, err := p.parseStmtList(blockEndWords)
	if err != nil {
		return nil, err
	}
	if err := p.expectWord("UNTIL"); err != nil {
		return nil, err
	}
	cond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if err := p.expectWord("END_REPEAT"); err != nil {
		return nil, err
	}
	p.accept(TSEMI)
	return &RepeatStmt{Body: body, Until: cond, Line: head.Line}, nil
}

// ---------- 表达式（按 IEC 优先级分层） ----------

func (p *parser) parseExpr() (Expr, error) { return p.parseOr() }

func (p *parser) parseBinary(ops map[string]bool, upper func() (Expr, error)) (Expr, error) {
	left, err := upper()
	if err != nil {
		return nil, err
	}
	for p.peek().Kind == TIDENT && ops[p.peek().Text] {
		op := p.next().Text
		right, err := upper()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: op, Left: left, Right: right, Line: right.Pos()}
	}
	return left, nil
}

// IEC 优先级（低->高）：OR/XOR -> AND -> 比较 -> 一元 NOT -> 加减 -> 乘除/MOD -> 幂 -> 后缀
func (p *parser) parseOr() (Expr, error) {
	return p.parseBinary(map[string]bool{"OR": true, "XOR": true}, p.parseAnd)
}

func (p *parser) parseAnd() (Expr, error) {
	return p.parseBinary2(p.parseCompare, func(op string, l, r Expr) Expr {
		return &BinaryExpr{Op: op, Left: l, Right: r, Line: r.Pos()}
	}, "AND")
}

// parseBinary2 处理用独立 token（含 &）的双目运算。
func (p *parser) parseBinary2(upper func() (Expr, error), mk func(string, Expr, Expr) Expr, ops ...string) (Expr, error) {
	left, err := upper()
	if err != nil {
		return nil, err
	}
	opSet := map[string]bool{}
	for _, o := range ops {
		opSet[o] = true
	}
	for {
		op := ""
		if p.peek().Kind == TIDENT && opSet[p.peek().Text] {
			op = p.next().Text
		} else if p.is(TAMP) {
			p.next()
			op = "AND"
		} else {
			return left, nil
		}
		right, err := upper()
		if err != nil {
			return nil, err
		}
		left = mk(op, left, right)
	}
}

// parseUnary 处理一元 NOT 与一元负号（优先级高于比较），下接加减层。
func (p *parser) parseUnary() (Expr, error) {
	if p.isWord("NOT") {
		t := p.next()
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{Op: "NOT", Expr: e, Line: t.Line}, nil
	}
	if p.is(TMINUS) {
		t := p.next()
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{Op: "-", Expr: e, Line: t.Line}, nil
	}
	return p.parseAdd()
}

func (p *parser) parseCompare() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek().Kind {
		case TEQ, TNE, TLT, TGT, TLE, TGE:
			t := p.next()
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = &BinaryExpr{Op: t.Text, Left: left, Right: right, Line: t.Line}
		default:
			return left, nil
		}
	}
}

func (p *parser) parseAdd() (Expr, error) {
	left, err := p.parseMul()
	if err != nil {
		return nil, err
	}
	for p.is(TPLUS) || p.is(TMINUS) {
		t := p.next()
		right, err := p.parseMul()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: t.Text, Left: left, Right: right, Line: t.Line}
	}
	return left, nil
}

func (p *parser) parseMul() (Expr, error) {
	left, err := p.parsePow()
	if err != nil {
		return nil, err
	}
	for p.is(TSTAR) || p.is(TSLASH) || (p.peek().Kind == TIDENT && p.peek().Text == "MOD") {
		t := p.next()
		right, err := p.parsePow()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: t.Text, Left: left, Right: right, Line: t.Line}
	}
	return left, nil
}

func (p *parser) parsePow() (Expr, error) {
	left, err := p.parsePostfix()
	if err != nil {
		return nil, err
	}
	if p.is(TPOW) {
		t := p.next()
		right, err := p.parsePow() // 右结合
		if err != nil {
			return nil, err
		}
		return &BinaryExpr{Op: "**", Left: left, Right: right, Line: t.Line}, nil
	}
	return left, nil
}

func (p *parser) parsePostfix() (Expr, error) {
	e, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.is(TLBRACK):
			t := p.next()
			var idx []Expr
			for {
				ie, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				idx = append(idx, ie)
				if _, ok := p.accept(TCOMMA); !ok {
					break
				}
			}
			if _, err := p.expect(TRBRACK, "']'"); err != nil {
				return nil, err
			}
			e = &IndexExpr{Array: e, Index: idx, Line: t.Line}
		case p.is(TDOTDOT):
			// CASE 区间标签的 '..' 不属于后缀表达式，交回上层处理
			return e, nil
		case p.is(TDOT):
			t := p.next()
			f, err := p.expect(TIDENT, "字段名")
			if err != nil {
				return nil, err
			}
			e = &FieldExpr{Object: e, Field: f.Text, Line: t.Line}
		case p.is(TLPAREN):
			// 函数/功能块调用
			t := p.next()
			ident, ok := e.(*IdentExpr)
			if !ok {
				return nil, &ParseError{File: p.file, Line: t.Line, Msg: "调用目标必须是名称"}
			}
			call := &CallExpr{Callee: ident.Name, Line: t.Line}
			if !p.is(TRPAREN) {
				for {
					arg := &CallArg{}
					// 命名参数：name := expr 或 name => expr
					if p.peek().Kind == TIDENT {
						save := p.pos
						name := p.next().Text
						if p.is(TASSIGN) || p.is(TARROW) {
							op := p.next()
							arg.Name = name
							if op.Kind == TARROW {
								arg.Name = "OUT:" + name
							}
							ex, err := p.parseExpr()
							if err != nil {
								return nil, err
							}
							arg.Expr = ex
						} else {
							p.pos = save
							ex, err := p.parseExpr()
							if err != nil {
								return nil, err
							}
							arg.Expr = ex
						}
					} else {
						ex, err := p.parseExpr()
						if err != nil {
							return nil, err
						}
						arg.Expr = ex
					}
					call.Args = append(call.Args, arg)
					if _, ok := p.accept(TCOMMA); !ok {
						break
					}
				}
			}
			if _, err := p.expect(TRPAREN, "')'"); err != nil {
				return nil, err
			}
			e = call
		default:
			return e, nil
		}
	}
}

func (p *parser) parsePrimary() (Expr, error) {
	t := p.peek()
	switch t.Kind {
	case TNUMBER:
		p.next()
		return &LiteralExpr{Kind: LitInt, Raw: t.Text, Line: t.Line}, nil
	case TTIME:
		p.next()
		return &LiteralExpr{Kind: LitTime, Raw: t.Text, Line: t.Line}, nil
	case TSTRING:
		p.next()
		return &LiteralExpr{Kind: LitString, Raw: t.Text, Line: t.Line}, nil
	case TLPAREN:
		p.next()
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TRPAREN, "')'"); err != nil {
			return nil, err
		}
		return e, nil
	case TIDENT:
		// TRUE/FALSE
		if t.Text == "TRUE" || t.Text == "FALSE" {
			p.next()
			return &LiteralExpr{Kind: LitBool, Raw: t.Text, Line: t.Line}, nil
		}
		p.next()
		// 枚举限定 Type#Value
		if p.is(TSHARP) {
			p.next()
			v, err := p.expect(TIDENT, "枚举值")
			if err != nil {
				return nil, err
			}
			return &EnumExpr{EnumType: t.Text, Value: v.Text, Line: t.Line}, nil
		}
		return &IdentExpr{Name: t.Text, Line: t.Line}, nil
	default:
		return nil, p.errf("表达式中出现意外符号 %s", t.Text)
	}
}
