package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teddymalhan/pallasdb/client"
)

const (
	replPrompt         = "pallasdb> "
	replContinuePrompt = "      ..> "
)

// newSQLCommand runs SQL against a running server, either as a one-shot
// statement or as an interactive REPL.
func newSQLCommand(config *configOptions) *cobra.Command {
	var (
		format string
		remote *remoteOptions
	)

	cmd := &cobra.Command{
		Use:   "sql [STATEMENT]",
		Short: "Run SQL against a running PallasDB server",
		Long: "Run SQL against a running PallasDB server.\n\n" +
			"With a STATEMENT argument the statement is executed once and the result " +
			"printed. With no argument an interactive session is started: statements " +
			"are terminated by a semicolon, \\q quits, and Ctrl-C or Ctrl-D ends the " +
			"session.",
		Example: "  pallasdb sql --addr 127.0.0.1:50051 \"SELECT id, name FROM users;\"\n" +
			"  pallasdb sql --addr 127.0.0.1:50051 --format json \"SELECT * FROM users;\"\n" +
			"  pallasdb sql --addr 127.0.0.1:50051",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(format); err != nil {
				return err
			}
			consistency, err := remote.consistencyLevel()
			if err != nil {
				return err
			}
			if len(args) == 1 && strings.TrimSpace(args[0]) == "" {
				return errors.New("statement must not be empty")
			}

			session := &sqlSession{format: format, consistency: consistency, remote: remote}
			if len(args) == 1 {
				// A one-shot statement is bounded by --timeout.
				return remote.withClient(cmd, func(ctx context.Context, c *client.Client) error {
					return session.execute(ctx, c, cmd.OutOrStdout(), args[0])
				})
			}
			// An interactive session lives until Ctrl-C or end of input;
			// --timeout bounds each statement instead.
			return remote.withSessionClient(cmd, func(ctx context.Context, c *client.Client) error {
				return session.repl(ctx, c, cmd)
			})
		},
	}

	remote = addRemoteFlags(cmd, config)
	cmd.Flags().StringVar(&format, "format", formatTable, "output format: table or json")
	return cmd
}

// sqlSession executes statements and renders their results.
type sqlSession struct {
	format      string
	consistency client.Consistency
	remote      *remoteOptions
}

func (s *sqlSession) execute(ctx context.Context, c *client.Client, out io.Writer, statement string) error {
	result, err := c.Query(ctx, normalizeStatement(statement), s.consistency)
	if err != nil {
		return err
	}
	return renderResult(out, s.format, result)
}

// repl runs the interactive session. Statements accumulate across lines until a
// semicolon terminates them; \q quits. The session ends cleanly on Ctrl-C
// (ctx cancellation), at end of input, and on \q. A failing statement is
// reported and the session continues.
func (s *sqlSession) repl(ctx context.Context, c *client.Client, cmd *cobra.Command) error {
	in := cmd.InOrStdin()
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	interactive := isTerminal(in)

	if interactive {
		_, err := fmt.Fprintf(out,
			"PallasDB SQL — connected to %s\nTerminate statements with ';'. Type \\q to quit.\n",
			s.remote.addr,
		)
		if err != nil {
			return err
		}
	}

	reader := newLineReader(in)
	var buffer strings.Builder

	for {
		if interactive {
			prompt := replPrompt
			if buffer.Len() > 0 {
				prompt = replContinuePrompt
			}
			if _, err := io.WriteString(out, prompt); err != nil {
				return err
			}
		}

		line, err := reader.next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				if interactive {
					_, _ = io.WriteString(out, "\n")
				}
				return nil
			}
			return err
		}

		trimmed := strings.TrimSpace(line)
		if buffer.Len() == 0 {
			switch trimmed {
			case "":
				continue
			case "\\q", "\\quit":
				return nil
			case "\\?", "\\h":
				if err := writeREPLHelp(out); err != nil {
					return err
				}
				continue
			}
		}

		if buffer.Len() > 0 {
			buffer.WriteByte('\n')
		}
		buffer.WriteString(line)
		if !strings.HasSuffix(trimmed, ";") {
			continue
		}

		statement := buffer.String()
		buffer.Reset()

		stmtCtx, cancel := s.remote.callContext(ctx)
		err = s.execute(stmtCtx, c, out, statement)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if _, writeErr := fmt.Fprintf(errOut, "error: %v\n", err); writeErr != nil {
				return writeErr
			}
		}
	}
}

func writeREPLHelp(out io.Writer) error {
	_, err := io.WriteString(out,
		"\\q  quit\n"+
			"\\?  this help\n"+
			"Statements end with a semicolon and may span multiple lines.\n",
	)
	return err
}

// normalizeStatement trims surrounding whitespace and guarantees the single
// trailing semicolon the SQL grammar expects, so `pallasdb sql "SELECT 1"` and
// `pallasdb sql "SELECT 1;"` behave identically.
func normalizeStatement(statement string) string {
	trimmed := strings.TrimSpace(statement)
	if trimmed == "" || strings.HasSuffix(trimmed, ";") {
		return trimmed
	}
	return trimmed + ";"
}

// isTerminal reports whether r is an interactive terminal. The banner and
// prompts are suppressed for piped input so scripted sessions produce clean,
// diffable output.
func isTerminal(r io.Reader) bool {
	file, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// lineReader turns a blocking io.Reader into a cancellable line source so that
// Ctrl-C ends the session even while the REPL waits for input. Line editing is
// whatever the terminal's canonical mode provides: no history is kept and no
// line-editing library is linked in.
type lineReader struct {
	lines <-chan lineResult
}

type lineResult struct {
	text string
	err  error
}

func newLineReader(r io.Reader) *lineReader {
	lines := make(chan lineResult)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), maxREPLLineBytes)
		for scanner.Scan() {
			lines <- lineResult{text: scanner.Text()}
		}
		if err := scanner.Err(); err != nil {
			lines <- lineResult{err: err}
		}
	}()
	return &lineReader{lines: lines}
}

const maxREPLLineBytes = 8 << 20

// next returns the next line, ctx.Err() if the session was cancelled, or io.EOF
// at end of input.
func (lr *lineReader) next(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result, ok := <-lr.lines:
		if !ok {
			return "", io.EOF
		}
		return result.text, result.err
	}
}
