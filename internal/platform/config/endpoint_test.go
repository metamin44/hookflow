package config

import (
	"strings"
	"testing"
)

func TestLoadEndpointLimitsDefault(t *testing.T) {
	limits, err := loadEndpointLimits(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("loadEndpointLimits() error = %v", err)
	}
	if limits.MaxActivePerOrganization != DefaultMaxActiveEndpoints {
		t.Fatalf("default = %d, want %d", limits.MaxActivePerOrganization, DefaultMaxActiveEndpoints)
	}
}

func TestLoadEndpointLimitsExplicit(t *testing.T) {
	limits, err := loadEndpointLimits(func(key string) (string, bool) {
		if key == EnvMaxActiveEndpoints {
			return " 250 ", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("loadEndpointLimits() error = %v", err)
	}
	if limits.MaxActivePerOrganization != 250 {
		t.Fatalf("limit = %d, want 250", limits.MaxActivePerOrganization)
	}
}

func TestLoadEndpointLimitsRejectsInvalid(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "abc", "1.5"} {
		value := value
		t.Run(value, func(t *testing.T) {
			_, err := loadEndpointLimits(func(key string) (string, bool) {
				if key == EnvMaxActiveEndpoints {
					return value, true
				}
				return "", false
			})
			if err == nil {
				t.Fatalf("value %q accepted", value)
			}
			if strings.Contains(err.Error(), value) && value != "" {
				t.Fatalf("error reflects the raw value: %v", err)
			}
		})
	}
}
