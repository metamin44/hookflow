package orgsubscription

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"hash"
	"time"
)

// ETagDomain is the immutable domain separator for the organization
// subscription representation.
const ETagDomain = "hookflow.etag.organization_subscription.v1"

// ETagPrefix is the version prefix. A field-set or encoding change requires a
// new prefix and an API contract change.
const ETagPrefix = "hf1-"

// StrongTag computes the strong validator for a stored period. Every exposed
// field participates, so a period, trial or status change always moves the tag.
func StrongTag(rep Representation) string {
	digest := sha256.New()
	writeString(digest, ETagDomain)
	writeString(digest, rep.ID)
	writeString(digest, rep.Plan)
	writeString(digest, rep.Status)
	writeTime(digest, rep.CurrentPeriodStart)
	writeTime(digest, rep.CurrentPeriodEnd)
	writeTime(digest, rep.TrialEndsAt)
	writeTime(digest, rep.CanceledAt)
	writeTimestamp(digest, rep.CreatedAt)
	writeTimestamp(digest, rep.UpdatedAt)
	return ETagPrefix + base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func writeString(w hash.Hash, value string) {
	var length [4]byte
	// #nosec G115 -- value length is bounded by API and schema field widths and fits in uint32.
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = w.Write(length[:])
	_, _ = w.Write([]byte(value))
}

func writeTime(w hash.Hash, value *time.Time) {
	if value == nil {
		_, _ = w.Write([]byte{0x00})
		return
	}
	_, _ = w.Write([]byte{0x01})
	writeTimestamp(w, *value)
}

func writeTimestamp(w hash.Hash, value time.Time) {
	utc := value.UTC()
	var encoded [16]byte
	// #nosec G115 -- full int64 Unix seconds range is intentionally encoded bit-for-bit as uint64.
	binary.BigEndian.PutUint64(encoded[:8], uint64(utc.Unix()))
	// #nosec G115 -- time.Time.Nanosecond is documented in [0, 999999999] and fits in uint64.
	binary.BigEndian.PutUint64(encoded[8:], uint64(utc.Nanosecond()))
	_, _ = w.Write(encoded[:])
}
