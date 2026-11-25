// Command migrate applies or rolls back HookFlow database migrations explicitly.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"

	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
)

const (
	commandUp      = "up"
	commandDown    = "down"
	commandVersion = "version"
)

// Exit statuses follow the usual command line convention: 2 is a usage error,
// 1 is a reported operational failure (including a result the tool could not
// deliver to its caller).
const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	out := &reportWriter{w: stdout}
	errOut := &reportWriter{w: stderr}
	return finalizeExitCode(execute(args, out, errOut), out, errOut)
}

// reportWriter emits command output and remembers the first write failure.
//
// The migration outcome is the command's product: an unchecked stdout or stderr
// failure would otherwise let a successful exit code claim a result that no
// operator or CI script ever received.
type reportWriter struct {
	w   io.Writer
	err error
}

func (r *reportWriter) Write(p []byte) (int, error) {
	n, err := r.w.Write(p)
	if err != nil && r.err == nil {
		r.err = err
	}
	return n, err
}

func (r *reportWriter) printf(format string, args ...any) {
	if r.err != nil {
		return
	}
	if _, err := fmt.Fprintf(r.w, format, args...); err != nil && r.err == nil {
		r.err = err
	}
}

func (r *reportWriter) println(text string) {
	r.printf("%s\n", text)
}

// finalizeExitCode turns an undeliverable report into a failure status and still
// preserves the command's own usage or runtime exit code. When both streams are
// unusable, the exit status is the only signal left for the caller.
func finalizeExitCode(code int, out, errOut *reportWriter) int {
	switch {
	case out.err != nil && errOut.err != nil:
	case out.err != nil:
		errOut.printf("error: migration result could not be reported: %v\n", out.err)
	case errOut.err != nil:
		out.printf("warning: migration diagnostics could not be reported: %v\n", errOut.err)
	}
	if code == exitSuccess && (out.err != nil || errOut.err != nil) {
		return exitFailure
	}
	return code
}

func execute(args []string, out, errOut *reportWriter) (code int) {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(errOut)
	migrationsPath := fs.String("path", "", "migration directory (default: discover db/migrations)")
	fs.Usage = func() {
		errOut.println("usage: migrate [-path <dir>] <up|down N|version>")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	command, steps, err := parseArgs(fs.Args())
	if err != nil {
		errOut.printf("error: %v\n", err)
		fs.Usage()
		return exitUsage
	}

	cfg, err := config.Load()
	if err != nil {
		errOut.printf("error: %v\n", err)
		return exitFailure
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	runner, err := database.NewRunner(ctx, cfg.Database, *migrationsPath)
	if err != nil {
		errOut.printf("error: %v\n", err)
		return exitFailure
	}
	defer func() {
		if closeErr := runner.Close(); closeErr != nil {
			errOut.printf("error: close migration runner: %v\n", closeErr)
			if code == exitSuccess {
				code = exitFailure
			}
		}
	}()

	switch command {
	case commandUp:
		changed, err := runner.Up()
		if err != nil {
			errOut.printf("error: %v\n", err)
			return exitFailure
		}
		if changed {
			out.println("migrations applied")
		} else {
			out.println("no pending migrations")
		}
	case commandDown:
		changed, err := runner.Down(steps)
		if err != nil {
			errOut.printf("error: %v\n", err)
			return exitFailure
		}
		if changed {
			out.println("migrations rolled back")
		} else {
			out.println("no migrations rolled back")
		}
	case commandVersion:
		version, dirty, err := runner.Version()
		if errors.Is(err, database.ErrNoMigration) {
			out.println("No migration has been applied.")
			return exitSuccess
		}
		if err != nil {
			errOut.printf("error: %v\n", err)
			return exitFailure
		}
		out.printf("version: %d\n", version)
		out.printf("dirty: %t\n", dirty)
		if dirty {
			errOut.println("warning: migration state is dirty; inspect and recover it manually; no force was applied")
		}
	}

	return exitSuccess
}

func parseArgs(args []string) (string, int, error) {
	if len(args) == 0 {
		return "", 0, errors.New("a command is required: up, down N, or version")
	}

	switch args[0] {
	case commandUp:
		if len(args) != 1 {
			return "", 0, fmt.Errorf("%s does not accept arguments", commandUp)
		}
		return commandUp, 0, nil
	case commandVersion:
		if len(args) != 1 {
			return "", 0, fmt.Errorf("%s does not accept arguments", commandVersion)
		}
		return commandVersion, 0, nil
	case commandDown:
		if len(args) != 2 {
			return "", 0, errors.New("down requires exactly one positive integer argument")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 {
			return "", 0, errors.New("down requires a positive integer argument")
		}
		return commandDown, n, nil
	default:
		return "", 0, fmt.Errorf("unknown command %q; expected up, down N, or version", args[0])
	}
}
