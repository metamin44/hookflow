# Phase 6 implementation report — Endpoint Management API

Date: 2026-11-24
Authorization: Owner authorization GRANTED (2026-11-24), Phase 6 Authorized Implementation.
Scope executed: management API, API-key authentication, endpoint lifecycle with Contract A ETags, secret rotation, subscriptions, audit, operator CLI, tests, and the documentation needed to describe them.

## 1. Executive verdict

**PASS WITH FINDINGS.**

Phase 6 is implemented and locally verified. The authenticated management API, API-key
authentication (Argon2id PHC verifiers, scoped bearer tokens), endpoint create/list/get/
patch/delete under Contract A ETags, the U37/U38 active-endpoint ceiling, suspended and
archived organization policy, secret rotation behind the existing KEK provider boundary,
subscription management, audit writes, and the `apikey` operator CLI all exist in the
working tree and are covered by unit and PostgreSQL/RabbitMQ integration tests.

No schema change, migration, Compose change, container recreation, volume replacement,
or Git mutation was performed. No pre-existing modification or untracked file was
removed or overwritten.

Findings and limits (detail in section 15):

- Hosted CI was not executed; no commit or push was authorized. This report does not
  claim hosted CI passed.
- Production KEK/KMS/Vault providers remain absent. `HOOKFLOW_DEPLOYMENT_MODE=production`
  fails closed at startup; only the development file provider exists.
- The D6/U25 architecture (D6-A…D6-G, U25-A…U25-D) is **OWNER APPROVED** (2026-11-24)
  and the development runtime is **IMPLEMENTED**. This report records no new approval.
  Production KMS/Vault custody and production readiness remain **NOT IMPLEMENTED / NOT READY**;
  the absent production providers do not un-approve the architecture. Phase 6 used the
  development runtime already present in the tree.
- URL validation is syntax-only (HTTPS, no userinfo, no fragment). SSRF defenses
  (U13) remain deferred.
- `golangci-lint` reports 17 advisory findings: 13 pre-existing complexity findings,
  one new `PatchEndpoint` cognitive-complexity finding, and 3 pre-existing `gosec`
  integer-conversion findings. Lint cleanliness is not claimed.
- Phase 7 (event ingestion, idempotency, fan-out, event API) and production
  deployment/KMS/Vault work were not started.

## 2. Baseline and final Git status

Baseline captured before Phase 6 implementation (after Phase 5) and final status are
identical:

```
 M .gitignore
?? .env.example
?? .gitattributes
?? .github/
?? .golangci.yml
?? README.md
?? cmd/
?? compose.yaml
?? db/
?? deployments/
?? docs/
?? go.mod
?? go.sum
?? internal/
?? scripts/
?? test/
```

- The ` M .gitignore` modification and every untracked path pre-existed Phase 6 and were
  preserved unchanged. Nothing was reset, checked out, cleaned, stashed, or overwritten.
- `git diff --cached --stat` is empty: nothing is staged.
- `git diff --stat` shows only `.gitignore` (3 insertions, 1 deletion), the pre-existing
  change.
- HEAD remains the pre-existing initial commit `c6db32fd50ee8d7beb3108674b4cf46443d2aba4`
  (`2026-11-22 01:06:04 +0330`). No commit, amend, rebase, branch, or push was created.
- `git diff --check` exits 0 (no whitespace errors).
- `.env` was not touched during Phase 6 (last modification 2026-11-24 17:57, before the
  Phase 6 brief at 19:41). No secret value was read, printed, or recorded.

## 3. Files created or modified, by workstream

Attribution is by the Phase 6 implementation window (files modified after the Phase 6
brief). Everything listed is untracked in Git, consistent with the pre-existing
repository state.

**A. Domain modules**

- `internal/modules/apikey/`: `token.go`, `phc.go`, `scopes.go`, `store.go` and tests
  `token_test.go`, `phc_test.go`, `scopes_test.go`.
- `internal/modules/endpoint/`: `endpoint.go`, `validate.go`, `etag.go`, `store.go` and
  tests `validate_test.go`, `etag_test.go`.
- `internal/modules/subscription/`: `subscription.go`, `subscription_test.go`.
- `internal/modules/organization/organization.go` (approved lock helper, status reads).
- `internal/modules/audit/audit.go` (target types used by the new audit writes).

**B. Management application layer**

- `internal/app/management/`: `auth.go`, `body.go`, `doc.go`, `errors.go`, `http.go`,
  `ifmatch.go`, `secrets.go`, `service.go` plus `auth_test.go`, `body_test.go`,
  `ifmatch_test.go`, `service_test.go`.

**C. CLI, wiring, configuration**

- `cmd/apikey/main.go` (new operator CLI: create, list, revoke).
- `cmd/api/lifecycle.go` and `cmd/api/lifecycle_test.go` (mount `/v1/`, load limits).
- `internal/app/accesskeys/accesskeys.go` (CLI storage path wiring).
- `internal/platform/config/endpoint.go` and `endpoint_test.go`
  (`HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG`, default 100).

**D. Tests**

- `test/integration/management_api_integration_test.go` (unit and real PostgreSQL
  integration coverage for authentication, lifecycle, PATCH contract, ceiling,
  organization policy, rotation, subscriptions, tenant isolation, lock compatibility,
  routing and limits).
- All package-level tests listed above.

**E. Dependencies**

- `go.mod`, `go.sum`: `golang.org/x/crypto` promoted to a direct dependency for
  Argon2id (`x/sync`, `x/sys`, `x/text` remain indirect). No other dependency change.

**F. Documentation and configuration templates**

- `README.md`, `.env.example`.
- `docs/architecture/overview.md`, `database-schema.md`, `erd.mmd`,
  `runtime-readiness-decisions.md`, `d6-u25-decision-proposal.md`, and new
  `p6-od-etag-contract-a.md`.
- `docs/operations/configuration.md`, `docs/operations/runbook.md`.
- This report.

No change was made to `db/migrations/`, `compose.yaml`, `.github/`, deployment
manifests, or the development containers/volumes.

## 4. API routes and exact public fields

All routes are mounted under `/v1/` by `cmd/api`. Authentication is
`Authorization: Bearer <api key>`; responses are JSON. Every path organization must equal
the authenticated tenant, otherwise the response is `404` without existence disclosure.
Errors use `application/problem+json` (RFC 9457 shape) with `type: "about:blank"`,
`title`, `status`, `detail`, `code`, and an optional `field`.

| Method | Path | Success | Notes |
| --- | --- | --- | --- |
| POST | `/v1/organizations/{organization_id}/endpoints` | `201` | Body: `name`, `description`, `url`, `http_headers`. Response adds one-time `secret`. `Location` and `ETag` set. Requires `endpoints:write`. |
| GET | `/v1/organizations/{organization_id}/endpoints` | `200` | Response `{"endpoints":[...]}`. Requires `endpoints:read` or `endpoints:write`. |
| GET | `/v1/organizations/{organization_id}/endpoints/{endpoint_id}` | `200` | Endpoint document plus `ETag`. |
| PATCH | `/v1/organizations/{organization_id}/endpoints/{endpoint_id}` | `200` | Body: `name`, `description` (nullable), `url`, `http_headers`, `status`. Requires `If-Match` and `endpoints:write`. |
| DELETE | `/v1/organizations/{organization_id}/endpoints/{endpoint_id}` | `204` | Soft delete. No `If-Match` evaluation. Requires `endpoints:write`. |
| POST | `/v1/organizations/{organization_id}/endpoints/{endpoint_id}/secret/rotate` | `200` | Response `{"secret":"whsec_..."}`; new `ETag` returned. Requires `If-Match` and `endpoints:secrets:rotate`. |
| GET | `/v1/organizations/{organization_id}/endpoints/{endpoint_id}/subscriptions` | `200` | Response `{"subscriptions":[...]}`. |
| POST | `/v1/organizations/{organization_id}/endpoints/{endpoint_id}/subscriptions` | `201` created / `200` reactivated / `409` if already active | Body: `{"event_type":"..."}`. `Location` on creation. |
| DELETE | `/v1/organizations/{organization_id}/endpoints/{endpoint_id}/subscriptions/{subscription_id}` | `200` | Deactivates (soft), returns the record with `is_active:false`. No `If-Match`. |

Endpoint document fields: `id`, `name`, `description` (nullable), `url`, `status`
(`active`/`paused`), `signing_scheme` (always `hmac_sha256` in V1), `http_headers`
(object of string values). Subscription document fields: `id`, `endpoint_id`,
`event_type`, `is_active`.

Rejected create/PATCH keys, including reserved ones, produce `400`: `id`,
`organization_id`, `secret`, `secret_encrypted`, `signing_scheme`, `timeout_ms`,
`max_attempts`, `deleted_at`, `created_at`, `updated_at`, `last_success_at`,
`last_failure_at`. `timeout_ms`/`max_attempts` remain reserved per OD-3.

Other error statuses of note: `401` for a missing/invalid/malformed key, `403`
`insufficient_scope`, `403` `organization_suspended`/`organization_archived`, `404` for
unknown or cross-tenant resources, `409` `active_endpoint_cap`/`endpoint_name_conflict`,
`413` `payload_too_large`, `400` for a body that is not a JSON object, `428` missing `If-Match`,
`412` stale/weak `If-Match` (with the current `ETag`), `400` malformed `If-Match`, and
`405` with `Allow` on known paths.

## 5. API-key format, PHC parameters, scopes, CLI

Format: 32 random bytes rendered as 43-character unpadded base64url
(`A-Z a-z 0-9 - _`). The first 8 characters are stored as the non-secret lookup prefix;
the raw token is shown once in the `apikey create` output and never stored or logged.
Verification uses Argon2id verifiers in PHC format: `$argon2id$v=19$m=19456,t=2,p=1$...`
with a 16-byte salt and 32-byte key. Verification bounds are `m` 19456–32768, `t` 2–4,
`p` 1–2, concurrency capped at 4. Unknown prefixes run a dummy verification so timing does
not disclose key existence. Verifier and error values never include token, salt, or hash
bytes.

Scopes: `events:write` (default for new keys), `endpoints:read`, `endpoints:write`,
`endpoints:secrets:rotate`. `endpoints:write` implies `endpoints:read` but does not grant
rotation. `endpoints:secrets:rotate` is independent and grants rotation only. Suspended
organizations remain readable and rotatable and may soft-delete endpoints and revoke API
keys; archived organizations reject all tenant management writes, including subscription
deactivation, endpoint soft-delete and API-key creation/revocation. API-key revocation is
immediate.

CLI (`go run ./cmd/apikey`, or the built binary):

```
apikey create  -organization <uuid> -name <name> [-scopes <csv>] [-expires <duration>]
apikey list    -organization <uuid>
apikey revoke  -organization <uuid> -id <uuid>
```

`create` prints the metadata and the raw `api_key` once. `list` never prints tokens.
Audit actions written by the CLI are `apikey.created` and `apikey.revoked` with
`actor_type = service` and target type `api_key`. Management API writes use
`actor_type = api_key`.

## 6. Endpoint validation, cap, suspended/archived policy

Validation occurs before any lock:

- `name`: valid UTF-8, 1–120 characters, no control characters.
- `description`: valid UTF-8, at most 500 characters, no control characters; nullable on
  PATCH.
- `url`: 1–2048 bytes, absolute `https` URL, non-empty host, no userinfo, no fragment,
  no whitespace/control characters; no DNS resolution or SSRF claim.
- `http_headers`: at most 32 headers; RFC 7230 token names; values at most 4096 bytes; no
  control characters; names lowercased, duplicates rejected case-insensitively, total
  length at most 16 KiB; canonical deterministic form stored.
- `status`: `active` or `paused`.
- `event_type`: exact lowercase routing key, 1–160 characters, no wildcard syntax.

Ceiling (U37/U38): `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG` is read by `cmd/api`, defaults
to 100, and must be a positive integer when set. The count is taken inside the same
transaction that holds the organization `FOR NO KEY UPDATE` lock, immediately before the
write, so concurrent creations or activations cannot exceed the ceiling. Reaching the
ceiling returns `409 active_endpoint_cap`. The count includes only live (`deleted_at IS
NULL`) active endpoints. Create always proposes `status = active`, so create at the
ceiling is rejected.

Organization policy: archived organizations receive `403 organization_archived` for every tenant
management write, including endpoint changes, endpoint soft-delete, subscription
create/reactivate/deactivate, secret rotation, and API-key creation/revocation; reads follow the
existing policy. Suspended organizations can read, rotate secrets, change safe metadata
(`name`, `description`), pause, soft-delete an endpoint, deactivate a subscription and revoke an
API key, but cannot create endpoints, change `url` or `http_headers`, activate an endpoint,
create/reactivate a subscription, or create an API key (`403 organization_suspended`). PATCH
checks field presence under the lock rather than evaluating values, matching the approved
suspended policy. A no-op PATCH is evaluated after the suspended check and is not used to
bypass it.

## 7. PATCH order, no-op semantics, audit mapping

PATCH executes in this fixed order:

1. Decode and validate the request (unknown/forbidden keys, field values, canonical
   headers) — `400` before any lock.
2. Begin a transaction.
3. Lock the organization `FOR NO KEY UPDATE`; archived check.
4. Lock the endpoint `FOR NO KEY UPDATE`.
5. Live check (deleted rows are `404`).
6. Suspended field-presence check.
7. Evaluate `If-Match` against the current Contract A ETag (missing `428`, malformed
   `400`, stale/weak `412` with the current ETag, `*` unconditional).
8. Compute the next representation and changed-field set. If nothing changes, return the
   current representation with no `UPDATE`, no audit row, and no ceiling check.
9. Only when the status transitions to `active`, count active endpoints under the held
   organization lock and enforce the ceiling (`409`).
10. `UPDATE`, write the audit row, commit.

No-op PATCH requires a valid current `If-Match` and returns `200` with the unchanged
document and ETag. Canonical header maps and nullable descriptions are compared in their
stored form, so semantically identical payloads do not create false changes.

Audit mapping: `endpoint.created` (create), `endpoint.updated` (status unchanged),
`endpoint.paused` (new status `paused`), `endpoint.activated` (new status `active`),
`endpoint.deleted`, `endpoint.secret_rotated`, `subscription.created`,
`subscription.reactivated`, `subscription.deactivated`. PATCH audit metadata includes
the sorted `fields` list. Actor is the API-key identity (`actor_type = api_key`) with the
request IP and bounded user agent captured by the request metadata.

DELETE performs tenant and archive checks, soft-deletes the endpoint, and writes
`endpoint.deleted`. It does not evaluate `If-Match` and does not touch subscription rows
or the secret envelope; subscription rows become unreachable with the deleted endpoint.

## 8. ETag: exact canonical fields and encoding

Contract A, implemented in `internal/modules/endpoint/etag.go`:

- Value shape: `hf1-` + unpadded base64url (RawURLEncoding) of
  SHA-256 over a length-prefixed encoding with the domain separator
  `hookflow.etag.endpoint.v1`.
- Canonical inputs, in order: domain; endpoint `id`; `name`; description presence byte
  (`0x00` absent, `0x01` present) and, when present, its bytes; `url`; `status`;
  `signing_scheme`; header count plus header names and values in sorted order; and
  SHA-256 of the stored `secret_encrypted` envelope.
- Excluded: `organization_id`, `created_at`, `updated_at`, `deleted_at`, and the secret
  in plaintext (never available to ETag computation).
- Header form is a quoted strong entity-tag (`"hf1-..."`).
- `If-Match` semantics: absent/empty `428`; malformed syntax `400`; `*` unconditional;
  weak tags never match; a comma-separated list matches when any strong tag equals the
  current value; otherwise `412` with the current ETag returned.
- Rotating the secret changes the envelope and therefore the ETag.
- ETags are returned on create, GET, PATCH, and rotate. DELETE has no validator by
  design.

## 9. Secret creation and rotation, provider-I/O boundary evidence

- Endpoint secret token: `whsec_` + standard base64 of 32 random bytes. It is returned
  once in the create response and once per rotation; it is not persisted in plaintext and
  is never logged.
- Stored form: `hfse1.` envelope produced by the existing `internal/platform/secrets`
  implementation, wrapped by the KEK provider with the approved 64-byte secret AAD and
  51-byte KEK-wrap AAD.
- Create: organization status is checked before provider I/O; the envelope is produced
  before the transaction and the transaction inserts the row and audit record.
- Rotation is two-phase, implemented in `internal/app/management/service.go`:
  1. Read organization status, read the endpoint without a lock, evaluate `If-Match`
     against the current ETag, then generate and wrap the new secret with **no database
     lock held** (provider I/O never blocks a transaction).
  2. Re-open a transaction, lock the organization and endpoint `FOR NO KEY UPDATE`,
     re-check archive/deletion and re-evaluate `If-Match` against the locked row. A stale
     validator returns `412` with the current ETag and the freshly wrapped envelope is
     discarded. Otherwise update the envelope, write `endpoint.secret_rotated`, commit.
- Provider failures are mapped to a static `500` problem document; the internal error is
  logged by type only, never with secret material. A discarded envelope is never
  persisted.
- Evidence: integration test `TestManagementRotationReleasesLocksDuringProviderIO`
  (provider I/O observed to complete while no row lock is held) and
  `TestManagementSecretRotation` (rotation changes the ETag; the old token stops
  matching; stale `If-Match` returns `412` with the current ETag). Unit coverage for
  AAD/envelope behavior remains in `internal/platform/secrets` and
  `internal/platform/kek`.
- Production provider boundary: not implemented. Only the development file KEK provider
  exists; `cmd/api` and `cmd/worker` reject production mode without a production
  provider. No KMS/Vault call was made.

## 10. Subscription lock order and concurrency evidence

Lock order for every management write is organizations → endpoints →
endpoint_subscriptions, all with `FOR NO KEY UPDATE`. Delivery-path foreign-key checks
use `FOR KEY SHARE`, which is compatible with these management locks.

Subscription operations: create requires a live endpoint and organization policy
clearance (creation and reactivation are rejected for archived and suspended organizations;
deactivation is allowed for suspended organizations but denied for archived ones; endpoint
soft-delete is allowed for suspended organizations). An inactive row for the
same `event_type` is reactivated (`200`, `subscription.reactivated`) instead of inserting
a second row, using `INSERT ... ON CONFLICT (endpoint_id, event_type) DO UPDATE ... WHERE
is_active = false` and `xmax = 0` to distinguish insert from reactivation; an already
active duplicate is `409 subscription_exists`. Deactivate locks the subscription row
(`FOR NO KEY UPDATE`), is idempotent for an already inactive row, and returns the record
with `is_active:false`. Delete never touches other subscriptions.

Evidence tests:

- `TestManagementConcurrentSubscriptionCreate` — concurrent create attempts for the same
  `(endpoint, event_type)` converge on one row, with no duplicate or deadlock.
- `TestManagementKeyShareCompatibility` — management `FOR NO KEY UPDATE` locks coexist
  with delivery-path `FOR KEY SHARE` reads.
- `TestManagementEndpointCapAndConcurrentActivation` — concurrent reactivations cannot
  exceed the ceiling.
- `TestManagementTenantIsolation` — cross-tenant paths are `404` and never disclose
  existence.

## 11. Test commands and exact pass/fail counts

Environment: existing development PostgreSQL 18 (`hookflow-postgres-dev`, localhost:5433)
and RabbitMQ 4.4.1 (`hookflow-rabbitmq-dev`, localhost:5673), using the repository's
standard environment variables (values not reproduced). Go 1.27.4 with
`GOTOOLCHAIN=auto`; the local SDK is never pinned.

```
gofmt -l .                                              # empty
go vet ./...                                            # pass
go build ./...                                          # pass
go test ./... -count=1                                  # unit: ok=25 none=4 fail=0
go test -tags=integration ./... -count=1 -p 1           # integration: ok=26 none=4 fail=0
go mod verify                                           # all modules verified
go mod tidy -diff                                       # empty (tidy-clean)
```

The unit suite covers 25 test-bearing packages out of 29 (4 packages have no test files).
The integration suite covers 28 test-bearing packages out of 30, runs serially (`-p 1`),
and now reports **2 skips** (both Windows-only POSIX-permission checks; see §17), correcting
the earlier claim of "no skips". It includes all 12 Phase 6 management integration tests plus
11 Phase 6.1 tests (23 top-level management tests) and the existing delivery, publisher,
recovery, worker, and safety suites against the real PostgreSQL and RabbitMQ development
services. (The earlier "8 management integration tests" count in this report was wrong; the
Phase 6 file contains twelve.)

The full validation suite was rerun after the final code changes (background run
completed with exit code 0): `gofmt` clean, `vet` and `build` pass, unit
`ok=25 none=4 fail=0`, integration `ok=26 none=4 fail=0`, `go mod tidy -diff` empty,
`go mod verify` "all modules verified".

## 12. Race, fuzz, lint, and integration results

- Race: the whole module was run with `-race` in a `golang:1.27.4` Linux container with
  CGO and GCC available (the established repository method; the Windows host has no CGO
  toolchain). Exit code 0, 25 packages `ok`, no data races. The management package
  completed in 2.618s under the race detector.
- Fuzz: `go test -run=^$ -fuzz=FuzzDecode -fuzztime=10s ./internal/platform/secrets`
  passed, executing 551,647 inputs. No crashes, no unexpected envelope acceptance.
- Lint: `golangci-lint v2.15.0` ran as a tool and reported 17 advisory findings:
  13 pre-existing cyclomatic/cognitive-complexity findings, one new `PatchEndpoint`
  cognitive-complexity finding, and 3 pre-existing `gosec` G115 integer-conversion
  findings. The new-code `errcheck`, `errorlint`, `revive`, and `staticcheck` findings
  raised during implementation were fixed; lint cleanliness is not claimed.
- Integration: `TestManagementAuthenticationAndScopes`, `TestManagementEndpointLifecycle`,
  `TestManagementPatchContract`, `TestManagementEndpointCapAndConcurrentActivation`,
  `TestManagementOrganizationPolicy`, `TestManagementSecretRotation`,
  `TestManagementRotationReleasesLocksDuringProviderIO`, `TestManagementSubscriptions`,
  `TestManagementConcurrentSubscriptionCreate`, `TestManagementTenantIsolation`,
  `TestManagementKeyShareCompatibility`, and `TestManagementRoutingAndLimits` all pass.
- `git diff --check` exits 0.
- Hosted GitHub Actions was not executed. The workflow definition was not modified in
  Phase 6; its hosted status remains unverified.

## 13. Schema, migration, database, Compose, and volume preservation

- Schema: `schema_migrations` reports version 13, `dirty = false` on the development
  database. No migration was added or edited; `db/migrations` still contains 13 Up files,
  the newest dated 2026-11-23 15:43, before Phase 6.
- No schema change was needed: the Phase 6 code uses existing tables (`api_keys`,
  `organizations`, `endpoints`, `endpoint_subscriptions`, `audit_logs`) and only sets
  `deleted_at` on soft delete. `last_success_at`/`last_failure_at` remain reserved and
  unwritten.
- `compose.yaml` was not modified (last change 2026-11-24 02:03, before Phase 6).
  `.env` was not modified (17:57, before Phase 6). No keyring content or deployment
  manifest was changed.
- Containers were not recreated or deleted:
  `hookflow-postgres-dev` created 2026-11-23T12:47:45Z, image
  `postgres:18-alpine@sha256:0115267ab5a509022df9319c77be365cef335adb71556c75f60f5360ee247e71`, healthy;
  `hookflow-rabbitmq-dev` created 2026-11-24T14:29:08Z, image
  `rabbitmq:4.4.1-management-alpine@sha256:2b47b1aa05ec7e373950ad2e9200e9f95a9da7039c8d22c19dbf5a59101362c4`, healthy.
- Volumes were preserved: `hookflow-postgres-data` created 2026-11-23T12:39:42Z,
  `hookflow-rabbitmq-data` created 2026-11-24T14:29:10Z. No volume was removed or
  recreated.
- Integration tests ran against this existing environment under the harness's own
  preconditions (the suite skips rather than touch a non-idle queue or a non-isolated
  database). Container and volume creation timestamps are unchanged from before Phase 6,
  proving no container or volume was recreated or replaced. No database drop or volume
  operation was issued during validation.

## 14. Documentation updated

- `README.md`: Phase 6 scope, keyring step, `/v1` management API, API-key/endpoint
  walkthrough, `If-Match` and validator behavior (`DELETE` takes no validator), metrics
  renumbering, and corrected local verification evidence (test counts, race, fuzz,
  `go mod` checks, and the 17-finding lint breakdown).
- `.env.example`: management-API ceiling variable with default and whitespace/unset
  semantics; delivery-security block comment corrected to name `cmd/api` and `cmd/worker`
  as consumers and to state the production fail-closed behavior.
- `docs/architecture/p6-od-etag-contract-a.md` (new): Contract A specification,
  canonical field table, exclusion list, `If-Match` evaluation table, fixed service
  order, side effects including `DELETE` having no validator, rotation behavior,
  verification, and non-goals.
- `docs/architecture/overview.md` (documentation revision 19): Phase 6 management API
  status, package list, evidence table for routes, auth, endpoint contract, ceiling,
  rotation, audit, CLI, verification, and not-verified/absent; Phase 5 dev runtime
  recorded as current state while every D6/U25 row remains NOT APPROVED (superseded by §17.4 and the Phase 6.1
  reconciliation: D6/U25 architecture is Owner-approved and implemented in development; production is not ready).
- `docs/architecture/database-schema.md` (documentation revision 18): no schema change,
  version 13, soft-delete behavior for subscriptions, `FOR NO KEY UPDATE` and lock order,
  reserved timestamp columns, and delivery FK lock compatibility.
- `docs/architecture/erd.mmd`: revision note, lock-order comment on U38, and the
  organization lock anchor.
- `docs/architecture/d6-u25-decision-proposal.md`: appended status note — a later phase
  implemented the development runtime per the recommendations; no row was approved; no
  KMS/Vault provider exists; the earlier "no deployment-mode concept" text remains as the
  pre-implementation record.
- `docs/architecture/runtime-readiness-decisions.md`: addendum recording the dev-mode
  matrix, file KEK, `hfse1.` envelope, Standard Webhooks v1 signing, and the Phase 6 API,
  with Production still NOT READY.
- `docs/operations/configuration.md`: management limits and delivery-security tables,
  loading rules, route summary, management authentication (CLI examples, scope table,
  contract summary with `428`/`412`/`400`/`*`, `DELETE` unconditional, ceiling, audit).
- `docs/operations/runbook.md`: management-API operations (API keys, endpoint writes and
  ETags, ceiling, rotation, audit troubleshooting), renumbered sections, redaction
  guidance, and production blockers including hosted CI, HA, SSRF, observability,
  D6/U25 approval status, and KEK retirement.

Historical findings were not rewritten: earlier reports and the earlier proposal text
remain, with status notes explaining what changed and when.

## 15. Remaining findings, limitations, and deferred work

1. 17 advisory lint findings remain (13 pre-existing complexity, 1 new
   `PatchEndpoint` cognitive complexity, 3 pre-existing `gosec` G115). The new
   `PatchEndpoint` finding documents the mandated fixed-order transaction and was not
   silenced.
2. Hosted CI is unexecuted; no workflow run is claimed. The local suite and the container
   race run are the only executed evidence.
3. Production KMS/Vault providers are absent; production mode fails closed. No
   production deployment, HA topology, external TLS termination, or production
   observability was implemented.
4. D6/U25 architecture is **OWNER APPROVED** and the development implementation is
   **IMPLEMENTED**. The development file provider and keyring constitute a development
   runtime; production KMS/Vault custody and production readiness remain
   **NOT IMPLEMENTED / NOT READY**.
5. SSRF defenses (U13) remain deferred; URL validation is syntax-only and performs no DNS
   or network policy checks.
6. The management API has no rate limiting or dedicated operational metrics beyond the
   existing HTTP scaffolding; observability remains a listed production blocker.
7. `last_success_at`/`last_failure_at` are reserved and never written by current code.
8. `timeout_ms`/`max_attempts` remain reserved per OD-3; submitting them is a `400`.
9. Soft-deleted endpoints keep their subscription rows and secret envelope; they are
   unreachable while deleted. This matches the approved schema semantics and is
   documented rather than deleted.
10. The race run depends on the Linux `golang:1.27.4` container methodology because the
    Windows host lacks CGO; Windows file-ACL behavior remains a documented development
    caveat.
11. The README lint-finding arithmetic was inconsistent and has been corrected to
    13 + 1 + 3 = 17.

## 16. Confirmation that Phase 7 and production implementation were not started

**Phase 7 event ingestion, idempotency implementation, fan-out, and event API.**
No ingestion endpoint, idempotency handler, fan-out worker, or event API route was added.

**Production deployment or production KMS/Vault provider implementation.**
No production provider, cloud KMS call, Vault client, production deployment, or
production configuration was created. Production mode still fails closed.

No database schema change or migration was made. No development database data, PostgreSQL
or RabbitMQ volume, `.env`, keyring content, or Compose infrastructure was changed,
recreated, or deleted. No unrelated module or pre-existing user work was modified. No Git
staging, commit, push, amend, rebase, or branch creation was performed.

## 17. Phase 6.1 verification closure (2026-11-24)

Phase 6.1 added tests and documentation only; application behavior, SQL, migrations, runtime
configuration, `.env`, Compose and Git state were not changed. Verdict for the closure pass:
**PASS WITH FINDINGS** (remaining findings: hosted CI not run, production not ready, and the
in-memory verifier exposure described below, which was fixed at the service boundary on
2026-11-25 under separate authorization; its CLI end-to-end rerun is NOT RUN — see §17.6).

### 17.1 New automated coverage

- `test/integration/management_policy_state_integration_test.go`: archived policy closure
  (deactivation, re-create/reactivate, soft-delete, create, API-key create/revoke all denied
  with no rows/audit and reads still allowed); suspended policy closure (deactivation allowed
  with actor/target audit, API-key revocation effective immediately, endpoint soft-delete 204
  with secret envelope unchanged, one subscription row preserved and active, deleted endpoint
  reads 404); `Cache-Control: no-store` on create/rotate successes and on 412/500 errors,
  with no secret/provider leakage and no mutation on failure.
- `test/integration/management_concurrency_integration_test.go`: deterministic both-order
  subscription-versus-soft-delete for create/reactivate/deactivate; endpoint-lock serialization
  for two concurrent creates; paused/deleted subscription policy; `FOR KEY SHARE` compatibility;
  delivery-snapshot byte-for-byte preservation across soft delete; creation-versus-organization
  transition in both orders (including a `55P03` NOWAIT probe); cap-1 concurrent creation;
  bounded lock-order stress (4 workers x 4 iterations) with audit and duplicate checks.
- `internal/app/accesskeys/accesskeys_test.go` and `cmd/apikey/main_test.go` (unit) plus
  `cmd/apikey/main_integration_test.go` (real PostgreSQL): raw key appears once, only prefix
  and PHC verifier persist, `list` never prints token or verifier, revoke organization/tenant
  checks, expiry metadata, no secret in errors, `apikey.created`/`apikey.revoked` audit
  actor/target.

### 17.2 Executed commands and observed results

Environment: existing development PostgreSQL 18.7 (`hookflow-postgres-dev`) and RabbitMQ 4.4.1
(`hookflow-rabbitmq-dev`); `.env` loaded into the test process without printing values.
Go 1.27.4, `GOTOOLCHAIN=auto`.

```
gofmt -l .                                                   # empty, exit 0
go vet ./...                                                 # exit 0
go vet -tags=integration ./...                               # exit 0
go build ./...                                               # exit 0
go test ./... -count=1                                       # exit 0; 25 ok, 4 no test files
go test -tags=integration ./... -count=1 -p 1 -v             # exit 0; 28 ok, 2 no test files,
                                                             # 0 FAIL, 2 SKIP, 1592 RUN entries
go test -tags=integration ./test/integration -run '<new tests>' -count=1 -p 1 -v
                                                             # exit 0; 11/11 new top-level tests PASS
go test -tags=integration ./cmd/apikey -count=1 -v           # exit 0; 2 integration + unit tests PASS
docker run ... golang:1.27.4 go test -race ./... -count=1    # exit 0; 27 ok, 2 no test files,
                                                             # 0 FAIL, no data-race report
go mod verify                                                # all modules verified, exit 0
go mod tidy -diff                                            # empty, exit 0
git diff --check                                             # exit 0
```

The two skips are `TestEnsureRejectsInsecurePermissions` and
`TestNewFileProviderRejectsInsecurePermissions`: POSIX permission bits are not enforced on
Windows, and both skip only there. The full management suite contains 23 top-level tests: the
12 Phase 6 tests plus the 11 Phase 6.1 tests.

### 17.3 Preservation

Schema remains `schema_migrations` version 13, dirty=false. Container creation timestamps,
images, health and volume timestamps match the Phase 6 record:
`hookflow-postgres-dev` 2026-11-23T12:47:45Z, `hookflow-rabbitmq-dev`
2026-11-24T14:29:08Z, `hookflow-postgres-data` 2026-11-23T12:39:42Z,
`hookflow-rabbitmq-data` 2026-11-24T14:29:10Z. `.env` (2026-11-24 17:57) and `compose.yaml`
(2026-11-24 02:03) were not modified. Git HEAD remains
`c6db32fd50ee8d7beb3108674b4cf46443d2aba4`; nothing is staged; the pre-existing `.gitignore`
modification and untracked tree are unchanged.

### 17.4 Documentation corrections

Corrected in this pass: D6/U25 statuses (architecture Owner-approved; development implemented;
production not implemented/not ready) across the proposal, runtime-readiness, overview,
database-schema, `erd.mmd`, configuration reference, runbook, README, the readiness and
cross-document reconciliation reports, the Bundle 1 closure body and this report; archived policy now denies every tenant
management write including subscription deactivation; suspended policy now records the
approved endpoint soft-delete and API-key revocation; scope descriptions no longer claim
`endpoints:write` grants rotation; "8 management integration tests" corrected to twelve
(23 with Phase 6.1); lint arithmetic stated as 13 + 1 + 3 = 17; the prior "no skips" claim
corrected to the two observed Windows-only skips. Historical reports keep their original text
with dated supersession notes.

### 17.5 Findings and not-executed work

> **Status update (2026-11-25):** The G-1 item below was subsequently fixed at the service
> boundary under separate authorization; its boundary unit tests pass, and the CLI end-to-end
> rerun is NOT RUN because the development database was unavailable (see §17.6). Its original
> text is retained as the Phase 6.1 record.

- **G-1 (non-critical, not fixed):** `accesskeys.List` returns `apikey.Record` values,
  including the Argon2id PHC verifier in `SecretHash`, to its caller. The CLI prints only
  ID/name/prefix/scopes/timestamps and never the verifier, and no HTTP route exposes it, but
  the service API carries verifier material in memory. Fixing this is an application change
  and requires separate authorization.
- Hosted CI was **not run** (no commit/push authorized); it is NOT VERIFIED.
- `golangci-lint` was not rerun in Phase 6.1; the 17-finding advisory result remains the Phase 6
  record for production code and is not claimed for the new test files.
- Production KMS/Vault custody and production readiness remain NOT IMPLEMENTED / NOT READY.
- Phase 7 (ingestion, fan-out, idempotency implementation, event API) was not started.

### 17.6 G-1 remediation (2026-11-25)

**Status: FIXED at the source and boundary-test level; CLI end-to-end regression NOT RERUN.**
The fix is an application-code, test and documentation change only.

- `apikey.Metadata` is a new operator-facing struct that does not embed `Record` and has no
  `SecretHash`, raw-token, salt or verifier field. It carries ID, organization, name, prefix,
  hash algorithm, scopes, last-used, expiry, revocation and creation timestamps.
- `List`, `Revoke` and the internal `get` fallback use a `metadataColumns` projection that
  omits `k.secret_hash`; the verifier is never selected or scanned into an operator result.
  `List` preserves newest-first ordering, revoked visibility and organization scoping.
  `Revoke` preserves its idempotent no-op, `ErrNotFound` mapping, locking, policy, audit and
  transaction behavior.
- `accesskeys.Create` now returns `(string, apikey.Metadata, error)`; the raw token is still
  returned only after a successful commit and exactly once, failures return an empty token and
  zero metadata, and the internal `Record` still carries the verifier to `Insert`.
  `accesskeys.List` returns `[]apikey.Metadata`; `accesskeys.Revoke` returns
  `(apikey.Metadata, bool, error)`.
- `Record`, `AuthRecord` and `FindForAuth` are unchanged. `Insert` was extended in the F-5
  follow-up to return the database-generated `created_at` (see §17.7); it still persists the
  verifier internally and does not return it. Authentication still retrieves `SecretHash` and
  verifies with the same PHC/Argon2id path. `cmd/apikey/main.go` was not edited, so
  create/list/revoke output and the one-time raw-token contract are unchanged.
- No schema, migration, constraint, persisted data, `.env`, keyring, Compose, container,
  volume, audit, scope, authorization or HTTP representation changed; no Git command mutated
  state.

New tests: `internal/modules/apikey/store_boundary_test.go` proves the metadata shape, absence
of verifier rendering, metadata-only List/Revoke projections (including no-op and not-found
paths), and that `FindForAuth` still selects the verifier. `internal/app/accesskeys/accesskeys_test.go`
asserts failed operations return zero metadata. Existing authentication and CLI assertions
were retained unchanged.

Observed validation: `gofmt -l` on the changed files exit 0 (empty), `go vet ./...` exit 0,
`go vet -tags=integration ./...` exit 0 (compile only), `go build ./...` exit 0,
`go test ./... -count=1` exit 0 (27 test-bearing packages ok, 2 no test files), focused
apikey/accesskeys/management/CLI unit tests exit 0, `go mod verify` exit 0, `go mod tidy -diff`
exit 0 (empty), `git diff --check` exit 0. The CLI integration tests could not run because the
development PostgreSQL at `127.0.0.1:5433` refused connections; both integration tests failed
at connect and no container, volume, database or queue was started or reset. They remain
NOT RUN for this remediation and must be rerun when the development database is available.

This addendum fixes G-1 at the source and boundary-test level but does not declare Phase 6
closed, authorize Phase 7, or assert production readiness; the CLI integration rerun remains
outstanding.

### 17.7 F-3/F-4/F-5 follow-up (2026-11-25)

This follow-up completes the remaining non-blocking findings from the G-1 source audit.

- **F-4 FIXED.** The `accesskeys.Create` comment now distinguishes the persistence/authentication
  path, which may handle the verifier, from operator-facing results, which must be metadata-only.
- **F-5 FIXED at source and unit-test level.** `apikey.Insert` now uses `RETURNING id, created_at`
  and returns the database-generated timestamp; `accesskeys.Create` puts it into the returned
  metadata after commit. The existing `api_keys.created_at DEFAULT now()` schema and timestamp
  semantics are unchanged; no migration or substitute timestamp was introduced. The actual
  PostgreSQL value was **NOT RUNTIME VERIFIED** after this follow-up.
- **F-3 FIXED at source and unit-test level.** `internal/app/accesskeys/service_boundary_test.go`
  adds deterministic fake-database/fake-transaction coverage for successful Create (raw token,
  verifier reaching Insert, metadata without verifier, persisted CreatedAt), begin/insert/audit/
  commit/archived Create failures returning empty token and zero metadata, and Revoke
  post-storage/transaction failures returning zero metadata. `TestInsertReturnsDatabaseCreatedAt`
  verifies the new `RETURNING` projection. A focused integration test,
  `TestManagementAPIKeyCreateReturnsPersistedCreatedAt`, was added to compare `Create` metadata with the
  `api_keys.created_at` row; it compiles but was **NOT RUN** because no safe integration database was available.
  The unit tests above are source-level tests, not PostgreSQL evidence.

Observed validation after this follow-up: `gofmt -l` on the changed Go files exit 0 (empty),
focused `go test ./internal/modules/apikey ./internal/app/accesskeys -count=1` exit 0 (both
packages ok), full `go test ./... -count=1` exit 0 (27 test-bearing packages ok, 2 no test
files), `go vet ./...` exit 0, `go vet -tags=integration ./...` exit 0 (compile only),
`go build ./...` exit 0, `go mod verify` exit 0, `go mod tidy -diff` exit 0 (empty), and
`git diff --check` exit 0. `go test -race` for the affected packages exited 2 and is **NOT RUN**
because the local toolchain has `CGO_ENABLED=0` (`-race requires cgo`). CLI and affected
management PostgreSQL integration tests are **NOT RUN**: `127.0.0.1:5433` refused connections and
the Docker daemon was unavailable; no infrastructure was started or recreated. The real-
PostgreSQL `created_at` value and CLI end-to-end behavior therefore remain unverified at
runtime, and the source/unit evidence above is not presented as PostgreSQL or CLI runtime
verification.
