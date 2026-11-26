package config

import (
	"strings"
	"testing"
)

func TestLoadMetricsAddr(t *testing.T) {
	for _, value := range []string{"", "  ", "127.0.0.1:9101", "localhost:9101", "[::1]:9101", "192.0.2.10:9101"} {
		got, err := loadMetricsAddr(func(string) (string, bool) { return value, true })
		if err != nil {
			t.Fatalf("loadMetricsAddr(%q): %v", value, err)
		}
		if strings.TrimSpace(value) != got {
			t.Fatalf("address = %q, want %q", got, strings.TrimSpace(value))
		}
	}
	for _, value := range []string{
		"localhost", ":9101", "0.0.0.0:9101", "[::]:9101",
		"[::ffff:0.0.0.0]:9101", "[::ffff:0:0]:9101", "[::%eth0]:9101",
		"localhost:0", "localhost:65536", "bad host:1", "localhost/path:1",
	} {
		if _, err := loadMetricsAddr(func(string) (string, bool) { return value, true }); err == nil || !strings.Contains(err.Error(), EnvMetricsAddr) {
			t.Fatalf("invalid address %q error = %v", value, err)
		}
	}
}
