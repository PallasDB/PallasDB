package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func executeCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd := newRootCommand()
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(args)

	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestVersionCommand(t *testing.T) {
	stdout, stderr, err := executeCommand(t, "version")

	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Contains(t, stdout, "pallasdb")
	require.Contains(t, stdout, "commit:")
}
