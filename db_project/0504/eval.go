package db0504

import (
	"bytes"
	"cmp"
	"errors"
	"slices"
)

func evalExpr(schema *Schema, row Row, expr interface{}) (*Cell, error) {
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
		kid, err := evalExpr(schema, row, e.kid)
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
		left, err := evalExpr(schema, row, e.left)
		if err != nil {
			return nil, err
		}
		right, err := evalExpr(schema, row, e.right)
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
			switch out.Type {
			case TypeI64:
				r = cmp.Compare(left.I64, right.I64)
			case TypeStr:
				r = bytes.Compare(left.Str, right.Str)
			default:
				panic("unreachable")
			}
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
	default:
		panic("unreachable")
	}
}

// QzBQWVJJOUhU https://trialofcode.org/
