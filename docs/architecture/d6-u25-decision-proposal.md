# HookFlow — D6 and U25 Architecture Decision Proposal

**Document type:** decision package (contract record after Owner approval)
**Status:** **OWNER APPROVED (architecture, 2026-11-24)** — all eleven rows D6-A…D6-G and U25-A…U25-D. Development implementation: **IMPLEMENTED** (Phase 6/6.1, subject to the evidence recorded in `overview.md` §16.9 and the Phase 6.1 closure report). Production KMS/Vault provider and production readiness: **NOT IMPLEMENTED / NOT READY**. Every pre-approval "NOT APPROVED" label retained below is a historical record of Rev 2, not the current status.
**Date:** 2026-11-24
**Revision:** Rev 3 (2026-11-24, Phase 6.1) — status reconciliation only: records the Owner's approval of every decision row, the delivered development runtime, and the absent production provider. It changes no contract value, byte layout, recommendation or consequence. Rev 2 (2026-11-24) was the finalization pass, followed by the same-day cross-document reconciliation: it corrected the pre-HTTP failure model to preserve U18, gated unsigned development delivery, separated provider startup validation from per-envelope failure classification, replaced calendar-based KEK retirement with a reference-based rule using the already-agreed 90/90/72 retention facts, and stated the lease invariant including its 3 s slack. Rev 1 text is preserved except where a later revision corrects it. **Rev 2 approved nothing; the Owner approval is recorded separately in §8.**
**Scope:** D6 (secret encryption, envelope format, key custody, rotation) and U25 (outbound HTTP request signing and verification contract)
**Repository:** `github.com/metamin44/hookflow`, branch `main`, HEAD `c6db32fd50ee8d7beb3108674b4cf46443d2aba4`

This package originally proposed contracts and modified no application code, tests, `go.mod`/`go.sum`, migrations, schema, `.env` files, Compose, CI, or runtime configuration. No proposal here was approved when Rev 2 was written; the Owner approved all eleven rows on 2026-11-24, and the development implementation followed. Approval of the architecture does not by itself implement the production custody path.

Statuses are reported on three separate surfaces and are never inferred from one another:

| Surface | Status |
|---|---|
| D6/U25 architecture (D6-A…D6-G, U25-A…U25-D) | **OWNER APPROVED** (2026-11-24) |
| D6/U25 development implementation | **IMPLEMENTED** (subject to the Phase 6.1 verification evidence) |
| Production KMS/Vault custody and production readiness | **NOT IMPLEMENTED / NOT READY** |

The four pre-existing runtime labels are preserved and are not re-graded by this document:

| Surface | Status |
|---|---|
| Runtime Dev | ACCEPTED |
| Local Closure | CLOSED-WITH-ADVISORY |
| Hosted CI | NOT VERIFIED |
| Production | NOT READY |

**Historical Rev 2 status (superseded for current status):** D6 and U25 were unapproved and unimplemented, consistent with `docs/architecture/runtime-readiness-decisions.md` (§preamble, §8, §11) and `docs/architecture/overview.md` (§6, §8). At that time a non-empty `secret_encrypted` failed closed with no HTTP request, and empty-secret snapshots executed HTTP unconditionally because the repository had no deployment-mode concept at all (§3.6). The proposal required that unsigned path to be confined to an explicitly gated Development configuration and to fail closed everywhere else (D6-F); that development gate now exists.

> **Status note (2026-11-24, Phase 6.1; supersedes the Rev 2 "every row remains NOT APPROVED" wording).** The Owner
> approved D6-A through D6-G and U25-A through U25-D as the architecture contract on 2026-11-24. A development-only
> runtime implements the recommendations: local file KEK provider, `hfse1.` envelope codec/AAD builder, HMAC-SHA256
> Standard Webhooks v1 signing, and the D6-F deployment-mode plus unsigned-delivery gate. That implementation does not
> provide a KMS/Vault or any other production provider; production activation and production readiness are **NOT
> IMPLEMENTED / NOT READY**. Sentences above that describe "no deployment-mode concept at all" and an absent AAD
> implementation are retained as the pre-implementation record; see overview §16.8–§16.9 and the Phase 6.1 closure
> report for current evidence. Earlier status notes in this document are historical.

---

## 1. Executive summary

The repository already contains the correct structural placeholders for D6 and U25 but no cryptography. The two contracts recommended below are deliberately small, provider-neutral, and compatible with the existing approved Bundle 1 behavior (B5 decrypt-failure handling, A8 snapshot semantics, the fixed lease budget, and at-least-once delivery).

**D6 (recommended contract).** AES-256-GCM with a fresh 32-byte data encryption key (DEK) per endpoint secret version; a self-describing ASCII envelope `hfse1.<base64url>` carrying magic, version, algorithm, purpose, key identifier, provider tag, a variable-length provider-wrapped DEK, a 96-bit data nonce, ciphertext, and a 128-bit tag. The authenticated data binds only immutable semantic fields plus the organization and endpoint UUIDs, so that rewrapping the DEK under a rotated key encryption key (KEK) does not invalidate the payload tag. The KEK is never stored in the database, source, images, logs, or ordinary configuration; a provider interface isolates wrap/unwrap, with a development-only file provider and cloud KMS/Vault adapters left as deployment choices.

**U25 (recommended contract).** Standard Webhooks v1 construction: HMAC-SHA256 over `webhook-id + "." + webhook-timestamp + "." + body-bytes`, with headers `webhook-id`, `webhook-timestamp`, and `webhook-signature: v1,<base64>`. No key identifier, KEK identifier, DEK identifier, or provider name is transmitted. Timestamps are fresh per attempt with a recommended ±300 s receiver tolerance; `webhook-id` is the stable delivery id and carries the idempotency signal. Delivery remains at-least-once; nothing here claims exactly-once.

**Rev 2 corrections that constrain both contracts.**

- **U18 is preserved.** A failure before `client.Do` is not an HTTP attempt: it creates no `delivery_attempts` row, does not increment `attempt_count`, and never causes an unsigned request. This includes a signing-construction programming fault, which must not be recorded as an HTTP attempt or confused with a secret-decryption failure. Whether the existing durable B5 mechanics may be generalized to carry it is a new Owner decision, D6-G.
- **Unsigned delivery is gated.** An empty secret may use the unsigned path only when an explicit Development-only gate is enabled. Production, and any ambiguous configuration, fails closed with no HTTP. The gate is new configuration, not something the repository already has, so it is proposed as D6-F rather than assumed.
- **Provider misconfiguration is a startup failure.** An invalid configured provider, allowlist, key file, or current `kid` stops the process at startup. Only the provider tag inside an individual stored envelope is classified per delivery (§4.5.1).
- **KEK retirement is reference-based.** The documented retention is 90 days for `events` and `deliveries` and 72 hours for idempotency records (`database-schema.md` §6). Those are horizons, not permission to delete a KEK: a version stays decryptable while any retained endpoint envelope or delivery snapshot references it, and retirement requires a completed purge plus a bounded reference audit (§4.4).
- **The lease invariant is exact.** `SecretResolutionBudget` is approved at 2 s; `30 s > 10 + 2 + 3 + 10 + 2 = 27 s`; the residual 3 s is slack, not a sixth component. The Worker re-samples time after secret resolution instead of trusting a frozen remainder (§4.7).

**Owner decisions (approved).** Eleven grouped choices were put to the Owner: D6-A algorithm/envelope, D6-B DEK scope/AAD, D6-C custody/provider boundary and startup validation, D6-D reference-based rotation and retirement, D6-E pre-HTTP budget amendment, D6-F Development-only unsigned-delivery gate, D6-G pre-HTTP internal-failure disposition, U25-A algorithm/key usage, U25-B canonical input and headers, U25-C timestamp/replay/receiver compatibility, U25-D signing-key rotation. §8 states each with a recommended default, alternatives, and consequences, followed by the approval table. The Rev 2 text "every row is marked NOT APPROVED" was the pre-approval state; the Owner approved all eleven rows on 2026-11-24.

**Implementation impact.** No database migration is required. The work is Go-only: a codec package, a KEK provider package, a signing package, `endpoint_id` propagation into the claim/preparation structs and two SQL projections, Worker orchestration and failure classification, one new budget value, a deployment-mode value plus the unsigned gate, provider startup validation, a read-only reference-audit procedure for retirement, and tests. Nothing is implemented here.

---

## 2. Method, evidence classes, and limits

Repository inspection covered the migrations and the actual Go implementation, not only prose:

- `db/migrations/0005_endpoints.up.sql`, `0008_deliveries.up.sql`, `0009_delivery_attempts.up.sql`, `0007_events.up.sql`
- `internal/app/delivery/store.go`, `pointer.go`
- `internal/app/worker/processor.go`, `executor.go`, `handle.go`, `retry.go`
- `internal/app/disposition/policy.go`
- `internal/platform/config/config.go`, `runtime.go`
- `internal/platform/messaging/message.go`
- `cmd/api`, `cmd/worker`, `test/integration/worker_integration_test.go`, `internal/app/worker/executor_test.go`
- `docs/architecture/overview.md`, `database-schema.md`, `runtime-readiness-decisions.md`, `docs/operations/*`

Every statement below is labelled in one of four ways:

- **[V]** verified standard or library behavior, with a primary source in §10.
- **[R]** repository finding, with a file path and symbol or line.
- **[E]** engineering recommendation made by this proposal; not verified fact, not approved.
- **[O]** unresolved Owner decision (§8).

Where a source could not be read in full, §10 says so rather than filling the gap. No standard, API, library behavior, or security guarantee is invented. No dependency is added to `go.mod`; the recommendation is to use only the Go standard library (`crypto/aes`, `crypto/cipher`, `crypto/hmac`, `crypto/rand`, `crypto/sha256`, `encoding/base64`), which is already available under Go 1.27.4.

Limits of this package:

- No endpoint-management API exists, so the write path (where a secret is generated, encrypted, and stored) is specified as a contract but cannot be exercised yet.
- No production deployment target is named, so the production KEK provider cannot be selected here.
- Retention is documented, not implemented. `database-schema.md` §6 fixes 90 days for `events` (by `received_at`), 90 days for `deliveries` (by `created_at`, removed with the parent event), 90 days inherited for `delivery_attempts` (purged before `deliveries`), and 72 hours for `idempotency_records`; `overview.md` restates the 90-day and 72-hour windows. The repository contains **no** Events/Deliveries/Attempts purge job, and §6 leaves purge table coverage and batch sizes open (U2). D6-D therefore depends on a reference audit, not on a calendar duration, and §4.4 states exactly what cannot be claimed safe today.
- No deployment-mode or environment-name variable exists in `internal/platform/config`; the unsigned gate of D6-F is new configuration, and its consequences are proposed rather than discovered.
- `delivery_attempts` records only completed HTTP executions (U18); there is no approved way to persist a pre-HTTP internal fault as an attempt, and this revision does not invent one (§4.7, D6-G).
- The NIST SP 800-38D PDF could not be read (binary response), so GCM limits are cited through Go's documentation and Vault's explicit restatement of the same bound (§10).

---

## 3. Repository findings

### 3.1 Schema is already compatible with an envelope

**[R]** `db/migrations/0005_endpoints.up.sql:10-11` declares:

```sql
secret_encrypted text        NOT NULL,
signing_scheme   text        NOT NULL DEFAULT 'hmac_sha256',
```

Neither column has a length check, a format check, or an enumerated `signing_scheme` check. A `text` column can store the ASCII envelope described in §4.1 without migration. `signing_scheme` exists but is unconstrained, so an unsupported value can only be rejected in code.

**[R]** `db/migrations/0008_deliveries.up.sql:2,11,15` confirms snapshot semantics and that the snapshot column has the same type:

```sql
-- Snapshot payload fields url/http_headers/secret_encrypted are immutable after acceptance.
endpoint_id               uuid        NOT NULL,
secret_encrypted          text        NOT NULL,
```

`endpoint_id` is already present and non-null on every delivery, which is what the authenticated data needs (§4.3). There is **no** `signing_scheme` column on `deliveries`: a delivery does not snapshot the scheme, only the secret. This constrains U25 versioning (§6.5).

**[R]** `db/migrations/0009_delivery_attempts.up.sql` allows `error_class` only `('timeout','connect','tls','dns','http_status','internal')` and allows `request_headers jsonb`. Cryptographic failure classes cannot be persisted as new values without a migration; any attempt row produced after cryptography succeeds must use `internal` for internal faults.

### 3.2 The Go implementation has no cryptography, and fails closed

**[R]** `internal/app/worker/executor.go:158-163` is the current guard:

```go
if preparation.SecretCiphertext != "" {
    outcome.Kind = delivery.OutcomeTerminalFailure
    outcome.ErrorClass = "internal"
    outcome.ErrorMessage = ErrSecretUnavailable.Error()
    return outcome, ErrSecretUnavailable
}
```

This executes before URL validation, header parsing, request construction, and `client.Do`. It is the property that must be preserved: a configured secret can never produce an unsigned request.

**[R]** `internal/app/worker/executor.go:213-220` sets the current outbound headers and prevents transport replay:

```go
request.Header = headers
request.Header.Set("Content-Type", "application/json")
request.Header.Set("X-HookFlow-Delivery-Id", preparation.DeliveryID)
request.Header.Set("X-HookFlow-Event-Id", preparation.EventID)
request.Header.Set("X-HookFlow-Dispatch-Sequence", strconv.Itoa(preparation.DispatchSeq))
request.GetBody = nil
```

No signature header exists. Both the integration test (`test/integration/worker_integration_test.go:100-103`) and the unit test (`internal/app/worker/executor_test.go:192-202`) explicitly assert that `X-HookFlow-Signature` is empty. These assertions are the current regression surface and must be inverted when U25 is implemented, not before.

**[R]** `internal/app/worker/processor.go:112-130` maps `ErrSecretUnavailable` to a metric, computes deterministic pacing, calls `store.RecordDecryptFailure`, and settles the broker message according to `AckEligible`. This is the approved B5 path and must remain the single route for secret-resolution failures.

**[R]** `internal/app/delivery/store.go:472-579` (`RecordDecryptFailure`) validates the claim, locks the row, fences on the lease token, samples fresh database time, applies event-horizon precedence, then lease expiry, then a paced `next_attempt_at`, builds a retry outbox entry only when the event is still live, clears the lease, bumps `dispatch_seq`, and commits before returning `AckEligible: true`. It writes **no** `delivery_attempts` row and does **not** increment `attempt_count`.

**[R]** `internal/app/disposition/policy.go` (package `disposition`) has no NACK action; transient failures requeue, uncertain outcomes hold, and acknowledgement requires durable state.

### 3.3 `endpoint_id` is missing exactly where the AAD needs it

**[R]** `internal/app/delivery/store.go:103-119` — `Claim` carries `OrganizationID`, `DeliveryID`, `EventID`, and `SecretCiphertext`, but no `EndpointID`.

**[R]** `internal/app/delivery/pointer.go:386-400` — `HTTPPreparation` likewise has no `EndpointID`.

**[R]** `internal/app/delivery/pointer.go:88` and `:419` — both SQL projections select `d.url, d.http_headers::text, d.secret_encrypted` but not `d.endpoint_id`.

Consequence: an authenticated-data binding to the endpoint (§4.3) cannot be built today without adding one field to each struct and one column to each `SELECT`. This is a Go/SQL change with **no schema change**, because the column already exists on `deliveries` (and on `endpoints`).

### 3.4 Budget invariant has no room for secret resolution

**[R]** `internal/platform/config/runtime.go:418-423` validates `LeaseDuration > HTTPDeadline + HandshakeBudget + PostProcessBudget + ShutdownReserve`. Defaults at `:442-446` are 30 s > 10 + 2 + 3 + 10 = 25 s, leaving 5 s of slack.

**[R]** `internal/app/worker/processor.go` computes `required` as the sum of those four values (25 s) and `PrepareHTTP` refuses when the lease remainder is smaller. `HTTPPreparation.Remaining` is frozen at preparation time, and `executor.go:176` compares that frozen value again.

Consequence: if a network KEK unwrap is inserted between preparation and `client.Do` without being part of the validated budget, its latency silently consumes the 5 s slack. D6-E converts 2 s of that slack into the explicit `SecretResolutionBudget` component, so the validated sum becomes 27 s and the residual 3 s remains slack — **not** an additional budget component. Promotion alone is insufficient: a slow KMS can still push the HTTP call past the lease unless the Worker re-samples time after unwrap and refuses to start HTTP when the recomputed remainder is insufficient (§4.7).

### 3.5 Attempt rows would lose the transmitted headers

**[R]** `internal/app/delivery/store.go` (`Complete`) persists `sanitizeRequestHeaders(locked.requestHeaders)` — the **snapshot** headers, not the headers actually transmitted. `docs/architecture/database-schema.md` §4.9 says signature and timestamp headers are retained and secret-bearing headers redacted. Once U25 adds transmitted-only headers, the audit row would omit them unless the completion path also records what was sent.

This is a documentation/implementation divergence that exists today and would become visible under U25.

### 3.6 No key material can enter the process today

**[R]** `internal/platform/config/config.go` loads only six database variables (`HOOKFLOW_DB_{NAME,USER,PASSWORD,HOST,PORT,SSLMODE}`) and rejects missing or malformed values. `internal/platform/config` contains no `HOOKFLOW_ENV`, `APP_ENV`, or equivalent deployment-mode value; the remaining variables are connection and tuning settings (API listen address, RabbitMQ URI, `HOOKFLOW_WORKER_*`, publisher, recovery, idempotency sweep, endpoint cap). There is no provider selector, no key path, and no secret configuration. Fail-closed activation of D6 (§4.5) and the unsigned-delivery gate (D6-F) therefore require new, non-secret configuration; neither exists to be switched on today.

**[R]** `.gitignore` ignores `.env` and `.env.*` (except `.env.example`) but has **no** entry for key files or a secrets directory.

**[R]** `go.mod` has no encryption, KMS, or signing dependency; the standard library covers everything recommended here.

### 3.7 API surface

**[R]** `cmd/api` registers no domain routes; it serves health only, consistent with `runtime-readiness-decisions.md` ("The API remains health-only"). Endpoint creation, secret generation, and rotation endpoints do not exist, so D6's write path is a contract for future work.

### 3.8 Summary of gaps this package must resolve

| # | Gap | Type | Fixed by |
|---|---|---|---|
| G1 | No envelope codec, no strict parser | Code | D6-A |
| G2 | No DEK/KEK separation or provider | Code | D6-B, D6-C |
| G3 | `endpoint_id` absent from claim/preparation and SQL | Code, no schema | D6-B |
| G4 | No budget for secret resolution | Config invariant | D6-E |
| G5 | No signing implementation, tests pin "no signature" | Code, tests | U25-A/B |
| G6 | No scheme snapshot on `deliveries` | Design constraint | U25-D |
| G7 | Attempt audit omits transmitted headers | Code/doc mismatch | U25-B (implementation item) |
| G8 | No key-file exclusion; no deployment-mode value | Config/hygiene | D6-C, D6-F |
| G9 | Pre-HTTP signing-construction fault has no U18-safe durable representation; B5 is scoped to secret unavailability | Design/state machine | D6-G |
| G10 | Empty-secret unsigned path is ungated in both code and documentation | Security/config | D6-F |
| G11 | Retention is documented but not implemented; purge coverage and batch values open (U2); soft-deleted endpoints are outside the retention table | Ops/implementation | D6-D with U2 |
| G12 | No startup validation of the configured provider, allowlist, key file, or current `kid`; a deployment defect would surface per delivery | Config/classification | D6-C (§4.5.1) |

---

## 4. D6 recommended contract

### 4.1 Algorithm and envelope (2.1) — D6-A

**Algorithm.** AES-256-GCM, 96-bit (12-byte) nonce, 128-bit (16-byte) tag. **[V]** Go's `cipher.NewGCM` uses the standard 12-byte nonce and requires a nonce that is "unique for all time, for a given key"; the standard library documents that no more than 2^32 random nonces may be used with one key; `NewGCMWithTagSize` exists but non-standard sizes are discouraged. OWASP recommends authenticated modes (GCM/CCM) first and AES-256. Because each DEK encrypts exactly one secret, the invocation count per key is 1, so the 2^32 bound is never approached.

**DEK.** 32 bytes from `crypto/rand.Read`, which **[V]** "never returns an error, and always fills b entirely".

**Why not a single KEK encrypting data directly.** Direct KEK encryption would make every rotation a full re-encryption of every stored secret and would put the long-lived key on the data path. Envelope encryption with a per-secret DEK is the pattern recommended by OWASP and implemented by AWS KMS, Google Cloud KMS, and Vault Transit. **[V]**

**Envelope layout (decoded bytes).** All multi-byte integers are big-endian. The layout is fixed for version 1; any incompatible change must change the version byte and the domain tag.

| Offset | Size | Field | Value / constraint |
|---|---|---|---|
| 0 | 4 | `magic` | ASCII `HFSE` (`48 46 53 45`) |
| 4 | 1 | `env_version` | `0x01` |
| 5 | 1 | `alg` | `0x01` = AES-256-GCM, 32-byte key, 12-byte nonce, 16-byte tag |
| 6 | 1 | `purpose` | `0x01` = webhook signing secret |
| 7 | 1 | `kid_len` | `1..64` |
| 8 | `kid_len` | `kid` | ASCII matching `[A-Za-z0-9._:-]{1,64}` |
| `8+kid_len` | 1 | `provider` | `0x01` local file, `0x02` AWS KMS, `0x03` GCP KMS, `0x04` Vault Transit |
| `9+kid_len` | 2 | `wrapped_len` | `1..1024`, unsigned 16-bit big-endian |
| `11+kid_len` | `wrapped_len` | `wrapped_dek` | Opaque to HookFlow core; interpreted only by the provider |
| `11+kid_len+wrapped_len` | 12 | `data_nonce` | Random, from `crypto/rand`, never reused under one DEK |
| `+12` | `n` | `ciphertext` | `n` = plaintext length, `1..1024` |
| `+n` | 16 | `tag` | GCM authentication tag |

Total length is `39 + kid_len + wrapped_len + n`, at most `39 + 64 + 1024 + 1024 = 2151` bytes.

**Local provider wrapped blob** (`provider = 0x01`): `0x01 || wrap_nonce(12) || AES-256-GCM(KEK, DEK)` = 1 + 12 + 48 = 61 bytes. The 48 bytes are the 32-byte DEK plus the 16-byte tag.

**Variable-length `wrapped_dek` is deliberate.** **[V]** AWS KMS `Encrypt` ciphertext and Vault Transit ciphertext are opaque, provider-defined blobs whose length is not fixed; Google Cloud KMS likewise treats the wrapped key as opaque. A fixed 48-byte field (as sketched in the historical D6a draft) could not hold them.

**Storage encoding.** ASCII `hfse1.` followed by unpadded base64url (RFC 4648 §5) of the decoded envelope. Unpadded base64url avoids `+`, `/`, and `=`, which keeps the value safe in text columns, JSON, logs, and shell contexts. A structurally maximal v1 envelope encodes to 2,874 characters (2,151 bytes); the decoder rejects input beyond 5,468 characters (the unpadded encoding of 4,096 bytes plus the prefix) and decoded input beyond 4,096 bytes, so future versions have bounded headroom.

**Strict parsing rules.** The decoder must:

1. Reject any input longer than 5,468 characters (the unpadded base64url encoding of 4,096 bytes plus the `hfse1.` prefix) before decoding.
2. Require the exact prefix `hfse1.`; reject any other prefix, including a bare base64 value or a legacy format.
3. Decode with `base64.RawURLEncoding.Strict()`, which rejects padding characters and non-zero trailing bits.
4. Reject decoded input longer than 4,096 bytes, then apply the v1 structural bound of 2,151 bytes.
5. Reject unknown `env_version`, `alg`, `purpose`, or `provider`.
6. Reject `kid_len == 0`, `kid_len > 64`, and any `kid` byte outside the allowed alphabet.
7. Reject `wrapped_len == 0` and `wrapped_len > 1024`.
8. Reject plaintext length outside `1..1024`.
9. Reject truncation at every length check and **any trailing byte** after the tag.
10. Never log or include envelope bytes, wrapped bytes, or plaintext in an error; errors carry a reason code only.

**Plaintext constraints.** Opaque bytes, `1..1024` bytes, used verbatim as the HMAC key. No trimming, no Unicode normalization, no re-encoding. U25-A explains why the key must be the raw bytes, not a display form. The envelope itself permits 1 byte so that `purpose` can later cover non-key values; for `purpose = 0x01` the signing layer additionally requires at least 16 bytes (§6.1, §6.7).

**Why not store the secret as JSON or PEM.** A JSON or PEM wrapper invites whitespace and re-encoding mistakes in the HMAC key (§6.1) and adds a parser surface. The envelope is binary with fixed fields; the only text layer is the storage prefix.

**Relationship to the historical draft.** `runtime-readiness-decisions.md` §8.1 (Rev 3) proposed magic `HF1`, a fixed 48-byte wrapped DEK, and storage `v<key_version>:aes-256-gcm:<base64(nonce)+base64(ct+tag)>`. No data exists in either format, and today's code sends nothing when a secret is configured, so the format choice has no migration cost. This proposal supersedes the draft **as a proposal** for four reasons: variable-length wrapped DEKs, an explicit provider tag, an AAD that permits rewrap (§4.3), and a single canonical storage prefix. The Rev 3 text remains historical evidence and is not edited.

### 4.2 DEK scope and lifetime (2.2) — D6-B

**Scope.** One DEK per endpoint secret version, not per endpoint and not global. When an endpoint secret is rotated, a new DEK and a new envelope are produced; the old envelope remains valid and decryptable under its recorded `kid`. This matches Google Cloud KMS's guidance to generate a new DEK for each write and not to share a DEK across independent data. **[V]**

**Randomness.** 32 bytes from `crypto/rand`. **[V]** Never derived from a password, endpoint id, delivery id, or timestamp; never reused across endpoints or across rotations.

**Wrapping.** The DEK is wrapped only by the provider's KEK. The core never sees KEK material. The provider returns the `kid` and blob it used; the core stores them verbatim and never interprets them.

**Where the DEK lives.** The plaintext DEK exists only inside a single wrap or unwrap call. It is not a column, not a cache entry, not a struct field that outlives the call, not a fixture, and never appears in logs, traces, metrics, or error messages. After use, the slice is overwritten (`clear`) before it goes out of scope.

**Honest limit on zeroization. [E]** Go cannot guarantee that no copy of a slice remains in memory: the garbage collector may relocate data, the runtime may copy on stack growth, and the compiler may keep values in registers. Overwriting the slice is best-effort hygiene, not a guarantee. This is stated so that no one later claims memory scrubbing as a control.

**DB exposure.** The database holds only ciphertext plus non-secret metadata (`kid`, provider tag, nonce). A database dump without the KEK yields nothing usable. `kid` is not a secret; AWS documents that key IDs are unrelated to key material and can be moved via aliases. **[V]**

**Interface isolation.** The DEK is never passed to the HTTP layer, the signing layer, or the delivery store. The only value that crosses from decryption to signing is the plaintext webhook secret (§6.1), which is a different key with a different purpose.

### 4.3 Authenticated data and canonical encoding (2.3) — D6-B

**AAD, exact bytes (64 bytes):**

```
"hookflow.secret.v1"  18 bytes ASCII
alg                    1 byte  (0x01)
purpose                1 byte  (0x01)
data_nonce            12 bytes
organization_id       16 bytes  raw UUID bytes
endpoint_id           16 bytes  raw UUID bytes
------------------------------------------------
total                 64 bytes
```

"Raw UUID bytes" means the 16-byte big-endian value of the UUID, not its 36-character text form. The encoding is unambiguous because the layout is fixed-length and positional; no delimiters, length prefixes, or separators are needed, and none may be inserted.

**[R] Historical construction and its resolution.** `runtime-readiness-decisions.md` §8.1 historically built the AAD from the ids "rendered canonically (lowercase text)" and included `header_prefix`. That text is internally inconsistent — its formula labels each id as 16 bytes while its next bullet specifies canonical lowercase text — and no AAD builder or envelope codec exists anywhere in `internal/`, `cmd/`, or the tests, so **neither the historical form nor this proposal is implemented**. The 2026-11-24 reconciliation adds a matching supersession note to `runtime-readiness-decisions.md`, `overview.md`, and `database-schema.md`; it does not change any decision.

This proposal is the single exact proposed byte definition: 64 bytes from raw 16-byte UUIDs, excluding the header. The reasons are fixed-size unambiguous encoding and rewrap compatibility (§ above). If the Owner keeps this fixed layout but substitutes 36-byte lowercase UUID text, the AAD becomes **104 bytes**; if the Owner instead revives the historical header-prefix-plus-text construction, the AAD is **variable-length** — 168–231 bytes for a 1–64-byte `kid` — and every AAD test vector, length check, and canonical-encoding statement in this package must change. D6-B remains **NOT APPROVED**.

**Why each field is present.**

- The domain tag plus `v1` suffix binds the entire construction to this protocol. Any incompatible change must use a new tag, which makes old tags unverifiable under new rules and vice versa.
- `alg` and `purpose` are duplicated from the header into the AAD so that they cannot be swapped for a different semantic without breaking the tag, even if a future version reuses the field positions.
- `data_nonce` is authenticated so that a nonce substitution fails before decryption rather than producing a valid-looking but substituted plaintext.
- `organization_id` and `endpoint_id` prevent cross-tenant and cross-endpoint transplants: a ciphertext copied from another organization or another endpoint fails authentication deterministically.

**What is deliberately excluded, and why it is still protected.**

`kid`, `provider`, and `wrapped_dek` are **not** in the AAD. Including them would make "rewrap the DEK under a rotated KEK without re-encrypting the payload" impossible: the wrap bytes necessarily change on rewrap, so the AAD would change, so the payload tag would break. This was an internal contradiction in the Rev 3 draft, which included the header prefix in the AAD while also proposing rewrap.

Excluding them is safe because:

1. The wrapped DEK is authenticated by the KEK's own AEAD (local provider) or by the KMS/Vault decrypt operation; an altered blob is rejected by the provider.
2. `kid` selects a key. A substituted `kid` either names an unknown key (permanent failure) or the wrong key, which yields the wrong DEK and therefore a GCM tag failure.
3. `provider` selects an adapter. A deployment allowlist (below) rejects unlisted providers before any network call.
4. `ciphertext` and `tag` are GCM outputs over the AAD, so any change to them fails.
5. `data_nonce` is bound in the AAD, so it cannot be altered.

**Residual, documented property. [E]** An actor with arbitrary database write access could swap two complete, valid envelopes belonging to the *same* organization and endpoint (for example, the pre-rotation and post-rotation secret of one endpoint). Decryption would succeed and the Worker would sign with the wrong-but-valid secret; the receiver, holding the current secret, rejects the signature. The result is failed deliveries, not forged acceptance. Cross-organization and cross-endpoint swaps are impossible because of the AAD. A database-write attacker can already cause worse damage; the design does not claim protection against that actor, and says so.

**KEK wrap context.** Where the provider supports associated data, the wrap should bind `hookflow.kekwrap.v1` (19 bytes ASCII) plus the same 16-byte organization and endpoint values (51 bytes total). For the local provider this is a GCM AAD. For AWS KMS it is the `EncryptionContext` with keys `hookflow:purpose`, `hookflow:org`, `hookflow:endpoint`. For Vault Transit and Google Cloud KMS, associated data is not available on the wrap operation, so the data AAD alone carries the binding; this asymmetry is a provider difference, not a weakening, because the data AAD is still enforced.

**Why not length-prefix or JSON-canonicalize the AAD.** Length-prefixing is unnecessary when every field has a fixed size or a fixed position. JSON canonicalization would introduce exactly the ambiguity (key order, escaping, number formatting) that the fixed layout avoids. **[E]**

### 4.4 Rotation and key lifecycle (2.4) — D6-D

Three distinct operations must not be conflated:

| Operation | What changes | Payload re-encrypted? | Receiver impact |
|---|---|---|---|
| KEK rotation | New KEK version and new `kid`; DEK rewrapped | No | None; same plaintext secret |
| DEK (secret) rotation | New DEK, new nonce, new ciphertext, new plaintext secret | Yes | Receiver must accept the new secret |
| KEK retirement | Old KEK removed from the provider | No | None if no envelope references it |

**Key lookup.** The envelope is self-describing. The provider selects the KEK from `kid`; the core passes `kid`, `provider`, and the wrapped blob through unchanged. An unknown `kid` is a permanent cryptographic failure, never a fallback to another key.

**Retention facts, verified. [R]** `docs/architecture/database-schema.md` §6 (lines 1703–1731) fixes the agreed windows:

| Table | Retention | Basis | Notes |
|---|---|---|---|
| `events` | 90 days | `received_at` | purged oldest first |
| `deliveries` | 90 days | `created_at` | removed with the parent event |
| `delivery_attempts` | 90 days (inherited) | `started_at` | purged **before** `deliveries` |
| `idempotency_records` | **72 hours** (U4 approved) | `expires_at` | independent of event history; U43 sweep approved |
| `outbox_messages` | short, e.g. 7 days | `created_at` | queue table, not history (U2) |
| `audit_logs` | separate policy, proposed ≥ 365 days | `occurred_at` | product decision (U2) |

Purge order is child-first: `outbox_messages` → `delivery_attempts` → `deliveries` → `events`. `overview.md` restates the 90-day/72-hour windows and the child-first order.

Two facts bound what retirement may claim:

1. **The purge is documented, not implemented.** No Events/Deliveries/Attempts purge exists in `cmd/worker` (only the U43 idempotency sweep is wired), and §6 says table coverage and batch sizes remain open (U2). With the current repository, a delivery snapshot is never removed by code, so any KEK that has wrapped one has a live reference indefinitely.
2. **Endpoints are not in the retention table, and their envelopes persist on soft deletion.** `endpoints` uses soft deletion (`deleted_at`, with partial unique/live indexes filtered by `deleted_at IS NULL`; `0005_endpoints.up.sql:20,35,42`), and `secret_encrypted` is `NOT NULL` (`:10`). A soft-deleted row is not removed, so its envelope can reference a `kid` indefinitely. The repository contains no endpoint application module or CRUD route — `internal/modules/endpoint/` holds only `.gitkeep`, and `cmd/api` registers health routes only — so no production code sets `deleted_at`, nulls `secret_encrypted`, or purges endpoints. The only demonstrated hard delete is test cleanup (`test/integration/harness_test.go:223`), which is not production behavior. The organization FK's `ON DELETE CASCADE` is a schema-level hard-delete path, but organization erasure (U7) is open and is not an endpoint cleanup capability. Therefore 90 days is **not** an upper bound on how long an endpoint envelope can reference a `kid`.

**Reference-based retirement rule. [E]** This replaces any calendar-only formulation:

1. Every KEK version remains available for unwrap while **any** retained endpoint envelope or delivery snapshot references it. A version is not retired because a date passed.
2. The 90-day events/deliveries window is the expected **minimum** horizon for snapshots, not permission to delete a KEK at day 90. The true horizon is the later of (a) the last retained reference and (b) the completion of a verified purge or rewrap.
3. A version may be retired only after all dependent ciphertext has been purged **and** a bounded, documented audit reports zero remaining references.
4. If the audit cannot run, or reports an unreadable value it cannot classify, retirement stops. "Unknown" is a reference, not an absence.

**Operational conclusion for the current repository. [R]** KEK retirement is **operationally blocked**. No production code purges delivery snapshots, delivery attempts, events, or endpoint rows; soft-deleted endpoint envelopes are retained; and no reference-audit tool exists yet. Those are not documentation gaps that a calendar can override — they mean the precondition "zero references" cannot currently be established. The correct safe behavior is to keep every configured version available for unwrap, and to treat any claim that a version is unreferenced after 90 days as unproven. D6-D is **NOT APPROVED**, and this package recommends against approving a calendar-only alternative.

**Per-`kid` reference and usage accounting.** Three distinct instruments; only the second is authoritative:

- **Usage metrics (early warning, not proof).** The Worker increments `hookflow_kek_operations_total{kid, provider, operation, outcome}` on every wrap and unwrap (`kid` is not secret; cardinality is bounded by the number of configured versions). A silent version suggests retirement is *plausible*; it never proves that a retained snapshot does not reference it, because a snapshot can sit undelivered and never unwrap.
- **Reference audit (authoritative).** A read-only, bounded, paginated query decodes only the envelope **header** — prefix/version, `kid`, provider tag — from `endpoints.secret_encrypted` (**with no `deleted_at IS NULL` filter**, so soft-deleted rows are counted) and `deliveries.secret_encrypted`, never decrypting and never emitting envelope bytes. It reports counts per `kid`, and it **lists** every value it could not parse instead of skipping it, because a legacy or malformed value may still be ciphertext that depends on a key. The audit result is recorded with the time, query version, and configuration state.
- **Provider registry (what can still decrypt).** Configured versions carry explicit state — wrap-enabled, unwrap-enabled, retired — so that "the provider still has the key" is a deliberate, reviewable fact rather than an inference.

**Retirement procedure (bounded and documented).**

1. Disable wrapping under the version (`wrap-enabled = false`); keep unwrapping enabled.
2. Rewrap every endpoint envelope that still references it, or confirm none exists. Endpoint rows may be rewrapped; delivery snapshots are not (§ below).
3. Confirm the retention purge has actually removed every delivery snapshot that referenced it. With the current repository this step cannot be satisfied by code — it requires the missing purge (U2) or an equivalent, recorded deletion whose effect is verified by step 4. A row that still exists is still a reference, regardless of any exception note.
4. Run the reference audit and require zero references (unknown values block retirement).
5. Observe a quiet usage window and alert on any unwrap after step 4; a late unwrap means the audit is wrong and retirement must stop.
6. Disable unwrapping, record the evidence, then destroy the key version under the provider's own deletion policy. Destruction is irreversible.

Steps 2–6 are the "bounded, documented safety/verification procedure" required by the contract. Metrics may trigger step 5 early, but cannot replace steps 3–4.

**Rewrap vs re-encrypt.**

- Endpoint rows (`endpoints.secret_encrypted`) **are** rewrapped on KEK rotation: the same DEK is unwrapped and rewrapped under the new KEK, the ciphertext, nonce, and tag are untouched, and the row's `kid`, `provider`, and wrapped bytes are replaced. Rewrap only rewrites the header region. This is the recommended rotation procedure (D6-D, not yet approved) and it moves the reference to the new `kid`.
- Delivery snapshots (`deliveries.secret_encrypted`) **are not** rewrapped. The migration comment states these fields are immutable after acceptance, and A8 approved that immutability so that later endpoint edits cannot change an in-flight delivery. Rewrapping would mutate the column even though the plaintext would be unchanged; the safe reading of A8 is to leave the snapshot byte-identical for its lifetime. A snapshot exception would be an A8 amendment; this package recommends against it.
- The two paths do **not** behave identically, and retirement must not assume they do: a rewrap removes an endpoint reference, while only purge (or expiry of the delivery row) removes a snapshot reference.
- Consequence: an old KEK must remain available until every endpoint envelope that referenced it has been rewrapped and every delivery snapshot that referenced it has been purged. The 90-day window says when purge is *supposed* to have happened; the audit says whether it did. This is the governing rule for D6-D.

**Rotation sequence (recommended).**

1. Create the new KEK version in the provider and add it under a new `kid`; keep the old `kid` configured for unwrapping.
2. Mark the new `kid` as current for new writes.
3. Rewrap endpoint envelopes in bounded batches; each row update is independent, so an interrupted run leaves a mix of old and new `kid` values, all decryptable.
4. Observe per-`kid` usage as an early warning (§ above). Do **not** retire on the quiet window alone.
5. Complete the retirement procedure: purge verification, reference audit at zero, then disable unwrap and destroy.

**Interrupted rotation.** Because every envelope is self-contained and every row update is atomic, there is no global state to corrupt. A crash mid-run resumes idempotently. No distributed transaction is required.

**Concurrent rotation.** Two operators rotating simultaneously produce two new KEK versions; whichever write lands last determines the row's `kid`. Both versions remain decryptable, so this is safe but untidy. Serialize rotation through a deployment tool, not through application code. Do not add an advisory-lock column for this.

**Rollback.** Reverting the "current" `kid` to the previous version is safe for new writes because the old KEK is still present. Reverting after an old KEK was destroyed is not: envelopes wrapped under it become permanently undecryptable, which must be treated as data loss. Destruction is therefore the last step, gated by steps 3–5 above.

**Failed or unavailable KMS.** A timeout, throttling response, 5xx, or network error is **transient**. An unknown `kid`, malformed envelope, access-denied, invalid-ciphertext, or tag mismatch is **permanent**, unless the provider's classified error supplies transient evidence for that specific response. Both route through the approved B5 path (§4.7); the difference is the diagnostic reason code and the metric label. Routing transient KMS errors through broker reject/requeue instead would consume the 20-attempt broker delivery limit and risk DLQ stalls, while B5 already paces durably without touching `attempt_count`. **[E]** This is the **per-delivery** classification; a defect in the deployment's own provider configuration is a startup failure, not a delivery failure (§4.5.1).

**Superseded retirement text, now reconciled across documents. [R]** `docs/architecture/runtime-readiness-decisions.md` §8.1 historically said to keep decryption enabled "for at least the event retention window (90 days) or until a verified rewrap pass completes" and said delivered snapshots "can be rewrapped by a bounded background job". That wording is calendar-based when read alone and permits snapshot rewrap; it conflicts with A8's snapshot immutability and with the reference-based rule above. The 2026-11-24 reconciliation adds an explicit supersession note at that location, and matching notes in `overview.md` and `database-schema.md`; the historical text is retained, not erased. This proposal governs only if D6-D is approved; no decision is changed by the reconciliation.

**DEK rotation and receiver coupling.** Rotating a webhook secret necessarily changes the key the receiver must use. Until a receiver secret-distribution feature exists (out of scope), rotation is an operator procedure, not a self-service action. U25-D defines the overlap window.

### 4.5 Minimal Go interface and deployment comparison (2.5) — D6-C

**Core-facing interface (three methods, no provider leakage):**

```go
type Context struct {
    OrganizationID [16]byte
    EndpointID     [16]byte
}

type Wrapped struct {
    KID      string
    Provider byte
    Blob     []byte
}

type Provider interface {
    Wrap(ctx context.Context, dek []byte, c Context) (Wrapped, error)
    Unwrap(ctx context.Context, w Wrapped, c Context) ([]byte, error)
    CurrentKID() string
}
```

Design notes:

- `Wrap` chooses the current KEK. The returned `kid` is stored verbatim; the core never builds one.
- `Unwrap` receives exactly what was stored. It never falls back to another key.
- Errors must be classified by the adapter into `ErrTransient` or `ErrPermanent` with a reason code; adapters must not embed key material, ciphertext, or provider response bodies in error text.
- The interface has no `Delete`, `Rotate`, or `List` methods. Rotation and retirement are deployment operations performed with provider tooling, which keeps the runtime permission set minimal (wrap/unwrap only). **[E]**
- A second, optional interface for rewrap (`Rewrap(ctx, w, c) (Wrapped, error)`) may be implemented directly or as `Unwrap`+`Wrap`. For KMS adapters, a native re-encrypt call avoids exposing plaintext to the process and is preferable. **[V]** Vault documents exactly this property for `rewrap`: the plaintext is not revealed. AWS KMS `ReEncrypt` has the same shape.

**Local file provider (development only).** A ring file, JSON, shaped approximately as:

```json
{"current":"dev-2026-11","keys":{"dev-2026-11":"<32 bytes, base64url>"}}
```

Rules:

- File permissions `0600`, owned by the Worker user; the provider refuses to start if group or other bits are set. **[E]** (OWASP: restrict file permissions, avoid environment variables for key material.)
- Path supplied by non-secret configuration; key material is read from the file, never from an environment variable, never from a default path.
- Missing file, unreadable file, malformed JSON, wrong key length, or unknown `current` ⇒ provider construction fails at startup, not at first delivery.
- The file lives outside the repository; `.gitignore` must gain an entry for it (§7, implementation item). Backup is the operator's responsibility; losing the file loses every dev secret. This is acceptable for development and unacceptable for production.

**Deployment comparison (no provider selected).**

| Option | Custody of KEK | Runtime dependency | Rotation model | Fit with this envelope | Notes |
|---|---|---|---|---|---|
| Local file ring | Filesystem, operator-managed | None (in-process) | Manual: add key, switch `current` | Native | Development and single-operator test only; no HSM, no audit trail, no split custody |
| AWS KMS | AWS-managed HSM; plaintext never leaves KMS | AWS API availability and IAM | Automatic yearly key-material rotation, old versions retained for decrypt; alias can move **[V]** | Native; `kid` maps to alias/ARN in adapter config | Requires AWS deployment, IAM policy, and a retry budget for network latency |
| Google Cloud KMS | Google-managed; KEK never leaves KMS **[V]** | GCP API availability and IAM | Key versions; new version for writes, old versions decrypt | Native; `Encrypt`/`Decrypt` on the DEK | Google documents that KMS is designed for KEKs, with 64 KiB direct-encrypt limit; envelope is the intended pattern |
| HashiCorp Vault Transit | Self-hosted or managed Vault; key material never returned **[V]** | Vault availability, auth, unsealing | `rotate` creates a version; ciphertext carries `vault:vN:`; `rewrap` upgrades without plaintext **[V]** | Native, though Vault's own ciphertext already embeds a version, so the `kid` is redundant but harmless | Adds a stateful service with its own HA and unseal requirements |

**Recommendation.** Approve the interface and the development file provider now; do **not** select a production provider until the deployment target is named. The provider choice is D6-C, and the recommended default is deferral with the interface frozen, which unblocks all codec work without pre-committing infrastructure.

**Fail-closed mode gate. [E]** The local provider must be usable only when the deployment mode is `development` (D6-F). Without that mode, construction with the file provider fails and the Worker refuses to start. This prevents a production deployment from silently running on a file key because an operator copied a dev configuration. The gate is configuration, not a compile-time tag, so that tests can exercise both.

### 4.5.1 Startup validation vs per-envelope classification (D6-C)

A deployment configuration defect and a bad stored envelope are different failures and must not share a path. Conflating them turns one configuration mistake into a repeating per-delivery failure for every delivery in the deployment.

**Fail at startup — before any message is consumed:**

| Condition | Why it is startup state |
|---|---|
| Provider selector unset, unknown, or not implemented by this build (including a cloud adapter with no deployment target) | There is no runtime decision to make; the process cannot unwrap anything |
| Local file provider selected while mode is not `development` | A production deployment must never run on a file key |
| Key file missing, unreadable, malformed JSON, wrong key length, unknown `current`, group/other permission bits set | The ring is a startup input; discovering it per delivery would be repeated failure |
| Configured `kid` not present in the ring; duplicate or conflicting `kid` definitions | The active configuration is internally inconsistent |
| Provider allowlist empty, malformed, or names an adapter not available in this build | The allowlist is a deployment policy, not envelope data |
| Contradictory settings across mode, provider, gate, and allowlist | Failing closed is the only unambiguous resolution |

Startup failure means the Worker does not start; there is no delivery to classify and no metric per delivery.

**Classify per delivery — the stored envelope is untrusted input:**

| Condition | Classification | Path |
|---|---|---|
| Provider tag in this envelope unknown, unsupported, or not in the configured allowlist | Permanent, per delivery | B5 mechanics, reason `provider_disallowed` |
| Envelope malformed, unsupported version/algorithm/purpose, truncation, bad base64 | Permanent, per delivery | B5, reason `envelope_invalid` |
| Unknown `kid`, access denied, invalid wrapped ciphertext, authentication/tag failure | Permanent, per delivery, unless the provider's classified error supplies evidence that this specific condition is transient | B5, reason `key_or_auth` |
| Provider unavailable, throttled, timeout, 5xx, classified transient infrastructure error | Transient, per delivery | B5, reason `provider_transient` |
| Runtime loss of a previously valid provider input (for example, a deleted key file) | Classified by what the adapter reports; never reclassified as an envelope defect | B5 with the adapter's class; operational alert |

The last row is not a second startup check. Once the process is running, a provider that has become unavailable is a runtime fault; it must be classified by the adapter's error, attributed to the provider, and alerted at deployment scope. It must not be reported as though each affected envelope were independently corrupt, and it must not be "fixed" by falling back to another key or to unsigned delivery.

**Rule.** A global defect must produce **one** startup failure or **one** deployment-scope alert, never a stream of per-delivery failures that looks like data corruption.

**Never in the KEK's place.** Environment variables, source constants, Docker image layers, database columns, log fields, metric labels, and panic messages are all disallowed carriers for KEK material. A provider selector, file path, `kid`, and region are non-secret and may be configuration.

### 4.6 Schema compatibility (2.6)

**[R]** The current `secret_encrypted text NOT NULL` columns on both `endpoints` and `deliveries` can store the maximal v1 envelope (2,874 characters) with no migration and no type change.

**Smallest compatible change if a change were ever required:** none. Recommended: **no schema change**, and no `CHECK` constraint on the envelope format. Reasons:

1. A `CHECK` would reject existing non-empty values in development databases that were written before any format existed, turning a code-level classification into a migration failure.
2. It would encode a version-specific format in DDL, so every future envelope version would need a constraint migration.
3. It would not add security: the Worker must parse strictly regardless, because the database is not a trust boundary for content it did not write.

If the Owner later wants a defensive constraint, the only acceptable form is `ADD CONSTRAINT ... CHECK (...) NOT VALID` after auditing existing rows, followed by validation. This package recommends against it.

**Behavior change for existing non-envelope data. [E]** Today any non-empty value fails closed at `executor.go:158`. After implementation, a non-empty value that is not a valid envelope becomes a **permanent per-envelope failure** through the B5 mechanics: still no HTTP, still no attempt, still paced until the event horizon. The posture is unchanged (never unsigned); only the diagnostic and the retry pacing differ. This is an envelope classification, not provider configuration: it never blocks startup and never implicates the KEK ring. Development databases containing placeholder strings should expect paced retries instead of immediate terminal failure.

### 4.7 Worker decryption lifecycle (2.7) and B5 preservation — D6-E, D6-F, D6-G

**Exact sequence (proposed):**

1. Receive the AMQP message, parse the identifiers-only envelope, reject malformed messages as today.
2. `AcquirePointer` acquires the fixed lease; the claim carries `organization_id`, `delivery_id`, `endpoint_id` (new), `secret_encrypted`, URL, snapshot headers, and event payload.
3. Compute the required budget and call `PrepareHTTP`; it locks the delivery and event, revalidates the fence, checks event expiry and lease remainder, and returns an immutable preparation. Commit and release the transaction.
4. If `SecretCiphertext == ""`, evaluate the D6-F gate before deciding whether any request may be built. The unsigned development path is taken only when the deployment mode is `development` and `HOOKFLOW_ALLOW_UNSIGNED_DELIVERY` is `true`; then do not add signature headers and do not invent a key. In every other case — production, an unset or unknown mode, or the gate disabled — no HTTP is started, and the delivery is recorded through the pre-HTTP durable no-attempt path with reason `secret_missing` (D6-F, D6-G). The decision is made on the stored value plus configuration, never on an error, and a failed unwrap never becomes an empty secret.
5. Otherwise, with `context.WithTimeout(ctx, SecretResolutionBudget)`: strictly decode the envelope (pure, no I/O). Malformed, unsupported version/algorithm/purpose, or an unlisted provider ⇒ permanent.
6. `provider.Unwrap` with the recorded `kid`, provider tag, blob, and the organization/endpoint context. Timeout or classified transient ⇒ transient. Unknown `kid`, access denied, invalid ciphertext ⇒ permanent, unless the adapter's classification supplies transient evidence for that specific response.
7. Build the 64-byte AAD from the preparation's organization id and endpoint id plus the envelope's `alg`, `purpose`, and `data_nonce`; `aes.NewCipher(dek)`, `cipher.NewGCM`, `Open`. A tag failure ⇒ permanent.
8. Validate the plaintext: length `1..1024` and at least 16 bytes (a shorter HMAC key is rejected); reject an empty result. Failure ⇒ permanent.
9. **Re-sample the clock and recompute the remainder** against the lease deadline. Do not use the frozen `HTTPPreparation.Remaining` unchanged: the safe form is `preparation.Remaining` minus the monotonic elapsed time measured from immediately before `PrepareHTTP` was called (conservative, because it also counts the preparation round-trip), which preserves the database-time baseline and avoids host/database skew. Comparing `LeaseExpiresAt` directly to `time.Now()` is acceptable only if host/database skew is bounded and documented. From this point the lease must still cover `HTTPDeadline + HandshakeBudget + PostProcessBudget + ShutdownReserve`; the secret-resolution budget has already been spent. If it does not, do not start HTTP: reuse the existing `ErrInsufficientLease` handling (`processor.go:101–106` — release the fenced lease, settle transient under RB-4) and increment `hookflow_worker_budget_refused_total`. Never shorten the HTTP deadline to make the request fit.
10. Construct the request: URL validation, snapshot headers, `Content-Type`, diagnostics headers, body from `event.payload` bytes, **then** the three `webhook-*` headers computed over the final body bytes. If signature construction itself fails, it is a pre-HTTP internal failure: no HTTP, no attempt row, no `attempt_count` increment, no unsigned fallback, reason `signing_internal`, routed per D6-G (§ below). It is **not** a secret-decryption failure.
11. `client.Do`. Redirects remain disabled; no re-signing path exists.
12. Clear the DEK and plaintext secret slices (best effort, §4.2). Install the clear as a deferred action as soon as each slice is produced, so the failure returns at steps 6–10 clear them too; this step is the normal completion path, not the only one.

V1 treats `signing_scheme` as the protocol constant `hmac_sha256`; the delivery path does not read the endpoint column, because the snapshot does not carry it (§6.5). Rejecting any other stored value belongs to the future endpoint write path, not to per-delivery classification.

**Failure routing.** Secret-resolution failures in steps 5–8 call the existing durable pre-HTTP method (today `RecordDecryptFailure`, generalized per D6-G) with:

- a positive pacing value derived from the delivery id, as today;
- a bounded, scrubbed diagnostic (≤ 500 runes; reason code plus provider category, never bytes);
- a metric increment with a stable, low-cardinality reason label (`envelope_invalid`, `provider_disallowed`, `provider_transient`, `key_or_auth`, `plaintext_invalid`);
- no HTTP request;
- no `delivery_attempts` row and no `attempt_count` increment;
- event-horizon precedence before lease expiry, fresh database time, fencing revalidation, and `AckEligible` only after the durable commit.

This is the approved B5 behavior with classification added; it is not a new disposition. "Permanent" here means no retry can change the outcome; it does not mean the row is terminalized immediately, because pacing to the event horizon is the existing mechanics.

**Pre-HTTP internal failures (step 4 gate refusal and step 10 signing fault).** These also create no HTTP, no attempt row, and no `attempt_count` increment, and they never fall back to unsigned delivery. Their durable representation is the subject of D6-G:

- The existing B5 mechanics can **physically** store such a failure without violating U18: they write `deliveries.last_error`, a paced `next_attempt_at`, a retry outbox row, no attempt row, and the same states. No new column or state is required.
- The existing B5 semantics, however, are scoped to secret unavailability (sentinel `ErrSecretUnavailable`, method name, processor branch, metric). Using them unchanged would report a programming fault or a missing secret as a decryption failure and would retry a deterministic condition until the event horizon. That is a **misclassification**, not a U18 violation.
- D6-G therefore asks the Owner to approve an explicit reason code (`secret_unavailable`, `secret_missing`, `signing_internal`) on the same mechanics, with distinct metrics, or to approve a terminal `failed` transition with zero attempts instead. Until one is approved, this path has no approved durable representation and must not be implemented on an improvised basis.
- Operational visibility for `signing_internal` is a bounded, scrubbed `last_error` value, a low-cardinality counter (`hookflow_worker_pre_http_failure_total{reason="signing_internal"}`), and an alert: it indicates a code defect, not a receiver or secret problem.

**Expired secret-resolution context.** The durable disposition and the broker settlement must run under a fresh bounded context. They must not reuse a context already cancelled by the `SecretResolutionBudget` deadline, or the failure record itself would be lost.

**Why decryption stays after `PrepareHTTP`.** `PrepareHTTP` revalidates the fence and the lease before any expensive work. Decrypting earlier would either duplicate that validation or spend provider latency on a lease that is about to be found invalid.

**Budget amendment (D6-E).** `SecretResolutionBudget` is approved at `2s` (`HOOKFLOW_WORKER_SECRET_RESOLUTION_BUDGET`) as a fifth component of the required sum and of configuration validation:

```
LeaseDuration > HTTPDeadline + HandshakeBudget + PostProcessBudget
              + ShutdownReserve + SecretResolutionBudget
```

With current defaults: `30 s > 10 + 2 + 3 + 10 + 2 = 27 s`. The remaining **3 s is slack, not a sixth budget component** and not spendable budget that may be assigned to a new phase without re-checking the inequality. Any change to a component must re-verify the whole sum.

The fifth component applies only to the **pre-resolution** `PrepareHTTP` requirement and to configuration validation. The executor's existing post-resolution check remains the four-component `HTTPOptions.RequiredLeaseBudget()` (25 s); adding `SecretResolutionBudget` to that check would reject every delivery after a successful unwrap, so it must not be added there.

**Re-sampling after unwrap.** The Worker must re-sample the clock and recompute the remainder after secret resolution and before starting HTTP; it must not use the frozen `HTTPPreparation.Remaining` unchanged. Use the monotonic elapsed time since preparation against the database-sampled remainder (or a fresh database time reading), so that host/database clock skew cannot inflate the remainder; a raw `LeaseExpiresAt - time.Now()` comparison requires bounded, documented skew. From that point, the lease must cover the four post-resolution components (`HTTPDeadline + HandshakeBudget + PostProcessBudget + ShutdownReserve`), because the secret-resolution budget has already been consumed. If it does not, HTTP does not start and the existing `ErrInsufficientLease` handling applies (§4.7 step 9) — there is no shortened HTTP deadline. Acceptance test: with a provider stub that sleeps near the budget, the HTTP call either starts with a provably sufficient remainder or does not start; with a stub that oversleeps the budget, the resolution context expires, the failure is transient through B5 (with `provider_transient`), and the disposition is written under a fresh bounded context.

**Signing failures.** With a validated non-empty key, construction has no realistic failure: `hmac.New` has no error return, and the standard library's `sha256`/`hmac` writers do not return errors in practice. **[V]** If a defensive signer nevertheless reports an error, it is a pre-HTTP internal failure: **no HTTP, no attempt row, no `attempt_count` increment, no unsigned fallback.** It must be reported under the distinct reason `signing_internal`, never as a secret-decryption failure, and its durable representation is D6-G. It must not be turned into a fabricated attempt row to make it "visible"; U18 forbids that, because no HTTP execution occurred.

**Attempt rows and crypto classes.** Because every pre-HTTP path above writes no attempt row, the `error_class` enum needs no change and no new value is invented. An attempt row remains what U18 defines — a completed HTTP execution whose result was observed — and a post-execution internal fault, if one is persisted, uses the existing `internal` value.

---

## 5. D6 contract summary (single view)

| Element | Recommended value |
|---|---|
| Cipher | AES-256-GCM, 12-byte nonce, 16-byte tag |
| DEK | 32 random bytes per endpoint secret version; one encryption per DEK |
| Envelope | `HFSE` binary header + `data_nonce` + ciphertext + tag |
| Storage | `hfse1.` + unpadded base64url; max 2,874 chars; decode cap 4,096 bytes |
| AAD | 64 bytes: `hookflow.secret.v1` ‖ alg ‖ purpose ‖ data_nonce ‖ org(16) ‖ endpoint(16) |
| Key slot | `kid`, `provider`, variable-length wrapped DEK; not in AAD; authenticated by provider and by tag selection |
| KEK | External; provider interface; never in DB/source/images/env/logs |
| Dev provider | 0600 JSON ring file, explicit development gate, fail at startup |
| Production provider | Not selected; defer until deployment target is named |
| Rotation | Endpoints rewrapped; delivery snapshots never rewrapped |
| Retention | Events 90 d, deliveries 90 d, attempts 90 d inherited, idempotency 72 h (documented; purge implementation open, U2) |
| KEK retirement | Reference-based: available while any retained endpoint envelope or snapshot references it; purge + audit at zero before destruction |
| Schema | No change; no CHECK constraint |
| Failure path | B5 mechanics unchanged: no HTTP, no attempt, paced, ACK after durable commit; reason-coded per D6-G |
| Pre-HTTP internal fault | No HTTP, no attempt, no count, no unsigned fallback; durable representation is D6-G (`signing_internal`) |
| Empty secret | Unsigned only when mode = development **and** the unsigned gate is enabled; otherwise fail closed (`secret_missing`) |
| Provider errors | Configuration defects fail startup; envelope tags classify per delivery; transient provider faults go through B5 |
| Budget | Add 2 s component: 30 s > 27 s; 3 s slack is not a component; re-sample after unwrap |

---

## 6. U25 recommended contract

### 6.1 Algorithm and key usage (3.1) — U25-A

**Construction.** Standard Webhooks v1: `HMAC-SHA256(secret_bytes, webhook_id + "." + webhook_timestamp + "." + body_bytes)`. **[V]** The Standard Webhooks specification defines the signed content as message id, timestamp, and payload joined by `.`; HMAC-SHA256; the payload sent must equal the payload signed; ids and timestamps must not be user-controlled or contain `.`; verification uses constant-time comparison.

**Why HMAC-SHA256 rather than an asymmetric scheme.** The receiver is a third-party endpoint that must already share a secret with HookFlow; symmetric MACs need only that secret, need no key distribution or certificate lifecycle, and are the norm for webhook signing (Standard Webhooks, Stripe). **[V]** Asymmetric signatures (Ed25519/ECDSA) would require publishing a verification key and give no benefit for a per-endpoint shared secret. Full RFC 9421 HTTP Message Signatures are a general framework over arbitrary components with its own signature base, `Signature-Input`/`Signature` fields, and canonicalization rules; it is more machinery than a fixed webhook shape needs, and off-the-shelf receivers would still need RFC 9421 support. **[V]** RFC 9421 does define `hmac-sha256`, so a future migration is possible without changing the primitive.

**Exact key bytes.** The HMAC key is the **decrypted plaintext, byte for byte**. No trimming, case folding, base64 re-decoding, or prefix handling at sign time. The plaintext contract (§4.1) says the stored bytes are the key. If the future management API presents a Standard Webhooks style `whsec_<base64>` string to users, that presentation must be decoded **before** encryption, so that the stored plaintext is the raw key and an off-the-shelf verifier that decodes `whsec_` arrives at the same bytes. This must be stated in the API contract when that API is written; it cannot be inferred later without breaking receivers.

**Key length.** Signing requires at least 16 bytes and accepts up to 1,024 (the envelope cap). Standard Webhooks recommends 24–64 random bytes for the secret; **[V]** the proposed generated default is 32 bytes. A shorter key is rejected as a permanent configuration failure rather than signed with, because an HMAC key of a few bytes is brute-forceable offline from a single captured request/response pair.

**Is the webhook secret the signing key?** Yes. The per-endpoint secret is the HMAC key. It is not the DEK and not the KEK.

**Encryption/signing key separation.** Three distinct keys with three distinct jobs:

| Key | Purpose | Location | Never used for |
|---|---|---|---|
| KEK | Wrap/unwrap DEKs | KMS or dev file | Signing, HMAC, direct data encryption in bulk |
| DEK | Encrypt one endpoint secret | Inside the envelope | Signing |
| Webhook secret | HMAC outbound requests | Decrypted per attempt | Encryption |

Using the DEK or KEK as an HMAC key would mean a single compromise yields both confidentiality and forgery, and would make rotation semantics ambiguous. This is prohibited.

**Signature encoding.** `webhook-signature: v1,<standard base64 of the 32-byte MAC>`, standard alphabet with padding, exactly as Standard Webhooks specifies. The prefix `v1` identifies the scheme and must be parsed strictly; unknown versions are ignored by receivers, which is how rotation of the construction would work.

**Constant-time comparison.** HookFlow sends; it does not verify inbound signatures, so `hmac.Equal` is not on the delivery path. Any future inbound verification (for example, a test receiver) must use `hmac.Equal`, which compares without leaking timing information. **[V]**

### 6.2 Headers (3.2) — U25-B

| Header | Present | Value | Signed | Notes |
|---|---|---|---|---|
| `webhook-id` | Required when a secret is configured | The delivery id, canonical lowercase hyphenated UUID | Yes | Stable across all attempts of one delivery |
| `webhook-timestamp` | Required when a secret is configured | Unix seconds, ASCII digits, no leading zeros | Yes | Fresh on every attempt |
| `webhook-signature` | Required when a secret is configured | `v1,<base64>` | No (it is the signature) | Exactly one signature in V1 |
| `Content-Type` | Always | `application/json` | No | Fixed by the executor; body is JSON text |
| `X-HookFlow-Event-Id` | Diagnostic | Event UUID | No | Untrusted metadata; receivers must not make security decisions from it |
| `X-HookFlow-Dispatch-Sequence` | Diagnostic | Integer | No | Untrusted metadata |
| `X-HookFlow-Delivery-Id` | Diagnostic | Same value as `webhook-id` | No | Retained because existing docs tell consumers to dedupe on it |

No key id, KEK id, DEK id, key version, provider name, envelope prefix, or algorithm identifier beyond `v1` appears in any header or in the body. Encryption metadata is a database-only concern. **[E]**

The duplication between `webhook-id` and `X-HookFlow-Delivery-Id` is intentional: the `webhook-*` set is the protocol, and the `X-HookFlow-*` set is unsigned diagnostics for operators. Documentation must say the unsigned headers are not authenticated.

Empty-secret snapshots send only `Content-Type` and the `X-HookFlow-*` diagnostics, exactly as now, and only when the deployment mode is `development` with the unsigned gate enabled (D6-F). With the gate disabled, an empty secret produces no request at all; no signature header is invented in either case.

### 6.3 Canonical signing input (3.3) — U25-B

**Exact bytes to MAC:**

```
webhook_id || 0x2E || webhook_timestamp_ascii || 0x2E || body_bytes
```

where `0x2E` is `.`. No length prefixes, delimiters, trailing newline, or charset conversion. This is unambiguous because:

- `webhook_id` is a UUID in canonical lowercase form, which contains hyphens but no dots;
- `webhook_timestamp` is digits only;
- `body_bytes` is last, so any dots it contains cannot create a boundary ambiguity.

**What is not signed.** Method, scheme, host, path, query, `Content-Type`, organization id, endpoint id, event id, and `X-HookFlow-*` headers are **not** signed. This matches Standard Webhooks, whose signed content is id, timestamp, and payload only, and it is what makes ordinary webhook verification libraries work without custom canonicalization.

**Why not sign the URL.** URL normalization is a trap: case of scheme and host, default ports, percent-encoding equivalence, dot-segment resolution, duplicate or reordered query parameters, and fragment handling all admit multiple textual forms for the same resource. RFC 9421 devotes a security section to canonicalization attacks for exactly this reason. **[V]** A signer that normalizes and a receiver that normalizes differently produce silent verification failures. By not covering the URL, HookFlow avoids this class entirely. The cost is documented below.

**Residual property, stated plainly. [E]** Because the URL is not signed, a captured request is not bound to a path: within the timestamp window, it could be replayed to a different path on the same receiving host. Identical properties exist in Standard Webhooks, Stripe, and GitHub-style webhooks. Mitigations are receiver-side: dedupe on `webhook-id`, and route on the payload, not on the path. If the Owner considers path binding essential, U25-B offers an extended profile as an alternative; it breaks off-the-shelf verifiers.

**Body bytes.** Sign `[]byte(preparation.EventPayload)`, which is the exact PostgreSQL `jsonb` text that the request body is built from, and construct the reader from that same slice. Never parse, re-marshal, or pretty-print before signing. Redirects are disabled, so there is no second request to sign; request compression is not enabled, so the bytes on the wire equal the bytes signed; TLS does not alter the body. **[R]** U17 (raw payload retention) is about byte-exact replay of the original producer bytes and remains open; it does not affect signature validity, because whatever HookFlow sends is what it signs.

**Canonicalization rules applied.** None beyond the above: no whitespace stripping, no case folding, no Unicode normalization, no query sorting. The inputs are generated by HookFlow itself in a fixed form.

### 6.4 Timestamp, replay, and delivery semantics (3.4) — U25-C

- **Format:** Unix seconds since the epoch, base-10 ASCII, no leading zeros. Not milliseconds, not RFC 3339.
- **Generation time:** at request construction, per attempt. A retry 30 seconds later carries a new timestamp and a new signature with the same `webhook-id`. If the timestamp were the event creation time, retries hours later would fall outside any sane tolerance and always fail.
- **Clock source:** the Worker host clock. Deployments must run NTP; HookFlow cannot compensate for a skewed receiver clock. Recommended receiver tolerance: ±300 seconds, which absorbs ordinary NTP error and modest drift. **[E]**
- **Replay:** the timestamp window bounds replay to the tolerance interval. It does not prevent replay inside that window; `webhook-id` deduplication does. A receiver should record the id after durable processing, so that a retry following a receiver-side failure is still processed, while a duplicate following success is dropped. Standard Webhooks recommends a storage interval on the order of minutes and says verification must include an idempotency check. **[V]**
- **Delivery id:** `deliveries.id`, app-generated UUIDv7, stable across attempts, not user-controlled, no dots. It identifies the logical message, which is the right unit for idempotency.
- **Duplicate behavior:** duplicates are expected and permitted. At-least-once is the contract. Nothing in this design gives exactly-once, and no documentation may claim that a timestamp does.

### 6.5 Key identification and rotation (3.5) — U25-D

**No key identifier is transmitted.** Standard Webhooks has none; receivers hold exactly one secret per endpoint and try it. Stripe's model is different (multiple `v1` signatures during rotation), and RFC 9421 allows explicit key ids, but neither is required here.

**Why HookFlow can omit a key id.** A delivery snapshots the secret that existed when it was accepted. Every attempt of that delivery signs with that snapshot, even if the endpoint has since rotated. Therefore a receiver can keep both the old and the new secret and accept either, with no need to tell which one was used. This is a direct consequence of the approved A8 snapshot semantics. **[R]**

**Overlap window.** The receiver must accept the previous secret for at least the maximum time an old-snapshot delivery can still be sent: the retry horizon (12 h) plus the timestamp tolerance. Recommended minimum: **24 hours** of dual acceptance after rotation, with the previous secret removed only after no deliveries referencing it remain. **[E]** Because HookFlow cannot see the receiver's configuration, this is a documented contract, not an enforced one.

**Multiple signatures in one request.** Not supported in V1. Emitting two signatures would require the snapshot to carry two secrets. The snapshot holds one ciphertext whose plaintext is one key. A "keyring plaintext" (a JSON array of keys inside the envelope) would make dual signing possible without schema change, because the plaintext is opaque bytes of up to 1,024 bytes, but it changes the plaintext contract, complicates validation, and is not needed if receivers keep both secrets. Recommended: single key plus the documented overlap. The keyring is listed as the alternative under U25-D.

**Deliveries have no `signing_scheme` column. [R]** The scheme cannot vary per delivery today. Options: (a) treat the scheme as a protocol constant (`hmac_sha256`) for V1 and validate the endpoint column when it is read in the future write path; (b) add a column later if per-endpoint schemes become a real requirement. Recommended: (a). Adding a column now would be speculative.

### 6.6 Integration point (3.6) — U25-B

Signing happens inside the executor, after all of the following and before `client.Do`:

1. decryption, plaintext validation, and the post-resolution budget check (§4.7 steps 5–9);
2. URL and scheme/host validation (`executor.go:207`);
3. assignment of snapshot headers and fixed headers (`executor.go:213-217`);
4. construction of the body reader from `EventPayload`;
5. `request.GetBody = nil`.

Then set `webhook-id`, `webhook-timestamp`, and `webhook-signature`, and only then call the client. The request object at that point is the exact object that will be transmitted; there is no later header mutation, and redirects are disabled. **Do not** compute the signature over a re-parsed payload, a header map serialization, or a URL string.

Signature inputs must be immutable by construction: build them once, pass them as a value, and never expose a setter that could be called after signing.

### 6.7 Failure handling and the no-unsigned-fallback invariant (3.7)

| Situation | Classification | HTTP? | Path |
|---|---|---|---|
| `secret_encrypted` empty; mode = `development` **and** unsigned gate enabled | None | Yes, unsigned | Existing development path, gated (D6-F); request bytes themselves unchanged |
| `secret_encrypted` empty; gate disabled, production, or unset/unknown mode | Permanent, per delivery (empty stored secret) | No | Pre-HTTP durable no-attempt path, reason `secret_missing` (D6-F/D6-G); never unsigned |
| Non-empty, envelope malformed | Permanent per-envelope | No | B5 mechanics, reason `envelope_invalid`; no attempt, paced |
| Unsupported version/alg/purpose | Permanent per-envelope | No | B5, `envelope_invalid` |
| Provider tag unknown, unsupported, or not in the configured allowlist | Permanent per-envelope | No | B5, reason `provider_disallowed`; distinct from startup validation |
| Provider unavailable, throttled, timed out, or classified transient infrastructure error | Transient | No | B5, reason `provider_transient`, distinct metric |
| Unknown `kid`, access denied, invalid ciphertext, authentication failure | Permanent, unless the provider evidence shows it is transient | No | B5, reason `key_or_auth` |
| GCM tag mismatch | Permanent | No | B5, `key_or_auth` |
| Plaintext empty or shorter than 16 bytes | Permanent | No | B5, `plaintext_invalid` |
| Signature construction error (programming fault) | Internal pre-HTTP | No | D6-G: reason `signing_internal`, no attempt, no count, distinct metric; **not** a decrypt failure |
| Invalid URL | Pre-HTTP internal (existing behavior) | No | Processor already settles this as a transient requeue with no attempt row; unchanged by this package |
| Transport error | Existing classification | Attempted | Unchanged from today |

**The invariant.** If `SecretCiphertext != ""`, the request must carry a valid `webhook-signature`. There must be no branch that reaches `client.Do` without it. If `SecretCiphertext == ""`, the request may be sent only under the D6-F gate; otherwise no branch reaches `client.Do` at all. Enforce this structurally by building the request only inside one function that either returns a fully signed request (or a gate-authorized unsigned one) or an error, plus a test that injects a fake `RoundTripper`, drives every failure case above, and asserts the transport was never invoked.

**Empty-secret path.** An empty string means "no secret configured"; it never means "signing failed, continue unsigned". The unsigned behavior is preserved only inside the explicit Development gate. In production, an empty secret is a per-delivery failure caused by the stored value, recorded without an attempt; it is not sent unsigned, not silently skipped, and not converted into a signature over an invented key. It is not a startup condition, because the stored value is delivery data, not deployment configuration. The distinction is made on the stored value plus configuration, never on an error.

---

## 7. Cross-cutting security review (scoped to D6/U25)

**Addressed by this design.**

- Separation of duties between KEK, DEK, and signing key, with no key used for two purposes.
- Envelope encryption: database disclosure alone does not reveal secrets; the KEK never enters the database.
- Cross-tenant and cross-endpoint transplant resistance through the AAD.
- No unsigned fallback and no silent downgrade when a secret is configured; unsigned delivery is confined to an explicit Development gate and fails closed elsewhere.
- Pre-HTTP failures (missing secret, signing-construction fault) create no attempt row, so U18 remains true and the audit trail is not falsified.
- Provider configuration is validated at startup, so a deployment defect cannot masquerade as per-delivery envelope corruption.
- No encryption metadata (kid, provider, DEK, nonce) in outbound headers; no secret in headers, bodies, logs, metrics, or error text.
- KEK retirement is reference-based and audited, not calendar-based; a retained snapshot cannot be silently orphaned.
- Deterministic, non-secret reason codes for failures; bounded, scrubbed diagnostics.
- Receiver-side forge resistance: SHA-256-length MAC under a per-endpoint key, constant-time verification on the receiver side per the specification.
- Replay bounded by the timestamp window and idempotent processing keyed by `webhook-id`.
- Rotation without downtime for in-flight deliveries, by snapshot semantics.
- Strict parsing with explicit size caps, so malformed input is a classified failure rather than a crash or an unbounded allocation.

**Residual risks the design accepts, with mitigations.**

- Path-independent signatures (no URL binding): replay to another path on the same host within the tolerance window; mitigated by id dedupe and payload-based routing.
- Same-endpoint envelope swap by a database-write attacker: causes failed deliveries, not forged acceptance.
- Receiver clock skew outside tolerance: rejected deliveries; mitigated by 300 s tolerance and NTP.
- Receiver that does not implement dedupe: duplicate processing; inherent to at-least-once.
- Best-effort memory clearing: a process-memory attacker can still recover plaintext.
- Deterministic pre-HTTP failures (missing secret, programming fault) pace the same delivery until the event horizon because the approved durable path does not terminalize on its own. This is visible and bounded by the event horizon; changing it would require a new disposition (D6-G), which this document does not assume.
- Until the retention purge exists (U2), old KEKs must be kept indefinitely for any version that wrapped a delivery snapshot. That is the safe failure direction: key accumulation, not data loss.

**Explicitly future infrastructure or operational controls, not solved here.**

- IAM policy scoping of KMS permissions, key policies, and separation of the Encrypt/Decrypt principals from administrators.
- HSM/FIPS posture and provider certification.
- Backup, restore, and disaster recovery for the KEK ring and for ciphertext; restoring a database without the KEK yields nothing.
- Key-compromise runbook: rotate KEK, re-encrypt every secret, rotate every webhook secret, notify receivers.
- Audit logging of wrap/unwrap calls at the provider.
- Per-tenant key isolation at the KMS level (today one KEK per deployment; encryption context carries tenant identity).
- Secret distribution to receivers and a rotation UI/API.
- SSRF/egress controls for outbound URLs, HA/failover, and capacity.

**Out of scope.** This is not a general production-readiness review. No claim is made that these controls are implemented, and the Production status remains NOT READY.

---

## 8. Owner decision package

Each item states the exact question, the recommended default, the alternatives, and the exact consequence of each option. Decisions are grouped only where a technical dependency is real.

### D6-A — Cipher and envelope format

**Question.** Approve AES-256-GCM with a per-secret DEK and the `hfse1.` binary envelope of §4.1, including the 1,024-byte plaintext cap, the 4,096-byte decode cap, and the strict parser rules?

**Recommended.** Approve as written.

**Alternatives.** (a) AES-256-GCM with a fixed 48-byte wrapped-DEK field and no provider byte (the Rev 3 shape) — rejected because it cannot carry KMS ciphertext. (b) ChaCha20-Poly1305 — no Go/KMS advantage here, and Vault notes it is not FIPS-approved in FIPS mode. (c) Direct KEK encryption of secrets with no DEK — simpler, but every rotation re-encrypts everything and puts the long-lived key on the data path.

**Consequences.** Approving fixes the persisted format; any later change requires a new version byte plus a migration of existing envelopes. Rejecting leaves D6 unimplementable. Modifying the size caps changes the parser, the storage bound, and the tests together.

### D6-B — DEK scope and authenticated data

**Question.** Approve one DEK per endpoint secret version, and the 64-byte AAD of §4.3 that binds domain tag, alg, purpose, data nonce, organization, and endpoint, while deliberately excluding `kid`, provider, and wrapped bytes?

**Recommended.** Approve as written, with the explicit consequence that `endpoint_id` must be added to `Claim` and `HTTPPreparation` and to both SQL projections.

**Alternatives.** (a) Include the key slot in the AAD — then rewrap requires re-encryption of the payload, and the snapshot-immutability benefit is lost. (b) Bind only the organization, not the endpoint — weaker; a same-tenant endpoint swap would decrypt. (c) Add a further secret-identity field to the AAD — requires a schema column, which the tasks forbids designing without necessity.

**Consequences.** Without the endpoint in the AAD, cross-endpoint transplant is possible within a tenant; rejecting is a security regression. Without excluding the key slot, D6-D's rewrap model is invalid.

### D6-C — KEK custody, provider interface, and deployment boundary

**Question.** Approve the three-method provider interface with provider-classified errors, a development-only 0600 file ring behind the deployment-mode gate, no production provider selection until a deployment target is named, startup validation of the selected provider / allowlist / key file / current `kid`, per-envelope classification of provider tags, and the prohibition on KEK material in source, images, environment variables, database rows, and logs?

**Recommended.** Approve the interface, the development provider, and the startup-versus-envelope split of §4.5.1; defer the production provider.

**Alternatives.** (a) Select AWS KMS now — only sensible if the deployment target is AWS; brings IAM and latency dependencies. (b) Select GCP KMS now — same, for GCP. (c) Select Vault Transit now — adds a stateful service requiring HA and unseal. (d) Allow a KEK in an environment variable — rejected: environment is readable by child processes, appears in crash dumps, and OWASP explicitly warns against it.

**Consequences.** Deferral unblocks all codec and signing work but leaves production activation blocked, which is already the case. Selecting a provider now creates an infrastructure dependency that cannot be reversed cheaply. Any option must keep wrap/unwrap permissions minimal and must define what happens when the provider is unreachable (D6-D, D6-E). The startup validation in §4.5.1 adds refusals to start on an inconsistent configuration; that is a behavior change for operators and must be documented before rollout. A per-envelope provider tag remains untrusted input and must never be able to block startup.

### D6-D — Rotation, retention, and reference-based retirement

**Question.** Approve: rewrap endpoint envelopes on KEK rotation; never rewrap delivery snapshots (A8); keep every KEK version available for unwrap while **any** retained endpoint envelope or delivery snapshot references it; treat the documented 90-day events/deliveries and 72-hour idempotency windows as horizons rather than a deletion calendar; retire a version only after the dependent ciphertext is purged **and** a bounded reference audit reports zero references, with per-`kid` usage metrics as an early warning rather than proof; classify provider faults as transient or permanent and route both through B5; and record that the current repository cannot satisfy the purge precondition until the retention purge (U2) exists?

**Recommended.** Approve as stated. **No retention duration needs to be chosen**: `database-schema.md` §6 already fixes 90 days for events and deliveries, 90 days inherited for attempts, and 72 hours for idempotency records. The decision is the **reference-based rule**, not a number.

**Alternatives.** (a) Rewrap delivery snapshots too — mutates immutable snapshots, contradicts A8, and rewrites potentially huge numbers of rows on every rotation. (b) Delete old KEK versions on a fixed calendar — risks silent, permanent data loss, because soft-deleted endpoints and unpurged snapshots can outlive any date. (c) Retire on a quiet usage window alone — a snapshot that is never dispatched produces no unwrap, so silence is not absence. (d) Shorten the retention window to shorten key life — discards delivery audit history and does not address endpoint envelopes.

**Consequences.** With the recommended rule, KEK versions accumulate while references exist or while the purge is unimplemented. That is the safe failure direction. Retiring on a calendar or on metrics alone is rejected. If the Owner later approves a snapshot-rewrap exception, it is an A8 amendment and this package recommends against it. The discrepancy recorded at `runtime-readiness-decisions.md:496–500` must be resolved in the architecture documents in favor of A8 and this rule before implementation claims consistency.

### D6-E — Pre-HTTP budget amendment and crypto failure classification

**Question.** Approve adding `SecretResolutionBudget` (default 2 s, subject to approval) as the fifth component of the lease invariant and of configuration validation — `LeaseDuration > HTTPDeadline + HandshakeBudget + PostProcessBudget + ShutdownReserve + SecretResolutionBudget`, i.e. `30 s > 10 + 2 + 3 + 10 + 2 = 27 s` — with the residual 3 s explicitly slack rather than a budget component; require the Worker to re-sample the clock and recompute the remainder after secret resolution and before HTTP instead of trusting a frozen pre-unwrap value; and route secret-resolution failures through B5 with the classification in §6.7?

**Recommended.** Approve. The invariant is satisfied with 3 s to spare, and the 3 s is slack, not spendable budget. Re-sampling is part of the amendment; adding the component without it does not prevent an overrun.

**Alternatives.** (a) Raise the lease to 35 s and keep four budget components — larger blast radius on recovery and pacing assumptions. (b) Reduce the HTTP deadline to 8 s — shrinks the time available to slow receivers. (c) Do not bound secret resolution — breaks the fencing guarantee under slow KMS; rejected. (d) Add the component and rely on the frozen `Remaining` — rejected: the value predates the unwrap and proves nothing after it.

**Consequences.** Without this change, a slow unwrap can push the HTTP call past the lease, which is a correctness failure, not a performance one. With it, an over-budget unwrap is a transient failure handled by the already-approved durable path, and an unwrap that succeeds but leaves an insufficient post-resolution remainder starts no HTTP. A signing-construction fault is **not** a cryptographic failure and is not routed here; it is D6-G.

### D6-F — Development-only unsigned-delivery gate

**Question.** Approve one non-secret deployment mode value and one boolean gate — proposed as `HOOKFLOW_ENV` (`development` | `production`, required, no default) and `HOOKFLOW_ALLOW_UNSIGNED_DELIVERY` (default `false`) — such that an empty `secret_encrypted` may use the existing unsigned path **only** when mode = `development` and the gate = `true`; production, an unset/unknown mode, or gate = `false` fails closed on an empty secret with no HTTP; a non-empty secret can never reach `client.Do` without successful decryption, validation, and signature construction; invalid or contradictory environment/gate combinations fail startup; and the gate can never be implied by the absence of a value?

**Recommended.** Approve as stated. This is **not** an existing approved contract: the fail-closed posture for configured secrets is already approved, but the repository has no deployment-mode value (§3.6), so the mode and gate are new configuration and are proposed here.

**Alternatives.** (a) Keep the current unconditional unsigned path — rejected: production could send unsigned requests merely because a stored value is empty. (b) Default the gate to `true` and require operators to disable it — rejected: an omission must not weaken security. (c) Infer development from the database host, a debug flag, or the provider choice — rejected: ambiguous and silent. (d) Refuse unsigned delivery entirely — safest, but breaks the existing development/test flow and the two tests that assert no signature header; it is the fallback if the gate is rejected.

**Consequences.** Development and test setups must set mode = `development` and enable the gate; Worker startup fails without a valid mode. In production, an empty secret becomes a durable, paced, no-attempt `secret_missing` failure (D6-G) rather than an unsigned request; the delivery is visible, never silently sent. If D6-G is not approved, D6-F still prohibits unsigned delivery, but the durable representation of `secret_missing` remains unresolved, so the two decisions must be resolved together before implementation. The gate is configuration, not a compile-time tag, and cannot be changed per delivery.

### D6-G — Durable representation of pre-HTTP internal failures

**Question.** A failure before `client.Do` (an empty secret outside the Development gate, a signing-construction fault) is not an HTTP attempt: it must create no `delivery_attempts` row, not increment `attempt_count`, and never fall back to unsigned. The existing durable B5 mechanics can store such a failure with no new column or state, but B5 is scoped to secret unavailability. Approve **either** (a) generalizing that pre-HTTP durable path to carry an explicit reason code — `secret_unavailable`, `secret_missing`, `signing_internal` — with distinct low-cardinality metrics, reusing the same states, fencing, event-horizon precedence, pacing, and ACK-after-commit; **or** (b) a terminal `failed` transition with zero attempts for deterministic internal faults, which is a new transition and is not approved today?

**Recommended.** (a) Generalize by reason code. It preserves U18, reuses approved mechanics, adds no state, column, or disposition value, and keeps the failure visible in `deliveries.last_error`, the retry schedule, and a distinct metric. (b) is cleaner operationally for a deterministic bug but changes approved state-machine semantics, so it needs explicit approval and is not the default.

**Rejected alternatives.** Reuse `RecordDecryptFailure` unchanged — misclassifies a programming fault or an absent secret as a decryption failure and conflates metrics. Hold/requeue through the broker — consumes the 20-attempt broker delivery limit and risks DLQ, while the condition is known and deterministic. Terminalize immediately as `expired` — expiry is reserved for the event horizon. Log and drop — no durable visibility. Write an attempt row with `error_class = internal` — violates U18 outright, because no HTTP execution occurred.

**Consequences.** With (a), a deterministic pre-HTTP fault paces until the event horizon; that is visible and bounded, and it is the same cost B5 already accepts. With (b), the delivery reaches a terminal state sooner but the transition and its operational meaning need approval. Without either, the path must not be implemented: the only existing behavior available is an unbounded requeue, which this package rejects. The `error_class` enum is unchanged either way, and no new table, column, state, or `delivery_attempts` disposition is introduced.

### U25-A — Signing algorithm and key usage

**Question.** Approve HMAC-SHA256 over the Standard Webhooks construction, with the decrypted secret bytes as the HMAC key, the stored plaintext defined as raw key bytes (never a display encoding), and a strict prohibition on using the DEK or KEK for signing?

**Recommended.** Approve as written.

**Alternatives.** (a) Asymmetric signatures (Ed25519/ECDSA) — receiver must obtain and trust a public key; no gain for a shared-secret webhook. (b) RFC 9421 components — more machinery and receiver requirements. (c) HMAC-SHA512 — no interoperability benefit; Standard Webhooks specifies SHA-256.

**Consequences.** If the future API presents `whsec_`-style secrets without defining the decoding step, receivers using standard libraries will compute a different MAC and every delivery will fail verification. This is a contract-level trap and the reason the raw-bytes definition is part of the decision.

### U25-B — Canonical signing input and header set

**Question.** Approve `id.timestamp.body` over exact transmitted bytes, headers `webhook-id`/`webhook-timestamp`/`webhook-signature`, retention of `X-HookFlow-*` as unsigned diagnostics, and no URL/path/query/org/endpoint in the signed content?

**Recommended.** Approve strict Standard Webhooks compatibility.

**Alternatives.** (a) Extended profile: additionally sign method, path, and endpoint id — gains path and tenant binding, loses compatibility with off-the-shelf verifiers and reintroduces URL normalization. (b) Sign a body digest header instead of the body — adds a second representation to keep consistent, with no benefit. (c) Use `event_id` as `webhook-id` — then all endpoints receiving one event share an id, which weakens per-delivery dedupe; `delivery_id` is recommended.

**Consequences.** The extended profile is a one-way interoperability decision: once receivers implement it, reverting breaks them. The recommended profile accepts path-independent replay within the tolerance window, mitigated receiver-side.

### U25-C — Timestamp, replay, and receiver compatibility

**Question.** Approve Unix-seconds timestamps generated per attempt, a ±300 s receiver tolerance, idempotency keyed on `webhook-id` recorded after durable processing, and documentation that delivery is at-least-once with no exactly-once claim?

**Recommended.** Approve.

**Alternatives.** (a) Millisecond timestamps — no benefit, and incompatible with standard verifiers. (b) Timestamp at event creation — breaks every retry beyond the window. (c) No timestamp, body-only HMAC (GitHub style) — loses replay bounding; requires receivers to implement their own replay defense.

**Consequences.** A tolerance below realistic clock skew causes intermittent, hard-to-diagnose rejections; above it widens the replay window. HookFlow cannot enforce the receiver side, so this is a published contract plus documentation.

### U25-D — Signing-key rotation and versioning

**Question.** Approve no key-id header, rotation via snapshot semantics, a documented minimum 24-hour dual-secret acceptance window at the receiver, a single signature per request in V1, and treatment of the scheme as a protocol constant rather than a per-delivery column?

**Recommended.** Approve.

**Alternatives.** (a) Dual signatures per request — requires a keyring plaintext or a schema change; seamless rotation, higher complexity. (b) A key-id header — leaks rotation state and requires receiver lookup logic. (c) Add `deliveries.signing_scheme` now — speculative; no consumer exists.

**Consequences.** With the recommended option, a receiver that retires the old secret immediately will reject in-flight old deliveries for up to 12 hours; that is a documented operational requirement, not a HookFlow defect. Dual signatures would remove that requirement at the cost of the plaintext contract.

### Approval table

The Owner answers each row with `APPROVE`, `MODIFY` (stating the replacement value), or `REJECT`. Only `APPROVE` rows become contracts. **The Rev 2 pre-approval state marked every row `NOT APPROVED`; the Owner approved all eleven rows on 2026-11-24.** The status column below records that approval. Development implementation is a separate status (implemented); production custody/readiness remains not implemented.

| ID | Decision (short) | Recommended value | Status | Owner response |
|---|---|---|---|---|
| D6-A | Cipher and envelope format | AES-256-GCM; `hfse1.` layout; 1,024-byte plaintext; 4,096-byte decode cap; strict parser | OWNER APPROVED | APPROVE (2026-11-24) |
| D6-B | DEK scope and AAD | DEK per endpoint secret version; 64-byte AAD excluding the key slot; add `endpoint_id` to the claim path | OWNER APPROVED | APPROVE (2026-11-24) |
| D6-C | Custody, provider boundary, startup validation | Provider interface + dev file provider only; no env-var KEK; production provider deferred; configuration defects fail startup, envelope tags classify per delivery | OWNER APPROVED | APPROVE (2026-11-24) |
| D6-D | Rotation and reference-based retirement | Rewrap endpoints, never snapshots; keep a KEK while any retained reference exists; purge + audit at zero before destruction; 90/90/72 are horizons, not a calendar | OWNER APPROVED | APPROVE (2026-11-24) |
| D6-E | Budget and failure routing | Add 2 s fifth component (30 > 27); 3 s slack; re-sample after unwrap; crypto failures via B5 | OWNER APPROVED | APPROVE (2026-11-24) |
| D6-F | Development-only unsigned gate | Required mode (`development`/`production`) + gate default false; unsigned only in development with gate; production empty secret fails closed; contradictions fail startup | OWNER APPROVED | APPROVE (2026-11-24) |
| D6-G | Pre-HTTP internal-failure disposition | Generalize B5 mechanics with reason codes (`secret_unavailable`, `secret_missing`, `signing_internal`), distinct metrics, no new state; or terminal `failed` with zero attempts | OWNER APPROVED | APPROVE (2026-11-24) |
| U25-A | Algorithm and key usage | HMAC-SHA256; raw secret bytes; no DEK/KEK signing | OWNER APPROVED | APPROVE (2026-11-24) |
| U25-B | Canonical input and headers | `id.timestamp.body`; `webhook-*` headers; no URL binding | OWNER APPROVED | APPROVE (2026-11-24) |
| U25-C | Timestamp and replay | Unix seconds per attempt; ±300 s; `webhook-id` idempotency; at-least-once | OWNER APPROVED | APPROVE (2026-11-24) |
| U25-D | Signing-key rotation | No key id; snapshot rotation; ≥ 24 h receiver overlap; one signature | OWNER APPROVED | APPROVE (2026-11-24) |

---

## 9. Implementation record (development implemented; production provider absent)

This section was written before implementation and is retained as the pre-implementation plan. The development
runtime now implements the envelope, AAD, provider boundary, development file provider, signing, worker wiring,
configuration validation and the management secret lifecycle. Not implemented: a production KMS/Vault adapter,
production custody, and production readiness. The plan below therefore describes mostly-delivered work; it is not a
claim that every bullet exists verbatim.

### 9.1 Packages and interfaces

New:

- `internal/platform/secrets` — `Encode`, `Decode`, `BuildAAD`, envelope constants, typed errors (`ErrMalformed`, `ErrUnsupportedVersion`, `ErrUnsupportedAlg`, `ErrUnsupportedPurpose`, `ErrUnknownKID`, `ErrAuthFailed`, `ErrProviderDisallowed`, `ErrTransient`, `ErrPlaintextInvalid`), and the stable reason codes used by the pre-HTTP disposition (`secret_unavailable`, `secret_missing`, `signing_internal`). No I/O.
- `internal/platform/kek` — `Provider` interface, `Context`, `Wrapped`, the development file provider, an in-memory test provider, and error classification helpers. Cloud adapters are deliberately absent until D6-C names a target.
- `internal/platform/signing` — a `Signer` value with `Sign(id string, at time.Time, body []byte) (HeaderSet, error)`, an injected clock for tests, and the canonical-string builder. Uses only `crypto/hmac`, `crypto/sha256`, `encoding/base64`.

Modified:

- `internal/app/delivery/store.go` — add `Claim.EndpointID`, populate from the scan, pass through to completion and failure paths as needed; persist the **transmitted** header set (including `webhook-id`, `webhook-timestamp`, and `webhook-signature`) rather than only the snapshot headers, so U25 does not create an audit gap; generalize the pre-HTTP durable method (today `RecordDecryptFailure`) to accept a reason code without changing its SQL states, if D6-G(a) is approved.
- `internal/app/delivery/pointer.go` — add `d.endpoint_id` to both `SELECT`s and `HTTPPreparation.EndpointID`.
- `internal/app/worker/executor.go` — remove the fail-closed placeholder; accept a secret-resolution result and a signer; enforce the unsigned gate for empty secrets; set the `webhook-*` headers after all validation and before `Do`; return a distinct pre-HTTP internal sentinel for signing-construction failure; consume the re-sampled remainder supplied by the caller rather than the frozen `Remaining`, while keeping its own check at the four post-resolution components (`HTTPOptions.RequiredLeaseBudget`, 25 s) and never adding `SecretResolutionBudget` to it; keep redirects disabled and `GetBody = nil`.
- `internal/app/worker/processor.go` — orchestrate the gate, decode/unwrap/open/validate, compute the five-component `PrepareHTTP` requirement (including `SecretResolutionBudget`), re-sample after resolution, and route each failure class to its own reason code and metric (crypto vs `signing_internal` vs `secret_missing`) under a fresh bounded context.
- `internal/platform/config/runtime.go` — add `SecretResolutionBudget` with env override consistent with the existing `HOOKFLOW_WORKER_*` pattern, include it in validation, and add non-secret provider configuration (selector, file path, current `kid`, allowlist), the deployment mode, the unsigned gate, and the startup validation of §4.5.1.
- A read-only retirement-audit command or tool (new, no schema impact) that reports per-`kid` envelope-header references across `endpoints` — including soft-deleted rows, with no `deleted_at IS NULL` filter — and `deliveries`, listing unparseable values rather than skipping them.
- `.gitignore` — add the dev key file and any local secrets directory.
- `docs/architecture/overview.md`, `database-schema.md`, `runtime-readiness-decisions.md`, `docs/operations/runbook.md` — record the contract once approved, the actual envelope, the header set, the rotation and retirement procedure, and the corrected attempt-header statement.

### 9.2 Database impact

None. No migration, no new column, no new index, no constraint. `endpoints.secret_encrypted`, `deliveries.secret_encrypted`, and `deliveries.endpoint_id` already exist with compatible types. The `error_class` enum is unchanged because every pre-HTTP path writes no attempt row. The retirement audit is read-only and requires no index; if reference counting ever becomes too slow at production volume, adding one is a separate decision with its own cost, not an implicit part of this package. The unsigned gate and provider validation are configuration only; they persist nothing.

### 9.3 Tests

Unit:

- Envelope golden vectors with a deterministic nonce and DEK injected through a test seam; exact byte comparison.
- Round-trip for every legal `kid_len`, `wrapped_len`, and plaintext length boundary (1, 16, 1,024).
- Tamper matrix: flip each field in turn; expect a specific typed error for the enclosing region, never a panic.
- Cross-organization and cross-endpoint transplant; expect `ErrAuthFailed`.
- Rewrap: after replacing `kid` and wrapped bytes, decryption still yields the same plaintext and the ciphertext region is byte-identical.
- Parser negatives: wrong magic, version, alg, purpose, provider; empty/oversized `kid`; illegal `kid` bytes; `wrapped_len` 0 and 1,025; truncation at every offset; trailing bytes; padded base64; non-canonical base64; over-long input string.
- Fuzz target for the decoder; all failures must be typed errors.
- Provider: missing file, 0644 permissions, malformed JSON, unknown `current`, unknown `kid`, wrong key length, wrong KEK.
- Signing: RFC 4231 HMAC-SHA-256 test cases (use the published vectors, not invented ones); canonical-string golden values; header names and prefix; per-attempt timestamp change with an unchanged id.

PostgreSQL integration:

- Endpoint envelope copied into a delivery snapshot decrypts with the same organization/endpoint context.
- Mixed `kid` rows across a rotation both decrypt.
- `RecordDecryptFailure` invariants: no attempt row, unchanged `attempt_count`, event-horizon precedence over lease expiry, fresh time, fencing, `AckEligible` only after commit.
- A completed attempt's `request_headers` contains the transmitted `webhook-id`, `webhook-timestamp`, and `webhook-signature` values, not only the snapshot headers, and contains no secret.
- `endpoint_id` is present in the claim and preparation for every delivery.
- Pre-HTTP disposition: `secret_missing` and `signing_internal` write no `delivery_attempts` row, leave `attempt_count` unchanged, persist a bounded scrubbed `last_error`, and acknowledge only after commit; neither increments the decryption-failure metric.

Configuration and startup:

- Mode/gate matrix: development + gate ⇒ unsigned allowed; development + no gate ⇒ no HTTP; production + gate ⇒ startup failure; production + no gate ⇒ empty secret fails closed with no HTTP; unset/unknown mode ⇒ startup failure.
- Provider startup matrix: unset/unknown provider, local provider outside development, missing/0644/malformed key file, unknown `current`, unknown configured `kid`, empty allowlist ⇒ Worker refuses to start.
- Per-envelope matrix: unknown provider tag, malformed envelope, unknown `kid` ⇒ per-delivery permanent through B5, never a startup failure and never a request.
- Re-sample: a provider stub that consumes part of the lease either yields a sufficient remainder or starts no HTTP; the frozen `Remaining` value is never the deciding input.
- Audit: reference counts are correct for reused `kid` values across endpoints (including soft-deleted endpoint rows) and deliveries; unparseable values are listed, not skipped; the audit decrypts nothing.

Worker integration:

- An `httptest` receiver recomputes the signature from the received headers and body and accepts it.
- A retry reuses `webhook-id`, carries a later timestamp, and produces a valid signature.
- Failure injection for each pre-HTTP row of §6.7 asserts zero HTTP requests via a counting `RoundTripper`.
- A slow provider stub near the budget either starts HTTP with a sufficient remainder or starts nothing.

Security tests:

- Logs, metrics, and error strings contain no plaintext, no wrapped bytes, no envelope bytes, and no KEK material.
- Diagnostics are bounded at 500 runes and pass the existing scrubber.
- No code path reaches `client.Do` with a non-empty secret and no signature.

### 9.4 Compatibility and rollout order

1. Merge the codec, provider, and signing packages with tests. Additive; nothing calls them.
2. Add configuration: deployment mode, unsigned gate, provider startup validation, and the budget validation. This is a startup behavior change — a Worker without a valid mode does not start — but no delivery behavior changes while the gate is off and no secret is configured.
3. Wire the read path: `endpoint_id` propagation, then the gate for empty secrets, then decode/unwrap/open/validate, then signing, then the post-resolution re-sample. Update the two tests that currently assert no signature.
4. Update documentation and the runbook, including the rotation procedure, the reference audit, the receiver overlap contract, and the new startup refusals.
5. Add rotation/audit tooling and per-`kid` usage metrics. Do not retire any version that has wrapped a delivery snapshot until the retention purge exists (U2) and the audit reports zero references; likewise, do not retire a version referenced by an endpoint envelope until every such endpoint row — including a soft-deleted row — has been rewrapped or purged and the audit reports zero.
6. Only after D6-C names a deployment target, add that provider adapter.

Receiver-side secret distribution is a product prerequisite that is out of scope; until it exists, production use of non-empty secrets is an operator procedure.

### 9.5 Acceptance criteria

- Every §6.7 **pre-HTTP** failure case produces no HTTP request and no attempt row, and acknowledges only after a durable commit.
- An empty secret produces an unsigned request **only** in development with the gate enabled; in every other configuration it produces no request, no attempt row, and a `secret_missing` reason.
- A signing-construction fault produces no request, no attempt row, no `attempt_count` change, and reason `signing_internal` on a distinct metric — never a decryption-failure metric.
- A valid envelope produces exactly one request with a signature that a standard-compliant verifier accepts.
- The completed attempt's `request_headers` audit row contains the transmitted `webhook-*` values and no secret-bearing header.
- Rewrapping an endpoint envelope changes only the key-slot region and leaves decryption results identical.
- The decoder rejects every malformed input with a typed error and no panic, under fuzzing.
- The lease invariant includes the secret-resolution budget as a fifth component, the 3 s residual is treated as slack, and configuration load rejects a sum that is not strictly less than the lease.
- Time is re-sampled after secret resolution; no HTTP starts on the strength of the frozen pre-unwrap remainder.
- Startup validation refuses every inconsistent provider/mode/gate configuration before a message is consumed, while an unknown provider tag inside a stored envelope stays a per-delivery failure.
- A KEK version can be retired only with a recorded reference audit at zero references after purge, counting soft-deleted endpoint rows until their ciphertext is actually purged; no code path deletes a key on elapsed time alone.
- No secret, DEK, KEK, wrapped blob, or envelope byte appears in any log, metric, error, or panic message.
- `go vet`, the configured linters, unit tests, PostgreSQL integration tests, and Worker integration tests pass locally; hosted CI remains a separate, unverified gate.

---

## 10. Evidence and citations

**[V] Verified against the cited primary sources.** Claims are limited to what the cited text states.

| # | Source | Claim supported | Confidence |
|---|---|---|---|
| V1 | [Standard Webhooks v1.0.0 specification](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md) | Signed content is `msg_id.timestamp.payload`; ids and timestamps must not be user-controlled and must not contain `.`; the payload sent must equal the payload signed; HMAC-SHA256; secret 24–64 random bytes presented base64 with a `whsec_` prefix; header values `v1,<base64>`; headers named `webhook-id`, `webhook-timestamp` (integer Unix seconds), `webhook-signature`; the signature header is a space-delimited list to permit rotation; verification uses constant-time comparison, a timestamp tolerance, and `webhook-id` idempotency; symmetric keys must be unique per endpoint | High |
| V2 | [Stripe webhook signatures](https://docs.stripe.com/webhooks/signature) | Multiple active secrets during rotation (immediate or up to seven days), one signature per secret; only the `v1` HMAC is verifiable; debug fields are unsigned; the raw body must be used exactly; 3xx is a failure | High |
| V3 | [Go `crypto/cipher` (go1.27.4)](https://pkg.go.dev/crypto/cipher) | `NewGCM` uses the standard 12-byte nonce; the nonce must be unique for all time for a given key; no more than 2^32 random nonces per key; `NewGCMWithRandomNonce` prepends a 96-bit nonce with `Overhead` 28; `NewGCMWithTagSize` accepts 12–16 bytes and non-standard sizes are discouraged | High |
| V4 | [Go `crypto/hmac` (go1.27.4)](https://pkg.go.dev/crypto/hmac) | `New(h func() hash.Hash, key []byte) hash.Hash` has no error return; `Equal` compares without leaking timing information; FIPS 198 basis | High |
| V5 | [Go `crypto/rand` (go1.27.4)](https://pkg.go.dev/crypto/rand) | `Read` fills the buffer, never returns an error, and crashes irrecoverably rather than returning one; `Reader` is a CSPRNG per platform; `Text` provides at least 128 bits of randomness | High |
| V6 | [OWASP Cryptographic Storage Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html) | Prefer authenticated modes (GCM/CCM) and AES-256; do not implement custom algorithms; rotate on cryptoperiod and on usage limits, citing NIST SP 800-57 and SP 800-38D §5.2.1.1/§8; use key IDs or re-encryption for old data; never hard-code or commit keys; restrict file permissions; avoid environment variables for keys; separate keys from data; envelope encryption with DEK/KEK and a KEK at least as strong | High |
| V7 | [AWS KMS concepts](https://docs.aws.amazon.com/kms/latest/developerguide/concepts.html) | Plaintext and ciphertext are never stored within KMS; automatic key-material rotation is optional and rotates yearly for CMKs; older key material is retained for decryption while only the active material protects new data; key IDs are unrelated to key material; aliases can be repointed | High |
| V8 | [Google Cloud KMS envelope encryption](https://docs.cloud.google.com/kms/docs/envelope-encryption) | Generate DEKs locally; generate a new DEK per write; never store a plaintext DEK; use AES-256-GCM; the KEK never leaves KMS; KMS is designed for KEKs and supports envelope encryption | High |
| V9 | [Vault Transit secrets engine](https://developer.hashicorp.com/vault/docs/secrets/transit) | Encryption as a service without returning key material; ciphertext carries `vault:vN:` so the key version is recoverable; rotation creates a new version and old data remains decryptable via the keyring; `rewrap` upgrades ciphertext without revealing plaintext; states that AES-GCM keys should be rotated before roughly 2^32 encryptions, citing NIST SP 800-38D | High |
| V10 | [RFC 9421 HTTP Message Signatures](https://www.rfc-editor.org/rfc/rfc9421.html) | Signs a signature base built from selected HTTP components and derived components; defines `hmac-sha256` among its algorithms; carries `Signature-Input` and `Signature` fields; key material is often identified by an explicit key id; includes a security section on canonicalization attacks | High |
| V11 | [NIST SP 800-38D](https://csrc.nist.gov/pubs/sp/800/38d/final) | GCM specification, IV requirements, and per-key invocation limits | **Not read during preparation** — the fetched PDF was binary and unusable. The specific numeric bound used in this document (2^32) is taken from V3 and V9, both of which state it explicitly. Do not quote section text from the standard on the strength of this document. |
| V12 | [NIST SP 800-57 Part 1 Rev. 5](https://csrc.nist.gov/pubs/sp/800/57/pt1/r5/final) | Key states and cryptoperiod concepts used by OWASP's rotation guidance | **Not fetched during preparation** — referenced only through V6 and V9. |

**[R] Repository findings** are cited inline in §3 and §4 with file paths and symbols. The relevant references re-read for this revision are: `0005_endpoints.up.sql:10-11,20,35,42`; `0008_deliveries.up.sql:2,11,15`; `0009_delivery_attempts.up.sql:1-48`; `executor.go:158-163,176,207,213-220,222`; `processor.go:94-130`; `pointer.go:88,386-400,419`; `store.go:103-119,472-579,777-804`; `runtime.go:418-423,442-446`; `config.go`; `cmd/worker/lifecycle.go`; `database-schema.md` §4.5/§4.5.1/§6; `overview.md` §5.2/§7/§8 and its 2026-11-24 reconciliation note; `runtime-readiness-decisions.md` §8.1 and the supersession notes added there on 2026-11-24; `worker_integration_test.go:100-103`; `executor_test.go:192-202`. Absence evidence for the AAD/codec: the only matches for `secret_encrypted`, `SecretCiphertext`, or decrypt/encrypt in Go files are the storage/snapshot fields, the fail-closed guard, the B5 disposition, and tests — no `HF1`, `hfse1`, `hookflow.secret.v1`, `crypto/aes`, `cipher.NewGCM`, or AAD builder. The reconciliation evidence is recorded in [`../operations/d6-u25-cross-document-reconciliation-report.md`](../operations/d6-u25-cross-document-reconciliation-report.md).

**[E] Engineering recommendations** are identified wherever they appear and are not presented as verified fact. The main ones are the 2-second secret-resolution budget and its re-sampling requirement, the endpoint-only rewrap policy, the 24-hour receiver overlap, the 300-second tolerance, the reference-based retirement procedure and its audit-at-zero rule, the Development-only unsigned gate, the generalized reason-coded pre-HTTP path, and the startup-versus-envelope classification split.

**[O] Unresolved Owner decisions** are the eleven rows of §8. None is approved.

**Not verified, and therefore not claimed:** any provider latency figure, any throughput or benchmark number, any Go compatibility statement beyond the cited go1.27.4 documentation, any claim that a specific third-party verifier library supports these headers, and any claim that encryption or signing is implemented in this repository.

---

## 11. What this document does not do

- It does not approve, implement, or schedule D6 or U25.
- It does not modify code, tests, dependencies, migrations, schema, environment files, Compose, or CI.
- It does not run migrations or alter any database.
- It does not select a production KEK provider.
- It does not change delivery semantics; at-least-once stands, and no timestamp is claimed to provide exactly-once.
- It does not reintroduce `delivery.cancel`, `cancelled`, or any cancelled state.
- It does not weaken fencing, lease, retry, scheduling, Recovery, or disposition rules.
- It does not authorize an unsigned fallback when a non-empty encrypted secret is configured.
- It does not authorize unsigned delivery for an empty secret outside an approved Development gate, and it does not approve that gate.
- It does not authorize using the KEK or wrapping key for request signing.
- It does not authorize retiring a KEK on elapsed time while any retained endpoint envelope or delivery snapshot can reference it.
- It does not create a new delivery state, column, or `delivery_attempts` disposition for pre-HTTP failures; it asks the Owner to choose between reusing the existing mechanics with a reason code and an explicitly approved terminal transition.
- It does not implement the retention purge (U2); it states that the current repository cannot satisfy the retirement precondition without it.
- It does not review production readiness generally.
