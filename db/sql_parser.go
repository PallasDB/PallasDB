package db

import (
	"errors"
	"strconv"
	"strings"
)

const maxExprDepth = 1000

type Parser struct {
	buf   string
	pos   int
	depth int
}

func NewParser(s string) Parser {
	return Parser{buf: s, pos: 0}
}

// StmtSelect is a parsed SELECT. A nil cond matches every row and a negative
// limit means "no LIMIT".
type StmtSelect struct {
	table  string
	cols   []any // ExprUnOp | ExprBinOp | ExprTuple | ExprStar | string | *Cell
	cond   any
	limit  int64
	offset int64
}

type NamedCell struct {
	column string
	value  Cell
}

type ExprAssign struct {
	column string
	expr   any // ExprUnOp | ExprBinOp | string | *Cell
}

type StmtCreatTable struct {
	table   string
	cols    []Column
	pkey    []string
	indices [][]string
}

type StmtInsert struct {
	table string
	value []Cell
}

type StmtUpdate struct {
	table string
	cond  any
	value []ExprAssign
}

type StmtDelete struct {
	table string
	cond  any
}

type StmtDropTable struct {
	table string
}

func isSpace(ch byte) bool {
	switch ch {
	case '\t', '\n', '\v', '\f', '\r', ' ':
		return true
	}
	return false
}
func isAlpha(ch byte) bool {
	return 'a' <= (ch|32) && (ch|32) <= 'z'
}
func isDigit(ch byte) bool {
	return '0' <= ch && ch <= '9'
}
func isNameStart(ch byte) bool {
	return isAlpha(ch) || ch == '_'
}
func isNameContinue(ch byte) bool {
	return isAlpha(ch) || isDigit(ch) || ch == '_'
}
func isSeparator(ch byte) bool {
	return ch < 128 && !isNameContinue(ch)
}

func (p *Parser) skipSpaces() {
	for p.pos < len(p.buf) && isSpace(p.buf[p.pos]) {
		p.pos += 1
	}
}

func (p *Parser) tryKeyword(kws ...string) bool {
	save := p.pos
	for _, kw := range kws {
		p.skipSpaces()
		if p.pos+len(kw) > len(p.buf) || !strings.EqualFold(p.buf[p.pos:p.pos+len(kw)], kw) {
			p.pos = save
			return false
		}
		if p.pos+len(kw) < len(p.buf) && !isSeparator(p.buf[p.pos+len(kw)]) {
			p.pos = save
			return false
		}
		p.pos += len(kw)
	}
	return true
}

func (p *Parser) tryPunctuation(tok string) bool {
	p.skipSpaces()
	if p.pos+len(tok) > len(p.buf) || p.buf[p.pos:p.pos+len(tok)] != tok {
		return false
	}
	p.pos += len(tok)
	return true
}

func (p *Parser) tryName() (string, bool) {
	p.skipSpaces()
	start, cur := p.pos, p.pos
	if cur >= len(p.buf) || !isNameStart(p.buf[cur]) {
		return "", false
	}
	cur++
	for cur < len(p.buf) && isNameContinue(p.buf[cur]) {
		cur++
	}
	p.pos = cur
	return p.buf[start:cur], true
}

func (p *Parser) parseValue(out *Cell) error {
	p.skipSpaces()
	if p.pos >= len(p.buf) {
		return errors.New("expect value")
	}
	ch := p.buf[p.pos]
	if ch == '"' || ch == '\'' {
		return p.parseString(out)
	} else if isDigit(ch) || ch == '-' || ch == '+' {
		return p.parseInt(out)
	} else {
		return errors.New("expect value")
	}
}

func (p *Parser) parseString(out *Cell) error {
	quote := p.buf[p.pos]
	cur := p.pos + 1
	for cur < len(p.buf) {
		ch := p.buf[cur]
		switch ch {
		case '\\':
			cur++
			if cur < len(p.buf) && (p.buf[cur] == '"' || p.buf[cur] == '\'') {
				out.Str = append(out.Str, p.buf[cur])
				cur++
			} else {
				return errors.New("bad escape")
			}
		case quote:
			out.Type = TypeStr
			p.pos = cur + 1
			return nil
		default:
			out.Str = append(out.Str, p.buf[cur])
			cur++
		}
	}
	return errors.New("string is not terminated")
}

func (p *Parser) parseInt(out *Cell) (err error) {
	start, cur := p.pos, p.pos
	if p.buf[cur] == '-' || p.buf[cur] == '+' {
		cur++
	}
	for cur < len(p.buf) && isDigit(p.buf[cur]) {
		cur++
	}

	if out.I64, err = strconv.ParseInt(p.buf[start:cur], 10, 64); err != nil {
		return err
	}
	out.Type = TypeI64
	p.pos = cur
	return nil
}

func (p *Parser) parseAssign(out *ExprAssign) (err error) {
	var ok bool
	out.column, ok = p.tryName()
	if !ok {
		return errors.New("expect column")
	}
	if !p.tryPunctuation("=") {
		return errors.New("expect =")
	}
	out.expr, err = p.parseExpr()
	return err
}

func (p *Parser) parseSelect(out *StmtSelect) (err error) {
	out.limit = -1
	for !p.tryKeyword("FROM") {
		if len(out.cols) > 0 && !p.tryPunctuation(",") {
			return errors.New("expect comma")
		}
		if p.tryStar() {
			out.cols = append(out.cols, &ExprStar{})
			continue
		}
		expr, err := p.parseExpr()
		if err != nil {
			return err
		}
		out.cols = append(out.cols, expr)
	}
	if len(out.cols) == 0 {
		return errors.New("expect column list")
	}
	var ok bool
	if out.table, ok = p.tryName(); !ok {
		return errors.New("expect table name")
	}
	if out.cond, err = p.parseWhere(); err != nil {
		return err
	}
	if err = p.parseLimit(out); err != nil {
		return err
	}
	return p.parseEnd()
}

// tryStar accepts `*` only where a whole column item is expected, that is when
// it is immediately followed by `,` or FROM. Everywhere else `*` stays
// multiplication.
func (p *Parser) tryStar() bool {
	save := p.pos
	if !p.tryPunctuation("*") {
		return false
	}
	star := p.pos
	if p.tryPunctuation(",") || p.tryKeyword("FROM") {
		p.pos = star
		return true
	}
	p.pos = save
	return false
}

// parseWhere parses an optional WHERE clause. A missing WHERE yields a nil
// condition, which matches every row.
func (p *Parser) parseWhere() (expr any, err error) {
	if !p.tryKeyword("WHERE") {
		return nil, nil
	}
	return p.parseExpr()
}

// parseLimit parses an optional `LIMIT n [OFFSET m]` clause.
func (p *Parser) parseLimit(out *StmtSelect) (err error) {
	if !p.tryKeyword("LIMIT") {
		return nil
	}
	if out.limit, err = p.parseCount(); err != nil {
		return err
	}
	if p.tryKeyword("OFFSET") {
		if out.offset, err = p.parseCount(); err != nil {
			return err
		}
	}
	return nil
}

func (p *Parser) parseCount() (int64, error) {
	cell := Cell{}
	if err := p.parseValue(&cell); err != nil {
		return 0, err
	}
	if cell.Type != TypeI64 || cell.I64 < 0 {
		return 0, errors.New("expect a non-negative integer")
	}
	return cell.I64, nil
}

func (p *Parser) parseEnd() error {
	if !p.tryPunctuation(";") {
		return errors.New("expect ;")
	}
	return nil
}

func (p *Parser) parseCommaList(item func() error) error {
	if !p.tryPunctuation("(") {
		return errors.New("expect (")
	}
	comma := false
	for !p.tryPunctuation(")") {
		if comma && !p.tryPunctuation(",") {
			return errors.New("expect ,")
		}
		comma = true
		if err := item(); err != nil {
			return err
		}
	}
	return nil
}

func (p *Parser) parseNameItem(out *[]string) error {
	name, ok := p.tryName()
	if !ok {
		return errors.New("expect name")
	}
	*out = append(*out, name)
	return nil
}

func (p *Parser) parseCreateTableItem(out *StmtCreatTable) error {
	if p.tryKeyword("PRIMARY", "KEY") {
		return p.parseCommaList(func() error { return p.parseNameItem(&out.pkey) })
	} else if p.tryKeyword("INDEX") {
		index := []string{}
		err := p.parseCommaList(func() error { return p.parseNameItem(&index) })
		if err == nil {
			out.indices = append(out.indices, index)
		}
		return err
	}

	var ok bool
	col := Column{}
	if col.Name, ok = p.tryName(); !ok {
		return errors.New("expect name")
	}
	kind, ok := p.tryName()
	if !ok {
		return errors.New("expect name")
	}
	switch kind {
	case "int64":
		col.Type = TypeI64
	case "string":
		col.Type = TypeStr
	default:
		return errors.New("unknown column type")
	}
	out.cols = append(out.cols, col)
	return nil
}

func (p *Parser) parseCreateTable(out *StmtCreatTable) error {
	var ok bool
	if out.table, ok = p.tryName(); !ok {
		return errors.New("expect table name")
	}
	if err := p.parseCommaList(func() error { return p.parseCreateTableItem(out) }); err != nil {
		return err
	}
	return p.parseEnd()
}

func (p *Parser) parseValueItem(out *[]Cell) error {
	cell := Cell{}
	if err := p.parseValue(&cell); err != nil {
		return err
	}
	*out = append(*out, cell)
	return nil
}

func (p *Parser) parseInsert(out *StmtInsert) error {
	var ok bool
	if out.table, ok = p.tryName(); !ok {
		return errors.New("expect table name")
	}
	if !p.tryKeyword("VALUES") {
		return errors.New("expect VALUES")
	}
	if err := p.parseCommaList(func() error { return p.parseValueItem(&out.value) }); err != nil {
		return err
	}
	return p.parseEnd()
}

func (p *Parser) parseUpdate(out *StmtUpdate) (err error) {
	var ok bool
	if out.table, ok = p.tryName(); !ok {
		return errors.New("expect table name")
	}
	if !p.tryKeyword("SET") {
		return errors.New("expect SET")
	}
	for {
		expr := ExprAssign{}
		if err := p.parseAssign(&expr); err != nil {
			return err
		}
		out.value = append(out.value, expr)
		if !p.tryPunctuation(",") {
			break
		}
	}
	if out.cond, err = p.parseWhere(); err != nil {
		return err
	}
	return p.parseEnd()
}

func (p *Parser) parseDelete(out *StmtDelete) (err error) {
	var ok bool
	if out.table, ok = p.tryName(); !ok {
		return errors.New("expect table name")
	}
	if out.cond, err = p.parseWhere(); err != nil {
		return err
	}
	return p.parseEnd()
}

func (p *Parser) parseDropTable(out *StmtDropTable) error {
	var ok bool
	if out.table, ok = p.tryName(); !ok {
		return errors.New("expect table name")
	}
	return p.parseEnd()
}

func (p *Parser) parseStmt() (out any, err error) {
	if p.tryKeyword("SELECT") {
		stmt := &StmtSelect{}
		err = p.parseSelect(stmt)
		out = stmt
	} else if p.tryKeyword("CREATE", "TABLE") {
		stmt := &StmtCreatTable{}
		err = p.parseCreateTable(stmt)
		out = stmt
	} else if p.tryKeyword("INSERT", "INTO") {
		stmt := &StmtInsert{}
		err = p.parseInsert(stmt)
		out = stmt
	} else if p.tryKeyword("UPDATE") {
		stmt := &StmtUpdate{}
		err = p.parseUpdate(stmt)
		out = stmt
	} else if p.tryKeyword("DELETE", "FROM") {
		stmt := &StmtDelete{}
		err = p.parseDelete(stmt)
		out = stmt
	} else if p.tryKeyword("DROP", "TABLE") {
		stmt := &StmtDropTable{}
		err = p.parseDropTable(stmt)
		out = stmt
	} else {
		err = errors.New("unknown statement")
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (p *Parser) isEnd() bool {
	p.skipSpaces()
	return p.pos >= len(p.buf)
}

// ParseStmt parses exactly one SQL statement, which must consume the whole
// input. The returned value is one of the *Stmt* types and is meant to be fed
// to (*DB).ExecStmt.
func ParseStmt(s string) (any, error) {
	p := NewParser(s)
	stmt, err := p.parseStmt()
	if err != nil {
		return nil, err
	}
	if !p.isEnd() {
		return nil, errors.New("trailing garbage after the statement")
	}
	return stmt, nil
}

type ExprOp uint8

const (
	OP_ADD ExprOp = 1  // +
	OP_SUB ExprOp = 2  // -
	OP_MUL ExprOp = 3  // *
	OP_DIV ExprOp = 4  // /
	OP_EQ  ExprOp = 10 // =
	OP_NE  ExprOp = 11 // !=
	OP_LE  ExprOp = 12 // <=
	OP_GE  ExprOp = 13 // >=
	OP_LT  ExprOp = 14 // <
	OP_GT  ExprOp = 15 // >
	OP_AND ExprOp = 20 // AND
	OP_OR  ExprOp = 21 // OR
	OP_NOT ExprOp = 30 // not
	OP_NEG ExprOp = 31 // -
)

type ExprBinOp struct {
	op    ExprOp
	left  any
	right any
}

type ExprUnOp struct {
	op  ExprOp
	kid any
}

type ExprTuple struct {
	kids []any
}

// ExprStar is the `*` of `SELECT *`. It only ever appears in a select list and
// is expanded to the table columns by the executor.
type ExprStar struct{}

var errTooDeep = errors.New("expression is nested too deeply")

// enter guards every recursive descent into a sub-expression so that a
// pathological input cannot overflow the stack. Callers must pair a successful
// enter with a deferred leave.
func (p *Parser) enter() error {
	p.depth++
	if p.depth > maxExprDepth {
		p.depth--
		return errTooDeep
	}
	return nil
}

func (p *Parser) leave() { p.depth-- }

func (p *Parser) parseTuple() (expr any, err error) {
	kids := []any{}
	err = p.parseCommaList(func() error {
		expr, err := p.parseExpr()
		if err != nil {
			return err
		}
		kids = append(kids, expr)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(kids) == 0 {
		return nil, errors.New("empty tuple")
	} else if len(kids) == 1 {
		return kids[0], nil
	} else {
		return &ExprTuple{kids}, nil
	}
}

func (p *Parser) parseAtom() (expr any, err error) {
	if p.tryPunctuation("(") {
		p.pos--
		if err = p.enter(); err != nil {
			return nil, err
		}
		defer p.leave()
		return p.parseTuple()
	}
	if name, ok := p.tryName(); ok {
		return name, nil
	}
	cell := &Cell{}
	if err := p.parseValue(cell); err != nil {
		return nil, err
	}
	return cell, nil
}

func (p *Parser) parseBinop(
	tokens []string, ops []ExprOp,
	inner func() (any, error),
) (any, error) {
	check(len(tokens) == len(ops))

	left, err := inner()
	if err != nil {
		return nil, err
	}

	for ok := true; ok; {
		ok = false
		for i := range tokens {
			if !p.tryPunctuation(tokens[i]) && !p.tryKeyword(tokens[i]) {
				continue
			}

			ok = true
			right, err := inner()
			if err != nil {
				return nil, err
			}
			left = &ExprBinOp{op: ops[i], left: left, right: right}
			break
		}
	}
	return left, nil
}

func (p *Parser) parseExpr() (any, error) {
	return p.parseOr()
}

func (p *Parser) parseOr() (any, error) {
	return p.parseBinop([]string{"OR"}, []ExprOp{OP_OR}, p.parseAnd)
}

func (p *Parser) parseAnd() (any, error) {
	return p.parseBinop([]string{"AND"}, []ExprOp{OP_AND}, p.parseNot)
}

func (p *Parser) parseNot() (expr any, err error) {
	if p.tryKeyword("NOT") {
		if err = p.enter(); err != nil {
			return nil, err
		}
		defer p.leave()
		if expr, err = p.parseNot(); err != nil {
			return nil, err
		}
		return &ExprUnOp{op: OP_NOT, kid: expr}, nil
	}
	return p.parseCmp()
}

func (p *Parser) parseCmp() (any, error) {
	return p.parseBinop(
		[]string{"=", "!=", "<>", "<=", ">=", "<", ">"},
		[]ExprOp{OP_EQ, OP_NE, OP_NE, OP_LE, OP_GE, OP_LT, OP_GT},
		p.parseAdd,
	)
}

func (p *Parser) parseAdd() (any, error) {
	return p.parseBinop([]string{"+", "-"}, []ExprOp{OP_ADD, OP_SUB}, p.parseMul)
}

func (p *Parser) parseMul() (any, error) {
	return p.parseBinop([]string{"*", "/"}, []ExprOp{OP_MUL, OP_DIV}, p.parseNeg)
}

func (p *Parser) parseNeg() (expr any, err error) {
	if p.tryPunctuation("-") {
		if err = p.enter(); err != nil {
			return nil, err
		}
		defer p.leave()
		if expr, err = p.parseNeg(); err != nil {
			return nil, err
		}
		return &ExprUnOp{op: OP_NEG, kid: expr}, nil
	}
	return p.parseAtom()
}
