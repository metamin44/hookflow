package config

import (
	"strings"
	"testing"
)

func TestLoadAPIDefault(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "unset", env: map[string]string{}},
		{name: "empty", env: map[string]string{EnvAPIAddr: ""}},
		{name: "whitespace", env: map[string]string{EnvAPIAddr: "   "}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadAPI(lookupFrom(tt.env))
			if err != nil {
				t.Fatalf("loadAPI() error: %v", err)
			}
			if cfg.Addr != DefaultAPIAddr {
				t.Fatalf("API address = %q, want %q", cfg.Addr, DefaultAPIAddr)
			}
		})
	}
}

func TestLoadAPICustom(t *testing.T) {
	tests := []struct {
		name string
		addr string
	}{
		{name: "ipv4", addr: "127.0.0.1:9090"},
		{name: "wildcard", addr: ":8081"},
		{name: "hostname", addr: "localhost:8080"},
		{name: "ipv6", addr: "[::1]:8080"},
		{name: "trimmed", addr: "  127.0.0.1:9090  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{EnvAPIAddr: tt.addr}
			cfg, err := loadAPI(lookupFrom(env))
			if err != nil {
				t.Fatalf("loadAPI() error: %v", err)
			}
			if cfg.Addr != strings.TrimSpace(tt.addr) {
				t.Fatalf("API address = %q, want %q", cfg.Addr, strings.TrimSpace(tt.addr))
			}
		})
	}
}

func TestLoadAPIFromEnvironment(t *testing.T) {
	t.Setenv(EnvAPIAddr, "127.0.0.1:9090")

	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("LoadAPI() error: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9090" {
		t.Fatalf("API address = %q", cfg.Addr)
	}
}

func TestLoadAPIInvalid(t *testing.T) {
	tests := []string{
		"localhost",
		"localhost:",
		"localhost:0",
		"localhost:65536",
		"localhost:not-a-port",
		"localhost:8080/extra",
		"http://localhost:8080",
		"bad host:8080",
		"::1:8080",
	}

	for _, addr := range tests {
		t.Run(addr, func(t *testing.T) {
			env := map[string]string{EnvAPIAddr: addr}
			_, err := loadAPI(lookupFrom(env))
			if err == nil {
				t.Fatal("loadAPI() succeeded with an invalid address")
			}
			if !strings.Contains(err.Error(), EnvAPIAddr) {
				t.Fatalf("error does not identify %s: %v", EnvAPIAddr, err)
			}
			if strings.Contains(err.Error(), addr) {
				t.Fatalf("error repeats the invalid address: %v", err)
			}
		})
	}
}

func TestLoadDoesNotReadAPIAddr(t *testing.T) {
	getenv := func(key string) (string, bool) {
		if key == EnvAPIAddr {
			t.Fatalf("database configuration read %s", EnvAPIAddr)
		}
		value, ok := baseEnv()[key]
		return value, ok
	}

	cfg, err := load(getenv)
	if err != nil {
		t.Fatalf("load() error: %v", err)
	}
	if cfg.Database.Port != 5433 {
		t.Fatalf("database port = %d", cfg.Database.Port)
	}
}
