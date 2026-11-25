package config

import (
	"strings"
	"testing"
)

func baseEnv() map[string]string {
	return map[string]string{
		EnvDBName:     "hookflow_dev",
		EnvDBUser:     "hookflow",
		EnvDBPassword: "unit-test-password",
		EnvDBHost:     "127.0.0.1",
		EnvDBPort:     "5433",
		EnvDBSSLMode:  "disable",
	}
}

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

func TestLoadValid(t *testing.T) {
	cfg, err := load(lookupFrom(baseEnv()))
	if err != nil {
		t.Fatalf("load() error: %v", err)
	}

	if cfg.Database.Name != "hookflow_dev" {
		t.Fatalf("database name = %q", cfg.Database.Name)
	}
	if cfg.Database.User != "hookflow" {
		t.Fatalf("database user = %q", cfg.Database.User)
	}
	if cfg.Database.Password != "unit-test-password" {
		t.Fatal("database password was not loaded")
	}
	if cfg.Database.Host != "127.0.0.1" {
		t.Fatalf("database host = %q", cfg.Database.Host)
	}
	if cfg.Database.Port != 5433 {
		t.Fatalf("database port = %d", cfg.Database.Port)
	}
	if cfg.Database.SSLMode != "disable" {
		t.Fatalf("database ssl mode = %q", cfg.Database.SSLMode)
	}
}

func TestLoadReadsEnvironment(t *testing.T) {
	for key, value := range baseEnv() {
		t.Setenv(key, value)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Database.Port != 5433 {
		t.Fatalf("database port = %d", cfg.Database.Port)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	for _, key := range requiredDatabaseKeys {
		t.Run(key, func(t *testing.T) {
			env := baseEnv()
			delete(env, key)

			_, err := load(lookupFrom(env))
			if err == nil {
				t.Fatalf("load() succeeded with %s missing", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error does not identify %s: %v", key, err)
			}
		})
	}
}

func TestLoadMultipleMissing(t *testing.T) {
	env := baseEnv()
	delete(env, EnvDBUser)
	delete(env, EnvDBPassword)

	_, err := load(lookupFrom(env))
	if err == nil {
		t.Fatal("load() succeeded with required values missing")
	}
	if !strings.Contains(err.Error(), EnvDBUser) || !strings.Contains(err.Error(), EnvDBPassword) {
		t.Fatalf("error does not identify both missing variables: %v", err)
	}
}

func TestLoadEmptyRequiredValue(t *testing.T) {
	env := baseEnv()
	env[EnvDBName] = ""

	_, err := load(lookupFrom(env))
	if err == nil {
		t.Fatal("load() succeeded with an empty required value")
	}
	if !strings.Contains(err.Error(), EnvDBName) {
		t.Fatalf("error does not identify %s: %v", EnvDBName, err)
	}
}

func TestLoadInvalidPort(t *testing.T) {
	tests := []string{
		"not-a-number",
		"-1",
		"0",
		"65536",
	}

	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			env := baseEnv()
			env[EnvDBPort] = value

			_, err := load(lookupFrom(env))
			if err == nil {
				t.Fatal("load() succeeded with an invalid port")
			}
			if !strings.Contains(err.Error(), EnvDBPort) {
				t.Fatalf("error does not identify %s: %v", EnvDBPort, err)
			}
		})
	}
}

func TestLoadPortBoundaries(t *testing.T) {
	for _, value := range []string{"1", "65535"} {
		t.Run(value, func(t *testing.T) {
			env := baseEnv()
			env[EnvDBPort] = value

			if _, err := load(lookupFrom(env)); err != nil {
				t.Fatalf("load() rejected valid port %s: %v", value, err)
			}
		})
	}
}

func TestLoadInvalidSSLMode(t *testing.T) {
	env := baseEnv()
	env[EnvDBSSLMode] = "bogus"

	_, err := load(lookupFrom(env))
	if err == nil {
		t.Fatal("load() succeeded with an invalid SSL mode")
	}
	if !strings.Contains(err.Error(), EnvDBSSLMode) {
		t.Fatalf("error does not identify %s: %v", EnvDBSSLMode, err)
	}
	for _, mode := range allowedSSLModes {
		if !strings.Contains(err.Error(), mode) {
			t.Fatalf("error does not list supported SSL mode %q: %v", mode, err)
		}
	}
}

func TestLoadSupportedSSLModes(t *testing.T) {
	for _, mode := range allowedSSLModes {
		t.Run(mode, func(t *testing.T) {
			env := baseEnv()
			env[EnvDBSSLMode] = mode

			cfg, err := load(lookupFrom(env))
			if err != nil {
				t.Fatalf("load() rejected supported SSL mode %q: %v", mode, err)
			}
			if cfg.Database.SSLMode != mode {
				t.Fatalf("database ssl mode = %q, want %q", cfg.Database.SSLMode, mode)
			}
		})
	}
}

func TestLoadErrorDoesNotLeakPassword(t *testing.T) {
	env := baseEnv()
	env[EnvDBPassword] = "unit-test-secret-do-not-log"
	env[EnvDBPort] = "not-a-port"

	_, err := load(lookupFrom(env))
	if err == nil {
		t.Fatal("load() succeeded with an invalid port")
	}
	if strings.Contains(err.Error(), env[EnvDBPassword]) {
		t.Fatal("configuration error contains the database password")
	}
}
