package db

import (
	"bytes"
	"cmp"
	"errors"
	"slices"
	"strconv"
	"strings"
)

// evalExpr evaluates expr against row. The expression tree is caller supplied
// (and may be deeply left nested, e.g. `1+1+1+...`), so the recursion is
// depth-capped instead of trusting the parser's own cap.
func evalExpr(schema *Schema, row Row, expr any) (*Cell, error) {
	return evalExprAt(schema, row, expr, 0)
}

func evalExprAt(schema *Schema, row Row, expr any, depth int) (*Cell, error) {
	if depth > maxExprDepth {
		return nil, errTooDeep
	}
	switch e := expr.(type) {
	case string:
		idx := slices.IndexFunc(schema.Cols, func(col Column) bool {
			return col.Name == e
		})
		if idx < 0 {
			return nil, errors.New("unknown column")
		}
		return &row[idx], nil
	case *Cell:
		return e, nil
	case *ExprUnOp:
		kid, err := evalExprAt(schema, row, e.kid, depth+1)
		if err != nil {
			return nil, err
		}
		if e.op == OP_NEG && kid.Type == TypeI64 {
			return &Cell{Type: TypeI64, I64: -kid.I64}, nil
		} else if e.op == OP_NOT && kid.Type == TypeI64 {
			b := int64(0)
			if kid.I64 == 0 {
				b = 1
			}
			return &Cell{Type: TypeI64, I64: b}, nil
		} else {
			return nil, errors.New("bad unary op")
		}
	case *ExprBinOp:
		// A tuple is not a value, but a comparison of two tuples is: compare
		// element-wise, left to right, exactly as the index-prefix pushdown in
		// makeRange does. Without this a tuple predicate the planner cannot
		// turn into a key range has nowhere to go, so a legal query fails.
		if isCmpOp(e.op) {
			lt, lok := e.left.(*ExprTuple)
			rt, rok := e.right.(*ExprTuple)
			if lok || rok {
				if !lok || !rok {
					return nil, errors.New("cannot compare a tuple with a scalar")
				}
				return evalTupleCmp(schema, row, e.op, lt, rt, depth)
			}
		}
		left, err := evalExprAt(schema, row, e.left, depth+1)
		if err != nil {
			return nil, err
		}
		right, err := evalExprAt(schema, row, e.right, depth+1)
		if err != nil {
			return nil, err
		}
		if left.Type != right.Type {
			return nil, errors.New("binary op type mismatch")
		}

		out := &Cell{Type: left.Type}
		switch e.op {
		// comparison
		case OP_EQ, OP_NE, OP_LE, OP_GE, OP_LT, OP_GT:
			r := 0
			switch left.Type {
			case TypeI64:
				r = cmp.Compare(left.I64, right.I64)
			case TypeStr:
				r = bytes.Compare(left.Str, right.Str)
			default:
				return nil, errors.New("bad cell type")
			}
			// A comparison is a boolean, whatever the operands were: typing it
			// after the operands made `a = 'x' AND ...` a type error.
			out.Type = TypeI64
			out.Str = nil
			b := false
			switch e.op {
			case OP_EQ:
				b = (r == 0)
			case OP_NE:
				b = (r != 0)
			case OP_LE:
				b = (r <= 0)
			case OP_GE:
				b = (r >= 0)
			case OP_LT:
				b = (r < 0)
			case OP_GT:
				b = (r > 0)
			}
			if b {
				out.I64 = 1
			}
			return out, nil
		}

		switch {
		// string concat
		case e.op == OP_ADD && out.Type == TypeStr:
			if len(left.Str)+len(right.Str) > MaxEntrySize {
				return nil, errors.New("string too large")
			}
			out.Str = slices.Concat(left.Str, right.Str)
		// arithmetic
		case e.op == OP_ADD && out.Type == TypeI64:
			out.I64 = left.I64 + right.I64
		case e.op == OP_SUB && out.Type == TypeI64:
			out.I64 = left.I64 - right.I64
		case e.op == OP_MUL && out.Type == TypeI64:
			out.I64 = left.I64 * right.I64
		case e.op == OP_DIV && out.Type == TypeI64:
			if right.I64 == 0 {
				return nil, errors.New("division by 0")
			}
			out.I64 = left.I64 / right.I64
		// boolean
		case e.op == OP_AND && out.Type == TypeI64:
			if left.I64 != 0 && right.I64 != 0 {
				out.I64 = 1
			}
		case e.op == OP_OR && out.Type == TypeI64:
			if left.I64 != 0 || right.I64 != 0 {
				out.I64 = 1
			}
		default:
			return nil, errors.New("bad binary op")
		}
		return out, nil
	case *ExprTuple:
		return nil, errors.New("a tuple is not a value")
	default:
		return nil, errors.New("unsupported expression")
	}
}

func isCmpOp(op ExprOp) bool {
	switch op {
	case OP_EQ, OP_NE, OP_LE, OP_GE, OP_LT, OP_GT:
		return true
	default:
		return false
	}
}

// evalTupleCmp compares two tuples lexicographically. Arity must match: a
// comparison between tuples of different lengths has no meaning here, and
// silently padding one side would answer a question nobody asked.
func evalTupleCmp(schema *Schema, row Row, op ExprOp, left, right *ExprTuple, depth int) (*Cell, error) {
	if len(left.kids) != len(right.kids) {
		return nil, errors.New("tuple comparison arity mismatch")
	}
	r := 0
	for i := range left.kids {
		lc, err := evalExprAt(schema, row, left.kids[i], depth+1)
		if err != nil {
			return nil, err
		}
		rc, err := evalExprAt(schema, row, right.kids[i], depth+1)
		if err != nil {
			return nil, err
		}
		if lc.Type != rc.Type {
			return nil, errors.New("binary op type mismatch")
		}
		switch lc.Type {
		case TypeI64:
			r = cmp.Compare(lc.I64, rc.I64)
		case TypeStr:
			r = bytes.Compare(lc.Str, rc.Str)
		default:
			return nil, errors.New("bad cell type")
		}
		if r != 0 {
			break
		}
	}
	return boolCell(cmpResult(op, r)), nil
}

func cmpResult(op ExprOp, r int) bool {
	switch op {
	case OP_EQ:
		return r == 0
	case OP_NE:
		return r != 0
	case OP_LE:
		return r <= 0
	case OP_GE:
		return r >= 0
	case OP_LT:
		return r < 0
	case OP_GT:
		return r > 0
	default:
		return false
	}
}

func boolCell(b bool) *Cell {
	out := &Cell{Type: TypeI64}
	if b {
		out.I64 = 1
	}
	return out
}

func cell2str(cell *Cell) string {
	switch cell.Type {
	case TypeI64:
		return strconv.FormatInt(cell.I64, 10)
	case TypeStr:
		return string(cell.Str)
	default:
		return ""
	}
}

func exprop2str(op ExprOp) string {
	switch op {
	case OP_ADD:
		return "+"
	case OP_SUB:
		return "-"
	case OP_MUL:
		return "*"
	case OP_DIV:
		return "/"
	case OP_EQ:
		return "="
	case OP_NE:
		return "!="
	case OP_LE:
		return "<="
	case OP_GE:
		return ">="
	case OP_LT:
		return "<"
	case OP_GT:
		return ">"
	case OP_AND:
		return "AND"
	case OP_OR:
		return "OR"
	case OP_NOT:
		return "NOT"
	case OP_NEG:
		return "-"
	default:
		return "?"
	}
}

// expr2str renders an expression for a result header. It is display only: an
// unrepresentable node degrades to a placeholder rather than panicking, and the
// recursion is depth-capped like evalExpr.
func expr2str(expr any) string {
	return expr2strAt(expr, 0)
}

func expr2strAt(expr any, depth int) string {
	if depth > maxExprDepth {
		return "..."
	}
	switch e := expr.(type) {
	case string:
		return e
	case *Cell:
		return cell2str(e)
	case *ExprStar:
		return "*"
	case *ExprUnOp:
		switch e.op {
		case OP_NEG:
			return "-" + expr2strAt(e.kid, depth+1)
		case OP_NOT:
			return "NOT " + expr2strAt(e.kid, depth+1)
		default:
			return "?"
		}
	case *ExprBinOp:
		return "(" + expr2strAt(e.left, depth+1) + " " + exprop2str(e.op) + " " + expr2strAt(e.right, depth+1) + ")"
	case *ExprTuple:
		parts := make([]string, len(e.kids))
		for i, kid := range e.kids {
			parts[i] = expr2strAt(kid, depth+1)
		}
		return "(" + strings.Join(parts, ", ") + ")"
	default:
		return "?"
	}
}

func exprs2header(cols []any) []string {
	header := make([]string, len(cols))
	for i, expr := range cols {
		header[i] = expr2str(expr)
	}
	return header
}
