package secrets

import (
	"errors"
	"strings"
	"testing"
)

const (
	testOrganizationUUID = "11111111-1111-4111-8111-111111111111"
	testEndpointUUID     = "66666666-6666-4666-8666-666666666666"
)

func TestParseUUID(t *testing.T) {
	organization, err := ParseUUID(testOrganizationUUID)
	if err != nil {
		t.Fatalf("canonical UUID rejected: %v", err)
	}
	if organization[0] != 0x11 || organization[15] != 0x11 {
		t.Fatalf("decoded UUID = %x", organization)
	}
	endpoint, err := ParseUUID(testEndpointUUID)
	if err != nil {
		t.Fatalf("canonical UUID rejected: %v", err)
	}
	if organization == endpoint {
		t.Fatal("distinct identities collided")
	}

	invalid := []string{
		"",
		"11111111111141118111111111111111",
		"{11111111-1111-4111-8111-111111111111}",
		"urn:uuid:11111111-1111-4111-8111-111111111111",
		"11111111-1111-4111-8111-11111111111",
		"11111111-1111-4111-8111-1111111111111",
		"11111111_1111-4111-8111-111111111111",
		"g1111111-1111-4111-8111-111111111111",
		" 1111111-1111-4111-8111-111111111111",
	}
	for _, value := range invalid {
		if _, err := ParseUUID(value); err == nil {
			t.Fatalf("invalid UUID %q accepted", value)
		}
	}
}

func TestBuildAADLayout(t *testing.T) {
	organization := UUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	endpoint := UUID{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	nonce := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	aad, err := BuildAAD(AlgorithmAES256GCM, PurposeWebhookSecret, nonce, organization, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(aad) != 64 {
		t.Fatalf("aad length = %d", len(aad))
	}
	if string(aad[0:18]) != AADDomain {
		t.Fatalf("aad domain = %q", aad[0:18])
	}
	if aad[18] != AlgorithmAES256GCM || aad[19] != PurposeWebhookSecret {
		t.Fatalf("algorithm/purpose = %#x/%#x", aad[18], aad[19])
	}
	for i := 0; i < NonceSize; i++ {
		if aad[20+i] != nonce[i] {
			t.Fatalf("nonce byte %d = %#x", i, aad[20+i])
		}
	}
	if [16]byte(aad[32:48]) != [16]byte(organization) {
		t.Fatal("organization bytes are not bound at offset 32")
	}
	if [16]byte(aad[48:64]) != [16]byte(endpoint) {
		t.Fatal("endpoint bytes are not bound at offset 48")
	}
}

func TestBuildAADRejectsUnsupportedInputs(t *testing.T) {
	organization := UUID{}
	endpoint := UUID{}
	nonce := make([]byte, NonceSize)
	if _, err := BuildAAD(0x02, PurposeWebhookSecret, nonce, organization, endpoint); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("algorithm error = %v", err)
	}
	if _, err := BuildAAD(AlgorithmAES256GCM, 0x02, nonce, organization, endpoint); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("purpose error = %v", err)
	}
	if _, err := BuildAAD(AlgorithmAES256GCM, PurposeWebhookSecret, nonce[:11], organization, endpoint); !errors.Is(err, ErrMalformed) {
		t.Fatalf("nonce error = %v", err)
	}
}

func TestBuildKEKWrapAADLayout(t *testing.T) {
	organization := UUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	endpoint := UUID{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	aad := BuildKEKWrapAAD(organization, endpoint)
	if len(aad) != 51 {
		t.Fatalf("kek aad length = %d", len(aad))
	}
	if string(aad[0:19]) != KEKWrapDomain {
		t.Fatalf("kek domain = %q", aad[0:19])
	}
	if [16]byte(aad[19:35]) != [16]byte(organization) || [16]byte(aad[35:51]) != [16]byte(endpoint) {
		t.Fatal("tenant identities are not bound in the wrap context")
	}
	if strings.Contains(AADDomain, KEKWrapDomain) || strings.Contains(KEKWrapDomain, AADDomain) {
		t.Fatal("domain tags overlap")
	}
}
