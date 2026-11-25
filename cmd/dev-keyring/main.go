// Command dev-keyring provisions and validates the development-only File KEK
// keyring used by cmd/worker in development.
//
// It is a developer setup tool, not a runtime component:
//   - generate only when no keyring exists (O_EXCL, never overwrite);
//   - never print, log, or embed key material;
//   - validate an existing keyring with the real File Provider and the same
//     kid allowlist the Worker will use;
//   - fail closed on missing, malformed, insecure or inconsistent rings.
//
// The keyring is intentionally stored outside the repository.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

const usageText = `Usage: dev-keyring [-path FILE] [-kid KID] [-verify-only]

Creates the development File KEK keyring only when it does not exist, then
validates it with the real File Provider. Existing key material is never
overwritten, replaced, or printed.

Defaults come from HOOKFLOW_SECRET_KEYRING_PATH and
HOOKFLOW_SECRET_CURRENT_KID, falling back to
<home>/.hookflow/dev/keyring.json and kek-1.
`

type options struct {
	path        string
	kid         string
	allowedKIDs []string
	verifyOnly  bool
}

func main() {
	opts, err := parseOptions(os.Args[1:], os.LookupEnv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stderr, usageText)
			return
		}
		fmt.Fprintf(os.Stderr, "dev-keyring: %v\n", err)
		os.Exit(1)
	}
	if err := ensure(os.Stdout, opts); err != nil {
		fmt.Fprintf(os.Stderr, "dev-keyring: %v\n", err)
		os.Exit(1)
	}
}

func parseOptions(args []string, getenv func(string) (string, bool)) (options, error) {
	flags := flag.NewFlagSet("dev-keyring", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	defaultPath := strings.TrimSpace(environmentValue(getenv, config.EnvSecretKeyringPath))
	if defaultPath == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return options{}, errors.New("cannot resolve a default keyring path: set -path or HOOKFLOW_SECRET_KEYRING_PATH")
		}
		defaultPath = filepath.Join(home, ".hookflow", "dev", "keyring.json")
	}
	defaultKID := strings.TrimSpace(environmentValue(getenv, config.EnvSecretCurrentKID))
	if defaultKID == "" {
		defaultKID = "kek-1"
	}

	path := flags.String("path", defaultPath, "absolute keyring file path")
	kid := flags.String("kid", defaultKID, "current KEK id")
	verifyOnly := flags.Bool("verify-only", false, "validate an existing keyring without creating one")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	allowed := splitList(environmentValue(getenv, config.EnvSecretAllowedKIDs))
	if len(allowed) == 0 {
		allowed = []string{strings.TrimSpace(*kid)}
	}
	opts := options{
		path:        strings.TrimSpace(*path),
		kid:         strings.TrimSpace(*kid),
		allowedKIDs: allowed,
		verifyOnly:  *verifyOnly,
	}
	if opts.path == "" {
		return options{}, errors.New("keyring path is required")
	}
	if !filepath.IsAbs(opts.path) {
		return options{}, fmt.Errorf("keyring path must be absolute (Windows: C:/Users/...; POSIX: /home/...): %q", opts.path)
	}
	if opts.kid == "" {
		return options{}, errors.New("current kid is required")
	}
	return opts, nil
}

func environmentValue(getenv func(string) (string, bool), key string) string {
	value, _ := getenv(key)
	return value
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// ensure creates the keyring only when it does not exist and validates every
// existing file with the real File Provider.
func ensure(w io.Writer, opts options) error {
	info, err := os.Lstat(opts.path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("keyring path exists but is not a regular file: %s", opts.path)
		}
		return verify(w, opts)
	case errors.Is(err, fs.ErrNotExist):
		if opts.verifyOnly {
			return fmt.Errorf("development keyring does not exist: %s", opts.path)
		}
		return generate(w, opts)
	default:
		return fmt.Errorf("inspect keyring path: %w", err)
	}
}

func generate(w io.Writer, opts options) error {
	if err := os.MkdirAll(filepath.Dir(opts.path), 0o700); err != nil {
		return fmt.Errorf("create keyring directory: %w", err)
	}

	key := make([]byte, secrets.DEKSize)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate key material: %w", err)
	}
	defer clear(key)

	payload, err := json.Marshal(map[string]any{
		"current": opts.kid,
		"keys":    map[string]string{opts.kid: base64.RawURLEncoding.EncodeToString(key)},
	})
	if err != nil {
		return fmt.Errorf("encode keyring: %w", err)
	}
	defer clear(payload)

	// O_EXCL makes creation race-safe and guarantees an existing keyring is
	// never replaced, even when two setup commands run at the same time.
	file, err := os.OpenFile(opts.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return verify(w, opts)
	}
	if err != nil {
		return fmt.Errorf("create keyring file: %w", err)
	}
	cleanup := func() {
		_ = file.Close()
		// #nosec G703 -- opts.path is an operator-supplied absolute path; this
		// command only ever removes the file it just created itself.
		_ = os.Remove(opts.path)
	}
	if _, err := file.Write(payload); err != nil {
		cleanup()
		return fmt.Errorf("write keyring file: %w", err)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync keyring file: %w", err)
	}
	if err := file.Close(); err != nil {
		// #nosec G703 -- opts.path is an operator-supplied absolute path; the
		// file was created by this process moments ago.
		_ = os.Remove(opts.path)
		return fmt.Errorf("close keyring file: %w", err)
	}
	if err := os.Chmod(opts.path, 0o600); err != nil {
		return fmt.Errorf("restrict keyring permissions: %w", err)
	}
	if err := validate(opts); err != nil {
		return fmt.Errorf("generated keyring failed validation: %w", err)
	}

	writef(w, "created development keyring: %s\n", opts.path)
	writef(w, "current kid: %s\n", opts.kid)
	writef(w, "set this path (never the key material) in .env:\n  %s=%s\n", config.EnvSecretKeyringPath, opts.path)
	return nil
}

func verify(w io.Writer, opts options) error {
	if err := validate(opts); err != nil {
		return fmt.Errorf("existing development keyring is not usable: %w", err)
	}
	writef(w, "development keyring is valid: %s\n", opts.path)
	writef(w, "current kid: %s\n", opts.kid)
	writef(w, "%s=%s\n", config.EnvSecretKeyringPath, opts.path)
	return nil
}

// writef reports the non-secret outcome. A developer setup command has no
// useful recovery when its own terminal writes fail, so the error is dropped
// deliberately rather than escalated.
func writef(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// validate uses the exact provider and allowlist validation the Worker performs
// at startup, so success here means the same file will be accepted there.
func validate(opts options) error {
	provider, err := kek.NewFileProvider(opts.path, opts.kid, kek.FileOptions{})
	if err != nil {
		return err
	}
	return provider.ValidateKIDs(opts.allowedKIDs)
}
