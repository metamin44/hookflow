# HookFlow — Bundle 1 Final Closure Report

Date: 2026-11-24. Scope: final closure verification and limited remediation of Bundle 1.
Primary evidence source: [bundle1-execution-report.md](E:/hookflow/docs/operations/bundle1-execution-report.md) (preserved unchanged, Persian, sections A–K).
Approved decisions D1a, D1b, D2b, D2c, D2d, D3, D4, D5 and D7 were not changed. No new decision was adopted.

> **D6/U25 status addendum (2026-11-24, documentation-only):** the later cross-document reconciliation confirmed that D6/U25 remain **NOT APPROVED** and unimplemented. Wording in §F about an "empty-secret unsigned development snapshot" describes the current ungated runtime; no deployment-mode gate exists (D6-F is proposed, not approved). No Bundle 1 result or approved decision changes. See [d6-u25-cross-document-reconciliation-report.md](d6-u25-cross-document-reconciliation-report.md).
>
> **Phase 6.1 correction to the addendum above (2026-11-24).** The addendum is preserved as the earlier record. The
> Owner subsequently approved D6-A…D6-G and U25-A…U25-D as architecture, and the development runtime now implements
> the deployment-mode/unsigned gate, a local file KEK, the `hfse1.` envelope and Standard Webhooks v1 signing.
> Development implementation status: **IMPLEMENTED**; production KMS/Vault custody and production readiness:
> **NOT IMPLEMENTED / NOT READY**. No Bundle 1 result or approved decision changes. Current evidence is in the Phase 6.1
> closure report and `overview.md` §16.9.

## A. Executive Summary and Final Verdict

**Verdict: Bundle 1 is closed for local development — PASS, with 16 documented advisory lint findings remaining.**

All 32 findings from the original run were inspected individually. The 16 `errcheck` findings were real error-handling defects in the migration CLI and are now **Fixed**: the tool can no longer exit `0` after failing to deliver its own result. The remaining 16 findings (`cyclop` 9, `gocognit` 6, `nestif` 1) are complexity reports on validated protocol, validation and transaction code paths; they are classified **Advisory** and no refactor was performed solely to lower a score. No lint rule, threshold or exclusion was weakened, and no suppression comment was added.

The complete migration lifecycle was proven against a **brand-new disposable PostgreSQL 18.7 instance**: empty → up 13 → full down → back to the initial state → up 13 again, all through the project's own runner, with every command's exit code recorded. The disposable container and volume were removed afterwards; the development PostgreSQL container and volume were never contacted.

`.github/workflows/ci.yml` was validated statically and behaviourally: `actionlint` 1.7.12 reported zero findings (with `shellcheck` and `pyflakes` available in the validator image), a 35-assertion structural audit passed 35/35, and the workflow's "fail on skipped integration gates" script was extracted from the YAML and executed against two fixtures — it exits `0` on an all-pass stream and `1`, naming the test, on a stream where a package reports `pass` while containing a `skip`.

Hosted GitHub Actions remains **not verified** and cannot be claimed: `.github/` is untracked, so the workflow has never been committed, and the GitHub API reports `total_count: 0` runs for `metamin44/hookflow`. Production remains **not ready**: D6 encryption and U25 signing are still unapproved and unimplemented, and ingestion, egress/SSRF, HA/capacity and production observability are still open.

The four statuses are reported separately in section I and are never inferred from one another.

## B. Original 32 Lint Findings — Complete Classification

Baseline command (exact version previously used):

```text
$ golangci-lint version
golangci-lint has version 2.15.0 built with go1.27.3 from (unknown, modified: ?, ...) on (unknown)

$ golangci-lint run --timeout 5m      # exit code 1
32 issues:
* cyclop: 9
* errcheck: 16
* gocognit: 6
* nestif: 1
```

All 32 findings reproduced identically to the original report. Classifications below use three labels only: `Fixed`, `Advisory`, `Requires Owner Decision`.

### B.1 The 16 `errcheck` findings — all `Fixed`

Every one is in [cmd/migrate/main.go](E:/hookflow/cmd/migrate/main.go), on `fmt.Fprintf`/`fmt.Fprintln` calls against the injected `stdout`/`stderr io.Writer` values. Line numbers are from the pre-fix file.

| ID | Line | Statement | Real technical concern | Can it mislead or hide failure? | Fix applied | Class |
| --- | --- | --- | --- | --- | --- | --- |
| E-01 | 33 | `Fprintln(stderr, "usage: migrate ...")` | Usage text silently lost; `flag` also writes parse errors to the same stream unchecked. | No false success (path returns 2), but the operator loses the reason for a usage failure. | `fs.SetOutput(errOut)` plus `errOut.println`; write failures recorded. | Fixed |
| E-02 | 43 | `Fprintf(stderr, "error: %v", err)` (bad arguments) | Argument error message lost. | No false success (returns 2). | Checked through `reportWriter`. | Fixed |
| E-03 | 50 | `Fprintf(stderr, "error: %v", err)` (config load) | Configuration failure message lost. | No false success (returns 1). | Checked; failure status preserved. | Fixed |
| E-04 | 59 | `Fprintf(stderr, "error: %v", err)` (runner create) | Connection/DDL-adjacent failure message lost. | No false success (returns 1). | Checked. | Fixed |
| E-05 | 64 | `Fprintf(stderr, "error: close migration runner: %v", closeErr)` | Leaked runner resources reported only on stderr. | **Yes** — a failed `Close()` was printed and the command still exited `0`. | Deferred close now sets the exit code to `1` when it fails on an otherwise successful command. | Fixed |
| E-06 | 72 | `Fprintf(stderr, "error: %v", err)` (`Up`) | Migration failure message lost. | No false success (returns 1). | Checked. | Fixed |
| E-07 | 76 | `Fprintln(stdout, "migrations applied")` | Success result of a schema-changing command. | **Yes** — exit `0` with the result never delivered. | Checked; undeliverable result now exits `1`. | Fixed |
| E-08 | 78 | `Fprintln(stdout, "no pending migrations")` | "Nothing to do" conclusion lost. | **Yes** — a CI gate could report success with no evidence. | Checked; now exits `1` if unwritable. | Fixed |
| E-09 | 83 | `Fprintf(stderr, "error: %v", err)` (`Down`) | Rollback failure message lost. | No false success (returns 1). | Checked. | Fixed |
| E-10 | 87 | `Fprintln(stdout, "migrations rolled back")` | Destructive operation reported as done. | **Yes** — most sensitive case: exit `0` with the outcome lost. | Checked; now exits `1` if unwritable. | Fixed |
| E-11 | 89 | `Fprintln(stdout, "no migrations rolled back")` | "Nothing rolled back" conclusion lost. | **Yes** — could be mistaken for a completed rollback downstream. | Checked. | Fixed |
| E-12 | 94 | `Fprintln(stdout, "No migration has been applied.")` | Empty-database answer lost. | **Yes** — a provisioning script would see `0` and assume an applied state. | Checked. | Fixed |
| E-13 | 98 | `Fprintf(stderr, "error: %v", err)` (`Version`) | Version-read failure message lost. | No false success (returns 1). | Checked. | Fixed |
| E-14 | 101 | `Fprintf(stdout, "version: %d", version)` | The command's primary answer. | **Yes** — exit `0` while the reported version never reached the caller. | Checked. | Fixed |
| E-15 | 102 | `Fprintf(stdout, "dirty: %t", dirty)` | The dirty-state signal itself. | **Yes** — a dirty database could go unnoticed while exiting `0`. | Checked. | Fixed |
| E-16 | 104 | `Fprintln(stderr, "warning: migration state is dirty ...")` | Warning lost. | Not a false success (state still printed on stdout), but the explicit recovery warning disappeared. | Checked; a failed stderr write now also surfaces a warning on stdout and forces a non-zero status. | Fixed |

Why these were treated as genuine defects rather than style noise: `run` takes `io.Writer` arguments, so output can legitimately fail (closed pipe, broken redirect, full filesystem on a captured log). The pre-fix code discarded every such error and still returned `0`. For a command whose entire product is "did the schema reach version 13", that combination is a false green in any automation that trusts the exit status.

The fix is error propagation plus a meaningful exit code, not suppression:

- `reportWriter` wraps each stream, records the **first** write failure, and short-circuits later reports.
- `Write` is also wired into `fs.SetOutput(errOut)`, so the `flag` package's own error and default output is covered.
- `finalizeExitCode` converts "command succeeded but the report could not be delivered" into exit `1`, and preserves an existing usage (`2`) or runtime (`1`) status. When both streams are unusable, the exit status becomes the only signal — which is exactly the intended fail-closed behaviour.
- A failed `runner.Close()` now changes the exit status instead of only printing.

No `_ =` was introduced to silence a diagnostic, no `//nolint` was added, `errcheck` was not reconfigured, and every original message string is byte-identical.

### B.2 The 9 `cyclop` findings — all `Advisory`

Threshold is `max-complexity: 20`, set in [.golangci.yml](E:/hookflow/.golangci.yml) (unchanged during closure; modified 2026-11-24 02:03 by the earlier implementation run). All nine are guard-clause chains where each branch returns, never falls through, and no error is discarded.

| ID | File:line | Function | Score | What the branches actually are | Correctness/reliability impact | Class |
| --- | --- | --- | --- | --- | --- | --- |
| C-01 | [cmd/worker/lifecycle.go:142](E:/hookflow/cmd/worker/lifecycle.go) | `run` | 26 | Six sequential config loaders, component startup, error join, shared shutdown deadline, pool-cleanup timeout. | None observed. Every loader error is wrapped; cleanup failures are joined into `runErr`. Verified by real lifecycle tests including pool-deadline timeout. | Advisory |
| C-02 | [internal/app/delivery/store.go:472](E:/hookflow/internal/app/delivery/store.go) | `RecordDecryptFailure` | 21 | Transaction guard clauses plus the RB-4/B-1 disposition matrix. | None observed. Terminal/lease-expiry branches were verified against real PostgreSQL. | Advisory |
| C-03 | [internal/app/outbox/store.go:379](E:/hookflow/internal/app/outbox/store.go) | `ReapExpired` | 22 | `SKIP LOCKED` candidate loop plus T3 reaper and batch-bound checks. | None observed; reaper independence is covered by integration tests. | Advisory |
| C-04 | [internal/app/worker/processor.go:77](E:/hookflow/internal/app/worker/processor.go) | `Process` | 26 | The disposition state machine: parse → acquire → budget → HTTP → complete, each error mapped to one disposition. | None observed; every path returns an explicit `disposition.Decision`, so no branch can silently ACK. | Advisory |
| C-05 | [internal/platform/config/rabbitmq.go:78](E:/hookflow/internal/platform/config/rabbitmq.go) | `validateRabbitMQURI` | 22 | Reject-list validation of URI shapes, plus secret-safe fixed error text. | None observed; the branch count *is* the security posture. | Advisory |
| C-06 | [internal/platform/config/runtime.go:369](E:/hookflow/internal/platform/config/runtime.go) | `DeliveryWorker.Validate` | 21 | Lease invariant `L > D + H + P + S` plus each bound. | None observed; unit-tested. | Advisory |
| C-07 | [internal/platform/messaging/connection.go:155](E:/hookflow/internal/platform/messaging/connection.go) | `parseRabbitURI` | 29 | Flat pre-validation of scheme, credentials, port, fragment, control characters, `heartbeat`/`connection_timeout` overrides, then delegation to the pinned client parser. | None observed. Verified by unit tests and by real handshakes through a TCP proxy on both brokers. | Advisory |
| C-08 | [internal/platform/messaging/message.go:135](E:/hookflow/internal/platform/messaging/message.go) | `decodeEnvelope` | 23 | Five required identifier fields, rejects unknown JSON keys. | None observed; strictness is the approved identifiers-only contract D2b. | Advisory |
| C-09 | [internal/platform/messaging/message.go:208](E:/hookflow/internal/platform/messaging/message.go) | `validateDeliveryProperties` | 27 | Cross-check of MessageId/CorrelationId/headers plus the broker-owned `x-acquired-count`/`x-delivery-count` allow-list. | None observed; the allow-list was derived from real quorum redelivery behaviour, not relaxed speculatively. | Advisory |

### B.3 The 6 `gocognit` findings — all `Advisory`

Threshold `min-complexity: 30` (unchanged).

| ID | File:line | Function | Score | Assessment | Class |
| --- | --- | --- | --- | --- | --- |
| G-01 | [internal/app/publisher/session.go:126](E:/hookflow/internal/app/publisher/session.go) | `Session.Publish` | 43 | Highest score in the repository and the most protocol-sensitive function: bounded N=1 confirm/return wait, drain before and after publish, mandatory routing check, `UNKNOWN` plus whole-generation invalidation on any anomaly. Every branch returns `ResultUnknown` with an explicit reason; the two `_ = cause` lines are deliberate non-logging of a close reason that must not leak, each carrying a comment. Lowering the score would mean splitting the single owner-goroutine critical section under `s.mu`, which is a real deadlock/ordering risk for zero behavioural gain. | Advisory |
| G-02 | [internal/app/delivery/store.go:320](E:/hookflow/internal/app/delivery/store.go) | `Store.Complete` | 31 | Atomic completion with fencing, attempt accounting and terminal transitions. One row over the threshold; behaviour verified against real PostgreSQL. | Advisory |
| G-03 | [internal/app/outbox/store.go:144](E:/hookflow/internal/app/outbox/store.go) | `Store.Claim` | 32 | Claim batching, pacing/jitter and T1 state transition guards. | Advisory |
| G-04 | [internal/app/recovery/sweep.go:248](E:/hookflow/internal/app/recovery/sweep.go) | `Sweep.runPass` | 33 | Four-pass budget/candidate/cap accounting with per-candidate error classification. | Advisory |
| G-05 | [internal/app/worker/executor.go:140](E:/hookflow/internal/app/worker/executor.go) | `Executor.Execute` | 32 | Fail-closed guard chain (non-empty ciphertext, executor absence, non-POST, insufficient lease budget, nil context) followed by bounded response reading and status classification. | Advisory |
| G-06 | [internal/platform/messaging/topology.go:155](E:/hookflow/internal/platform/messaging/topology.go) | `EnsurePolicy` | 33 | Management-API reconciliation: lookup, exact-definition comparison, list, create/update, verify — each with an unexpected-status rejection. Idempotent and fail-closed by design (D2d). | Advisory |

### B.4 The 1 `nestif` finding — `Advisory`

| ID | File:line | Report | Assessment | Class |
| --- | --- | --- | --- | --- |
| N-01 | [internal/app/worker/processor.go:113](E:/hookflow/internal/app/worker/processor.go) | `if err != nil` has complex nested blocks (complexity 7) | The nesting is the `ErrSecretUnavailable` path: pacing computation, `RecordDecryptFailure`, then a three-way mapping to `TransientFailure` / `Failed` / `Uncertain`. It is one error path with four sequential dependencies, not tangled control flow; extracting it into a helper would remove the warning without changing behaviour, but that is a style-only edit inside the RB-4 disposition core, which the Owner-approved scope excludes ("do not perform broad refactoring solely to reduce complexity scores"). | Advisory |

### B.5 Roll-up

| Checker | Original | Fixed | Advisory | Requires Owner Decision |
| --- | --- | --- | --- | --- |
| errcheck | 16 | 16 | 0 | 0 |
| cyclop | 9 | 0 | 9 | 0 |
| gocognit | 6 | 0 | 6 | 0 |
| nestif | 1 | 0 | 1 | 0 |
| **Total** | **32** | **16** | **16** | **0** |

`Requires Owner Decision` is empty: none of the remaining items blocks correctness, and none can be resolved without a scope change the Owner has not asked for.

## C. Changes Made During Closure

Two source files changed; both are in the migration command. Nothing else in the repository was edited.

1. [cmd/migrate/main.go](E:/hookflow/cmd/migrate/main.go)
   - `run` now allocates two `reportWriter` values and delegates: `run → execute(...) → finalizeExitCode(...)`.
   - New `reportWriter` (`Write`, `printf`, `println`) records the first write failure and stops emitting afterwards.
   - New `exitSuccess` / `exitFailure` / `exitUsage` constants replace bare `0`, `1`, `2` returns; the numeric contract is unchanged.
   - `fs.SetOutput(errOut)` routes `flag` output through the checked writer.
   - The deferred `runner.Close()` failure now sets the exit code to `exitFailure` when the command itself succeeded.
   - All emitted strings are unchanged; the dirty-state warning still exits `0` when it is delivered.
2. [cmd/migrate/main_test.go](E:/hookflow/cmd/migrate/main_test.go)
   - Added `failingWriter` plus three tests: first-failure capture and short-circuiting, `finalizeExitCode` precedence (undeliverable success → `1`; usage `2` and runtime `1` preserved; clean output → `0`; both streams broken → `1`), and the usage path still returning `2` when stderr is undeliverable.
   - `TestParseArgs` and `TestRunRejectsInvalidArgumentsBeforeConfiguration` were left intact.
3. This report was created. The original Persian execution report was not modified.

Explicitly **not** done: no change to `.golangci.yml`, no `//nolint`, no `_ =` used to hide a diagnostic, no dependency added, no migration or schema file touched, no `.env` change.

### C.1 Live verification of the fix (real database, real ENOSPC)

A Linux binary was built from the changed tree (`GOOS=linux CGO_ENABLED=0 go build -o migrate-linux ./cmd/migrate`) and executed in a container against the disposable PostgreSQL 18.7 at version 13, writing stdout to `/dev/full`:

```text
--- 1. version, normal stdout ---
version: 13
dirty: false
exit=0
--- 2. version, stdout to /dev/full ---
error: migration result could not be reported: write /dev/stdout: no space left on device
exit=1
--- 3. up (no pending), stdout to /dev/full ---
error: migration result could not be reported: write /dev/stdout: no space left on device
exit=1
--- 4. up normal ---
no pending migrations
exit=0
--- 5. both streams to /dev/full ---
exit=1
--- 6. usage error keeps code 2 ---
exit=2
```

Before the change, cases 2, 3 and 5 printed the result into a failing stream and still exited `0`; they now exit `1`. Case 6 confirms the usage status still takes precedence, and cases 1 and 4 confirm the happy path is unchanged.

## D. Disposable PostgreSQL Migration Proof

**Isolation established before any write.** The image tag `postgres:18-alpine` had to be mounted at `/var/lib/postgresql` (not `/var/lib/postgresql/data`); the first attempt exited `1` with the image's own PG18 layout notice and was recreated. Identity was verified before running any migration:

| Attribute | Verified value |
| --- | --- |
| Container | `hookflow-b1closure-pg` (created in this section, removed in this section) |
| Volume | `hookflow-b1closure-pg-data`, single bind → `/var/lib/postgresql` |
| Port | `127.0.0.1:55433` (confirmed free first; `5432` and `5433` were already in use by unrelated projects) |
| Database / role | `hookflow_closure` / `hookflow` |
| `server_version` / `server_version_num` | `18.7` / `180007` |
| `data_directory` | `/var/lib/postgresql/18/docker` |
| Starting state | `public` tables = `0` (completely empty) |

The development container `hookflow-postgres-dev` and the volume `hookflow-postgres-data` were never targeted by any command; no project container or volume was modified. Database credentials were a task-local disposable placeholder injected per command and are not recorded here.

Commands used the project's own runner (`go run ./cmd/migrate`, Go 1.27.4) with only `HOOKFLOW_DB_*` environment variables pointing at `127.0.0.1:55433`:

| # | Command | Output | Exit |
| --- | --- | --- | --- |
| 1 | `migrate version` (empty database) | `No migration has been applied.` | 0 |
| 2 | `migrate up` | `migrations applied` | 0 |
| 3 | `migrate version` | `version: 13` / `dirty: false` | 0 |
| 4 | `migrate down 13` | `migrations rolled back` | 0 |
| 5 | `migrate version` | `No migration has been applied.` | 0 |
| 6 | `migrate up` (again) | `migrations applied` | 0 |
| 7 | `migrate version` | `version: 13` / `dirty: false` | 0 |
| 8 | `migrate up` (idempotency) | `no pending migrations` | 0 |
| 9 | `migrate reset` (invalid) | `error: unknown command "reset"; ...` + usage | tool exit 2 (`go run` wrapper: 1) |

Schema evidence from the container's own `psql`:

- After step 2 and again after step 6 (identical): `tables=14`, `indexes=56`, `triggers=2`, `constraints=213`, `schema_migrations = 13|f`.
- Tables: `api_keys, audit_logs, deliveries, delivery_attempts, endpoint_subscriptions, endpoints, events, idempotency_records, organization_members, organization_subscriptions, organizations, outbox_messages, schema_migrations, users`.
- After the full rollback (step 5) the only residue is `schema_migrations` with its primary key, two NOT NULL constraints and the corresponding composite/array types, holding **0 rows**. No application table, index, constraint or type remains. This is golang-migrate's own bookkeeping table, and the runner treats a zero-row `schema_migrations` as the initial state, which step 1 confirms by reporting "No migration has been applied". No special rollback procedure was needed: the runner's `Down(n)` maps to `Steps(-n)` and `down 13` is a complete rollback.
- `dirty` was never observed; the runner intentionally never forces a version, and no forcing was performed.

Integrity and cleanup:

```text
$ sha256sum -c b1c-migration-hashes.txt   # baseline captured before any closure edit
0 non-OK results out of 26 files            # all up/down migrations byte-identical
```

`hookflow-b1closure-pg` and `hookflow-b1closure-pg-data` were stopped, removed and confirmed absent. Afterwards `docker ps -a` still lists 22 containers (same as before the section), `hookflow-postgres-dev` remains `Up 11 hours (healthy)` and `hookflow-postgres-data` still exists. As a post-change regression, `go test -tags=integration ./internal/platform/database -run TestMigrationRunnerIntegration` passed against this disposable database.

## E. GitHub Actions Workflow Validation

No hosted run was performed or claimed. Two independent local validators plus one behavioural test were used against [ci.yml](E:/hookflow/.github/workflows/ci.yml).

Validator availability on this host: `actionlint` and `yamllint` are **not installed** locally (no `actionlint`, no `yamllint`, no usable `python3`). Both were therefore run from official images inside Docker, which is a substitution noted here rather than hidden:

| Tool | How it was run | Result |
| --- | --- | --- |
| `actionlint` 1.7.12 (built with go1.26.1, linux/amd64) | `docker run --rm -v E:/hookflow:/github/workspace -w /github/workspace --entrypoint actionlint rhysd/actionlint:latest -no-color -oneline .github/workflows/ci.yml` | **exit 0, zero findings** |
| `shellcheck` `/usr/local/bin/shellcheck` and `pyflakes` `/usr/bin/pyflakes` | Present inside the same validator image, so `actionlint` checked the embedded `run:` blocks as well | No findings |
| `yamllint` 1.32.0 + PyYAML 6.0.1 | `docker run --rm ... cytopia/yamllint:latest` | Parses cleanly; only default-style notices: missing `---`, `on:` treated as truthy, comment spacing, and 80-column `line-length` on the pinned digest lines. None is a workflow requirement; the long lines are precisely the intentional digest pins. |

Structural audit (a 35-assertion PyYAML script, read-only repo mount, **35/35 passed**):

- Jobs exactly `quality`, `integration`, `race`, `lint`; `permissions: contents: read`; `concurrency.cancel-in-progress: true`; every job has `timeout-minutes` (15/25/20/15).
- Pinning: **9/9** `uses:` are commit-SHA pinned; **3/3** container images digest-pinned (`postgres:18-alpine@sha256:0115…`, `rabbitmq:4.4.1-management-alpine@sha256:2b47…`, `rabbitmq:4.3.8-management-alpine@sha256:977a…`); Go pinned to `1.27.4` in all four jobs; `golangci-lint` pinned to `v2.15.0`.
- Matrix: exactly `4.4.1` and `4.3.8`, `fail-fast: false`, and the `rabbitmq:` service consumes `${{ matrix.image }}`.
- Services: PostgreSQL is 18-series with `pg_isready` health check (`interval 5s`, `timeout 5s`, `retries 20`) on a dedicated `hookflow_ci` database; RabbitMQ has `rabbitmq-diagnostics -q ping` (`interval 5s`, `timeout 10s`, `retries 30`).
- Migration gates: `Apply migrations` runs before the suite; `Prove down and up migrations` runs `migrate down 13` → `migrate up` → `migrate version` on the disposable CI service.
- Race job: `CGO_ENABLED: "1"` and `go test -race ./... -count=1`.
- Lint advisory policy preserved: `continue-on-error: true`, no conditional, no `if:` step that could turn findings into a hard failure — matching approved D7 guidance.
- Secrets: no `${{ secrets.` reference anywhere, no `set -x`, no `env`/`printenv` dump, no `.env` read; the three inline service credentials are all `hookflow-ci-`-prefixed throwaways for ephemeral services, and endpoints are loopback (`amqp://hookflow:***@127.0.0.1:5672/%2F`).

Behavioural test of the no-skip gate: the Python body was extracted verbatim from the YAML and run as the CI job would run it.

| Fixture | Contents | Exit | Behaviour |
| --- | --- | --- | --- |
| `b1c_gate_pass.jsonl` | `run`/`pass` only | **0** | gate stays silent |
| `b1c_gate_skip.jsonl` | one `Action:"skip"` inside a package that finally reports `pass` | **1** | prints `Required integration gate skipped: example.com/b TestNeedsRabbitMQ` |

This confirms the gate catches the dangerous case where `go test` exits `0` while a mandatory integration gate was skipped. `set -o pipefail` is present and the output is written with `tee`, so a `go test` failure is not masked by the JSON capture.

Cleanup on failure: the workflow creates no persistent resource. `services:` containers and the runner VM are destroyed at job end, and no step leaves state behind, so no `if: always()` cleanup step is required — absence of an explicit cleanup step is correct here, not a gap.

Limitations stated plainly: these are static and local-subprocess checks; they cannot prove runner environment behaviour, download availability of the pinned digests at Actions runtime, or test timing on hosted hardware. The remote repository currently reports `total_count: 0` workflow runs, and `.github/` is untracked, so this workflow has never executed on GitHub. **Hosted CI is not green and is not claimed to be.**

## F. Regression Test Results

All commands were run after the source change, from `E:\hookflow`.

| Check | Command | Result |
| --- | --- | --- |
| Formatting | `gofmt -l .` | PASS — empty output |
| Vet | `go vet ./...` | PASS — exit 0, empty |
| Build | `go build ./...` | PASS — exit 0 |
| Unit tests | `go test ./... -count=1` | PASS — **17/17** packages, including `cmd/migrate` with the new tests |
| Modules | `go mod verify` | `all modules verified` |
| Tidiness | `go mod tidy -diff` | PASS — no diff |
| Lint | `golangci-lint run --timeout 5m` | exit 1 with **16** findings (`cyclop` 9, `gocognit` 6, `nestif` 1). `errcheck` is now **0**; no new finding was introduced (one transient `revive unused-parameter` in the new test helper was fixed before this run) |
| Race (Linux) | `docker run -v E:/hookflow:/src:ro -w /src -e CGO_ENABLED=1 golang:1.27.4 go test -race ./... -count=1` | PASS — **17/17** packages, `race-exit=0`, no `DATA RACE` reported; repository mounted read-only |
| Migration runner | `go test -tags=integration ./internal/platform/database -run TestMigrationRunnerIntegration` | PASS against the disposable PostgreSQL 18.7 |
| Git hygiene | `git diff --check` | PASS — clean |

Deliberately **not** re-run: the full RabbitMQ 4.4.1 / 4.3.8 integration matrix and the real-TCP watchdog race tests. Reason and evidence: the only changed files are `cmd/migrate/main.go` and its test. `cmd/migrate` is `package main` and no file in the repository imports it (`grep -rn "hookflow/cmd/migrate" --include=*.go` → empty), and `depguard` already forbids `internal/…` from importing `cmd/…`. Publisher, Worker, Recovery, lifecycle and shared runtime sources are byte-identical to the tree that passed 18/18 on each broker with zero skips in the original run. The affected code path — the migration CLI's own reporting — was instead exercised directly against a real PostgreSQL 18.7 in sections C.1 and D, covering `up`, `version`, idempotency, invalid arguments, the deferred-close path and both stream-failure modes. No test was deleted, weakened or skipped to obtain these results.

## G. Remaining Risks and Blockers

Advisory, non-blocking for local development:

1. **16 complexity findings** remain. They are concentrated in exactly the code the Owner approved as protocol cores (bounded confirm/return protocol, identifiers-only contract, RB-4 disposition, four-pass Recovery, fail-closed validation). Reducing them requires behaviour-preserving extraction and, for `Session.Publish` in particular, splitting a mutex-held critical section — a change that could reorder listener draining or generation invalidation. Recommendation: treat as a separately scheduled engineering task that must be followed by the full two-broker integration matrix, not as part of Bundle 1 closure.
2. **Development environment persistence**: `.env` remains untouched; the throwaway test credentials used during the earlier matrix run were never persisted. RabbitMQ service credentials must be reconciled with the local environment before permanent runtime, as documented in the runbook.
3. **Delivery semantics are unchanged**: at-least-once with database-driven Recovery is not exactly-once at the receiver; a dead-letter copy can be lost and DLQ replay stays manual by decision D2d. Receiving endpoints must deduplicate.

Blockers that cannot be closed inside this authorization:

4. **Hosted CI verification** — requires committing and pushing `.github/workflows/ci.yml`, which is explicitly prohibited here. Until then the four jobs are validated only statically and locally.
5. **Production readiness** — still blocked on: production KMS/Vault custody and production activation (D6/U25 architecture is Owner-approved and development-implemented), event ingestion API with authentication and fan-out, SSRF/egress controls, HA/capacity/failover validation, and production metrics/alerting topology. Empty-secret snapshots execute unsigned HTTP only behind the development-only opt-in (`HOOKFLOW_DEPLOYMENT_MODE=development` plus `HOOKFLOW_ALLOW_UNSIGNED_DELIVERY=true`); production mode fails closed. A non-empty `secret_encrypted` fails closed before HTTP when it cannot be decrypted. *(This item is superseded for D6/U25 status by the Phase 6.1 correction addendum at the top of this report.)*
6. **No fresh production claims**: all evidence is from local, isolated PostgreSQL 18.7 and RabbitMQ 4.4.1/4.3.8 instances.

## H. Git Safety Confirmation

```text
$ git rev-parse HEAD
c6db32fd50ee8d7beb3108674b4cf46443d2aba4      # identical before and after closure

$ git status --porcelain
 M .gitignore
?? .env.example  ?? .gitattributes  ?? .github/  ?? .golangci.yml  ?? README.md
?? cmd/  ?? compose.yaml  ?? db/  ?? deployments/  ?? docs/  ?? go.mod  ?? go.sum
?? internal/  ?? scripts/  ?? test/

$ git diff --cached --name-only | wc -l
0                                              # staging area untouched
```

The only tracked modification is the pre-existing `.gitignore` change. The working tree gained exactly one new file in this session — this report — inside the already-untracked `docs/` tree, so `git status` output is unchanged in shape. No `add`, `commit`, `push`, `branch`, `stash`, `reset`, `checkout` or `clean` was executed, and no history or index was rewritten. `git diff --check` is clean.

`.env` was never read, printed or written: its timestamp is still `2026-11-23 16:07:43`, before this session began, and no credential value appears in any command output or in this report. Container operations were limited to creating and then removing the task-owned disposable PostgreSQL instance; the two helper images used for YAML validation were pulled but no existing image, container or volume was removed or modified. Total container count is 22 before and after, with `hookflow-postgres-dev` still healthy.

## I. Final Readiness Assessment

| Status | Result | Basis |
| --- | --- | --- |
| **Runtime Development Acceptance** | **ACCEPTED** | Unchanged from the original run and reconfirmed here: Publisher, Worker and Recovery execute the mandatory E2E path against real PostgreSQL 18.7 and real RabbitMQ 4.4.1 and 4.3.8, 18/18 packages per broker with zero skips; 17/17 unit packages; 17/17 Linux race packages. |
| **Local Final Closure** | **CLOSED — PASS WITH 16 ADVISORY FINDINGS** | All 32 original findings individually classified; 16 `errcheck` defects fixed and live-verified; 16 complexity items advisory with documented rationale; disposable down/up migration lifecycle proven end to end; workflow validated by `actionlint`, `yamllint` and a behavioural no-skip-gate test; full local regression suite green. No `Requires Owner Decision` item emerged. |
| **Hosted CI Verification** | **NOT VERIFIED** | `.github/` is untracked, so the workflow has never been committed or pushed, and the GitHub API reports `total_count: 0` runs for the repository. Local static validation is not hosted CI evidence, and is not presented as such. |
| **Production Readiness** | **NOT READY** | D6/U25 undecided and unimplemented; ingestion API, authentication, fan-out, SSRF/egress, HA/capacity/failover and production observability remain open; no production approval was recorded in this or any earlier run. |

**Can Bundle 1 be considered closed for local development?** Yes. The runtime is implemented, the local verification matrix is complete, the previously open lint work is now resolved or explicitly classified, and the migration lifecycle has primary evidence on a fresh disposable database. The remaining items are a documented advisory complexity backlog and two gates — hosted CI and production — that are structurally outside this authorization and require a separate Owner decision to start. This report makes no claim of production readiness, and no further implementation work was begun.

## Sources:

- [docker-library/postgres#1259 — PG18 image data directory layout](https://github.com/docker-library/postgres/pull/1259)
- [rhysd/actionlint — checks and usage](https://github.com/rhysd/actionlint/tree/v1.7.12/docs/checks.md)
- [GitHub Actions workflow run events REST API](https://docs.github.com/en/rest/actions/workflow-runs?apiVersion=2022-11-28)
- [golang-migrate — down/Steps and dirty state behaviour](https://github.com/golang-migrate/migrate)
- [golangci-lint v2 configuration reference](https://golangci-lint.run/usage/configuration/)
- [RabbitMQ publisher confirms](https://www.rabbitmq.com/docs/confirms)
- [RabbitMQ quorum queues and delivery limit](https://www.rabbitmq.com/docs/4.3/quorum-queues)

Protocol and tool semantics were checked against these sources; every PASS statement above comes from a command actually executed in this session, and the runtime results additionally rest on the real-broker runs recorded in the original execution report.
