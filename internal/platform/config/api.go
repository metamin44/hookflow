package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// EnvAPIAddr is the environment variable that controls the HTTP listen address.
const EnvAPIAddr = "HOOKFLOW_API_ADDR"

// DefaultAPIAddr is used when HOOKFLOW_API_ADDR is unset or empty.
const DefaultAPIAddr = ":8080"

// API contains the HTTP server settings.
type API struct {
	Addr string
}

// LoadAPI reads and validates the API-specific configuration.
//
// It is deliberately separate from Load so cmd/migrate keeps its existing
// database-only configuration behavior.
func LoadAPI() (API, error) {
	return loadAPI(os.LookupEnv)
}

func loadAPI(getenv func(string) (string, bool)) (API, error) {
	raw, _ := getenv(EnvAPIAddr)
	addr := strings.TrimSpace(raw)
	if addr == "" {
		addr = DefaultAPIAddr
	}

	if err := validateAPIAddr(addr); err != nil {
		return API{}, err
	}
	return API{Addr: addr}, nil
}

func validateAPIAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s must be a host:port address", EnvAPIAddr)
	}
	if strings.ContainsAny(host, " \t\r\n/") {
		return fmt.Errorf("%s host must not contain whitespace or slashes", EnvAPIAddr)
	}

	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return fmt.Errorf("%s must use a port between 1 and 65535", EnvAPIAddr)
	}
	return nil
}
