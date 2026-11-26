# Runtime readiness - consolidated owner decision package

**Prepared:** 2026-11-23; protocol audit updated 2026-11-24; Phase 6.1 status reconciliation 2026-11-24
**Revision:** Rev 3 (evidence cut-off 2026-11-24) - Bundle 1 final protocol consistency audit, followed by a same-day implementation reconciliation, followed by the Phase 6.1 status reconciliation. Rev 3 corrected broker-redelivery versus retry-scheduling language, traced the RabbitMQ delivery-limit/DLX scenario through the implemented Recovery core, and replaced the underspecified "confirm and no return" rule with a bounded, source-verified Publisher protocol. Its proposal wording and decision history are preserved below.
**Status:** historical decision-preparation package plus current implementation reconciliation. Owner approval, the Bundle 1 implementation and the Phase 6 development security runtime supersede its pre-implementation "absent", "recommended", "not authorized" and "not approved" status statements where explicitly reconciled. D6/U25 architecture is **OWNER APPROVED** (2026-11-24) and the development implementation is **IMPLEMENTED**; production KMS/Vault and production readiness remain **NOT IMPLEMENTED / NOT READY**. Any remaining "NOT APPROVED" label in a historical proposal section is a pre-approval record, not the current status.
**Authority:** prepared on owner instruction ("HookFlow - Architecture Decision Closure & Runtime Readiness Assessment", 2026-11-23), revised for "HookFlow - Bundle 1 Final Protocol Consistency Audit" (2026-11-24), and reconciled after the owner-approved Bundle 1 implementation and D7 authorization. No Git mutation is performed by this reconciliation.
**Authoritative homes:** [overview.md](overview.md) (§1-§16, including the approval history in §12.16-§12.19 and the register in §13), [database-schema.md](database-schema.md) (§4-§7), [erd.mmd](erd.mmd). Where this package states a contract it links to those sections; it does not restate them.

> **Implementation reconciliation - 2026-11-24 (authoritative for current status).** The owner approved Bundle 1 (D1a/D1b, D2b-D2d and the numeric runtime profile) and authorized D7. The repository now contains amqp091-go v1.17.0; application-managed reconnect; the topic/quorum dispatch and direct-DLX/quorum-DLQ topology; policy `hookflow-dispatch-dlq`; strict identifiers-only envelopes; bounded N=1 Publisher T1/T2/T3; exact-pointer, fixed-lease, pre-HTTP-guarded Worker with real HTTP and the retry ladder; generation-owned handles; worker-hosted Recovery scheduling; optional Publisher/Worker metrics; and the D7 workflow/lint configuration. PostgreSQL integration uses RabbitMQ 4.4.1 as primary and 4.3.8 as floor, both pinned by multi-arch digest in CI. **Verification is pending:** this reconciliation did not run application tests, the broker matrix, migration down/up proof, race detector or lint analysis, and claims none passed.
>
> **D6/U25 status reconciliation - 2026-11-24 (Phase 6.1, authoritative for current status).** The Owner approved
> D6-A through D6-G and U25-A through U25-D as architecture on 2026-11-24. The development runtime is **IMPLEMENTED**:
> local file KEK provider, `hfse1.` envelope codec/AAD builder, Standard Webhooks v1 signing, the deployment-mode plus
> unsigned-delivery gate, and the management secret lifecycle. This does not provide a KMS/Vault or any production
> provider; **production custody and production readiness are NOT IMPLEMENTED / NOT READY**, and production activation
> is not approved. A nonempty `secret_encrypted` value fails closed through decrypt-failure pacing and never reaches
> HTTP; empty-secret snapshots execute unsigned HTTP only under the explicitly gated development configuration. The
> API serves health plus the `/v1` management routes. No migration/schema change is introduced. Historical
> recommendation tables, roadmap wording and earlier "not approved/unimplemented" sentences remain as Rev 3 evidence;
> the current-status sections below state what changed, and the D6 reconciliation notes in §8 mark the superseded
> proposal text.

---

**Dated execution addendum:** §17 records actual Bundle 1 execution on 2026-11-24 and supersedes this package's earlier local-test "verification pending" labels. Historical proposal/approval text remains intact. Hosted CI and production activation are not claimed.

## 1. How to read this package

Every item carries one of these statuses:

| Status | Meaning |
| --- | --- |
| **Already approved** | Settled by an earlier owner entry (§12.16, §12.17, §12.18 or the revision-16 approvals). Not reopened here; no new approval needed. |
| **Recommended for approval** | Fully analyzed here, no missing evidence; the owner can approve or reject it. |
| **Requires additional information** | The technical shape is proposed, but one input (deployment target, provider, or workload measurement) can only come from the owner; the partial proposal still stands. |
| **Blocked by external evidence** | A compatibility or tooling claim cannot be verified from authoritative sources yet; no approval is asked until it is. |

Decision identifiers are used **only where the repository already defines them** (U6, U21, U22, U24, U30, U34, B1-B6, RB-3/RB-5, §12.18). Dependency and topology pins have **no register ID**: §12.16 lists dependency pins as "not approved" but never numbered them. This package therefore uses local labels (D1a, D1b, D2a-D2d, D1-D7) and identifies them as local each time. No register ID is invented. D2a is the already-approved U21 minimum, not a new decision.

Two label traps in the task brief are resolved explicitly:

* **"B6" in the repository is not the Go client.** B6 is the approved *broker* floor: "RabbitMQ 4.3.x minimum compatibility target; the exact patch and verification are not approved" ([overview.md §12.16](overview.md)). The client-library choice is the local label D1a below (not a register ID).
* **U21's "minimal durable quorum-queue requirements" were approved, but the approved text is not in this repository.** §12.16 records that approval as given in the external Revision 17 owner-decision attachment. The §13 row therefore remains Open, and this package treats only the in-repo constraints as citable, marking the rest as an evidence gap (§12.2). Anything below that goes beyond those minimal requirements is a recommendation awaiting approval, not a restatement of an approved text.

**Approved versus new.** §4.1 quotes only the approved U21 minimum. Every other broker-topology element - exchange type and name, queue name, sharing model, routing key, tenant fairness, sharding, DLX target, dead-letter strategy, delivery limit, persistence, mandatory publishing, prefetch and deployment size - is a new recommendation (D2b-D2d) and is labelled as such. None of it borrows authority from U21 or B6. D2a requires no approval and must not be presented to the owner for re-approval.

---

## 2. Ground truth (Stage A)

### 2.1 Repository state

| Fact | Evidence |
| --- | --- |
| Module `github.com/metamin44/hookflow`, Go 1.27.4; direct dependencies are golang-migrate v4.20.1, pgx v5.13.0 and amqp091-go v1.17.0 | `go.mod`, `go.sum`; [overview.md §16.1](overview.md) |
| AMQP client and standard-library HTTP client are implemented; no encryption/KMS SDK is present | `go.mod`; `internal/platform/messaging`; `internal/app/worker/executor.go`; §16.4 |
| 13 schema units as 26 paired up/down migrations; trigger unit 13; no Bundle 1 schema change | `db/migrations/`; §16.1 |
| Implemented packages: `cmd/{api,migrate,outbox-publisher,worker}`; `internal/app/{delivery,disposition,idempotency,outbox,publisher,recovery,worker}`; `internal/platform/{config,database,http,messaging,observability}` | directory contents; §16 |
| Placeholders only: `internal/modules/*`, `internal/app/event`, `internal/platform/logger`, `internal/shared`, `deployments`; Bundle 1 adds `test/integration` cross-process tests, and D7 adds `.github/workflows/ci.yml` and `.golangci.yml` | directory contents; §16 |
| Earlier PostgreSQL 18.7 integration result is retained in §16.5; Bundle 1 RabbitMQ matrix, migration down/up proof and race verification are pending | [overview.md §16.5-§16.6](overview.md) |
| Git history is the docs-only HEAD. All implementation, migrations, compose and docs are **untracked**; only `.gitignore` is tracked and pre-existing modified | `git status`; §16.1 |
| HEAD before this task: `c6db32fd50ee8d7beb3108674b4cf46443d2aba4` | `git rev-parse HEAD` |

### 2.2 Implemented behaviour that constrains the decisions

| Area | What exists | Exact location |
| --- | --- | --- |
| Publisher database core T1/T2/T3 | T1 claim where `attempts < MaxClaims` with `SKIP LOCKED LIMIT BatchSize` and fresh `clock_timestamp()`; T2 publish/failure finalisation with `GREATEST` gate; T3 expired-lease reaping | `internal/app/outbox/store.go` T1 115-137, T2 246-299, T3 339-360, re-sample 422-428 |
| Publisher runtime loop | Implemented: bounded polling/rounds, N=1 confirm/return session, T1/T2/T3 outcomes, expired-lease reaping and application-managed reconnect | `cmd/outbox-publisher/lifecycle.go`; `internal/app/publisher/{session,relay}.go` |
| Recovery Sweep and scheduler | Four passes in fixed order expire -> repair -> re-arm -> stale; per-pass candidate/duration budgets; total budget; deadline checks; `SKIP LOCKED`; delivery-before-outbox lock order; `NOWAIT` helper; worker-hosted fixed-delay scheduler | `internal/app/recovery/{sweep,scheduler}.go`; `cmd/worker/lifecycle.go` |
| Budget invariant | Overflow-safe sum of pass durations <= total; equality permitted | `internal/platform/config/runtime.go` `ValidateDurationBudget`; tests `runtime_test.go` |
| Worker store | Exact-pointer acquire with lease/token/generation fence; pre-HTTP preparation guard; fenced Complete; decrypt-failure path validates fence, samples fresh time, checks event horizon before worker lease, paces, returns `AckEligible`; diagnostics bounded and scrubbed | `internal/app/delivery/{pointer,store}.go`; `internal/app/worker/processor.go` |
| Worker runtime | Implemented: generation-owned consumer, per-consumer QoS (`global=false`), real HTTP executor, retry ladder, Recovery and idempotency sweep; nonempty `secret_encrypted` HTTP remains fail-closed | `cmd/worker/lifecycle.go`; `internal/app/worker/{consumer,executor,processor,retry}.go` |
| B4 HTTP-budget guard | Implemented before HTTP through `PrepareHTTP` and executor recheck; fixed lease must exceed HTTP + handshake + post-process + shutdown reserve | `internal/app/delivery/store.go` `PrepareHTTP`; `internal/app/worker/executor.go` `RequiredLeaseBudget` |
| Disposition policy | Outcomes/actions with **no** normal `Nack`; ACK only after durable disposition; transient infrastructure may `reject(requeue=true)` | `internal/app/disposition/policy.go`; `internal/app/worker/consumer.go` |
| Configuration | Owner-approved Publisher/Worker/Recovery defaults and RabbitMQ timing defaults are implemented with validation; all `HOOKFLOW_*` variables are referenced in `docs/operations/configuration.md` | `internal/platform/config/{runtime,rabbitmq,metrics,worker}.go`; configuration reference |
| Infrastructure | D7 CI pins PostgreSQL 18 and RabbitMQ 4.4.1/4.3.8 service images by digest and runs migration up/down/up proof. `compose.yaml` is unchanged by D7; local RabbitMQ service wiring remains integration-owned | `.github/workflows/ci.yml`; `compose.yaml`; scripts |

### 2.3 Current-state classification

| Claim | Class |
| --- | --- |
| Producer-side transactional outbox, fenced claim, four-pass Recovery and scheduler, fixed worker lease, B5 decrypt-failure handling, RB-4 disposition policy | **Implemented** (database cores plus worker scheduling) |
| Broker client/topology/policy, Publisher loop, Worker consumer, unsigned empty-secret HTTP, retry ladder, generation-owned handles, optional Publisher/Worker metrics, D7 workflow/lint config | **Implemented; local build, integration and race verification executed 2026-11-24 (hosted CI pending)** |
| Signing, encryption/decryption, event-ingestion API, production HA/SSRF controls, production activation | **D6/U25 encryption, signing and the deployment-mode gate are Owner-approved and development-implemented; the authenticated Phase 7 ingestion API and transactional fan-out are development-implemented; production KMS/Vault custody, production HA/SSRF controls and production activation are not implemented / not ready** |
| Broker pins, topology, numeric runtime profile, CI shape and service-container integration harness | **Owner-approved/authorized and implemented**; historical Rev 3 proposal wording is retained below |
| Race detection, RabbitMQ matrix, fresh CI run, full Git auditability | **Race detection and the local RabbitMQ 4.4.1/4.3.8 matrix executed 2026-11-24; a fresh hosted CI run is pending, so no hosted-CI pass is claimed; Git auditability is verified read-only** |

### 2.4 Existing approvals that must not be reopened

| Approval | Scope preserved verbatim | Where |
| --- | --- | --- |
| A1-A10 | Lease columns, generation, gate/time, completed-attempt DDL, non-partitioned tables, PostgreSQL 18 target, text+CHECK enums, encrypted-copy snapshot, audit/U12 details | §12.16 |
| B1 | Publisher per-cycle claim accounting: pending-only claim, fresh DB time, atomic claim+attempt increment, complete lease tuple, commit before publish, fenced T2, atomic T3, final allowed claim valid, never claims failed, Sweep alone re-arms | §12.16, [overview.md §5.3](overview.md) |
| B2 | Four-pass Recovery contract: ordering, expiry precedence, bounded transactions, `SKIP LOCKED`, lock order, deterministic ordering, rollback/defer/cursor/conflict revalidation | §12.16, [database-schema.md §5](database-schema.md) |
| B3 | Acquisition, fencing, completion and event-expiry semantics, including late valid completion | §12.16, §12.18 |
| B4 | Fixed-lease approach and `L > D + H + P + S` enforcement requirements; numbers were expressly not approved in §12.16 and were approved later with Bundle 1 | §12.16; reconciliation above |
| B5 | Pre-HTTP decryption failure: no attempt row, no `attempt_count`, reacquire and validate fence, fresh time after lock, expiry precedence, scrubbed bounded diagnostic and pacing, ACK after commit | §12.16; schema §5 |
| B6 | RabbitMQ **4.3.x minimum compatibility target**; exact patch was unapproved in §12.16. The floor is unchanged; owner-approved D1b later selected 4.4.1 primary and 4.3.8 floor/fallback | §12.16; reconciliation above |
| F3-A/B/C/F8 | Provenance FK forms and `uq_outbox_lease_token` | §12.17 |
| §12.18 | Recovery event expiry wins over an active worker lease; late `Complete` fenced | §12.18, §5.3 |
| U1, U8, U18, U21 (external), U47, U48, U42-U46 | Id strategy, gate authority, completion-only attempts, quorum-queue minimums, JCS parser, number policy | §13, §14 |
| RB-4, RB-6 | No normal `basic.nack`; ACK after durable commit; transient infrastructure uses `reject(requeue=true)`; graceful shutdown never NACKs; DB is authoritative; low-cardinality labels | §5.3, schema §5, overview §7 |

---

## 3. Decision group D1 - Broker client and exact broker version (two independent approvals)

**Status:** approved by the owner after Rev 3 and implemented with amqp091-go v1.17.0 plus RabbitMQ 4.4.1 primary/4.3.8 floor pins. Build/vet and the §3.3 compatibility matrix remain verification gates; no successful run is claimed. B6 is unchanged: the approved floor remains "RabbitMQ 4.3.x minimum compatibility target".

**Local labels:** D1a, D1b and D2a-D2d are labels used by this package only. The repository has no numbered IDs for dependency or topology pins (§12.16 lists dependency pins as "not approved" and never numbered them). No new register ID is created.

### 3.1 D1a - RabbitMQ Go client

| Item | Content |
| --- | --- |
| Question | Which Go AMQP 0-9-1 client does HookFlow pin, at which exact version, and under what update rule? |
| Recommendation | `github.com/rabbitmq/amqp091-go` **v1.17.0**, an exact pin in `go.mod`, no wrapper library. |
| Is v1.17.0 current? | Verified, not assumed. GitHub releases checked 2026-11-23: v1.17.0 is the newest, published 2026-11-17; v1.16.0 on 2026-10-19, v1.15.0 on 2026-09-15, v1.14.0 on 2026-08-18, v1.13.0 on 2026-07-21. Roughly monthly cadence. |
| Release notes that matter | Pre-negotiation frame-size enforcement on reconnect; bounded body pre-allocation when the frame size is unlimited; `prefetchSize` guard and `QosConfig.PrefetchSize` deprecation; rejection of `prefetchCount > 65535`; `QosConfig.Global` deprecation with global-QoS documentation; URI `connection_timeout` honoured during recovery; TLS config built before dialing; channel QoS restored after recovery independently of topology recovery; socket closed on `DialConfig` errors. These are recovery-correctness fixes for a long-lived publisher and consumer. |
| Go compatibility | Module directive `go 1.20`. README: "This client supports two most recent Go release series." On 2026-11-23 those series are Go 1.26 and Go 1.27, so Go 1.27.4 is inside the documented policy. Direct evidence of the policy and **inference** for this exact toolchain; gate: an isolated stage-0 `go build`/`go vet` probe of v1.17.0 under Go 1.27.4 before the pin, then repository-wide build/vet after adoption at stage 3. |
| RabbitMQ compatibility | README: supports RabbitMQ from 2.0 "but primarily tested against currently supported RabbitMQ release series"; AMQP 0-9-1 is the stable protocol. Both 4.3.8 and 4.4.1 are in community support on 2026-11-23. Direct for the policy, inference for the exact matrix; gated in §3.3. |
| Publisher confirm API | `Channel.Confirm(noWait)`, `NotifyPublish`/`Confirmation{DeliveryTag, Ack}`, `PublishWithDeferredConfirm` and `PublishWithDeferredConfirmWithContext`, and exported `DeferredConfirmation.DeliveryTag` plus `{Done, Acked, Wait, WaitContext}`. `WaitContext` returns `ctx.Err()` if the context ends before the confirmation. Multiple outstanding publishes are supported, but `NotifyPublish` requires listener capacity at least equal to the outstanding count; `multiple` acks/nacks resolve a tag range and `confirms.go` resequences listeners in publish order. This capability is usable only inside the bounded protocol in §4.6. |
| What `false` from Wait means | "Not positively confirmed." `Acked()` is also false while pending. In v1.17.0 `confirms.go`, `Close()` nacks all pending deferred confirmations, and a publish error removes the tag with `ack=false`. A false result therefore conflates broker nack, channel close and local publish failure; a cancelled `WaitContext` returns false with its context error and is equally inconclusive. It is **not** evidence that the broker did not accept the message (§4.6). |
| Mandatory publishing and returns | `Publishing.Mandatory`; `NotifyReturn`; `Return` carries `ReplyCode`, `ReplyText`, `Exchange`, `RoutingKey`, properties (including `MessageId`) and `Body`. A `Return` has **no publisher sequence number**, so return-to-publish correlation uses `MessageId` within a channel generation, not a delivery tag. |
| Return-listener buffering | In v1.17.0 `channel.go`, `NotifyReturn` appends the caller's channel to the listener list. `notifyAll` performs a bounded send and abandons a send that blocks for the five-second `notifyTimeout`; a full return listener can therefore lose a return. Capacity is a correctness invariant, not a tuning preference (§4.6). The listener is closed during channel cleanup. |
| Manual consumer ack | `Delivery.Ack(multiple)`, `Delivery.Reject(requeue)`, `Delivery.Nack(multiple, requeue)` on the delivery's own channel. `ConsumeWithContext` stops delivery on context end. |
| Recovery API | `Connection.Recovery`, `DefaultConnectionRecovery`, `ReconnectionConfig`, `TopologyRecovery`/`DefaultTopologyRecovery`, `NotifyStateChange`, `IsConnectionRecoveryEnabled`, `Channel.NotifyClose`. Recovery is opt-in; topology must be re-verified after reconnect. Confirm sequencing/listeners are reset on recovery, so application mappings must be scoped to a channel generation. |
| Context cancellation limits | `PublishWithContext`/`PublishWithDeferredConfirmWithContext` cancellation stops the local attempt; frames already written cannot be retracted, so cancellation is an unknown outcome, not a rollback. `ConsumeWithContext` cancellation does not requeue in-flight deliveries; closing the channel does (RabbitMQ consumer docs). |
| Ordering limitation | Documented non-goal: no support for `basic.return` versus `basic.ack` frame ordering; protocol events arrive on separate Go channels, so their application receive order is not guaranteed. The safe protocol does not depend on `select` choosing the return first: it uses RabbitMQ's same-channel wire order, amqp091-go's synchronous same-channel dispatch, a bounded return buffer and a mandatory post-confirm return drain (§4.6). |
| License and maintenance | BSD-2-Clause (LICENSE and pkg.go.dev); maintained by the RabbitMQ core team; descended from `streadway/amqp`; 4,544 importing modules on pkg.go.dev (2026-11-23); monthly releases through 2026. Actively maintained. |
| Alternatives | (a) `go-rabbitmq` wrapper: extra dependency and less direct confirm/return control - rejected. (b) Other AMQP clients: no maintained equivalent confirm/recovery surface for RabbitMQ 4.x - rejected. (c) Streams client: different protocol, wrong queue model - rejected. |
| Version strategy | Exact `v1.17.0`, no ranges. A patch bump repeats this evidence (release notes, Go policy, broker policy) and the stage-3 gate; dependency updates are owner decisions, not routine. |
| Reversibility | Confined to the `internal/platform/messaging` placeholder boundary; swapping clients later is localized but not free once publisher/worker code exists. |

### 3.2 D1b - Exact broker version and image

**Correction to the earlier draft.** Rev 1 of this package recommended 4.3.5 and called it the latest 4.3 patch. That is wrong. The official release-information page (fetched 2026-11-23) lists 4.3.8 (16 Nov 2026), 4.3.7 (20 Oct 2026) and 4.3.6 (16 Sept 2026); 4.3.5 is from 17 Aug 2026 and is three patches behind its own line. 4.3.5 must not be pinned.

| Item | Content |
| --- | --- |
| Question | Which exact RabbitMQ release and image tag does HookFlow target, given the unchanged B6 floor? |
| Recommendation | **RabbitMQ 4.4.1**, image `rabbitmq:4.4.1-management-alpine`, exact tag in compose at adoption and the multi-arch digest captured then, following the PostgreSQL pinning convention. **4.3.8** (`rabbitmq:4.3.8-management-alpine`) is the floor-verification image and the fallback if the 4.4 matrix fails. Conditional on §3.3. |
| Release facts | 4.4.1: 16 Nov 2026 (4.4.0: 20 Oct 2026); community support to **31 Jul 2027**, commercial to 31 Oct 2028; minimum Erlang **27.0**. 4.3.8: 16 Nov 2026; community support to **31 Jan 2027**, commercial to 30 Apr 2028; minimum Erlang **27.0**. 4.2 community support ended 31 Jul 2026, so 4.2 is not a fallback. Sources: rabbitmq.com/release-information and the v4.3.8 release notes. |
| Images | Docker Hub tags verified 2026-11-23: `4.4.1-management-alpine` (amd64 image digest `sha256:4dcae0814af31d5c745eb5dc0cc88995ba14fb7178c107459aa4788e993f369e`, last pushed 2026-11-17T23:53:29Z); `4.3.8-management-alpine` (`sha256:cca1aab97f823e9fe7c9c7e2b8528c590a091c15ea7a53c4b290553108f7759d`, 2026-11-17T23:55:26Z); `4.3.8-alpine` (`sha256:3ef1cdffbaf1a43ce3fb69921fdd9c161c89840e1e90034d0c38063b7d71bff9`, 2026-11-17T23:54:47Z). Per-arch digests prove tag existence only; the multi-arch manifest digest is still to capture (§12.2). |
| Why 4.4.1 rather than 4.3.8 | (1) Lifecycle: 4.3 loses community security and bug fixes on 31 Jan 2027, about nine weeks after this package; 4.4 runs to 31 Jul 2027. Pinning 4.3.8 would force an upgrade before or immediately after go-live for no functional gain. (2) Feature parity for the contract: publisher confirms, mandatory/return, quorum queues, DLX, delivery-limit and reject/nack accounting all exist in 4.4; there is no 4.3-only capability HookFlow needs. (3) Same Erlang floor (27.0), and both lines are covered by the client's "currently supported release series" statement. (4) Both 4.3.8 and 4.4.1 shipped on 2026-11-16, so 4.4.1 is not less mature than 4.3.8; the 4.4 line has had 4.4.0 since 20 Oct 2026. |
| Risk of 4.4.1 | It is one week old. Mitigated by an unchanged 4.3.8 floor suite and by 4.4.1 being a maintenance release that fixes 4.4.0 regressions. If the floor suite exposes any AMQP 0-9-1, quorum, DLX or confirm difference, D1b reverts to 4.3.8 and a 4.4 review is scheduled before 31 Jan 2027. |
| Why not 4.3.0-4.3.7 | Superseded maintenance releases. The 4.3.8 notes record stricter dead-letter target validation and AMQP 0-9-1 validation fixes; 4.4.1 is the current 4.4 maintenance release. Pinning an older patch buys nothing and forgoes those fixes. |
| Why not 4.4.0 | Superseded by 4.4.1 four weeks later. |
| Erlang/OTP | Both lines require Erlang 27.0 minimum, and nodes fail to start on older releases. The official images bundle Erlang, so the host needs none; verify inside the image at stage 3 (`rabbitmqctl version`, `erl` version) and record the bundled OTP version. |
| Quorum and DLX behaviour at the selected version | Identical on the 4.3 and 4.4 documentation pages: delivery limit defaults to 20; `basic.reject` increments `delivery-count`; `basic.nack` does not; connection loss and channel crash increment it; consumer timeout does not; exceeding the limit drops or dead-letters; at-most-once is the default dead-letter strategy and at-least-once requires `overflow=reject-publish` plus the `stream_queue` feature flag. 4.4 adds explicit `x-opt-delivery-time`, deliberately not used (D2d). |
| B6 is unchanged | This approves an exact deployment version inside/above the "4.3.x minimum" floor, not a change to the floor. 4.3.8 remains the floor-compatibility image. If the owner instead requires the deployment itself to stay on 4.3.x, D1b reduces to 4.3.8 with a 4.4 review before 31 Jan 2027; no other decision in this package changes. |
| Reversibility | Before production data: a compose/image change. After go-live: a rolling upgrade within quorum-queue-compatible releases; no schema impact. |

### 3.3 D1b compatibility gate (stage 3, blocking)

Owner approval is recorded; implementation exists, but this matrix is **verification pending** in D7 CI. Run the same matrix against 4.4.1 (primary) and 4.3.8 (floor):

1. Declare and verify the quorum work queue and the DLX/DLQ; re-declare with a mismatched type and confirm startup fails closed.
2. Publish mandatory to an unbound routing key: `basic.return` is observed, the post-confirm drain records it, failure accounting runs and T2 `published` is never called.
3. Publish routable: positive confirm observed; T2 runs only after the post-confirm return drain finds no matching return.
4. `reject(requeue=true)`: redelivery observed; `x-delivery-count` and `x-acquired-count` increment.
5. `reject(requeue=false)`: dead-lettered with reason `rejected`; with the DLX removed, silently dropped (documented).
6. Delivery limit: on a throwaway queue with `delivery-limit=2`, the failed delivery that takes the count above the limit dead-letters with reason `delivery_limit` (operationally the third failed delivery; cheap proof of the 20-default arithmetic and the documented "more times than the limit" threshold).
7. Close a channel with unacknowledged deliveries: messages requeue; cancelling the consumer alone does not.
8. Reconnect: application-managed topology/policy re-verification and per-consumer QoS restore behave as configured. **Rev 3 correction:** RabbitMQ 4.3.8/4.4.1 accepted `global=true` but behaved per-consumer/ignored the global setting; it was not rejected. The implementation uses `global=false`.
9. Confirm-versus-close: an outstanding `DeferredConfirmation` resolves false on channel close; code treats it as unknown, discards the channel generation and never calls T2 `published`.
10. Return-versus-confirm race: make the positive confirm observable to the application before it drains an already-enqueued return; the return still wins and no `published` marker is written. Repeat with the maximum approved in-flight window and with confirms arriving out of publish order.
11. Listener bounds: return and confirm listener capacities are at least the in-flight window; no notification is dropped at that bound. Unknown, duplicate or stale returns fail closed and invalidate the channel generation.
12. Unknown outcomes: connection loss, channel close, local publish error, confirm timeout and context cancellation before a completed success decision all leave T2 uncommitted and force a fresh channel generation on retry.

Any failure on 4.4.1 selects the 4.3.8 fallback and is recorded as the D1b outcome. Items 10-12 are implementation-evidence gates for D2b as well as version-compatibility checks; they do not change the client or broker pins.

**Dependency:** D1a and D1b are independently approvable but are tested together with D2b at stage 3.

---

## 4. Decision group D2 - Broker topology, delivery contract and dead-lettering

**Status:** D2a was already approved and asks for nothing. D2b, D2c and D2d were Rev 3 recommendations and are now owner-approved and implemented: topic `hookflow.dispatch`/routing `delivery`, shared quorum `hookflow.dispatch.q`, strict identifiers-only messages, direct DLX `hookflow.dispatch.dlx`/routing `delivery.failed` to quorum `hookflow.dispatch.dlq`, and at-most-once delivery-limit-20 policy `hookflow-dispatch-dlq`. Broker-matrix verification remains pending. The approved U21 minimum and the Rev 3 proposals remain physically separated below so their histories cannot be conflated.

### 4.1 D2a - Already approved U21 minimum (no new approval, do not re-ask)

The owner's Revision 17 attachment, as recorded in §12.16, approved these minimal requirements:

> durable quorum delivery queues; a configured DLX; publisher confirms required.

That is the whole approved minimum. Explicitly **outside** it: queue and exchange names, exchange type, routing keys, one shared versus per-organization or per-endpoint queues, tenant fairness, sharding, DLX target, dead-letter strategy, delivery limit, persistence settings, mandatory publishing, consumer prefetch and deployment size. Rev 1 of this package presented concrete values for those elements inside one undifferentiated "D2", which made new choices look approved. Rev 2 separates them.

**Evidence gap (unchanged):** the approved text exists in the external attachment, not in this repository; §13 row U21 therefore stays Open. This subsection restates the three approved elements only and does not invent the rest of the attachment.

### 4.2 D2b - Exchange, queue, routing and message contract (Rev 3 proposal, subsequently approved)

Each row preserves the Rev 3 proposal, its trade-off, operational impact and reversibility. The owner subsequently approved this contract and Bundle 1 implements it; verification is pending.

| Element | Proposal | Why | Trade-off / operational impact | Reversibility |
| --- | --- | --- | --- | --- |
| Exchange | `hookflow.dispatch`, type `topic`, durable | Routing keys can later separate work (shards, priority) without a new exchange; durable matches the quorum direction | Topic routing costs slightly more than direct and offers more than V1 needs | Rename is a drain-and-switch; no stored data |
| Routing key | `delivery`, constant in V1 | Pointers are already targeted by `deliveries` rows; no broker fan-out needed | No broker-side filtering; acceptable at the assumed scale | Additive keys later; the old binding stays |
| Work queue | `hookflow.dispatch.q`, quorum, durable, `x-queue-type=quorum`, declared identically by publisher and worker | Satisfies the approved durable quorum queue; one queue keeps operations simple | Quorum queues are disk-heavy and unsuitable for very long backlogs (5M+); out of scope here | Queue type cannot change in place; a change is a new queue plus drain |
| DLX/DLQ | `hookflow.dispatch.dlx` (direct, durable), `hookflow.dispatch.dlq` (quorum, durable), binding `delivery.failed`; configured by policy | Realises the approved "configured DLX" and prevents silent drops at the delivery limit. Strategy and limit are D2d | An extra queue to monitor; a missing policy silently drops (startup assertion required) | Policy changes are dynamic; names changeable before data exists |
| Message body | Identifiers only: outbox id, delivery id, organization id, dispatch sequence, schema version. Payload stays in PostgreSQL | No PII or payload on the broker; small messages; the database stays authoritative | Every delivery costs the DB read it already needs for fencing | Additive schema-version evolution |
| Properties | `message_id = outbox id`; `correlation_id = delivery id`; headers `x-hookflow-schema-version`, `x-hookflow-delivery-id`, `x-hookflow-outbox-id`, `x-hookflow-sequence`; `delivery_mode=2`; `content_type=application/json` | Correlates returns within one channel generation and supports stale-generation detection | `message_id` is not broker dedupe and does not prove event ordering; duplicates are expected and fenced. An unknown outcome discards the channel generation so a stale return cannot attach to a later retry | Header additions are backward-compatible |
| Publishing | Confirm mode on a dedicated publisher channel generation; `mandatory=true`; one owner; bounded in-flight window `N >= 1`; buffered `NotifyReturn` and `NotifyPublish` listeners each with capacity at least `N`; tag-to-claim map; nonblocking return drain before every confirmation decision | Accept success only for a positive confirm whose matching return is absent after the drain. Mandatory exposes unroutable publishes; the bounded window makes return delivery lossless under v1.17.0's five-second listener timeout | The simplest reference is `N=1`. A larger `N` preserves the per-channel proof but needs a measured D3 window/latency profile; arbitrary unbounded publishes, undersized listeners and timer-based absence proofs are forbidden | Code-level; `N` is a new numeric runtime parameter requiring D3 approval |
| Declarations | Idempotent, fail-closed on type/durability/policy mismatch | A silently mismatched queue is worse than a refused start | Both binaries declare; a policy change must ship with the code | Code and startup check |
| Naming/isolation | Prefix `hookflow.dispatch`; environments isolated by vhost | Lets environments share code and topology declarations | One more vhost to provision | High |

Not in D2b: sharing and fairness (D2c), dead-letter strategy and limit (D2d), numeric prefetch/concurrency/lease (Bundle 2).

### 4.3 D2c - Queue sharing and tenant fairness (Rev 3 proposal, subsequently approved)

The shared-queue V1 proposal below is owner-approved and implemented. Per-organization queues, sharding and broker fairness remain unapproved.

| Decision | V1 proposal | Alternatives | Why | Operational impact | Reversibility |
| --- | --- | --- | --- | --- | --- |
| Queue cardinality | One shared queue | Per-organization, per-endpoint, sharded queues | Bounded topology, one consumer pool, no dynamic declaration, simplest operations | A high-volume organization can dominate the head of the queue; latency, not correctness | Adding per-organization queues later is a routing change plus in-flight drain; no data loss because the DB owns retries |
| Fairness | No broker-level fairness in V1; per-consumer prefetch only | Fair dispatch per organization, separate queues, broker priorities | No measured need; U32 tenant rate limiting is the approved later control | Hot-tenant head-of-line blocking is an accepted, stated V1 limitation | Additive later |
| Sharding | None in V1 | Key-prefix shards on the topic exchange | No measured need; sharding multiplies queues, consumers and operators | Single-queue throughput ceiling is the practical limit | The topic exchange leaves room for `delivery.<shard>` without a new exchange |
| Deployment assumption | 3 worker instances x 24 concurrent (Bundle 2) | More or fewer consumers | Bounded by prefetch and the DB pool; no queue-per-instance coupling | Scaling is a deployment change, not a topology change | High |

Not approved here: whether per-organization queues are ever required. That is a future decision, not deferred work authorized now.

### 4.4 D2d - Dead-letter target, strategy and limit (Rev 3 proposal, subsequently approved)

U21 already requires "a configured DLX", so the question is not whether to have one. The Rev 3 D2d proposal below is owner-approved and implemented, including startup policy verification. At-most-once copy loss and manual inspection/drain are explicitly accepted; no replay exists.

| Item | Proposal | Why | Trade-off / operational impact | Reversibility |
| --- | --- | --- | --- | --- |
| DLX/DLQ target | `hookflow.dispatch.dlx` (direct, durable) to `hookflow.dispatch.dlq` (quorum, durable) with `dead-letter-routing-key=delivery.failed` | Concrete realisation of the approved DLX; a durable target survives broker restart | One extra queue; DLQ depth must be monitored and runbooked | Dynamic policy change |
| Configuration method | RabbitMQ policy (`dead-letter-exchange`, `dead-letter-routing-key`), not `x-arguments` | RabbitMQ recommends policies: x-arguments cannot be changed without deleting and redeclaring the queue, while policies apply at runtime | The policy is deployment state, not code; a missing policy silently drops dead-letters, so startup must assert it | High |
| Guarantee | **At-most-once (the default) for V1**, not at-least-once | The DLQ is telemetry. For an eligible persisted Delivery, the Recovery stale pass creates a new generation rather than depending on the broker copy (§4.5); the implemented Worker scheduler and Publisher perform redispatch, with end-to-end verification still pending. At-least-once requires `overflow=reject-publish`, the `stream_queue` feature flag, holds dead-lettered copies in the source queue against its limits, and adds CPU/memory | A dead-lettered pointer can be lost in transit to the DLQ; documented and acceptable for telemetry. If the owner wants guaranteed DLQ copies, that is the alternative to approve, with higher resource cost | Strategy is switchable at runtime; switching from at-least-once to at-most-once deletes unconfirmed dead-letters |
| Delivery limit | 20 (the 4.0+ default), no `-1` | Keeps poison protection; the DB ladder has 8 attempts, so 20 is a backstop, not a scheduler. RabbitMQ dead-letters when the delivery count exceeds the limit | A prolonged reject loop or crash loop can dead-letter a still-live Delivery. The old Outbox row remains `published`; the stale pass may insert generation N+1 when its persisted-state predicates hold. No automatic DLQ replay occurs | Policy change; in-flight counts are affected, so change with alerting |
| Retry timers | No broker-side delayed/business retry scheduler: no DLX cycle, no TTL retry queue, no `x-delayed-retry-*`, no `x-opt-delivery-time` | PostgreSQL owns backoff and `next_attempt_at`; RabbitMQ may immediately requeue on `reject(requeue=true)` or channel/connection loss, but that is redelivery, not delayed retry scheduling (§4.5) | Broker-side backoff would be a second scheduler and is intentionally absent | A later addition would be a deliberate second-authority decision, not a config tweak |
| DLQ consumption | None in V1; inspect and drain manually with a runbook | Replaying a DLQ pointer would duplicate the DB-owned retry | Requires an operator | High |

### 4.5 Dead-letter and retry consistency (critical review)

**1. Separate the mechanisms.** PostgreSQL business retry scheduling is `outbox_messages.available_at`, `deliveries.next_attempt_at`, the DB attempt counters and Recovery. Immediate broker redelivery is `reject(requeue=true)` or automatic requeue on channel/connection loss. Quorum delivery-limit protection is a poison-message backstop; DLX/DLQ handling is telemetry and manual operator work. RabbitMQ does not run HookFlow's backoff ladder: V1 has no DLX retry cycle, TTL retry queue, `x-delayed-retry-*` or `x-opt-delivery-time`.

**2. RabbitMQ 4.3 accounting.** `basic.reject` increments both `x-acquired-count` and `x-delivery-count`; `basic.nack` increments only `x-acquired-count`. Client crash/connection loss and channel/session failure increment `delivery-count`; an intra-cluster partition and consumer timeout do not. RB-4 forbids normal `basic.nack`, so the approved transient path uses `reject(requeue=true)` and does count towards the limit. The quorum default is 20. The documented threshold is **exceeds** / "returned more times than the limit", not merely "equals": exceeding the limit drops the message or dead-letters it when a DLX is configured.

**3. At-most-once DLX loss is real.** The default quorum dead-letter strategy republishes without internal publisher confirms and removes the source message after publishing. A missing DLX silently drops; an unavailable or unroutable target can lose the copy in transit. D2d accepts that only because the DLQ is telemetry. At-least-once dead-lettering is not proposed; it requires `dead-letter-strategy=at-least-once`, `overflow=reject-publish`, a configured DLX and the `stream_queue` feature flag, and `drop-head` falls back to at-most-once.

**4. Failure classes and their broker action.**

| Situation | Broker action | DB action | Reaches DLQ? |
| --- | --- | --- | --- |
| Transient infrastructure failure before disposition (DB down, commit failed) | `reject(requeue=true)` | No attempt written; the Delivery may remain leased until expiry or another fenced transition | Only if the delivery limit is later exceeded |
| Invalid/poison pointer (unknown schema, missing delivery row, undecodable identifiers) | ACK after confirming no work is possible | Terminal state recorded or already terminal | No - deliberately not rejected, because rejection would loop a pointer that can never succeed |
| Stale dispatch generation | ACK, no HTTP | Discarded by the generation fence | No |
| Duplicate delivery (same `dispatch_seq`, already terminal or fenced) | ACK | No mutation | No |
| Terminal DB disposition (`succeeded`/`failed`/`expired`) | ACK after commit | Disposition committed before ACK | No |
| Retryable HTTP outcome | ACK after commit | Retry row with `next_attempt_at` | No |

**5. Requested scenario: publish -> repeated infrastructure reject -> DLX -> Recovery.**

1. The Publisher completes T1, safely publishes generation N under §4.6, and commits T2 `published`. Outbox N is `published`; the Delivery is still `pending` or `retrying`.
2. The worker consumes pointer N and acquires its fixed lease. Repeated transient infrastructure failures happen **before durable disposition**. RB-4 requires `reject(requeue=true)`; no attempt row and no `attempt_count` are written. The current Delivery store has no broker-reject/lease-release helper, so the lease must not be assumed NULL merely because the pointer was requeued.
3. RabbitMQ immediately redelivers the pointer. Each `reject(requeue=true)` increments the quorum delivery count. The DB backoff clock is not advanced by this broker action.
4. When the count exceeds 20, RabbitMQ dead-letters the pointer (or drops it if the DLX is missing/unusable under D2d). V1 never automatically replays the DLQ. The broker copy can therefore be absent even though the Delivery is live.
5. PostgreSQL still has the eligible Delivery and the published Outbox N. The implemented Recovery path is the **fourth pass, `stale`**, subject to the predicates below. It does not depend on the broker copy.

**6. Exact persisted-state eligibility.** `Run` executes expire -> repair -> re-arm -> stale and does not loop (`internal/app/recovery/sweep.go` 180-221). For the stated state:

| Pass / branch | Predicate | Applies after DLX? |
| --- | --- | --- |
| `expire` | Delivery `pending`/`retrying` and event `expires_at <= clock_timestamp()` | Yes, first; expiry wins and makes stale recovery ineligible |
| `repair` | Unleased, due Delivery and **no** Outbox row for current `dispatch_seq` | No; Outbox N exists |
| `re-arm` | Unleased, due Delivery and current Outbox row is `failed` | No; Outbox N is `published` |
| `stale`, expired worker lease | Delivery `pending`/`retrying`, event unexpired, lease token non-NULL and lease expiry `<= clock_timestamp()` | Yes. This branch does **not** require `next_attempt_at` to be due, does **not** apply `StalePublishedAfter`, and does **not** require an Outbox row in `staleEligible` |
| `stale`, unleased published generation | Delivery `pending`/`retrying`, event unexpired, lease NULL, `next_attempt_at <= clock_timestamp()`, current Outbox N `published`, and `published_at <= clock_timestamp() - StalePublishedAfter` | Yes, once both the schedule and age gates hold |

The SQL predicates are `repairSelectSQL`, `rearmSelectSQL` and `staleSelectSQL` (`sweep.go` 337-396); the post-lock revalidation is `staleEligible` (`sweep.go` 810-828). Existing tests use a one-minute stale threshold; D5 proposes five minutes. A still-live worker lease therefore delays the leased branch until expiry, while an already-cleared lease waits for both `next_attempt_at` and the published-age threshold.

**7. What Recovery mutates.** `staleCandidate` (`sweep.go` 649-725) locks the Delivery and its Outbox rows, samples fresh DB time, revalidates eligibility, rejects an existing N+1, and commits one atomic transition: advance `dispatch_seq` N -> N+1, clear the worker lease, and insert a new Outbox N+1 with `status='pending'`, `attempts=0`, no publisher lease and `available_at=GREATEST(now, next_attempt_at)`. Outbox N remains `published`. The old row is **neither recreated nor re-armed**; a **new generation is required**.

**8. Does Recovery guarantee redispatch?** The database core guarantees that, when the stale pass reaches, locks, revalidates and commits an eligible candidate, generation N+1 exists atomically. Rev 3 found no scheduler, Worker runtime or Publisher loop; those are now implemented (`internal/app/recovery/scheduler.go`, `cmd/worker/lifecycle.go`, `internal/app/publisher/relay.go`). N+1 still has `available_at=GREATEST(now, next_attempt_at)` and requires Publisher claim, safe publish and T2. The event may expire first, and budgets/contention/cancellation may defer or abort a candidate. End-to-end DLX-to-N+1-to-publish verification remains pending; implementation is not treated as a successful test result.

**9. Existing test coverage.** `TestStaleExpiredLeaseRedispatchIntegration` (`sweep_integration_test.go`) covers an expired worker lease plus published N, asserts N+1 pending with zero attempts, preserves published N, and proves a second run does not duplicate N+1. `TestStalePublishedGenerationRedispatchIntegration` covers the unleased age threshold and excludes recent-published and pending-current rows. `TestEligibilityPredicates` (`sweep_test.go`) covers the leased/unleased branches. The integration-owned `test/integration/recovery_e2e_integration_test.go` now contains `TestEndToEndDLQRecoveryRedispatchIntegration`; its execution on the pinned broker matrix is verification-pending, not a claimed pass.

**10. No replay.** A DLQ message can be a valid pointer whose DB Delivery remains eligible; replaying it would duplicate the DB-owned redispatch. Inspect and drain manually under a runbook; never re-inject automatically.

**11. Version and limit checks.** The 4.3 and 4.4 quorum-queue pages document the same delivery-count table and default 20. The selected version does not change this analysis. The DB ladder of 8 does not cap broker deliveries: infrastructure failures and crashes can requeue without writing a DB attempt, and prefetch can advance multiple outstanding pointers together. Alert on DLQ growth and stale-pass age; do not treat the DLQ as a work queue.

### 4.6 Publisher confirms and mandatory returns (critical review)

**1. Approved and implemented protocol: bounded channel generations.** Bundle 1 uses a dedicated confirm-mode N=1 publisher channel, never a consumer or RPC channel. One goroutine owns admission, the tag-to-claim map, the return set and every success/failure decision. Confirm mode and exactly one buffered `NotifyReturn`/`NotifyPublish` listener pair are installed before the first publish. A *channel generation* is the channel plus those listeners, sequence state and maps; an unknown outcome discards the whole generation.

**2. Bounded window and buffers.** Let `N >= 1` be the maximum outstanding publishes on one channel generation. Admission stops at N; `N=1` is the simplest reference implementation. Both listener channels have capacity at least N. Under this protocol at most N valid returns can be unprocessed, because there are at most N outstanding publishes and no new publish is admitted until the owner processes outcomes. In v1.17.0 `notifyAll` abandons a return send after five seconds when the listener is full (`channel.go`; the full-listener drop is also covered by `TestBasicReturnDispatchDropsNotificationOnFullListener`). The capacity invariant prevents that timeout on the correctness path. `NotifyPublish` independently documents the same outstanding-count capacity requirement and resequences confirms in publish order.

**3. Admission and correlation.** Before each call, register the retained claim under its unique `message_id=outbox id`; call `PublishWithDeferredConfirmWithContext` with `mandatory=true`, then register the returned `DeferredConfirmation.DeliveryTag` in the tag-to-claim map. A nil confirmation, publish error or pre-call cancellation removes the provisional message-id mapping and follows the failure path. Confirms map by tag; returns map by `MessageId` **within the current channel generation**. The decision stream is `NotifyPublish`; `Wait`/`WaitContext` alone is not the complete return-aware decision. A row is not republished on that generation until its prior tag is resolved; after an unknown outcome it moves to a fresh generation. `message_id` is identity correlation only: it neither supplies the missing return sequence nor proves ordering. Never correlate a return by arrival order or by "the next confirm". An unknown, duplicate or stale return fails closed and invalidates the generation.

**4. Decision algorithm.** For each confirmation event, the single owner first drains all immediately available returns, then applies the confirmation, then drains returns again before deciding:

1. Record every drained return against its current `message_id`; any unmatched/duplicate return invalidates the generation.
2. A positive confirm plus a matching return is `RETURNED`, never `CONFIRMED`.
3. A positive confirm with no matching return after the second drain is `CONFIRMED`.
4. A negative confirm is `NACKED`; RabbitMQ says the broker refuses responsibility. It is failure, not success, even if a return is also present.
5. Channel/connection close, listener closure, local publish error, confirm timeout or context cancellation observed before a completed success decision is `UNKNOWN`. No `MarkPublished` call is allowed.

**5. Why the confirm-first application race is safe.** RabbitMQ 4.3 specifies `basic.return` before `basic.ack` for an unroutable mandatory publish. amqp091-go v1.17.0's connection reader dispatches same-channel frames synchronously; `Channel.dispatch` calls `notifyAll(ch.returns, ...)` before `confirms.One/Multiple(...)`. Thus, on the correctness path, the return is enqueued before the confirm becomes observable. The application may still receive the confirm first because returns and confirms use different Go channels and `select` has no ordering guarantee. The second nonblocking drain observes the already-enqueued return. This is an application-level proof from wire order + synchronous dispatch + buffer capacity + drain; it does **not** claim that amqp091-go provides a combined ordered event stream.

**6. Multiple outstanding publishes.** RabbitMQ may confirm out of publish order and may use `multiple`; amqp091-go resolves tag ranges and resequences `NotifyPublish` listeners. The owner therefore keeps all N mappings until each tag is resolved and applies the same drain-before-decision rule to every confirmation. A larger N does not weaken the proof while the buffer/admission invariants hold. Arbitrary unbounded publishing or buffers smaller than N is outside the approved design and is blocking.

**7. Unknown outcomes and recovery.** `Wait()` false, `Acked()==false`, a false `WaitContext`, channel close and connection loss do not prove non-acceptance. `PublishWithDeferredConfirmWithContext` checks cancellation before publishing but cannot retract frames already written. On any unknown outcome, leave T2 uncommitted, close/discard the channel generation and establish a fresh channel with fresh listeners/maps before retrying. Library-automatic recovery may reuse transport/channel state, but the application must not reuse the old generation or its mappings because confirm sequencing is reset. If the claim is still valid, fenced T2 failure accounting may schedule a retry; otherwise T3 lease expiry handles it. Duplicates remain expected and fenced.

**8. No grace-period proof.** A timer may bound waiting and trigger `UNKNOWN`, but silence for a grace period is never evidence that no return exists. Only the ordered, drained confirmation decision in items 4-5 can establish success.

**9. Preserved invariant and throughput.** T1 still commits before publish; T2 `MarkPublished` remains fenced by message id, owner and lease token; T3 remains unchanged. Only `CONFIRMED` may call T2 success. At-least-once publishing and duplicate tolerance are preserved. The per-channel window N and confirm latency determine throughput, so D3's 400 msg/s figure is a benchmark target, not a proven property of `N=1` or of an unmeasured pool.

**10. Publisher state transitions (implemented by Bundle 1).**

| State | Trigger | Next |
| --- | --- | --- |
| CLAIMED | B1 claim committed | PUBLISH_SENT |
| PUBLISH_SENT | Positive confirm and post-confirm drain finds no matching return | CONFIRMED |
| CONFIRMED | Fenced T2 commit succeeds | PUBLISHED |
| PUBLISH_SENT | Matching return or negative confirm | RETURNED/NACKED -> fenced T2 failure accounting |
| PUBLISH_SENT | Publish error, channel/connection loss, listener close, timeout or cancellation before success decision | UNKNOWN -> no T2 success; discard generation |
| CONFIRMED | T2 commit fails or process dies | Duplicate window; T3 or later claim republishes |
| RETURNED / NACKED / UNKNOWN | Retryable and claim still fenced-valid | T2 failure sets pending/failed per B1; never `published` |

No terminal state assumes the broker did not accept the message; no success state exists without the complete confirmation-and-return protocol.

**11. Telemetry only.** Confirm latency, nack count, return count, unknown/abandoned-generation count, listener occupancy, unconfirmed-at-close count, `x-delivery-count` and `x-acquired-count` are observability inputs. None may drive scheduling or substitute for the protocol.

### 4.7 Delivery discipline (approved constraints, restated not changed)

RB-4 and the existing dispatch contract already fix these rules; they are repeated so the new topology proposals do not obscure them. None of this needs approval again.

* ACK only after the durable database disposition is committed; never after a successful HTTP call alone.
* No normal `basic.nack`. A transient infrastructure failure before disposition uses `basic.reject(requeue=true)`.
* Graceful shutdown stops consumption, lets in-flight work finish within the lease, and closes the channel so unacknowledged deliveries requeue; it never NACKs remaining work. Cancelling the consumer alone does not requeue in RabbitMQ 4.x.
* Duplicate and stale pointers (older `dispatch_seq`, already terminal) are ACKed and discarded without HTTP; this is database fencing, not broker deduplication.
* A broker outage stops consumption; Recovery and the database continue; on reconnect the topology is re-declared and consumption resumes.
* The database remains authoritative for retry scheduling; broker counters are telemetry only.

### 4.8 Bundle 1 protocol-audit classification

| Issue | Classification | Consequence |
| --- | --- | --- |
| RabbitMQ 4.3 `reject`/`nack`, default 20 and "exceeds" threshold | **Resolved by verified design** | D2d can be approved with the corrected wording; RB-4 is unchanged |
| At-most-once DLX as telemetry rather than a replay source | **Owner-approved and implemented** | Possible DLQ-copy loss is accepted; inspect/drain manually, never replay |
| Recovery after DLX: exact stale-pass predicates and N -> N+1 transition | **Implemented; verification pending** | The database transition and scheduler exist; no new test pass is claimed |
| Eventual redispatch after DLX | **Runtime implemented; end-to-end evidence pending** | Worker scheduler and Publisher loop exist; a real DLX-to-Recovery-to-publish test is still required before activation |
| Safe Publisher confirm/return protocol | **Owner-approved and implemented** | N=1 generation-owned session, bounded listeners and post-confirm return drain exist |
| Publisher implementation and confirm-first/return-buffer failure tests | **Implemented; stage-3/4 verification pending** | The §3.3/§13 gates must pass before activation |
| D3 capacity under the approved in-flight window | **Approved profile implemented; benchmark pending** | N=1 throughput and confirm latency still require measurement on both broker targets |
| Unbounded publishes, undersized listeners, cross-generation correlation or timer-only absence proof | **Blocking** | These variants are not approved and must not be implemented |

No unresolved architectural blocker remains in the approved bounded protocol. Production activation remains blocked by pending implementation evidence and by D6/U25, API, HA and SSRF gaps recorded in the operations runbook.

---

## 5. Decision group D3 - Publisher tuning and B1/RB-5 numerics

**Status:** owner-approved after Rev 3 and implemented as validated defaults/configuration. Capacity under the serialized N=1 Publisher remains benchmark-pending; approval of the profile is not a throughput result.

### 5.1 Numeric table

| Parameter | Value | Unit | Existing config field | Note |
| --- | --- | --- | --- | --- |
| Batch size (rows per claim transaction) | 200 | rows | `HOOKFLOW_PUBLISHER_BATCH_SIZE` | Implemented default. |
| Maximum claims per retry cycle M | 5 | claims | `HOOKFLOW_PUBLISHER_MAX_CLAIMS` | This is the **per-row** `attempts < M` counter, not a loop cap. |
| Poll interval | 500 | ms | `HOOKFLOW_PUBLISHER_POLL_INTERVAL` | Implemented; loop sleeps only after a non-full batch. |
| Consecutive claim rounds per tick | 4 | rounds | Internal constant | Not configuration; bounds one tick to 800 messages. |
| Publisher lease | 30 | s | `HOOKFLOW_PUBLISHER_LEASE_DURATION` | Implemented default. |
| Lease reaping interval | 10 | s | `HOOKFLOW_PUBLISHER_REAP_INTERVAL` | Implemented ticker; reaps at most one batch per run. |
| Backoff B(c) | `min(2s * 2^(c-1), 60s)` | duration | `HOOKFLOW_PUBLISHER_BACKOFF_BASE`, `..._BACKOFF_CAP` | c = consumed claim count, 1..M. With M=5 the applied maximum is 32 s; the 60 s cap is forward-looking. |
| Jitter | full jitter, `uniform[100ms, B(c)]` | duration | `HOOKFLOW_PUBLISHER_BACKOFF_MIN` plus base/cap | Implemented; keeps retries de-synchronised without a near-zero tight loop. |
| Scheduling horizon | none | - | - | No lifetime bound is approved or introduced; gates stay `next_attempt_at`, `available_at`, event expiry. |

### 5.2 Why these values

* **Capacity.** 200 rows per claim, one claim per 500 ms with up to four continuation rounds gives 1,600 msg/s of row-claim headroom and a **400 msg/s planning target** per publisher instance. Bundle 1 implements the safe N=1 reference, not a channel pool. Therefore 400 msg/s is not a proven confirm/return throughput: benchmark N=1 (and any later approved bounded window), listener occupancy and p95 confirm latency on both broker targets before the profile is treated as capacity-validated.
* **DB load.** At most 2 claim transactions/s idle-busy, plus one reap transaction per 10 s and one T2 per batch. This is negligible against the ingestion path and keeps the outbox from becoming the bottleneck.
* **Duplicate window.** A publisher crash after publish and before T2 duplicates work until the lease expires: worst case 30 s lease + 10 s reap + republish. That is acceptable because duplicates are fenced and never lose data.
* **M and recovery.** After M failed claims a row is `failed`; only the Sweep re-arms it. With 2/4/8/16/32 s backoff the row fails within about 62 s of a broker outage and is re-armed on the next Sweep pass, so an outage is retried roughly every 1-2 minutes without a tight loop.
* **Lease coupling.** Lease (30 s) must exceed the confirm wait (proposed `min(5 s, lease/6)`) plus a full batch publish and T2 commit; 30 s covers six full cycles. Reap interval must be shorter than the lease (10 s < 30 s) so expired leases are cleared promptly.

### 5.3 Explicit workload assumptions (no measurements exist)

The repository contains no traffic data. The initial profile assumes: 10 organizations, 1,000 active endpoints, **10 events/s sustained and 25 events/s peak**, 2.5 destinations per event, subscriber p50 latency 300 ms, p95 1 s, and 1% of endpoints as slow as the 10 s HTTP deadline. Validation plan: before stage 7, record 7 days of real ingress and publish/delivery histograms and re-derive batch/poll/concurrency; every parameter above is configuration, so re-tuning needs no schema change.

### 5.4 Reversibility

All values are implemented runtime configuration. Reversible without migration; the main operational hazard is prolonged duplicate publishing if the lease were raised carelessly while old binaries still run, so deployment must roll publisher instances together.

---

## 6. Decision group D4 - Worker lease and HTTP execution (B3/B4) plus retry values

**Status:** the Bundle 1 B3/B4 numeric profile and retry ladder are owner-approved and implemented. U24's historical register row is preserved; this reconciliation does not infer closure of unrelated U24 scope. HTTP execution uses the gated development unsigned path for empty-secret snapshots; no deployment-mode gate existed when this paragraph was written. D6-F is now Owner-approved and implemented for development, while production custody remains not implemented. Historical "verification pending" labels are superseded by §17 and the Phase 6.1 evidence.

### 6.1 Lease model

| Parameter | Symbol | Value | Unit | Field |
| --- | --- | --- | --- | --- |
| Worker lease | L | 30 | s | `HOOKFLOW_WORKER_LEASE_DURATION` |
| HTTP deadline (network end to end) | D | 10 | s | `HOOKFLOW_WORKER_HTTP_DEADLINE` |
| Handshake reserve | H | 2 | s | `HOOKFLOW_WORKER_HANDSHAKE_BUDGET` |
| Post-process/commit reserve | P | 3 | s | `HOOKFLOW_WORKER_POST_PROCESS_BUDGET` |
| Shutdown reserve | S | 10 | s | `HOOKFLOW_WORKER_SHUTDOWN_RESERVE` |
| Worker concurrency per instance | W | 24 | goroutines | `HOOKFLOW_WORKER_CONCURRENCY` |
| Consumer prefetch per instance | - | 24 | messages | `HOOKFLOW_WORKER_PREFETCH` |
| Worker pgx pool | - | 30 | connections | `HOOKFLOW_WORKER_POOL_SIZE` |
| Worker instances (initial) | - | 3 | processes | operational assumption, not configuration |

**Invariant:** `D + H + P + S = 25 s < L = 30 s`, leaving 5 s of unallocated headroom beyond the explicit safety term. `S` absorbs Go scheduler/GC pauses, clock steps, and shutdown signalling; it is deliberately larger than any measured pause and is not "guessed spare".

**D vs H/P, without double counting.** `D` is one `context` deadline that covers dial, TLS, request write, time-to-first-byte and the bounded body read. `H` reserves dial/TLS/response-header setup inside that network window; `P` reserves CPU-local outcome classification, header/body redaction/truncation and durable completion after the response. The loader requires the full `D + H + P + S` reservation even though `D` is the active HTTP context deadline.

**Pre-HTTP budget guard (B4, implemented).** Before starting any request the Worker computes `remaining = dispatch_lease_expires_at - db_now` from freshly locked state and refuses to start when `remaining < D + H + P + S`; the executor rechecks the same budget. Refusal is a pre-HTTP abort: no attempt row, no `attempt_count`, no retry row; the lease is released by fence/lease semantics. The implementation is in `delivery.Store.PrepareHTTP` and `worker.Executor.Execute`; behavioural verification remains pending.

**Cancellation and partitions.** The HTTP context is a child of the worker context with the `D` deadline; a partition surfaces as a deadline error before `D`. Lease expiry never implies the original worker stopped: an expired lease only permits re-claim, and the late worker's write is fenced. The design intentionally keeps the fixed lease and adds no renewal loop.

### 6.2 Throughput coupling

`W` and prefetch are one knob: three instances x 24 concurrent = 72 in-flight requests, which at p95 1 s yields about 72 deliveries/s, above the assumed 25 events/s x 2.5 = 63 deliveries/s peak. Prefetch equals `W` so a consumer never holds more messages than it has slots; the Worker pgx pool defaults to 30 connections and validation requires `POOL_SIZE >= CONCURRENCY`. This is implemented configuration; the capacity figures remain assumptions, not measurements.

### 6.3 Retry values (Rev 3 proposal, implemented Bundle 1 behavior)

| Item | Implemented value |
| --- | --- |
| Maximum attempts | 8 (`HOOKFLOW_WORKER_MAX_ATTEMPTS`, existing required) |
| Ladder | 10 s, 30 s, 2 min, 5 min, 15 min, 1 h, 4 h, 12 h |
| Jitter | full jitter over `[50%, 100%]` of the step, derived from the delivery id (deterministic per attempt, no extra config) |
| `Retry-After` | honoured on 429 up to `min(value, 1 h)` |
| Maximum delay | 12 h; cumulative elapsed about 17.4 h across eight attempts |
| Decryption-failure pacing | 30 s base with 0-20% deterministic jitter from the delivery id (existing field holds the base) |

The §5.4 illustrative ladder is now the implemented Bundle 1 behavior. The historical U24 register row is not rewritten here. The current executor classifies 2xx as success, 408/429/5xx as retryable and other statuses as terminal; tests and production activation remain verification gates.

### 6.4 Reversibility

All numbers are configuration except the pre-HTTP guard, which is code. Lowering `L` below `D+H+P+S` must be rejected by validation; raising `W` requires pool sizing, so it is reversible only as a paired change.

---

## 7. Decision group D5 - Recovery budgets (RB-3/B2)

**Status:** owner-approved after Rev 3 and implemented, including the worker-hosted fixed-delay scheduler. Runtime verification is pending.

### 7.1 Numeric profile

| Parameter | Value | Unit | Existing field |
| --- | --- | --- | --- |
| Sweep cadence | 60 | s | `HOOKFLOW_RECOVERY_CADENCE` |
| Total sweep budget | 45 | s | `HOOKFLOW_RECOVERY_TOTAL_DURATION` (required) |
| Expire pass | 12 s / 500 candidates | s / rows | duration + `..._EXPIRE_MAX_CANDIDATES` |
| Repair pass | 10 s / 250 candidates | s / rows | duration + `..._REPAIR_MAX_CANDIDATES` |
| Re-arm pass | 10 s / 250 candidates | s / rows | duration + `..._REARM_MAX_CANDIDATES` |
| Stale-generation pass | 8 s / 100 candidates | s / rows | duration + `..._STALE_MAX_CANDIDATES` |
| Stale-published threshold | 5 | min | `..._STALE_PUBLISHED_AFTER` (required) |
| Cadence vs budget | cadence (60 s) > total (45 s) | - | fixed-delay scheduling, so runs never overlap |

**Invariant check:** `12 + 10 + 10 + 8 = 40 s <= 45 s`. The existing `ValidateDurationBudget` accepts equality; the proposal leaves 5 s of unallocated headroom so a pass that hits its own deadline still finishes inside the total. All arithmetic is integer milliseconds or seconds in `int64`, so the overflow-safe comparison already implemented is unaffected.

### 7.2 Derived behaviour

* **Expiry latency:** worst case `TTL boundary + cadence + expire pass = 0 + 60 + 12 = 72 s` after an event expires; median about 30 s. No delivery may outlive its event by more than 72 s under this budget, and §12.18 precedence still applies.
* **Recovery throughput:** the expire pass can retire up to 500 deliveries per 60 s (8.3/s); repair and re-arm each 250 per 60 s. At the assumed workload the steady-state candidate count is a few rows per minute, so the caps are two orders of magnitude of headroom. A mass expiry event (for example a bulk endpoint deletion followed by TTL) drains at 8.3/s; that is the deliberately conservative first profile, to be raised only after measured passes show headroom.
* **Stale threshold:** 5 min is long enough that a queue backlog or a 30 s worker lease does not look stale. Scheduled Recovery and the Publisher are now wired, so the threshold targets redispatch within one cycle plus the next pass, subject to `next_attempt_at`, pass budgets and contention. A lower value would amplify duplicates under backlog; a higher value would delay recovery. End-to-end timing remains unverified.
* **Contention:** one sweep process, one candidate per short transaction, `SKIP LOCKED`, delivery-before-outbox lock order, `NOWAIT` on the outbox lock; a conflict defers the candidate, it never retries in a loop. The Sweep therefore adds at most four short transactions every few seconds.

### 7.3 Reversibility

Fully reversible configuration, with one caveat: lowering the total below the sum of pass durations is rejected by validation, so a partial rollback must move a pass duration first. Changing the cadence changes only latency, not correctness.

---

## 8. Decision group D6 - Encryption and key custody (U6)

U6 has two independently approvable halves. Approving the format without the custody choice is useful and safe: the envelope is self-describing and provider-agnostic.

**Current status (Phase 6.1, 2026-11-24):** the D6/U25 architecture is **OWNER APPROVED** and the development
implementation is **IMPLEMENTED** — file KEK provider, `hfse1.` envelope codec/AAD builder, encryption/decryption,
Standard Webhooks v1 signing, deployment-mode gate and secret rotation. Production KMS/Vault custody and production
readiness remain **NOT IMPLEMENTED / NOT READY**. The Worker fails closed on nonempty `secret_encrypted`: durable
decrypt-failure handling with fresh-time/fence/expiry checks and deterministic pacing, no HTTP, no attempt row and no
`attempt_count` increment. Empty-secret snapshots send unsigned HTTP only under the explicitly gated development
configuration. The proposal text below is decision history; the current consolidated contract is
[`d6-u25-decision-proposal.md`](./d6-u25-decision-proposal.md) Rev 3, and the notes in §8.1 identify where it
supersedes this historical text.

### 8.1 D6a - Envelope format, AAD and DEK lifecycle (recommended for approval)

> **Reconciliation note (Phase 6.1, 2026-11-24; supersedes the earlier "D6 remains NOT APPROVED" note).** This section
> is the historical Rev 3 D6a proposal and is retained as decision history. The Owner approved the consolidated
> contract in [`d6-u25-decision-proposal.md`](./d6-u25-decision-proposal.md) Rev 3 §4.1–§4.4. Two parts below are
> superseded: (1) the AAD construction, which is internally inconsistent here and is replaced by one exact 64-byte
> binary construction; and (2) the calendar-based retirement and snapshot-rewrap wording, which conflicts with A8
> snapshot immutability and with reference-based accounting. The development runtime implements the consolidated
> contract; production custody remains not implemented.

**Cipher suite v1:** AES-256-GCM, 96-bit random nonce, 128-bit tag. Go's `cipher.NewGCM` uses the standard 12-byte nonce and documents that a nonce must be unique for all time for a given key; the standard library also documents a 2^32 random-nonce bound per key ([crypto/cipher, go1.27.4 docs](https://pkg.go.dev/crypto/cipher#NewGCM), fetched 2026-11-23). The envelope never encrypts more than once under one DEK, so the bound is not approached.

**Why envelope encryption:** the DEK is generated per webhook secret and wrapped by a KEK that never enters the database. AWS's KMS documentation describes exactly this shape: KMS can return a customer data key "encrypted as ciphertext, in plaintext, or both", and plaintext data keys are never stored inside KMS ([AWS KMS concepts](https://docs.aws.amazon.com/kms/latest/developerguide/concepts.html), fetched 2026-11-23). The same shape maps to GCP KMS or Vault Transit without changing the envelope.

**Wire format (stored as `text`, base64url without padding):**

| Field | Size | Meaning |
| --- | --- | --- |
| magic | 3 bytes | `HF1` - format family |
| alg | 1 byte | `1` = AES-256-GCM |
| kid length + kid | 1 + 1..64 bytes | KEK identifier/version; ASCII, no secrets |
| wrap nonce | 12 bytes | nonce for wrapping the DEK |
| wrapped DEK | 48 bytes | 32-byte DEK sealed under the KEK (ciphertext+tag) |
| wrap tag included above | - | GCM tag under KEK |
| data nonce | 12 bytes | nonce for the secret payload |
| ciphertext + tag | len(secret)+16 bytes | sealed webhook secret |

**AAD construction:** `AAD = "hookflow.secret.v1" || header_prefix || organization_id (16 bytes) || endpoint_id (16 bytes)`, where `header_prefix` is every byte from `magic` through `wrapped DEK`. Consequences:

* Every header field (including `kid` and `alg`) is authenticated, closing algorithm-confusion and key-substitution attacks.
* The tenant binding uses the **stored `uuid` values**, rendered canonically (lowercase text) as §12.15 already requires for AAD built from ids.
* `deliveries.organization_id` and `deliveries.endpoint_id` are immutable snapshot/identity columns (composite FK to `endpoints`, schema §3.1), so decryption never depends on a mutable subscription pointer. `endpoint_subscription_id` may become NULL (F3-C) and is deliberately **not** in the AAD. The URL is also not in the AAD: URL edits do not strand old ciphertext, and endpoint reassignment is already blocked by the endpoint id.
* Cross-organization substitution fails on the organization id; transplanting a secret between endpoints in the same organization fails on the endpoint id.

**AAD reconciliation (Phase 6.1, 2026-11-24).** The historical text above is internally inconsistent: the formula
writes `organization_id (16 bytes)` and `endpoint_id (16 bytes)`, while the next bullet says the values are rendered
canonically as lowercase text. The development runtime now implements the approved 64-byte form from
`d6-u25-decision-proposal.md` §4.3 (D6-B, Owner-approved): `"hookflow.secret.v1"` (18 ASCII bytes) ‖ `alg` (1) ‖
`purpose` (1) ‖ `data_nonce` (12) ‖ raw organization UUID (16) ‖ raw endpoint UUID (16). It deliberately excludes the
header prefix/key slot so that rewrapping an endpoint DEK under a rotated KEK does not break the payload tag. The
earlier "no AAD builder or envelope codec exists" statement is retained as pre-implementation history. An
alternative 36-byte lowercase UUID text would make the AAD 104 bytes and is not the approved form.

**DEK lifecycle:** 32 random bytes per secret version from `crypto/rand`; wrapped by the KEK; plaintext exists only in memory for the duration of one encrypt or decrypt; zeroized after use where Go permits (byte slices overwritten, no copies retained); never logged, never a column, never a fixture. Rotation of a webhook secret creates a new DEK and a new envelope; old delivered snapshots keep their own copy and remain decryptable under their old `kid`.

**Rotation and revocation:**

* **KEK rotation:** rewrap DEKs (envelope text changes, ciphertext payload does not) on the endpoint path; delivered snapshots can be rewrapped by a bounded background job, but they do not need to be, because the old `kid` stays decryptable during the overlap window.
* **Retiring a KEK:** disable wrapping under it first, keep decryption enabled for at least the event retention window (90 days) or until a verified rewrap pass completes. Removing decryption for a `kid` still referenced by live deliveries would permanently break them (they would pace until event expiry), so revocation is a two-phase, audited operation.
* **Compromised key:** disable wrap, rewrap all live ciphertexts under a new KEK, then remove the old `kid`; this is an incident procedure, not a routine.

**Retirement reconciliation (2026-11-24).** The rotation and retirement bullets above are superseded **as proposals**. The governing rule is reference-based, not calendar-based:

* Every persisted envelope that may still require decryption is a **KEK reference**. That includes every `endpoints.secret_encrypted` row, **including a soft-deleted endpoint row** (`deleted_at IS NOT NULL`), unless its ciphertext has actually been purged, and every `deliveries.secret_encrypted` snapshot.
* Endpoint envelopes **may** be rewrapped under a new KEK; delivery snapshots **must not** be rewrapped, because `url`, `http_headers`, and `secret_encrypted` are immutable after acceptance under A8. Only purge of the delivery row removes a snapshot reference.
* The 90-day events/deliveries window is a retention **horizon**, not permission to delete a KEK. A version remains decryptable while any reference exists, and retirement requires all dependent ciphertext purged **and** a recorded reference audit at zero; an unparseable value is treated as a reference, not skipped.
* The current repository cannot satisfy that precondition. The Phase 6 management API can soft-delete an endpoint
  (`deleted_at`), but no endpoint purge exists; `endpoints` is outside the retention table, and soft-deleted rows keep
  their secret envelope. There is also no Events/Deliveries/Attempts purge — only the U43 idempotency sweep is wired.
  **KEK retirement is therefore operationally blocked** until the cleanup/reference-accounting capability exists. Test
  cleanup is not production behavior.

This note records the Owner-approved D6-D contract; production retirement tooling remains **not implemented**. The
historical snapshot-rewrap allowance and the "90 days or until a verified rewrap pass" formulation must not be used
as an operating rule.

**Failure classification (feeds B5 unchanged):**

| Failure | Class | Handling |
| --- | --- | --- |
| KEK provider unreachable/timeout | transient | B5 path: no attempt row, no count, paced retry, scrubbed diagnostic |
| Unknown `kid` (retired/revoked) | permanent | same B5 mechanical path, paced; alert; delivery reaches event expiry if never repaired. No silent failure to `failed` state. |
| Malformed envelope / base64 / length | permanent | same path; diagnostic contains only a bounded reason code, never bytes |
| Authentication failure (GCM open error) | permanent (tamper or AAD mismatch) | same path; treated as security signal in logs |
| Event already expired | expiry precedence | `expired` disposition wins before any retry decision, as approved |

The mechanical B5 rules are preserved verbatim: fence first, fresh DB time after locking, event horizon before worker lease, no attempt row or count change, ACK only after durable commit.

**Reversibility:** the format is not reversible after data exists without a rewrap migration. That is why it is a separate approval and why the algorithm byte exists from v1.

### 8.2 D6b - Key custody and provider (requires additional information)

| Option | Fit | Assessment |
| --- | --- | --- |
| Local file KEK (32-byte key, path in env, 0600) | Development and tests only | Recommended for dev/test; never accepted when the process is in production mode |
| AWS KMS (wrap/unwrap via GenerateDataKey/Decrypt) | Production on AWS | Recommended if the deployment target is AWS. KMS never stores plaintext data keys. |
| GCP KMS | Production on GCP | Same envelope, different provider implementation |
| HashiCorp Vault Transit | Self-hosted or multi-cloud | Same envelope; adds Vault as a runtime dependency |
| Env-var KEK | None | Rejected: process environment leaks into crash dumps and child processes; violates the "never a log field or fixture" rule |

**What is missing:** the production deployment target (and therefore the KMS provider) has not been chosen. The provider interface can be approved now:

```text
Wrap(ctx, plaintextDEK []byte) (wrapped []byte, kid string, err error)
Unwrap(ctx, kid string, wrapped []byte) (plaintextDEK []byte, err error)
```

Provider selection is configuration; startup must fail closed when production mode has no provider, mirroring the existing "no invented defaults" rule. Approval requested: the interface and the dev-file provider now; the production provider once the target is named.

### 8.3 Secret-exposure controls (part of D6a)

The sealed secret and DEK never appear in logs, metrics labels, errors, test names, fixtures, or the Git tree. Diagnostics reuse the existing 500-character, rune-safe, scrubbed `last_error` machinery. Local development keys are generated into an ignored file with restrictive permissions; tests generate ephemeral keys and assert that a tampered ciphertext, a swapped organization id, and a swapped endpoint id all fail authentication.

---

## 9. Decision group D7 - CI and integration-test infrastructure (U30 plus U34)

**Status:** owner-authorized D7 and U34 service-container CI are implemented in `.github/workflows/ci.yml` with `.golangci.yml`. Workflow execution is **verification pending**; lint remains advisory with `continue-on-error: true`. Govulncheck is not enabled because no exact tool/action pin was verified for this change.

| Check | Runs on | Implemented content |
| --- | --- | --- |
| Format | every change | `gofmt -l .` must be empty |
| Vet/build | every change | `go vet ./...`, `go build ./...` |
| Unit tests | every change | `go test ./... -count=1` |
| Dependency verification | every change | `go mod verify`; `go mod tidy -diff` (verified non-mutating on Go 1.27.4) |
| Integration | every change, separate matrix job | PostgreSQL 18 service container pinned to the `compose.yaml` digest; RabbitMQ 4.4.1/4.3.8 management-alpine images pinned by supplied multi-arch digests; migrate up; `go test -tags=integration ./... -count=1 -p 1`; migrate down 13; migrate up all 13; print version |
| Race | every change on Ubuntu | `CGO_ENABLED=1 go test -race ./... -count=1` |
| Lint | every change, **advisory** | golangci-lint v2.15.0 via `golangci/golangci-lint-action` v9.3.0; `.golangci.yml` includes core analysis, bounded complexity, `depguard` layering and the `platformhttp` alias |
| Actions | all jobs | Full commit pins verified through the official GitHub API on 2026-11-24: checkout v7.0.1 `3d3c42e5aac5ba805825da76410c181273ba90b1`; setup-go v7.1.0 `d8cbb8045d7f43706b9177cceb36c1f91a6644d5`; golangci-lint-action v9.3.0 `ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a` |

**U34 implementation:** CI uses GitHub-managed PostgreSQL/RabbitMQ service containers rather than testcontainers, adding no Go dependency. The PostgreSQL digest matches `compose.yaml`; local Compose remains the developer environment. Service credentials are disposable CI-only environment values and are never echoed.

**Branch protection:** not changed by D7. Required-check policy, no-force-push rules, linear history and review requirements remain repository-administration work.

**D7 boundary:** no production Go code, existing test, compose file, `.env.example`, script, migration or module manifest is changed. The workflow and lint configuration are documentation/CI changes only; their first real run remains pending.

---

## 10. Cross-decision consistency review (Stage C)

| # | Check | Result |
| --- | --- | --- |
| 1 | Publisher lease vs worker lease | Independent: 30 s each, different tables and fences. No shared budget, no shared token. **Consistent.** |
| 2 | Publisher backoff vs scheduling authority | `B(c)` only moves `outbox_messages.available_at` via the approved `GREATEST(A, t, N)` gates; it never writes `deliveries.next_attempt_at`. **Consistent.** |
| 3 | Recovery sole re-arming owner | Publisher never claims `failed`; the re-arm pass is the only writer that moves `failed -> pending`. Retry rows are created by the worker only for `retrying` outcomes. **Consistent.** |
| 4 | Prefetch/concurrency vs lease budget | Prefetch = W = 24, so no slot waits behind more than one batch of prefetched work; the 30 s lease starts at claim and the guard requires 25 s remaining. QoS uses per-consumer `global=false`: 4.3.8/4.4.1 accepted `global=true` but behaved per-consumer/ignored it. **Consistent.** |
| 5 | Broker redelivery vs DB fencing | Redelivery is another attempt to acquire; the generation/token fence decides. Stale messages are ACK-discarded. `reject(requeue=true)` increments the quorum delivery count; exceeding 20 can dead-letter the pointer. Recovery does not recreate or re-arm published N: its stale pass inserts N+1 only when the persisted-state predicates hold (§4.5). **Consistent.** |
| 6 | Confirms vs silent loss | T2 commits only after a positive confirm and the bounded protocol's post-confirm return drain finds no matching return. Nack, close, loss, cancellation and listener anomalies fail closed; an unobserved confirm is never read as non-acceptance. Duplicates are possible; unroutable publishes are not marked `published`. **Consistent**, subject to the stage-3/4 implementation gates. |
| 7 | Recovery expiry vs active worker leases | §12.18 precedence is preserved; the expire pass has no live-lease filter; the late worker is fenced. **Consistent.** |
| 8 | Late worker Complete after expiry | Rejected by fence; no attempt, no mutation, no revival. Already tested (L-1). **Consistent.** |
| 9 | Decryption failure vs B5 | D6 reuses B5 unchanged and only adds failure classification; no attempt row, no count, fresh time, horizon first. **Consistent.** |
| 10 | Encryption rotation vs scheduled deliveries | Delivered snapshots are self-contained envelopes with their own wrapped DEK. **The reconciliation supersedes this row's original calendar wording:** endpoint envelopes may be rewrapped, delivery snapshots are not (A8), and a KEK stays decryptable while any retained endpoint envelope or snapshot references it — not merely for a fixed calendar window. D6-D is Owner-approved; production retirement tooling is not implemented. Scheduled deliveries are unaffected. |
| 11 | CI reproduces DB and broker behaviour | Integration uses the same PostgreSQL digest and `cmd/migrate` path as development, plus pinned RabbitMQ 4.4.1 primary/4.3.8 floor service containers and down-13/up proof. Race runs on Ubuntu where CGO exists. **Configured; execution pending**, with lint advisory. |
| 12 | Activation with missing parameters | Required DB/RabbitMQ values and all cross-field invariants fail closed; owner-approved runtime defaults apply when optional values are unset. Empty/whitespace optional runtime values fail rather than silently defaulting. **Consistent.** |
| 13 | No `delivery.cancel`/`cancelled` reintroduction | No proposed state, action, column, message or metric uses them. Expiry and terminal failure cover the same ground. **Consistent.** |
| 14 | No high-cardinality labels | Labels stay outcome/state/queue/error-class; entity ids remain log fields only, per RB-6. **Consistent.** |

**Hidden couplings made explicit:**

* Publisher confirm-wait timeout is bounded by the lease; lease, batch, in-flight window, listener capacity and confirm policy move together.
* Publisher channel window N and channel-pool size require a confirm-latency benchmark; D3's 400 msg/s target is not proof by itself.
* Worker concurrency, prefetch and pgx pool size move together.
* Recovery cadence must exceed the total budget (fixed-delay scheduling) or runs overlap and duplicate candidates.
* Retry ladder and max attempts move together (eight attempts is the ladder length).
* For the unleased stale-published branch, the stale threshold must exceed the worker lease plus one sweep cadence; the expired-lease branch is governed by lease expiry instead (§4.5).
* Encryption `kid` retention must be at least the 90-day event retention, or live deliveries become undecryptable.
* The broker delivery limit (20) and the DB attempt ladder (8) are independent backstops; neither schedules the other, and a DLQ copy is telemetry, never work.
* A DLX configured by policy is deployment state, not code; startup must assert the policy exists or dead-letters are silently dropped.

---

## 11. Runtime activation matrix (Stage F)

| Component | Minimum prerequisites | Current behaviour | Safe in development now? | Production gate |
| --- | --- | --- | --- | --- |
| `cmd/migrate` | PostgreSQL 18.7 | Works (up/down/N/version) | Yes | Schema version and tooling verified |
| `cmd/api` | PostgreSQL | Runs the health server plus the authenticated `/v1` management routes and the Phase 7 `POST /v1/events` ingestion route | Yes, in the development runtime | Rate limits (U32) and production deployment controls remain out of scope of this package |
| Recovery Sweep | PostgreSQL only (writes outbox rows, no broker call) | Implemented and scheduled by every Worker immediately then at fixed cadence; bounded four-pass invocation | Yes, in the Bundle 1 development runtime | D5 profile implemented; runtime/end-to-end DLX verification pending |
| `cmd/outbox-publisher` | PG + D1a/D1b + D2b-D2d + D3 values | Implemented N=1 confirm/return Publisher with T1/T2/T3 and reconnect; startup fails closed on broker/topology/policy errors | Yes, development | Broker matrix, failure-path tests and capacity benchmark pending |
| `cmd/worker` (consumer) | PG + broker + D1a/D1b + D2b-D2d + D4 values + pre-HTTP guard + HTTP executor | Implemented consumer, retry ladder, Recovery and idempotency sweep. Nonempty `secret_encrypted` fails closed without HTTP; empty-secret snapshots send unsigned HTTP only under the gated development configuration. The pre-implementation version of this row ("no deployment-mode gate exists; D6-F proposed, NOT APPROVED") is retained as history. | Unsuitable where unsigned delivery is unacceptable; ensure the development gate is off outside development | D6/U25 production custody, HA/SSRF controls and stage-7 acceptance remain blockers |

Fail-closed rule: activation is not a new flag. Each component validates required configuration and startup topology/policy; absence or mismatch exits. Optional runtime values use owner-approved defaults only when unset. The database remains authoritative in every row of the matrix.

---

## 12. External evidence and open evidence gaps

### 12.1 Verified

| Claim | Source | Date |
| --- | --- | --- |
| amqp091-go v1.17.0 is the newest release and was published 2026-11-17; BSD-2-Clause; module `go 1.20`; supports the two most recent Go series; confirm/return/recovery APIs; exported `DeferredConfirmation.DeliveryTag`; no cross-channel `basic.return`/`basic.ack` ordering guarantee; channel close nacks pending deferred confirmations | https://github.com/rabbitmq/amqp091-go/releases/tag/v1.17.0, pkg.go.dev, v1.17.0 `types.go`, `confirms.go`, `channel.go`, `connection.go` and README | released 2026-11-17; release checked 2026-11-23; protocol source checked 2026-11-24 |
| RabbitMQ 4.3.8 and 4.4.1 released 2026-11-16; both minimum Erlang 27.0; community support ends 2027-01-31 and 2027-07-31 | https://www.rabbitmq.com/release-information and https://github.com/rabbitmq/rabbitmq-server/releases/tag/v4.3.8 | fetched 2026-11-23 |
| Images `4.4.1-management-alpine`, `4.3.8-management-alpine`, `4.3.8-alpine` exist | Docker Hub API tag endpoint | pushed 2026-11-17; checked 2026-11-23 |
| In RabbitMQ 4.3, `basic.reject` increments acquired and delivery counts, `basic.nack` only acquired; client/connection or channel/session failure increments delivery count; partition and consumer timeout do not; default limit 20; threshold is "more times than the limit"; cancelling a consumer does not requeue in-flight messages, closing the channel does | https://www.rabbitmq.com/docs/4.3/quorum-queues, https://www.rabbitmq.com/docs/4.3/nack, https://www.rabbitmq.com/docs/4.3/consumers | verified 2026-11-24 |
| Dead-lettering occurs on reject/nack(requeue=false), per-message TTL, max-length drop and quorum delivery-limit; default at-most-once republishing has no internal confirms and removes the source after publishing; missing DLX silently drops; at-least-once requires `dead-letter-strategy=at-least-once`, `overflow=reject-publish`, a DLX and `stream_queue` | https://www.rabbitmq.com/docs/4.3/dlx and https://www.rabbitmq.com/docs/4.3/quorum-queues | fetched 2026-11-24 |
| Mandatory unroutable publishes get `basic.return` before `basic.ack`; the ack does not mean a queue accepted the message; a broker `basic.nack` means an internal queue-process error; confirms are asynchronous and may arrive out of order | https://www.rabbitmq.com/docs/4.3/confirms | fetched 2026-11-24 |
| amqp091-go v1.17.0 dispatches same-channel frames synchronously and enqueues a return before handling its confirm; `NotifyReturn` uses `notifyAll`, which abandons a full-listener send after five seconds; return listeners close during cleanup; `NotifyPublish` requires capacity >= outstanding publishes | v1.17.0 `connection.go` (`reader`/`demux`), `channel.go` (`notifyAll`, `dispatch`, `NotifyReturn`, `NotifyPublish`, `closeResources`), `channel_test.go` full-listener test | source checked 2026-11-24 |
| PostgreSQL default unique index treats NULLs as distinct; multicolumn equality rules | https://www.postgresql.org/docs/current/indexes-unique.html (§11.6) | verified 2026-11-22 |
| KMS returns customer data keys encrypted, in plaintext, or both; plaintext never stored in KMS | https://docs.aws.amazon.com/kms/latest/developerguide/concepts.html | fetched 2026-11-23 |
| `cipher.NewGCM` uses the standard 12-byte nonce; nonce must be unique per key; 2^32 random-nonce warning | https://pkg.go.dev/crypto/cipher#NewGCM (go1.27.4 docs) | fetched 2026-11-23 |
| golangci-lint v2.15.0 release exists | https://github.com/golangci/golangci-lint/releases/tag/v2.15.0 | released 2026-10-16 |

### 12.2 Unverified / blocked

| Item | Why it matters | Handling |
| --- | --- | --- |
| golangci-lint v2.15.0 analysis of the Go 1.27.4 codebase | v2.15.0 and its config schema are available locally, but no lint analysis was run by D7. | Advisory CI with `continue-on-error: true`; first run pending. Do not claim lint coverage until it succeeds. |
| Exact multi-arch manifest digest for RabbitMQ 4.4.1 and 4.3.8 | Rev 3 had only per-architecture digests. | **Resolved for CI:** supplied multi-arch digests are pinned in `.github/workflows/ci.yml`; image pull/matrix verification pending. |
| Go 1.27.4 build and vet of amqp091-go v1.17.0 | The pin and runtime exist, but D7 did not execute build/vet. | Pending in the quality job; no pass is claimed. |
| Full RabbitMQ 4.4.1/4.3.8 protocol matrix (AMQP 0-9-1, quorum, DLX, confirms) | CI now provides both pinned service images, but no matrix run has occurred. | Stage-3 gate per §3.3 remains verification-pending; 4.3.8 is the recorded floor/fallback. |
| Publisher confirm/return correlator | `internal/app/publisher/session.go` now implements the N=1 generation-owned correlator. | Implementation exists; bounded-window, confirm-first race, close/loss/cancel, listener and no-premature-T2 verification remain pending. |
| Recovery scheduling and end-to-end DLX redispatch | `internal/app/recovery/scheduler.go` is wired into `cmd/worker`, and the Publisher loop exists. | Run a real delivery-limit/DLX-to-N+1-to-publish test before activation; implementation is not test evidence. |
| D3 Publisher throughput | 400 msg/s is a planning target; N=1 confirm latency has not been measured. | Benchmark the implemented N=1 session on 4.4.1 and 4.3.8. Owner approval of the profile does not validate capacity. |
| Docker Hub image age/security posture | Not assessed here | Operational scan at adoption |
| Race detector behaviour on this Windows host | Environment limitation; never run | Moved to CI (`ubuntu-latest`) in D7 |
| U21's approved external text | The "minimal durable quorum-queue requirements" were approved outside the repository; §13 remains Open | **Evidence gap.** D2b-D2d are compatible with a durable quorum queue plus DLX but are not presented as that text. If the owner wants the external text to govern, it must be recorded in-repo first. |
| Workload figures | No measurement exists | Explicit assumptions in §5.3 with a validation plan |

### 12.3 Bundle 1 evidence ledger

| Claim | Official source | Exact version/date | What the source proves | Direct or inference |
| --- | --- | --- | --- | --- |
| amqp091-go v1.17.0 is the latest release | https://github.com/rabbitmq/amqp091-go/releases | checked 2026-11-23 | No newer tag exists on the releases page | Direct |
| Client supports Go 1.27 | https://github.com/rabbitmq/amqp091-go#requirements | v1.17.0 README | Policy covers the two most recent Go series, which are 1.26 and 1.27 on the cut-off date | Direct for policy; inference for this exact 1.27.4 build |
| Client works with RabbitMQ 4.3.x/4.4.x | Same README | v1.17.0 | Supports 2.0+ and is primarily tested against currently supported series; both 4.3 and 4.4 are in support | Direct for policy; inference for the matrix (stage 3) |
| `Wait` false is ambiguous | v1.17.0 `confirms.go` and pkg.go.dev | v1.17.0 | Channel close nacks pending deferred confirmations; publish error removes the tag with `ack=false` | Direct |
| Publisher tags and return correlation | v1.17.0 `types.go`, `confirms.go`, `return.go` | v1.17.0 | `DeferredConfirmation.DeliveryTag` and `Confirmation.DeliveryTag` are exported; `Return` exposes properties/routing data but no publisher sequence number, so the two identifiers require separate maps | Direct |
| No cross-channel event-order guarantee | README "Non-goals" | v1.17.0 | The client does not order events delivered to separate Go channels; application receive order cannot be assumed | Direct |
| Same-channel return-before-confirm dispatch | v1.17.0 `connection.go` and `channel.go` | v1.17.0 | The reader is synchronous; `dispatch` enqueues `basic.return` before `confirms.One/Multiple` handles `basic.ack`/`basic.nack` | Direct |
| Return notifications can be dropped on a full listener | v1.17.0 `channel.go` `notifyAll`; `channel_test.go` `TestBasicReturnDispatchDropsNotificationOnFullListener` | v1.17.0 | A send blocked for the five-second notify timeout is abandoned | Direct |
| Safe absence-of-return proof | RabbitMQ 4.3 confirms + v1.17.0 `types.go`, `connection.go`, `channel.go` and `confirms.go` | 4.3 / v1.17.0 | Exported delivery tags support the confirm map; under return capacity >= N and one owner, post-confirm drain observes a return enqueued before the confirm; no timer is needed | Inference from direct protocol/source facts; implementation test still required |
| License and active maintenance | LICENSE, pkg.go.dev, release cadence | v1.17.0, 2026 releases | BSD-2-Clause; RabbitMQ core team; roughly monthly releases through 2026 | Direct |
| 4.3.8 is the latest 4.3 patch | https://www.rabbitmq.com/release-information | 4.3.8, 2026-11-16 | 4.3.6 (16 Sept 2026), 4.3.7 (20 Oct 2026) and 4.3.8 exist; 4.3.5 is not latest | Direct |
| Support lifecycles | Same page | fetched 2026-11-23 | 4.3 community support ends 2027-01-31; 4.4 community support ends 2027-07-31 | Direct |
| Erlang floor | https://github.com/rabbitmq/rabbitmq-server/releases/tag/v4.3.8 and the 4.4.1 notes | 2026-11-16 | Both lines require Erlang/OTP 27.0; nodes fail to start on older versions | Direct |
| Docker tags exist | https://hub.docker.com/_/rabbitmq tag API | pushed 2026-11-17 | 4.4.1 and 4.3.8 tags exist; only per-arch digests were captured | Direct; multi-arch digest still open (§12.2) |
| DLX rules | https://www.rabbitmq.com/docs/4.3/dlx | 4.3 docs, fetched 2026-11-24 | Four dead-letter events; at-most-once has no internal confirms and can lose the copy; missing DLX drops; at-least-once prerequisites | Direct |
| Confirm rules | https://www.rabbitmq.com/docs/4.3/confirms | 4.3 docs, fetched 2026-11-24 | Return precedes ack/nack for unroutable mandatory publishes; nack only on internal error; async/out-of-order confirms | Direct |
| Delivery-count accounting | https://www.rabbitmq.com/docs/4.3/quorum-queues and https://www.rabbitmq.com/docs/4.3/nack | 4.3 docs, checked 2026-11-24 | Default 20; threshold exceeds; reject increments delivery count, nack does not; connection/channel failure does, partition/timeout do not | Direct |
| Recovery-after-DLX database path | `internal/app/recovery/sweep.go` `Run`, `staleSelectSQL`, `staleEligible`, `staleCandidate`; `sweep_integration_test.go`; `test/integration/recovery_e2e_integration_test.go` | repository read 2026-11-24 | Fourth pass; leased/unleased predicates; N -> N+1 insert; old N preserved; duplicate prevention; broker E2E test file exists | Direct code/test inspection; E2E execution pending |
| Runtime redispatch implementation | `cmd/worker/lifecycle.go`; `internal/app/recovery/scheduler.go`; `cmd/outbox-publisher/lifecycle.go`; `internal/app/publisher/relay.go` | repository read 2026-11-24 | Recovery scheduler, Worker consumer and Publisher loop are wired | Direct code inspection; end-to-end broker verification remains pending |

---

## 13. Recommended implementation roadmap (Stage D)

The order below is dependency-driven, not a restatement of the task's suggested list. CI moves earlier than "last" because every later stage needs a reproducible integration environment; HTTP execution moves after the consumer loop because signing (U25) and the B4 guard must land together.

**Current mapping (2026-11-24, Phase 6.1):** stages 1, 3, 4, 5 and 6 are implemented in the development runtime, and stage 7's Recovery scheduling, optional metrics and shutdown behavior are implemented; the D6/U25 architecture is Owner-approved. Stage 6's encryption/decryption and U25 signing run under the development file KEK; production KMS/Vault custody, production signing-key rotation beyond the keyring allowlist and production activation are not implemented. The roadmap remains historical plan context; current status is §2.3/§11 and the Phase 6.1 closure report.

| Stage | Prerequisites | Scope | Likely files/packages | Required tests | Acceptance | Rollback boundary | Excluded |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0. Approval and record | This package | Owner decisions recorded in a new §12 entry; isolated Go 1.27.4 build/vet probe of amqp091-go v1.17.0 without changing repository dependencies; no contract edits | `docs/architecture/overview.md` (approval history only) | doc consistency; isolated dependency probe | Owner signs the bundle; build/vet probe passes before the client pin | Delete the new entry | Any behaviour change |
| 1. CI and integration baseline | D7, U34 | Workflows, `.golangci.yml`, compose RabbitMQ services (4.4.1 primary, 4.3.8 floor), integration job with PG + broker | `.github/workflows/`, compose, scripts | pipeline runs on current code | format/vet/build/unit/integration green; race green on Linux; lint advisory result recorded | Remove workflow files | Application code |
| 2. Encryption package | D6a | Envelope codec, AAD builder, dev file provider, provider interface | `internal/platform/crypto` (new), `internal/app/secret` (new) | golden vectors, tamper, cross-org, cross-endpoint, rotation/rewrap, malformed input, no secret in errors | all vectors and negative tests pass; fuzz target for the decoder | Package is additive; delete it | Endpoint service wiring, KMS |
| 3. Broker client and topology | D1a, D1b, D2b | Connection/recovery wrapper, declarations, bounded confirm publisher, return correlator, consumer helper | `internal/platform/messaging` (placeholder today), compose, deployments | declare/verify, reconnect, bounded window, confirm-first/queued-return race, out-of-order/multiple confirms, listener capacity, timeout/cancel/close/loss, mandatory return, consumer cancel/close, delivery-limit on a throwaway queue | all §3.3 items pass on 4.4.1; 4.3.8 floor check passes; fallback recorded | Wrapper isolated; revert compose service | Publisher/worker loops |
| 4. Publisher runtime | D3, stage 3 | Poll loop, batch/rounds, bounded channel window/pool, confirm/return decisions, T2 outcomes, reaper ticker, new config fields | `cmd/outbox-publisher`, `internal/app/outbox`, `internal/platform/config` | idle/full batch, no T2 before post-confirm drain, return overrides confirm, unknown-outcome generation reset, broker down, crash-after-publish, lease reap, backoff boundary, N/pool latency benchmark | pending -> published; kill -9 duplicate test passes; 400 msg/s target validated or D3 re-tuned; no premature `MarkPublished` | Binary still exits on missing config | Worker consumption |
| 5. Worker consumer | D4 (lease/retry), D2c, D2d, stages 3-4 | Consume loop, generation fence, duplicate discard, graceful close, ACK after commit, transient `reject(requeue=true)` | `internal/app/delivery`, `cmd/worker` | stale generation, already-terminal, shutdown requeue, reject on DB outage, repeated reject to delivery-limit/DLX, then scheduled Recovery creates N+1 and Publisher redispatches | duplicate/stale messages never trigger HTTP; close requeues; DLQ is not replayed; eligible DB Delivery reaches N+1 and is published | Worker remains behind its enable gate | HTTP, decryption |
| 6. HTTP execution and signing | D4 numbers, D6, U24, U25 | HTTP client with deadlines/body cap, pre-HTTP budget guard, signing, redaction | `internal/app/delivery`, new HTTP package, `internal/platform/http` | budget refusal, slow endpoint, cancellation, 429 `Retry-After`, 4xx terminal, signature vectors | guard provably prevents over-lease start; all outcomes persist correctly | Guard is additive; disable HTTP path | Production activation |
| 7. Activation and hardening | Stages 1-6 | Startup validation, scheduled Recovery wiring, metrics/alerts per RB-6, runbooks, reaping/shutdown/DLX drills | binaries, deployments | fail-closed matrix, outage drill, shutdown drill, sweep-age alert, manual DLQ inspection/drain, real DLX-to-Recovery redispatch | matrix in §11 all green; alert on DLQ growth and sweep age; no automatic DLQ replay | Disable components individually | Schema changes |
| 8. Race and CI hardening | Stage 1 | Nightly race/integration, govulncheck pin, branch protection | workflows | nightly green | protection enabled | Revert protection | Lint coverage claim while §12.2 blocks it |

Each stage was scoped as a separate reviewable change set. Rev 3 itself authorized no implementation; subsequent owner approval and the D7 task authorization are recorded in the reconciliation above. No production activation is authorized.

---

## 14. Owner decision bundle

The package is deliberately split so that independent decisions can be approved without waiting for the others.

### Bundle 1 - Broker runtime (five checkboxes; D2a informational)

D2a is already approved and appears only so the bundle is complete. It is not a request and must not be sent to the owner for re-approval.

| Item | Status | What approval would mean | Independence / grouping |
| --- | --- | --- | --- |
| D1a client `amqp091-go` v1.17.0 | **Owner-approved and implemented**; build/vet verification pending | Exact pin in `go.mod`, no wrapper | CI matrix couples D1a/D1b; run pending |
| D1b broker 4.4.1, image `rabbitmq:4.4.1-management-alpine`, multi-arch digest pinned in CI; 4.3.8 floor/fallback | **Owner-approved and implemented**; §3.3 matrix pending | Exact deployment version/image; B6 floor unchanged | Testing coupled to D1a |
| D2a U21 minimum | Already approved (§12.16, external text) | Nothing; not reopened | - |
| D2b exchange/queue/routing/message contract | **Owner-approved and implemented**; stage-3/4 evidence pending | Names, topic exchange, quorum work queue, identifiers-only body, mandatory+confirm, N=1 channel-generation return/confirm protocol, fail-closed declarations | Coupled to D2c/D2d |
| D2c shared queue V1, no sharding, fairness deferred | **Owner-approved and implemented** | One shared queue; per-organization/per-endpoint queues and sharding remain unapproved | Coupled to D2b |
| D2d DLX/DLQ target, at-most-once, delivery limit 20, no broker-side delayed retry scheduler | **Owner-approved and implemented**; broker matrix pending | DLX/DLQ, telemetry-only guarantee, 20 as backstop, DB-owned retry scheduling and N+1 Recovery for eligible rows; possible DLQ-copy loss accepted | Policy-level; no replay |

**Smallest coherent request.** D1a + D1b + D2b let stage 3 start: the client compiles, the broker image runs, and the declare/confirm/return contract exists. D2c is one topology constraint that can travel with D2b. D2d matters at stages 5 and 7 (DLQ monitoring, at-most-once choice) and can be approved separately; deferring D2d must not be read as dropping the DLX, because U21 already requires one.

**Rev 3 request boundary (historical).** Re-approval of U21, B6, RB-4, B1-B5 or §12.18 was not requested. Rev 3 authorized no activation or code; the subsequent owner approval and implementation are recorded above. Production activation is still not approved.

### Bundle 2 - Numeric runtime profile (approve together)

| Item | Status | Approve |
| --- | --- | --- |
| Publisher numbers (§5) | **Owner-approved and implemented**; capacity benchmark pending | B=200, M=5, poll 500 ms, lease 30 s, reap 10 s, backoff 2 s x2 cap 60 s |
| Worker numbers and guard (§6) | **Owner-approved and implemented** for Bundle 1; runtime verification pending | L=30, D=10, H=2, P=3, S=10, W=24, prefetch=24, pool 30, ladder 8 steps |
| Recovery numbers (§7) | **Owner-approved and implemented**; runtime verification pending | cadence 60 s, total 45 s, passes 12/10/10/8, caps 500/250/250/100, stale 5 min |

### Bundle 3 - Encryption

| Item | Status | Approve |
| --- | --- | --- |
| D6a envelope, AAD, DEK lifecycle, failure classes | **Owner-approved architecture and development-implemented**; historical Rev 3 proposal retained. Current contract: [`d6-u25-decision-proposal.md`](./d6-u25-decision-proposal.md) Rev 3 (D6-A/D6-B/D6-D/D6-E approved 2026-11-24); production custody not implemented | format v1 and the 64-byte AAD are implemented in development; production provider absent |
| D6b provider interface + dev file provider | **Owner-approved architecture (D6-C) and development-implemented**; historical Rev 3 proposal retained; the production provider is deferred | interface and development file provider exist; production adapter does not |
| D6-D reference-based retirement | **Owner-approved architecture; not operationally implemented** — blocked by the missing purge and endpoint reference accounting | no calendar-only retirement; see §8.1 reconciliation note |
| D6b production KMS provider | **Not implemented / Not ready**; requires a deployment target and a separate implementation authorization | name the deployment target first |

### Bundle 4 - CI

| Item | Status | Approve |
| --- | --- | --- |
| D7 CI shape and U34 service-container integration | **Authorized and implemented**; first workflow run pending | `.github/workflows/ci.yml`, `.golangci.yml`, README and operations docs |
| golangci-lint Go 1.27 compatibility | Config schema verified with v2.15.0; **analysis advisory/pending** | no lint-coverage claim until a successful run |

### Bundle 5 - Activation (last)

**Not approved.** Bundle 1, D7, the D6/U25 architecture, the development security runtime and the Phase 7 ingestion API are implemented, but production activation, production KMS/Vault custody, production HA/SSRF controls and stage-7 acceptance are not. Production readiness remains **NOT IMPLEMENTED / NOT READY**, and no fail-closed development behavior is relabelled as production readiness.

---

## 15. Scope and Git safety

This file was created by the Bundle 1 assessment task, modified by the Rev 2 evidence-correction task, and revised by the Rev 3 final protocol-consistency audit. Rev 3 modified only this file. The checks below were executed after the Rev 3 edit; the pre-existing `.gitignore` modification and untracked implementation/infrastructure tree are not Rev 3 changes, and no staging, commit, branch or history operation was performed.

* Files created by Rev 3: none.
* Files modified by Rev 3: `docs/architecture/runtime-readiness-decisions.md` (this file).
* Files deleted or renamed: none.
* No Go source, test, migration, schema, dependency, Docker, compose, script, workflow or configuration change.
* No database connection or mutation was used for this phase. Recovery and Publisher evidence comes from source/test inspection plus official RabbitMQ documentation and amqp091-go v1.17.0 source; no application test was rerun because no code changed.
* `git diff --check`: clean (exit 0) for tracked changes. Because this file is untracked, `git diff --no-index --check /dev/null docs/architecture/runtime-readiness-decisions.md` was also run: no whitespace errors, only Git's LF-to-CRLF normalization warning.
* Document validation: UTF-8 decode passed with no NUL, replacement or C1 control characters; Markdown table column counts are consistent; all 12 local document links resolve.
* No `git add`, `git commit`, `git push`, `git stash`, `git reset`, `git checkout`, `git clean` or branch command was run; the index is empty and `HEAD` remains `c6db32fd50ee8d7beb3108674b4cf46443d2aba4`.

`git status` is reported in the final response of this task; the untracked implementation tree that existed before this task remains untracked, and the Git auditability gap recorded in [overview.md §16.1](overview.md) is unchanged.

### 15.1 D7 implementation reconciliation scope

The 2026-11-24 reconciliation adds current owner-approval/implementation status, corrects §3.3/§10 global-QoS wording, and preserves Rev 3's proposals/history. D7 also adds `.github/workflows/ci.yml`, `.golangci.yml`, `README.md`, `docs/operations/configuration.md` and `docs/operations/runbook.md`; no production Go code, existing test, compose file, `.env.example`, script, migration, schema/ERD or module manifest is changed by this task.

Action SHAs were resolved through the official GitHub API. The `.golangci.yml` schema is checked with golangci-lint v2.15.0; this is configuration validation, not lint analysis. Application build/vet/unit/integration/race/lint runs are **not executed by this reconciliation and remain pending**. No Git add, commit, push, stash, reset, checkout, clean or branch mutation is performed.

**Addendum (2026-11-24, Phase 6, appended):** the §9 `cmd/worker` row's "no deployment-mode gate exists" describes
the pre-implementation state. The current tree implements the development-only `HOOKFLOW_DEPLOYMENT_MODE` matrix
with the unsigned gate, a local file KEK, the `hfse1.` envelope and Standard Webhooks v1 signing, and the Phase 6
management API. **Phase 6.1 correction:** the sentence that every D6/U25 proposal row remains NOT APPROVED was the
Phase 6 record; the Owner approved D6-A…D6-G and U25-A…U25-D on 2026-11-24. The architecture is approved and the
development implementation is implemented; no production provider exists, and Production remains **NOT READY**.
Current evidence is in [overview.md §16.9](overview.md#169-phase-61-verification-evidence-2026-11-24) and the Phase 6.1
closure report.

---

## 16. Sources

* [overview.md](overview.md) - §5.3 dispatch contract, §5.4 retry sketch, §12.16 A1-A10/B1-B6, §12.17 F3, §12.18 expiry precedence, §13 register, §16 implementation status.
* [database-schema.md](database-schema.md) - §4.5.1 secret sketch, §4.8 deliveries snapshot, §5 B2/B3/B5 contracts, §7 implementation status.
* [erd.mmd](erd.mmd) - 13 entities, 17 relationship lines, 157 attribute lines.
* RabbitMQ 4.3 protocol semantics: https://www.rabbitmq.com/docs/4.3/quorum-queues, https://www.rabbitmq.com/docs/4.3/nack, https://www.rabbitmq.com/docs/4.3/consumers (re-verified 2026-11-24).
* RabbitMQ 4.3 dead-lettering and confirms: https://www.rabbitmq.com/docs/4.3/dlx, https://www.rabbitmq.com/docs/4.3/confirms (fetched 2026-11-24). The 4.4 pages at https://www.rabbitmq.com/docs/dlx and https://www.rabbitmq.com/docs/confirms were also checked for D1b parity.
* RabbitMQ support lifecycle: https://www.rabbitmq.com/release-information (fetched 2026-11-23; 4.3 community support ends 2027-01-31, 4.4 ends 2027-07-31).
* RabbitMQ releases: https://github.com/rabbitmq/rabbitmq-server/releases/tag/v4.3.8 (2026-11-16), https://github.com/rabbitmq/rabbitmq-server/releases/tag/v4.4.1 (2026-11-16). 4.3.5 (2026-08-17) is recorded only as the superseded version that Rev 1 wrongly called latest.
* RabbitMQ images: Docker Hub tag API for `rabbitmq`, tags `4.4.1-management-alpine`, `4.3.8-management-alpine`, `4.3.8-alpine` (pushed 2026-11-17, checked 2026-11-23).
* amqp091-go: https://github.com/rabbitmq/amqp091-go/releases/tag/v1.17.0 (2026-11-17), https://pkg.go.dev/github.com/rabbitmq/amqp091-go, README requirements and non-goals; v1.17.0 source inspected 2026-11-24: https://github.com/rabbitmq/amqp091-go/blob/v1.17.0/types.go (`DeferredConfirmation`, `Confirmation`), https://github.com/rabbitmq/amqp091-go/blob/v1.17.0/channel.go (`PublishWithDeferredConfirmWithContext`, `notifyAll`, `dispatch`, `NotifyReturn`, `NotifyPublish`, `closeResources`), https://github.com/rabbitmq/amqp091-go/blob/v1.17.0/connection.go (`reader`, `demux`, `dispatchN`), https://github.com/rabbitmq/amqp091-go/blob/v1.17.0/confirms.go (`One`, `Multiple`, `Close`, `reset`, `WaitContext`), https://github.com/rabbitmq/amqp091-go/blob/v1.17.0/return.go, and https://github.com/rabbitmq/amqp091-go/blob/v1.17.0/channel_test.go (`TestBasicReturnDispatchDropsNotificationOnFullListener`).
* PostgreSQL unique indexes: https://www.postgresql.org/docs/current/indexes-unique.html (§11.6).
* GitHub Actions pin verification (official API, 2026-11-24): https://api.github.com/repos/actions/checkout/commits/v7.0.1, https://api.github.com/repos/actions/setup-go/commits/v7.1.0, https://api.github.com/repos/golangci/golangci-lint-action/commits/v9.3.0.
* Go crypto/cipher: https://pkg.go.dev/crypto/cipher (go1.27.4 docs, fetched 2026-11-23).
* AWS KMS concepts: https://docs.aws.amazon.com/kms/latest/developerguide/concepts.html (fetched 2026-11-23).
* golangci-lint v2.15.0: https://github.com/golangci/golangci-lint/releases/tag/v2.15.0 (2026-10-16); the earlier compatibility gap is superseded by the actual Go 1.27.4 run in §17 (advisory findings remain).
* GitHub Actions versions verified 2026-11-23: actions/checkout v7.0.1, actions/setup-go v7.1.0, golangci/golangci-lint-action v9.3.0.

## 17. Owner-approved Bundle 1 execution evidence (2026-11-24)

This dated subsection supersedes pre-execution pending labels in §2.3, §3.3, §4.8, §11-§14 and §15.1 only for checks actually executed below. It does not rewrite approval history, change Rev 3, or approve another bundle.

D1a/D1b/D2b/D2c/D2d/D3/D4/D5 are implemented and locally verified: exact amqp091-go v1.17.0; RabbitMQ 4.4.1 primary and 4.3.8 compatibility; one durable shared quorum work queue; durable direct-DLX/quorum-DLQ, at-most-once strategy and delivery limit 20; approved numeric profiles; application-managed reconnect; N=1 mandatory confirm/return ownership; exact-pointer fixed-lease Worker; four-pass Recovery scheduler. Bodies still contain exactly five identifiers/version fields and publishers emit exactly four contract headers. Worker parsing permits only RabbitMQ-added x-acquired-count/x-delivery-count in addition to those headers, not extra application data.

Actual Go 1.27.4 evidence: build/vet/format PASS; unit 17/17 packages PASS; PostgreSQL 18.7 plus RabbitMQ 4.4.1 and 4.3.8 integration 18/18 packages per version PASS without skips; Linux CGO race suite 17/17 packages PASS; real blocked-RPC cancellation/teardown tests PASS on both brokers and under the Linux race detector; module verification PASS and tidy diff empty. golangci-lint v2.15.0 ran, reporting 32 advisory complexity/nesting/output-errcheck findings, not a compatibility failure or clean lint pass.

Real failure evidence includes matching mandatory return overriding a positive confirm, UNKNOWN on channel/connection loss and timeout/cancel, fenced T2/T3, duplicate publication with one durable HTTP attempt, pre-T2 live-pointer retention, terminal NULL timestamp decoding, reused numeric tag 1 with stale Handle rejection, active-work shutdown without NACK, actual x-death DLQ inspection, missing DLX route/at-most-once copy loss, and lease-expiry -> Recovery N+1 -> publication -> successful completion. Management telemetry sampling is not treated as immediate queue state.

D7 workflow implementation is complete, including pinned two-broker service images, module checks, CGO race job, advisory lint, disposable down/up steps and a no-skipped-integration gate. **Hosted Actions execution is unverified:** no Git commit/push was permitted. No new schema/migration was needed; schema version remains 13 with dirty=false and unchanged migration hashes. The existing migration integration test calls Up only at already-current version 13 and verifies that it applies nothing. A fresh destructive down/up cycle was not run locally in this execution.

Development runtime evidence covers the implemented development runtime, including Phase 7 ingestion, but not production activation or production readiness. D6/U25, HA/failover/capacity, SSRF/egress and production scrape/alert topology remain unapproved/unimplemented. Historical U24/U30/U34 register labels are not silently closed beyond the Owner's explicitly approved Bundle 1/D7 scope. Git HEAD and staging remain unchanged; implementation files remain untracked until separate Owner authorization.
