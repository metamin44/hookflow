# Phase 6.1 verification closure report

**Date:** 2026-11-24
**Repository:** `github.com/metamin44/hookflow`, branch `main`, HEAD `c6db32fd50ee8d7beb3108674b4cf46443d2aba4`
**Authorization:** Owner-approved Phase 6.1 Verification Closure — tests and documentation only.
**Scope executed:** automated policy, concurrency, lock-order, Cache-Control and CLI/accesskeys tests; documentation
status/policy reconciliation; safe local verification; this report. No application behavior, production code, SQL,
migration, schema, `.env`, keyring, Compose, deployment manifest or Git state was changed.

**Status update (2026-11-25):** G-1 was subsequently fixed at the service boundary under separate authorization;
its boundary unit tests pass. The F-3/F-4/F-5 follow-up is recorded in §13.5. The CLI end-to-end rerun is recorded
as NOT RUN in §13 because the development database was unavailable. The original finding and this report's Phase 6.1
evidence are preserved unchanged.

## 1. Executive verdict

**PASS WITH FINDINGS.**

All critical Phase 6.1 requirements pass with observed evidence: organization-state policy (including archived
deactivation denial and the approved suspended soft-delete/key-revocation path), both transaction orders for
subscription-versus-soft-delete, bounded lock-order stress, endpoint-creation-versus-status-transition races, cap-1
concurrent creation, `Cache-Control: no-store` and no-leak behavior, and the CLI/accesskeys key-handling contract. Git,
database schema and the development environment were preserved.

Findings are non-critical and do not block the development closure:

- **G-1:** `accesskeys.List` returns `apikey.Record` values that include the Argon2id PHC verifier in `SecretHash`.
  The CLI prints only metadata and never the verifier; no HTTP route exposes it. This is an in-memory service API
  exposure, not a leak to users, logs or responses. Fixing it is an application change and was not authorized.
  **FIXED 2026-11-25 at the service boundary; boundary unit tests pass; CLI integration rerun NOT RUN — see §13.**
- **Hosted CI:** NOT RUN / NOT VERIFIED (no commit or push authorized).
- **Production:** KMS/Vault custody and production readiness remain NOT IMPLEMENTED / NOT READY.
- `golangci-lint` was not rerun; the Phase 6 advisory count (17) is retained for production code and not claimed for
  the new test files.

The development-scope Phase 6 work can be considered closed on this evidence. Production activation and Phase 7 remain
separately blocked.

## 2. Requirement matrix

| Requirement | Evidence | Result |
| --- | --- | --- |
| A. Organization-state policy, no mutation/no audit on rejects, audit actor/target on successes | `TestManagementArchivedPolicyClosure`, `TestManagementSuspendedPolicyClosure` | **PASS** |
| B. Both-order concurrency for create/reactivate/deactivate versus soft delete; paused/deleted; `FOR KEY SHARE`; snapshot preservation | `TestManagementSubscriptionVersusSoftDelete`, `TestManagementPausedAndDeletedSubscriptionPolicy`, `TestManagementDeliveryKeyShareCompatibility`, `TestManagementSoftDeletePreservesDeliverySnapshot`, `TestManagementSubscriptionLockSerialization` | **PASS** |
| C. Bounded deadlock/lock-order stress | `TestManagementLockOrderStress` (4 workers x 4 iterations) | **PASS** |
| D. Endpoint creation versus suspended/archived transition in both orders | `TestManagementEndpointCreationVersusOrganizationTransition` | **PASS** |
| E. Cap-1 concurrent creation | `TestManagementConcurrentCreationAtCapMinusOne` | **PASS** |
| F. `Cache-Control: no-store` for create/rotate and error paths; no secret/provider leakage | `TestManagementSecretResponsesAreUncacheable` | **PASS** |
| G. CLI/accesskeys: raw key once, prefix+PHC only, list never prints token/verifier, revoke policy/tenant checks, expiry, no secret in errors, audit | `TestAPIKeyCLIEndToEnd`, `TestAPIKeyCLIOrganizationPolicyAndOwnership`, accesskeys/CLI unit tests | **PASS (G-1 fixed at source/unit level 2026-11-25; CLI integration NOT RERUN; §13)** |
| Safe local verification with observed exit codes | Section 8 | **PASS** |
| Git/database/environment preservation | Section 9 | **PASS** |
| Documentation reconciliation | Section 7 | **PASS** |
| Hosted CI | No push/commit | **NOT VERIFIED** |
| Production KMS/Vault and readiness | No provider or production deployment | **NOT IMPLEMENTED / NOT READY** |
| Phase 7 ingestion/fan-out/idempotency/event API | Not started | **NOT STARTED** |

## 3. New tests and exact observed results

### 3.1 Policy state (group A) — `test/integration/management_policy_state_integration_test.go`

| Test | Observed result |
| --- | --- |
| `TestManagementArchivedPolicyClosure` | PASS (0.34s). Archived subscription deactivation and re-create/reactivate return 403 with unchanged rows/audits; soft-delete 403 with `deleted_at` still null and no audit; create 403 with zero rows/audit; `access.Create`/`access.Revoke` reject archived without mutation/audit; GET still 200; the pre-existing active subscription is preserved. |
| `TestManagementSuspendedPolicyClosure` | PASS (0.33s). Suspended deactivation 200 with `subscription.deactivated` actor `api_key`/key ID, target subscription, metadata `event_type`; suspended API-key revocation succeeds, sets `revoked_at`, makes the old token 401, and writes exactly one `apikey.revoked` service-actor audit; suspended soft-delete 204 sets `deleted_at`, leaves the stored secret envelope byte-identical, preserves one active subscription row, writes one `endpoint.deleted` audit with actor/target; GET is 404; a later subscription create is denied by organization state (403 `organization_suspended`, no mutation/audit). |
| `TestManagementSecretResponsesAreUncacheable` | PASS (0.23s). Success subtest: create 201 with `Cache-Control: no-store`, one-time `whsec_` + 44-character secret, Location-derived ID; rotate 200 with `no-store` and a fresh different secret. Error subtest: stale `If-Match` 412 `no-store` with no leak; armed provider failure 500 `internal_error` `no-store`, no leak, stored secret unchanged, zero audits. |

### 3.2 Concurrency and lock order (groups B–E) — `test/integration/management_concurrency_integration_test.go`

| Test | Observed result |
| --- | --- |
| `TestManagementSubscriptionVersusSoftDelete` | PASS (1.16s). Delete-first: create/reactivate/deactivate requests queue, complete after commit with 404, leave no new row/activation and no audit. Subscription-first: the DELETE waits, completes 204, the created/reactivated/deactivated state is preserved, later create is 404, and exactly the expected audits exist. |
| `TestManagementSubscriptionLockSerialization` | PASS (0.14s). Two concurrent creates each wait on one lock level (endpoint and organization), both complete 201 after release, two subscriptions and two `subscription.created` audits. |
| `TestManagementPausedAndDeletedSubscriptionPolicy` | PASS (0.24s). Paused endpoint accepts create 201 and deactivate 200 with audits; after soft delete, both return 404 and rows/audits are unchanged. |
| `TestManagementDeliveryKeyShareCompatibility` | PASS (0.14s). A held organization+endpoint `FOR KEY SHARE` transaction does not block subscription create 201 or soft delete 204; the holder remains usable. |
| `TestManagementSoftDeletePreservesDeliverySnapshot` | PASS (0.12s). `to_jsonb(deliveries)` is byte-identical before and after soft delete, including URL, headers and secret envelope. |
| `TestManagementEndpointCreationVersusOrganizationTransition` | PASS (0.32s). Status-first (suspended and archived): POST queues on the organization lock, then returns 403 `organization_<status>` with zero endpoints and zero `endpoint.created` audits. Creation-first: an endpoint inserted under the organization lock makes a `FOR NO KEY UPDATE NOWAIT` transition probe fail with `55P03`; after commit the endpoint exists live, the later archived POST is 403, and no second endpoint is inserted. |
| `TestManagementConcurrentCreationAtCapMinusOne` | PASS (0.11s). Four simultaneous creates produce exactly one 201 and three 409; final rows total/active = 1/1; one `endpoint.created` audit. |
| `TestManagementLockOrderStress` | PASS (0.76s). 16 create/deactivate/PATCH cycles under four workers complete without deadlock or failure; 16 rows, zero active, 16 each of created/deactivated/updated audits, no duplicate event types; post-delete creates are 404 with unchanged rows/audits. |

### 3.3 CLI and accesskeys (group G)

| Test | Observed result |
| --- | --- |
| `TestAPIKeyCLIEndToEnd` (integration, real PostgreSQL) | PASS. Raw token appears exactly once in `create` output, matches the stored prefix, scopes are canonical and sorted, expiry is the fixed RFC3339 value, the stored hash is PHC, the row JSON contains no token, `list` prints name/prefix but no token, PHC string or raw key, `apikey.created` audit is service-actor with metadata, revoke output is exact, `revoked_at` persists, one `apikey.revoked` audit is written, and a repeat revoke adds no audit. |
| `TestAPIKeyCLIOrganizationPolicyAndOwnership` (integration) | PASS. Suspended revoke succeeds; suspended create fails with an error containing "suspended" and empty stdout; cross-tenant revoke is "not found" with no mutation; archived revoke fails with "archived" and no mutation/audit; malformed IDs are rejected without leaks. |
| `accesskeys` unit tests | PASS. Name validation (Unicode, length, controls, invalid UTF-8), dependency guards, validation errors carry no key material, malformed revoke rejected before storage. |
| `cmd/apikey` unit tests | PASS. Scope splitting, time formatting (`2030-01-02T03:04:05+03:00` → `2030-01-02T00:04:05Z`, nil → `never`), and usage rejection before configuration. |

### 3.4 First-run failures and their resolution

The first execution of the new tests produced three **test-only** problems, all corrected without touching application
code:

1. `waitForUngrantedLock` inspected `pg_locks` relation locks, but PostgreSQL row-lock waiters wait on the holder's
   transaction ID. The helper now observes blocked statements in `pg_stat_activity`
   (`wait_event_type = 'Lock'` and the locked relation named in the query).
2. The cap test sorted statuses and compared the first element to 409; after sorting, the single 201 is first. The
   assertion now checks one 201 followed by 409s.
3. The suspended test expected 404 for subscription creation against a deleted endpoint, but suspended policy rejects
   subscription creation before endpoint lookup. The test now expects 403 `organization_suspended` and additionally
   proves no row or audit changed.

No application defect was exposed. The final focused run passed 11/11 top-level management tests.

## 4. Concurrency methodology

Concurrency is proven deterministically, without sleeps:

- A holder transaction takes explicit `FOR NO KEY UPDATE` locks through the production lock helpers and is released
  only when the test commits.
- Requests run asynchronously; a channel select proves each request is still pending while the holder is active.
- `waitForUngrantedLock` polls PostgreSQL's own wait state until the expected backend is blocked on the expected
  relation, instead of assuming a delay.
- The creation-versus-transition race uses a `SELECT … FOR NO KEY UPDATE NOWAIT` probe and asserts SQLSTATE `55P03`
  while the creation transaction holds the organization lock; the endpoint row is then observed after commit.
- The stress test bounds work (4 workers x 4 iterations) and asserts final rows, audit counts and duplicate
  `event_type` absence.

This demonstrates the approved order organizations → endpoints → endpoint_subscriptions under real database locking,
including compatibility with delivery `FOR KEY SHARE` and with immutable delivery snapshots.

## 5. Policy results

- **Archived:** every tenant management write is denied with `403 organization_archived`, including endpoint
  create/patch/soft-delete, subscription create/reactivate/**deactivate**, secret rotation, and API-key
  create/revoke. Reads remain allowed. Rejected requests produce no row change and no audit.
- **Suspended:** allowed — reads, rotation, name/description updates, pausing, **endpoint soft-delete**, subscription
  deactivation, **API-key revocation**. Denied — endpoint creation, URL/header changes, activation, subscription
  create/reactivate, API-key creation. Successful operations write the expected audit action with the API-key actor
  and target; rejected operations write none.
- Soft delete never hard-deletes, never clears the secret envelope, never mutates subscription rows, and never changes
  an accepted delivery snapshot. The FK remains `ON DELETE RESTRICT` for deliveries.

## 6. Cache-Control and CLI results

- Create and rotate responses carry `Cache-Control: no-store`; so do 412 and 500 error responses. No response body
  contains `whsec_`, `hfse1.`, keyring paths, provider text, argon2 or salt material.
- A provider failure leaves the stored secret unchanged and writes no audit.
- Raw API keys appear only in the explicit create output. Only the 8-character prefix and the Argon2id PHC verifier are
  persisted. `list` prints a metadata table only. Errors never contain key material, and audit rows identify
  `apikey.created`/`apikey.revoked` with service actor and key target.
- **Finding G-1:** `accesskeys.List` returns the verifier in `apikey.Record.SecretHash` to callers. The CLI does not
  print it. This requires a separate application change and is reported, not fixed. **Remediated 2026-11-25 — see
  §13.**

## 7. Documentation corrections

Updated with dated reconciliation, preserving historical text:

- D6/U25 now reports three separate statuses everywhere: architecture **OWNER APPROVED** (D6-A…D6-G, U25-A…U25-D,
  2026-11-24); development implementation **IMPLEMENTED**; production KMS/Vault custody and readiness **NOT
  IMPLEMENTED / NOT READY**. Absent production providers no longer cause approved rows to be labelled NOT APPROVED.
- Archived policy corrected to deny subscription deactivation (and all other tenant writes).
- Suspended policy now records the approved endpoint soft-delete and API-key revocation.
- Scope documentation corrected: `endpoints:write` does not grant rotation; `endpoints:secrets:rotate` grants rotation
  only.
- Management integration test count corrected from "eight" to twelve (23 with Phase 6.1); lint arithmetic stated as
  13 pre-existing complexity + 1 `PatchEndpoint` + 3 `gosec` G115 = 17; the earlier "no skips" claim corrected to the
  two observed Windows-only skips.
- Historical text is preserved: pre-approval "NOT APPROVED" blocks in dated revision notes remain but are marked
  superseded; the current-state statements in `overview.md` §5/§8 and the decision register, `database-schema.md`
  §4.5/§6/§9, `runtime-readiness-decisions.md` §2.3/§13, the Bundle 1 closure body and the ERD comment now reflect
  the three statuses. The standalone cross-document reconciliation report carries a status addendum as the
  pre-approval record.
- Files touched: `docs/architecture/d6-u25-decision-proposal.md`,
  `docs/architecture/runtime-readiness-decisions.md`, `docs/architecture/overview.md`,
  `docs/architecture/database-schema.md`, `docs/architecture/erd.mmd`,
  `docs/operations/configuration.md`, `docs/operations/runbook.md`,
  `docs/operations/phase6-implementation-report.md`,
  `docs/operations/d6-u25-implementation-readiness-report.md`,
  `docs/operations/d6-u25-cross-document-reconciliation-report.md`,
  `docs/operations/bundle1-final-closure-report.md`, and `README.md`.

## 8. Commands, counts, exit codes and skipped tests

All commands were executed; results below are observed, not inferred. Environment variables were loaded from `.env`
into the test process without printing values.

| Command | Exit | Observed result |
| --- | --- | --- |
| `gofmt -l .` | 0 | empty output |
| `go vet ./...` | 0 | pass |
| `go vet -tags=integration ./...` | 0 | pass |
| `go build ./...` | 0 | pass |
| `go test ./... -count=1` | 0 | 25 packages `ok`, 4 `no test files`, 0 failures |
| `go test -tags=integration ./... -count=1 -p 1 -v` | 0 | 28 packages `ok`, 2 `no test files`, 0 FAIL, 2 SKIP, 1592 `=== RUN` entries |
| `go test -tags=integration ./test/integration -run '<11 new tests>' -count=1 -p 1 -v` | 0 | 11/11 top-level PASS (runs 4.0s) |
| `go test -tags=integration ./cmd/apikey -count=1 -v` | 0 | 2 integration tests + 3 unit tests PASS (0.385s) |
| `go test ./internal/app/accesskeys ./cmd/apikey -run '<new unit tests>' -count=1` | 0 | both packages `ok` |
| `docker run … golang:1.27.4 go test -race ./... -count=1` | 0 | 27 packages `ok`, 2 `no test files`, 0 FAIL, no data-race report |
| `go mod verify` | 0 | all modules verified |
| `go mod tidy -diff` | 0 | empty |
| `git diff --check` | 0 | clean |

Skipped tests (environment-specific, not failures): `TestEnsureRejectsInsecurePermissions` and
`TestNewFileProviderRejectsInsecurePermissions`; both skip because POSIX permission bits are not enforced on Windows.
They execute in the Linux race container and passed there.

Hosted GitHub Actions: **NOT RUN** — no commit or push was authorized. Lint: not rerun in Phase 6.1.

## 9. Git, database and environment preservation

- HEAD `c6db32fd50ee8d7beb3108674b4cf46443d2aba4`; nothing staged; `git diff --cached --stat` empty;
  `git status` still shows only the pre-existing ` M .gitignore` and the pre-existing untracked tree.
- No add, commit, push, amend, rebase, branch, stash, reset, checkout or clean was run.
- Schema: `schema_migrations` = `13|f` (version 13, not dirty). No migration row or file changed.
- Containers unchanged: `hookflow-postgres-dev` created 2026-11-23T12:47:45Z, healthy;
  `hookflow-rabbitmq-dev` created 2026-11-24T14:29:08Z, healthy.
- Volumes unchanged: `hookflow-postgres-data` created 2026-11-23T12:39:42Z;
  `hookflow-rabbitmq-data` created 2026-11-24T14:29:10Z.
- `.env` mtime 2026-11-24 17:57:03 and `compose.yaml` mtime 2026-11-24 02:03:38 are unchanged from the Phase 6
  baseline; keyring contents were not read or modified.
- Integration fixtures create isolated organizations and clean up their own rows; no queue was reset, no table
  truncated, no database dropped, and no container/volume replaced.

## 10. Remaining limitations

1. Hosted CI is NOT VERIFIED; no workflow run exists.
2. Production KMS/Vault custody, production activation, HA, SSRF/egress controls and production observability remain
   NOT IMPLEMENTED / NOT READY.
3. Finding G-1: **resolved 2026-11-25**; verifier material no longer crosses `accesskeys.List`, `Create` result, or
   `Revoke` result boundaries (§13).
4. Lint was not rerun after adding test files; the 17 advisory findings remain the production-code baseline.
5. Two integration tests skip on Windows by design; the Linux race run covers them.
6. The race run uses the established Linux container method because the Windows host lacks CGO.

## 11. Closure recommendation

Close the development-scope Phase 6 work on this evidence. All critical policy, lock-order, concurrency,
tenant-isolation and preservation requirements are PASS with observed command results. Do not treat this closure as
production readiness or as authorization for Phase 7.

## 12. Phase 7 and production

Phase 7 (event ingestion, idempotency implementation, fan-out, event API) was **not started and is not authorized**.
Production KMS/Vault implementation, production activation and deployment remain **not authorized, not implemented
and not ready**. No production system was contacted, and no production claim is made.

## 13. G-1 remediation (2026-11-25)

**Status: FIXED at the source and boundary-test level; F-3/F-4/F-5 follow-up completed; CLI end-to-end regression
NOT RERUN.** This section is a dated addendum under separate Owner authorization; the Phase 6.1 finding text above
and the §8 evidence from 2026-11-24 are retained as the historical record.

### 13.1 Change

The verifier boundary is now explicit instead of relying on callers not to inspect `SecretHash`:

- `internal/modules/apikey/store.go` adds `apikey.Metadata`, a separate struct (it does not embed `Record`) with
  `ID`, `OrganizationID`, `Name`, `KeyPrefix`, `HashAlgorithm`, `Scopes`, `LastUsedAt`, `ExpiresAt`, `RevokedAt`
  and `CreatedAt`. It has no field for `SecretHash`, a raw token, salt, or a verifier.
- A separate `metadataColumns` SQL projection omits `k.secret_hash`. `List` selects that projection and scans only
  `Metadata`; the verifier never enters the list result path. The projection cannot include the verifier and then
  zero it, because the verifier is never selected.
- `Revoke` and its internal `get` fallback use `metadataColumns` in both the `UPDATE ... RETURNING` and the fallback
  `SELECT`, and return `Metadata`. Idempotent repeat revocation, `pgx.ErrNoRows`/`ErrNotFound` mapping, organization
  locking, archived/suspended policy, audit insertion and transaction boundaries are unchanged.
- `internal/app/accesskeys/accesskeys.go` now returns:
  - `Create(ctx, ...) (string, apikey.Metadata, error)` — the raw token is still returned only after a successful
    commit and exactly once; failures return an empty token and zero `Metadata`. The internal `Record` is still
    built with the Argon2id verifier and passed to `Insert`; only the result crossing the service boundary changes.
  - `List(ctx, ...) ([]apikey.Metadata, error)`.
  - `Revoke(ctx, ...) (apikey.Metadata, bool, error)`.
- `Record`, `AuthRecord` and `FindForAuth` are unchanged. `Insert` was extended in the F-5 follow-up to return the
  database-generated `created_at` (see §13.5); it still persists the verifier internally and does not return it.
  `FindForAuth` still selects `secret_hash` and returns the verifier-bearing `AuthRecord`, so
  `Authenticator.Authenticate` still verifies keys with the same PHC parsing, Argon2id parameters, concurrency
  limit, dummy verification and failure behavior.
- `cmd/apikey/main.go` was not edited. Its create output, seven-column list output, revoke output and one-time raw
  token printing are byte-for-byte unchanged; it simply receives the metadata-only result type.
- No schema, migration, constraint, persisted data, `.env`, keyring, Compose, container, volume, audit, scope,
  authorization or HTTP representation changed. `SecretHash` is still persisted and read by the authentication path.

### 13.2 Tests

- New `internal/modules/apikey/store_boundary_test.go` covers: `Metadata` has no authentication-material field and
  does not embed `Record`; `Record.Metadata()` does not render the verifier; `List` scans metadata and returns
  ordering, revoked state and organization-scoped rows with no `secret_hash` in its SQL; `Revoke` success,
  idempotent already-revoked and not-found paths use metadata-only projections; `FindForAuth` still selects and
  returns `SecretHash`.
- New `internal/app/accesskeys/accesskeys_test.go` coverage asserts validation failures return an empty token and zero
  `Metadata` (or `nil` list) before storage.
- Existing management authentication tests pass unchanged, proving `FindForAuth` still feeds verifier verification.
- Existing CLI integration assertions (raw key once, no verifier in list output or errors, stored PHC verifier) were
  retained without weakening. The integration binary compiles (`go vet -tags=integration ./...`), but the CLI
  integration tests were **NOT RUN** in this remediation because the development PostgreSQL at `127.0.0.1:5433` was
  not reachable (`connection refused`). No container, volume, database or queue was started, recreated or reset.
  The end-to-end CLI behavior is therefore compile-verified here and remains covered by the existing tests for the
  next run with the development database available; it is not claimed as re-executed.

### 13.3 Observed validation

| Command | Exit | Observed result |
| --- | --- | --- |
| `gofmt -l <four changed Go files>` | 0 | empty output |
| `go vet ./...` | 0 | pass |
| `go vet -tags=integration ./...` | 0 | pass (integration tests compile; not executed) |
| `go build ./...` | 0 | pass |
| `go test ./... -count=1` | 0 | 27 test-bearing packages `ok`, 2 `no test files`, 0 failures |
| `go test ./internal/modules/apikey ./internal/app/accesskeys ./internal/app/management ./cmd/apikey -count=1` | 0 | all four packages `ok` |
| `go test -tags=integration ./cmd/apikey -count=1 -v` | 1 | NOT RUN against a database: both integration tests failed at connect to `127.0.0.1:5433`; 3 unit tests passed |
| `go mod verify` | 0 | all modules verified |
| `go mod tidy -diff` | 0 | empty |
| `git diff --check` | 0 | clean |

### 13.4 Boundary conclusion

`Insert` may receive verifier material. `FindForAuth` may retrieve verifier material for authentication. `List`,
the `Create` result and the `Revoke` result are metadata-only. G-1 is fixed at the verified service boundary; the
remaining limitations are the pre-existing hosted-CI and production-readiness items, plus the unrerun CLI
integration test recorded above. This addendum does not close Phase 6, authorize Phase 7, or assert production
readiness.

### 13.5 F-3/F-4/F-5 follow-up (2026-11-25)

- **F-4 FIXED.** The `accesskeys.Create` comment now states that the verifier may pass through the persistence and
  authentication paths but must never appear in an operator-facing service result; operator-facing methods return
  metadata-only values.
- **F-5 FIXED at source and unit-test level.** `apikey.Insert` now uses `RETURNING id, created_at` and returns the
  database-generated `time.Time`; `accesskeys.Create` copies that value into the returned metadata after the
  transaction commits. The existing `api_keys.created_at DEFAULT now()` schema and timestamp semantics are
  unchanged; no migration or application-generated substitute timestamp was introduced. `Insert` still persists
  the verifier but does not return it. The actual PostgreSQL runtime value was **NOT RUNTIME VERIFIED** after this
  follow-up because the integration database was unavailable.
- **F-3 FIXED at source and unit-test level.** New `internal/app/accesskeys/service_boundary_test.go` uses a
  deterministic fake `Database`/`pgx.Tx` to cover: successful `Create` returns the raw token and persisted
  `CreatedAt`; the verifier reaches `Insert` but not the returned metadata; begin, insert, audit, commit and
  archived failures return an empty token and zero metadata; post-storage audit/commit failures in `Revoke` return
  zero metadata with `changed=false`. New `TestInsertReturnsDatabaseCreatedAt` verifies the `RETURNING id,
  created_at` projection. A focused integration test,
  `TestManagementAPIKeyCreateReturnsPersistedCreatedAt`, was added to compare `Create` metadata with the
  `api_keys.created_at` row; it compiles but was **NOT RUN** because no safe integration database was available.
  The unit tests above are source-level tests, not real PostgreSQL evidence.
- Existing tests for metadata shape, List/Revoke projections, `FindForAuth`, authentication, organization scope,
  ordering, idempotency, policy and audit were retained.

| Command | Exit | Observed result |
| --- | --- | --- |
| `gofmt -l <five changed Go files>` | 0 | empty output |
| `go test ./internal/modules/apikey ./internal/app/accesskeys -count=1` | 0 | both packages `ok` |
| `go test ./... -count=1` | 0 | 27 test-bearing packages `ok`, 2 `no test files`, 0 failures |
| `go vet ./...` | 0 | pass |
| `go vet -tags=integration ./...` | 0 | pass (integration tests compile; not executed) |
| `go build ./...` | 0 | pass |
| `go mod verify` | 0 | all modules verified |
| `go mod tidy -diff` | 0 | empty |
| `git diff --check` | 0 | clean |
| `go test -race ./internal/modules/apikey ./internal/app/accesskeys -count=1` | 2 | **NOT RUN**: `-race requires cgo`; local `CGO_ENABLED=0` |
| CLI and management PostgreSQL integration tests | — | **NOT RUN**: `127.0.0.1:5433` refused connections and the Docker daemon was unavailable; no infrastructure was started or recreated |

The F-3/F-4/F-5 follow-up does not change the conclusion that real PostgreSQL and CLI end-to-end verification
remain outstanding for G-1. Phase 6 is not declared closed, Phase 7 remains not started, and production readiness
is not asserted.
