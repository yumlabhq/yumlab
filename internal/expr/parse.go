package expr

import "fmt"

// parser is a recursive-descent parser for the GitHub Actions expression
// grammar. Operator precedence, lowest to highest:
//
//	||  &&  (== != < <= > >=)  (! -)  (postfix . [] ())
type parser struct {
	lex  *lexer
	tok  token
	prev token
}

func newParser(src string, base int) *parser {
	return &parser{lex: &lexer{src: src, base: base}}
}

func (p *parser) advance() error {
	t, err := p.lex.next()
	if err != nil {
		return err
	}
	p.prev = p.tok
	p.tok = t
	return nil
}

func (p *parser) isOp(op string) bool {
	return p.tok.kind == tokOp && p.tok.val == op
}

func (p *parser) expectOp(op string) error {
	if !p.isOp(op) {
		return fmt.Errorf("offset %d: expected %q", p.tok.off, op)
	}
	return p.advance()
}

// parseExpr parses a complete expression and verifies that nothing is left over.
func (p *parser) parseExpr() (node, error) {
	if err := p.advance(); err != nil {
		return nil, err
	}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.tok.kind != tokEOF {
		return nil, fmt.Errorf("offset %d: unexpected trailing input", p.tok.off)
	}
	return n, nil
}

func (p *parser) parseOr() (node, error) {
	x, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.isOp("||") {
		if err := p.advance(); err != nil {
			return nil, err
		}
		y, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		x = &binaryNode{span{x.Off(), y.End()}, "||", x, y}
	}
	return x, nil
}

func (p *parser) parseAnd() (node, error) {
	x, err := p.parseComparison()
	if err != nil {
		return nil, err
	}
	for p.isOp("&&") {
		if err := p.advance(); err != nil {
			return nil, err
		}
		y, err := p.parseComparison()
		if err != nil {
			return nil, err
		}
		x = &binaryNode{span{x.Off(), y.End()}, "&&", x, y}
	}
	return x, nil
}

var comparisonOps = map[string]bool{"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true}

func (p *parser) parseComparison() (node, error) {
	x, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.tok.kind == tokOp && comparisonOps[p.tok.val] {
		op := p.tok.val
		if err := p.advance(); err != nil {
			return nil, err
		}
		y, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		x = &binaryNode{span{x.Off(), y.End()}, op, x, y}
	}
	return x, nil
}

func (p *parser) parseUnary() (node, error) {
	if p.isOp("!") || p.isOp("-") {
		op := p.tok.val
		off := p.tok.off
		if err := p.advance(); err != nil {
			return nil, err
		}
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &unaryNode{span{off, x.End()}, op, x}, nil
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() (node, error) {
	x, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.isOp("."):
			if err := p.advance(); err != nil {
				return nil, err
			}
			switch {
			case p.tok.kind == tokIdent:
				x = &propNode{span{x.Off(), p.tok.end}, x, p.tok.val}
			case p.isOp("*"):
				x = &propNode{span{x.Off(), p.tok.end}, x, "*"}
			default:
				return nil, fmt.Errorf("offset %d: expected a property name after '.'", p.tok.off)
			}
			if err := p.advance(); err != nil {
				return nil, err
			}

		case p.isOp("["):
			if err := p.advance(); err != nil {
				return nil, err
			}
			idx, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			end := p.tok.end
			if err := p.expectOp("]"); err != nil {
				return nil, err
			}
			x = &indexNode{span{x.Off(), end}, x, idx}

		case p.isOp("("):
			// Only a bare identifier is callable: there are no first-class
			// functions in this language.
			id, ok := x.(*identNode)
			if !ok {
				return nil, fmt.Errorf("offset %d: cannot call this expression", p.tok.off)
			}
			args, end, err := p.parseArgs()
			if err != nil {
				return nil, err
			}
			x = &callNode{span{id.Off(), end}, id.name, args}

		default:
			return x, nil
		}
	}
}

func (p *parser) parseArgs() ([]node, int, error) {
	if err := p.advance(); err != nil { // consume '('
		return nil, 0, err
	}
	var args []node
	if p.isOp(")") {
		end := p.tok.end
		return args, end, p.advance()
	}
	for {
		a, err := p.parseOr()
		if err != nil {
			return nil, 0, err
		}
		args = append(args, a)
		if p.isOp(",") {
			if err := p.advance(); err != nil {
				return nil, 0, err
			}
			continue
		}
		end := p.tok.end
		if err := p.expectOp(")"); err != nil {
			return nil, 0, err
		}
		return args, end, nil
	}
}

func (p *parser) parsePrimary() (node, error) {
	t := p.tok
	switch {
	case t.kind == tokIdent:
		if err := p.advance(); err != nil {
			return nil, err
		}
		switch lower(t.val) {
		case "true", "false", "null":
			return &literalNode{span{t.off, t.end}, t.val, false}, nil
		}
		return &identNode{span{t.off, t.end}, t.val}, nil

	case t.kind == tokNumber:
		if err := p.advance(); err != nil {
			return nil, err
		}
		return &literalNode{span{t.off, t.end}, t.val, false}, nil

	case t.kind == tokString:
		if err := p.advance(); err != nil {
			return nil, err
		}
		return &literalNode{span{t.off, t.end}, t.val, true}, nil

	case p.isOp("("):
		off := t.off
		if err := p.advance(); err != nil {
			return nil, err
		}
		x, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		end := p.tok.end
		if err := p.expectOp(")"); err != nil {
			return nil, err
		}
		return &groupNode{span{off, end}, x}, nil

	case p.isOp("*"):
		// Bare '*' only appears as a filter target, e.g. fromJSON(x).*.
		if err := p.advance(); err != nil {
			return nil, err
		}
		return &identNode{span{t.off, t.end}, "*"}, nil
	}

	if t.kind == tokEOF {
		return nil, fmt.Errorf("offset %d: unexpected end of expression", t.off)
	}
	return nil, fmt.Errorf("offset %d: unexpected token %q", t.off, t.val)
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
