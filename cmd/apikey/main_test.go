package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/platform/config"
)

func TestSplitScopes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"spaces", "   ", nil},
		{"one", "events:write", []string{"events:write"}},
		{"many", " events:write , endpoints:read ,, ", []string{"events:write", "endpoints:read"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := splitScopes(testCase.input)
			if len(got) != len(testCase.want) {
				t.Fatalf("splitScopes(%q) length = %d, want %d", testCase.input, len(got), len(testCase.want))
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("splitScopes(%q)[%d] = %q, want %q", testCase.input, index, got[index], testCase.want[index])
				}
			}
		})
	}
}

func TestFormatTime(t *testing.T) {
	value := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3*3600))
	if got := formatTime(&value); got != "2030-01-02T00:04:05Z" {
		t.Fatalf("formatTime(offset) = %q", got)
	}
	if got := formatTime(nil); got != "never" {
		t.Fatalf("formatTime(nil) = %q, want never", got)
	}
}

func TestRunRejectsMissingOrUnknownCommandBeforeConfiguration(t *testing.T) {
	deps := dependencies{
		loadConfig: func() (config.Config, error) {
			t.Fatal("configuration was loaded before command validation")
			return config.Config{}, nil
		},
	}
	for _, args := range [][]string{nil, {"unknown"}} {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), args, &stdout, &stderr, deps); err == nil {
			t.Fatalf("run(%v) returned no error", args)
		}
		if !strings.Contains(stderr.String(), "usage: apikey") {
			t.Fatalf("run(%v) did not print usage", args)
		}
	}
}
