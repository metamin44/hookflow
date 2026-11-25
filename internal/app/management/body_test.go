package management

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeBody(t *testing.T, body string) (map[string]json.RawMessage, *HTTPError) {
	t.Helper()
	request := httptest.NewRequest("POST", "/v1/organizations/x/endpoints", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	return decodeObject(recorder, request)
}

func TestDecodeObject(t *testing.T) {
	if _, appErr := decodeBody(t, `{"name":"a"}`); appErr != nil {
		t.Fatalf("decodeObject(valid) = %v", appErr)
	}
	cases := map[string]struct {
		body   string
		status int
		code   string
	}{
		"empty":     {"", 400, "invalid_body"},
		"array":     {`[]`, 400, "invalid_body"},
		"string":    {`"x"`, 400, "invalid_body"},
		"null":      {`null`, 400, "invalid_body"},
		"malformed": {`{"name":}`, 400, "invalid_body"},
		"trailing":  {`{"name":"a"} extra`, 400, "invalid_body"},
		"too large": {`{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, 413, "payload_too_large"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, appErr := decodeBody(t, testCase.body)
			if appErr == nil {
				t.Fatalf("decodeObject(%s) = nil, want %s", name, testCase.code)
			}
			if appErr.Status != testCase.status || appErr.Code != testCase.code {
				t.Fatalf("decodeObject(%s) = %d/%s, want %d/%s",
					name, appErr.Status, appErr.Code, testCase.status, testCase.code)
			}
		})
	}
}

func TestDecodeCreate(t *testing.T) {
	document := mustDecode(t, `{
		"name": "billing",
		"description": null,
		"url": "https://example.test/hook",
		"http_headers": {"X-Trace": "abc"}
	}`)
	decoded, appErr := decodeCreate(document)
	if appErr != nil {
		t.Fatalf("decodeCreate() = %v", appErr)
	}
	if decoded.Name != "billing" || decoded.Description != nil || decoded.URL != "https://example.test/hook" {
		t.Fatalf("decodeCreate() = %+v", decoded)
	}
	if decoded.Headers["X-Trace"] != "abc" {
		t.Fatalf("decodeCreate() headers = %v", decoded.Headers)
	}
}

func TestDecodeCreateDefaultsHeaders(t *testing.T) {
	document := mustDecode(t, `{"name":"a","url":"https://example.test/hook"}`)
	decoded, appErr := decodeCreate(document)
	if appErr != nil {
		t.Fatalf("decodeCreate() = %v", appErr)
	}
	if decoded.Headers == nil || len(decoded.Headers) != 0 {
		t.Fatalf("decodeCreate() headers = %v, want empty map", decoded.Headers)
	}
}

func TestDecodeCreateRejectsFields(t *testing.T) {
	cases := map[string]string{
		"unknown":            `{"name":"a","url":"https://example.test/h","colour":"red"}`,
		"signing_scheme":     `{"name":"a","url":"https://example.test/h","signing_scheme":"hmac_sha256"}`,
		"timeout_ms":         `{"name":"a","url":"https://example.test/h","timeout_ms":5000}`,
		"max_attempts":       `{"name":"a","url":"https://example.test/h","max_attempts":8}`,
		"secret":             `{"name":"a","url":"https://example.test/h","secret":"x"}`,
		"organization_id":    `{"name":"a","url":"https://example.test/h","organization_id":"x"}`,
		"status on create":   `{"name":"a","url":"https://example.test/h","status":"active"}`,
		"missing name":       `{"url":"https://example.test/h"}`,
		"missing url":        `{"name":"a"}`,
		"null name":          `{"name":null,"url":"https://example.test/h"}`,
		"non-string url":     `{"name":"a","url":42}`,
		"headers not object": `{"name":"a","url":"https://example.test/h","http_headers":[]}`,
		"headers null":       `{"name":"a","url":"https://example.test/h","http_headers":null}`,
		"header non-string":  `{"name":"a","url":"https://example.test/h","http_headers":{"x":1}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, appErr := decodeCreate(mustDecode(t, body))
			if appErr == nil {
				t.Fatal("decodeCreate() = nil, want a field rejection")
			}
			if appErr.Status != 400 {
				t.Fatalf("decodeCreate() status = %d, want 400", appErr.Status)
			}
		})
	}
}

func TestDecodePatchPresence(t *testing.T) {
	document := mustDecode(t, `{"description": null}`)
	decoded, appErr := decodePatch(document)
	if appErr != nil {
		t.Fatalf("decodePatch() = %v", appErr)
	}
	if !decoded.DescriptionSet || decoded.Description != nil {
		t.Fatalf("decodePatch() = %+v, want an explicit null description", decoded)
	}

	absent := mustDecode(t, `{"name":"x"}`)
	decoded, appErr = decodePatch(absent)
	if appErr != nil {
		t.Fatalf("decodePatch() = %v", appErr)
	}
	if decoded.DescriptionSet {
		t.Fatal("decodePatch() marked an absent description as present")
	}
}

func TestDecodePatchRejects(t *testing.T) {
	cases := map[string]string{
		"empty":          `{}`,
		"unknown":        `{"colour":"red"}`,
		"signing_scheme": `{"signing_scheme":"hmac_sha256"}`,
		"timeout_ms":     `{"timeout_ms":5000}`,
		"max_attempts":   `{"max_attempts":8}`,
		"id":             `{"id":"0198c0de-0000-7000-8000-000000000001"}`,
		"secret":         `{"secret":"x"}`,
		"null status":    `{"status":null}`,
		"null url":       `{"url":null}`,
		"null name":      `{"name":null}`,
		"null headers":   `{"http_headers":null}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, appErr := decodePatch(mustDecode(t, body))
			if appErr == nil {
				t.Fatal("decodePatch() = nil, want a field rejection")
			}
			if appErr.Status != 400 {
				t.Fatalf("decodePatch() status = %d, want 400", appErr.Status)
			}
		})
	}
}

func mustDecode(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	document, appErr := decodeBody(t, body)
	if appErr != nil {
		t.Fatalf("decodeObject(%s) = %v", body, appErr)
	}
	return document
}
