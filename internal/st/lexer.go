package st

import "fmt"

// LexError 描述词法错误。
type LexError struct {
	Line int
	Col  int
	Msg  string
}

func (e *LexError) Error() string { return fmt.Sprintf("词法错误 第%d行第%d列: %s", e.Line, e.Col, e.Msg) }

// Lex 把 ST 源码切成 Token 序列。标识符与关键字统一转大写，
// 数字/时间/字符串保留原文交给语法层解析。
func Lex(src string) ([]Token, error) {
	l := &lexer{src: src, line: 1, col: 1}
	for {
		l.skipSpaceAndComments()
		if l.eof() {
			l.emit(TEOF, "")
			return l.toks, l.err
		}
		line, col := l.line, l.col
		c := l.peek()
		switch {
		case isIdentStart(c):
			l.lexIdent()
		case c >= '0' && c <= '9':
			l.lexNumberOrTime()
		case c == '\'':
			l.lexString()
		default:
			l.lexSymbol(line, col)
		}
		if l.err != nil {
			return l.toks, l.err
		}
	}
}

type lexer struct {
	src  string
	pos  int
	line int
	col  int
	toks []Token
	err  error
}

func (l *lexer) eof() bool { return l.pos >= len(l.src) }

func (l *lexer) peek() byte { return l.src[l.pos] }

func (l *lexer) peekAt(n int) byte {
	if l.pos+n >= len(l.src) {
		return 0
	}
	return l.src[l.pos+n]
}

func (l *lexer) advance() byte {
	c := l.src[l.pos]
	l.pos++
	if c == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return c
}

func (l *lexer) emit(kind TokenKind, text string) {
	l.toks = append(l.toks, Token{Kind: kind, Text: text, Line: l.line, Col: l.col})
}

func (l *lexer) fail(line, col int, format string, args ...any) {
	if l.err == nil {
		l.err = &LexError{Line: line, Col: col, Msg: fmt.Sprintf(format, args...)}
	}
}

func (l *lexer) skipSpaceAndComments() {
	for !l.eof() {
		c := l.peek()
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			l.advance()
			continue
		}
		// 行注释 //
		if c == '/' && l.peekAt(1) == '/' {
			for !l.eof() && l.peek() != '\n' {
				l.advance()
			}
			continue
		}
		// 块注释 (* ... *)，允许嵌套
		if c == '(' && l.peekAt(1) == '*' {
			startLine, startCol := l.line, l.col
			l.advance()
			l.advance()
			depth := 1
			for !l.eof() && depth > 0 {
				if l.peek() == '(' && l.peekAt(1) == '*' {
					l.advance()
					l.advance()
					depth++
					continue
				}
				if l.peek() == '*' && l.peekAt(1) == ')' {
					l.advance()
					l.advance()
					depth--
					continue
				}
				l.advance()
			}
			if depth > 0 {
				l.fail(startLine, startCol, "块注释未闭合")
				return
			}
			continue
		}
		return
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func (l *lexer) lexIdent() {
	start := l.pos
	for !l.eof() && isIdentPart(l.peek()) {
		l.advance()
	}
	word := l.src[start:l.pos]
	up := upper(word)
	// 时间字面量前缀 T# / TIME#（也允许 LTIME#），整体作为一个 token。
	if (up == "T" || up == "TIME" || up == "LTIME") && !l.eof() && l.peek() == '#' {
		line, col := l.line, l.col
		l.advance() // 吃掉 #
		vStart := l.pos
		// 允许 1d2h3m4s6ms 这类连续时间分量
		for !l.eof() {
			c := l.peek()
			if (c >= '0' && c <= '9') || c == '.' || c == '_' ||
				(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '#' {
				// 第二个 # 只可能出现在负数时间 T#-... 之外的场景，不吞
				if c == '#' {
					break
				}
				l.advance()
				continue
			}
			// 允许 T#-5s 这种负号写法
			if c == '-' && l.pos == vStart {
				l.advance()
				continue
			}
			break
		}
		if l.pos == vStart {
			l.fail(line, col, "时间字面量缺少时间值")
			return
		}
		l.emit(TTIME, upper(l.src[start:l.pos]))
		return
	}
	l.emit(TIDENT, up)
}

// lexNumberOrTime 处理普通数字、基数数字以及时间字面量。
// 时间字面量形如 T#500ms / TIME#1d2h3m4s5ms；
// 基数字面量形如 16#FF、2#1010、8#777。
func (l *lexer) lexNumberOrTime() {
	line, col := l.line, l.col
	// 回看 T# 前缀：由 lexSymbol 在遇到字母时不会进来，这里实际处理的是
	// 以数字开头的形式；T# 前缀在 lexIdent 之后于语法层重组。
	start := l.pos
	isReal := false
	for !l.eof() {
		c := l.peek()
		if c >= '0' && c <= '9' {
			l.advance()
			continue
		}
		if c == '.' && l.peekAt(1) != '.' {
			isReal = true
			l.advance()
			continue
		}
		if c == 'e' || c == 'E' {
			isReal = true
			l.advance()
			if !l.eof() && (l.peek() == '+' || l.peek() == '-') {
				l.advance()
			}
			continue
		}
		break
	}
	text := l.src[start:l.pos]
	// 基数字面量 base#digits
	if !l.eof() && l.peek() == '#' && !isReal {
		l.advance() // #
		dStart := l.pos
		for !l.eof() {
			c := l.peek()
			if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || c == '_' {
				l.advance()
				continue
			}
			break
		}
		if l.pos == dStart {
			l.fail(line, col, "基数字面量缺少数字部分")
			return
		}
		l.emit(TNUMBER, text+"#"+l.src[dStart:l.pos])
		return
	}
	l.emit(TNUMBER, text)
}

func (l *lexer) lexString() {
	line, col := l.line, l.col
	l.advance() // 开头单引号
	var buf []byte
	buf = append(buf, '\'')
	for !l.eof() {
		c := l.advance()
		if c == '\n' {
			l.fail(line, col, "字符串字面量未闭合")
			return
		}
		buf = append(buf, c)
		if c == '$' {
			// IEC 转义：$$ 单引号等，直接保留下一字符
			if !l.eof() {
				buf = append(buf, l.advance())
			}
			continue
		}
		if c == '\'' {
			l.emit(TSTRING, string(buf))
			return
		}
	}
	l.fail(line, col, "字符串字面量未闭合")
}

func (l *lexer) lexSymbol(line, col int) {
	c := l.advance()
	switch c {
	case '(':
		l.emit(TLPAREN, "(")
	case ')':
		l.emit(TRPAREN, ")")
	case '[':
		l.emit(TLBRACK, "[")
	case ']':
		l.emit(TRBRACK, "]")
	case ',':
		l.emit(TCOMMA, ",")
	case ';':
		l.emit(TSEMI, ";")
	case '.':
		if !l.eof() && l.peek() == '.' {
			l.advance()
			l.emit(TDOTDOT, "..")
		} else {
			l.emit(TDOT, ".")
		}
	case ':':
		if !l.eof() && l.peek() == '=' {
			l.advance()
			l.emit(TASSIGN, ":=")
		} else {
			l.emit(TCOLON, ":")
		}
	case '=':
		if !l.eof() && l.peek() == '>' {
			l.advance()
			l.emit(TARROW, "=>")
		} else {
			l.emit(TEQ, "=")
		}
	case '<':
		if !l.eof() && l.peek() == '=' {
			l.advance()
			l.emit(TLE, "<=")
		} else if !l.eof() && l.peek() == '>' {
			l.advance()
			l.emit(TNE, "<>")
		} else {
			l.emit(TLT, "<")
		}
	case '>':
		if !l.eof() && l.peek() == '=' {
			l.advance()
			l.emit(TGE, ">=")
		} else {
			l.emit(TGT, ">")
		}
	case '+':
		l.emit(TPLUS, "+")
	case '-':
		l.emit(TMINUS, "-")
	case '*':
		if !l.eof() && l.peek() == '*' {
			l.advance()
			l.emit(TPOW, "**")
		} else {
			l.emit(TSTAR, "*")
		}
	case '/':
		l.emit(TSLASH, "/")
	case '&':
		l.emit(TAMP, "&")
	case '#':
		l.emit(TSHARP, "#")
	default:
		l.fail(line, col, "无法识别的字符 %q", c)
	}
}

// upper 不依赖 strings 包，按 ASCII 转大写。
func upper(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		b[i] = c
	}
	return string(b)
}
