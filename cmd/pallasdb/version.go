package main

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version, commit, and date are stamped at link time by the release build:
//
//	go build -ldflags "-X main.version=v1.2.3 -X main.commit=$(git rev-parse HEAD) -X main.date=$(date -u +%FT%TZ)"
//
// The Go linker names the main package "main" regardless of its import path, so
// the -X symbol prefix is "main", not the full package path. When the stamps
// are absent — a plain `go build` or `go install` — the values are recovered
// from the module build info instead.
var (
	version string
	commit  string
	date    string
)

const unknownBuildValue = "unknown"

// buildDetails is the resolved identity of this binary.
type buildDetails struct {
	Version   string
	Commit    string
	Date      string
	GoVersion string
	Platform  string
}

// resolveBuildDetails prefers link-time stamps and falls back to the VCS stamps
// the toolchain embeds in the build info. A nil info means the toolchain
// embedded none, which is the case for `go run` and for builds made with
// -buildvcs=false or from a git worktree.
func resolveBuildDetails(info *debug.BuildInfo) buildDetails {
	details := buildDetails{
		Version:   version,
		Commit:    commit,
		Date:      date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}

	if info != nil {
		if details.Version == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			details.Version = info.Main.Version
		}
		dirty := false
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if details.Commit == "" {
					details.Commit = setting.Value
				}
			case "vcs.time":
				if details.Date == "" {
					details.Date = setting.Value
				}
			case "vcs.modified":
				dirty = setting.Value == "true"
			}
		}
		// Only mark a recovered commit dirty: a stamped one names a release.
		if dirty && commit == "" && details.Commit != "" {
			details.Commit += "-dirty"
		}
	}

	if details.Version == "" {
		details.Version = "dev"
	}
	if details.Commit == "" {
		details.Commit = unknownBuildValue
	}
	if details.Date == "" {
		details.Date = unknownBuildValue
	}
	return details
}

// currentBuildDetails resolves the identity of the running binary.
func currentBuildDetails() buildDetails {
	if info, ok := debug.ReadBuildInfo(); ok {
		return resolveBuildDetails(info)
	}
	return resolveBuildDetails(nil)
}

func newVersionCommand() *cobra.Command {
	var short bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			details := currentBuildDetails()
			if short {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), details.Version)
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"pallasdb %s\ncommit: %s\nbuilt: %s\ngo: %s %s\n",
				details.Version, details.Commit, details.Date, details.GoVersion, details.Platform,
			)
			return err
		},
	}
	cmd.Flags().BoolVar(&short, "short", false, "print only the version string")
	return cmd
}
