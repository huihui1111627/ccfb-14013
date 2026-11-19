package st

// Node 是所有 AST 节点的通用接口。
type Node interface {
	Pos() (line int)
}

// ---------- 顶层单元 ----------

// CompilationUnit 是一个 ST 源文件解析结果。
type CompilationUnit struct {
	File       string
	Types      []*TypeDecl      // TYPE ... END_TYPE
	GlobalVars []*VarSection    // VAR_GLOBAL
	POUs       []*POU
	Line       int
}

func (n *CompilationUnit) Pos() int { return n.Line }

// TypeDecl 是 TYPE 块中的自定义类型（枚举/结构/数组别名）。
type TypeDecl struct {
	Name    string
	Type    TypeRef
	Line    int
}

func (n *TypeDecl) Pos() int { return n.Line }

// POUKind 区分 PROGRAM / FUNCTION_BLOCK / FUNCTION。
type POUKind int

const (
	KProgram POUKind = iota
	KFunctionBlock
	KFunction
)

// POU 是程序组织单元。
type POU struct {
	Kind     POUKind
	Name     string
	Sections []*VarSection
	Body     []Stmt
	// FUNCTION 的返回类型
	ReturnType *TypeRef
	Line       int
}

func (n *POU) Pos() int { return n.Line }

// VarSection 是一段变量声明区。
type VarSection struct {
	Kind      VarSectionKind
	Constant  bool
	Retain    bool
	Variables []*VarDecl
	Line      int
}

type VarSectionKind int

const (
	VarLocal VarSectionKind = iota
	VarInput
	VarOutput
	VarInOut
	VarGlobal
	VarTemp
)

// VarDecl 是单条变量声明：a, b : ARRAY[1..3] OF INT := 0;
type VarDecl struct {
	Names   []string
	Type    TypeRef
	Init    Expr // 可空
	Line    int
}

func (n *VarDecl) Pos() int { return n.Line }

// TypeRef 是语法层面的类型引用，类型检查阶段解析成 Type。
type TypeRef struct {
	Name     string     // 基本类型名或自定义类型名（大写）
	ArrayOf  *TypeRef   // 数组元素类型；非 nil 表示 ARRAY
	Dims     []*Range   // 数组各维上下界
	Struct   []*VarDecl // 内联结构体字段；非 nil 表示 STRUCT
	Line     int
}

func (r *TypeRef) Pos() int { return r.Line }

// Range 是数组维的常量上下界表达式。
type Range struct {
	Low  Expr
	High Expr
}

// ---------- 语句 ----------

type Stmt interface {
	Node
	stmtNode()
}

type AssignStmt struct {
	Target Expr // 左值：变量/数组元素/结构体字段
	Value  Expr
	Line   int
}

func (s *AssignStmt) Pos() int { return s.Line }
func (s *AssignStmt) stmtNode() {}

type CallStmt struct {
	Call *CallExpr
	Line int
}

func (s *CallStmt) Pos() int { return s.Line }
func (s *CallStmt) stmtNode() {}

type IfStmt struct {
	Cond   Expr
	Then   []Stmt
	Elsifs []*Elsif
	Else   []Stmt
	Line   int
}

type Elsif struct {
	Cond Expr
	Body []Stmt
}

func (s *IfStmt) Pos() int { return s.Line }
func (s *IfStmt) stmtNode() {}

type CaseStmt struct {
	Selector Expr
	Cases    []*CaseBranch
	Else     []Stmt
	Line     int
}

type CaseBranch struct {
	Labels []Expr // 单个值或 low..high
	Body   []Stmt
}

func (s *CaseStmt) Pos() int { return s.Line }
func (s *CaseStmt) stmtNode() {}

type ForStmt struct {
	Counter string
	From    Expr
	To      Expr
	Step    Expr // 可空，默认 1
	Body    []Stmt
	Line    int
}

func (s *ForStmt) Pos() int { return s.Line }
func (s *ForStmt) stmtNode() {}

type WhileStmt struct {
	Cond Expr
	Body []Stmt
	Line int
}

func (s *WhileStmt) Pos() int { return s.Line }
func (s *WhileStmt) stmtNode() {}

type RepeatStmt struct {
	Body  []Stmt
	Until Expr
	Line  int
}

func (s *RepeatStmt) Pos() int { return s.Line }
func (s *RepeatStmt) stmtNode() {}

type ExitStmt struct{ Line int }

func (s *ExitStmt) Pos() int { return s.Line }
func (s *ExitStmt) stmtNode() {}

type ReturnStmt struct{ Line int }

func (s *ReturnStmt) Pos() int { return s.Line }
func (s *ReturnStmt) stmtNode() {}

// ---------- 表达式 ----------

type Expr interface {
	Node
	exprNode()
}

// IdentExpr 普通标识符引用。
type IdentExpr struct {
	Name string
	Line int
}

func (e *IdentExpr) Pos() int { return e.Line }
func (e *IdentExpr) exprNode() {}

// LiteralExpr 字面量：kind 区分 int/real/bool/string/time/bitstring/enum。
type LiteralExpr struct {
	Kind   LiteralKind
	Raw    string // 原文
	Line   int
}

type LiteralKind int

const (
	LitInt LiteralKind = iota
	LitReal
	LitBool
	LitString
	LitTime
	LitBitString // 2#/8#/16# 位串
)

func (e *LiteralExpr) Pos() int { return e.Line }
func (e *LiteralExpr) exprNode() {}

// EnumExpr 枚举值 Color#RED（也支持直接 RED，在类型检查时消歧）。
type EnumExpr struct {
	EnumType string // 可空
	Value    string
	Line     int
}

func (e *EnumExpr) Pos() int { return e.Line }
func (e *EnumExpr) exprNode() {}

type UnaryExpr struct {
	Op   string // NOT / -
	Expr Expr
	Line int
}

func (e *UnaryExpr) Pos() int { return e.Line }
func (e *UnaryExpr) exprNode() {}

type BinaryExpr struct {
	Op    string // AND OR XOR = <> < > <= >= + - * / MOD ** &
	Left  Expr
	Right Expr
	Line  int
}

func (e *BinaryExpr) Pos() int { return e.Line }
func (e *BinaryExpr) exprNode() {}

// IndexExpr 数组下标访问 a[i, j]。
type IndexExpr struct {
	Array  Expr
	Index  []Expr
	Line   int
}

func (e *IndexExpr) Pos() int { return e.Line }
func (e *IndexExpr) exprNode() {}

// FieldExpr 结构体字段访问 s.f。
type FieldExpr struct {
	Object Expr
	Field  string
	Line   int
}

func (e *FieldExpr) Pos() int { return e.Line }
func (e *FieldExpr) exprNode() {}

// CallArg 是调用实参：位置参数（Name 为空）或命名参数 Name := V / Name=> V。
type CallArg struct {
	Name string // 大写，可空
	Expr Expr
}

// CallExpr 函数/功能块调用。功能块输出参数用 => 标记（OutParam=true）。
type CallExpr struct {
	Callee string
	Args   []*CallArg
	Line   int
}

func (e *CallExpr) Pos() int { return e.Line }
func (e *CallExpr) exprNode() {}
