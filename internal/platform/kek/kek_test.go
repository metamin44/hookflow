package kek

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metamin44/hookflow/internal/platform/secrets"
)

var testWrapContext = Context{
	OrganizationID: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
	EndpointID:     [16]byte{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
}

func testKey(seed byte) []byte {
	key := make([]byte, secrets.DEKSize)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

func encodeKey(key []byte) string { return base64.RawURLEncoding.EncodeToString(key) }

func keyringJSON(current string, keys map[string][]byte) string {
	var builder strings.Builder
	builder.WriteString(`{"current":"`)
	builder.WriteString(current)
	builder.WriteString(`","keys":{`)
	first := true
	for kid, key := range keys {
		if !first {
			builder.WriteString(",")
		}
		first = false
		builder.WriteString(`"`)
		builder.WriteString(kid)
		builder.WriteString(`":"`)
		builder.WriteString(encodeKey(key))
		builder.WriteString(`"`)
	}
	builder.WriteString("}}")
	return builder.String()
}

func writeKeyring(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestFileProvider(t *testing.T, current string, keys map[string][]byte, reader *bytes.Reader) *FileProvider {
	t.Helper()
	path := writeKeyring(t, keyringJSON(current, keys))
	options := FileOptions{}
	if reader != nil {
		options.Reader = reader
	}
	provider, err := NewFileProvider(path, current, options)
	if err != nil {
		t.Fatalf("NewFileProvider: %v", err)
	}
	return provider
}

func TestNewFileProviderRejectsBadRings(t *testing.T) {
	key := testKey(0x01)
	valid := keyringJSON("kek-1", map[string][]byte{"kek-1": key})
	validPath := writeKeyring(t, valid)

	if _, err := NewFileProvider("", "kek-1", FileOptions{}); !errors.Is(err, ErrKeyring) {
		t.Fatalf("empty path error = %v", err)
	}
	if _, err := NewFileProvider(validPath, "", FileOptions{}); !errors.Is(err, ErrKeyring) {
		t.Fatalf("empty current error = %v", err)
	}
	if _, err := NewFileProvider(validPath, "bad kid", FileOptions{}); !errors.Is(err, ErrKeyring) {
		t.Fatalf("invalid current kid error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "absent.json")
	if _, err := NewFileProvider(missing, "kek-1", FileOptions{}); !errors.Is(err, ErrKeyring) {
		t.Fatalf("missing file error = %v", err)
	}
	if _, err := NewFileProvider(t.TempDir(), "kek-1", FileOptions{}); !errors.Is(err, ErrKeyring) {
		t.Fatalf("directory error = %v", err)
	}

	encoded := encodeKey(key)
	cases := map[string]string{
		"empty file":        "",
		"not an object":     "[]",
		"malformed":         "{",
		"extra field":       `{"current":"kek-1","keys":{"kek-1":"` + encoded + `"},"extra":1}`,
		"missing keys":      `{"current":"kek-1"}`,
		"missing current":   `{"keys":{"kek-1":"` + encoded + `"}}`,
		"empty current":     keyringJSON("", map[string][]byte{"kek-1": key}),
		"duplicate kid":     `{"current":"kek-1","keys":{"kek-1":"` + encoded + `","kek-1":"` + encoded + `"}}`,
		"short key":         keyringJSON("kek-1", map[string][]byte{"kek-1": make([]byte, 31)}),
		"long key":          keyringJSON("kek-1", map[string][]byte{"kek-1": make([]byte, 33)}),
		"padded key":        `{"current":"kek-1","keys":{"kek-1":"` + base64.URLEncoding.EncodeToString(key) + `"}}`,
		"non-canonical key": `{"current":"kek-1","keys":{"kek-1":"` + encoded[:len(encoded)-1] + `B"}}`,
		"invalid kid":       keyringJSON("bad kid", map[string][]byte{"bad kid": key}),
		"empty keys":        `{"current":"kek-1","keys":{}}`,
		"trailing data":     valid + "{}",
		"current absent":    keyringJSON("kek-1", map[string][]byte{"kek-2": key}),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeKeyring(t, content)
			if _, err := NewFileProvider(path, "kek-1", FileOptions{}); !errors.Is(err, ErrKeyring) {
				t.Fatalf("error = %v, want ErrKeyring", err)
			}
		})
	}
}

func TestNewFileProviderRejectsInsecurePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	path := writeKeyring(t, keyringJSON("kek-1", map[string][]byte{"kek-1": testKey(0x01)}))
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileProvider(path, "kek-1", FileOptions{}); !errors.Is(err, ErrKeyring) {
		t.Fatalf("insecure permissions error = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileProvider(path, "kek-1", FileOptions{}); err != nil {
		t.Fatalf("0600 keyring rejected: %v", err)
	}
}

func TestFileProviderWrapUnwrap(t *testing.T) {
	provider := newTestFileProvider(t, "kek-1", map[string][]byte{"kek-1": testKey(0x01)}, nil)
	dek := testKey(0x40)
	wrapped, err := provider.Wrap(context.Background(), dek, testWrapContext)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if wrapped.KID != "kek-1" || wrapped.Provider != secrets.ProviderLocalFile {
		t.Fatalf("wrapped metadata = %+v", wrapped)
	}
	if len(wrapped.Blob) != 1+secrets.NonceSize+secrets.DEKSize+secrets.TagSize {
		t.Fatalf("local blob length = %d", len(wrapped.Blob))
	}
	if wrapped.Blob[0] != 0x01 {
		t.Fatalf("local blob version = %#x", wrapped.Blob[0])
	}
	if bytes.Contains(wrapped.Blob, dek) {
		t.Fatal("wrapped blob contains the plaintext DEK")
	}

	unwrapped, err := provider.Unwrap(context.Background(), wrapped, testWrapContext)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if !bytes.Equal(unwrapped, dek) {
		t.Fatal("unwrapped DEK differs")
	}
	if provider.CurrentKID() != "kek-1" {
		t.Fatalf("CurrentKID = %q", provider.CurrentKID())
	}
}

func TestFileProviderWrapIsDeterministicForAnInjectedNonce(t *testing.T) {
	nonce := bytes.Repeat([]byte{0x77}, secrets.NonceSize)
	keys := map[string][]byte{"kek-1": testKey(0x01)}
	first := newTestFileProvider(t, "kek-1", keys, bytes.NewReader(nonce))
	second := newTestFileProvider(t, "kek-1", keys, bytes.NewReader(nonce))
	dek := testKey(0x40)
	one, err := first.Wrap(context.Background(), dek, testWrapContext)
	if err != nil {
		t.Fatal(err)
	}
	two, err := second.Wrap(context.Background(), dek, testWrapContext)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one.Blob, two.Blob) {
		t.Fatal("identical inputs produced different wrapped blobs")
	}
}

func TestFileProviderUnwrapRejections(t *testing.T) {
	provider := newTestFileProvider(t, "kek-1", map[string][]byte{"kek-1": testKey(0x01)}, nil)
	dek := testKey(0x40)
	wrapped, err := provider.Wrap(context.Background(), dek, testWrapContext)
	if err != nil {
		t.Fatal(err)
	}

	otherContext := testWrapContext
	otherContext.OrganizationID = [16]byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}
	if _, err := provider.Unwrap(context.Background(), wrapped, otherContext); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("cross-organization error = %v", err)
	}
	otherContext = testWrapContext
	otherContext.EndpointID = [16]byte{8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8}
	if _, err := provider.Unwrap(context.Background(), wrapped, otherContext); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("cross-endpoint error = %v", err)
	}

	unsupported := wrapped
	unsupported.Provider = secrets.ProviderAWSKMS
	if _, err := provider.Unwrap(context.Background(), unsupported, testWrapContext); !errors.Is(err, ErrUnsupportedProvider) {
		t.Fatalf("provider tag error = %v", err)
	}

	unknown := wrapped
	unknown.KID = "kek-9"
	if _, err := provider.Unwrap(context.Background(), unknown, testWrapContext); !errors.Is(err, ErrUnknownKID) {
		t.Fatalf("unknown kid error = %v", err)
	}

	invalid := wrapped
	invalid.KID = "bad kid"
	if _, err := provider.Unwrap(context.Background(), invalid, testWrapContext); !errors.Is(err, ErrInvalidWrappedKey) {
		t.Fatalf("invalid kid error = %v", err)
	}

	truncated := wrapped
	truncated.Blob = append([]byte(nil), wrapped.Blob[:len(wrapped.Blob)-1]...)
	if _, err := provider.Unwrap(context.Background(), truncated, testWrapContext); !errors.Is(err, ErrInvalidWrappedKey) {
		t.Fatalf("truncated blob error = %v", err)
	}

	tampered := wrapped
	tampered.Blob = append([]byte(nil), wrapped.Blob...)
	tampered.Blob[len(tampered.Blob)-1] ^= 0x01
	if _, err := provider.Unwrap(context.Background(), tampered, testWrapContext); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("tampered blob error = %v", err)
	}

	wrongKey := newTestFileProvider(t, "kek-1", map[string][]byte{"kek-1": testKey(0x50)}, nil)
	if _, err := wrongKey.Unwrap(context.Background(), wrapped, testWrapContext); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong KEK error = %v", err)
	}
}

func TestFileProviderContextCancellationIsTransient(t *testing.T) {
	provider := newTestFileProvider(t, "kek-1", map[string][]byte{"kek-1": testKey(0x01)}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Wrap(ctx, testKey(0x40), testWrapContext); !errors.Is(err, ErrTransient) {
		t.Fatalf("Wrap cancellation error = %v", err)
	}
	wrapped, err := provider.Wrap(context.Background(), testKey(0x40), testWrapContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Unwrap(ctx, wrapped, testWrapContext); !errors.Is(err, ErrTransient) {
		t.Fatalf("Unwrap cancellation error = %v", err)
	}
}

func TestFileProviderRewrapKeepsTheDataKey(t *testing.T) {
	keys := map[string][]byte{"kek-1": testKey(0x01), "kek-2": testKey(0x02)}
	ringPath := writeKeyring(t, keyringJSON("kek-2", keys))
	old, err := NewFileProvider(ringPath, "kek-1", FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := NewFileProvider(ringPath, "kek-2", FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dek := testKey(0x40)
	wrapped, err := old.Wrap(context.Background(), dek, testWrapContext)
	if err != nil {
		t.Fatal(err)
	}
	rewrapped, err := current.Rewrap(context.Background(), wrapped, testWrapContext)
	if err != nil {
		t.Fatalf("Rewrap: %v", err)
	}
	if rewrapped.KID != "kek-2" {
		t.Fatalf("rewrapped kid = %q", rewrapped.KID)
	}
	recovered, err := current.Unwrap(context.Background(), rewrapped, testWrapContext)
	if err != nil {
		t.Fatalf("unwrap rewrapped: %v", err)
	}
	if !bytes.Equal(recovered, dek) {
		t.Fatal("rewrap changed the data key")
	}
	if _, err := current.Unwrap(context.Background(), wrapped, testWrapContext); err != nil {
		t.Fatalf("old kid is no longer usable: %v", err)
	}
}

func TestFileProviderValidateKIDs(t *testing.T) {
	provider := newTestFileProvider(t, "kek-1", map[string][]byte{"kek-1": testKey(0x01), "kek-2": testKey(0x02)}, nil)
	if err := provider.ValidateKIDs([]string{"kek-1", "kek-2"}); err != nil {
		t.Fatalf("known kids rejected: %v", err)
	}
	if err := provider.ValidateKIDs([]string{"kek-3"}); !errors.Is(err, ErrKeyring) {
		t.Fatalf("unknown kid error = %v", err)
	}
	if err := provider.ValidateKIDs([]string{"bad kid"}); !errors.Is(err, ErrKeyring) {
		t.Fatalf("invalid kid error = %v", err)
	}
}

func TestProviderErrorsNeverCarryKeyMaterial(t *testing.T) {
	key := testKey(0x01)
	provider := newTestFileProvider(t, "kek-1", map[string][]byte{"kek-1": key}, nil)
	wrapped, err := provider.Wrap(context.Background(), testKey(0x40), testWrapContext)
	if err != nil {
		t.Fatal(err)
	}
	tampered := wrapped
	tampered.Blob = append([]byte(nil), wrapped.Blob...)
	tampered.Blob[len(tampered.Blob)-1] ^= 0x01
	_, err = provider.Unwrap(context.Background(), tampered, testWrapContext)
	if err == nil {
		t.Fatal("tampered blob accepted")
	}
	for _, leak := range []string{encodeKey(key), string(key), encodeKey(tampered.Blob), string(tampered.Blob)} {
		if leak != "" && strings.Contains(err.Error(), leak) {
			t.Fatal("provider error contains key material")
		}
	}
}

func TestNewMemoryProviderValidation(t *testing.T) {
	if _, err := NewMemoryProvider("", map[string][]byte{"kek-1": testKey(0x01)}, nil); !errors.Is(err, ErrKeyring) {
		t.Fatalf("empty current error = %v", err)
	}
	if _, err := NewMemoryProvider("bad kid", map[string][]byte{"bad kid": testKey(0x01)}, nil); !errors.Is(err, ErrKeyring) {
		t.Fatalf("invalid current error = %v", err)
	}
	if _, err := NewMemoryProvider("kek-1", map[string][]byte{"kek-1": make([]byte, 31)}, nil); !errors.Is(err, ErrKeyring) {
		t.Fatalf("short key error = %v", err)
	}
	if _, err := NewMemoryProvider("kek-1", map[string][]byte{"kek-2": testKey(0x02)}, nil); !errors.Is(err, ErrKeyring) {
		t.Fatalf("missing current key error = %v", err)
	}
	key := testKey(0x01)
	provider, err := NewMemoryProvider("kek-1", map[string][]byte{"kek-1": key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	clear(key)
	if _, err := provider.Wrap(context.Background(), testKey(0x40), testWrapContext); err != nil {
		t.Fatalf("provider retained the caller's key slice: %v", err)
	}
}

func TestMemoryProviderMatchesFileProviderSemantics(t *testing.T) {
	keys := map[string][]byte{"kek-1": testKey(0x01), "kek-2": testKey(0x02)}
	provider, err := NewMemoryProvider("kek-2", keys, bytes.NewReader(bytes.Repeat([]byte{0x55}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	dek := testKey(0x40)
	wrapped, err := provider.Wrap(context.Background(), dek, testWrapContext)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if wrapped.KID != "kek-2" || wrapped.Provider != secrets.ProviderLocalFile || len(wrapped.Blob) != 61 {
		t.Fatalf("wrapped = %+v", wrapped)
	}
	recovered, err := provider.Unwrap(context.Background(), wrapped, testWrapContext)
	if err != nil || !bytes.Equal(recovered, dek) {
		t.Fatalf("Unwrap = (%x, %v)", recovered, err)
	}

	other := testWrapContext
	other.EndpointID = [16]byte{7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7}
	if _, err := provider.Unwrap(context.Background(), wrapped, other); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("cross-endpoint error = %v", err)
	}

	tampered := wrapped
	tampered.Blob = append([]byte(nil), wrapped.Blob...)
	tampered.Blob[len(tampered.Blob)-1] ^= 0x01
	if _, err := provider.Unwrap(context.Background(), tampered, testWrapContext); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("tampered blob error = %v", err)
	}

	rewrapped, err := provider.Rewrap(context.Background(), wrapped, testWrapContext)
	if err != nil {
		t.Fatalf("Rewrap: %v", err)
	}
	if rewrapped.KID != "kek-2" {
		t.Fatalf("rewrapped kid = %q", rewrapped.KID)
	}
	recovered, err = provider.Unwrap(context.Background(), rewrapped, testWrapContext)
	if err != nil || !bytes.Equal(recovered, dek) {
		t.Fatalf("unwrap rewrapped = (%x, %v)", recovered, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Unwrap(ctx, wrapped, testWrapContext); !errors.Is(err, ErrTransient) {
		t.Fatalf("cancellation error = %v", err)
	}
}
