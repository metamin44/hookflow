package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/metamin44/hookflow/internal/platform/kek"
)

func approvedSecurity(t *testing.T) DeliverySecurity {
	t.Helper()
	return DeliverySecurity{
		Mode:                  ModeDevelopment,
		AllowUnsignedDelivery: true,
		Provider:              kek.ProviderLocalFile,
		KeyringPath:           filepath.Join(t.TempDir(), "dev-keyring.json"),
		CurrentKID:            "dev-2026-11",
		AllowedKIDs:           []string{"dev-2026-11", "dev-2026-10"},
		AllowedProviders:      []string{kek.ProviderLocalFile},
	}
}

func TestDeliverySecurityValidateMatrix(t *testing.T) {
	if err := approvedSecurity(t).Validate(); err != nil {
		t.Fatalf("approved development security rejected: %v", err)
	}

	cases := []struct {
		name     string
		mutate   func(*DeliverySecurity)
		contains string
	}{
		{"unset mode", func(s *DeliverySecurity) { s.Mode = "" }, "deployment mode"},
		{"unknown mode", func(s *DeliverySecurity) { s.Mode = "staging" }, "deployment mode"},
		{"production unsigned", func(s *DeliverySecurity) { s.Mode = ModeProduction }, "unsigned delivery"},
		{"production file provider", func(s *DeliverySecurity) {
			s.Mode = ModeProduction
			s.AllowUnsignedDelivery = false
		}, "production"},
		{"missing provider", func(s *DeliverySecurity) { s.Provider = "" }, "provider selector"},
		{"unknown provider", func(s *DeliverySecurity) { s.Provider = "kms" }, "not implemented"},
		{"missing keyring", func(s *DeliverySecurity) { s.KeyringPath = "" }, "keyring path"},
		{"relative keyring", func(s *DeliverySecurity) { s.KeyringPath = "keys/dev-keyring.json" }, "absolute native path"},
		{"malformed keyring", func(s *DeliverySecurity) { s.KeyringPath = approvedSecurity(t).KeyringPath + "\x00" }, "absolute native path"},
		{"missing current kid", func(s *DeliverySecurity) { s.CurrentKID = "" }, "current kid"},
		{"empty kid allowlist", func(s *DeliverySecurity) { s.AllowedKIDs = nil }, "kid allowlist"},
		{"empty kid entry", func(s *DeliverySecurity) { s.AllowedKIDs = []string{"dev-2026-11", " "} }, "empty entry"},
		{"current kid not allowed", func(s *DeliverySecurity) { s.AllowedKIDs = []string{"dev-2026-10"} }, "current kid"},
		{"empty provider allowlist", func(s *DeliverySecurity) { s.AllowedProviders = nil }, "provider allowlist"},
		{"unimplemented provider allowlist", func(s *DeliverySecurity) { s.AllowedProviders = []string{"aws_kms"} }, "unimplemented"},
		{"provider not allowlisted", func(s *DeliverySecurity) { s.AllowedProviders = []string{"aws_kms", kek.ProviderLocalFile} }, "unimplemented"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			security := approvedSecurity(t)
			tc.mutate(&security)
			err := security.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.contains)
			}
		})
	}
}

func TestDeliverySecurityProviderSelection(t *testing.T) {
	security := approvedSecurity(t)
	if !security.UnsignedAllowed() {
		t.Fatal("development gate is not enabled")
	}
	if !security.AllowsProvider(0x01) {
		t.Fatal("local file provider is not allowlisted")
	}
	if security.AllowsProvider(0x02) {
		t.Fatal("unconfigured provider tag accepted")
	}
	if security.AllowsProvider(0x7f) {
		t.Fatal("unknown provider tag accepted")
	}
	security.AllowUnsignedDelivery = false
	if security.UnsignedAllowed() {
		t.Fatal("disabled gate reported enabled")
	}
}

func TestLoadDeliverySecurityFromEnvironment(t *testing.T) {
	values := map[string]string{
		EnvDeploymentMode:        "development",
		EnvAllowUnsignedDelivery: "true",
		EnvSecretProvider:        "file",
		EnvSecretKeyringPath:     " " + filepath.Join(t.TempDir(), "ring.json") + " ",
		EnvSecretCurrentKID:      "dev-2",
		EnvSecretAllowedKIDs:     " dev-2, dev-1 ,dev-2, ",
		EnvSecretAllowedProvider: "file,file",
	}
	security, err := loadDeliverySecurity(lookup(values))
	if err != nil {
		t.Fatalf("loadDeliverySecurity() error: %v", err)
	}
	if !filepath.IsAbs(security.KeyringPath) || security.CurrentKID != "dev-2" {
		t.Fatalf("security = %+v", security)
	}
	if len(security.AllowedKIDs) != 2 || security.AllowedKIDs[0] != "dev-2" || security.AllowedKIDs[1] != "dev-1" {
		t.Fatalf("allowed kids = %#v", security.AllowedKIDs)
	}
	if len(security.AllowedProviders) != 1 {
		t.Fatalf("allowed providers = %#v", security.AllowedProviders)
	}
	if !security.UnsignedAllowed() {
		t.Fatal("gate override not applied")
	}
}

func TestLoadDeliverySecurityRejectsInvalidEnvironment(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			EnvDeploymentMode:        "development",
			EnvSecretProvider:        "file",
			EnvSecretKeyringPath:     filepath.Join(t.TempDir(), "ring.json"),
			EnvSecretCurrentKID:      "dev-2",
			EnvSecretAllowedKIDs:     "dev-2",
			EnvSecretAllowedProvider: "file",
		}
	}
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"unset mode", EnvDeploymentMode, ""},
		{"unknown mode", EnvDeploymentMode, "qa"},
		{"invalid gate", EnvAllowUnsignedDelivery, "yes"},
		{"unset provider", EnvSecretProvider, ""},
		{"unknown provider", EnvSecretProvider, "vault_transit"},
		{"unset keyring", EnvSecretKeyringPath, ""},
		{"relative keyring", EnvSecretKeyringPath, "keys/ring.json"},
		{"malformed keyring", EnvSecretKeyringPath, filepath.Join(t.TempDir(), "ring.json") + "\x00"},
		{"unset current", EnvSecretCurrentKID, ""},
		{"unset allowed kids", EnvSecretAllowedKIDs, ""},
		{"unset allowed providers", EnvSecretAllowedProvider, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := base()
			values[tc.key] = tc.value
			if _, err := loadDeliverySecurity(lookup(values)); err == nil {
				t.Fatalf("invalid security accepted: %s=%q", tc.key, tc.value)
			}
		})
	}
}
