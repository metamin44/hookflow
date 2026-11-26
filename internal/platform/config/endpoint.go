package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// EnvMaxActiveEndpoints is the approved per-organization active-endpoint
// ceiling (U37/U38).
const EnvMaxActiveEndpoints = "HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG"

// DefaultMaxActiveEndpoints is used when the ceiling variable is unset.
const DefaultMaxActiveEndpoints = 100

// EndpointLimits contains the management-path numeric limits.
type EndpointLimits struct {
	MaxActivePerOrganization int
}

// Validate reports whether every limit is usable.
func (l EndpointLimits) Validate() error {
	if l.MaxActivePerOrganization <= 0 {
		return fmt.Errorf("%s must be a positive integer", EnvMaxActiveEndpoints)
	}
	return nil
}

// LoadEndpointLimits reads and validates the endpoint ceiling.
func LoadEndpointLimits() (EndpointLimits, error) {
	return loadEndpointLimits(os.LookupEnv)
}

func loadEndpointLimits(getenv func(string) (string, bool)) (EndpointLimits, error) {
	limits := EndpointLimits{MaxActivePerOrganization: DefaultMaxActiveEndpoints}
	value, ok := getenv(EnvMaxActiveEndpoints)
	if ok {
		trimmed := strings.TrimSpace(value)
		parsed, err := strconv.Atoi(trimmed)
		if err != nil || parsed <= 0 {
			return EndpointLimits{}, fmt.Errorf("%s must be a positive integer", EnvMaxActiveEndpoints)
		}
		limits.MaxActivePerOrganization = parsed
	}
	if err := limits.Validate(); err != nil {
		return EndpointLimits{}, err
	}
	return limits, nil
}
