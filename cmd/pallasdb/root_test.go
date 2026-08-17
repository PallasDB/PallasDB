package main

import (
	"bytes"
	"runtime"
	"runtime/debug"
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
	require.Equal(t, currentBuildDetails().Version+"\n", stdout)
	require.NotContains(t, stdout, "commit:")
}

// stampsRestored resets the link-time variables the release build sets, so a
// test may pretend it was or was not stamped.
func stampsRestored(t *testing.T) {
	t.Helper()
	t.Cleanup(func(v, c, d string) func() {
		return func() { version, commit, date = v, c, d }
	}(version, commit, date))
}

func TestVersionPrefersLinkTimeStamps(t *testing.T) {
	stampsRestored(t)
	version, commit, date = "v1.2.3", "abc123", "2026-01-02T03:04:05Z"

	// Build info is present and disagrees; the stamps still win.
	details := resolveBuildDetails(&debug.BuildInfo{
		Main: debug.Module{Version: "v9.9.9"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "deadbeef"},
			{Key: "vcs.time", Value: "1999-01-01T00:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	})
	require.Equal(t, "v1.2.3", details.Version)
	require.Equal(t, "abc123", details.Commit, "a stamped commit is never marked dirty")
	require.Equal(t, "2026-01-02T03:04:05Z", details.Date)
}

func TestVersionRecoveredFromBuildInfo(t *testing.T) {
	stampsRestored(t)
	version, commit, date = "", "", ""

	details := resolveBuildDetails(&debug.BuildInfo{
		Main: debug.Module{Version: "v0.4.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "deadbeef"},
			{Key: "vcs.time", Value: "2026-01-02T03:04:05Z"},
			{Key: "vcs.modified", Value: "false"},
		},
	})
	require.Equal(t, "v0.4.0", details.Version)
	require.Equal(t, "deadbeef", details.Commit)
	require.Equal(t, "2026-01-02T03:04:05Z", details.Date)
}

func TestVersionMarksARecoveredDirtyBuild(t *testing.T) {
	stampsRestored(t)
	version, commit, date = "", "", ""

	details := resolveBuildDetails(&debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "deadbeef"},
			{Key: "vcs.modified", Value: "true"},
		},
	})
	require.Equal(t, "dev", details.Version, "(devel) is not a version a user can act on")
	require.Equal(t, "deadbeef-dirty", details.Commit)
	require.Equal(t, "unknown", details.Date)
}

// `go run`, -buildvcs=false and builds from a git worktree embed nothing.
func TestVersionFallsBackWithoutBuildInfo(t *testing.T) {
	stampsRestored(t)
	version, commit, date = "", "", ""

	details := resolveBuildDetails(nil)
	require.Equal(t, "dev", details.Version)
	require.Equal(t, "unknown", details.Commit)
	require.Equal(t, "unknown", details.Date)
	require.Equal(t, runtime.Version(), details.GoVersion)
	require.Equal(t, runtime.GOOS+"/"+runtime.GOARCH, details.Platform)
}
