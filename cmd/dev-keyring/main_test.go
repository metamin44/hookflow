package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metamin44/hookflow/internal/platform/kek"
)

func testOptions(t *testing.T, path, kid string) options {
	t.Helper()
	return options{path: path, kid: kid, allowedKIDs: []string{kid}}
}

func TestEnsureCreatesAUsableKeyringOutsideAnyRepositoryPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "keyring.json")
	var out bytes.Buffer
	if err := ensure(&out, testOptions(t, path, "kek-1")); err != nil {
		t.Fatalf("ensure() error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat keyring: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("keyring is not a regular file: %v", info.Mode())
	}
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("keyring mode = %o, want 600", got)
		}
	}

	provider, err := kek.NewFileProvider(path, "kek-1", kek.FileOptions{})
	if err != nil {
		t.Fatalf("file provider rejected the generated keyring: %v", err)
	}
	if err := provider.ValidateKIDs([]string{"kek-1"}); err != nil {
		t.Fatalf("generated keyring is inconsistent with the configured kid: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, path) {
		t.Fatalf("output does not name the keyring path: %q", output)
	}
	encoded := encodedKeyForKid(t, path, "kek-1")
	if strings.Contains(output, encoded) {
		t.Fatal("tool output contains key material")
	}
}

func TestEnsureWithRootEnforcesRepositoryPlacement(t *testing.T) {
	repository := t.TempDir()
	inside := filepath.Join(repository, "keys", "keyring.json")
	if err := ensureWithRoot(io.Discard, testOptions(t, inside, "kek-1"), repository); err == nil {
		t.Fatal("repository-contained keyring path was accepted")
	}
	if _, err := os.Stat(inside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected keyring path was created: %v", err)
	}
	if err := ensureWithRoot(io.Discard, testOptions(t, "relative/keyring.json", "kek-1"), repository); err == nil {
		t.Fatal("relative keyring path was accepted")
	}

	external := filepath.Join(t.TempDir(), "external", "keyring.json")
	if err := ensureWithRoot(io.Discard, testOptions(t, external, "kek-1"), repository); err != nil {
		t.Fatalf("external keyring path was rejected: %v", err)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("external keyring was not created: %v", err)
	}
}

func TestEnsureWithRootResolvesSymlinks(t *testing.T) {
	repository := t.TempDir()
	external := t.TempDir()
	insideLink := filepath.Join(repository, "external-link")
	if err := os.Symlink(external, insideLink); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	allowed := filepath.Join(insideLink, "keyring.json")
	if err := ensureWithRoot(io.Discard, testOptions(t, allowed, "kek-1"), repository); err != nil {
		t.Fatalf("repository symlink to external storage was rejected: %v", err)
	}

	outsideLink := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(repository, outsideLink); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	rejected := filepath.Join(outsideLink, "nested", "keyring.json")
	if err := ensureWithRoot(io.Discard, testOptions(t, rejected, "kek-1"), repository); err == nil {
		t.Fatal("external symlink resolving into the repository was accepted")
	}
	if _, err := os.Stat(rejected); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink-resolved repository path was created: %v", err)
	}
}

func TestEnsureNeverOverwritesExistingKeyMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	var first bytes.Buffer
	if err := ensure(&first, testOptions(t, path, "kek-1")); err != nil {
		t.Fatalf("first ensure() error: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read keyring: %v", err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat keyring: %v", err)
	}

	var second bytes.Buffer
	if err := ensure(&second, testOptions(t, path, "kek-1")); err != nil {
		t.Fatalf("second ensure() error: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read keyring: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("second ensure() replaced existing key material")
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat keyring: %v", err)
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatal("second ensure() rewrote the existing keyring file")
	}
	if !strings.Contains(second.String(), "valid") {
		t.Fatalf("second ensure() did not report validation: %q", second.String())
	}
}

func TestVerifyOnlyDoesNotCreateOrRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	opts := testOptions(t, path, "kek-1")
	opts.verifyOnly = true
	var out bytes.Buffer
	if err := ensure(&out, opts); err == nil {
		t.Fatal("verify-only succeeded without a keyring")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verify-only created a keyring: %v", err)
	}
}

func TestEnsureRejectsMalformedExistingKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, []byte(`{"current":"kek-1","keys":{}}`), 0o600); err != nil {
		t.Fatalf("write malformed keyring: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read keyring: %v", err)
	}

	var out bytes.Buffer
	if err := ensure(&out, testOptions(t, path, "kek-1")); err == nil {
		t.Fatal("malformed keyring was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read keyring: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("malformed keyring was silently replaced")
	}
}

func TestEnsureRejectsInsecurePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows; the File Provider skips the check there")
	}
	path := filepath.Join(t.TempDir(), "keyring.json")
	var out bytes.Buffer
	if err := ensure(&out, testOptions(t, path, "kek-1")); err != nil {
		t.Fatalf("ensure() error: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod keyring: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read keyring: %v", err)
	}

	out.Reset()
	err = ensure(&out, testOptions(t, path, "kek-1"))
	if err == nil {
		t.Fatal("insecure keyring permissions were accepted")
	}
	if !strings.Contains(err.Error(), "0600") {
		t.Fatalf("error does not explain the permission requirement: %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read keyring: %v", readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("insecure keyring was silently replaced")
	}
}

func TestEnsureRequiresEveryConfiguredKID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	var out bytes.Buffer
	if err := ensure(&out, testOptions(t, path, "kek-1")); err != nil {
		t.Fatalf("ensure() error: %v", err)
	}
	opts := testOptions(t, path, "kek-1")
	opts.allowedKIDs = []string{"kek-1", "kek-2"}
	out.Reset()
	if err := ensure(&out, opts); err == nil {
		t.Fatal("keyring accepted an allowed kid that is absent from the ring")
	}
}

func TestParseOptionsDefaultsAndValidation(t *testing.T) {
	env := map[string]string{
		"HOOKFLOW_SECRET_KEYRING_PATH": filepath.Join(t.TempDir(), "keyring.json"),
		"HOOKFLOW_SECRET_CURRENT_KID":  "kek-env",
		"HOOKFLOW_SECRET_ALLOWED_KIDS": "kek-env, kek-old",
	}
	lookup := func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
	opts, err := parseOptions(nil, lookup)
	if err != nil {
		t.Fatalf("parseOptions() error: %v", err)
	}
	if opts.kid != "kek-env" || len(opts.allowedKIDs) != 2 || opts.verifyOnly {
		t.Fatalf("unexpected options: %+v", opts)
	}

	if _, err := parseOptions([]string{"-path", "relative/keyring.json"}, lookup); err == nil {
		t.Fatal("relative keyring path was accepted")
	}
	if _, err := parseOptions([]string{"extra"}, lookup); err == nil {
		t.Fatal("unexpected positional argument was accepted")
	}
}

// encodedKeyForKid returns the canonical key encodings stored in the ring so a
// test can prove they never appear in tool output. It is never logged.
func encodedKeyForKid(t *testing.T, path, kid string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read keyring: %v", err)
	}
	var ring struct {
		Keys map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &ring); err != nil {
		t.Fatalf("decode keyring: %v", err)
	}
	return ring.Keys[kid]
}
