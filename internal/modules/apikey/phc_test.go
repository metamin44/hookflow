package apikey

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func fixedToken() string { return strings.Repeat("A", TokenLength-1) + "Q" }

func TestHashVerifyRoundTrip(t *testing.T) {
	token := fixedToken()
	encoded, err := HashWithReader(token, strings.NewReader(strings.Repeat("s", ArgonSaltSize)))
	if err != nil {
		t.Fatalf("HashWithReader() error: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC prefix: %q", encoded)
	}
	if len(encoded) > MaxPHCLength {
		t.Fatalf("PHC length = %d, want <= %d", len(encoded), MaxPHCLength)
	}
	verifier := NewVerifier()
	if err := verifier.Verify(context.Background(), token, encoded); err != nil {
		t.Fatalf("Verify() error: %v", err)
	}
	other := strings.Repeat("B", TokenLength-1) + "Q"
	if err := verifier.Verify(context.Background(), other, encoded); !errors.Is(err, ErrMismatch) {
		t.Fatalf("Verify(other) error = %v, want ErrMismatch", err)
	}
}

func TestVerifyRejectsMalformedVerifiers(t *testing.T) {
	token := fixedToken()
	salt := base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("s", ArgonSaltSize)))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, ArgonKeySize))
	cases := map[string]string{
		"empty":            "",
		"too long":         strings.Repeat("$", MaxPHCLength+1),
		"wrong algorithm":  "$argon2i$v=19$m=19456,t=2,p=1$" + salt + "$" + key,
		"wrong version":    "$argon2id$v=18$m=19456,t=2,p=1$" + salt + "$" + key,
		"memory low":       "$argon2id$v=19$m=19455,t=2,p=1$" + salt + "$" + key,
		"memory high":      "$argon2id$v=19$m=32769,t=2,p=1$" + salt + "$" + key,
		"time low":         "$argon2id$v=19$m=19456,t=1,p=1$" + salt + "$" + key,
		"time high":        "$argon2id$v=19$m=19456,t=5,p=1$" + salt + "$" + key,
		"threads zero":     "$argon2id$v=19$m=19456,t=2,p=0$" + salt + "$" + key,
		"threads high":     "$argon2id$v=19$m=19456,t=2,p=3$" + salt + "$" + key,
		"short salt":       "$argon2id$v=19$m=19456,t=2,p=1$" + salt[:20] + "$" + key,
		"short key":        "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$" + key[:40],
		"padded base64":    "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "=$" + key,
		"missing fields":   "$argon2id$v=19$m=19456,t=2$" + salt + "$" + key,
		"reordered params": "$argon2id$v=19$t=2,m=19456,p=1$" + salt + "$" + key,
	}
	verifier := NewVerifier()
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			err := verifier.Verify(context.Background(), token, encoded)
			if !errors.Is(err, ErrInvalidVerifier) {
				t.Fatalf("Verify() error = %v, want ErrInvalidVerifier", err)
			}
		})
	}
}

func TestVerifyRejectsMalformedTokensWithoutDerivation(t *testing.T) {
	verifier := NewVerifier()
	err := verifier.Verify(context.Background(), "short", "$argon2id$v=19$m=19456,t=2,p=1$AAAA$AAAA")
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("Verify(short token) error = %v, want ErrMismatch", err)
	}
}

func TestDummyVerifyAlwaysSucceeds(t *testing.T) {
	verifier := NewVerifier()
	if err := verifier.DummyVerify(context.Background(), "unknown-prefix-probe"); err != nil {
		t.Fatalf("DummyVerify() error: %v", err)
	}
}

func TestVerifyRespectsCancelledContext(t *testing.T) {
	token := fixedToken()
	encoded, err := HashWithReader(token, strings.NewReader(strings.Repeat("s", ArgonSaltSize)))
	if err != nil {
		t.Fatalf("HashWithReader() error: %v", err)
	}
	verifier := NewVerifier()
	for i := 0; i < MaxConcurrent; i++ {
		verifier.sem <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = verifier.Verify(ctx, token, encoded)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify() error = %v, want context.Canceled", err)
	}
	for i := 0; i < MaxConcurrent; i++ {
		<-verifier.sem
	}
}
