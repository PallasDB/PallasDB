package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/teddymalhan/pallasdb/client"
)

// Output formats accepted by `pallasdb sql --format`.
const (
	formatTable = "table"
	formatJSON  = "json"
)

func validateOutputFormat(format string) error {
	switch format {
	case formatTable, formatJSON:
		return nil
	default:
		return fmt.Errorf("unsupported output format %q (want table or json)", format)
	}
}

// renderResult writes a query result in the requested format.
func renderResult(w io.Writer, format string, result *client.Result) error {
	switch format {
	case formatJSON:
		return renderJSON(w, result)
	case formatTable:
		return renderTable(w, result)
	default:
		return fmt.Errorf("unsupported output format %q (want table or json)", format)
	}
}

// renderTable draws an aligned, box-drawn table:
//
//	+----+-------+
//	| id | name  |
//	+----+-------+
//	|  1 | alpha |
//	+----+-------+
//	(1 row)
//
// A statement that projects no columns is a non-SELECT, and reports the rows it
// affected instead.
func renderTable(w io.Writer, result *client.Result) error {
	if len(result.Columns) == 0 {
		_, err := fmt.Fprintf(w, "OK, %s affected\n", pluralRows(result.RowsAffected))
		return err
	}

	widths := make([]int, len(result.Columns))
	for i, col := range result.Columns {
		widths[i] = utf8.RuneCountInString(col.Name)
	}
	cells := make([][]string, len(result.Rows))
	for r, row := range result.Rows {
		cells[r] = make([]string, len(result.Columns))
		for c := range result.Columns {
			if c >= len(row) {
				continue
			}
			text := row[c].String()
			cells[r][c] = text
			if n := utf8.RuneCountInString(text); n > widths[c] {
				widths[c] = n
			}
		}
	}

	rule := tableRule(widths)
	if _, err := io.WriteString(w, rule); err != nil {
		return err
	}
	header := make([]string, len(result.Columns))
	for i, col := range result.Columns {
		header[i] = col.Name
	}
	if err := writeTableRow(w, header, widths, nil); err != nil {
		return err
	}
	if _, err := io.WriteString(w, rule); err != nil {
		return err
	}
	for _, row := range cells {
		if err := writeTableRow(w, row, widths, result.Columns); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, rule); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "(%s)\n", pluralRows(uint64(len(result.Rows))))
	return err
}

// tableRule builds the "+----+----+" separator for the given column widths.
func tableRule(widths []int) string {
	var b strings.Builder
	b.WriteByte('+')
	for _, width := range widths {
		b.WriteString(strings.Repeat("-", width+2))
		b.WriteByte('+')
	}
	b.WriteByte('\n')
	return b.String()
}

// writeTableRow writes one padded row. Numeric columns are right-aligned;
// columns is nil for the header, which is always left-aligned.
func writeTableRow(w io.Writer, cells []string, widths []int, columns []client.Column) error {
	var b strings.Builder
	b.WriteByte('|')
	for i, width := range widths {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		padding := width - utf8.RuneCountInString(cell)
		if padding < 0 {
			padding = 0
		}
		b.WriteByte(' ')
		if i < len(columns) && columns[i].Type == client.ValueTypeInt64 {
			b.WriteString(strings.Repeat(" ", padding))
			b.WriteString(cell)
		} else {
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", padding))
		}
		b.WriteString(" |")
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}

func pluralRows(n uint64) string {
	if n == 1 {
		return "1 row"
	}
	return fmt.Sprintf("%d rows", n)
}

// jsonColumn and jsonResult are the stable shape of `--format json`.
type jsonColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type jsonResult struct {
	Columns      []jsonColumn `json:"columns"`
	Rows         [][]any      `json:"rows"`
	RowsAffected uint64       `json:"rows_affected"`
}

func renderJSON(w io.Writer, result *client.Result) error {
	out := jsonResult{
		Columns:      make([]jsonColumn, 0, len(result.Columns)),
		Rows:         make([][]any, 0, len(result.Rows)),
		RowsAffected: result.RowsAffected,
	}
	for _, col := range result.Columns {
		out.Columns = append(out.Columns, jsonColumn{Name: col.Name, Type: col.Type.String()})
	}
	for _, row := range result.Rows {
		values := make([]any, len(row))
		for i, value := range row {
			values[i] = value.Any()
		}
		out.Rows = append(out.Rows, values)
	}

	return json.NewEncoder(w).Encode(out)
}
