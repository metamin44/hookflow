package endpoint

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"hash"
	"strings"
)

// ETagDomain is the immutable domain separator of the Contract A encoding.
const ETagDomain = "hookflow.etag.endpoint.v1"

// ETagPrefix is the version prefix. A field-set or encoding change requires a
// new prefix (hf2) and an API contract change.
const ETagPrefix = "hf1-"

// StrongTag computes the P6-OD-ETAG Contract A strong validator from the public
// representation and the stored secret envelope digest.
//
// Encoding: the domain separator and every field are length-prefixed with a
// big-endian uint32. Optional values carry a 0x00/0x01 presence byte. Headers
// are canonical (lowercase, sorted), encoded as a pair count followed by every
// name then every value; duplicate canonical names cannot occur because the
// write path rejects them. The secret contributes SHA-256(secret_encrypted).
// Timestamps, metrics, organization id, deleted_at, timeout_ms and
// max_attempts are excluded by construction.
func StrongTag(rep Representation, secretEncrypted string) string {
	digest := sha256.New()
	writeField(digest, []byte(ETagDomain))
	writeField(digest, []byte(rep.ID))
	writeField(digest, []byte(rep.Name))
	writeOptional(digest, rep.Description)
	writeField(digest, []byte(rep.URL))
	writeField(digest, []byte(rep.Status))
	writeField(digest, []byte(rep.SigningScheme))
	writeHeaders(digest, rep.HTTPHeaders)
	secretDigest := sha256.Sum256([]byte(secretEncrypted))
	writeField(digest, secretDigest[:])
	return ETagPrefix + base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

// QuotedHeader renders an ETag header value, including the mandatory DQUOTEs.
func QuotedHeader(tag string) string { return `"` + tag + `"` }

func writeField(w hash.Hash, value []byte) {
	var length [4]byte
	//nolint:gosec // Field lengths are bounded by the request limits (<= 16 KiB).
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = w.Write(length[:])
	_, _ = w.Write(value)
}

func writeOptional(w hash.Hash, value *string) {
	if value == nil {
		_, _ = w.Write([]byte{0x00})
		return
	}
	_, _ = w.Write([]byte{0x01})
	writeField(w, []byte(*value))
}

func writeHeaders(w hash.Hash, headers map[string]string) {
	normalized := make(map[string]string, len(headers))
	for name, value := range headers {
		normalized[strings.ToLower(name)] = value
	}
	names := SortedHeaderNames(normalized)
	var count [4]byte
	//nolint:gosec // Header count is capped at 32.
	binary.BigEndian.PutUint32(count[:], uint32(len(names)))
	_, _ = w.Write(count[:])
	for _, name := range names {
		writeField(w, []byte(name))
	}
	for _, name := range names {
		writeField(w, []byte(normalized[name]))
	}
}
