package db0803

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

type DB struct {
	KV     KV
	tables map[string]Schema
}

func (db *DB) Open() error {
	db.tables = map[string]Schema{}
	return db.KV.Open()
}

func (db *DB) Close() error { return db.KV.Close() }

func (db *DB) Select(schema *Schema, row Row) (ok bool, err error) {
	key := row.EncodeKey(schema, 0)
	val, ok, err := db.KV.Get(key)
	if err != nil || !ok {
		return ok, err
	}
	if err = row.DecodeVal(schema, val); err != nil {
		return false, err
	}
	return true, nil
}

func (db *DB) update(schema *Schema, row Row, mode UpdateMode) (updated bool, err error) {
	key := row.EncodeKey(schema, 0)
	val := row.EncodeVal(schema)
	oldVal, exist, err := db.KV.Get(key)
	if err != nil {
		return false, err
	}

	switch mode {
	case ModeUpsert:
		updated = !exist || !bytes.Equal(oldVal, val)
	case ModeInsert:
		updated = !exist
	case ModeUpdate:
		updated = exist && !bytes.Equal(oldVal, val)
	default:
		panic("unreachable")
	}
	if !updated {
		return false, nil
	}

	if exist {
		oldRow := slices.Clone(row)
		if err = oldRow.DecodeVal(schema, oldVal); err != nil {
			return false, err
		}
		if _, err = db.Delete(schema, oldRow); err != nil {
			return false, err
		}
	}

	for i := 0; i < len(schema.Indices) && err == nil; i++ {
		if i > 0 {
			key, val = row.EncodeKey(schema, i), nil
		}
		updated, err = db.KV.SetEx(key, val, ModeInsert)
		if err == nil && !updated {
			panic("impossible")
		}
	}
	return updated, err
}

func (db *DB) Insert(schema *Schema, row Row) (updated bool, err error) {
	return db.update(schema, row, ModeInsert)
}

func (db *DB) Upsert(schema *Schema, row Row) (updated bool, err error) {
	return db.update(schema, row, ModeUpsert)
}

func (db *DB) Update(schema *Schema, row Row) (updated bool, err error) {
	return db.update(schema, row, ModeUpdate)
}

func (db *DB) Delete(schema *Schema, row Row) (deleted bool, err error) {
	for i := 0; i < len(schema.Indices) && err == nil; i++ {
		key := row.EncodeKey(schema, i)
		deleted, err = db.KV.Del(key)
		if err == nil && !deleted {
			if i != 0 {
				return false, errors.New("inconsistent index")
			}
			break
		}
	}
	return deleted, err
}

type RowIterator struct {
	db      *DB
	schema  *Schema
	indexNo int
	iter    *RangedKVIter
	valid   bool
	row     Row
}

func (iter *RowIterator) decodeKVIter() (bool, error)

func (iter *RowIterator) Valid() bool {
	return iter.valid
}

func (iter *RowIterator) Row() Row {
	check(iter.valid)
	return iter.row
}

func (iter *RowIterator) Next() (err error) {
	if err = iter.iter.Next(); err != nil {
		return err
	}
	iter.valid, err = iter.decodeKVIter()
	return err
}

func (db *DB) Seek(schema *Schema, row Row) (*RowIterator, error) {
	start := make([]Cell, len(schema.Indices[0]))
	for i, idx := range schema.Indices[0] {
		check(row[idx].Type == schema.Cols[idx].Type)
		start[i] = row[idx]
	}
	return db.Range(schema, &RangeReq{
		StartCmp: OP_GE,
		StopCmp:  OP_LE, // +inf
		Start:    start,
		Stop:     nil,
		IndexNo:  0,
	})
}

type RangeReq struct {
	StartCmp ExprOp // <= >= < >
	StopCmp  ExprOp
	Start    []Cell
	Stop     []Cell
	IndexNo  int
}

func suffixPositive(op ExprOp) bool {
	switch op {
	case OP_LE, OP_GT:
		return true
	case OP_GE, OP_LT:
		return false
	default:
		panic("unreachable")
	}
}

func isDescending(op ExprOp) bool {
	switch op {
	case OP_LE, OP_LT:
		return true
	case OP_GE, OP_GT:
		return false
	default:
		panic("unreachable")
	}
}

func (db *DB) Range(schema *Schema, req *RangeReq) (out *RowIterator, err error) {
	check(isDescending(req.StartCmp) != isDescending(req.StopCmp))
	start := EncodeKeyPrefix(schema, req.IndexNo, req.Start, suffixPositive(req.StartCmp))
	stop := EncodeKeyPrefix(schema, req.IndexNo, req.Stop, suffixPositive(req.StopCmp))
	desc := isDescending(req.StartCmp)
	out = &RowIterator{db: db, schema: schema, indexNo: req.IndexNo, row: schema.NewRow()}
	if out.iter, err = db.KV.Range(start, stop, desc); err != nil {
		return nil, err
	}
	if out.valid, err = out.decodeKVIter(); err != nil {
		return nil, err
	}
	return out, nil
}

type SQLResult struct {
	Updated int
	Header  []string
	Values  []Row
}

func (db *DB) ExecStmt(stmt interface{}) (r SQLResult, err error) {
	switch ptr := stmt.(type) {
	case *StmtCreatTable:
		err = db.execCreateTable(ptr)
	case *StmtSelect:
		r.Header = exprs2header(ptr.cols)
		r.Values, err = db.execSelect(ptr)
	case *StmtInsert:
		r.Updated, err = db.execInsert(ptr)
	case *StmtUpdate:
		r.Updated, err = db.execUpdate(ptr)
	case *StmtDelete:
		r.Updated, err = db.execDelete(ptr)
	default:
		panic("unreachable")
	}
	return
}

func (db *DB) execCreateTable(stmt *StmtCreatTable) (err error) {
	if _, err := db.GetSchema(stmt.table); err == nil {
		return errors.New("duplicate table name")
	}

	schema := Schema{
		Table: stmt.table,
		Cols:  stmt.cols,
	}
	for i, names := range append([][]string{stmt.pkey}, stmt.indices...) {
		index, err := lookupColumns(stmt.cols, names)
		if err != nil {
			return err
		}
		if i > 0 {
			index = addPKeyToIndex(index, schema.Indices[0])
		}
		schema.Indices = append(schema.Indices, index)
	}
	if len(schema.Indices) > 256 {
		return errors.New("too many indices")
	}

	val, err := json.Marshal(schema)
	check(err == nil)
	if _, err = db.KV.Set([]byte("@schema_"+stmt.table), val); err != nil {
		return err
	}

	db.tables[schema.Table] = schema
	return nil
}

func (db *DB) GetSchema(table string) (Schema, error) {
	schema, ok := db.tables[table]
	if !ok {
		val, ok, err := db.KV.Get([]byte("@schema_" + table))
		if err == nil && ok {
			err = json.Unmarshal(val, &schema)
		}
		if err != nil {
			return Schema{}, err
		}
		if !ok {
			return Schema{}, errors.New("table is not found")
		}
		db.tables[table] = schema
	}
	return schema, nil
}

func addPKeyToIndex(index []int, pkey []int) []int {
	for _, idx := range pkey {
		if !slices.Contains(index, idx) {
			index = append(index, idx)
		}
	}
	return index
}

func lookupColumns(cols []Column, names []string) (indices []int, err error) {
	for _, name := range names {
		idx := slices.IndexFunc(cols, func(col Column) bool {
			return col.Name == name
		})
		if idx < 0 {
			return nil, errors.New("column is not found")
		}
		indices = append(indices, idx)
	}
	return
}

func extractPKey(schema *Schema, pkey []NamedCell) (cells []Cell, ok bool) {
	if len(schema.Indices[0]) != len(pkey) {
		return nil, false
	}
	for _, idx := range schema.Indices[0] {
		col := schema.Cols[idx]
		i := slices.IndexFunc(pkey, func(e NamedCell) bool {
			return col.Name == e.column && col.Type == e.value.Type
		})
		if i < 0 {
			return nil, false
		}
		cells = append(cells, pkey[i].value)
	}
	return cells, true
}

func matchAllEq(cond interface{}, out []NamedCell) ([]NamedCell, bool) {
	binop, ok := cond.(*ExprBinOp)
	if ok && binop.op == OP_AND {
		if out, ok = matchAllEq(binop.left, out); !ok {
			return nil, false
		}
		if out, ok = matchAllEq(binop.right, out); !ok {
			return nil, false
		}
		return out, true
	} else if ok && binop.op == OP_EQ {
		left, right := binop.left, binop.right
		name, ok := left.(string)
		if !ok {
			left, right = right, left
			name, ok = left.(string)
		}
		if !ok {
			return nil, false
		}
		cell, ok := right.(*Cell)
		if !ok {
			return nil, false
		}
		return append(out, NamedCell{name, *cell}), true
	}
	return nil, false
}

func asNameList(expr interface{}) (out []string, ok bool) {
	switch e := expr.(type) {
	case string:
		return []string{e}, true
	case *ExprTuple:
		for _, kid := range e.kids {
			if s, ok := kid.(string); ok {
				out = append(out, s)
			} else {
				return nil, false
			}
		}
		return out, true
	}
	return nil, false
}

func asCellList(expr interface{}) (out []Cell, ok bool) {
	switch e := expr.(type) {
	case *Cell:
		return []Cell{*e}, true
	case *ExprTuple:
		for _, kid := range e.kids {
			if s, ok := kid.(*Cell); ok {
				out = append(out, *s)
			} else {
				return nil, false
			}
		}
		return out, true
	}
	return nil, false
}

func matchCmp(cond interface{}) (ExprOp, []string, []Cell, bool) {
	binop, ok := cond.(*ExprBinOp)
	if !ok {
		return 0, nil, nil, false
	}
	switch binop.op {
	case OP_LE, OP_GE, OP_LT, OP_GT:
	default:
		return 0, nil, nil, false
	}

	op := binop.op
	left, right := binop.left, binop.right
	names, ok := asNameList(left)
	if !ok {
		left, right = right, left
		names, ok = asNameList(left)
		switch op {
		case OP_LE:
			op = OP_GE
		case OP_GE:
			op = OP_LE
		case OP_LT:
			op = OP_GT
		case OP_GT:
			op = OP_LT
		}
	}
	if !ok {
		return 0, nil, nil, false
	}
	cells, ok := asCellList(right)
	if !ok {
		return 0, nil, nil, false
	}
	return op, names, cells, true
}

func isPKeyPrefix(schema *Schema, indexNo int, cols []string, cells []Cell) bool {
	if len(cols) != len(cells) || len(cols) > len(schema.Cols) {
		return false
	}
	for i := range cols {
		col := schema.Cols[schema.Indices[indexNo][i]]
		if col.Name != cols[i] || col.Type != cells[i].Type {
			return false
		}
	}
	return true
}

func matchRangeByIndex(schema *Schema, indexNo int, cond interface{}) (*RangeReq, bool) {
	binop, ok := cond.(*ExprBinOp)
	if ok && binop.op == OP_AND {
		op1, cols1, cells1, ok := matchCmp(binop.left)
		if !ok || !isPKeyPrefix(schema, indexNo, cols1, cells1) {
			return nil, false
		}
		op2, cols2, cells2, ok := matchCmp(binop.left)
		if !ok || !isPKeyPrefix(schema, indexNo, cols2, cells2) {
			return nil, false
		}
		if isDescending(op1) != isDescending(op2) {
			return nil, false
		}
		if isDescending(op1) {
			op1, op2, cells1, cells2 = op2, op1, cells2, cells1
		}
		return &RangeReq{
			StartCmp: op1,
			StopCmp:  op2,
			Start:    cells1,
			Stop:     cells2,
			IndexNo:  indexNo,
		}, true
	} else if ok {
		op1, cols1, cells1, ok := matchCmp(cond)
		if !ok || !isPKeyPrefix(schema, indexNo, cols1, cells1) {
			return nil, false
		}
		op2 := OP_LE
		if isDescending(op1) {
			op2 = OP_GE
		}
		return &RangeReq{
			StartCmp: op1,
			StopCmp:  op2,
			Start:    cells1,
			Stop:     nil,
			IndexNo:  indexNo,
		}, true
	}
	return nil, false
}

func matchRange(schema *Schema, cond interface{}) (*RangeReq, bool) {
	for indexNo := range schema.Indices {
		if req, ok := matchRangeByIndex(schema, indexNo, cond); ok {
			return req, ok
		}
	}
	return nil, false
}

func makeRange(schema *Schema, cond interface{}) (*RangeReq, error) {
	if keys, ok := matchAllEq(cond, nil); ok {
		if pkey, ok := extractPKey(schema, keys); ok {
			return &RangeReq{
				StartCmp: OP_GE,
				StopCmp:  OP_LE,
				Start:    pkey,
				Stop:     pkey,
			}, nil
		}
	}
	if req, ok := matchRange(schema, cond); ok {
		return req, nil
	}
	return nil, errors.New("unimplemented WHERE")
}

func (db *DB) execCond(schema *Schema, cond interface{}) (*RowIterator, error) {
	req, err := makeRange(schema, cond)
	if err != nil {
		return nil, err
	}
	return db.Range(schema, req)
}

func (db *DB) execSelect(stmt *StmtSelect) (output []Row, err error) {
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return nil, err
	}

	iter, err := db.execCond(&schema, stmt.cond)
	if err != nil {
		return nil, err
	}

	for ; err == nil && iter.Valid(); err = iter.Next() {
		row := iter.Row()
		computed := make(Row, len(stmt.cols))
		for i, expr := range stmt.cols {
			cell, err := evalExpr(&schema, row, expr)
			if err != nil {
				return nil, err
			}
			computed[i] = *cell
		}
		output = append(output, computed)
	}
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (db *DB) execInsert(stmt *StmtInsert) (count int, err error) {
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}
	if len(schema.Cols) != len(stmt.value) {
		return 0, errors.New("schema mismatch")
	}
	for i := range schema.Cols {
		if schema.Cols[i].Type != stmt.value[i].Type {
			return 0, errors.New("schema mismatch")
		}
	}

	updated, err := db.Insert(&schema, stmt.value)
	if err != nil {
		return 0, err
	}
	if updated {
		count++
	}
	return count, nil
}

func fillNonPKey(schema *Schema, updates []NamedCell, out Row) error {
	for _, expr := range updates {
		idx := slices.IndexFunc(schema.Cols, func(col Column) bool {
			return col.Name == expr.column && col.Type == expr.value.Type
		})
		if idx < 0 || slices.Contains(schema.Indices[0], idx) {
			return errors.New("cannot update column")
		}
		out[idx] = expr.value
	}
	return nil
}

func (db *DB) execUpdate(stmt *StmtUpdate) (count int, err error) {
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}

	iter, err := db.execCond(&schema, stmt.cond)
	if err != nil {
		return 0, err
	}

	oldRows := []Row{}
	for ; err == nil && iter.Valid(); err = iter.Next() {
		oldRows = append(oldRows, slices.Clone(iter.Row()))
	}
	if err != nil {
		return 0, err
	}

	for _, row := range oldRows {
		updates := make([]NamedCell, len(stmt.value))
		for i, assign := range stmt.value {
			cell, err := evalExpr(&schema, row, assign.expr)
			if err != nil {
				return 0, err
			}
			updates[i] = NamedCell{column: assign.column, value: *cell}
		}
		if err = fillNonPKey(&schema, updates, row); err != nil {
			return 0, err
		}
		updated, err := db.Update(&schema, row)
		if err != nil {
			return 0, err
		}
		if updated {
			count++
		}
	}

	return count, nil
}

func (db *DB) execDelete(stmt *StmtDelete) (count int, err error) {
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}

	iter, err := db.execCond(&schema, stmt.cond)
	if err != nil {
		return 0, err
	}

	oldRows := []Row{}
	for ; err == nil && iter.Valid(); err = iter.Next() {
		oldRows = append(oldRows, slices.Clone(iter.Row()))
	}
	if err != nil {
		return 0, err
	}

	for _, row := range oldRows {
		updated, err := db.Delete(&schema, row)
		if err != nil {
			return 0, err
		}
		if updated {
			count++
		}
	}
	return count, nil
}

// QzBQWVJJOUhU https://trialofcode.org/
