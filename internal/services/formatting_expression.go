package services

import (
	"fmt"
	"strings"
	"unicode"
)

// Boolean match expressions for formatting rules.
//
// Grammar (whitespace-insensitive):
//
//	expr       := orExpr
//	orExpr     := andExpr (("||" | "or") andExpr)*
//	andExpr    := unaryExpr (("&&" | "and") unaryExpr)*
//	unaryExpr  := ("!" | "not") unaryExpr | primary
//	primary    := "(" expr ")" | comparison
//	comparison := field ("==" | "!=") stringLiteral
//	field      := "source_kind" | "trigger" | "channel" | "skill"
//	literal    := double- or single-quoted string
//
// Field names are case-insensitive; `last_skill` is accepted as an alias for
// `skill`. Comparisons trim whitespace on both sides and compare exactly,
// matching the simple match-field semantics.

// exprField maps an expression field name (lower-cased) to its FormatFlow
// accessor. Kept in sync with the simple match_* columns.
var exprFields = map[string]func(FormatFlow) string{
	"source_kind": func(f FormatFlow) string { return f.SourceKind },
	"trigger":     func(f FormatFlow) string { return f.TriggerUUID },
	"channel":     func(f FormatFlow) string { return f.ChannelUUID },
	"skill":       func(f FormatFlow) string { return f.LastSkill },
	"last_skill":  func(f FormatFlow) string { return f.LastSkill },
}

// matchExprNode is a parsed expression AST node.
type matchExprNode interface {
	eval(flow FormatFlow) bool
}

type exprAnd struct{ left, right matchExprNode }
type exprOr struct{ left, right matchExprNode }
type exprNot struct{ inner matchExprNode }
type exprCompare struct {
	field  string // key into exprFields
	value  string
	negate bool // true for !=
}

func (n exprAnd) eval(f FormatFlow) bool { return n.left.eval(f) && n.right.eval(f) }
func (n exprOr) eval(f FormatFlow) bool  { return n.left.eval(f) || n.right.eval(f) }
func (n exprNot) eval(f FormatFlow) bool { return !n.inner.eval(f) }
func (n exprCompare) eval(f FormatFlow) bool {
	got := strings.TrimSpace(exprFields[n.field](f))
	equal := got == strings.TrimSpace(n.value)
	if n.negate {
		return !equal
	}
	return equal
}

// ParseMatchExpression parses a boolean match expression into an evaluable
// AST. Errors are position-aware and phrased for operators editing rules in
// the UI.
func ParseMatchExpression(input string) (matchExprNode, error) {
	p := &exprParser{input: input}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	p.skipSpaces()
	if p.pos < len(p.input) {
		return nil, p.errorf("unexpected %q — expected && / || or end of expression", p.peekToken())
	}
	return node, nil
}

// ValidateMatchExpression reports whether the expression parses. Empty input
// is valid (no expression configured).
func ValidateMatchExpression(input string) error {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	_, err := ParseMatchExpression(input)
	return err
}

// EvalMatchExpression parses and evaluates in one shot. Returns an error on
// parse failure so callers can decide the fail-safe behavior.
func EvalMatchExpression(input string, flow FormatFlow) (bool, error) {
	node, err := ParseMatchExpression(input)
	if err != nil {
		return false, err
	}
	return node.eval(flow), nil
}

// exprParser is a hand-rolled recursive-descent parser. No dependencies, no
// allocations beyond the AST.
type exprParser struct {
	input string
	pos   int
}

func (p *exprParser) errorf(format string, args ...any) error {
	return fmt.Errorf("position %d: %s", p.pos+1, fmt.Sprintf(format, args...))
}

func (p *exprParser) skipSpaces() {
	for p.pos < len(p.input) && unicode.IsSpace(rune(p.input[p.pos])) {
		p.pos++
	}
}

// peekToken returns a short snippet at the cursor for error messages.
func (p *exprParser) peekToken() string {
	rest := p.input[p.pos:]
	if len(rest) > 12 {
		rest = rest[:12] + "…"
	}
	return rest
}

// tryKeyword consumes a case-insensitive word keyword (or/and/not) followed
// by a non-word boundary. Returns true when consumed.
func (p *exprParser) tryKeyword(kw string) bool {
	p.skipSpaces()
	end := p.pos + len(kw)
	if end > len(p.input) || !strings.EqualFold(p.input[p.pos:end], kw) {
		return false
	}
	if end < len(p.input) && isWordChar(p.input[end]) {
		return false
	}
	p.pos = end
	return true
}

// trySymbol consumes a literal symbol like "&&" or "(".
func (p *exprParser) trySymbol(sym string) bool {
	p.skipSpaces()
	if strings.HasPrefix(p.input[p.pos:], sym) {
		p.pos += len(sym)
		return true
	}
	return false
}

func isWordChar(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func (p *exprParser) parseOr() (matchExprNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		if !p.trySymbol("||") && !p.tryKeyword("or") {
			return left, nil
		}
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = exprOr{left: left, right: right}
	}
}

func (p *exprParser) parseAnd() (matchExprNode, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		if !p.trySymbol("&&") && !p.tryKeyword("and") {
			return left, nil
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = exprAnd{left: left, right: right}
	}
}

func (p *exprParser) parseUnary() (matchExprNode, error) {
	p.skipSpaces()
	// `!` negates a term; `!=` is a comparison operator and never starts one.
	isNot := strings.HasPrefix(p.input[p.pos:], "!") && !strings.HasPrefix(p.input[p.pos:], "!=")
	if isNot {
		p.pos++
	} else {
		isNot = p.tryKeyword("not")
	}
	if isNot {
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return exprNot{inner: inner}, nil
	}
	return p.parsePrimary()
}

func (p *exprParser) parsePrimary() (matchExprNode, error) {
	if p.trySymbol("(") {
		node, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if !p.trySymbol(")") {
			return nil, p.errorf("missing closing parenthesis")
		}
		return node, nil
	}
	return p.parseComparison()
}

func (p *exprParser) parseComparison() (matchExprNode, error) {
	p.skipSpaces()
	if p.pos >= len(p.input) {
		return nil, p.errorf("expected a condition like: skill == \"netbox\"")
	}

	start := p.pos
	for p.pos < len(p.input) && isWordChar(p.input[p.pos]) {
		p.pos++
	}
	word := p.input[start:p.pos]
	if word == "" {
		return nil, p.errorf("expected a field name (source_kind, trigger, channel, skill), got %q", p.peekToken())
	}
	field := strings.ToLower(word)
	if _, ok := exprFields[field]; !ok {
		p.pos = start
		return nil, p.errorf("unknown field %q — valid fields: source_kind, trigger, channel, skill", word)
	}

	var negate bool
	switch {
	case p.trySymbol("=="):
	case p.trySymbol("!="):
		negate = true
	case p.trySymbol("="):
		// A single '=' is a common typo; accept it as equality for
		// friendliness (the UI builder always emits '==').
	default:
		return nil, p.errorf("expected == or != after %q", word)
	}

	value, err := p.parseStringLiteral()
	if err != nil {
		return nil, err
	}
	return exprCompare{field: field, value: value, negate: negate}, nil
}

func (p *exprParser) parseStringLiteral() (string, error) {
	p.skipSpaces()
	if p.pos >= len(p.input) {
		return "", p.errorf("expected a quoted value, e.g. \"netbox\"")
	}
	quote := p.input[p.pos]
	if quote != '"' && quote != '\'' {
		return "", p.errorf("value must be quoted, e.g. \"netbox\" — got %q", p.peekToken())
	}
	p.pos++
	start := p.pos
	for p.pos < len(p.input) && p.input[p.pos] != quote {
		p.pos++
	}
	if p.pos >= len(p.input) {
		return "", p.errorf("unterminated string — missing closing %c", quote)
	}
	value := p.input[start:p.pos]
	p.pos++ // closing quote
	return value, nil
}
