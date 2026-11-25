package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/metamin44/hookflow/internal/platform/kek"
)

// Deployment environment variable names for the approved secret-delivery gate.
const (
	EnvDeploymentMode        = "HOOKFLOW_DEPLOYMENT_MODE"
	EnvAllowUnsignedDelivery = "HOOKFLOW_ALLOW_UNSIGNED_DELIVERY"
	EnvSecretProvider        = "HOOKFLOW_SECRET_PROVIDER"
	EnvSecretKeyringPath     = "HOOKFLOW_SECRET_KEYRING_PATH"
	EnvSecretCurrentKID      = "HOOKFLOW_SECRET_CURRENT_KID"
	EnvSecretAllowedKIDs     = "HOOKFLOW_SECRET_ALLOWED_KIDS"
	EnvSecretAllowedProvider = "HOOKFLOW_SECRET_ALLOWED_PROVIDERS"
)

// DeploymentMode is the explicit, fail-closed deployment selector.
type DeploymentMode string

// The only accepted deployment modes.
const (
	ModeDevelopment DeploymentMode = "development"
	ModeProduction  DeploymentMode = "production"
)

// DeliverySecurity is the non-secret delivery security configuration. It never
// carries KEK material; only a ring path, kid names, and provider selectors.
type DeliverySecurity struct {
	Mode                  DeploymentMode
	AllowUnsignedDelivery bool
	Provider              string
	KeyringPath           string
	CurrentKID            string
	AllowedKIDs           []string
	AllowedProviders      []string
}

// Validate applies the startup matrix. Any contradiction fails closed.
func (s DeliverySecurity) Validate() error {
	switch s.Mode {
	case ModeDevelopment, ModeProduction:
	default:
		return errors.New("deployment mode must be development or production")
	}
	if s.AllowUnsignedDelivery && s.Mode != ModeDevelopment {
		return errors.New("unsigned delivery may be enabled only in development")
	}
	if err := s.validateProviderSelection(); err != nil {
		return err
	}
	if s.KeyringPath == "" {
		return errors.New("secret keyring path is required")
	}
	if s.CurrentKID == "" {
		return errors.New("secret current kid is required")
	}
	return s.validateAllowlists()
}

func (s DeliverySecurity) validateProviderSelection() error {
	if s.Provider == "" {
		return errors.New("secret provider selector is required")
	}
	if _, ok := kek.ProviderTag(s.Provider); !ok || !kek.AvailableProvider(s.Provider) {
		return errors.New("secret provider selector is not implemented by this build")
	}
	if s.Mode == ModeProduction && s.Provider == kek.ProviderLocalFile {
		// The local file ring is a development provider. No production provider
		// is implemented by this build, so production fails closed here.
		return errors.New("secret provider is not available for production in this build")
	}
	return nil
}

func (s DeliverySecurity) validateAllowlists() error {
	if len(s.AllowedKIDs) == 0 {
		return errors.New("secret kid allowlist must not be empty")
	}
	allowed := make(map[string]bool, len(s.AllowedKIDs))
	for _, kid := range s.AllowedKIDs {
		if strings.TrimSpace(kid) == "" {
			return errors.New("secret kid allowlist contains an empty entry")
		}
		allowed[kid] = true
	}
	if !allowed[s.CurrentKID] {
		return errors.New("secret current kid must be in the kid allowlist")
	}
	if len(s.AllowedProviders) == 0 {
		return errors.New("secret provider allowlist must not be empty")
	}
	providerAllowed := false
	for _, name := range s.AllowedProviders {
		if _, ok := kek.ProviderTag(name); !ok || !kek.AvailableProvider(name) {
			return errors.New("secret provider allowlist names an unimplemented provider")
		}
		if name == s.Provider {
			providerAllowed = true
		}
	}
	if !providerAllowed {
		return errors.New("secret provider selector must be in the provider allowlist")
	}
	return nil
}

// AllowsProvider reports whether the envelope provider tag is allowlisted.
func (s DeliverySecurity) AllowsProvider(tag byte) bool {
	name := kek.NameForProvider(tag)
	if name == "" {
		return false
	}
	for _, allowed := range s.AllowedProviders {
		if allowed == name {
			return true
		}
	}
	return false
}

// UnsignedAllowed reports whether an empty stored secret may be sent unsigned.
func (s DeliverySecurity) UnsignedAllowed() bool {
	return s.Mode == ModeDevelopment && s.AllowUnsignedDelivery
}

// LoadDeliverySecurity reads and validates the delivery security configuration.
func LoadDeliverySecurity() (DeliverySecurity, error) {
	return loadDeliverySecurity(os.LookupEnv)
}

func loadDeliverySecurity(getenv func(string) (string, bool)) (DeliverySecurity, error) {
	security := DeliverySecurity{Mode: DeploymentMode(strings.TrimSpace(getenvValue(getenv, EnvDeploymentMode)))}
	if value, ok := getenv(EnvAllowUnsignedDelivery); ok {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return DeliverySecurity{}, fmt.Errorf("invalid deployment configuration: %s must be a boolean", EnvAllowUnsignedDelivery)
		}
		security.AllowUnsignedDelivery = parsed
	}
	security.Provider = strings.TrimSpace(getenvValue(getenv, EnvSecretProvider))
	security.KeyringPath = strings.TrimSpace(getenvValue(getenv, EnvSecretKeyringPath))
	security.CurrentKID = strings.TrimSpace(getenvValue(getenv, EnvSecretCurrentKID))
	security.AllowedKIDs = splitCSV(getenvValue(getenv, EnvSecretAllowedKIDs))
	security.AllowedProviders = splitCSV(getenvValue(getenv, EnvSecretAllowedProvider))
	if err := security.Validate(); err != nil {
		return DeliverySecurity{}, fmt.Errorf("invalid deployment configuration: %w", err)
	}
	return security, nil
}

func getenvValue(getenv func(string) (string, bool), key string) string {
	value, _ := getenv(key)
	return value
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	return result
}
