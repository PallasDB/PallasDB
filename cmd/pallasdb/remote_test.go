package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKVCommandsAgainstRunningServer(t *testing.T) {
	server := startFakeServer(t)

	stdout, stderr, err := executeCommand(t, "kv", "put", "alpha", "bravo", "--addr", server.addr)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, "updated\n", stdout)

	stdout, _, err = executeCommand(t, "kv", "get", "alpha", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, "bravo\n", stdout)

	stdout, _, err = executeCommand(t, "kv", "delete", "alpha", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, "deleted\n", stdout)

	stdout, _, err = executeCommand(t, "kv", "get", "alpha", "--addr", server.addr)
	require.Error(t, err)
	require.Empty(t, stdout)
	require.ErrorContains(t, err, "key not found")
}

func TestKVPutInsertModeReportsUnchanged(t *testing.T) {
	server := startFakeServer(t)
	server.seed(map[string]string{"alpha": "existing"})

	stdout, _, err := executeCommand(t, "kv", "put", "alpha", "new", "--mode", "insert", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, "unchanged\n", stdout)
	require.Equal(t, "existing", server.snapshot()["alpha"])
}

func TestKVPutRejectsUnknownMode(t *testing.T) {
	server := startFakeServer(t)

	_, _, err := executeCommand(t, "kv", "put", "a", "b", "--mode", "clobber", "--addr", server.addr)
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid write mode")
}

func TestKVRangeStreamsAndHonoursFlags(t *testing.T) {
	server := startFakeServer(t)
	server.seed(map[string]string{"a": "1", "b": "2", "c": "3"})

	stdout, _, err := executeCommand(t, "kv", "range", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, "a\t1\nb\t2\nc\t3\n", stdout)

	stdout, _, err = executeCommand(t, "kv", "range", "a", "b", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, "a\t1\nb\t2\n", stdout)

	stdout, _, err = executeCommand(t, "kv", "range", "--limit", "2", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, "a\t1\nb\t2\n", stdout)

	stdout, _, err = executeCommand(t, "kv", "range", "--keys-only", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, "a\nb\nc\n", stdout)

	// Descending needs an explicit start: the server has no seek-to-last.
	stdout, _, err = executeCommand(t, "kv", "range", "c", "", "--descending", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, "c\t3\nb\t2\na\t1\n", stdout)

	_, _, err = executeCommand(t, "kv", "range", "--descending", "--addr", server.addr)
	require.Error(t, err)
	require.ErrorContains(t, err, "descending range requires a start key")
}

func TestKVRejectsUnknownConsistency(t *testing.T) {
	server := startFakeServer(t)

	_, _, err := executeCommand(t, "kv", "get", "a", "--consistency", "eventual", "--addr", server.addr)
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid consistency")
}

func TestRemoteAddrComesFromConfigFile(t *testing.T) {
	server := startFakeServer(t)
	server.seed(map[string]string{"alpha": "bravo"})
	configFile := writeConfigFile(t, "client:\n  addr: "+quoteYAML(server.addr)+"\n")

	stdout, _, err := executeCommand(t, "--config", configFile, "kv", "get", "alpha")
	require.NoError(t, err)
	require.Equal(t, "bravo\n", stdout)
}

func TestRemoteAddrFlagBeatsConfigFile(t *testing.T) {
	server := startFakeServer(t)
	server.seed(map[string]string{"alpha": "bravo"})
	// The config file points somewhere useless; the flag must win.
	configFile := writeConfigFile(t, "client:\n  addr: \"127.0.0.1:1\"\n")

	stdout, _, err := executeCommand(t, "--config", configFile, "kv", "get", "alpha", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, "bravo\n", stdout)
}

func TestSQLOneShotRendersTable(t *testing.T) {
	server := startFakeServer(t)

	stdout, stderr, err := executeCommand(t, "sql", "SELECT id, name FROM t", "--addr", server.addr)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, "+----+-------+\n"+
		"| id | name  |\n"+
		"+----+-------+\n"+
		"|  1 | alpha |\n"+
		"|  2 | bravo |\n"+
		"+----+-------+\n"+
		"(2 rows)\n", stdout)

	// The CLI supplies the terminating semicolon the grammar expects.
	require.Equal(t, "SELECT id, name FROM t;", server.lastStatement())
}

func TestSQLOneShotRendersJSON(t *testing.T) {
	server := startFakeServer(t)

	stdout, _, err := executeCommand(t, "sql", "SELECT *;", "--format", "json", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, `{"columns":[{"name":"id","type":"int64"},{"name":"name","type":"string"}],`+
		`"rows":[[1,"alpha"],[2,"bravo"]],"rows_affected":0}`+"\n", stdout)
}

func TestSQLOneShotReportsRowsAffected(t *testing.T) {
	server := startFakeServer(t)

	stdout, _, err := executeCommand(t, "sql", "INSERT INTO t (id) VALUES (1)", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, "OK, 2 rows affected\n", stdout)
}

func TestSQLRejectsUnknownFormatAndEmptyStatement(t *testing.T) {
	server := startFakeServer(t)

	_, _, err := executeCommand(t, "sql", "SELECT 1;", "--format", "csv", "--addr", server.addr)
	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported output format")

	_, _, err = executeCommand(t, "sql", "   ", "--addr", server.addr)
	require.Error(t, err)
	require.ErrorContains(t, err, "statement must not be empty")
}

func TestSQLOneShotSurfacesServerErrors(t *testing.T) {
	server := startFakeServer(t)

	_, _, err := executeCommand(t, "sql", "BOOM", "--addr", server.addr)
	require.Error(t, err)
	require.ErrorContains(t, err, "syntax error near BOOM")
}

func TestSQLREPLExecutesStatementsAndQuits(t *testing.T) {
	server := startFakeServer(t)

	input := "SELECT id, name\n  FROM t;\n\\q\nSELECT 'never runs';\n"
	stdout, stderr, err := executeCommandWithInput(t, input, "sql", "--addr", server.addr)
	require.NoError(t, err)
	require.Empty(t, stderr)
	// Non-interactive input prints no banner and no prompts.
	require.Equal(t, "+----+-------+\n"+
		"| id | name  |\n"+
		"+----+-------+\n"+
		"|  1 | alpha |\n"+
		"|  2 | bravo |\n"+
		"+----+-------+\n"+
		"(2 rows)\n", stdout)
	require.Equal(t, "SELECT id, name\n  FROM t;", server.lastStatement(), "multi-line statements are joined verbatim")
}

func TestSQLREPLEndsAtEndOfInput(t *testing.T) {
	server := startFakeServer(t)

	stdout, _, err := executeCommandWithInput(t, "UPDATE t SET a = 1;\n", "sql", "--addr", server.addr)
	require.NoError(t, err)
	require.Equal(t, "OK, 2 rows affected\n", stdout)
}

func TestSQLREPLReportsErrorsAndKeepsGoing(t *testing.T) {
	server := startFakeServer(t)

	input := "BOOM;\nSELECT 1;\n\\q\n"
	stdout, stderr, err := executeCommandWithInput(t, input, "sql", "--addr", server.addr)
	require.NoError(t, err)
	require.Contains(t, stderr, "syntax error near BOOM")
	require.Contains(t, stdout, "| alpha |")
}

func TestSQLREPLIgnoresBlankLinesAndPrintsHelp(t *testing.T) {
	server := startFakeServer(t)

	stdout, _, err := executeCommandWithInput(t, "\n\n\\?\n\\q\n", "sql", "--addr", server.addr)
	require.NoError(t, err)
	require.Contains(t, stdout, "\\q  quit")
	require.Empty(t, server.lastStatement())
}

func TestDumpRestoreAgainstRunningServer(t *testing.T) {
	source := startFakeServer(t)
	target := startFakeServer(t)
	source.seed(map[string]string{"a": "1", "b": "2", "binary\x00k": "binary\x00v"})
	backup := filepath.Join(t.TempDir(), "backup.pallas")

	_, stderr, err := executeCommand(t, "dump", "--addr", source.addr, "--output", backup)
	require.NoError(t, err)
	require.Contains(t, stderr, "dumped 3 keys")

	stdout, _, err := executeCommand(t, "restore", "--addr", target.addr, "--input", backup)
	require.NoError(t, err)
	require.Equal(t, "restored 3 keys\n", stdout)

	require.Equal(t, source.snapshot(), target.snapshot())
}

// The server caps a single scan and truncates it without saying so, so a dump
// that issues one Range call would quietly lose everything past the cap.
func TestDumpResumesPastTheServerRangeCap(t *testing.T) {
	source := startFakeServer(t)
	target := startFakeServer(t)
	source.rangeCap = 3

	want := make(map[string]string, 10)
	for i := range 10 {
		want["key-"+pad6(i)] = "value-" + pad6(i)
	}
	source.seed(want)
	backup := filepath.Join(t.TempDir(), "backup.pallas")

	_, stderr, err := executeCommand(t, "dump", "--addr", source.addr, "--output", backup)
	require.NoError(t, err)
	require.Contains(t, stderr, "dumped 10 keys")

	stdout, _, err := executeCommand(t, "restore", "--addr", target.addr, "--input", backup)
	require.NoError(t, err)
	require.Equal(t, "restored 10 keys\n", stdout)
	require.Equal(t, want, target.snapshot())
}

func TestDumpFromServerRestoreToLocalDirectory(t *testing.T) {
	source := startFakeServer(t)
	source.seed(map[string]string{"a": "1", "b": "2"})
	dir := t.TempDir()
	backup := filepath.Join(dir, "backup.pallas")
	target := filepath.Join(dir, "db")

	_, _, err := executeCommand(t, "dump", "--addr", source.addr, "--output", backup)
	require.NoError(t, err)
	_, _, err = executeCommand(t, "restore", "--data-dir", target, "--input", backup)
	require.NoError(t, err)

	require.Equal(t, map[string]string{"a": "1", "b": "2"}, readLocalStore(t, target))
}
