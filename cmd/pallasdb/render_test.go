package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/teddymalhan/pallasdb/client"
)

func selectResult() *client.Result {
	return &client.Result{
		Columns: []client.Column{
			{Name: "id", Type: client.ValueTypeInt64},
			{Name: "name", Type: client.ValueTypeString},
		},
		Rows: [][]client.Value{
			{{Type: client.ValueTypeInt64, Int: 1}, {Type: client.ValueTypeString, Str: "alpha"}},
			{{Type: client.ValueTypeInt64, Int: 2}, {Type: client.ValueTypeString, Str: "bravo"}},
		},
	}
}

func TestRenderTableGolden(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, renderResult(&out, formatTable, selectResult()))

	const want = "+----+-------+\n" +
		"| id | name  |\n" +
		"+----+-------+\n" +
		"|  1 | alpha |\n" +
		"|  2 | bravo |\n" +
		"+----+-------+\n" +
		"(2 rows)\n"
	require.Equal(t, want, out.String())
}

func TestRenderTableWidensToTheLongestCell(t *testing.T) {
	result := &client.Result{
		Columns: []client.Column{{Name: "n", Type: client.ValueTypeString}},
		Rows: [][]client.Value{
			{{Type: client.ValueTypeString, Str: "a-very-long-value"}},
			{{Type: client.ValueTypeString, Str: "x"}},
		},
	}

	var out bytes.Buffer
	require.NoError(t, renderResult(&out, formatTable, result))

	const want = "+-------------------+\n" +
		"| n                 |\n" +
		"+-------------------+\n" +
		"| a-very-long-value |\n" +
		"| x                 |\n" +
		"+-------------------+\n" +
		"(2 rows)\n"
	require.Equal(t, want, out.String())
}

func TestRenderTableEmptyAndNullCells(t *testing.T) {
	result := &client.Result{
		Columns: []client.Column{{Name: "id", Type: client.ValueTypeInt64}},
	}

	var out bytes.Buffer
	require.NoError(t, renderResult(&out, formatTable, result))
	require.Equal(t, "+----+\n| id |\n+----+\n+----+\n(0 rows)\n", out.String())

	result.Rows = [][]client.Value{{{}}}
	out.Reset()
	require.NoError(t, renderResult(&out, formatTable, result))
	require.Equal(t, "+------+\n| id   |\n+------+\n| NULL |\n+------+\n(1 row)\n", out.String())
}

func TestRenderTableReportsRowsAffected(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, renderResult(&out, formatTable, &client.Result{RowsAffected: 3}))
	require.Equal(t, "OK, 3 rows affected\n", out.String())

	out.Reset()
	require.NoError(t, renderResult(&out, formatTable, &client.Result{RowsAffected: 1}))
	require.Equal(t, "OK, 1 row affected\n", out.String())
}

func TestRenderJSONGolden(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, renderResult(&out, formatJSON, selectResult()))

	const want = `{"columns":[{"name":"id","type":"int64"},{"name":"name","type":"string"}],` +
		`"rows":[[1,"alpha"],[2,"bravo"]],"rows_affected":0}` + "\n"
	require.Equal(t, want, out.String())
}

func TestRenderJSONEmptyResultUsesEmptyArrays(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, renderResult(&out, formatJSON, &client.Result{RowsAffected: 2}))
	require.Equal(t, `{"columns":[],"rows":[],"rows_affected":2}`+"\n", out.String())
}

func TestRenderRejectsUnknownFormat(t *testing.T) {
	require.Error(t, validateOutputFormat("yaml"))
	require.NoError(t, validateOutputFormat(formatTable))
	require.NoError(t, validateOutputFormat(formatJSON))
	require.Error(t, renderResult(&bytes.Buffer{}, "yaml", selectResult()))
}

func TestNormalizeStatementAddsExactlyOneSemicolon(t *testing.T) {
	require.Equal(t, "SELECT 1;", normalizeStatement("SELECT 1"))
	require.Equal(t, "SELECT 1;", normalizeStatement("  SELECT 1;  "))
	require.Equal(t, "", normalizeStatement("   "))
}
