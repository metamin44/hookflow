# P6-OD-ETAG — Contract A endpoint validator

**Status:** implemented (Phase 6, 2026-11-24). This document records the binding validator contract; it approves
nothing else and changes no schema object. Code of record: `internal/modules/endpoint/etag.go` and
`internal/app/management/ifmatch.go`; black-box coverage in `test/integration/management_api_integration_test.go`.

## 1. Purpose and scope

Endpoint management needs optimistic concurrency that cannot silently lose an update, including updates whose only
effect is on the stored secret envelope. Contract A is a **strong, representation-derived ETag** returned by create
and GET, required on PATCH and secret rotation through `If-Match`. `DELETE` is intentionally unconditional: it soft
deletes the row after tenant and status checks and takes no validator. The tag is opaque to clients and must be echoed
verbatim (with its DQUOTEs).

It is not a capability, an authorization token, or stable across schema/encoding changes.

## 2. Header form

```text
ETag: "hf1-<base64url-without-padding(sha256)>"
```

- Prefix `hf1-` is the encoding version. A change to fields, ordering, domain separator or optional encoding
  requires `hf2-` and an API contract change; silently changing `hf1` is forbidden.
- `base64.RawURLEncoding` (no `=` padding), 43 characters of digest text.
- The header carries mandatory DQUOTEs. A bare tag or weak `W/"…"` tag never matches.

## 3. Digest input (length-prefixed)

SHA-256 over a byte stream built only from the public representation and the secret's stored ciphertext digest.
Every field is encoded as a big-endian `uint32` byte length followed by the exact bytes.

| Order | Field | Encoding |
| --- | --- | --- |
| 1 | Domain separator | literal `hookflow.etag.endpoint.v1` |
| 2 | `id` | representation UUID text |
| 3 | `name` | UTF-8 bytes |
| 4 | `description` | `0x00` when null; otherwise `0x01` then length-prefixed value |
| 5 | `url` | UTF-8 bytes |
| 6 | `status` | `active` or `paused` |
| 7 | `signing_scheme` | `hmac_sha256` |
| 8 | headers | `uint32` pair count, then every canonical name in sorted order, then every value in the same order |
| 9 | secret | raw 32 bytes of `SHA-256(secret_encrypted)` |

Canonical headers are lowercase with duplicate canonical names rejected at write time, so the pair count is exact and
unambiguous. The secret contributes only a digest: the envelope never leaves the database, and rotation changes the tag
because the stored ciphertext changes.

**Excluded by construction:** timestamps, `organization_id`, `deleted_at`, `timeout_ms`, `max_attempts`, audit data and
metrics. Two rows with equal public fields and equal stored envelopes therefore share a tag even if their row timestamps
differ.

## 4. `If-Match` evaluation

The header is parsed as a comma-separated list with `strings.TrimSpace` per candidate.

| Input | Result | HTTP |
| --- | --- | --- |
| Absent or empty after trim | missing precondition | `428 precondition_required` |
| `*` | unconditional write after the resource was located | proceeds (404 still wins) |
| Any strong tag equal to the current tag | match | proceeds |
| No strong match (stale list) | failed precondition | `412 precondition_failed` + current `ETag` response header |
| Weak `W/"…"` only | never matches | `412` (or `400` if the weak tag itself is malformed) |
| Malformed quoting, embedded space/tab/quote, empty list element | invalid syntax | `400 invalid_if_match` |

Evaluation order in the service is fixed: validate the request body (400) → open the transaction → lock the
organization row `FOR NO KEY UPDATE` → reject archived (403) → lock the endpoint (404 if absent/deleted) → suspended
field-presence policy (403) → `If-Match` (428/400/412) → no-op detection → ceiling (409) → update + audit + commit.
A stale tag therefore reports the current tag from a consistent, locked read.

## 5. Side effects and rotation

- A PATCH whose provided fields equal the stored state performs no `UPDATE`, writes no audit row, skips the ceiling and
  returns `200` with the unchanged `ETag`.
- Create returns `201` with the tag of the inserted row (including the new envelope digest), `Location`, and the
  `whsec_` secret once.
- Secret rotation validates `If-Match`, wraps the new secret **without a database lock**, then re-locks and re-checks
  status and `If-Match`. A concurrent change produces `412` with the current tag; the new envelope is discarded and
  neither the secret nor audit state changes. A successful rotation returns `200` with a new tag.
- DELETE performs no `If-Match` evaluation; it returns `204` after archival/tenant checks and sets `deleted_at`, so
  the tag of a deleted endpoint is no longer retrievable.

## 6. Verification

Integration coverage asserts: create/GET tag equality; missing `If-Match` 428; malformed 400; stale 412 with the current
header; weak-tag 412; no-op PATCH leaving tag, row and audit unchanged; rename changing the tag and audit metadata;
A→B→A reverting to the original tag; rotation changing the tag and discarding a conflicting wrap; and that the tag
changes when only the stored envelope changes. Unit tests cover the digest field encoding.

## 7. Non-goals

- No ETag on list responses; no `If-None-Match`/`304` caching contract.
- No multi-resource or collection validator.
- No requirement that a tag survive restore-from-backup byte changes; equality is defined over current stored values.
