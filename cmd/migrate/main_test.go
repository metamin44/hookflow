package main

import (
	"errors"
	"io"
	"testing"
)

var errWriterUnavailable = errors.New("output stream unavailable")

type failingWriter struct {
	writes int
}

func (f *failingWriter) Write([]byte) (int, error) {
	f.writes++
	return 0, errWriterUnavailable
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantCommand string
		wantSteps   int
		wantErr     bool
	}{
		{name: "up", args: []string{"up"}, wantCommand: commandUp},
		{name: "version", args: []string{"version"}, wantCommand: commandVersion},
		{name: "down", args: []string{"down", "2"}, wantCommand: commandDown, wantSteps: 2},
		{name: "no command", args: nil, wantErr: true},
		{name: "unknown command", args: []string{"reset"}, wantErr: true},
		{name: "up with argument", args: []string{"up", "extra"}, wantErr: true},
		{name: "version with argument", args: []string{"version", "extra"}, wantErr: true},
		{name: "down without count", args: []string{"down"}, wantErr: true},
		{name: "down zero", args: []string{"down", "0"}, wantErr: true},
		{name: "down negative", args: []string{"down", "-1"}, wantErr: true},
		{name: "down non-numeric", args: []string{"down", "many"}, wantErr: true},
		{name: "down extra argument", args: []string{"down", "1", "extra"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command, steps, err := parseArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("parseArgs() succeeded, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs() error: %v", err)
			}
			if command != tt.wantCommand || steps != tt.wantSteps {
				t.Fatalf("parseArgs() = (%q, %d), want (%q, %d)", command, steps, tt.wantCommand, tt.wantSteps)
			}
		})
	}
}

func TestRunRejectsInvalidArgumentsBeforeConfiguration(t *testing.T) {
	tests := [][]string{
		nil,
		{"bogus"},
		{"down"},
		{"down", "0"},
		{"up", "extra"},
	}

	for _, args := range tests {
		if code := run(args, io.Discard, io.Discard); code != 2 {
			t.Fatalf("run(%v) = %d, want 2", args, code)
		}
	}
}

func TestReportWriterCapturesFirstWriteFailure(t *testing.T) {
	stdout := &failingWriter{}
	out := &reportWriter{w: stdout}

	out.println("migrations applied")
	if !errors.Is(out.err, errWriterUnavailable) {
		t.Fatalf("reportWriter.err = %v, want %v", out.err, errWriterUnavailable)
	}
	if _, err := out.Write([]byte("second")); !errors.Is(err, errWriterUnavailable) {
		t.Fatalf("Write() error = %v, want %v", err, errWriterUnavailable)
	}
	out.println("third")
	if !errors.Is(out.err, errWriterUnavailable) {
		t.Fatalf("reportWriter.err changed after the first failure: %v", out.err)
	}
	if stdout.writes != 2 {
		t.Fatalf("failingWriter calls = %d, want 2: later reports must be short-circuited once a failure is recorded", stdout.writes)
	}
}

func TestFinalizeExitCodeNeverReportsSuccessForUndeliverableResults(t *testing.T) {
	unavailable := &reportWriter{w: &failingWriter{}}
	unavailable.println("version: 13")
	clean := &reportWriter{w: io.Discard}

	tests := []struct {
		name string
		code int
		out  *reportWriter
		want int
	}{
		{name: "successful command with lost result", code: 0, out: unavailable, want: 1},
		{name: "successful command with clean output", code: 0, out: clean, want: 0},
		{name: "usage error keeps precedence", code: 2, out: unavailable, want: 2},
		{name: "runtime failure keeps precedence", code: 1, out: unavailable, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errOut := &reportWriter{w: io.Discard}
			if got := finalizeExitCode(tt.code, tt.out, errOut); got != tt.want {
				t.Fatalf("finalizeExitCode(%d) = %d, want %d", tt.code, got, tt.want)
			}
		})
	}

	both := &reportWriter{w: &failingWriter{}}
	both.println("nothing can be reported")
	if got := finalizeExitCode(0, both, both); got != 1 {
		t.Fatalf("finalizeExitCode() with both streams broken = %d, want 1", got)
	}
}

func TestRunUsagePathFailsClosedWhenStderrIsUndeliverable(t *testing.T) {
	errOut := &failingWriter{}
	if code := run([]string{"down", "0"}, io.Discard, errOut); code != 2 {
		t.Fatalf("run() = %d, want the usage status 2", code)
	}
	if errOut.writes == 0 {
		t.Fatal("usage output never reached the stderr writer")
	}
}
