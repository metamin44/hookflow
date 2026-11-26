# D6/U25 Cross-Document Reconciliation Report

**Date:** 2026-11-24
**Task:** Reconcile the D6/U25 proposal with the authoritative architecture and operations documentation (documentation only)
**Result:** **PASS WITH FINDINGS**
**Proposal reconciled:** [`docs/architecture/d6-u25-decision-proposal.md`](../architecture/d6-u25-decision-proposal.md) Rev 2 plus the same-day cross-document notes

> **Status addendum (2026-11-24, later the same day; supersedes this report's status statements).** The Owner
> approved D6-A…D6-G and U25-A…U25-D as architecture, and the development runtime implements envelope
> encryption/decryption, the deployment-mode gate and Standard Webhooks v1 signing under a local file KEK.
> Production KMS/Vault custody and production readiness remain **NOT IMPLEMENTED / NOT READY**. The statements
> below ("NOT APPROVED", "no AAD/envelope codec") are retained as the pre-approval historical record. See
> [`phase6.1-verification-closure-report.md`](phase6.1-verification-closure-report.md) and `overview.md` §16.9.

Nothing in this task approved, implemented, or scheduled D6 or U25. D6-A through D6-G and U25-A through U25-D remain **NOT APPROVED**. No Go source, test, dependency, migration, schema, configuration, Compose, CI, environment file, database, or Git state was changed.

---

## 1. Files inspected and modified

### Inspected

Architecture:

- `docs/architecture/d6-u25-decision-proposal.md` — §3 findings, §4.3 AAD, §4.4 rotation/retirement, §5 summary, §8 decision table, §9 implementation plan, §10 evidence
- `docs/architecture/runtime-readiness-decisions.md` — §2 status, §6 worker status, §8.1 D6a proposal, §10 consistency review, §11 activation matrix, §14 approval bundles
- `docs/architecture/overview.md` — revision history and status, §5.2 snapshot, §7 retention, §8 security posture, §12.15 U6 row, §13 residual questions, §16 limitations
- `docs/architecture/database-schema.md` — top revision notes, §1 decision mapping, §2 deletes convention, §4.5 endpoints, §4.5.1 secret models, §4.7 deliveries, §4.9/§5 transaction notes, §6 retention, §12 residual approvals
- `docs/architecture/erd.mmd` — U6/AAD comment block

Operations and repository documentation:

- `docs/operations/runbook.md` — secret redaction, production blockers
- `docs/operations/configuration.md` — worker cross-field invariants
- `docs/operations/d6-u25-proposal-finalization-report.md` — prior finalization findings
- `docs/operations/bundle1-final-closure-report.md` — production-readiness wording
- `README.md` — current scope and worker description

Code, migrations, and tests (read-only):

- `db/migrations/0005_endpoints.up.sql`, `0005_endpoints.down.sql`
- `db/migrations/0008_deliveries.up.sql`
- `internal/app/delivery/pointer.go`, `store.go`
- `internal/app/worker/executor.go`, `processor.go`, `retry.go`
- `cmd/api/lifecycle.go`, `cmd/api/integration_test.go`
- `cmd/worker/lifecycle.go`
- `test/integration/harness_test.go`
- `internal/modules/endpoint/` (contents), `internal/app/`, `internal/platform/` (file inventory)
- Go-wide searches for `HF1`, `hfse1`, `hookflow.secret.v1`, `header_prefix`, `crypto/aes`, `crypto/cipher`, `cipher.NewGCM`, `SecretCiphertext`, `secret_encrypted`, `decrypt`, `encrypt`, `purge`, and `retention`

### Modified (documentation only)

- `docs/architecture/d6-u25-decision-proposal.md`
- `docs/architecture/runtime-readiness-decisions.md`
- `docs/architecture/overview.md`
- `docs/architecture/database-schema.md`
- `docs/architecture/erd.mmd` (comment text only)
- `docs/operations/runbook.md`
- `docs/operations/configuration.md`
- `docs/operations/d6-u25-proposal-finalization-report.md` (status banner)
- `docs/operations/bundle1-final-closure-report.md` (status banner)
- `README.md`
- `docs/operations/d6-u25-cross-document-reconciliation-report.md` (this report)

No Go source, test, dependency, migration, DDL, schema object, runtime configuration, Compose file, CI workflow, environment file, or database was modified.

---

## 2. Soft-deleted Endpoint secrets: evidence

### 2.1 Deletion is soft at the schema level

`db/migrations/0005_endpoints.up.sql` defines:

- `secret_encrypted text NOT NULL` (line 10): an endpoint always has a persisted reversible envelope.
- `deleted_at timestamptz` (line 20): deletion is represented by a timestamp, not row removal.
- `uq_endpoints_live_name ... WHERE deleted_at IS NULL` (lines 33–35), `idx_endpoints_org_live ... WHERE deleted_at IS NULL` (lines 37–39), and `idx_endpoints_org_deleted` (lines 41–42): live reads exclude soft-deleted rows; the row and its columns remain.

`0005_endpoints.down.sql` only drops the table when the migration is explicitly reversed; it is not a runtime cleanup path.

### 2.2 There is no Endpoint application implementation or purge

- `internal/modules/endpoint/` contains only `.gitkeep`; there is no endpoint service, repository, deletion method, or secret-rotation implementation.
- `cmd/api/lifecycle.go` describes the API as "health-only"; the integration test checks only `/health/live` and `/health/ready` (`cmd/api/integration_test.go:49`). No endpoint CRUD route exists.
- No production code sets `deleted_at`, nulls `secret_encrypted`, or deletes endpoint rows. A repository-wide search for `purge`/`retention` in `cmd/` and `internal/` returned no production purge path. The only idempotency cleanup is the U43 sweep wired in `cmd/worker/lifecycle.go`; it does not touch endpoints, events, deliveries, or attempts.
- The only demonstrated Endpoint row deletion is integration-test cleanup: `DELETE FROM endpoints WHERE organization_id = $1::uuid` at `test/integration/harness_test.go:223`. That is test teardown, not production behavior.
- The organization foreign key is `ON DELETE CASCADE`, which is a schema-level hard-delete path, but organization erasure (U7) is open and is not an endpoint-cleanup capability.

### 2.3 Consequence

A soft-deleted endpoint row retains `secret_encrypted` indefinitely in the current repository. Because no endpoint purge exists and `endpoints` is absent from the §6 retention table, 90 days is not an upper bound on how long an endpoint envelope can reference a KEK `kid`. This is a demonstrated property of the current schema and code, not an inference from documentation.

---

## 3. Exact KEK reference-accounting rule

The reconciled rule, recorded in `d6-u25-decision-proposal.md` §4.4 (D6-D, **NOT APPROVED**) and referenced from the other documents, is:

1. **Every persisted envelope that may still require decryption is a KEK reference.** This includes:
   - every `endpoints.secret_encrypted` row, **including a soft-deleted row** (`deleted_at IS NOT NULL`) unless its ciphertext has actually been purged; and
   - every `deliveries.secret_encrypted` snapshot until the delivery row is actually purged.
2. **The two populations are accounted for independently.** Endpoint envelopes may be rewrapped under a new KEK; a successful rewrap removes the old-`kid` reference. Delivery snapshots must not be rewrapped because A8 makes `url`, `http_headers`, and `secret_encrypted` immutable after acceptance; only purge removes a snapshot reference.
3. **Retention windows are horizons, not permission.** Events 90 days, Deliveries 90 days, Attempts 90 days inherited, and Idempotency 72 hours describe when purge is expected. They do not authorize KEK destruction while ciphertext still exists.
4. **Retirement requires zero actual references.** Before destroying a version: disable wrapping, rewrap or confirm absence of every endpoint envelope, confirm the purge actually removed every dependent snapshot, run a bounded reference audit at zero, observe a quiet usage window, then disable unwrapping and destroy. An unparseable value is a reference, not an absence.
5. **Operational conclusion for this repository: KEK retirement is blocked.** There is no endpoint purge, no Events/Deliveries/Attempts purge, and no reference-audit tool. The precondition "zero references" cannot currently be established. Keeping every version available for unwrap is the safe behavior.

The 90/90/72 values themselves are unchanged: Events 90 days (`received_at`), Deliveries 90 days (`created_at`, removed with the parent event), Attempts 90 days inherited (`started_at`), Idempotency 72 hours (`expires_at`). Purge order remains child-first: `outbox_messages` → `delivery_attempts` → `deliveries` → `events`.

---

## 4. Historical vs proposed AAD

### 4.1 Historical construction (documented, not implemented in the current tree)

`runtime-readiness-decisions.md` §8.1 originally stated:

```text
AAD = "hookflow.secret.v1" || header_prefix || organization_id (16 bytes) || endpoint_id (16 bytes)
```

where `header_prefix` was "every byte from `magic` through `wrapped DEK`", and the next bullet said the ids were "rendered canonically (lowercase text)". Those two statements are internally inconsistent: the formula labels each id as 16 bytes, while the bullet specifies 36-byte text.

The historical envelope table defined an `HF1` magic, a fixed 48-byte wrapped DEK, and a `kid` of 1–64 bytes. With that layout:

- `header_prefix` is 78–141 bytes: magic 3 + alg 1 + (kid length 1 + `kid` 1–64) + wrap nonce 12 + wrapped DEK 48 + data nonce 12.
- The historical header-prefix-plus-text form would therefore be variable-length: 18 + (78–141) + 36 + 36 = **168–231 bytes**.
- The historical header-prefix-plus-raw-bytes form, if the formula's "16 bytes" label were followed, would be 18 + (78–141) + 16 + 16 = **128–191 bytes**.

**Implementation status: no implementation exists in the current repository tree; prior Git history was not inspected.** Repository-wide Go searches found no `HF1`, `hfse1`, `hookflow.secret.v1`, `header_prefix`, `crypto/aes`, `crypto/cipher`, `cipher.NewGCM`, or AAD builder. The only matches for `secret_encrypted` / `SecretCiphertext` are the storage and snapshot fields, the fail-closed guard in `internal/app/worker/executor.go:158-163`, the B5 disposition in `internal/app/delivery/store.go`, and tests. There is no envelope codec to migrate and no stored envelope format to preserve. The documentation describes the format as a proposal, and no evidence of an implemented format was found; this report does not claim that no implementation ever existed in deleted history.

### 4.2 Proposed construction (D6-B, NOT APPROVED, not implemented)

`d6-u25-decision-proposal.md` §4.3 defines exactly one proposed byte string:

```text
"hookflow.secret.v1"   18 bytes ASCII
alg                     1 byte  (0x01)
purpose                 1 byte  (0x01)
data_nonce             12 bytes
organization_id        16 bytes  raw UUID bytes
endpoint_id            16 bytes  raw UUID bytes
---------------------------------------------
total                  64 bytes
```

`kid`, `provider`, and the wrapped DEK are excluded so that an endpoint DEK can be rewrapped under a rotated KEK without breaking the payload tag. Domain separation, algorithm/purpose binding, nonce binding, organization binding, and endpoint binding are all preserved.

### 4.3 Resolution options and consequences

- **Recommended for approval (D6-B):** raw 16-byte UUIDs in the fixed 64-byte layout above. This is the single proposed definition now referenced by the reconciled documents.
- **Text ids in the same fixed layout:** 18 + 1 + 1 + 12 + 36 + 36 = **104 bytes**. Every AAD vector and length statement would change.
- **Historical header-prefix-plus-text construction:** variable **168–231 bytes**, and it is incompatible with rewrap without payload re-encryption unless the header is excluded or versioned.
- **Historical header-prefix-plus-raw-bytes construction:** variable **128–191 bytes**, with the same rewrap problem.

No choice is approved by this report. If the Owner selects a text or header-prefix form, a compatibility/versioning decision is required before data exists; the proposal records that as part of D6-B, not as an implementation detail.

---

## 5. Superseded statements corrected

Historical text was retained and marked, not silently erased.

### 5.1 AAD

| Location | Correction |
| --- | --- |
| `runtime-readiness-decisions.md` §8.1 | Added a section-level reconciliation banner and an AAD note after the historical bullets. It names the 16-byte-vs-text inconsistency, records that neither form was implemented, states the 64-byte proposed contract, and gives the 104-byte text-layout and 168–231-byte historical ranges. |
| `overview.md` top reconciliation note, revision-16 history, U6 row, U6 residual table | Marked canonical-lowercase-text AAD as historical and superseded for current guidance; points to the 64-byte proposed definition and states it is NOT APPROVED and unimplemented. |
| `database-schema.md` top note and revision-16 block | Same supersession marking; U6 remains open. |
| `erd.mmd` U6 comment | Same marking in the comment block. |
| `d6-u25-decision-proposal.md` §4.3 | Removed the stale "source is not edited here" statement; records that the authoritative documents now carry matching notes and that neither historical nor proposed AAD is implemented. |

### 5.2 Retirement and snapshot handling

| Location | Correction |
| --- | --- |
| `runtime-readiness-decisions.md` §8.1 rotation/retirement bullets | Retained as history but explicitly superseded. Added: endpoint envelopes may be rewrapped; delivery snapshots must not; soft-deleted endpoint envelopes count; 90 days is a horizon; retirement requires purge plus audit at zero; current repository is operationally blocked. |
| `runtime-readiness-decisions.md` §10 consistency row 10 | Replaced the calendar-window claim with the reference-based proposed rule and marked the prior wording superseded. |
| `runtime-readiness-decisions.md` §14 D6 bundle | Added the current proposal status and a D6-D row stating operational blockage. |
| `overview.md` §5.2, §7, §8, U6 residual table | Distinguished endpoint rewrap from snapshot non-rewrap; added soft-deleted endpoint reference accounting and the retention-is-not-a-key-calendar rule. |
| `database-schema.md` §4.5, §4.5.1, §4.7, §4.9, §6, §12 notes | Added the soft-delete retention evidence, the non-rewrap rule, and reference-based retirement; state that no endpoint purge or event/delivery purge exists. |
| `d6-u25-decision-proposal.md` §4.4 and §10 | Added the endpoint module/purge evidence, the explicit "operationally blocked" conclusion, and updated evidence references. |
| `runbook.md` §6/§7, `configuration.md`, `README.md` | Replaced "development snapshots" wording that implied a deployment-mode gate. The empty-secret path is described as unsigned and ungated; D6-F is proposed, NOT APPROVED. Added the KEK-retirement blocker to the runbook. |

### 5.3 Gate wording

The phrase "empty-secret unsigned development snapshots execute HTTP" could be read as describing an implemented Development gate. It was corrected in all current-status documents to state the actual behavior: empty-secret snapshots execute unsigned HTTP unconditionally, and no deployment-mode gate exists. Historical quotations remain only where they are explicitly identified as superseded, and the two earlier operations reports carry status banners. D6-F (Development-only unsigned-delivery gate) remains **NOT APPROVED**.

### 5.4 Historical reports

`d6-u25-proposal-finalization-report.md` now carries a banner explaining that its "source was not edited" statements describe that earlier task, not the current state. `bundle1-final-closure-report.md` now carries a status addendum distinguishing its historical "development snapshot" wording from the current ungated behavior. Neither report's findings or approvals were rewritten.

---

## 6. Cross-document consistency validation

| Check | Result |
| --- | --- |
| A8 snapshot immutability | Preserved everywhere: `url`, `http_headers`, `secret_encrypted` immutable after acceptance; no document authorizes snapshot mutation. |
| Endpoint rewrap vs snapshot non-rewrap | Clearly separated in proposal §4.4, runtime §8.1, overview §5.2/§7, schema §4.5.1/§4.7/§6. |
| KEK retirement requires zero actual references | Stated in the proposal, runtime, overview, schema, runbook, and this report. |
| Soft-deleted Endpoint envelopes included | Explicitly included; evidence recorded from migration, module layout, API surface, and test cleanup. |
| Retention values | Unchanged: Events 90 days, Deliveries 90 days, Attempts 90 days inherited, Idempotency 72 hours. |
| Retention does not authorize KEK deletion | Stated in all reconciled documents. |
| AAD has one exact proposed byte definition | Proposal §4.3 is the sole proposed definition (64 bytes); all other statements are marked historical/superseded. |
| D6/U25 status | All decision rows remain **NOT APPROVED** in proposal §8; no document claims approval. |
| Implementation claims | No document claims an envelope, AAD builder, key provider, signing scheme, or retention purge is implemented. |
| Prior approved decisions | A8, A5, U4/U43, U33, U37/U38, and Bundle 1 numerics are unchanged. No approved decision was silently overturned. |

---

## 7. Remaining implementation dependency and Owner decisions

### Owner decisions still open

- **D6-A** cipher/envelope format — **NOT APPROVED**
- **D6-B** DEK scope and AAD — **NOT APPROVED**; the binary 64-byte vs text 104-byte vs historical header-prefix choice is unresolved
- **D6-C** KEK custody/provider/startup validation — **NOT APPROVED**
- **D6-D** rotation and reference-based retirement — **NOT APPROVED**
- **D6-E** pre-HTTP budget amendment and crypto failure classification — **NOT APPROVED**
- **D6-F** Development-only unsigned-delivery gate — **NOT APPROVED**
- **D6-G** durable representation of pre-HTTP internal failures — **NOT APPROVED**
- **U25-A through U25-D** signing algorithm, canonical input/headers, timestamp/replay compatibility, and signing-key rotation — **NOT APPROVED**

### Implementation dependencies

- An envelope codec and AAD builder (none exists).
- A KEK provider and non-secret provider configuration (none exists).
- `endpoint_id` in the claim/preparation path and SQL projections (currently absent).
- Endpoint CRUD and secret lifecycle implementation (the module is `.gitkeep` only).
- An Events/Deliveries/Attempts retention purge (U2 open; none implemented).
- An endpoint purge or an approved equivalent cleanup for soft-deleted rows (none exists).
- A bounded reference-audit tool covering both `endpoints` and `deliveries` (none exists).
- A deployment-mode gate for unsigned delivery (D6-F proposed; none exists).
- Signing implementation and the transmitted-header audit fix (U25; none exists).

### Is KEK retirement operationally safe?

**No.** Under the proposed reference-based rule, retirement is operationally blocked in the current repository because no endpoint purge, no event/delivery purge, and no reference-audit tool exist. The safe direction is to keep every configured KEK version available for unwrap. No key may be retired on elapsed time alone.

---

## 8. Scope and mutation confirmation

- **None** of these were modified: Go source, Go tests, `go.mod`/`go.sum`, dependencies, migrations, DDL, schema objects, database data, runtime configuration, `.env`/`.env.example`, Compose files, CI workflows, or any Git state.
- **No** migration was run; **no** database was altered.
- **No** Git command was executed; nothing was staged, committed, pushed, stashed, reset, checked out, or cleaned.
- **No** D6/U25 implementation was started.
- **No** Owner decision is presented as approved. Every D6/U25 decision remains **NOT APPROVED**.

The only changes are documentation edits and this report.
