package db0902

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

type DB struct {
	KV KV
}

type DBTX struct {
	kv     *KVTX
	tables map[string]Schema
}

func (db *DB) NewTX() *DBTX {
	return &DBTX{kv: db.KV.NewTX(), tables: map[string]Schema{}}
}

func (tx *DBTX) Abort() { tx.kv.Abort() }

func (tx *DBTX) Commit() error { return tx.kv.Commit() }

func (tx *DBTX) NewTX() *DBTX {
	return &DBTX{kv: tx.kv.NewTX(), tables: map[string]Schema{}}
}

func (db *DB) Open() error { return db.KV.Open() }

func (db *DB) Close() error { return db.KV.Close() }

func (db *DB) Select(schema *Schema, row Row) (ok bool, err error) {
	tx := db.NewTX()
	defer tx.Abort()
	return tx.Select(schema, row)
}

func (tx *DBTX) Select(schema *Schema, row Row) (ok bool, err error) {
	key := row.EncodeKey(schema, 0)
	val, ok, err := tx.kv.Get(key)
	if err != nil || !ok {
		return ok, err
	}
	if err = row.DecodeVal(schema, val); err != nil {
		return false, err
	}
	return true, nil
}

func (tx *DBTX) update(schema *Schema, row Row, mode UpdateMode) (updated bool, err error) {
	key := row.EncodeKey(schema, 0)
	val := row.EncodeVal(schema)
	oldVal, exist, err := tx.kv.Get(key)
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
		if _, err = tx.delete(schema, oldRow); err != nil {
			return false, err
		}
	}

	for i := 0; i < len(schema.Indices) && err == nil; i++ {
		if i > 0 {
			key, val = row.EncodeKey(schema, i), nil
		}
		updated, err = tx.kv.SetEx(key, val, ModeInsert)
		if err == nil && !updated {
			panic("impossible")
		}
	}
	return updated, err
}

func (tx *DBTX) Insert(schema *Schema, row Row) (updated bool, err error) {
	tx = tx.NewTX()
	updated, err = tx.update(schema, row, ModeInsert)
	return abortOrCommit(tx, updated, err)
}

func (tx *DBTX) Upsert(schema *Schema, row Row) (updated bool, err error) {
	tx = tx.NewTX()
	updated, err = tx.update(schema, row, ModeUpsert)
	return abortOrCommit(tx, updated, err)
}

func (tx *DBTX) Update(schema *Schema, row Row) (updated bool, err error) {
	tx = tx.NewTX()
	updated, err = tx.update(schema, row, ModeUpdate)
	return abortOrCommit(tx, updated, err)
}

func (tx *DBTX) delete(schema *Schema, row Row) (deleted bool, err error) {
	for i := 0; i < len(schema.Indices) && err == nil; i++ {
		key := row.EncodeKey(schema, i)
		deleted, err = tx.kv.Del(key)
		if err == nil && !deleted {
			if i != 0 {
				return false, errors.New("inconsistent index")
			}
			break
		}
	}
	return deleted, err
}

func (tx *DBTX) Delete(schema *Schema, row Row) (deleted bool, err error) {
	tx = tx.NewTX()
	deleted, err = tx.delete(schema, row)
	return abortOrCommit(tx, deleted, err)
}

func (db *DB) Insert(schema *Schema, row Row) (updated bool, err error) {
	tx := db.NewTX()
	updated, err = tx.update(schema, row, ModeInsert)
	return abortOrCommit(tx, updated, err)
}

func (db *DB) Upsert(schema *Schema, row Row) (updated bool, err error) {
	tx := db.NewTX()
	updated, err = tx.update(schema, row, ModeUpsert)
	return abortOrCommit(tx, updated, err)
}

func (db *DB) Update(schema *Schema, row Row) (updated bool, err error) {
	tx := db.NewTX()
	updated, err = tx.update(schema, row, ModeUpdate)
	return abortOrCommit(tx, updated, err)
}

func (db *DB) Delete(schema *Schema, row Row) (deleted bool, err error) {
	tx := db.NewTX()
	deleted, err = tx.delete(schema, row)
	return abortOrCommit(tx, deleted, err)
}

type RowIterator struct {
	tx      *DBTX
	schema  *Schema
	indexNo int
	iter    *RangedKVIter
	valid   bool
	row     Row
}

func (iter *RowIterator) decodeKVIter() (bool, error) {
	if !iter.iter.Valid() {
		return false, nil
	}
	if err := iter.row.DecodeKey(iter.schema, iter.indexNo, iter.iter.Key()); err != nil {
		check(err != ErrOutOfRange)
		return false, err
	}
	if iter.indexNo > 0 {
		ok, err := iter.tx.Select(iter.schema, iter.row)
		if err != nil {
			return false, err
		} else if !ok {
			return false, errors.New("inconsistent index")
		}
	} else {
		if err := iter.row.DecodeVal(iter.schema, iter.iter.Val()); err != nil {
			return false, err
		}
	}
	return true, nil
}

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

func (tx *DBTX) Seek(schema *Schema, row Row) (*RowIterator, error) {
	start := make([]Cell, len(schema.Indices[0]))
	for i, idx := range schema.Indices[0] {
		check(row[idx].Type == schema.Cols[idx].Type)
		start[i] = row[idx]
	}
	return tx.Range(schema, &RangeReq{
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

func (tx *DBTX) Range(schema *Schema, req *RangeReq) (out *RowIterator, err error) {
	check(isDescending(req.StartCmp) != isDescending(req.StopCmp))
	start := EncodeKeyPrefix(schema, req.IndexNo, req.Start, suffixPositive(req.StartCmp))
	stop := EncodeKeyPrefix(schema, req.IndexNo, req.Stop, suffixPositive(req.StopCmp))
	desc := isDescending(req.StartCmp)
	out = &RowIterator{tx: tx, schema: schema, indexNo: req.IndexNo, row: schema.NewRow()}
	if out.iter, err = tx.kv.Range(start, stop, desc); err != nil {
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

func (tx *DBTX) execStmt(stmt interface{}) (r SQLResult, err error) {
	switch ptr := stmt.(type) {
	case *StmtCreatTable:
		err = tx.execCreateTable(ptr)
	case *StmtSelect:
		r.Header = exprs2header(ptr.cols)
		r.Values, err = tx.execSelect(ptr)
	case *StmtInsert:
		r.Updated, err = tx.execInsert(ptr)
	case *StmtUpdate:
		r.Updated, err = tx.execUpdate(ptr)
	case *StmtDelete:
		r.Updated, err = tx.execDelete(ptr)
	default:
		panic("unreachable")
	}
	return
}

func (tx *DBTX) ExecStmt(stmt interface{}) (r SQLResult, err error) {
	tx = tx.NewTX()
	r, err = tx.execStmt(stmt)
	if _, err = abortOrCommit(tx, true, err); err != nil {
		return SQLResult{}, err
	}
	return r, nil
}

func (db *DB) ExecStmt(stmt interface{}) (r SQLResult, err error) {
	tx := db.NewTX()
	r, err = tx.execStmt(stmt)
	if _, err = abortOrCommit(tx, true, err); err != nil {
		return SQLResult{}, err
	}
	return r, nil
}

func (tx *DBTX) execCreateTable(stmt *StmtCreatTable) (err error) {
	if _, err := tx.GetSchema(stmt.table); err == nil {
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
	if _, err = tx.kv.Set([]byte("@schema_"+stmt.table), val); err != nil {
		return err
	}

	tx.tables[schema.Table] = schema
	return nil
}

func (tx *DBTX) GetSchema(table string) (Schema, error) {
	schema, ok := tx.tables[table]
	if !ok {
		val, ok, err := tx.kv.Get([]byte("@schema_" + table))
		if err == nil && ok {
			err = json.Unmarshal(val, &schema)
		}
		if err != nil {
			return Schema{}, err
		}
		if !ok {
			return Schema{}, errors.New("table is not found")
		}
		tx.tables[table] = schema
	}
	return schema, nil
}

func (db *DB) GetSchema(table string) (Schema, error) {
	tx := db.NewTX()
	defer tx.Abort()
	return tx.GetSchema(table)
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

func (tx *DBTX) execCond(schema *Schema, cond interface{}) (*RowIterator, error) {
	req, err := makeRange(schema, cond)
	if err != nil {
		return nil, err
	}
	return tx.Range(schema, req)
}

func (tx *DBTX) execSelect(stmt *StmtSelect) (output []Row, err error) {
	schema, err := tx.GetSchema(stmt.table)
	if err != nil {
		return nil, err
	}

	iter, err := tx.execCond(&schema, stmt.cond)
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

func (tx *DBTX) execInsert(stmt *StmtInsert) (count int, err error) {
	schema, err := tx.GetSchema(stmt.table)
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

	updated, err := tx.Insert(&schema, stmt.value)
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

func (tx *DBTX) execUpdate(stmt *StmtUpdate) (count int, err error) {
	schema, err := tx.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}

	iter, err := tx.execCond(&schema, stmt.cond)
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
		updated, err := tx.Update(&schema, row)
		if err != nil {
			return 0, err
		}
		if updated {
			count++
		}
	}

	return count, nil
}

func (tx *DBTX) execDelete(stmt *StmtDelete) (count int, err error) {
	schema, err := tx.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}

	iter, err := tx.execCond(&schema, stmt.cond)
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
		updated, err := tx.Delete(&schema, row)
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
