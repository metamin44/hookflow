# D6/U25 Proposal Finalization — Change Report

**Date:** 2026-11-24
**Task:** Finalize the D6/U25 architecture decision proposal (documentation only)
**Result:** PASS WITH FINDINGS
**Proposal (updated in place):** `docs/architecture/d6-u25-decision-proposal.md` (Rev 2)
**This report:** `docs/operations/d6-u25-proposal-finalization-report.md`

Nothing in this task approved, implemented, or scheduled D6 or U25. No Go source, test, dependency, migration, schema, configuration, Compose, CI, environment file, database, or Git state was changed.

> **Post-report reconciliation (2026-11-24):** a later documentation-only cross-document reconciliation added matching supersession notes to `runtime-readiness-decisions.md`, `overview.md`, and `database-schema.md`, and created [d6-u25-cross-document-reconciliation-report.md](d6-u25-cross-document-reconciliation-report.md). Statements in this report that the source documents "were not edited" describe the finalization task at the time it ran; they are not the current state. No D6/U25 decision was approved or changed.

---

## 1. Files inspected

Documentation:

- `docs/architecture/d6-u25-decision-proposal.md` (the document being revised)
- `docs/architecture/database-schema.md` — retention table and purge-order text (§6, lines 1703–1731); delivery/attempt schema notes
- `docs/architecture/overview.md` — retention windows (line 743), child-first purge and U2 (lines 795–797)
- `docs/architecture/runtime-readiness-decisions.md` — D6 preamble/status (line 11), historic envelope and retirement text (lines 486–514, especially 496–500), status table (lines 726–728)

Code and migrations (read-only):

- `db/migrations/0005_endpoints.up.sql` — `secret_encrypted`, `signing_scheme`, `deleted_at` soft delete and filtered unique indexes
- `db/migrations/0008_deliveries.up.sql` — snapshot immutability comment, `endpoint_id`, `secret_encrypted`, state/`completed_at` constraints, `last_error` 500-character bound
- `db/migrations/0009_delivery_attempts.up.sql` — U18 write-on-completion comment, `finished_at NOT NULL`, `error_class` enum, attempt-number uniqueness
- `internal/app/delivery/store.go` — `Claim`, `RecordDecryptFailure`, `expireDecryptFailure`, `insertAttemptSQL`, completion and decrypt-failure update SQL
- `internal/app/delivery/pointer.go` — claim/preparation projections and budget checks
- `internal/app/worker/processor.go` — `PrepareHTTP` requirement, `ErrInsufficientLease` handling, `ErrSecretUnavailable` branch, completion path
- `internal/app/worker/executor.go` — fail-closed placeholder, frozen `Remaining` check, URL/header validation, `client.Do`
- `internal/platform/config/config.go` and `internal/platform/config/runtime.go` — loaded variables, lease invariant validation, defaults
- `cmd/worker/lifecycle.go`, `internal/app/idempotency/sweep.go` — idempotency sweep wiring; no events/deliveries purge
- `test/integration/worker_integration_test.go`, `internal/app/worker/executor_test.go` — assertions that no signature header is sent

No external source was re-fetched for this revision. The §10 [V] evidence table is retained; V11 (NIST SP 800-38D) and V12 (NIST SP 800-57) remain unread as already disclosed.

---

## 2. Corrections made

### A. U18 attempt semantics preserved

- Removed the Rev 1 recommendation that a signing-construction programming fault produces a terminal `internal` **attempt**. The proposal now states explicitly: no HTTP, no `delivery_attempts` row, no `attempt_count` increment, no unsigned fallback.
- Added §4.7 "Pre-HTTP internal failures" analysing the existing durable B5 mechanics. Conclusion: they can **physically** store such a failure without violating U18 (no new column or state), but their approved semantics are scoped to secret unavailability, so using them unchanged would misclassify a programming fault as a decryption failure.
- Added **D6-G**, an Owner decision offering (a) a generalized pre-HTTP durable path with explicit reason codes (`secret_unavailable`, `secret_missing`, `signing_internal`) and distinct metrics, or (b) a terminal `failed` transition with zero attempts (a new transition). No state, column, or disposition is invented; implementation is blocked until one option is approved.
- Added bounded, scrubbed visibility: `deliveries.last_error` (already limited to 500 characters), a low-cardinality counter, and an alert.

### B. Unsigned development delivery explicitly gated

- Added **D6-F**: required deployment mode (`HOOKFLOW_ENV` = `development` | `production`, no default) plus `HOOKFLOW_ALLOW_UNSIGNED_DELIVERY` (default `false`).
- Unsigned delivery is permitted only when mode = `development` **and** the gate is `true`. Production, unset/unknown mode, or gate `false` fails closed on an empty secret with no HTTP through the pre-HTTP no-attempt path (`secret_missing`).
- Non-empty secrets must pass decryption, plaintext validation, and signature construction before `client.Do`; no error path falls back to unsigned.
- Invalid or contradictory mode/gate/provider combinations fail startup.
- The proposal states that the gate is **new configuration** (the repository has no deployment-mode value), not an existing approved contract.

### C. Deployment configuration errors separated from envelope errors

- Added §4.5.1 with two matrices:
  - **Startup failure:** unset/unknown/unimplemented provider, local provider outside development, missing/0644/malformed key file, wrong key length, unknown `current`, unknown configured `kid`, empty/invalid/unsupported allowlist, contradictory settings.
  - **Per-delivery classification:** unknown/unsupported/not-allowlisted provider tag (`provider_disallowed`), malformed envelope (`envelope_invalid`), unknown `kid`/access denied/invalid ciphertext/auth failure (`key_or_auth`), transient provider faults (`provider_transient`).
- Added the rule that a global deployment defect must produce one startup failure or one deployment-scope alert, never a stream of per-delivery failures.
- Updated §4.4, §4.6, §6.7, D6-C, and the acceptance criteria to match.

### D. Retention and KEK retirement corrected

- Replaced the Rev 1 statement that deliveries retention was unknown and Owner-supplied with the documented facts: events 90 days, deliveries 90 days, delivery_attempts 90 days inherited, idempotency 72 hours.
- Removed the recommendation that the Owner choose a retention duration; the duration is already fixed in `database-schema.md` §6. D6-D now asks for the **rule**, not a number.
- Rewrote retirement as reference-based: a KEK version remains available while any retained endpoint envelope or delivery snapshot references it; 90 days is a horizon, not deletion permission; retirement requires purge plus a bounded reference audit at zero references.
- Documented the asymmetric rewrap behavior: endpoint envelopes may be rewrapped; delivery snapshots must not be rewrapped if that would violate A8.
- Added per-`kid` accounting: usage metrics (early warning), read-only header audit (authoritative, lists unparseable values), provider registry (what can still decrypt), and a six-step retirement procedure.
- Recorded the repository discrepancy at `runtime-readiness-decisions.md:496–500` (calendar-based retirement and permitted snapshot rewrap) and stated that this proposal supersedes it **as a proposal** without editing the source.

### E. Lease budget preserved and clarified

- Kept `SecretResolutionBudget` at the proposed 2 s, subject to Owner approval.
- Stated the complete invariant with five components: `30 s > 10 + 2 + 3 + 10 + 2 = 27 s`.
- Stated explicitly that the remaining 3 s is **slack**, not a sixth component and not spendable budget.
- Required the Worker to re-sample the clock and recompute the remainder after secret resolution and before HTTP; the frozen pre-unwrap `Remaining` is not trusted.
- Specified the post-resolution requirement as the four remaining components (`HTTPDeadline + HandshakeBudget + PostProcessBudget + ShutdownReserve`) and reuse of the existing `ErrInsufficientLease` handling when it is not met.
- Required the durable disposition and settlement to use a fresh bounded context, not one cancelled by the resolution deadline.

### F. Final consistency corrections

- Removed the per-delivery `signing_scheme` check and the `scheme_unsupported` reason. The snapshot does not carry the scheme, so V1 treats `hmac_sha256` as the protocol constant and leaves rejection of any other endpoint value to the future write path, as §6.5 already recommended. §4.7 steps were renumbered and all cross-references corrected.
- Corrected the decoder's pre-decode input cap from 4,096 **characters** to 5,468 characters, the unpadded base64url length of the stated 4,096-byte decoded cap plus the `hfse1.` prefix (previously the byte cap was unreachable). The v1 maximum remains 2,151 bytes / 2,874 stored characters.
- Aligned `key_or_auth` classification with the required rule in §4.4, §4.5.1, and §4.7: unknown `kid`, access denied, invalid ciphertext, or authentication failure is permanent **unless the provider's classified error supplies transient evidence for that specific response**, matching §6.7.
- Removed an "audited exception" phrase from retirement step 3. A row that still exists is still a reference; step 4 requires the audit at zero.
- Stated that "permanent" means retries cannot change the outcome, not that the row terminalizes immediately; B5 still paces to the event horizon.
- Added the transmitted-header audit fix (G7) to the implementation plan, integration test, and acceptance criteria, so U25 does not leave `delivery_attempts.request_headers` describing only the snapshot.
- Specified the re-sample arithmetic as `preparation.Remaining` minus monotonic elapsed time measured from before `PrepareHTTP` (conservative), or a fresh database time reading, so the recomputation does not depend on unbounded host/database clock skew.
- Clarified that `SecretResolutionBudget` belongs only to the pre-resolution `PrepareHTTP` requirement and configuration validation; the executor's post-resolution check remains the four-component 25 s, otherwise a successful unwrap would be rejected by a 27 s check.

### Consistency corrections across the document

- Executive summary now describes eleven decisions (D6-A..D6-G, U25-A..U25-D), the U18 constraint, the gate, the startup/envelope split, reference-based retirement, and the exact budget.
- §2 limits corrected with the verified retention policy and the unimplemented purge.
- §3.4 corrected: 2 s becomes a budget component and 3 s remains slack.
- §3.6 records that no deployment-mode variable exists.
- §3.8 gains G9–G12 for the pre-HTTP representation, the unsigned gate, retention implementation, and provider startup validation.
- §5 summary table gains rows for retention, retirement, pre-HTTP faults, empty-secret gating, and provider classification; budget row corrected.
- §6.2 and §6.7 gate empty-secret delivery, split provider-configuration from envelope failures, and replace the terminal-attempt signing row with the D6-G path.
- §7 adds the gate, U18, startup-validation, and retirement controls; residual risks record deterministic pacing and key accumulation while the purge is missing.
- §9 adds the gate, reason codes, startup validation, re-sampling, retirement audit, and the corresponding tests and acceptance criteria.
- §10 updates repository references and evidence-class text; §11 adds explicit non-authorizations.

---

## 3. Retention verification evidence

| Fact | Evidence |
|---|---|
| Events retention 90 days, by `received_at` | `database-schema.md` §6 table, line 1707 |
| Deliveries retention 90 days, by `created_at`, removed with parent event | `database-schema.md` lines 1708; `overview.md` line 743 |
| Attempts 90 days inherited, purged before deliveries | `database-schema.md` line 1709 |
| Idempotency 72 hours (U4 approved), U43 sweep approved | `database-schema.md` line 1711; `internal/app/idempotency/sweep.go`; `cmd/worker/lifecycle.go` |
| Purge order child-first | `database-schema.md` lines 1718–1723; `overview.md` lines 795–797 |
| Purge coverage and batch sizes open (U2) | `database-schema.md` lines 1729–1731; `overview.md` line 797 |
| No events/deliveries/attempts purge implementation | `cmd/worker/lifecycle.go` wires only the idempotency sweep and Recovery; no retention purge exists |
| Endpoints soft-deleted; not in retention table | `0005_endpoints.up.sql:20,35,42` (`deleted_at`, filtered unique indexes); `database-schema.md` §6 lists no `endpoints` row |

---

## 4. Repository discrepancies and findings

1. **Calendar-based retirement text.** `runtime-readiness-decisions.md:496–500` says to keep decryption enabled "for at least the event retention window (90 days) or until a verified rewrap pass completes" and says delivered snapshots "can be rewrapped by a bounded background job". This conflicts with A8 snapshot immutability and with reference-based retirement. The proposal records the conflict and supersedes the wording **as a proposal**. The source file was not edited during that task; the later cross-document reconciliation added an explicit supersession note there (see the banner above).
2. **Purge not implemented.** The 90/90/72 policy is documented but no Events/Deliveries/Attempts purge exists, and U2 leaves table coverage and batch sizes open. Consequence stated in the proposal: any KEK that has wrapped a delivery snapshot has a reference indefinitely until purge exists or an audited manual deletion occurs, so retirement cannot be claimed safe today.
3. **Endpoints outside retention.** Soft-deleted endpoint rows and their envelopes persist, so 90 days is not an upper bound on endpoint-envelope references.
4. **Pre-HTTP error handling today.** `executor.go` returns an error for invalid URL, invalid method, and insufficient frozen budget; `processor.go:112–130` settles these as transient requeues without a `Complete` call, so no attempt row is written. The revised table records this existing behavior instead of labelling it a terminal attempt.
5. **Attempt audit header gap (pre-existing, G7).** `Complete` persists snapshot headers, not transmitted headers; U25 makes this visible. The proposal now includes persisting the transmitted `webhook-*` headers in the implementation plan and tests. No code was changed here.
6. **No provider or mode configuration exists.** There is no deployment-mode value, provider selector, key path, or unsigned gate in `internal/platform/config`; §3.6 and D6-C/D6-F record this.
7. **Historical AAD construction differs.** `runtime-readiness-decisions.md:487–490` builds the AAD from canonical lowercase **text** ids and includes `header_prefix`; this proposal uses raw 16-byte UUIDs and excludes the header for fixed-size encoding and rewrap compatibility. The divergence is recorded in §4.3 and is superseded **as a proposal** only. The source was not edited during that task; the later cross-document reconciliation added an explicit supersession note there (see the banner above).

No implementation was modified to make the proposal appear consistent.

---

## 5. Updated Owner decision list (all NOT APPROVED)

| ID | Scope | Change in this revision |
|---|---|---|
| D6-A | Cipher and envelope format | Unchanged |
| D6-B | DEK scope and AAD | Unchanged |
| D6-C | Custody, provider boundary, startup validation | Extended with startup-versus-envelope classification |
| D6-D | Rotation and reference-based retirement | Rescoped: no duration choice; reference audit at zero; discrepancy recorded |
| D6-E | Budget and failure routing | Clarified: five components, 27 s, 3 s slack, re-sampling |
| D6-F | Development-only unsigned-delivery gate | **New** |
| D6-G | Pre-HTTP internal-failure disposition | **New** |
| U25-A | Algorithm and key usage | Unchanged |
| U25-B | Canonical input and headers | Unchanged |
| U25-C | Timestamp and replay | Unchanged |
| U25-D | Signing-key rotation | Unchanged |

The approval table marks every row `NOT APPROVED`.

---

## 6. Evidence gaps

- NIST SP 800-38D and SP 800-57 were not read during preparation; the 2^32 GCM bound is supported by Go documentation (V3) and Vault Transit (V9) only.
- No production provider or deployment target is named, so provider latency, IAM, and availability behavior remain unverified.
- No endpoint-management API exists, so the write path (secret generation, encryption, storage) is a contract only.
- Retirement safety cannot be demonstrated until the retention purge exists (U2); the audit procedure is specified but not implemented.
- No benchmark, throughput, or third-party verifier compatibility claim is made.

---

## 7. Scope confirmation

Only two files were written:

1. `docs/architecture/d6-u25-decision-proposal.md` (revised in place; identity and history preserved with a Rev 2 note)
2. `docs/operations/d6-u25-proposal-finalization-report.md` (this report)

No Go source file, test, `go.mod`/`go.sum`, migration, schema, `.env` or `.env.example`, Docker Compose file, CI file, or runtime configuration was created or modified. No migration was run and no database was touched. No Git command was executed, so no staging, commit, push, stash, reset, checkout, or other Git mutation occurred. No proposed decision is presented as approved, and implementation must not start until the Owner explicitly approves the relevant rows.
