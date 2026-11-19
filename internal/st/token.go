package st

// TokenKind 是词法单元种类。
type TokenKind int

const (
	TEOF TokenKind = iota
	TIDENT     // 标识符 / 关键字（按内容在语法层区分）
	TNUMBER    // 数字字面量原文：整数、实数、基数字 2#1010、16#FF
	TTIME      // 时间字面量原文：T#500ms、TIME#1d2h
	TSTRING    // 字符串字面量（含外层引号）
	TLPAREN    // (
	TRPAREN    // )
	TLBRACK    // [
	TRBRACK    // ]
	TCOMMA     // ,
	TSEMI      // ;
	TCOLON     // :
	TASSIGN    // :=
	TDOT       // .
	TDOTDOT    // ..
	TARROW     // =>
	TSHARP     // # 枚举限定符 Color#RED
	TPLUS      // +
	TMINUS     // -
	TSTAR      // *
	TSLASH     // /
	TPOW       // **
	TEQ        // =
	TNE        // <>
	TLT        // <
	TGT        // >
	TLE        // <=
	TGE        // >=
	TAMP       // &
	TBAD       // 无法识别的字符
)

// Token 是带位置的词法单元。
type Token struct {
	Kind TokenKind
	Text string
	Line int
	Col  int
}

func (t Token) String() string {
	if t.Kind == TEOF {
		return "EOF"
	}
	return t.Text
}

// keywords 是 IEC 61131-3 结构化文本保留字（统一按大写比较）。
var keywords = map[string]bool{
	"PROGRAM": true, "END_PROGRAM": true,
	"FUNCTION_BLOCK": true, "END_FUNCTION_BLOCK": true,
	"FUNCTION": true, "END_FUNCTION": true,
	"TYPE": true, "END_TYPE": true, "STRUCT": true,
	"VAR": true, "VAR_INPUT": true, "VAR_OUTPUT": true, "VAR_IN_OUT": true,
	"VAR_GLOBAL": true, "VAR_TEMP": true, "END_VAR": true,
	"CONSTANT": true, "RETAIN": true,
	"IF": true, "THEN": true, "ELSIF": true, "ELSE": true, "END_IF": true,
	"CASE": true, "OF": true, "END_CASE": true,
	"FOR": true, "WHILE": true, "DO": true, "REPEAT": true, "UNTIL": true,
	"END_FOR": true, "END_WHILE": true, "END_REPEAT": true,
	"EXIT": true, "RETURN": true,
	"AND": true, "OR": true, "XOR": true, "NOT": true, "MOD": true,
	"TRUE": true, "FALSE": true,
	"ARRAY": true,
	"BY": true, "END_STRUCT": true,
}
