# Contributing to PallasDB

Thanks for taking the time. This document is the short version of everything CI
will check, so that a green local run means a green pull request.

## Prerequisites

- Go 1.25 or later.
- [`buf`](https://buf.build/docs/installation) — only if you touch `proto/`.
- The rest of the tooling installs itself:

```sh
make tools
```

`make tools` installs `golangci-lint`, `gosec`, `protoc-gen-go`, and
`protoc-gen-go-grpc` at the versions CI pins, into `$(go env GOPATH)/bin`. Make
sure that directory is on your `PATH`.

## The one command you need

```sh
make          # list every target
make check    # lint + race tests: what CI gates on
```

| Target | What it does |
|---|---|
| `make build` | Build `bin/pallasdb` with version/commit/date stamped in |
| `make test` | Run the suite |
| `make test-race` | Run the suite under `-race` with a coverage profile |
| `make lint` | `go vet` plus the blocking `.golangci.yml` set |
| `make lint-strict` | The advisory linter set (see below) |
| `make security` | `gosec` with the reviewed suppressions |
| `make proto` | `buf lint` and regenerate `pb/` |
| `make fuzz` | Fuzz every `Fuzz*` target in `db/` |
| `make docker` | Build the container image |
| `make clean` | Remove build, coverage, and release artifacts |

## What CI enforces

Blocking on every pull request:

- **Tests** (`.github/workflows/test.yml`) on `ubuntu-latest`, `macos-latest`,
  and `windows-latest`, against Go 1.25 and `stable`. The matrix is not
  decoration: `db/os_unix.go` and `db/os_other.go` are selected by build tag, so
  a Linux-only run never compiles the non-unix path. Race detection runs on
  Linux and macOS; the Windows leg runs without `-race`, because its job is
  build-tag coverage and a C toolchain is not guaranteed there.
- **Coverage** (`codecov.yml`) — the project status is a ratchet: `auto` with a
  1% threshold, so a pull request fails if it makes overall coverage worse. The
  patch status is a real number, 80% on the diff: new code must be tested. The
  upload itself fails the build if it errors.
- **Lint** (`.github/workflows/lint.yml`) — `go vet` and `.golangci.yml`.
- **Security** (`.github/workflows/security.yml`) — `govulncheck`, CodeQL, and
  `gosec`. `gosec` is blocking.
- **Proto** (`.github/workflows/proto.yml`) — `buf lint`, `buf breaking`
  against `main`, and a `buf generate` drift check.

Not blocking, by design:

- **Lint (strict, advisory)** — see below.
- **Fuzz** (`.github/workflows/fuzz.yml`) — nightly and manual only. Fuzzing is
  unbounded, nondeterministic work and must never gate a pull request.

## Linting

There are two linter configurations, on purpose.

`.golangci.yml` is the blocking set. It must stay green.

`.github/golangci-strict.yml` is the set we are migrating toward: `errorlint`,
`exhaustive`, `gocritic`, `gosec`, and `nilerr` on top of the blocking ones. It
runs in the advisory `lint-strict` job so its existing findings do not block
unrelated work. The rules:

- **Do not add exclusions to the strict config to make it green.** Fix the code.
- New code should not add strict findings. If `make lint-strict` reports
  something in a file you touched, fix it while you are there.
- When `make lint-strict` reports zero issues, fold its `linters.enable` and
  `linters.settings` blocks into `.golangci.yml`, delete
  `.github/golangci-strict.yml`, and delete the `lint-strict` job. That is the
  finish line, and it should be crossed in one mechanical commit.

## Security findings

`gosec` runs without `-no-fail`, so anything it reports fails the build.

Findings that are accepted rather than fixed live in `.github/gosec.json`, and
every one carries a written rationale in that file. If you need a new
suppression, add the rationale in the same commit; a bare rule ID with no
explanation will be sent back. Re-audit the raw, unsuppressed set at any time
with `gosec ./...`.

## Protobuf changes

`pb/` is generated. Never edit it by hand.

```sh
# edit proto/pallasdb/v1/*.proto
make proto            # buf lint + buf generate
make proto-breaking   # confirm you have not broken v1
git add proto pb
```

CI regenerates and diffs, so a proto change without its regenerated Go code
fails. `buf breaking` compares against `main`: `v1` is a published surface, and
breaking it needs a `v2` package, not a patch.

## Fuzzing

Fuzz targets live next to the code they exercise, currently in `db/`. The
nightly workflow discovers them with `go test -list '^Fuzz'`, so a new
`FuzzSomething` is picked up with no workflow change.

```sh
make fuzz                # 60s per target
make fuzz FUZZTIME=10m   # longer local run
```

Commit any crasher Go writes into `testdata/fuzz/` — it becomes a regression
test.

## Commits and changelog

Commit messages follow [Conventional
Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `perf:`,
`docs:`, `test:`, `chore:`, `ci:`). Release notes are generated from them.

User-visible changes also get a line in the `## [Unreleased]` section of
[`CHANGELOG.md`](CHANGELOG.md), under `Added`, `Changed`, `Fixed`, `Security`,
or `Removed`. Describe the effect, not the patch.

## Releasing

Maintainers only:

```sh
make snapshot   # dry run: build dist/ locally, publish nothing
git tag -a vX.Y.Z -m 'vX.Y.Z' && git push origin vX.Y.Z
```

The tag triggers `.github/workflows/release.yml`, which runs goreleaser for
darwin/linux amd64+arm64 archives and pushes a multi-arch image to
`ghcr.io/teddymalhan/pallasdb`.

## Where things live

Start with [`docs/`](docs/) for the in-repo documentation, and with the README in
each package directory for the code: [`db/`](db/README.md),
[`grpc/`](grpc/README.md), [`cluster/`](cluster/README.md),
[`cmd/pallasdb/`](cmd/pallasdb/README.md), [`proto/`](proto/README.md).
