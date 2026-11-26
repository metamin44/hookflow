# HookFlow — Final D6/U25 Implementation Readiness Review and Integrated Execution Plan

**Document status:** READ-ONLY REVIEW AND PLAN. This document is not implementation authorization.
**Date:** 2026-11-24
**Repository baseline:** `c6db32f` (`Initial commit`), Go 1.27.4, PostgreSQL 18 schema with 13 migration units.
**Owner-approved baseline:** the D6-A through D6-G and U25-A through U25-D constraints supplied in the Owner's review brief dated 2026-11-24.

**Evidence convention.** Every repository claim below is grounded in the working tree at the baseline above. References use `path:line` and are read-only observations. The architecture proposal remains the normative byte-level definition for the approved envelope; this report does not silently replace it.

**Documentary-status note.** The brief states that D6 and U25 are now Owner-approved. The repository's own documents still record every D6/U25 row as `NOT APPROVED` (`docs/architecture/d6-u25-decision-proposal.md:4`, `:873-883`; `docs/operations/runbook.md:172`). This report treats the Owner's brief as the approval record and records the documentary lag as finding F-01. It does not edit those documents, does not mark any new decision approved, and does not claim that the repository already reflects the approval.

> **Phase 6.1 addendum (2026-11-24).** Finding F-01 is resolved by the Phase 6.1 documentation reconciliation:
> `d6-u25-decision-proposal.md` Rev 3, `runtime-readiness-decisions.md`, `overview.md` Revision 20 and the runbook now
> record D6-A…D6-G and U25-A…U25-D as **OWNER APPROVED** architecture, the development implementation as
> **IMPLEMENTED**, and production KMS/Vault custody/readiness as **NOT IMPLEMENTED / NOT READY**. Sections that
> describe the absence of an endpoint write path, API-key authentication or a secret lifecycle describe the
> pre-Phase 6 tree; Phase 6 implemented the management API and secret rotation in development, while the production
> blockers in this report remain. This report's read-only analysis and its production BLOCKED verdicts are preserved
> unchanged.

---

## 1. Executive verdict

| Readiness question | Verdict | Meaning |
|---|---|---|
| Development implementation | **READY WITH DEPENDENCY** | The envelope, AAD, provider interface, development file provider, signing, worker wiring, configuration validation, and most tests can be implemented without a database migration. Four dependencies must be resolved first: the D6-G permanent-failure mechanism, `endpoint_id` propagation, the missing snapshot-creation path for end-to-end proof, and the transmitted-header audit change. |
| Production deployment | **BLOCKED** | A production KEK provider is unselected and unadapted; no deployment mode or unsigned-delivery gate exists; no endpoint-secret creation/rotation path exists; no Events/Deliveries/Attempts/outbox purge exists; no KEK reference audit or retirement procedure exists; receiver secret distribution is absent. The development file provider is explicitly not production-ready. |
| Production KEK retirement | **BLOCKED** | No production provider, no wrap-disable capability, no purge of every envelope-bearing table, no reference-audit tool, and no proof that no new reference can be created during retirement. Soft-deleted endpoints and immutable delivery snapshots retain envelopes. The current repository cannot establish the required "zero references" precondition. |

No workstream is unqualified **READY**. None of the seven assessed workstreams is **NOT REQUIRED**; the only **NOT REQUIRED** classifications below are the supplementary rows for new schema/state and the production-only provider adapter. The distinction between development and production is material: building the codec and signing path does not make the development file provider, the missing secret lifecycle, or the missing retention machinery production-ready.

The core technical finding is favorable: the approved D6/U25 design fits the existing schema. `endpoints.secret_encrypted` and `deliveries.secret_encrypted` are `text NOT NULL`; `deliveries.endpoint_id` already exists; `delivery_attempts.request_headers` is `jsonb`; `deliveries.state` already includes `failed`; `deliveries.last_error` is bounded at 500 characters. The core implementation therefore needs **no migration**. The blockers are behavioral, operational, and integration gaps, not missing columns.

---

## 2. Owner-approved baseline confirmation

This review carries the following baseline forward exactly. Nothing below reopens, weakens, or reinterprets it. No new decision is marked Owner-approved by this report.

| ID | Binding effect carried into this plan |
|---|---|
| D6-A | AES-256-GCM; fresh 32-byte DEK per secret version; `hfse1.` prefix; strict bounded parser; 1,024-byte plaintext maximum; 12-byte nonce; 16-byte tag; envelope carries version, algorithm, purpose, `kid`, provider tag, wrapped DEK, nonce, ciphertext, and tag; fail closed. |
| D6-B | Exact 64-byte AAD: `hookflow.secret.v1` (18) + algorithm (1) + purpose (1) + data nonce (12) + raw organization UUID (16) + raw endpoint UUID (16). The key slot is excluded. Endpoint envelopes may be rewrapped; delivery snapshots never. |
| D6-C | KEK never in the database, source, images, logs, or ordinary configuration; provider abstraction; development file provider with `0600`; production KMS/Vault required; startup validation separated from per-envelope classification; no vendor selected by this decision. |
| D6-D | Retention 90/90/72; every persisted envelope counts, including active endpoints, soft-deleted endpoints, and immutable delivery snapshots; KEK retirement only after an auditable zero-reference result; currently operationally blocked. |
| D6-E | Lease 30 s > 10 s HTTP + 2 s handshake + 3 s post-process + 10 s shutdown + 2 s secret resolution; re-evaluate the remaining budget after resolution; never start HTTP when the remaining budget is insufficient. |
| D6-F | Unsigned delivery only in development with an explicit gate; production and unknown/ambiguous configurations fail closed; no silent fallback; contradictions fail startup. |
| D6-G | Preserve U18: no attempt row, no attempt-count increment, no HTTP, durable fenced persistence before ACK, sanitized errors, pacing. Preferred direction is to generalize the existing fenced `RecordDecryptFailure` if the state model can safely represent all pre-HTTP classes (transient provider, permanent/malformed envelope, invalid/unusable signing configuration, internal pre-HTTP signing fault). A permanent failure must not become an invisible endless retry loop. Document the limitation and the smallest coherent alternative without implementing it. |
| U25-A | HMAC-SHA256 over raw decrypted secret bytes; minimum 16 bytes, recommended 32 random; no key reuse for encryption; constant-time verification where applicable. |
| U25-B | Input is `webhook-id + "." + webhook-timestamp + "." + body-bytes`; headers are `webhook-id`, `webhook-timestamp`, `webhook-signature: v1,<base64>`; stable delivery ID; fresh timestamp and signature per attempt; sign exact transmitted bytes; no URL/path in the signature; persist transmitted webhook audit headers without secrets. |
| U25-C | Unix seconds; ±300 s receiver tolerance; at-least-once; `webhook-id` idempotency; document receiver compatibility. |
| U25-D | Controlled current/previous signing-key overlap, proposed at least 24 hours; new key for subsequent attempts; old key retained through overlap; encryption rotation and signing rotation are independent; no key identifier; assess receiver coordination without silently adding a header. |

Two consequences of this baseline deserve emphasis before the evidence map:

1. **A development provider is not a production provider.** D6-C approves an interface and a development file provider while explicitly deferring production selection. Production readiness cannot be inferred from a working development provider.
2. **"No new state" does not mean "no state-machine change."** D6-G's preferred B5 generalization is safe for transient provider failures. It is not safe for deterministic permanent faults unless the state model terminalizes them; otherwise the delivery is paced until the event horizon. That distinction is the one genuine mechanism decision left open by the baseline and is treated in Findings F-02 and Section 12.

---

## 3. Repository evidence and current-state map

### 3.1 Repository, dependency, and Git baseline

- Module: `github.com/metamin44/hookflow`, `go 1.27.4` (`go.mod:1-3`). Direct dependencies are `golang-migrate/migrate/v4 v4.20.1`, `jackc/pgx/v5 v5.13.0`, and `rabbitmq/amqp091-go v1.17.0` (`go.mod:5-10`). No crypto/provider/signing dependency exists.
- HEAD is `c6db32f`. The only tracked modification is `.gitignore`; every other repository path is untracked in this worktree. This is pre-existing state and was preserved.
- `internal/app/event`, `internal/modules/event`, and `internal/modules/endpoint` contain only `.gitkeep`. There is no endpoint CRUD, event-ingest, fan-out, or delivery-creation implementation in the repository.
- `cmd/api` exposes only `/health/live` and `/health/ready` (`internal/platform/http/health.go:13-14`, `:52-55`; `cmd/api/integration_test.go:49`). There is no public endpoint-secret or delivery API.

### 3.2 Database schema evidence

| Area | Evidence | Current effect on D6/U25 |
|---|---|---|
| Endpoint secret | `db/migrations/0005_endpoints.up.sql:10` `secret_encrypted text NOT NULL`; `:11` `signing_scheme text NOT NULL DEFAULT 'hmac_sha256'`; `:20` `deleted_at`; `:33-42` live/deleted indexes | The envelope can be stored without a migration. Soft-deleted rows retain their envelope indefinitely. There is no version column; versioning must live inside the envelope. |
| Delivery snapshot | `db/migrations/0008_deliveries.up.sql:2` immutable-snapshot comment; `:11` `endpoint_id uuid NOT NULL`; `:15` `secret_encrypted text NOT NULL`; `:34-35` endpoint FK `ON DELETE RESTRICT`; `:43-45` states `pending/retrying/succeeded/failed/expired`; `:54-62` completed/scheduling/lease checks | `endpoint_id` already exists for AAD, but it is not selected into the claim/preparation. A terminal `failed` row with zero attempts satisfies the state and completion checks. Immutability is a comment, not a database constraint. |
| Attempts | `db/migrations/0009_delivery_attempts.up.sql:2-3` write-on-completion only; `:6-49` `finished_at NOT NULL`, `request_headers jsonb`, unique `(delivery_id, attempt_number)`, `error_class` enum limited to `timeout/connect/tls/dns/http_status/internal` | U18 is structurally enforced: there is no start row. A pre-HTTP failure must not insert an attempt. The current enum has no pre-HTTP reason values, which is consistent with writing no attempt row. |
| Outbox | `db/migrations/0010_outbox_messages.up.sql:7-80` statuses `pending/publishing/published/failed`, fenced lease shape, unique `(delivery_id, sequence)` | Retry/decrypt-failure pacing inserts a new outbox row with a new sequence. A permanent pre-HTTP loop therefore creates one outbox row per paced attempt. |
| Events | `db/migrations/0007_events.up.sql:5-35` `received_at`, `expires_at NOT NULL`, `idx_events_expires` | Event horizon is the pre-HTTP retry bound today. Recovery expires pending/retrying deliveries at this horizon (`internal/app/recovery/sweep.go:376-407`, `:451-503`). |
| Idempotency | `db/migrations/0013_idempotency_records.up.sql`; `docs/architecture/database-schema.md:1072-1073` 72-hour window | Only implemented cleanup job is idempotency (`cmd/worker/lifecycle.go:73`), not event/delivery/attempt purge. |
| Audit logs | `db/migrations/0012_audit_logs.up.sql:5-31`; `organization_id uuid NOT NULL`; action text; metadata jsonb | A global KEK audit event has no natural organization scope. Persisting it in this table requires a tenancy decision or an external audit record. |

### 3.3 Worker and delivery runtime evidence

- `internal/app/worker/executor.go:158-163` fails closed for **any** non-empty `secret_encrypted` with `ErrSecretUnavailable`; it produces no HTTP request. This is the current placeholder for the unapproved signed path.
- `internal/app/worker/executor.go:176` enforces the four-component `RequiredLeaseBudget()` (10 + 2 + 3 + 10 = 25 s) against the frozen `HTTPPreparation.Remaining`. There is no secret-resolution component and no post-resolution re-sample.
- `internal/app/worker/executor.go:206` builds the request with `strings.NewReader(preparation.EventPayload)`; `:215-217` adds `X-HookFlow-*` diagnostics; `:220` removes `GetBody`; redirects are disabled (`:118-121`). No `webhook-*` header is built.
- `internal/app/worker/processor.go:93-94` prepares with the four-component sum; `:114-126` maps `ErrSecretUnavailable` to `RecordDecryptFailure`, pacing, and ACK eligibility after commit; `:137-163` completes HTTP outcomes. There is no reason-code model, no signer, and no secret resolver.
- `internal/app/delivery/pointer.go:386-400` `HTTPPreparation` has **no `EndpointID`**; both claim projections select `d.url, d.http_headers::text, d.secret_encrypted` but not `d.endpoint_id` (`internal/app/delivery/pointer.go:88-90`, `:416-427`; `internal/app/delivery/store.go:134-141`).
- `internal/app/delivery/store.go:320-460` `Complete` locks the delivery, samples fresh database time, inserts one attempt, updates the delivery with the exact `dispatch_seq`/`lease_token` fence, inserts the retry outbox row, and commits. `internal/app/delivery/store.go:472-578` `RecordDecryptFailure` takes the same fenced path but writes **no attempt** and does **not** increment `attempt_count` (`:792-804`); it advances `dispatch_seq`, paces `next_attempt_at`, clears the lease, and inserts a new outbox row when a retry is due.
- `internal/app/delivery/store.go:697-707` persists `locked.requestHeaders` through `sanitizeRequestHeaders` (`:716-766`), i.e. the **snapshot** headers, not the actual transmitted header set. It does not add `webhook-*` values.
- `internal/app/worker/consumer.go` uses application-managed generations, prefetch semaphore, a 20 s work budget (`LeaseDuration - ShutdownReserve`), ACK/reject-requeue/hold, and channel close to requeue. Fencing and stale-handle handling are established.
- `internal/app/publisher/session.go` and `internal/app/outbox/store.go` preserve T1 claim-before-publish, confirm/return-aware T2, and fenced completion. The retry path's delivery-update-then-outbox-insert ordering is inside one transaction.

### 3.4 Configuration evidence

- `internal/platform/config/config.go:14-19` loads only the six database variables. `internal/platform/config/runtime.go` adds RabbitMQ, publisher, worker, recovery, and idempotency settings.
- `internal/platform/config/runtime.go:352-367` defines `DeliveryWorker`; defaults at `:442-452` are lease 30 s, HTTP 10 s, handshake 2 s, post-process 3 s, shutdown 10 s, decrypt pacing 30 s. Validation at `:418-423` sums only the four components and requires `LeaseDuration > reserved`.
- There is no `HOOKFLOW_ENV`, deployment-mode value, unsigned gate, provider selector, key path, current `kid`, allowlist, or `SecretResolutionBudget`. `docs/operations/configuration.md:107` and `docs/operations/runbook.md:166` state the current behavior accurately: non-empty secret never reaches HTTP; empty-secret snapshots execute unsigned HTTP unconditionally; no gate exists.
- `.env.example` contains no mode, gate, provider, key path, or secret-resolution setting. `.gitignore` ignores `.env`/`.env.*` but has no dev key-file or secrets-directory entry.

### 3.5 Test and CI evidence

- Unit tests pin the current unsigned behavior: `internal/app/worker/executor_test.go:202-203` fails if `X-HookFlow-Signature` is non-empty; `test/integration/worker_integration_test.go:100-103` asserts it is empty and checks the diagnostic headers.
- The integration harness seeds an endpoint with `secret_encrypted=''` (`test/integration/harness_test.go:205-207`) and a delivery snapshot with the spec's secret (`:343-353`). It requires an isolated database (`:129`) and an idle dispatch queue (`:165`), cleans only its own rows (`:215-225`), and hard-deletes endpoints in test cleanup (`:223`). No production code deletes or updates endpoints.
- Pre-HTTP failure coverage already exists: `internal/app/delivery/store_integration_test.go:1227-1517` covers paced retry, near-expiry, expired event/lease, fencing, and builder failure; `internal/app/worker/processor_test.go:159-168` covers the secret-unavailable durable path; `TestExecutorSecretUnavailableFailsClosed` asserts zero network requests (`executor_test.go:282-300`).
- CI (`.github/workflows/ci.yml`) runs gofmt, vet, build, unit, module verification and tidiness; integration matrix RabbitMQ 4.4.1/4.3.8 with PostgreSQL 18, rejects skipped tests, uses `-p 1`, proves `down 13`/`up`; race job; advisory golangci-lint. This is a sound base. Hosted CI remains unverified in this review; no workflow run was performed.

### 3.6 Gap inventory (expanded from the proposal's G1–G12)

| ID | Gap | Evidence | Effect |
|---|---|---|---|
| G1 | No envelope codec or strict parser | no `internal/platform/secrets`; `go.mod` has no crypto helper | A |
| G2 | No DEK/KEK separation or provider | no `internal/platform/kek`; no config | A, B |
| G3 | `endpoint_id` absent from claim/preparation/SQL | `pointer.go:386-400`, `:88-90`, `:416-427`; `store.go:134-141` | A, C, D |
| G4 | No secret-resolution budget | `runtime.go:418-423`; `processor.go:93`; `executor.go:176` | D |
| G5 | No signer; tests pin "no signature" | `executor.go:158-163`; `executor_test.go:202-203`; `worker_integration_test.go:103` | E |
| G6 | No signing-scheme snapshot on deliveries | `0005` has `signing_scheme`; `0008` has no scheme column | E, U25-D |
| G7 | Attempt audit omits transmitted headers | `store.go:697-707` uses snapshot headers | E, U25-B |
| G8 | No key-file exclusion; no deployment mode | `.gitignore`; `config.go`; `runtime.go` | B, D |
| G9 | No U18-safe representation for permanent pre-HTTP faults | `decryptFailureUpdateSQL` only paces; no terminal path | D |
| G10 | Empty-secret unsigned path ungated in code and docs | `executor.go` removes only the non-empty guard; docs admit unconditional unsigned | D, production |
| G11 | No Events/Deliveries/Attempts/outbox purge; endpoints outside retention | only idempotency sweep in `cmd/worker/lifecycle.go:73` | F |
| G12 | No provider/key/startup validation | no provider config or construction | B, production |

---

## 4. Readiness matrix for every workstream

Classification uses the required four labels. The "Production" column is separate because development code readiness does not imply deployability.

| Workstream | Development implementation | Production deployment | Evidence summary | Blocking dependency |
|---|---|---|---|---|
| **A. Envelope and cryptography** | **READY WITH DEPENDENCY** | **BLOCKED** | Schema stores the envelope; AAD needs `endpoint_id`; no codec. | Add `endpoint_id`; define exact byte codec from the approved proposal; production needs a provider. |
| **B. Provider lifecycle** | **READY WITH DEPENDENCY** | **BLOCKED** | Interface and dev file provider can be built; production KMS/Vault unselected; no startup validation. | D6-C production selection and adapter; key-file hygiene; provider availability behavior. |
| **C. Secret storage and snapshot lifecycle** | **READY WITH DEPENDENCY** | **BLOCKED** | `endpoints`/`deliveries` can store envelopes; no endpoint/event module or delivery creation path; harness seeds only empty secrets. | Endpoint secret create/rotate path; fan-out snapshot creation; immutability enforcement. |
| **D. Worker and pre-HTTP state machine** | **READY WITH DEPENDENCY** | **BLOCKED** | Fencing, no-attempt B5, expiry precedence are solid; D6-G permanent mechanism undecided; no budget; no gate; transmitted headers missing. | D6-G decision; `SecretResolutionBudget`; deployment mode/gate; header audit. |
| **E. Signing and HTTP correctness** | **READY WITH DEPENDENCY** | **BLOCKED** | No signer; no `webhook-*` headers; tests pin unsigned. | Signer package; exact-body invariant; receiver coordination; production key distribution. |
| **F. Retention, purge, and KEK retirement** | **BLOCKED** | **BLOCKED** | No events/deliveries/attempts/outbox purge; endpoint soft delete; snapshots retained; no audit tool. | U2 purge scope; endpoint purge policy; audit tool; provider wrap-disable; retirement procedure. |
| **G. Test and CI readiness** | **READY WITH DEPENDENCY** | **READY WITH DEPENDENCY** | Quality/integration/race/lint jobs exist; harness isolation; new crypto/signing tests must be added; hosted CI unverified. | Add deterministic vectors/fault tests; run hosted CI; keep no-skip gate. |
| **Core new schema/state** | **NOT REQUIRED** | **NOT REQUIRED** | Approved codec/signing/reason codes fit existing columns and states; terminal zero-attempt `failed` fits existing checks. | None for the recommended D6-G alternative. |
| **Production provider adapter** | **NOT REQUIRED (development)** | **BLOCKED** | D6-C explicitly defers vendor selection. | Owner must name provider and deployment target. |

### 4.1 Workstream A — envelope and cryptography

The schema is ready; the code is not. The nearest positive evidence is that `endpoints.secret_encrypted` and `deliveries.secret_encrypted` are unbounded `text` and already `NOT NULL` (`0005:10`, `0008:15`), so the approved envelope has a home without migration. The AAD contract requires the raw endpoint UUID, which exists in `deliveries.endpoint_id` (`0008:11`) but is absent from both claim projections and `HTTPPreparation`. The codec, strict parser, bounds, nonce generation, and tamper classification are entirely new. Development work can proceed after the codec contract and `endpoint_id` plumbing are fixed; production is blocked by provider selection.

### 4.2 Workstream B — provider lifecycle

The development file provider is implementable with no schema. The production path is not: D6-C approves the abstraction but selects no KMS/Vault, and no startup validation exists. A production deployment therefore cannot know which provider is trusted, whether the key file is `0600`, which `kid` is current, or whether an unknown envelope tag is a startup defect or a per-delivery classification. The provider must expose enough for a wrap-disable phase (`CurrentKID`, `Rewrap`, and a way to reject new wraps under a retiring `kid`); otherwise retirement cannot be operationally gated.

### 4.3 Workstream C — secret storage and snapshot lifecycle

This is the largest end-to-end dependency. The database can store envelopes, but **no production code creates an endpoint secret, creates a delivery, or copies an endpoint envelope into a delivery snapshot**. The only writer is the integration harness, and it writes an empty secret (`harness_test.go:205-207`). The "same transaction as delivery insert" contract in Section 6.3 is therefore a design obligation, not a current property. Development can implement a snapshot helper and test it against fixtures; production is blocked until the endpoint/ingest/fan-out path exists and endpoint secret creation and rotation become real operations.

### 4.4 Workstream D — worker and pre-HTTP state machine

The durable machinery is the strongest part of the repository. `AcquirePointer` locks the exact delivery and verifies the published outbox sequence; `PrepareHTTP` revalidates fence, event horizon, and lease; `Complete` and `RecordDecryptFailure` are fenced and commit before ACK; recovery expires at the event horizon. The gaps are specific: `endpoint_id`, the D6-G permanent mechanism, the secret-resolution budget, the deployment mode/gate, and transmitted-header persistence. The transient B5 path can be generalized safely. The permanent path cannot use B5 alone without accepting a paced loop until the event horizon.

### 4.5 Workstream E — signing and HTTP correctness

No signer exists. The current executor refuses any non-empty secret (`executor.go:158-163`), and both the unit and integration tests assert the absence of `X-HookFlow-Signature` (`executor_test.go:202-203`, `worker_integration_test.go:103`). The body path is already favorable: the executor uses the exact `EventPayload` string for the request (`executor.go:206`) and removes `GetBody` (`:220`). The signer must wrap this into a single request-construction function so there is no branch that reaches `client.Do` without a signature, except the explicitly gated development unsigned path. Production is blocked by receiver coordination and secret distribution, not by the HMAC code itself.

### 4.6 Workstream F — retention, purge, and KEK retirement

The repository documents 90/90/72 retention (`database-schema.md` §6, `:1733-1742`) but implements only the 72-hour idempotency sweep (`cmd/worker/lifecycle.go:73`). There is no Events, Deliveries, Attempts, or outbox purge. Endpoints are soft-deleted and outside the retention table, so a soft-deleted endpoint envelope is retained. Delivery snapshots are immutable and retained with their parent event. The only schema-level deletion path for an endpoint is the integration test's hard delete; production code never deletes one. Without purge and an audit tool, the D6-D "auditable zero references" precondition cannot be met. This is a hard production blocker and a hard KEK-retirement blocker.

### 4.7 Workstream G — test and CI readiness

CI is already structured for real integration: it rejects skipped tests, uses an isolated database, runs the RabbitMQ matrix, proves Down/Up, and runs race tests. The missing pieces are new deterministic envelope vectors, tamper/fuzz tests, provider permission/error matrices, signer vectors, exact-body tests, a pre-HTTP class matrix with a counting RoundTripper, and hosted-CI evidence. The harness must gain a seeded **encrypted** endpoint and a delivery with a real envelope to test decryption end to end; it must remain isolated and must not weaken the no-skip policy. CI readiness is not a production blocker by itself, but production cannot be declared without hosted results and fault/load evidence.

---

## 5. Findings, blockers, and evidence

Severity is calibrated to the approved baseline and the specific readiness question. "High" means a production-critical security, correctness, or operability gap. "Medium" means a required implementation or audit gap. "Low" means hygiene or documentary risk.

| ID | Severity | Finding | Evidence | Effect |
|---|---|---|---|---|
| F-01 | Medium | Repository documents still say D6/U25 are `NOT APPROVED`, contradicting the Owner's approval brief. | `d6-u25-decision-proposal.md:4`, `:873-883`; `runbook.md:172` | Audit/operations inconsistency; docs must be reconciled with separate authorization. Not a code blocker, but no report may claim the docs already reflect approval. |
| F-02 | High | B5 generalization alone cannot satisfy D6-G for deterministic permanent faults: it paces until the event horizon, creates a new outbox row per pace, and never terminalizes. | `store.go:472-570`, `:792-804`; `0008:54-62`; `recovery/sweep.go:376-407`, `:451-503` | Permanent malformed/unusable-secret classes become a long, noisy retry loop. A terminal `failed` zero-attempt path is needed, or the requirement is unmet. |
| F-03 | High | No production snapshot-creation path exists; the "same transaction as delivery insert" contract cannot be verified. | empty `internal/app/event`, `internal/modules/event`, `internal/modules/endpoint`; harness inserts only fixtures (`harness_test.go:335-353`) | End-to-end secret lifecycle and immutability are unproven. Production blocked. |
| F-04 | Medium | `endpoint_id` is absent from `Claim`, `HTTPPreparation`, and both claim SQL projections, although the column exists. | `pointer.go:386-400`, `:88-90`, `:416-427`; `store.go:134-141`; `0008:11` | D6-B AAD cannot be built. Read-only code change; no migration. |
| F-05 | Medium | Attempt audit persists snapshot headers, not the transmitted `webhook-*` values. | `store.go:697-707`, `:716-766` | U25-B audit requirement is unmet; a successful delivery's attempt row cannot prove the exact signed headers. |
| F-06 | High | Empty-secret snapshots execute unsigned HTTP unconditionally; no deployment mode or gate exists. | `executor.go` non-empty guard is the only secret check; `config.go` has no mode; docs admit unconditional unsigned (`configuration.md:107`, `runbook.md:166`) | A production deployment can silently send unsigned webhooks. Production blocked until D6-F is enforced. |
| F-07 | High | No production KEK provider, provider selection, keyring config, or startup validation exists. | `go.mod`; `runtime.go`; no `internal/platform/kek` | Production cannot custody or validate KEKs. Development file provider is explicitly non-production. |
| F-08 | High | No Events/Deliveries/Attempts/outbox purge and no KEK reference audit; endpoints are soft-deleted and retained. | `cmd/worker/lifecycle.go:73` only idempotency; `database-schema.md:1737-1742`; `0005:20`; `0008:2` | Retention is noncompliant and KEK retirement cannot prove zero references. |
| F-09 | Medium | No `SecretResolutionBudget`; no post-resolution re-sample; the executor checks the frozen `Remaining` against four components. | `runtime.go:418-423`; `processor.go:93`; `executor.go:176` | A slow unwrap can consume the 3 s slack and push HTTP past the 30 s lease. |
| F-10 | Medium | No signer and tests pin "no signature". | `executor.go:158-163`; `executor_test.go:202-203`; `worker_integration_test.go:103` | U25 core is unimplemented and the current test suite actively protects the wrong behavior. |
| F-11 | Medium | No signing-scheme snapshot on deliveries; no key identifier; rotation is receiver-coordinated only. | `0005:11` has scheme on endpoint; `0008` has no scheme column; U25-D | A future scheme change would not be snapshot-versioned. V1 can use the fixed approved scheme, but the limitation must be documented. |
| F-12 | High | No endpoint secret create/rotate/delete path, no secret distribution, and no endpoint purge. | empty endpoint module; harness hard delete only (`harness_test.go:223`); docs describe rotation as operator procedure | Production secret lifecycle cannot be operated. |
| F-13 | Medium | Snapshot immutability is a comment, not a database constraint or trigger. | `0008:2`; no trigger in migrations; application is the only writer in this repo | A future SQL path or operator can mutate a snapshot without database refusal. Defense-in-depth gap; not required for the approved codec. |
| F-14 | Medium | `audit_logs.organization_id` is `NOT NULL`; a global KEK audit has no natural organization scope. | `0012:5-11`; audit action/target metadata exist but require a tenant | Persisting global KEK retirement evidence in `audit_logs` needs a tenancy decision or an external immutable audit record. |
| F-15 | Medium | New crypto/signing/provider tests are absent; hosted CI is unverified; `.gitignore` has no dev key file entry. | `ci.yml`; `.gitignore`; `go.mod` | Test readiness is structurally good but not yet evidence-complete; a development key could be committed by accident. |
| F-16 | Low | Operational docs describe the current unsigned behavior and still say `NOT APPROVED`; they will be wrong immediately after implementation. | `configuration.md:107`; `runbook.md:166`, `:172-173` | Misoperation risk until documentation reconciliation is authorized and completed. |

### 5.1 Detailed blockers

**B-1 — Production provider and startup validation (F-07).** D6-C approves a provider abstraction, not a production provider. A production deployment needs a concrete KMS/Vault adapter, credential acquisition without ordinary configuration, a current `kid`, an allowlist, and startup refusal on contradictions. None exists. This blocks production regardless of codec quality.

**B-2 — Production unsigned-delivery gate (F-06).** The current code has no deployment-mode concept. A non-empty secret fails closed, but an empty secret is sent unsigned. D6-F requires production, unknown, and contradictory configurations to fail startup or fail closed. Until the mode and gate are implemented and tested, a production deployment cannot be authorized.

**B-3 — Endpoint secret lifecycle and snapshot creation (F-03, F-12).** There is no endpoint write path, no delivery creation path, and no secret distribution. The schema can store the envelope, but nothing produces one in production. The "copy the endpoint envelope into the delivery snapshot in the same transaction" contract is not implemented. This blocks end-to-end acceptance and production operation.

**B-4 — Retention, purge, and reference audit (F-08).** The approved retention windows are documented but not implemented. Soft-deleted endpoints and immutable delivery snapshots retain envelopes. Without purge and an audit that counts every retained envelope, D6-D's zero-reference precondition cannot be established. This blocks KEK retirement and creates a compliance gap.

**B-5 — D6-G permanent-class mechanism (F-02).** This is a development dependency rather than a production-only blocker. B5 is safe for transient provider failures. For deterministic permanent faults, the approved constraint "must not become an invisible endless retry loop" is not met by pacing until the event horizon. The smallest coherent alternative uses the existing `failed` state with zero attempts, a distinct reason in `last_error`, and a distinct metric. It changes approved state-machine semantics (failure without an HTTP attempt) and therefore needs explicit Owner confirmation. It does not require a new column, table, or state value.

### 5.2 Why "event-horizon bounded" is not enough

The event horizon is a retention/delivery window, not a retry policy. With a 30 s decrypt pacing and an event horizon measured in days, a deterministic fault produces a very large number of paced retries, a new outbox row and `dispatch_seq` increment each time, and a continuously increasing operational signal. It does not create attempt rows or increment `attempt_count`, so it preserves U18, but it is not a durable terminal outcome and it is not a sane permanent-failure policy. The recovery sweep eventually expires the delivery at the event horizon (`sweep.go:376-407`, `:451-503`), which makes the loop bounded in wall-clock time but not in retry count or outbox growth. The approved constraint is stronger than "eventually expires"; the report therefore treats this as a genuine mechanism decision.

---

## 6. Answers to the ten mandatory design checks

### Check 1 — Can D6-G be implemented without new state or schema?

**Answer: Yes for the transient class; not by B5 alone for permanent classes. A no-new-schema permanent path exists only if the Owner accepts an existing-state terminalization.**

- **Transient provider failure:** generalize `RecordDecryptFailure` with a Go-level reason code. The existing method is fenced, samples fresh database time, checks event horizon then lease expiry, writes no attempt, does not increment `attempt_count`, clears the lease, advances `dispatch_seq`, paces `next_attempt_at`, and optionally inserts a retry outbox row (`store.go:472-570`, `:792-804`). Adding a reason string and a distinct metric changes no SQL state and no schema.
- **Permanent/malformed envelope, invalid signing configuration, internal signing fault:** B5 still only paces. The existing `failed` state and `last_error` column can represent a terminal outcome without HTTP, with no attempt row and no count increment. That uses existing state values (`0008:43-45`), existing constraints (`completed_at` required for terminal states, `next_attempt_at` null, lease cleared; `:54-62`), and existing `last_error` length (`:56-58`). It creates no new table, column, or enum. It does change semantics: a delivery can be `failed` with zero attempts. That change is precisely what needs Owner confirmation.
- **Structured reason querying:** the smallest path stores a bounded reason string in `last_error` and emits a distinct metric. A dedicated reason column or enum would be new schema and is not required by the approved contract. If the Owner requires structured queryability, that is a separate decision and a migration.
- **Conclusion:** D6-G is implementable without schema **if** the permanent path terminalizes in existing state. If the Owner insists on B5-only for all classes, the implementation cannot meet the no-endless-retry constraint without new state or a new bounded counter, and should not be represented as complete.

**Acceptance:** a table-driven store test for each class asserts `delivery_attempts` count 0, `attempt_count` unchanged, no HTTP, fenced commit, ACK only after commit, and the expected terminal or paced state. A permanent class must not create an outbox row; a transient class must create one paced row.

### Check 2 — Can a permanent failure be durably observable without U18 violation or endless retries?

**Answer: Yes, with a terminal zero-attempt disposition; no, with B5 alone.**

- **Durable observability:** delivery state `failed`, `completed_at` set, `last_error` bounded and scrubbed, metric `hookflow_worker_pre_http_failed_total{reason=...}` (or equivalent fixed metric names), and an operational log that contains identifiers and reason but no secret or envelope. The row remains queryable until retention purge.
- **U18:** no attempt row is created because no HTTP execution occurred. `attempt_count` remains unchanged. This is consistent with `delivery_attempts`' write-on-completion definition (`0009:2-3`) and the attempt-count semantics in `complete`.
- **No endless retry:** the terminal path writes no new outbox row and leaves `next_attempt_at` null. It cannot be reacquired because `AcquirePointer` only selects `pending`/`retrying` (`pointer.go:104-116`).
- **Current B5 limitation:** B5 keeps the delivery in `pending`/`retrying`, advances `dispatch_seq`, and creates a retry outbox row every pace. Recovery expires it at the event horizon. That is observable, but it is a long retry loop and outbox growth, not a terminal policy.
- **Smallest coherent alternative (not implemented):** add a fenced `RecordPreHTTPFailure` or a `Terminal` mode to the existing method that sets `state='failed'`, `completed_at=fresh database time`, `next_attempt_at=NULL`, clears the lease, writes `last_error`, and inserts no outbox row. No schema change. Requires Owner confirmation because it changes the state-machine meaning of `failed`.
- **Acceptance:** permanent class produces exactly one terminal transition, zero attempts, no outbox row, and ACK after commit; transient class produces a paced retry and ACK after commit; expiry precedence remains event horizon before lease.

### Check 3 — What is the exact snapshot creation point, and how do versions correspond?

**Answer: the approved contract is "copy the endpoint's current envelope into the delivery row in the same transaction that creates the delivery, after event acceptance and endpoint selection; never rewrap or re-encrypt a snapshot." That point is not implemented in this repository.**

- **Contract point:** within the fan-out transaction that inserts `deliveries`, after the endpoint has been selected and while its current `secret_encrypted` is the version the event is being delivered to, copy the envelope string exactly into `deliveries.secret_encrypted`. Store `endpoint_id` in the same row. Do not perform KMS unwrap, wrap, or network I/O inside this transaction. Do not rewrap the snapshot later.
- **Version correspondence:** the version lives inside the envelope, not in a separate column. A snapshot therefore carries the DEK/version/`kid`/provider that existed at acceptance. Rewrapping an endpoint replaces only the header and wrapped-DEK region while keeping the same DEK; a snapshot remains valid and is never rewritten. Rotating an endpoint secret creates a new envelope with a new DEK; subsequent deliveries copy the new envelope, while existing snapshots keep the old one. This is the intended "versions correspond to the moment of snapshot" rule.
- **Current evidence:** `deliveries.secret_encrypted` exists (`0008:15`), `endpoint_id` exists (`:11`), and the migration comment declares immutability (`:2`). No production code inserts deliveries; `internal/app/event` and `internal/modules/event` are empty; the harness inserts fixtures directly (`harness_test.go:335-353`). The exact transaction cannot be inspected because it does not exist.
- **Immutability:** the comment is not enforced by a constraint or trigger. Application code must never update the snapshot column. If database-level refusal is required, that is a new migration and a separate decision (F-13, OD-6).
- **Acceptance:** create two deliveries around an endpoint rotation; the first must decrypt with the old envelope, the second with the new; an attempted update of a snapshot must be rejected by the application (and by a trigger only if separately approved); an endpoint soft delete must not alter an existing snapshot; `endpoint_id` in the AAD must match the delivery row.

### Check 4 — Does the transaction/locking order preserve delivery→outbox ordering and fencing?

**Answer: Yes on the implemented retry and decrypt-failure paths; the initial delivery-creation path cannot be verified because it is absent.**

- `Complete` begins a transaction, locks the delivery, samples fresh database time, inserts the attempt, updates the delivery only with the exact `dispatch_seq` and `lease_token` fence, inserts the retry outbox row with the next sequence, and commits (`store.go:320-460`). The outbox insert is after the delivery update in the same transaction; a commit makes both visible atomically.
- `RecordDecryptFailure` follows the same fenced order: lock, fresh time, expiry precedence, update delivery with new `dispatch_seq`, clear lease, insert retry outbox if due, commit (`store.go:472-570`). It writes no attempt.
- `AcquirePointer` locks the delivery and requires an existing outbox row with the exact `sequence = dispatch_seq` and `status='published'` (`pointer.go:104-116`), so a worker cannot process a delivery before the publisher's fenced T2 completion.
- Publisher T1 claims rows and commits before publishing; T2 confirms/returns and marks success only after a confirmed decision; a stale T2 writes nothing (`internal/app/outbox/store.go`, `internal/app/publisher/session.go`). The order is claim→publish→fenced mark, with delivery state updated before the outbox row is created.
- **Secret resolution does not change this order:** unwrap must occur after `PrepareHTTP` has committed the lease claim and outside any delivery/outbox transaction. Signing is in-memory. Completion remains the single transaction. Holding a delivery lock across a KMS call would be an unnecessary contention and latency risk and is not part of the approved design.
- **Initial path:** event acceptance → fan-out must insert `events`, `deliveries`, and the initial outbox row atomically, with the delivery's `dispatch_seq` matching the outbox sequence. No such code exists. The plan makes it a Phase 5 dependency and a required integration test.
- **Acceptance:** concurrent worker/publisher tests; no outbox row without a delivery; no delivery processed without its exact published sequence; retry sequence increments exactly once; decrypt-failure outbox row is ordered after the delivery update and is not visible on rollback; unknown T2 outcome does not ACK.

### Check 5 — Can secret resolution fit 2 s without weakening the 30 s lease?

**Answer: Yes, if the 2 s is added as a fifth validated component and the worker re-samples time after resolution before starting HTTP. The current code does neither.**

- **Label mapping.** The brief's "completion 2 s" maps to `HandshakeBudget` (2 s), "pre-completion 3 s" maps to `PostProcessBudget` (3 s), and "safety 10 s" maps to `ShutdownReserve` (10 s). The current code sums `HTTPDeadline + HandshakeBudget + PostProcessBudget + ShutdownReserve` = 10 + 2 + 3 + 10 = 25 s and requires lease 30 s (`runtime.go:418-423`, `executor.go:79-83`, `:176`). The approved sum adds `SecretResolutionBudget` = 2 s, making 27 s and leaving 3 s slack, not a sixth component.
- **Where to enforce:** `processor` computes the five-component requirement for `PrepareHTTP`; after `PrepareHTTP` commits, it resolves the secret under a 2 s context; then it re-samples a monotonic elapsed time and recomputes the remainder. It must not use the frozen `HTTPPreparation.Remaining` unchanged. The executor's own check stays at the four post-resolution components (25 s); `SecretResolutionBudget` is not added to `HTTPOptions.RequiredLeaseBudget()`.
- **Failure behavior:** provider timeout or transient provider error is a pre-HTTP transient class routed through the durable disposition under a fresh bounded context; a successful unwrap with insufficient remaining lease must start no HTTP and use the existing `ErrInsufficientLease` handling. There is no shortened HTTP deadline. A provider that consumes the budget cannot cause a lease-expired HTTP call.
- **Clock source:** use the database-sampled preparation time and monotonic elapsed time, or a fresh database time read; do not rely on `LeaseExpiresAt - time.Now()` without a documented skew bound.
- **Acceptance:** provider stubs at 1.9 s, 2.1 s, and over-budget; a test proves either a provably sufficient remainder or no HTTP; a fake clock proves the frozen `Remaining` is not the deciding input; the lease remains 30 s; no HTTP starts with an insufficient post-resolution remainder.

### Check 6 — Is the KEK audit correct under concurrent rewrap, creation, soft delete, and purge?

**Answer: It can be made correct only with a defined snapshot and a frozen write path; no audit tool or procedure exists today.**

- **Coverage:** the audit must read every row that can persist an envelope: all `endpoints` rows **without** a `deleted_at IS NULL` filter, and all `deliveries` rows regardless of state, until they are purged. It must parse the envelope header in Go, count per `kid`/provider/version, and list unparseable values instead of skipping them. It must decrypt nothing.
- **Consistent snapshot:** run the audit in one `REPEATABLE READ` transaction (or export a single consistent snapshot) so rewrap updates cannot be observed half-applied. A row-level rewrap replaces the header atomically; a repeatable-read audit sees either the old or the new row, never a partial envelope.
- **Rewrap:** rewrap only endpoints, never delivery snapshots. After a rewrap, the old `kid` count for that endpoint becomes zero and the new `kid` count increases. The audit must be run after the rewrap pass, not concurrently with an unknown set of in-flight rewraps.
- **Creation:** a new delivery snapshot copies the endpoint envelope. If an endpoint is still on the old `kid` when a delivery is created, the snapshot becomes a new old-`kid` reference after any audit that ran earlier. The safe procedure is: disable new wraps under the old `kid`, finish rewrapping every endpoint, stop or gate endpoint secret writes and delivery ingestion, drain in-flight creation transactions, then audit at zero, then repeat after a quiet window. Without a database trigger or a serializable write barrier, an audit cannot prove that a concurrent transaction will not insert a new old-`kid` snapshot after the audit snapshot. This limitation must be stated in the runbook.
- **Soft delete:** a soft-deleted endpoint is still a reference. Include it. Do not infer purge from `deleted_at`.
- **Purge:** purge must precede the final audit. If purge runs concurrently, a repeatable-read audit may over-count (safe) or may not see a deletion that occurs after its snapshot (also safe for the zero test, because the deletion removes a reference). The dangerous direction is a new reference created after the audit; that is prevented by the write freeze, not by the audit.
- **Tenancy:** a global KEK audit has no organization. `audit_logs.organization_id` is `NOT NULL` (`0012:5-11`), so storing a global report there requires a tenancy decision or an external immutable audit record. This report does not authorize a schema change.
- **Acceptance:** seeded audit with reused `kid` across live and soft-deleted endpoints and deliveries; unparseable values listed; rewrap changes counts; a concurrent-insert test demonstrates the snapshot limitation and the need for the freeze; no decryption.

### Check 7 — How can retirement be prevented while an unobserved or concurrent reference exists?

**Answer: not by an audit alone. Retirement needs provider-level wrap-disable, a frozen creation path, a repeatable audit at zero, a quiet window, and unwrap monitoring. The current repository has none of these.**

- **Provider gate:** the provider must be able to reject new wraps under the retiring `kid` while still allowing unwrap. `CurrentKID` and `Rewrap` alone are insufficient for retirement; a `DisableWrap(kid)` capability (or equivalent configuration state) is required. This is a production-provider requirement, not a codec requirement.
- **Creation freeze:** because delivery creation copies the endpoint envelope, all endpoint envelopes must be on the new `kid` and all creation paths must be quiesced before the final audit. Otherwise a late transaction can create an old-`kid` snapshot. A row lock or serializable barrier would be needed to prove this without a freeze; none exists. The safest procedure is a maintenance window with ingestion paused.
- **Double audit:** audit at zero, wait a defined quiet period longer than any in-flight transaction and delayed writer, audit again at zero. Keep unwrap enabled. If any old-`kid` unwrap is observed, abort retirement, re-enable wrap, and investigate.
- **No database enforcement:** there is no trigger or constraint preventing a snapshot insert with an old `kid`. The audit is advisory evidence, not a cryptographic proof. The runbook must say so.
- **Retention prerequisite:** the 90/90/72 purge must be implemented and verified; a soft-deleted endpoint envelope is a reference until it is purged; delivery snapshots are references until purged. Calendar time is not evidence.
- **Recommendation:** do not retire any KEK in V1. Keep every configured version available for unwrap. Retirement remains **BLOCKED** until a production provider with wrap-disable, purge, an audit tool, and an approved procedure exist. If retirement is ever attempted, use a maintenance window, double audit, and an external immutable audit record.

### Check 8 — How do receivers coordinate signing-key rotation without a key ID?

**Answer: by keeping current and previous secrets at the receiver, verifying with both in constant time, and using a documented overlap longer than the maximum retry/backlog window; the sender must not add a key-ID header or a second signature in V1.**

- **Sender:** a delivery snapshot holds one envelope and one secret. All attempts for that delivery use that snapshot. A new endpoint secret produces a new envelope for subsequent deliveries; existing deliveries continue with their old snapshot until terminal. There is no key ID and no dual signature in V1; the approved baseline explicitly proposes overlap instead.
- **Receiver:** configure current and previous secrets, try verification with each using constant-time comparison, accept if either validates, and reject if neither does. Do not infer the key from a header. Deduplicate on `webhook-id` because delivery is at-least-once.
- **Overlap:** 24 hours is the approved minimum. It is longer than the 12-hour HTTP retry horizon plus the ±300 s tolerance, but it may not cover an arbitrary queue backlog before the first attempt or a pre-HTTP retry that later becomes HTTP. The operational procedure should require the receiver to retain the old secret until no delivery can still use it, and should monitor backlog. Do not silently raise or lower the approved 24-hour minimum; document the limitation and use a longer operational overlap when backlog is possible.
- **Rotation independence:** rotating the signing secret and rotating the KEK are different operations. KEK rewrap preserves the DEK and the plaintext secret; signing rotation changes the secret and therefore requires receiver coordination. Do not couple them in one procedure.
- **Compatibility:** a receiver that cannot hold two secrets cannot rotate without downtime. Document this; do not fall back to unsigned. A future key-ID or multi-signature design is out of scope and would be a new decision.
- **Acceptance:** a test receiver accepts a signature made with either configured secret, rejects a signature made with neither, and rejects a timestamp outside ±300 s. A retry test keeps `webhook-id` stable, changes the timestamp, and verifies with the same snapshot secret. A rotation test shows old deliveries still verify with the old secret while new deliveries use the new one.

### Check 9 — Are the transmitted body bytes identical to the HMAC input?

**Answer: Yes, if one byte slice is used for both, and the signer returns the exact header values that are then sent. The current request path is compatible but has no signer.**

- **Single source:** materialize `body := []byte(preparation.EventPayload)` once. Compute the canonical input over `[]byte(webhookID + "." + timestamp + ".") + body`, and construct the request with `bytes.NewReader(body)`. Do not decode/re-encode JSON, trim, normalize Unicode, or re-serialize. Do not sign a different copy than the one transmitted.
- **Header values:** set `webhook-id`, `webhook-timestamp`, and `webhook-signature` from the exact variables used in the canonical input. Do not recompute from a different clock value after signing. The delivery ID is stable; the timestamp and signature are fresh per attempt.
- **Transport:** keep redirects disabled and `GetBody = nil` (`executor.go:118-121`, `:220`). Do not introduce request compression. Set `Content-Type: application/json` as today; it is not part of the signed input and does not change the body.
- **No URL/path:** the canonical input contains only the id, timestamp, and body. Do not include the URL, method, or path; U25-B accepts that limitation and relies on receiver idempotency/payload routing.
- **Acceptance:** a test receiver captures the body and headers, recomputes the HMAC over its received bytes, and compares. Use bodies containing CRLF, UTF-8, and boundary lengths (including an empty body if the payload allows it). A fake transport asserts the captured body is byte-identical to the preparation payload and to the bytes used by the signer.

### Check 10 — Which exact tests prove no HTTP, no attempt, and no count for every pre-HTTP class?

**Answer: a table-driven matrix over every class, with a counting `RoundTripper`, a real PostgreSQL integration assertion on `delivery_attempts` and `attempt_count`, and a broker disposition assertion. The current suite covers only the secret-unavailable class and budget refusal.**

The required matrix is:

| Pre-HTTP class | HTTP count | `delivery_attempts` delta | `attempt_count` delta | State/outbox | Broker disposition |
|---|---:|---:|---:|---|---|
| Provider transient/timeout (`secret_unavailable`) | 0 | 0 | 0 | `pending`/`retrying`, paced, one retry outbox row | ACK only after commit |
| Empty secret, production/unknown mode/gate off (`secret_missing`) | 0 | 0 | 0 | terminal `failed` (recommended) or paced (if B5-only) | ACK after commit |
| Malformed/permanent envelope, unknown `kid`, disallowed provider | 0 | 0 | 0 | terminal `failed` (recommended) | ACK after commit |
| Invalid/unusable signing configuration | 0 | 0 | 0 | terminal `failed` (recommended) | ACK after commit |
| Internal signing fault (`signing_internal`) | 0 | 0 | 0 | terminal `failed` (recommended) | ACK after commit |
| Insufficient post-resolution lease budget | 0 | 0 | 0 | release or paced, no HTTP | reject-requeue or durable path |
| Event horizon expired / lease expired during disposition | 0 | 0 | 0 | `expired`, no retry outbox | ACK after commit |
| Fenced/stale claim | 0 | 0 | 0 | terminal/retrying state unchanged by stale worker | ACK after durable check |

**Required test mechanics:**

- **Unit worker test:** inject `executorRoundTripFunc` that increments an atomic counter and returns a synthetic response. Drive every row through `Runtime.Process`; assert the counter remains 0. The existing `TestExecutorSecretUnavailableFailsClosed` and `TestExecutorRefusesInsufficientLeaseBudget` are the pattern (`executor_test.go:282-300`, `:302+`).
- **Store integration test:** after each disposition, query `SELECT count(*) FROM delivery_attempts WHERE delivery_id=...` and `SELECT attempt_count FROM deliveries ...`; assert 0 and unchanged. Assert `state`, `completed_at`, `next_attempt_at`, `dispatch_lease_token`, `last_error`, and outbox row count. The existing `TestDecryptFailurePacedRetryIntegration`, `TestDecryptFailureNearExpiryIntegration`, `TestDecryptFailureExpiredEventIntegration`, `TestDecryptFailureExpiredLeaseIntegration`, `TestDecryptFailureFencedIntegration`, and `TestDecryptFailureBuilderFailureIntegration` (`store_integration_test.go:1227-1517`) are the base to extend.
- **Broker test:** assert ACK only after commit; transient path uses reject-requeue; uncertain path holds; no NACK. `internal/app/disposition/policy.go` and the consumer tests provide the policy surface.
- **Configuration/startup tests:** development + gate allows unsigned; development without gate produces no HTTP; production + gate fails startup; production + no gate fails closed with no HTTP; unset/unknown mode fails startup; missing/insecure key file fails startup; unknown per-envelope `kid` is per-delivery, not startup.
- **Invariant test:** inject a fake signer that errors and assert no HTTP, no attempt, no count, and no unsigned fallback. Inject a valid signer and assert a non-empty signature and a verifier success.
- **Leak test:** assert no plaintext, envelope bytes, wrapped DEK, or secret appears in logs, metrics, errors, `last_error`, or attempt headers.

---

## 7. Integrated phased implementation plan

Complexity is relative size only: S, M, L, XL. No calendar estimates are implied. Each phase is independently reviewable; no phase authorizes production deployment.

### Phase summary

| Phase | Name | Dev readiness after phase | Production effect | Complexity |
|---|---|---|---|---|
| 0 | Decision and guardrail freeze | Decisions recorded | None | S |
| 1 | Envelope, AAD, provider interface, dev file provider | Codec/provider libraries tested | No production provider | L |
| 2 | Configuration and startup validation | Mode/gate/provider/budget validation | Startup fail-closed behavior | M |
| 3 | Claim/preparation and pre-HTTP disposition | Reason-coded no-attempt paths | Still no production provider/secret lifecycle | L |
| 4 | Secret resolution, signing, and HTTP invariant | Signed delivery with exact-body proof | Still needs receiver coordination | XL |
| 5 | Snapshot creation and endpoint secret lifecycle | End-to-end encrypted fixtures | Requires endpoint/ingest workstream | XL |
| 6 | Retention, purge, audit, and retirement | Audit tooling | KEK retirement still blocked until all gates | XL |
| 7 | CI, fault, load, documentation, and production gates | Evidence-complete CI | Production authorization is a separate Owner act | M |

### Phase 0 — Decision and guardrail freeze (S)

| Field | Plan |
|---|---|
| Objective | Record the one remaining D6-G mechanism choice and the production/retirement guardrails before code depends on them. |
| Scope | No source, test, migration, configuration, or database change. A written decision record only. |
| Exact files | None in this phase. The decision is referenced by later code and by Section 12 of this report. |
| Interfaces/types | None. |
| DB/migration | None. |
| Dependencies | Owner input on OD-1 (permanent pre-HTTP mechanism) and ODs 2–5 in Section 12. |
| Tasks | Confirm terminal zero-attempt `failed` for permanent pre-HTTP classes (recommended) or explicitly accept B5-only event-horizon pacing; confirm no production provider selection now; confirm no KEK retirement; confirm documentation reconciliation is a separate authorized change. |
| Tests | None. |
| Security review | Ensure the decision does not permit unsigned production or attempt fabrication. |
| Acceptance | A written Owner decision that names the mechanism, the state, the reason/metric, and the fact that no schema change is required. |
| Out of scope | Any implementation. |
| Complexity | S. |
| Blocking risks / rollback | If the decision is absent, Phase 3 cannot implement permanent classes; rollback is not applicable because nothing changes. |

### Phase 1 — Envelope, AAD, and provider interface (L)

| Field | Plan |
|---|---|
| Objective | Build the approved cryptographic primitives as additive packages with deterministic tests, without wiring them into delivery. |
| Scope | `internal/platform/secrets` and `internal/platform/kek`; test-only in-memory provider. No worker behavior change. |
| Exact files | New `internal/platform/secrets/{codec.go,aad.go,errors.go,codec_test.go,codec_fuzz_test.go}`; new `internal/platform/kek/{provider.go,file.go,memory.go,errors.go,file_test.go,provider_test.go}`. |
| Interfaces/types | `secrets.Encode(plaintext []byte, ctx Context) (string, error)`, `secrets.Decode(string) (Envelope, error)`, `secrets.BuildAAD(Context) [64]byte`; typed errors `ErrMalformed`, `ErrUnsupportedVersion`, `ErrUnsupportedAlg`, `ErrUnsupportedPurpose`, `ErrUnknownKID`, `ErrAuthFailed`, `ErrProviderDisallowed`, `ErrTransient`, `ErrPlaintextInvalid`; `kek.Provider` with `Wrap`, `Unwrap`, `CurrentKID`, optional `Rewrap`; `kek.Context` carrying org/endpoint/purpose/DEK. |
| DB/migration | None. `text` storage and existing columns suffice. |
| Dependencies | The approved envelope/AAD definitions. No `endpoint_id` yet for library tests; tests use synthetic contexts. |
| Tasks | Encode exact header fields and bounds; strict parser rejects trailing bytes, unknown fields, padded/non-canonical base64, oversize input; fresh 32-byte DEK and 12-byte nonce; AES-256-GCM with 16-byte tag; `hfse1.` prefix; AAD exactly 64 bytes with raw UUIDs; file provider reads a keyring JSON with `0600` enforcement; memory provider for tests; error classification maps provider vs envelope vs auth failure. |
| Tests | Golden vectors with deterministic nonce/DEK seams; round trips at 1/16/1024-byte plaintext; per-field tamper matrix; cross-org and cross-endpoint transplant; rewrap keeps ciphertext/nonce/tag and returns same plaintext; parser negatives; fuzz; file missing/0644/malformed/unknown `current`/unknown `kid`/wrong key length. |
| Security review | AAD binds tenant and endpoint; key slot excluded to permit rewrap; no KEK in env or logs; zeroization limits documented; no plaintext in errors; parser is bounded before decode. |
| Acceptance | Exact golden bytes match the approved contract; every tamper is a typed error, never a panic; rewrap is byte-preserving outside the header; file provider refuses insecure permissions. |
| Out of scope | Worker wiring, configuration, production provider adapters. |
| Complexity | L. |
| Blocking risks / rollback | Canonical-encoding or AAD-length bugs would invalidate all later work; mitigate with independent golden vectors and review. Packages are additive; rollback is deletion. |

### Phase 2 — Configuration, deployment mode, and startup validation (M)

| Field | Plan |
|---|---|
| Objective | Make unsafe configurations impossible to start and make the development unsigned gate explicit. |
| Scope | Configuration parsing/validation, worker startup wiring, environment example; no delivery behavior change while the gate is off. |
| Exact files | `internal/platform/config/config.go`, `internal/platform/config/runtime.go`, `cmd/worker/lifecycle.go`, `.env.example`, `.gitignore`; tests in `internal/platform/config/*_test.go` and `cmd/worker/*_test.go`. |
| Interfaces/types | `config.DeploymentMode` (`development`, `production`); `AllowUnsignedDelivery bool`; provider selector and non-secret key path/current `kid`/allowlist; `SecretResolutionBudget time.Duration` with `HOOKFLOW_WORKER_SECRET_RESOLUTION_BUDGET`; `DeliveryWorker.Validate` includes the fifth budget component. |
| DB/migration | None. |
| Dependencies | Phase 1 provider validation; D6-F baseline. |
| Tasks | Require a valid mode; default the unsigned gate to false; allow the local file provider only in development; fail startup on production+gate, missing provider, unset/unknown mode, missing/insecure key file, unknown current `kid`, or an empty allowlist; add `.gitignore` entries for dev key files/secrets; document names in `.env.example`. |
| Tests | Full mode/gate matrix; provider startup matrix; malformed and contradictory settings; assert no secret material in error strings; assert `LeaseDuration > 10+2+3+10+2 = 27 s` and the 3 s slack is not a component. |
| Security review | Fail closed on ambiguity; no KEK in environment variables; local provider cannot be constructed outside development; startup errors must not echo key bytes. |
| Acceptance | A production or unknown configuration refuses to start; development with no gate starts but empty secrets do not reach HTTP; development with gate allows unsigned only. |
| Out of scope | Production provider adapter; delivery behavior for non-empty secrets. |
| Complexity | M. |
| Blocking risks / rollback | Startup refusal is a behavior change for operators; stage with explicit configuration and document it. Rollback is a config change only if no code depends on the new fields. |

### Phase 3 — Claim/preparation plumbing and pre-HTTP disposition (L)

| Field | Plan |
|---|---|
| Objective | Make the delivery claim carry the endpoint identity and make every pre-HTTP class durable with the correct U18 and retry semantics. |
| Scope | `internal/app/delivery` claim/pointer/pre-http methods; `internal/app/worker/processor` routing; metrics; tests. No crypto or signing yet. |
| Exact files | `internal/app/delivery/store.go`, `internal/app/delivery/pointer.go`, `internal/app/worker/processor.go`, `internal/app/worker/retry.go`, new tests in `internal/app/delivery/*_test.go` and `internal/app/worker/*_test.go`. |
| Interfaces/types | Add `Claim.EndpointID` and `HTTPPreparation.EndpointID`; add `PreHTTPFailureOptions{Reason, Error, Terminal}` and a fenced `RecordPreHTTPFailure` (or a generalized method) with reason constants `secret_unavailable`, `secret_missing`, `signing_internal`, `secret_invalid`, `provider_disallowed`; keep `DecryptFailureOptions` compatibility if needed. |
| DB/migration | None. Use existing `failed` state, `last_error`, `completed_at`, `next_attempt_at`, and lease columns. Confirm the terminal row satisfies `chk_deliveries_completed` and `chk_deliveries_scheduling` (`0008:54-62`). |
| Dependencies | Phase 0 mechanism decision; Phase 1 reason types; Phase 2 config for `secret_missing` gate. |
| Tasks | Add `d.endpoint_id` to both claim projections and `HTTPPreparation`; extend the pre-HTTP method with reason and terminal mode; ensure transient writes one paced outbox row and terminal writes none; preserve fresh time, event-horizon-before-lease precedence, fencing, and ACK-after-commit; add distinct metrics; add transmitted-header capture hook (if not deferred to Phase 4). |
| Tests | The full Check 10 matrix at store and worker level; `endpoint_id` non-empty for every claim; terminal zero-attempt row; transient paced row; expiry precedence; fenced and builder-failure paths; no attempt/count; no HTTP via fake executor; ACK only after commit. |
| Security review | No attempt fabrication for no HTTP; `last_error` bounded/scrubbed; no secret/envelope in diagnostics; terminal state cannot be reacquired; no unsigned fallback. |
| Acceptance | Every pre-HTTP class has a durable, observable, fenced disposition; permanent classes do not loop if OD-1 is approved; transient classes pace and ACK after commit. |
| Out of scope | Actual secret resolution, signing, and request construction. |
| Complexity | L. |
| Blocking risks / rollback | The terminal semantic is the main risk; keep it behind a code path that is not wired until the Owner decision is recorded. Additive fields/methods allow rollback. |

### Phase 4 — Secret resolution, signing, and HTTP invariant (XL)

| Field | Plan |
|---|---|
| Objective | Replace the fail-closed placeholder with a complete, bounded, signed HTTP path that never reaches `client.Do` without a signature except in the explicit development unsigned mode. |
| Scope | `internal/platform/signing`, worker executor/processor, provider wiring, request-construction invariant, budget re-sample, tests. |
| Exact files | New `internal/platform/signing/{signer.go,signer_test.go}`; `internal/app/worker/executor.go`, `internal/app/worker/processor.go`, `cmd/worker/lifecycle.go`; `internal/platform/config/runtime.go` if not fully covered in Phase 2; tests. |
| Interfaces/types | `signing.Signer.Sign(id string, at time.Time, body []byte) (HeaderSet, error)` with an injected clock; `HeaderSet{ID, Timestamp, Signature string}`; a secret-resolution result carrying plaintext only in memory; a single `buildRequest` function returning either a fully signed request or a typed pre-HTTP error. |
| DB/migration | None. Attempt header persistence uses existing `request_headers jsonb`. |
| Dependencies | Phases 1–3; Phase 0 decision; Phase 2 gate/provider config. |
| Tasks | Decode/unwrap/open/validate under the 2 s budget; enforce 1–1024 plaintext and at least 16 bytes for signing; build the exact 64-byte AAD; remove the non-empty secret guard; enforce the unsigned gate only for empty secrets; re-sample time after resolution; require 25 s post-resolution; build body once; sign `id.timestamp.body`; set `webhook-*` headers; keep redirects disabled and `GetBody=nil`; persist transmitted headers in the attempt row without secrets; invert the two tests that pin no signature. |
| Tests | `httptest` receiver recomputes HMAC; RFC 4231 vectors; exact-body equality; stable id/fresh timestamp on retry; no HTTP without signature; no HTTP for wrong key/AAD/tamper; provider timeout/over-budget; budget start/no-start; transmitted header audit; leak scan; unsigned gate tests. |
| Security review | No plaintext/wrapped bytes/envelope in logs/metrics/errors; constant-time verification in test/receiver helper; header injection prevention; key length; no URL/path in signed input; no unsigned fallback in production; exact transmitted bytes. |
| Acceptance | A standard-compliant verifier accepts the request; every non-empty secret path is signed; every pre-HTTP failure has no HTTP/attempt/count; the post-resolution budget is provably sufficient. |
| Out of scope | Receiver secret distribution; production provider adapter; endpoint secret creation. |
| Complexity | XL. |
| Blocking risks / rollback | The request-construction invariant is security-critical; implement it as a single function and test every branch. The worker only signs when a valid provider is configured; keep the old fail-closed path available as a rollback until the new path is accepted. |

### Phase 5 — Snapshot creation and endpoint secret lifecycle (XL, external dependency)

| Field | Plan |
|---|---|
| Objective | Make encrypted delivery possible from a real endpoint secret and prove the snapshot/version contract end to end. |
| Scope | Endpoint secret create/rotate path, event-to-delivery fan-out, snapshot copy, fixture updates. This is predominantly outside the code currently present. |
| Exact files | New code under `internal/modules/endpoint` and `internal/app/event` (both currently `.gitkeep` only), delivery creation in `internal/app/delivery`, API wiring in `cmd/api` as appropriate; test harness `test/integration/harness_test.go`. |
| Interfaces/types | `EndpointSecretWriter` that produces an envelope via `secrets.Encode` and `kek.Provider.Wrap`; `SnapshotBuilder`/fan-out that copies the envelope and `endpoint_id` into the delivery; a rotation operation that updates only the endpoint envelope; no snapshot rewrap. |
| DB/migration | None for the core columns. Existing `endpoints.secret_encrypted`, `deliveries.secret_encrypted`, `deliveries.endpoint_id`, and `signing_scheme` suffice. Endpoint purge is deferred to Phase 6. |
| Dependencies | Phase 1 codec/provider; the endpoint/ingest workstream (not present); D6-B AAD endpoint identity. |
| Tasks | Create endpoint secrets as envelopes on write; copy the exact envelope into the delivery in the same transaction as the delivery insert; never perform KMS I/O inside that transaction; ensure soft-deleted endpoints are not used for new deliveries; implement endpoint rotation as an envelope update; document that snapshots never rewrap; update the harness to seed a real test envelope and a non-empty secret. |
| Tests | Two deliveries around a rotation decrypt with old/new; snapshot immutable after endpoint update; soft delete does not alter snapshots; AAD endpoint mismatch fails; endpoint_id matches; no plaintext persisted; end-to-end signed delivery from a created endpoint. |
| Security review | Snapshot copy is exact; no envelope/log leakage; endpoint secret never logged; rotation authority; soft-delete semantics; no delivery creation for deleted endpoints. |
| Acceptance | A real endpoint secret produces a real envelope, a delivery snapshot copies it, and the worker decrypts and signs successfully; rotation is observable and old deliveries remain valid. |
| Out of scope | Endpoint CRUD UI, secret distribution to receivers, endpoint purge. |
| Complexity | XL because the endpoint/event modules do not exist. |
| Blocking risks / rollback | Schedule dependency; without this phase, end-to-end acceptance can only use fixtures. Keep the snapshot helper additive and testable before the full ingest path. |

### Phase 6 — Retention, purge, reference audit, and retirement (XL)

| Field | Plan |
|---|---|
| Objective | Implement the approved retention windows and produce auditable evidence for KEK retirement without ever destroying a referenced KEK. |
| Scope | Events/deliveries/attempts/outbox purge, endpoint purge policy, reference audit tool, rewrap/retirement runbook; no automatic KEK destruction. |
| Exact files | New `internal/app/retention` and/or `cmd/purge`; new `cmd/kek-audit` (read-only); `cmd/worker/lifecycle.go`; tests; `docs/operations/runbook.md` later with authorization. |
| Interfaces/types | Batch purge with explicit limits; purge order outbox → attempts → deliveries → events; endpoint purge by policy; audit report per `kid`/provider/version with counts and unparseable entries; optional rewrap operation. |
| DB/migration | None required for initial purge using existing indexes (`idx_events_expires`, `idx_deliveries_org_created`, `idx_attempts_org_started`). Outbox purge may need a `created_at` index if volume requires it; that is a separate migration decision. A global audit record may need an external store because `audit_logs.organization_id` is `NOT NULL`. |
| Dependencies | Phase 5 snapshots; U2 purge-coverage/batch decisions; production provider with wrap-disable for retirement. |
| Tasks | Implement retention jobs and batch limits; purge attempts before deliveries and deliveries before events; include terminal and expired rows; define endpoint purge only after dependencies are gone; audit every endpoint (including soft-deleted) and delivery; list unparseable values; run audit in a consistent snapshot; document the freeze/quiet-window procedure; do not destroy KEKs. |
| Tests | Batch isolation and clocks; FK order; attempts before deliveries; endpoint FK RESTRICT behavior; soft-deleted endpoint counted; audit counts reused `kid` across tables; unparseable listed; concurrent rewrap and audit limitation; no decryption by audit. |
| Security review | Purge is destructive: least-privilege role, dry-run, bounded batches, audit trail, no automatic retirement. Audit data must not contain envelopes or secrets. |
| Acceptance | Retention windows are enforced; audit reports zero only after purge and a frozen write path; retirement remains a manual, separately authorized procedure. |
| Out of scope | Automatic KEK destruction; provider-specific retirement implementation until selected. |
| Complexity | XL. |
| Blocking risks / rollback | Data loss risk; require dry-run and backups. The audit's concurrency limitation is a documented residual, not silently claimed away. |

### Phase 7 — CI, fault, load, documentation, and production gates (M)

| Field | Plan |
|---|---|
| Objective | Turn the implementation into evidence-complete CI and an approved operational procedure without implying production authorization. |
| Scope | CI test additions, hosted run, fault/load tests, runbook/config documentation updates, final readiness review. |
| Exact files | `.github/workflows/ci.yml`, new test files, `.env.example`, docs to be updated only with separate authorization. |
| Interfaces/types | None. |
| DB/migration | None unless Phase 6 adds an index/trigger; then update the CI Down count and get separate approval. |
| Dependencies | All prior phases. |
| Tasks | Add unit/integration/fuzz tests to CI; run hosted workflow; add provider latency/timeout, broker close, DB failure, and unknown-commit tests; keep the no-skip gate; update docs after authorization; verify no secret appears in CI logs; record test-key-only fixtures. |
| Tests | Full quality/integration/race/lint matrix; RabbitMQ 4.4.1/4.3.8; PostgreSQL 18; Down/Up; no skipped tests; deterministic crypto vectors. |
| Security review | CI logs, artifacts, fixtures contain no real secrets; key files are ephemeral and `0600`; no production credentials. |
| Acceptance | Hosted CI green; every Check 10 row covered; operational runbook reconciled; production readiness review can begin. |
| Out of scope | Production deployment, provider selection, KEK retirement. |
| Complexity | M. |
| Blocking risks / rollback | Hosted CI may differ from local; fix before claiming readiness. No runtime rollback needed. |

---

## 8. Critical dependency graph

```
Owner decision OD-1 (D6-G permanent mechanism)
        │
        └──────────────► Phase 3 (pre-HTTP disposition)
                                   │
Phase 1 (codec + AAD + provider) ──┼──► Phase 2 (mode/gate/provider/budget)
                                   │            │
                                   │            └──► Phase 4 (resolve + sign + HTTP)
                                   │                         │
Endpoint/event workstream ──► Phase 5 (snapshot + secret lifecycle)
                                   │                         │
                                   └──────────────► end-to-end signed delivery
                                                             │
U2 retention/purge policy ──► Phase 6 (purge + audit) ◄──────┘
                                   │
                                   └──► auditable zero references ──► KEK retirement
                                                                       (still blocked)

Production provider selection ──► production deployment (separate Owner act)
Receiver secret distribution  ──► signing rotation operation (external)
```

| Edge | Type | Why it is critical |
|---|---|---|
| OD-1 → Phase 3 | Hard | Permanent classes cannot be implemented without a mechanism decision; B5-only fails the no-loop constraint. |
| Phase 1 → Phase 2 | Hard | Startup validation needs provider/keyring validation behavior. |
| Phase 1 → Phase 4 | Hard | Signing needs a decrypted secret and AAD; the codec defines the plaintext contract. |
| Phase 2 → Phase 4 | Hard | The gate and provider configuration determine whether HTTP may start. |
| Phase 3 → Phase 4 | Hard | The failure-routing method and `endpoint_id` are prerequisites for the signed path. |
| Endpoint/event workstream → Phase 5 | Hard | There is no production delivery creation path today. |
| Phase 5 → end-to-end acceptance | Hard | Fixtures cannot prove the snapshot/rotation contract. |
| Phase 6 → audit → retirement | Hard | Zero references cannot be established without purge and audit. |
| Production provider → production deployment | Hard | The development file provider is explicitly not production-ready. |
| Receiver distribution → rotation operation | Hard | No key ID means the receiver must be configured with both secrets. |
| Phase 7 → production readiness review | Soft | CI evidence is required for confidence, but not for development code. |

---

## 9. Migration and schema impact

**No migration is required for the approved D6/U25 core.** The evidence is concrete:

| Need | Existing artifact | Required change | Migration |
|---|---|---|---|
| Store an envelope | `endpoints.secret_encrypted text NOT NULL` (`0005:10`); `deliveries.secret_encrypted text NOT NULL` (`0008:15`) | Store the `hfse1.` string | None |
| Bind AAD to endpoint | `deliveries.endpoint_id uuid NOT NULL` (`0008:11`) | Select it into `Claim`/`HTTPPreparation`; add to SQL projections | None |
| Persist transmitted headers | `delivery_attempts.request_headers jsonb` (`0009:6-49`) | Capture and sanitize the actual transmitted header set | None |
| Record a pre-HTTP reason | `deliveries.last_error` bounded 500 (`0008:56-58`) | Write a bounded reason string; emit metrics | None |
| Terminal pre-HTTP failure | `deliveries.state` includes `failed`; `completed_at`/`next_attempt_at` checks (`0008:43-62`) | Set `failed`, `completed_at`, null `next_attempt_at`, clear lease | None |
| Retry/decrypt pacing | `outbox_messages` statuses and unique sequence (`0010`) | Existing inserts; no new state | None |
| Deployment mode/gate | Environment/configuration only | New config values; no database row | None |
| Provider/keyring | File/provider configuration | No database storage | None |
| Retention | Existing tables and indexes | Add purge jobs; no new columns | None for initial jobs |
| Reference audit | Existing `text` columns | Read-only parser tool | None |

Constraints that matter for the terminal alternative:

- `chk_deliveries_completed` requires `completed_at IS NOT NULL` exactly when the state is terminal (`0008:54-58`). A terminal pre-HTTP `failed` row must set it.
- `chk_deliveries_scheduling` requires `next_attempt_at` non-null exactly when the state is `pending`/`retrying` (`:59-62`). A terminal row must null it.
- `chk_deliveries_worker_lease` requires the token and expiry to be both null or both non-null (`:60-62`). A terminal row must clear both.
- `delivery_attempts` has no row unless an HTTP execution completed (`0009:2-3`). The terminal path writes none.
- `outbox_messages` has a unique `(delivery_id, sequence)` (`0010:40-42`). The terminal path writes no new outbox row; the transient path increments `dispatch_seq` by exactly one.
- `endpoints.secret_encrypted` is `NOT NULL`; empty string is the current "no secret" sentinel. The gate must treat empty as a deliberate state and never turn a failed unwrap into empty.
- Snapshot immutability is a comment (`0008:2`), not a trigger. If database enforcement is required, a new migration would be needed; it is not part of the approved contract (OD-6).
- A global KEK audit cannot be stored in `audit_logs` without an organization because `organization_id` is `NOT NULL` (`0012:5-11`). Use an external immutable audit record or obtain a separate tenancy/schema decision (OD-5). This report does not authorize a schema change.

**If a later decision adds a schema change** (for example a trigger, a purge index, or a structured reason column), it must be a new migration unit 0014, with a Down migration, CI Down count updated from 13, and explicit Owner approval. It is not part of this plan.

---

## 10. Test and acceptance strategy

### 10.1 Unit tests

- Envelope golden vectors with deterministic DEK and nonce; exact byte comparison; boundary plaintext lengths 1, 16, 1024; all legal `kid`/wrapped lengths.
- Strict parser negatives: wrong magic/version/algorithm/purpose/provider; empty or oversized `kid`; illegal bytes; zero/oversized wrapped length; truncation at every offset; trailing bytes; padded/non-canonical base64; over-long input.
- Tamper matrix: flip each field; expect a specific typed error, never a panic.
- AAD tests: cross-organization and cross-endpoint transplant fail; raw UUID encoding is exactly 16 bytes; total is 64; key slot exclusion permits rewrap.
- Rewrap: replace `kid` and wrapped bytes; ciphertext/nonce/tag unchanged; plaintext identical.
- Provider: missing file, `0644`, malformed JSON, unknown `current`, unknown `kid`, wrong key length, wrong KEK, transient provider error classification.
- Signing: RFC 4231 HMAC-SHA-256 vectors; canonical string golden values; `v1,<base64>`; stable id; later timestamp on retry; exact body; constant-time verification helper.
- Config: mode/gate/provider matrix; budget validation; no secret in errors.
- Worker: every pre-HTTP class with a counting RoundTripper; signer failure; budget re-sample; unsigned gate; no HTTP without signature.
- Fuzz: envelope decoder and canonical signer input; all failures typed.

### 10.2 PostgreSQL integration tests

- Seed an endpoint with a real test envelope and a delivery with the same envelope; decrypt succeeds with the correct org/endpoint AAD.
- Wrong org, wrong endpoint, tampered ciphertext, tampered tag, wrong `kid`, disallowed provider: fail closed, no HTTP.
- Endpoint rotation: two deliveries around rotation decrypt with their own snapshots; old snapshot remains valid; no snapshot rewrap.
- `endpoint_id` present in claim and preparation; AAD uses the raw UUID.
- `RecordDecryptFailure` invariants: no attempt row, unchanged `attempt_count`, event-horizon precedence over lease expiry, fresh database time, fencing, `AckEligible` only after commit.
- Permanent terminal class: `failed`, `completed_at` set, `next_attempt_at` null, lease null, zero attempts, no outbox row.
- Transient class: `pending`/`retrying`, paced `next_attempt_at`, one retry outbox row, zero attempts, unchanged count.
- Completed attempt's `request_headers` contains transmitted `webhook-id`, `webhook-timestamp`, `webhook-signature` and no secret-bearing header.
- Publisher/worker concurrency: exact published sequence, stale generation, unknown T2, rollback, no ACK before commit.
- Recovery expiry at event horizon; no revival of terminal rows.
- Retention: batch isolation, purge order, endpoint FK behavior, soft-deleted endpoint counted, audit counts, unparseable values listed.

### 10.3 Broker and shutdown tests

- RabbitMQ 4.4.1 and 4.3.8; no NACK; ACK after durable; reject-requeue for transient; hold for uncertain; channel close requeues; in-flight drain within budget; stale handle; reconnect generation.
- Provider timeout and broker close interaction: no HTTP after lease loss; no ACK for unknown commit.

### 10.4 Pre-HTTP acceptance matrix

The table in Check 10 is normative. Each row is a test case with assertions on HTTP count, attempt rows, `attempt_count`, state, outbox, and broker disposition. The test must use a counting `RoundTripper` and real database queries, not only a mocked store. A test that asserts "no request" without checking the database, or "no attempt" without checking the count, is insufficient.

### 10.5 Startup and configuration acceptance

| Configuration | Expected |
|---|---|
| Development + gate enabled + valid dev provider | Starts; unsigned allowed only for empty secret |
| Development + gate disabled | Starts; empty secret produces no HTTP, durable `secret_missing` |
| Production + gate enabled | Startup failure |
| Production + valid provider + no gate | Starts; empty secret fails closed; signed path requires provider |
| Unset/unknown mode | Startup failure |
| Local file provider outside development | Startup failure |
| Missing/`0644`/malformed key file, unknown current `kid`, empty allowlist | Startup failure |
| Unknown per-envelope `kid`/provider tag | Starts; per-delivery permanent pre-HTTP disposition, no HTTP |

### 10.6 Security tests

- Logs, metrics, errors, `last_error`, and attempt headers contain no plaintext, envelope bytes, wrapped DEK, or KEK material.
- Diagnostics are bounded at 500 runes and pass the existing scrubber.
- No code path reaches `client.Do` with a non-empty secret and no signature; no production path reaches it with an empty secret.
- Test key files are ephemeral, `0600`, and never committed; CI artifacts contain no secrets.
- Audit output contains counts and identifiers only, never envelopes or plaintext.

### 10.7 Acceptance criteria for the integrated result

1. Every Check 10 class produces no HTTP and no attempt row, and ACKs only after a durable commit.
2. A permanent class produces a terminal, observable outcome with no endless retry; a transient class paces and remains recoverable.
3. A valid envelope produces exactly one signed request; a standard-compliant receiver verifies it.
4. The transmitted body is byte-identical to the HMAC input.
5. The attempt audit contains the transmitted `webhook-*` headers and no secret.
6. The post-resolution budget is provably sufficient or no HTTP starts.
7. Empty-secret unsigned delivery is possible only in development with the gate.
8. Endpoint rotation and snapshot immutability are proven end to end.
9. Retention and audit produce evidence; KEK retirement remains blocked until the separate gates are met.
10. CI is green with no skipped required tests, and the hosted run is recorded.

---

## 11. Security and operational risks

| Risk | Impact | Likelihood | Mitigation / residual |
|---|---|---|---|
| AAD omits `endpoint_id` | Cross-endpoint ciphertext transplant within a tenant | High if implemented without plumbing | Add `endpoint_id` to claim/preparation and AAD; test cross-endpoint transplant. Residual: none after fix. |
| AAD includes the key slot | Rewrap breaks decryption | High if contract misread | Exclude `kid`/wrapped bytes; golden rewrap test. Residual: none after fix. |
| KEK in env/source/image/logs | Full secret compromise | Medium | Provider abstraction; file `0600`; no env KEK; secret-safe errors; `.gitignore`; CI secret scan. Residual: development file provider requires operator discipline. |
| Empty secret sent unsigned in production | Unsigned webhooks trusted by receivers | High with current code | Mode + gate; startup refusal; no fallback; test matrix. Residual: none after fix. |
| Permanent pre-HTTP loop | Outbox growth, noise, delayed terminal visibility | High with B5-only | Terminal zero-attempt path; distinct metric; no outbox row; Owner decision. Residual: state semantics change. |
| Infinite/no retry policy mismatch | Delivery never terminal or invisible | Medium | D6-G decision; metrics; event-horizon expiry as backstop. Residual: documented. |
| Snapshot mutability by SQL | Historical delivery replay/repudiation | Low/Medium | Application never updates; optional trigger (OD-6). Residual: DB role discipline. |
| Transmitted header audit gap | Cannot prove what was signed | Medium | Capture actual header set in `request_headers`; test. Residual: none after fix. |
| Signing body mismatch | Receiver rejects valid delivery | Medium | Single byte slice; exact-body test; no re-encode/compression. Residual: none after fix. |
| No key ID / rotation coordination | Receiver rejects old or new signatures | Medium | Current+previous secrets; 24 h minimum; backlog-aware runbook; no dual signature V1. Residual: receiver capability. |
| URL not signed | Replay to a different path within tolerance | Accepted by U25-B | Receiver dedupe on `webhook-id`, payload routing; document. Residual: accepted. |
| At-least-once duplicates | Duplicate side effects | Inherent | `webhook-id` idempotency; receiver guidance. Residual: receiver responsibility. |
| Provider outage/latency | Transient failures, lease pressure | Medium | 2 s budget; transient classification; no HTTP without sufficient remainder. Residual: provider availability. |
| Purge data loss | Irreversible history loss | Medium | Dry-run, bounded batches, backups, least privilege, no automatic KEK destruction. Residual: operational. |
| Audit race / false zero | Premature KEK retirement | Medium | Wrap-disable, freeze, repeatable-read audit, quiet window, double audit, unwrap monitoring. Residual: no DB barrier; maintenance window required. |
| `audit_logs` tenancy mismatch | Cannot store global KEK evidence | Medium | External immutable audit record or separate tenancy decision. Residual: OD-5. |
| Hosted CI unverified | Undetected integration failure | Medium | Run hosted CI; no-skip gate; matrix. Residual: first run may reveal issues. |
| Docs still say NOT APPROVED/unsigned | Operator misconfiguration | Medium | Authorized documentation reconciliation; runbook updates. Residual: until done. |
| Dev key committed | Dev secret exposure | Low/Medium | `.gitignore`, `0600`, ephemeral test keys, secret scan. Residual: local discipline. |

**Accepted residual risk.** U25-B does not bind the URL/path. A captured request can be replayed to another path on the same host within the timestamp window. This is an accepted property of the approved profile; receivers must deduplicate on `webhook-id` and route on the payload. The report does not propose adding a URL to the signature or a key-ID header without a new Owner decision.

---

## 12. Genuine remaining Owner decisions

These are the only new decisions this review surfaces. None is marked approved. Existing D6/U25 decisions are not reopened; decisions that the baseline explicitly defers (such as production provider selection) are listed because production readiness depends on them, not because this report challenges the deferral.

| ID | Decision | Recommendation | Why it cannot be silently resolved | Effect if deferred |
|---|---|---|---|---|
| OD-1 | Permanent pre-HTTP failure mechanism under D6-G: terminal `failed` with zero attempts (existing state, no schema) vs B5-only pacing to the event horizon vs a new bounded-retry counter/schema. | **Terminal `failed` zero attempts.** It is the smallest coherent alternative, preserves U18, and avoids the loop. | It changes approved state-machine semantics (failure without an HTTP attempt). The baseline requires the limitation to be documented and the alternative not implemented without approval. | Phase 3 cannot implement permanent classes; production cannot truthfully claim D6-G complete. |
| OD-2 | Production KEK provider selection (D6-C deferral): which KMS/Vault, and when. | Defer until a deployment target is named; keep the interface frozen. | The baseline explicitly selects no vendor; production cannot start without one. | Production deployment blocked; development unaffected. |
| OD-3 | Retirement assurance procedure: provider wrap-disable + maintenance freeze + double audit vs a database trigger/barrier vs no retirement in V1. | **No retirement in V1**; revisit after purge and audit exist. | An audit alone cannot prove no concurrent new reference; the approved D6-D precondition is auditable zero. | KEK retirement blocked. |
| OD-4 | Retention/purge coverage and batch values (U2), including endpoint purge and outbox retention. | Approve a concrete purge plan before Phase 6; keep endpoints until dependent deliveries are gone. | Retention is documented but unimplemented; endpoint soft deletion is outside the table; the audit cannot reach zero without purge. | Phase 6 cannot close; KEK retirement blocked. |
| OD-5 | Global KEK audit evidence storage: external immutable audit record vs extending `audit_logs` tenancy (which requires a schema decision). | External immutable audit record for V1; no schema change. | `audit_logs.organization_id` is `NOT NULL`; a global KEK event has no natural tenant. | Audit evidence is not centrally stored; retirement remains manual. |
| OD-6 | Optional database enforcement of snapshot immutability (trigger) or transmitted-header constraints. | Defer; use application invariants first. | Any trigger is a new migration and a write-path behavior change; it is not in the approved contract. | Defense-in-depth gap remains, documented. |

**Authorization actions (not design decisions):**

- **A-1:** Authorize Phase 1–4 code/test implementation with no migrations, no production provider, and no KEK retirement.
- **A-2:** Authorize a separate documentation reconciliation to record the Owner-approved D6/U25 baseline and replace the current `NOT APPROVED`/unconditional-unsigned statements. This report does not do it.
- **A-3:** Production deployment and KEK retirement require a later, explicit authorization after the production gates are met.

---

## 13. Scope and mutation confirmation

This review was **read-only**. The following were inspected: the architecture proposal, runtime-readiness decisions, overview, database schema, ERD, configuration, runbook, Bundle 1 reports, reconciliation/finalization reports, all relevant Go source and tests, migrations, SQL, `.env.example`, `.gitignore`, Compose, and CI workflow.

No Go source, test, migration, SQL, schema, dependency, configuration, `.env`, Compose file, CI workflow, or runtime state was changed. No database or service was started. No migration was applied or rolled back. No row was inserted, updated, or deleted. No file was staged, committed, pushed, reset, checked out, stashed, or cleaned. The pre-existing tracked `.gitignore` modification and all untracked repository paths were preserved. The only created artifact is this report at `docs/operations/d6-u25-implementation-readiness-report.md`; no prior report was overwritten or modified.

**No implementation was started. No code, tests, migrations, configuration, database, or Git state was changed.**

---

## 14. Recommended next action and exact authorization needed

**Recommendation.** Resolve OD-1 first, then authorize Phase 1–4 as code-and-test work with no migrations. Those four phases deliver the complete development implementation: envelope/AAD/provider, configuration and startup refusal, reason-coded pre-HTTP disposition, and signed HTTP with the exact-body and no-unsigned invariants. Phase 5 is the end-to-end dependency on the missing endpoint/event/fill path; Phase 6 is the production retention/audit dependency; Phase 7 is evidence and documentation. Do not authorize production deployment or KEK retirement at this stage. The development file provider must never be described as production-ready.

**Exact authorization requested:**

> Authorize implementation of Phases 1–4 of the D6/U25 integrated plan in `docs/operations/d6-u25-implementation-readiness-report.md`: envelope/AAD/provider libraries, deployment-mode/gate/provider/secret-resolution configuration with startup validation, claim/preparation `endpoint_id` propagation and reason-coded pre-HTTP disposition, and secret resolution plus HMAC signing with the exact-body and no-unsigned invariants. This authorization is limited to Go source and tests; it does not authorize migrations, production provider selection, production deployment, endpoint/event module work, retention/purge execution, KEK retirement, or documentation edits. It includes the Owner decision on OD-1: permanent pre-HTTP classes terminalize in the existing `failed` state with zero attempt rows, a bounded reason in `last_error`, a distinct metric, and no retry outbox row. No new decision is approved by the report itself.

If the Owner prefers OD-1's B5-only option, the authorization should say so explicitly and acknowledge that permanent faults will pace until the event horizon and will not be terminal until expiry. The implementation must not silently choose either option.

**No implementation was started. No code, tests, migrations, configuration, database, or Git state was changed.**
