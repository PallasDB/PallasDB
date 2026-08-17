package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func executeCommand(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	return executeCommandWithInput(t, "", args...)
}

func executeCommandWithInput(t *testing.T, input string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	cmd := newRootCommand()
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)

	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestVersionCommand(t *testing.T) {
	stdout, stderr, err := executeCommand(t, "version")

	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Contains(t, stdout, "pallasdb")
	require.Contains(t, stdout, "commit:")
	require.Contains(t, stdout, "built:")
	require.Contains(t, stdout, runtime.Version())
}

func TestVersionShort(t *testing.T) {
	stdout, _, err := executeCommand(t, "version", "--short")

	require.NoError(t, err)
	require.Equal(t, resolveBuildDetails().Version+"\n", stdout)
	require.NotContains(t, stdout, "commit:")
}

func TestVersionPrefersLinkTimeStamps(t *testing.T) {
	// Restore the zero values a plain `go build` leaves behind.
	defer func(v, c, d string) { version, commit, date = v, c, d }(version, commit, date)

	version, commit, date = "v1.2.3", "abc123", "2026-01-02T03:04:05Z"
	details := resolveBuildDetails()
	require.Equal(t, "v1.2.3", details.Version)
	require.Equal(t, "abc123", details.Commit)
	require.Equal(t, "2026-01-02T03:04:05Z", details.Date)
}

func TestVersionFallsBackWhenUnstamped(t *testing.T) {
	defer func(v, c, d string) { version, commit, date = v, c, d }(version, commit, date)

	version, commit, date = "", "", ""
	details := resolveBuildDetails()
	// Never the empty string: either a real build-info value or a placeholder.
	require.NotEmpty(t, details.Version)
	require.NotEmpty(t, details.Commit)
	require.NotEmpty(t, details.Date)
	require.Equal(t, runtime.Version(), details.GoVersion)
	require.Equal(t, runtime.GOOS+"/"+runtime.GOARCH, details.Platform)
}
