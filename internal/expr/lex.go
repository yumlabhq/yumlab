package expr

import "fmt"

// tokKind classifies a lexical token of the GitHub Actions expression language.
type tokKind int

const (
	tokEOF tokKind = iota
	tokIdent
	tokNumber
	tokString
	tokOp
)

// token carries its absolute offset in the scanned source so that every
// reference we extract can be mapped back to a file:line later on.
type token struct {
	kind tokKind
	val  string // identifier, operator, decoded string, or raw number
	off  int    // absolute offset of the first byte
	end  int    // absolute offset just past the last byte
}

// lexer turns an expression body into tokens. base is the absolute offset of
// src[0] inside the original scalar, so offsets stay meaningful to the caller.
type lexer struct {
	src  string
	base int
	pos  int
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	// GitHub allows '-' inside property names: needs.build-app.outputs.sha.
	// The expression language has no arithmetic, so '-' is unambiguous here.
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '-'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func (l *lexer) skipSpace() {
	for l.pos < len(l.src) && isSpace(l.src[l.pos]) {
		l.pos++
	}
}

// next returns the next token. A lexical error aborts the whole expression,
// which the caller reports as UNKNOWN rather than as a finding.
func (l *lexer) next() (token, error) {
	l.skipSpace()
	if l.pos >= len(l.src) {
		return token{kind: tokEOF, off: l.base + l.pos, end: l.base + l.pos}, nil
	}

	start := l.pos
	c := l.src[l.pos]

	switch {
	case isIdentStart(c):
		for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
			l.pos++
		}
		return l.tok(tokIdent, l.src[start:l.pos], start), nil

	case isDigit(c):
		return l.lexNumber(start)

	case c == '\'':
		return l.lexString(start)

	case c == '"':
		// Double quotes are not valid in GitHub expressions. Refusing here is
		// safer than guessing: the expression becomes UNKNOWN.
		return token{}, fmt.Errorf("offset %d: double quotes are not valid in expressions, use single quotes", l.base+start)
	}

	// Multi-byte operators before single-byte ones.
	for _, op := range []string{"==", "!=", "<=", ">=", "&&", "||"} {
		if len(l.src)-l.pos >= 2 && l.src[l.pos:l.pos+2] == op {
			l.pos += 2
			return l.tok(tokOp, op, start), nil
		}
	}

	switch c {
	case '(', ')', '[', ']', '.', ',', '*', '!', '<', '>', '-':
		l.pos++
		return l.tok(tokOp, string(c), start), nil
	}

	return token{}, fmt.Errorf("offset %d: unexpected character %q", l.base+start, string(c))
}

func (l *lexer) tok(k tokKind, v string, start int) token {
	return token{kind: k, val: v, off: l.base + start, end: l.base + l.pos}
}

func (l *lexer) lexNumber(start int) (token, error) {
	if l.src[l.pos] == '0' && l.pos+1 < len(l.src) && (l.src[l.pos+1] == 'x' || l.src[l.pos+1] == 'X') {
		l.pos += 2
		digits := 0
		for l.pos < len(l.src) && isHex(l.src[l.pos]) {
			l.pos++
			digits++
		}
		if digits == 0 {
			return token{}, fmt.Errorf("offset %d: malformed hexadecimal number", l.base+start)
		}
		return l.tok(tokNumber, l.src[start:l.pos], start), nil
	}

	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1]) {
		l.pos++
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		save := l.pos
		l.pos++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
		if l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
				l.pos++
			}
		} else {
			l.pos = save
		}
	}
	return l.tok(tokNumber, l.src[start:l.pos], start), nil
}

func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// lexString reads a single-quoted literal. A doubled quote (”) is an escaped
// quote, which is the only escape the language has.
func (l *lexer) lexString(start int) (token, error) {
	l.pos++ // opening quote
	var b []byte
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '\'' {
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == '\'' {
				b = append(b, '\'')
				l.pos += 2
				continue
			}
			l.pos++
			return l.tok(tokString, string(b), start), nil
		}
		b = append(b, c)
		l.pos++
	}
	return token{}, fmt.Errorf("offset %d: unterminated string literal", l.base+start)
}
