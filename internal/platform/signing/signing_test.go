package signing

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testDeliveryID = "22222222-2222-4222-8222-222222222222"
	testSecret     = "webhook-secret-0123456789"
)

func TestSignAtMatchesTheApprovedConstruction(t *testing.T) {
	secret := []byte(testSecret)
	body := []byte(`{"event":"delivery.created"}`)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	headers, err := SignAt(testDeliveryID, secret, body, now)
	if err != nil {
		t.Fatalf("SignAt: %v", err)
	}
	if headers.ID != testDeliveryID {
		t.Fatalf("id = %q", headers.ID)
	}
	wantTimestamp := "1795521600"
	if headers.Timestamp != wantTimestamp {
		t.Fatalf("timestamp = %q, want %q", headers.Timestamp, wantTimestamp)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(testDeliveryID + "." + wantTimestamp + "." + string(body)))
	wantSignature := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if headers.Signature != wantSignature {
		t.Fatalf("signature = %q, want %q", headers.Signature, wantSignature)
	}
	if err := Verify(secret, testDeliveryID, wantTimestamp, body, wantSignature); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestCanonicalInputIsExact(t *testing.T) {
	got := CanonicalInput("id", "123", []byte("body"))
	if string(got) != "id.123.body" {
		t.Fatalf("canonical input = %q", got)
	}
	if !bytes.Equal(got, []byte{'i', 'd', '.', '1', '2', '3', '.', 'b', 'o', 'd', 'y'}) {
		t.Fatalf("canonical input bytes = %x", got)
	}
}

func TestSignerUsesAFreshTimestampPerAttemptAndAStableID(t *testing.T) {
	secret := []byte(testSecret)
	body := []byte(`{"n":1}`)
	clock := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	signer := NewWithClock(func() time.Time { return clock })

	first, err := signer.Sign(testDeliveryID, secret, body)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Minute)
	second, err := signer.Sign(testDeliveryID, secret, body)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.ID != testDeliveryID {
		t.Fatalf("ids = %q/%q", first.ID, second.ID)
	}
	if first.Timestamp == second.Timestamp || first.Signature == second.Signature {
		t.Fatal("retry reused the original timestamp or signature")
	}
	if err := VerifyWithTolerance(secret, second.ID, second.Timestamp, body, second.Signature, clock, 5*time.Minute); err != nil {
		t.Fatalf("fresh signature rejected: %v", err)
	}
	if err := VerifyWithTolerance(secret, first.ID, first.Timestamp, body, first.Signature, clock, 5*time.Minute); !errors.Is(err, ErrInvalidTimestamp) {
		t.Fatalf("stale signature error = %v", err)
	}
}

func TestSignerKeyLengthBounds(t *testing.T) {
	if _, err := SignAt(testDeliveryID, []byte(strings.Repeat("x", MinKeyLength-1)), []byte("b"), time.Unix(1, 0)); !errors.Is(err, ErrKeyLength) {
		t.Fatalf("short key error = %v", err)
	}
	if _, err := SignAt(testDeliveryID, []byte(strings.Repeat("x", MaxKeyLength+1)), []byte("b"), time.Unix(1, 0)); !errors.Is(err, ErrKeyLength) {
		t.Fatalf("long key error = %v", err)
	}
	if _, err := SignAt(testDeliveryID, []byte(strings.Repeat("x", MinKeyLength)), []byte("b"), time.Unix(1, 0)); err != nil {
		t.Fatalf("minimum key rejected: %v", err)
	}
	if _, err := SignAt(testDeliveryID, []byte(strings.Repeat("x", MaxKeyLength)), []byte("b"), time.Unix(1, 0)); err != nil {
		t.Fatalf("maximum key rejected: %v", err)
	}
}

func TestSignerRejectsInvalidInputs(t *testing.T) {
	secret := []byte(testSecret)
	invalidIDs := []string{
		"",
		"22222222222242228222222222222222",
		"22222222-2222-4222-8222-22222222222",
		"22222222-2222-4222-8222-2222222222222",
		"22222222-2222-4222-8222-22222222222z",
		"22222222.2222-4222-8222-222222222222",
		"AAAAAAAA-2222-4222-8222-222222222222",
	}
	for _, id := range invalidIDs {
		if _, err := SignAt(id, secret, []byte("b"), time.Unix(1, 0)); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("id %q error = %v", id, err)
		}
	}
	if _, err := SignAt(testDeliveryID, secret, []byte("b"), time.Unix(-1, 0)); !errors.Is(err, ErrInvalidTimestamp) {
		t.Fatalf("negative timestamp error = %v", err)
	}
	if _, err := NewWithClock(nil).Sign(testDeliveryID, secret, []byte("b")); err != nil {
		t.Fatalf("nil clock did not fall back to the host clock: %v", err)
	}
	var uninitialized *Signer
	if _, err := uninitialized.Sign(testDeliveryID, secret, []byte("b")); err == nil {
		t.Fatal("nil signer accepted")
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	secret := []byte(testSecret)
	body := []byte(`{"event":"delivery.created"}`)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	headers, err := SignAt(testDeliveryID, secret, body, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(secret, testDeliveryID, headers.Timestamp, body, headers.Signature); err != nil {
		t.Fatalf("baseline verify: %v", err)
	}

	cases := []struct {
		name      string
		secret    []byte
		id        string
		timestamp string
		body      []byte
		header    string
		want      error
	}{
		{"body", secret, testDeliveryID, headers.Timestamp, []byte(`{"event":"delivery.deleted"}`), headers.Signature, ErrInvalidSignature},
		{"id", secret, "33333333-3333-4333-8333-333333333333", headers.Timestamp, body, headers.Signature, ErrInvalidSignature},
		{"timestamp", secret, testDeliveryID, "1700000001", body, headers.Signature, ErrInvalidSignature},
		{"secret", []byte("another-secret-0123456789"), testDeliveryID, headers.Timestamp, body, headers.Signature, ErrInvalidSignature},
		{"prefix", secret, testDeliveryID, headers.Timestamp, body, "v2,AAAA", ErrInvalidSignature},
		{"no prefix", secret, testDeliveryID, headers.Timestamp, body, strings.TrimPrefix(headers.Signature, "v1,"), ErrInvalidSignature},
		{"two commas", secret, testDeliveryID, headers.Timestamp, body, headers.Signature + ",v1", ErrInvalidSignature},
		{"bad base64", secret, testDeliveryID, headers.Timestamp, body, "v1,!!!!", ErrInvalidSignature},
		{"short mac", secret, testDeliveryID, headers.Timestamp, body, "v1,AAAA", ErrInvalidSignature},
		{"empty header", secret, testDeliveryID, headers.Timestamp, body, "", ErrInvalidSignature},
		{"key too short", []byte("short"), testDeliveryID, headers.Timestamp, body, headers.Signature, ErrKeyLength},
		{"id invalid", secret, "bad", headers.Timestamp, body, headers.Signature, ErrInvalidID},
		{"timestamp empty", secret, testDeliveryID, "", body, headers.Signature, ErrInvalidTimestamp},
		{"timestamp leading zero", secret, testDeliveryID, "01700000000", body, headers.Signature, ErrInvalidTimestamp},
		{"timestamp non-digit", secret, testDeliveryID, "17e8", body, headers.Signature, ErrInvalidTimestamp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Verify(tc.secret, tc.id, tc.timestamp, tc.body, tc.header); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifyWithToleranceBoundaries(t *testing.T) {
	secret := []byte(testSecret)
	body := []byte(`{}`)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	headers, err := SignAt(testDeliveryID, secret, body, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyWithTolerance(secret, testDeliveryID, headers.Timestamp, body, headers.Signature, now.Add(5*time.Minute), 5*time.Minute); err != nil {
		t.Fatalf("exact tolerance rejected: %v", err)
	}
	if err := VerifyWithTolerance(secret, testDeliveryID, headers.Timestamp, body, headers.Signature, now.Add(-5*time.Minute), 5*time.Minute); err != nil {
		t.Fatalf("negative exact tolerance rejected: %v", err)
	}
	if err := VerifyWithTolerance(secret, testDeliveryID, headers.Timestamp, body, headers.Signature, now.Add(5*time.Minute+time.Second), 5*time.Minute); !errors.Is(err, ErrInvalidTimestamp) {
		t.Fatalf("future skew error = %v", err)
	}
	if err := VerifyWithTolerance(secret, testDeliveryID, headers.Timestamp, body, headers.Signature, now.Add(-5*time.Minute-time.Second), 5*time.Minute); !errors.Is(err, ErrInvalidTimestamp) {
		t.Fatalf("past skew error = %v", err)
	}
}
