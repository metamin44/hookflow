package config

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// EnvMetricsAddr optionally enables the process-local Prometheus endpoint.
const EnvMetricsAddr = "HOOKFLOW_METRICS_ADDR"

// LoadMetricsAddr reads an optional host:port address. Empty disables the
// listener; structured logs remain available in every process.
func LoadMetricsAddr() (string, error) {
	return loadMetricsAddr(os.LookupEnv)
}

func loadMetricsAddr(getenv func(string) (string, bool)) (string, error) {
	raw, _ := getenv(EnvMetricsAddr)
	address := strings.TrimSpace(raw)
	if address == "" {
		return "", nil
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("%s must be a host:port address", EnvMetricsAddr)
	}
	if host == "" {
		return "", fmt.Errorf("%s must use an explicit host; wildcard binding is not allowed", EnvMetricsAddr)
	}
	ipHost := host
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		ipHost = host[:zone]
	}
	if ip, err := netip.ParseAddr(ipHost); err == nil && ip.Unmap().IsUnspecified() {
		return "", fmt.Errorf("%s must use an explicit non-wildcard host", EnvMetricsAddr)
	}
	if strings.ContainsAny(host, " \t\r\n/") {
		return "", fmt.Errorf("%s host must not contain whitespace or slashes", EnvMetricsAddr)
	}
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return "", fmt.Errorf("%s must use a port between 1 and 65535", EnvMetricsAddr)
	}
	return address, nil
}
