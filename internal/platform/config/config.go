// Package config loads HookFlow configuration from the process environment.
package config

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Environment variable names approved for the PostgreSQL development settings.
const (
	EnvDBName     = "HOOKFLOW_DB_NAME"
	EnvDBUser     = "HOOKFLOW_DB_USER"
	EnvDBPassword = "HOOKFLOW_DB_PASSWORD" //nolint:gosec // Environment variable name, not a credential.
	EnvDBHost     = "HOOKFLOW_DB_HOST"
	EnvDBPort     = "HOOKFLOW_DB_PORT"
	EnvDBSSLMode  = "HOOKFLOW_DB_SSLMODE"
)

var requiredDatabaseKeys = []string{
	EnvDBName,
	EnvDBUser,
	EnvDBPassword,
	EnvDBHost,
	EnvDBPort,
	EnvDBSSLMode,
}

var allowedSSLModes = []string{
	"disable",
	"allow",
	"prefer",
	"require",
	"verify-ca",
	"verify-full",
}

// Config is the environment-driven application configuration.
type Config struct {
	Database Database
}

// Database contains the PostgreSQL connection settings.
type Database struct {
	Name     string
	User     string
	Password string
	Host     string
	Port     uint16
	SSLMode  string
}

// Load reads and validates configuration from the process environment.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(getenv func(string) (string, bool)) (Config, error) {
	values := make(map[string]string, len(requiredDatabaseKeys))
	missing := make([]string, 0, len(requiredDatabaseKeys))

	for _, key := range requiredDatabaseKeys {
		value, ok := getenv(key)
		if !ok || value == "" {
			missing = append(missing, key)
			continue
		}
		values[key] = value
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	port, err := strconv.ParseUint(values[EnvDBPort], 10, 16)
	if err != nil || port == 0 {
		return Config{}, fmt.Errorf("%s must be an integer between 1 and 65535", EnvDBPort)
	}

	sslMode := values[EnvDBSSLMode]
	if !slices.Contains(allowedSSLModes, sslMode) {
		return Config{}, fmt.Errorf("%s must be one of: %s", EnvDBSSLMode, strings.Join(allowedSSLModes, ", "))
	}

	return Config{
		Database: Database{
			Name:     values[EnvDBName],
			User:     values[EnvDBUser],
			Password: values[EnvDBPassword],
			Host:     values[EnvDBHost],
			Port:     uint16(port),
			SSLMode:  sslMode,
		},
	}, nil
}
