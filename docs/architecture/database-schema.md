# HookFlow — Database Schema (V1, 13 tables)

> **Status (documentation revision 18):** design reference for the approved 13-table schema, with the
> implementation status recorded in [overview.md §16](./overview.md).
> Revisions 2–16 remain the design and approval history; the later owner approvals are in overview §12.16
> (A1–A10, B1–B6), §12.17 (F3-A/F3-B/F3-C/F8) and §12.18 (Recovery expiry precedence, 2026-11-23).
> **The migration units below are no longer merely illustrative:** the 13 paired up/down migrations exist in
> `db/migrations/`, were applied to PostgreSQL 18.7 (schema version 13), and match the §4 text, including
> unit 13's `idem_assert_linked()` function and deferred `trg_idem_linked` trigger. **The migration files,
> not this document, are the DDL artifacts of record.** `db/queries/` is still empty and no generated query
> layer exists. The DDL excerpts below remain the design explanation of those files.
> Diagram: [erd.mmd](./erd.mmd).
> **Bundle 1 execution note (2026-11-24, no new schema revision):** Publisher/Worker/Recovery runtime now uses
> this existing schema. PostgreSQL remains 18.7, version 13, dirty=false; all 26 migration-file hashes are unchanged.
> No new table, column, index, constraint, trigger or ERD relationship was required. Local runtime test evidence
> is recorded in overview §16.7; hosted CI's fresh disposable down/up proof remains unexecuted.
>
> **D6/U25 cross-document reconciliation (2026-11-24, documentation-only; no schema change).** The historical
> statement that AAD uses "canonical lowercase UUID text" is retained as history and is **superseded for current
> guidance** by the single 64-byte binary construction in
> [`d6-u25-decision-proposal.md`](./d6-u25-decision-proposal.md) §4.3 (raw 16-byte UUIDs), which the Owner
> **approved on 2026-11-24 (D6-B)**. **Phase 6.1 status correction:** the earlier "NOT APPROVED / not implemented"
> labels in this note were the pre-approval record. D6-A…D6-G and U25-A…U25-D are **OWNER APPROVED** architecture and
> the development implementation is **IMPLEMENTED**; production KMS/Vault custody and production readiness remain
> **NOT IMPLEMENTED / NOT READY**. For key lifecycle, every persisted endpoint envelope — including a soft-deleted
> `endpoints` row whose `secret_encrypted` has not actually been purged — and every immutable delivery snapshot counts
> as a KEK reference. `endpoints` is outside the retention table and no endpoint purge exists; **KEK retirement remains
> operationally blocked** under the approved reference-based rule (D6-D) until purge and reference accounting exist.
> See §4.5, §4.5.1 and §6.
>
> **Revision 2** aligned this document with the canonical agreed inventory and relationships
> (`endpoint_subscriptions`, `organization_subscriptions`, `outbox_messages`). **Revision 3** recorded
> **U5** eager fan-out, **U16** exact-match event types, and **U33** the per-delivery destination
> snapshot.
>
> **Revision 4** records four further approvals: **U36** — `events.status` is **removed** from the
> schema and `events.destination_count` stays as a non-negative integer, with every accepted event
> persisted even when zero endpoints match; **U37** — the active-endpoint ceiling is the platform
> configuration `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG`, default 100, not a database column;
> **U38** — ceiling enforcement takes `SELECT … FOR UPDATE` on the parent `organizations` row inside
> the same transaction as the count and the write; **U39** — an application service/use-case layer
> above the domain modules owns the transaction boundary via a shared Unit of Work.
>
> The 12-table inventory is unchanged at that point. U1 (UUID generation), U3 (PostgreSQL version) and
> U6 (webhook secret storage and rotation) remain **unresolved by instruction**; U4 was still open in
> that revision and was closed in revision 7.
>
> **Revision 5** adds only the approved **U40** location for the U39 layer: `internal/app/` is the
> application service layer and `internal/app/event/` holds the event-acceptance use case. No table,
> column, constraint or index changed in this revision — U40 is a code-layout decision that the schema
> documents only because §4.5.2 and §5 point at the package that must own the locking protocol and the
> Unit of Work.
>
> **Revision 6** is also a code-layout revision only: **U26** fixes the Go module path as
> `github.com/metamin44/hookflow`, so every import and the generated `sqlc` packages referenced in these
> documents live under that path (`go.mod` was not created in that revision; it exists now — overview §16), and **U28** names
> the application-layer package `eventapp` at `internal/app/event` while the domain package stays
> `event`. No table, column, constraint or index changed.
>
> **Revision 7 closes U4 (idempotency)** and is the first revision to change the inventory: **13 tables**.
> A new `idempotency_records` table owns the organization-scoped key, the request fingerprint and the
> event it produced, with **72-hour** retention; `events.idempotency_key` and its unique constraint are
> **removed** so idempotency data exists in exactly one place. Previously approved decisions — eager
> fan-out (U5), the per-delivery snapshot (U33), the endpoint ceiling (U37/U38) and application-layer
> transaction ownership (U39/U40) — are unchanged and are what the new reservation step is built around.
>
> **Revision 8 closes U41 (request fingerprint canonicalization).** `request_hash` becomes
> `SHA-256(UTF-8(RFC 8785 JCS of {"event_type": …, "payload": …}))`, stored as **`bytea`** with an
> exact **32-byte** `CHECK`; `hash_algorithm` is pinned to `sha256`. Transport headers (`Authorization`,
> `Idempotency-Key`, timestamps, …) are excluded, and invalid JSON is rejected with HTTP 400 **without**
> any fallback to raw-byte hashing. Scope of this revision: one column's type and constraints, the
> fingerprint subsection (§4.13.1), and the ordering of validation before the key claim. The inventory
> stays **13 tables** and no relationship changed. U46 (numeric policy inside `payload`) and U47 (JCS
> library selection, deferred on instruction) are opened by it; U42–U45 stay unresolved.
>
> **Revision 9 closes U46 (JSON numeric and parsing policy)** and, in the same pass, drafts **U42–U45 as
> proposals that are explicitly NOT approved**. U46 is now canonical: accepted JSON integers stay inside
> `[-9007199254740991, 9007199254740991]`, larger values must arrive as JSON **strings**, duplicate object
> keys are **rejected**, decimals follow RFC 8785 number serialization, non-representable or non-finite
> values are rejected, and every one of those failures is **HTTP 400 before an idempotency key is claimed**
> — no record, event, delivery or outbox row. It does not soften U41: there is still no raw-body hashing
> fallback and PostgreSQL `jsonb` is still never the canonicalization input.
> Schema effect: **none** — 13 tables, the same columns, constraints and indexes; U46 is an
> API-boundary validation rule that `jsonb` cannot express. U42–U45 were drafted in §4.13.6 and overview §14
> at that point and changed no canonical behaviour — revision 10 approved them. One residual gap U46's
> approval leaves open is recorded as **U48** (decimal precision beyond double round-trip).
>
> **Revision 10 approves U42, U43, U44 and U45** and promotes them from §4.13.6's proposal table into the
> canonical sections. What that means for the schema:
>
> * **U42** (HTTP response contract) — `202` for both acceptance and replay, byte-identical body built from
>   `events (id, destination_count, received_at)`, `HookFlow-Idempotent-Replay: true` on replays only,
>   `409` on fingerprint mismatch, `400` on rejected input, RFC 9457 as the API error convention. **No DDL**:
>   no response column is stored, because the body is reconstructible from immutable columns (§4.13.3).
> * **U43** (cleanup) — the sweep SQL in §4.13.4 is now the locking-CTE form (`FOR UPDATE SKIP LOCKED` +
>   `DELETE … USING`), 500 rows per batch, 1-minute interval, at most 5 batches per tick, committed
>   per batch, hosted by `worker`, configuration rather than constants. **No DDL**: `idx_idem_expiry`
>   already serves it.
> * **U44** (committed-NULL invariant) — **the only schema change in this revision**: one function plus one
>   `DEFERRABLE INITIALLY DEFERRED` constraint trigger on `idempotency_records (INSERT, UPDATE)`, inside
>   migration unit 13 (§4.13 constraints, §7). The column stays nullable, because the NULL window is what
>   claim-first ordering (U4) bought; U1 is still open and this guard is deliberately U1-independent.
> * **U45** (no key history) — no table, and the per-conflict `audit_logs` write floated in the proposal is
>   **not adopted**, which keeps the approved "a 409 writes nothing" semantics exactly as they were.
>
> Inventory unchanged at **13 tables**, no relationship added, dropped or retyped. U47 (JCS parser
> selection, now with four concrete requirements) and U48 (decimal precision) stay **open**; U46 is
> re-verified against the checklist and left as approved.
>
> **Revision 11 changes no schema object and approves nothing.** It evaluates U47 and U48 and writes the
> result up as **Proposed — Pending Owner Approval** in
> [overview.md §15](./overview.md#15-u47-parser-selection-u48-numeric-policy-and-the-ingestion-parse-order-revision-14-approvals-and-open-remainders)
> (that section was retitled when revision 14 approved most of it):
> the U48 recommendation (Option B — standard RFC 8785 number semantics, strings for exact decimals, no
> digit-count rejection in V1), the verified Go candidate matrix whose headline is that **no candidate
> implements U46's numeric rules**, and the eight-step ingestion parse order that keeps every `400` ahead of
> the key claim. §4.7 and §4.13.5 gain pointers to that proposal; their canonical wording is untouched. Two
> documentation defects were also fixed here: a duplicated `## 5.` heading, and a §4.7 example that said a
> 20-digit decimal "canonicalizes to `1.0`" when RFC 8785 produces `1`.
>
> **Revision 12 corrects revision 11's numeric analysis and again changes no schema object.** U46 rule 1 judges a
> number's **mathematical value**, so `1.7e308`, `1E30` and `9223372036854775807` are unsafe integers written with
> an exponent and stay **rejected** — revision 11 wrongly accepted `1.7e308` by reading notation as integrality.
> Executed boundary evidence (Go 1.27.3 stdlib `encoding/json/jsontext`, run outside the repository) then showed
> the literal-only rule cannot be sufficient on its own: `9007199254740992.5` is a fractional literal that
> canonicalizes to the unsafe integer `9007199254740992` **with no error**, and `1e400` **saturates** to
> `1.7976931348623157e+308` with `err=nil`. §4.13.5 and §4.7 therefore carry the proposed post-conversion guard
> **G1** (necessary; reuses `integer_out_of_safe_range`, so no new status code) and the optional **G2** (accept a
> literal only when its canonical text denotes the same decimal value), and revision 11's ">15 significant digits"
> proxy is withdrawn because `1e-400` — one digit — is inexact too. RFC 8785's conformance sources are cited
> correctly now: Appendix B Table 1, §3.2.2, §3.2.3 and Appendix I; **the specification has no section 16**. All of
> it stays *Proposed — Pending Owner Approval*: 13 tables, 14 relationships, and every column, constraint, index
> and trigger unchanged.
>
> **Revision 13 finalizes the numeric recommendation and still changes no schema object.** Three points. (1) The
> guard set narrows to **G1 only**: `math.Abs(f) ≥ 2^53` after the binary64 conversion, reported as
> `integer_out_of_safe_range` — the `math.Trunc` clause revision 12 added is redundant, because every binary64 at or
> above 2^53 is an integer — and **G2 is deferred rather than recommended**, so `number_not_exactly_representable`
> never enters §5.5's code list and the accepted-decimal residue is measured by the inexactness counter instead of
> rejected (overview §15.1.1). (2) The decimal-string integrality test that decides U46 rule 1 was **wrong about
> trailing zeros** in revision 12 (`1.200e1` = 12 was classified fractional); §4.13.5 now points at the corrected
> procedure — `value = D × 10^p`, integral iff `p ≥ 0` or `tz(D) ≥ −p` — with the nine boundary literals worked
> through it in overview §15.3.1. (3) Both viable parser paths were **executed** against the same conformance
> battery (Go 1.27.3 stdlib `encoding/json/jsontext` and `github.com/goccy/go-json/jsontext` v0.12.0, outside the
> repository) with byte-identical results, so overview §15.2.2 recommends the **standard library (path A),
> conditional on U30's toolchain half being approved as Go 1.27.4** (§15.6). None of this alters a table, column,
> constraint, index, function or trigger: the schema shape is exactly revision 12's.
>
> **Revision 14 is the owner's approval round. It approves a defined set, changes no schema object, and leaves the
> rest open.** Approved (overview §11 items 25–32): **U30's toolchain half** (baseline go1.27.4), **U47** (path A —
> the standard-library `encoding/json/jsontext`, with `github.com/goccy/go-json` **not** adopted and **no JSON
> dependency added**), **U48 Option B** with **guard G1 approved** (`math.Abs(f) ≥ 2^53` → `integer_out_of_safe_range`)
> and **guard G2 deferred — not implemented and not required in V1**, the **§15.3 parse order** as normative, the new
> rule that **`payload` must be a JSON object** (scalars, arrays and `null` refused as `400 invalid_json_document`;
> unrecognised `payload` members accepted, none reserved), the **`hookflow_ingest_number_inexact_total`** counter as an
> observability instrument with **no numeric threshold in the contract**, and the **conformance gate** required in CI
> before merge. U41's formula and U46's five rules are re-verified **unchanged**; so is the corrected `D × 10^p`
> trailing-zero integrality procedure. **Evidence limits, stated plainly:** the executed conformance work ran on
> go1.27.3 outside the repository, the Appendix I sample file, V8 cross-checks and fuzzing have **not** been run, and
> **nothing has been run on go1.27.4** — so no implementation or runtime verification is claimed, and §4.13.7 records
> that as pending work. **U1, U3, U6 and U17 stay open** (no maximum payload size is written down anywhere), and U30's
> linter/complexity/Actions half stays open too. Schema shape: **nothing** — 13 tables, 14 agreed relationships,
> 152 ERD attribute lines, and no migration, `go.mod`, container or workflow file exists or was touched.
>
> **Revision 15 recorded U1, UUID generation, as an approval — and that claim was premature: the owner had not
> approved U1 at that time. The owner's approval is revision 16 (overview §11 item 34), and overview §12.15 is the
> correction record.** What revision 15 genuinely did was document the strategy reproduced below, and it changes no
> schema object; the strategy is adopted unchanged in revision 16. Recorded there, approved here
> (overview §11 items 33 and 34): **UUIDv7 for every entity identifier**, the publicly exposed `events.id` and
> `deliveries.id` included; generated **in the Go application layer before the `INSERT`**, so **not one `id` column
> in this document carries a `DEFAULT`** — none did before, and none does now, which is why the approval has no DDL
> artifact; the **same strategy in `api`, `worker` and `outbox-publisher`**, so a `delivery_attempts` row written by
> `worker` and the fresh `outbox_messages` row that a retry step inserts are built the same way an event row is — each
> id is generated once, at its own row's `INSERT`, and is never rewritten afterwards, so a row that already exists
> keeps its id through publish retries and re-drives (§4.10, consequence 25); the **approximate creation time
> the v7 layout carries is accepted**, with the explicit rule that **an identifier is not an authorization
> credential** — the §3.1 same-organization invariant and the API scopes decide access, never the id; **ordering and
> scheduling stay on timestamp columns** (`received_at`, `created_at`, `next_attempt_at`, `available_at`,
> `expires_at`), never on id order — and no strict monotonicity inside one process or one millisecond is claimed
> either, because RFC 9562 §5.7 makes the counter and sub-millisecond sub-fields optional and U1 point 6 names no
> generator (§2 records the same); and **no library, import path or dependency version is named**, so this revision
> adds nothing to any manifest — dependency selection stays with the open questions overview §13 already records.
> Consequences that are documentation only: §1 and §2 now state the generation rule next to `id uuid PRIMARY KEY`,
> every `id` row's note says *where the value comes from*, and consequence 24 records the one operational side effect
> worth naming — a hand-written `INSERT` that omits an id fails on `NOT NULL` instead of being quietly filled in.
> `idempotency_records.event_id` **stays nullable under U44's approved deferred constraint trigger**: it is revision
> 16's approval that
> satisfies the precondition §4.13 foresaw for replacing the trigger with `event_id uuid NOT NULL`; this round does not
> apply that replacement, and the choice stays a separate decision. **U3 is not decided and stays independently
> decidable**: U1 declines native `uuidv7()` as an id-generation route, which is not a PostgreSQL vote, no PostgreSQL
> major is implied, and nothing here changes or approves U3's row; **U20 is not decided**, so whether `audit_logs.id`
> stays `uuid` or
> becomes `bigserial` is still open. Register: **31 rows**, unchanged at **28 open + 3 approved and retained** (U1, U47, U48),
> **20 approved IDs**, overview §11 ending at item **34**. Schema shape again, stated as counts: **13 tables, 14
> agreed relationships, 13 ERD entity blocks / 17 relationship lines / 152 attribute lines — all unchanged**, and no
> migration, `go.mod`, container or workflow file exists or was touched.
>
> **Revision 16 records the owner's approval of U1, UUID generation, and corrects revision 15's status for it. It
> changes no schema object either.** The approved decision, in schema terms: **every entity id is a UUIDv7 value
> generated in the Go application before the `INSERT`**, so all 13 `id` columns stay `uuid PRIMARY KEY` with **no
> `DEFAULT`** — none exists, none may be added by any migration unit, and an omitted id fails on `NOT NULL` rather than
> being filled in by the database; **`idempotency_records.event_id` is likewise supplied by the application** and stays
> nullable under U44's approved deferred constraint trigger; **PostgreSQL's native `uuidv7()` is not used for id
> generation**, which declines an implementation route for U1 and is not a vote on the PostgreSQL version; the
> **approximate creation time carried by a v7 value is accepted**, including on the ids that leave the system
> (`events.id` in the `202` body, `deliveries.id` in `X-HookFlow-Delivery-Id`); **an id is an identifier, not an
> authorization control**, so access stays exactly where §3.1's same-organization invariant and the API scopes put it;
> and **ordering and scheduling read timestamp columns only** (`received_at`, `created_at`, `next_attempt_at`,
> `available_at`, `expires_at`), never id order, with no monotonicity claim inside a process or a millisecond.
> **Storage is unaffected by the choice**, which is worth stating because it is what makes U3 independently decidable:
> the `uuid` type is 16 bytes regardless of version, its text form is the RFC 4122 lowercase hyphenated rendering for v4
> and v7 alike, and indexes, comparisons, FKs and `jsonb` storage do not consult the version nibble. **Rationale, as the
> owner recorded it:** an id that exists before the `INSERT` is what §5's accept transaction requires; one generation
> rule serves `api`, `worker`, `outbox-publisher` and any batch or backfill write, where a column default would leave
> each writer to invent its own; and the rule couples id generation to **no PostgreSQL capability and to no SQL parser
> or catalog support**, so it stands whatever U3 eventually decides. **Historical correction, not a reversal:** revision
> 15 wrote this strategy up as owner-approved before any approval existed; §11 item 33 now records that as premature
> and overview §12.15 holds the correction, while §11 item 34 is the approval — U1's status is **Approved (revision
> 16)**, not open and not rescinded, and the strategy itself did not change. **Everything else stands as recorded**:
> **U3 open** (no version chosen and no implication drawn; overview §13's U3 row also had its obsolete `uuidv7()`
> rationale corrected in this same revision — a wording fix that selects no version and leaves the decision Open),
> **U6 open** — envelope, key
> custody, AAD and copy-vs-pointer remain undecided. The historical revision-16 wording said AAD used the
> **canonical lowercase UUID text of the stored `uuid` values**; that proposal is retained as history and is
> **superseded for current guidance** by the 2026-11-24 reconciliation note (proposed raw 16-byte UUIDs,
> 64-byte AAD, **now OWNER APPROVED as D6-B and implemented in development; production custody not implemented**) —
> plus **U8, U17, U20, U25, U30's lint/CI half,
> U34 and U35** all open, and **no library, import path or dependency version** named, so no manifest entry exists or is
> implied. Counts, unchanged: register **31 rows = 28 open + 3 approved and retained** (U1, U47, U48), **20 approved
> IDs**, overview §11 ending at item **34**, **13 tables / 14 agreed relationships / ERD 13 entity blocks / 17
> relationship lines / 152 attribute lines**. Documentation only: no DDL, SQL, migration, Go source, `go.mod`, container
> or workflow file was created or modified, nothing was staged, committed or pushed, and **this approval is not
> authorization to begin implementation**.
>
> **Revision 17 is documentation-only and changes no schema object.** It (1) points the status banner at overview
> §16 for what is implemented after M1-E Phase 2.1, (2) records the owner-approved Recovery expiry-precedence rule
> in §5 (cross-referenced from overview §12.18), and (3) stops describing `db/migrations/` as an empty placeholder.
> The 13 paired migrations exist and are authoritative for DDL; no table, column, constraint, index, trigger,
> relationship, default or numeric parameter changes in this revision.
>
> **Revision 18 (2026-11-24) records Phase 6 implementation against this unchanged schema and corrects three
> current-state descriptions. It changes no table, column, constraint, index, trigger, relationship or migration;
> schema version stays 13.** (1) The management API exists and now sets `endpoints.deleted_at` on `DELETE`; §4.5's
> "no CRUD route / `cmd/api` serves health only" statements describe the pre-Phase-6 tree and are superseded. (2)
> U38's count-then-write protocol is implemented with `FOR NO KEY UPDATE` on the organization row instead of
> `FOR UPDATE`, so fan-out foreign-key checks (`FOR KEY SHARE`) do not conflict with management writes; lock ordering
> is organizations -> endpoints -> endpoint_subscriptions. (3) `endpoints.last_success_at` / `last_failure_at` remain
> reserved columns with no writer. The D6/U25 development runtime (file KEK, `hfse1.` envelopes, signing) likewise
> required no schema change.

---

## 1. Agreed decisions and where each one lands

| Agreed decision | Schema artifact |
| --- | --- |
| PostgreSQL, UUID primary keys | `id uuid PRIMARY KEY` on all 13 tables. The generator question is **closed**: the owner approved **U1** in revision 16 (recorded as §11 item 34, after revision 15 documented the strategy under a premature approval claim) — the values are **UUIDv7** and are produced by the **application before the `INSERT`**, so no `id` column has, or may have, a `DEFAULT`. Storage is unaffected by that choice: the column type stays `uuid`, 16 bytes with the RFC 4122 text form, identical for v4 and v7 values, so application-generated UUIDv7 needs no type, length, index or constraint change. The PostgreSQL target is **18** (A6, 2026-11-22); the exact patch and tooling are verified before any migration executes, and native `uuidv7()` is *not* used for id generation |
| Every entity identifier is a UUIDv7 value generated in the application layer, with no database-generated defaults, and one strategy shared by `api`, `worker` and `outbox-publisher` (U1 approved, revision 16) | **no DDL artifact** — this is where values come from, not what the schema holds. §2 states it as a convention, §4 repeats it on every `id` row, and consequence 24 records the `NOT NULL` side effect. Publicly exposed ids (`events.id` in the `202` body, `deliveries.id` as `X-HookFlow-Delivery-Id`) are covered; **ids authorize nothing** — access is §3.1's same-organization invariant plus API scopes |
| `Idempotency-Key` required and unique per organization (U4 approved) | the header stays **required at the API boundary**, but the key is stored and uniquified in `idempotency_records`: `uq_idem_org_key UNIQUE (organization_id, idempotency_key)` (§4.13). `events.idempotency_key` is **removed** |
| 72-hour retention of idempotency records; key reusable after expiry; expiry never deletes event history (U4 approved) | `expires_at = created_at + interval '72 hours'` with `chk_idem_window`, cleanup served by `idx_idem_expiry`; because the FK points *from* the record *to* `events`, deleting a record cannot touch the event or its deliveries (§4.13, §6) |
| Same key + same request content replays the original event; same key + different content returns 409 (U4 approved) | `request_hash` comparison on the existing row; the 409 path writes nothing and rolls back (§4.13.3) |
| Request fingerprint = RFC 8785 (JCS) then SHA-256 over the canonical UTF-8 bytes, input `{"event_type", "payload"}` (U41 approved) | `idempotency_records.request_hash bytea NOT NULL` with `chk_idem_hash_len CHECK (octet_length(request_hash) = 32)` and `hash_algorithm = 'sha256'`; computed before the transaction opens (§4.13.1) |
| Concurrent replay returns the same status and a byte-identical body; `HookFlow-Idempotent-Replay: true` marks replays; errors use RFC 9457 with stable codes (U42 approved) | **no DDL artifact** — the body is derived from `events (id, destination_count, received_at)`, all written once at accept and never updated, so identity needs no stored response. Enforced by a test, not a constraint (§4.13.3, overview §5.5) |
| Expired-record cleanup runs in `worker`, 500 per batch, 1-minute interval, max 5 batches per tick, `FOR UPDATE SKIP LOCKED` + `DELETE … USING`, commit per batch (U43 approved) | the batching statement in §4.13.4 against the existing `idx_idem_expiry`; **no new index, no new column, no new process**. Configuration-driven values, never inline constants |
| A committed `idempotency_records` row never has a NULL `event_id` (U44 approved) | **one new DDL object**: `idem_assert_linked()` + `trg_idem_linked AFTER INSERT OR UPDATE … DEFERRABLE INITIALLY DEFERRED` in migration unit 13 (§4.13, §7). The column stays `NULL`-able because the claim precedes the event. U1 was approved application-side in revision 16 — revision 15 had documented the strategy under a premature approval claim — which **satisfies but does not apply** the `event_id uuid NOT NULL` simplification §4.13 foresaw; the approved trigger is unchanged and the simplification stays an open item in overview §13's residual notes on U1 |
| No idempotency key-use history table, and no per-conflict `audit_logs` write (U45 approved) | the latest-wins `UPDATE` in §4.13.3 stays the only behaviour; `audit_logs` gains no action, and the 409 path still writes nothing in any transaction |
| Accepted JSON numeric and parsing policy: safe-integer range judged by **mathematical value** (so `9007199254740992`, `9007199254740992.0` and `9.007199254740992e15` are all rejected), strings for larger magnitudes, no duplicate keys, JCS decimal serialization, no non-finite values (U46 approved) | **no DDL artifact** — `jsonb` stores arbitrary-precision numerics and cannot express per-element rules, so the constraint lives at the API boundary (§4.7, §4.13.1). It guarantees the §4.13.1 canonicalizer always round-trips an accepted integer exactly, and every violation is `HTTP 400` before the key claim |
| Request `payload` must be a JSON **object**, and unrecognised `payload` members are accepted (approved in revision 14, overview §11 item 30) | **no DDL artifact** — `jsonb` accepts every JSON value, so the requirement is an API-boundary check at parse step 3 (overview §15.3): an array, string, number, boolean or `null` `payload` is `400 invalid_json_document` before the claim and leaves no row anywhere (§4.7, §4.13.5) |
| Numbers beyond the double round-trip follow standard RFC 8785 / ECMAScript binary64 semantics, with post-conversion **guard G1 approved** and the exact-round-trip **guard G2 deferred** (U48 approved in revision 14) | **no DDL artifact** — `payload` keeps the client's accepted document as written, the residue is measured by an approved counter rather than refused, and no `CHECK` can express any of it (§4.7) |
| Canonicalization uses the standard-library `encoding/json/jsontext` at the go1.27.4 toolchain baseline, gated by a conformance run in CI before merge (U47 and U30's toolchain half approved in revision 14) | **no DDL artifact** — a package plus a toolchain version, not a module and not a schema object; `request_hash` stays the 32-byte SHA-256 of the canonical UTF-8 bytes (§4.13.1) |
| Concurrent requests on one key produce exactly one accepted event (U4 approved) | reservation `INSERT … ON CONFLICT (organization_id, idempotency_key) DO NOTHING` — the unique index blocks the second writer until the first commits, and the loser's whole transaction aborts (§4.13.2) |
| Event, Delivery, Delivery Attempt are separate concepts | tables 7, 8, 9 — one row per concept, no overlapping meaning |
| Delivery destinations selected from `endpoint_subscriptions` when the event is accepted | the matching read runs inside the accept transaction and its result set becomes the `deliveries` rows; `events` stores no destination collection (U33 approved, §5) |
| Subscription changes must not alter already accepted events | `deliveries.url` / `http_headers` / `secret_encrypted` **are** the per-delivery destination snapshot, immutable after acceptance; `endpoint_subscription_id` is a nullable provenance pointer that may be set to NULL when its subscription is deleted (F3-C, §4.8, U33 approved) |
| Endpoint deletion is soft deletion using `deleted_at` | `endpoints.deleted_at timestamptz NULL` + partial unique/partial indexes (§4.5) |
| Billing is a simple organization subscription model in V1 | `organization_subscriptions` (plan + status + period), kept separate from `organizations` (§4.11) |
| Transactional Outbox pattern | `outbox_messages` written in the same transaction as the rows that create work (§4.10) |
| At-least-once delivery | `deliveries` state machine + `delivery_attempts` history + `uq_deliveries_event_endpoint` re-creation guard |
| API keys stored as hashes | `api_keys.secret_hash`, never a reversible column (§4.4) |
| Endpoint webhook secrets require secure reversible encryption | `endpoints.secret_encrypted`, copied into `deliveries.secret_encrypted` as reversible ciphertext (A8); envelope format remains open (§4.5.1, §4.8, U6) |
| Composite FKs guarantee event and endpoint belong to the same organization | `UNIQUE (organization_id, id)` targets + composite FKs on `deliveries`, `delivery_attempts`, `outbox_messages`, `endpoint_subscriptions` (§3.1) |
| A delivery may have multiple outbox messages; not one-to-one | `outbox_messages.delivery_id NOT NULL` with **no** unique constraint on it (§4.10) |
| Eager fan-out; `202` only after the transaction commits (U5 approved) | `events` + `deliveries` + `outbox_messages` in a single transaction, response emitted after `COMMIT` (§5) |
| Exact-match subscriptions only, no wildcards (U16 approved) | `endpoint_subscriptions.event_type` equality in the fan-out read; the `CHECK` regex forbids `*`/`%`/prefix syntax (§4.6) |
| Configurable maximum active endpoints per organization (U37 approved) | platform config `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG`, default 100; no database column, changeable without a migration (§4.5.2) |
| Ceiling enforcement locks the organization row (U38 approved) | `SELECT … FOR UPDATE` on `organizations` then count + write in the same transaction (§4.5.2) |
| One application service owns the accept transaction (U39, U40 approved) | the use case in `internal/app/event` (`package eventapp`, U28) opens one Unit of Work; no module opens or commits its own transaction (§5, overview §4.1–§4.2) |
| No `events.status`; `events.destination_count` kept (U36 approved) | `events` has no status column at all; `destination_count integer NOT NULL CHECK (>= 0)`, and `0` is a normal accepted outcome (§4.7) |

## 2. Global conventions

| Concern | Convention |
| --- | --- |
| Names | snake_case, plural table names; constraints prefixed `tbl_`/`fk_`, indexes `idx_`, checks `chk_`, unique `uq_` |
| Primary key | `id uuid PRIMARY KEY` everywhere, including join tables (uniformity for `sqlc` and for composite-FK targeting). **No `DEFAULT` on any of them** — the value is supplied by the application (U1 approved, revision 16; see the next row) |
| Identifier values | **UUIDv7, generated by the Go application layer before the `INSERT`**, one strategy shared by `api`, `worker` and `outbox-publisher` (U1 approved, revision 16). Chosen over `gen_random_uuid()` (v4, random) because a leading 48-bit timestamp keeps the `uuid` PK and every `UNIQUE (organization_id, id)` composite-FK target inserting near the right-hand edge of the B-tree instead of scattering writes across it, and because the id exists before the row does — which is what lets the fan-out build its `2 + N + N` rows in memory and issue batched statements (§5). Accepted consequence: the first 48 bits approximate creation time, so **ids are not secret and must never be treated as authorization** — tenancy is §3.1's job, and no lookup trusts an id's plausibility. Explicitly **not** a rule about business ordering: business `ORDER BY`, scheduling and due-time scans use `received_at`, `created_at`, `next_attempt_at`, `available_at`, `expires_at`; `id` is never a business ordering key. The outbox claim's `ORDER BY available_at, id` (§4.10) uses `id` only as a deterministic tie-breaker after the timestamp, never as a substitute for one. The B-tree remark above is a **write-locality** observation, not a promise about id order: RFC 9562 §5.7 fills the 74 bits after the timestamp with random data by default and makes the counter and sub-millisecond sub-fields an optional *MAY* "to guarantee additional monotonicity within a millisecond", so two ids generated in the same millisecond have **no guaranteed relative order** unless the generator eventually chosen adds such a counter — a dependency property, and nothing in this schema may depend on it. An id is also written **once**, at its own `INSERT`: no documented statement updates a row's **own** `id` afterwards, so a row carries the same key for its whole life (§4.10, consequence 25). The one identifier `UPDATE` in these flows is `idempotency_records.event_id` (§4.13.2), which fills a *link* to another row's key — U44's deferred trigger exists precisely to check it — and is not a rewrite of any primary key. Explicitly **not** a dependency choice: no library, import path or version is named here (overview §13 keeps that with the open tooling and dependency questions) |
| Tenancy | every tenant-owned table carries `organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE` |
| Composite-FK target | any org-owned table referenced by a composite FK also declares `UNIQUE (organization_id, id)` |
| Time | `timestamptz` only; `created_at`/`updated_at NOT NULL DEFAULT now()`; append-only tables omit `updated_at` |
| Booleans | `boolean NOT NULL DEFAULT false`, never nullable |
| Enums | `text NOT NULL` + `CHECK (col IN (...))` rather than native enum types (U23, approved by A7 on 2026-11-22: a new state needs no type migration; plain strings in `sqlc`) |
| Short strings | `text` + `CHECK (char_length(col) BETWEEN 1 AND n)` — deliberate limits, no silent truncation |
| Variable data | `jsonb NOT NULL DEFAULT '{}'` for structured-but-opaque data (headers, attempt metadata, audit metadata) |
| Fixed-width digests | `bytea` + `CHECK (octet_length(col) = n)` — never hex in a `text` column (4× the storage, collation-sensitive comparisons). The producing algorithm stays in a separate `text` label column (§4.13.1, U41) |
| Accepted JSON numbers | validated **in the application, not by a `CHECK`**: integers confined to `[-9007199254740991, 9007199254740991]`, larger magnitudes arrive as strings, non-finite values rejected (U46 approved, §4.7). Revision 14 adds two boundary rules at the same place, with the same no-`CHECK` consequence: an integer **value** whose binary64 conversion reaches 2^53 is refused (guard **G1**, `integer_out_of_safe_range`), and the request document **and** `payload` must both be JSON objects (`invalid_json_document`). `jsonb` keeps numerics as `numeric` and would happily store what the API must refuse |
| Deletes | hard delete reserved for retention purges and org erasure; `endpoints` is the only soft-deleted entity (U31, approved by A10) |
| Case-insensitive uniqueness | functional unique index on `lower(col)` instead of the `citext` extension |
| Index discipline | every index names the query it serves; no speculative indexes |

## 3. Cross-table integrity rules

### 3.1 The same-organization invariant

A child row must never point at a parent in a different organization. A single-column reference
cannot express this: `deliveries.event_id` alone could reference another tenant's event while
`deliveries.organization_id` names the caller's own organization. Composite foreign keys close the
gap:

```sql
-- parent tables expose a composite unique key
ALTER TABLE events    ADD CONSTRAINT uq_events_org_id    UNIQUE (organization_id, id);
ALTER TABLE endpoints ADD CONSTRAINT uq_endpoints_org_id UNIQUE (organization_id, id);

-- child must match both columns against one parent row
ALTER TABLE deliveries
  ADD CONSTRAINT fk_deliveries_org_event
  FOREIGN KEY (organization_id, event_id)
  REFERENCES events (organization_id, id)
  ON DELETE CASCADE,
  ADD CONSTRAINT fk_deliveries_org_endpoint
  FOREIGN KEY (organization_id, endpoint_id)
  REFERENCES endpoints (organization_id, id)
  ON DELETE RESTRICT;
```

If `event_id` belongs to another organization, the `(organization_id, event_id)` pair matches no row
in `events` and the insert fails with a referential-integrity error. No trigger required. The same
pattern is applied to `endpoint_subscriptions`, `delivery_attempts`, `outbox_messages` and
`idempotency_records`, and to the two tenant-safe provenance edges:
`events (organization_id, api_key_id) → api_keys (organization_id, id)` and
`deliveries (organization_id, endpoint_subscription_id) → endpoint_subscriptions (organization_id, id)`
(F3-B/F3-C; §3.2). The deliberate exception is `api_keys.created_by_user_id` /
`revoked_by_user_id` → `users`: `users` is global and has no `organization_id`, so no composite form
exists; those references stay single-column and their organization-membership validation is an
application responsibility (F3-A).

**Composite target check for `idempotency_records` (requested in the U4 review):** the tenant-safe FK
`idempotency_records (organization_id, event_id) → events (organization_id, id)` needs
`events UNIQUE (organization_id, id)` to exist. It already does — `uq_events_org_id`, declared for
`deliveries` since revision 2 and re-verified in §4.7 — so **no additional composite unique constraint
is required on `events`**. Adding a second one would cost an extra index write on the hottest table for
no integrity gain, which is the failure mode this check exists to prevent.

### 3.2 The relationships, as foreign keys

The agreed inventory and relationship list maps to exactly these edges, plus the two added with the 13th
table when U4 was approved. The three nullable U12 provenance edges — `users`→`api_keys` (creator and
revoker), `api_keys`→`events`, and `endpoint_subscriptions`→`deliveries` — were adopted by A10
(2026-11-22), which recorded the nullable columns/FKs but not their actions. The owner resolved those
details on 2026-11-23 (overview §12.17): `users`→`api_keys` stays single-column with `SET NULL` and
application-enforced membership (F3-A); `api_keys`→`events` and `endpoint_subscriptions`→`deliveries`
use composite same-organization FKs with column-targeted `SET NULL` (F3-B/F3-C). These are referential
forms of the same three edges, so the 14-edge inventory below is unchanged.

| Agreed relationship | Implementation |
| --- | --- |
| `users` ↔ `organizations` many-to-many | through `organization_members` (both FKs `ON DELETE CASCADE`) |
| `organizations` 1:N `api_keys` | `api_keys.organization_id` + `uq_api_keys_org_id` |
| `organizations` 1:N `endpoints` | `endpoints.organization_id` + `uq_endpoints_org_id` |
| `organizations` 1:N `events` | `events.organization_id` + `uq_events_org_id` |
| `organizations` 1:N `organization_subscriptions` | `organization_subscriptions.organization_id` |
| `organizations` 1:N `audit_logs` | `audit_logs.organization_id NOT NULL` (§4.12; U9, organization-scoped rows per A9) |
| `organizations` 1:N `idempotency_records` | `idempotency_records.organization_id` + composite FK target (§4.13) — **added with U4** |
| `endpoints` 1:N `endpoint_subscriptions` | composite FK `(organization_id, endpoint_id)` CASCADE (§4.6) |
| `events` 1:N `deliveries` | composite FK `(organization_id, event_id)` |
| `endpoints` 1:N `deliveries` | composite FK `(organization_id, endpoint_id)` |
| `deliveries` 1:N `delivery_attempts` | composite FK `(organization_id, delivery_id)` |
| `deliveries` 1:N `outbox_messages` | composite FK `(organization_id, delivery_id)`, **not** unique (§4.10) |
| `events` 1:N `idempotency_records` | composite FK `(organization_id, event_id)`, nullable child column, `ON DELETE CASCADE` (§4.13) — **added with U4** |

Referential actions per edge:

| Relationship | Action | Reason |
| --- | --- | --- |
| `organizations` → any tenant table | `CASCADE` | tenant erasure removes all its rows |
| `users` → `organization_members` | `CASCADE` | a membership without a user is meaningless |
| `users` → `api_keys` provenance columns | `SET NULL` | keep the key, lose only provenance; `users` is global, so organization membership is validated by the application (F3-A) |
| `api_keys` → `events` provenance column | `SET NULL (api_key_id)` | keep the event, lose only the key reference; `events.organization_id` is preserved (F3-B) |
| `endpoint_subscriptions` → `deliveries` provenance column | `SET NULL (endpoint_subscription_id)` | keep the delivery and its sendable snapshot, lose only the provenance pointer (F3-C) |
| `endpoints` → `endpoint_subscriptions` | `CASCADE` | a routing rule without its endpoint is meaningless |
| `events` → `deliveries` | `CASCADE` | retention purge of events removes derived work |
| `endpoints` → `deliveries` | `RESTRICT` | delivery history keeps its destination reference |
| `deliveries` → `delivery_attempts` | `CASCADE` | attempts are the tail of a delivery |
| `deliveries` → `outbox_messages` | `CASCADE` | queued commands die with their delivery |
| `events` → `idempotency_records` | `CASCADE` | purging an event past its 90-day window also drops the (already expired) key record; the reverse never happens — deleting a record leaves the event and its delivery history intact, which is U4 requirement 7 |

**U12 provenance edges — resolved 2026-11-23.** A10 adopted the proposed nullable provenance
columns/FKs on three edges (2026-11-22). The owner then fixed their actions and same-organization forms
(F3-A/F3-B/F3-C, overview §12.17):

| U12 provenance edge | Documented action | Composite same-organization form |
| --- | --- | --- |
| `users` → `api_keys` (creator, revoker) | `SET NULL` (§4.4) | not applicable — `users` has no `organization_id`; membership is validated by the application (F3-A) |
| `api_keys` → `events` | `SET NULL (api_key_id)` (§4.7) | `(organization_id, api_key_id) → api_keys (organization_id, id)` (F3-B) |
| `endpoint_subscriptions` → `deliveries` | `SET NULL (endpoint_subscription_id)` (§4.8) | `(organization_id, endpoint_subscription_id) → endpoint_subscriptions (organization_id, id)` (F3-C) |

### 3.3 Known trap in that table

`deliveries → endpoints RESTRICT` also blocks **hard-deleting an organization** while delivery
history exists: PostgreSQL propagates the `organizations` `CASCADE` into `endpoints`, and that delete
is then restricted by `deliveries`. Two consistent options, both needing approval (U7):

* **Archive instead of erase** — set `organizations.status = 'archived'`; physical deletion is
  permitted only after the 90-day retention purge has removed the tenant's events and deliveries.
* **Cascade everything** — switch that FK to `CASCADE` and accept that deleting an endpoint row
  silently deletes its delivery history.

The first is recommended and matches the soft-deletion decision.

### 3.4 Idempotency and fan-out dedupe

Two different uniqueness rules, deliberately not merged:

* `uq_idem_org_key UNIQUE (organization_id, idempotency_key)` on `idempotency_records` — caller-facing:
  within one organization the same key is the same request, so a replay must not create a second event,
  delivery set or outbox message. Since U4 this lives in its **own** table rather than on `events`,
  because the two lifetimes differ: a key expires after **72 hours** and becomes reusable, while the
  event and its delivery history are retained for **90 days**. One column cannot serve both windows, and
  a key-only delete would have meant deleting the event (§4.13).
* `uq_deliveries_event_endpoint UNIQUE (event_id, endpoint_id)` — engine-facing: at most one
  delivery per (event, destination) pair, which is what makes eager fan-out safe to retry if the
  transaction is replayed.

`endpoint_subscriptions` is deduped by `UNIQUE (endpoint_id, event_type)`; `delivery_attempts` by
`UNIQUE (delivery_id, attempt_number)`.

**No idempotency data remains on `events`.** `events.idempotency_key` and
`uq_events_org_idem` were removed rather than left as a mirror, so the key→event mapping has exactly one
authoritative location and cannot drift from it. Looking up "which key created this event" is a reverse
query on `idx_idem_event`; looking up "which event did this key produce" is the unique-index hit
`(organization_id, idempotency_key)`.

**"Same request content" has a definition.** U41 approved it: RFC 8785 (JCS) applied to
`{"event_type", "payload"}`, then SHA-256 over the canonical UTF-8 bytes, stored in `bytea` under a
32-byte `CHECK`. Equality of two requests is therefore equality of JSON values, not equality of bytes on
the wire — formatting, member order and transport headers cannot influence it. Full contract in §4.13.1.

## 4. Tables

Notation: `NN` = `NOT NULL`. Constraint and index details follow each table.

### 4.1 `users` — dashboard identities

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | **UUIDv7 generated by the application before the `INSERT`** — no column `DEFAULT` (U1 approved, revision 16) |
| `email` | `text` | NN, UK (case-insensitive), CHK 3–254 | login identity, globally unique |
| `name` | `text` | NN, CHK 1–100 | length-capped per spec |
| `password_hash` | `text` | NULL | NULL = no local password (pending invite / future SSO) |
| `password_updated_at` | `timestamptz` | NULL | token invalidation reference after a password change |
| `status` | `text` | NN, CHK `invited`/`active`/`disabled` | `disabled` blocks auth without deleting rows |
| `mfa_enabled` | `boolean` | NN, default `false` | column reserved; MFA flow is a later phase |
| `last_login_at` | `timestamptz` | NULL | |
| `created_at` / `updated_at` | `timestamptz` | NN, default `now()` | |

Index: `uq_users_email_lower UNIQUE ON users (lower(email))`. `email` stays `text` to avoid the
`citext` extension. No `refresh_tokens` table exists in the agreed inventory — V1 uses short-lived
access tokens (U10).

### 4.2 `organizations` — tenant root

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | **UUIDv7 generated by the application before the `INSERT`** — no column `DEFAULT` (U1 approved, revision 16) |
| `name` | `text` | NN, CHK 1–120 | display name |
| `settings` | `jsonb` | NN, default `{}` | per-tenant preferences (timeouts, default retry budget, timezone) |
| `status` | `text` | NN, CHK `active`/`suspended`/`archived` | `suspended` blocks ingestion; `archived` is the erase-free off-ramp (§3.3) |
| `created_at` / `updated_at` | `timestamptz` | NN, default `now()` | |

No `plan` column: per correction 2, the commercial plan lives only in `organization_subscriptions`.
No indexes beyond the PK — there is no cross-tenant lookup path in V1. Note also that this table's row
is the **serialization point for the endpoint ceiling**: `SELECT … FOR NO KEY UPDATE` on
`organizations.id` is the implemented protocol before counting or activating endpoints (U38, §4.5.2), so
the row is locked even though no column on it participates in that check. Management writes lock in the
order organizations -> endpoints -> endpoint_subscriptions.

### 4.3 `organization_members` — users ↔ organizations

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | uniform id for `sqlc`/API addressing — **UUIDv7 from the application, no column `DEFAULT`** (U1 approved, revision 16) |
| `organization_id` | `uuid` | NN, FK → `organizations` CASCADE | |
| `user_id` | `uuid` | NN, FK → `users` CASCADE | |
| `role` | `text` | NN, CHK `owner`/`admin`/`member`/`viewer` | RBAC level within the tenant |
| `invited_by` | `uuid` | NULL, FK → `users` SET NULL | |
| `joined_at` | `timestamptz` | NN, default `now()` | present and not null per spec |
| `created_at` / `updated_at` | `timestamptz` | NN, default `now()` | |

Constraints: `uq_members_org_user UNIQUE (organization_id, user_id)` (one membership per pair),
`uq_members_org_id UNIQUE (organization_id, id)`, `idx_members_user ON organization_members (user_id)`
(the "my organizations" query). The rule "every organization keeps at least one `owner`" cannot be a
column `CHECK` (it spans rows); it needs a deferred trigger or an application transaction (U11).

### 4.4 `api_keys` — machine credentials (hashed)

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | **UUIDv7 generated by the application before the `INSERT`** — no column `DEFAULT` (U1 approved, revision 16) |
| `organization_id` | `uuid` | NN, FK → `organizations` CASCADE | |
| `name` | `text` | NN, CHK 1–80 | operator label |
| `key_prefix` | `text` | NN, UK | non-secret display/lookup fragment (first 8 chars) |
| `secret_hash` | `text` | NN | verifier only — **the secret is never recoverable**, by contrast with endpoint secrets (§4.5) |
| `hash_algorithm` | `text` | NN, default `argon2id` | so the verifier can migrate algorithms without a second column |
| `scopes` | `text[]` | NN, default `{}` | e.g. `{events:write,endpoints:read}`; empty = organization defaults |
| `last_used_at` | `timestamptz` | NULL | rotation hygiene signal |
| `expires_at` | `timestamptz` | NULL | NULL = no expiry |
| `revoked_at` | `timestamptz` | NULL | NULL = live key |
| `created_by_user_id` | `uuid` | NULL, FK → `users` SET NULL | provenance; single-column nullable FK adopted by A10; `users` is global, so organization membership is validated by the application (F3-A/U12) |
| `revoked_by_user_id` | `uuid` | NULL, FK → `users` SET NULL | provenance; single-column nullable FK adopted by A10; `users` is global, so organization membership is validated by the application (F3-A/U12) |
| `created_at` / `updated_at` | `timestamptz` | NN, default `now()` | |

Indexes: `uq_api_keys_prefix UNIQUE (key_prefix)` — the single-row auth hot path;
`uq_api_keys_org_name UNIQUE (organization_id, name) WHERE revoked_at IS NULL`;
`uq_api_keys_org_id UNIQUE (organization_id, id)`;
`idx_api_keys_org_live ON api_keys (organization_id) WHERE revoked_at IS NULL`.
Auth query shape: `WHERE key_prefix = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR
expires_at > now())`, then constant-time verification against `secret_hash`.
Rotation issues a new row and revokes the old one rather than mutating `secret_hash` in place.

### 4.5 `endpoints` — delivery destinations (soft-deletable)

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | **UUIDv7 generated by the application before the `INSERT`** — no column `DEFAULT` (U1 approved, revision 16) |
| `organization_id` | `uuid` | NN, FK → `organizations` CASCADE | |
| `name` | `text` | NN, CHK 1–120 | unique while live |
| `description` | `text` | NULL, CHK ≤ 500 | optional per spec |
| `url` | `text` | NN, CHK `~ '^https://[^[:space:]]+$'` | HTTPS-only, POST-only destinations (U13: private/loopback policy unresolved) |
| `secret_encrypted` | `text` | NN | **reversible** ciphertext of the webhook signing secret; envelope format open (§4.5.1, U6) |
| `signing_scheme` | `text` | NN, default `hmac_sha256` | |
| `http_headers` | `jsonb` | NN, default `{}` | extra static headers; must exclude credential-bearing keys (application rule) |
| `status` | `text` | NN, default `active`, CHK `active`/`paused` | `paused` stops new deliveries, keeps history |
| `timeout_ms` | `integer` | NN, default `5000`, CHK 250–30000 | per-destination HTTP timeout |
| `max_attempts` | `integer` | NULL, CHK 1–20 | NULL = organization/global default |
| `last_success_at` / `last_failure_at` | `timestamptz` | NULL | reserved dashboard denormalization; **no code path writes them yet** |
| `created_at` / `updated_at` | `timestamptz` | NN, default `now()` | |
| `deleted_at` | `timestamptz` | NULL | **soft deletion marker** — NULL = live |

Indexes: `uq_endpoints_live_name UNIQUE (organization_id, name) WHERE deleted_at IS NULL`,
`uq_endpoints_org_id UNIQUE (organization_id, id)` (composite-FK target for `deliveries` and
`endpoint_subscriptions`), `idx_endpoints_org_live ON endpoints (organization_id) WHERE deleted_at IS NULL`,
`idx_endpoints_org_deleted ON endpoints (organization_id, deleted_at)`.

**Soft deletion retains the secret envelope.** `deleted_at` only hides the row from live reads; it does not null,
rewrite, or purge `secret_encrypted`. Phase 6's `DELETE /v1/.../endpoints/{id}` sets `deleted_at` and leaves the
endpoint's subscription rows untouched (they are unreachable while the endpoint is deleted); no code path purges an
endpoint row or clears its ciphertext. Hard deletes exist only in test cleanup. Consequently a soft-deleted endpoint
can retain an encrypted secret — and therefore a KEK reference — indefinitely. Any KEK reference audit must include
`endpoints` rows with `deleted_at IS NOT NULL`; a retention horizon is not evidence that the row is gone. This is a
statement about the current schema and code, not an approval of D6 or D6-D.

`timeout_ms` is also the per-attempt HTTP budget component `H` — the enforced full HTTP deadline,
including connection setup, execution and bounded response handling — in the fixed-lease requirement
`L > D + H + P + S` (B4); it remains per-endpoint configuration, and no lease value is approved.

#### 4.5.1 Two different secret models, on purpose

`api_keys.secret_hash` is one-way; `endpoints.secret_encrypted` must be decryptable, because the
worker has to reproduce the HMAC signature of a delivery. That asymmetry is required by the agreed
decisions; the following is an **illustrative sketch, not an approved envelope format**:

```text
secret_encrypted = "v<key_version>:aes-256-gcm:<base64(nonce) + base64(ciphertext+tag)>"
```

A key version inside the ciphertext would permit rotation without invalidating stored secrets, but that
representation is retained as the historical sketch; the implemented form is the Owner-approved `hfse1.`
envelope (D6-A) with its wrapped-DEK header. The DEK itself lives outside the database (environment or KMS) —
never in a column, never in a log, never in a fixture. The development build uses a local file KEK; production
KMS custody remains **NOT IMPLEMENTED / NOT READY**. Rotation is constrained by A8: endpoint envelopes may be
rewrapped, but delivery snapshots must not be (see below).

**Security consequence worth reviewing:** `deliveries` carries its own copy of this ciphertext,
because U33 makes the delivery row the destination snapshot — the copy is what keeps accepted work
signing with the secret the destination had at accept time, immune to any rotation or endpoint edit
that happens afterwards. The
cost is that a reversible secret is duplicated into a 90-day-retained table. The alternative is to
store only `endpoint_id` + key version on the delivery and decrypt from `endpoints` at send time —
one copy, but rotating an endpoint secret would silently change pending deliveries. **The
copy-to-delivery snapshot is approved (A8, 2026-11-22)**; the duplicating option is the chosen storage
shape because it is consistent with the approved snapshot semantics. Envelope details and KMS custody remain
unresolved under the rest of U6.

Rotation must preserve that A8 immutability: under the Owner-approved D6-D (reference-based retirement; production
tooling not implemented), **endpoint
envelopes may be rewrapped** under a rotated KEK, but **delivery snapshots must not be rewrapped** — the snapshot
columns are immutable after acceptance, and rewrapping would mutate `deliveries.secret_encrypted` even though the
plaintext is unchanged. Only purging the delivery row removes a snapshot reference. A KEK remains available while
any retained endpoint envelope (including a soft-deleted endpoint row) or delivery snapshot references it; the
90-day window is a retention horizon, not a key-retirement calendar. See §6 and
[`d6-u25-decision-proposal.md`](./d6-u25-decision-proposal.md) §4.4.

#### 4.5.2 Maximum active endpoints per organization (U37 and U38 approved)

The ceiling is **platform configuration, not a database column**. It is a rule the application
enforces, because counting rows across `endpoints` cannot be expressed as a column `CHECK`, and the
approved table inventory is unchanged by it.

| Property | Value |
| --- | --- |
| Configuration name | `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG` |
| Initial default | `100` active endpoints per organization |
| Where it lives | process configuration (`internal/platform/config`), loaded from the environment at startup |
| Changing it | redeploy with a new value — **no database migration required** |
| Future direction | plan-based limits keyed off `organization_subscriptions.plan` may replace or extend this single global value later; that is not part of V1 (overlaps U19) |

"Active" means a live, enabled destination — the same predicate the fan-out read uses:

```sql
SELECT count(*) FROM endpoints
WHERE organization_id = $1 AND deleted_at IS NULL AND status = 'active';
```

Lowering the configured limit later does **not** retroactively pause or delete existing endpoints; the
check applies only to operations that would increase the count.

**Enforcement protocol (U38 approved).** Every code path that can raise the active count — endpoint
creation and `paused → active` activation — must, inside one transaction: (1) lock the parent
`organizations` row, (2) run the count, (3) apply the change.

```sql
BEGIN;
  -- 1. organization-level lock: the single serialization point for this tenant's endpoint count
  SELECT id FROM organizations WHERE id = $1 FOR NO KEY UPDATE;

  -- 2. count under that lock
  SELECT count(*) FROM endpoints
   WHERE organization_id = $1 AND deleted_at IS NULL AND status = 'active';

  -- 3. reject when count >= HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG, otherwise write
  INSERT INTO endpoints (id, organization_id, name, url, secret_encrypted, ...) VALUES (...);
COMMIT;
```

Locking the parent row is what makes count-then-insert atomic: concurrent requests for the same
organization queue on that one row lock, so the ceiling cannot be overshot by a race. Phase 6 implements
the lock as `FOR NO KEY UPDATE`, which excludes every other count-then-write and management writer but does
not conflict with the `FOR KEY SHARE` locks PostgreSQL takes for foreign-key checks during delivery fan-out.
Consequences to accept knowingly:

* **Endpoint creation and activation are serialized per organization.** Two simultaneous creates for
  the same tenant run one after the other; different organizations never contend. The critical section
  is a short count plus insert, and endpoint writes are an administrative path, so the throughput cost
  at a default of 100 is negligible.
* **The organization row becomes a shared lock anchor.** Any future feature that takes `FOR UPDATE` or
  `FOR NO KEY UPDATE` on the same row (settings writes, quota jobs) will serialize with endpoint creation. A dedicated
  counter row would avoid that but would mean a new table, which is out of bounds for V1.
* **Correctness is a protocol, not a helper function.** The invariant holds only if *all* writers
  follow the same lock-count-write sequence; a path that counts without locking is a silent ceiling
  bypass. That sequence therefore belongs in the application service layer — `internal/app/`, with the
  event use case in `internal/app/event` (`package eventapp`, U40 and U28 approved), as described in
  [overview.md §4.1 and §4.2](./overview.md#41-package-and-dependency-rules) — reached through one
  function, so
  the admin import tool and the HTTP handler cannot diverge. Domain modules must not import
  `internal/app/*`, so the ceiling helper is exposed by the application layer to handlers, not pushed
  down into the endpoint module.
* **Counted predicate vs. index.** `idx_endpoints_org_live` covers `deleted_at IS NULL` but not
  `status = 'active'`, so the count filters rows out of an existing partial index instead of needing
  its own. Because the count is capped by the ceiling, that scan stays small; a dedicated
  `(organization_id, status) WHERE deleted_at IS NULL` index is not justified.

Side effect worth noting: with the default of 100, the worst case for one accepted event is
`2 + 100 + 100 = 202` rows written in a single transaction (one idempotency record, one event, up to 100
deliveries, one initial outbox message each) — that is the concrete bound behind the batching requirement
in §8.7.

### 4.6 `endpoint_subscriptions` — event-routing rules

The single routing table: one row binds one endpoint to one event type.

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | app-generated **UUIDv7**, no `DEFAULT` (U1, revision 16); stored on the delivery as `endpoint_subscription_id` — records which rule matched |
| `organization_id` | `uuid` | NN, FK → `organizations` CASCADE | redundant by design — it is what makes the composite FK possible |
| `endpoint_id` | `uuid` | NN | composite-FK'd to `endpoints (organization_id, id)` |
| `event_type` | `text` | NN, CHK 1–160 + `~ '^[a-z0-9_.-]+$'` | **exact-match only (U16 approved)**: the character class has no `*`, `%` or prefix syntax, so a wildcard cannot be stored |
| `is_active` | `boolean` | NN, default `true` | retained as the enable/disable flag for a routing rule (A10, 2026-11-22) |
| `created_at` | `timestamptz` | NN, default `now()` | creation timestamp retained; `is_active` is the mutable enable/disable field (A10), and the row tracks no `updated_at` |

Constraints:
`fk_es_org_endpoint FOREIGN KEY (organization_id, endpoint_id) REFERENCES endpoints (organization_id, id) ON DELETE CASCADE` —
one rule can never attach an endpoint to another tenant's event stream,
`uq_es_endpoint_event UNIQUE (endpoint_id, event_type)` — an endpoint subscribes to a given event type at most once,
`uq_es_org_id UNIQUE (organization_id, id)` (composite-FK target for `deliveries.endpoint_subscription_id`, F3-C),
`idx_es_match ON endpoint_subscriptions (organization_id, event_type) WHERE is_active` — this is the
fan-out query, and `idx_es_endpoint ON endpoint_subscriptions (endpoint_id) WHERE is_active`.

Fan-out read (exact equality, no pattern matching):

```sql
SELECT e.id, e.url, e.http_headers, e.secret_encrypted, s.id AS endpoint_subscription_id, s.event_type
FROM endpoint_subscriptions s
JOIN endpoints e ON e.id = s.endpoint_id
WHERE s.organization_id = $1
  AND s.event_type = $2
  AND s.is_active
  AND e.deleted_at IS NULL
  AND e.status = 'active';
```

Note that `deleted_at IS NULL` and `status = 'active'` must be written in the query — a foreign key
cannot express "only live rows". Whether that guard stays service-layer-only or gains a trigger is
unresolved (U15).

**Matching is exact only, approved in review (was U16).** The `event_type_patterns text[]` glob column
from revision 1 belonged to the retired `subscriptions` table and is not reintroduced. Consequences to
accept knowingly: a caller wanting "all `order.*` events" registers one subscription row per concrete
type, so the number of `endpoint_subscriptions` rows grows with the type vocabulary rather than with
intent. If prefix matching is ever added, it needs both a different `CHECK` and a different read
(`LIKE`/operator-class based), which the `idx_es_match` b-tree cannot serve — recorded as a future
migration cost rather than a V1 feature.

### 4.7 `events` — accepted events

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | **returned to the caller** as `event_id` in the `202` body (U42) — app-generated **UUIDv7**, no `DEFAULT`; its v7 prefix approximately reveals the creation minute, which U1 accepts, and it carries no authority (§3.1, overview §8) |
| `organization_id` | `uuid` | NN, FK → `organizations` CASCADE | |
| `type` | `text` | NN, CHK 1–160 + `~ '^[a-z0-9_.-]+$'` | matched by equality against `endpoint_subscriptions.event_type` (U16 approved: exact only) |
| `payload` | `jsonb` | NN | event body; **must be a JSON object** (approved in revision 14, overview §11 item 30 — enforced at the API boundary, not by a `CHECK`); the request body is capped by the API before insert at **1 MiB** (OD-5 approved and implemented; `400 payload_too_large`); accepted **value domain** fixed by U46 — see the paragraph below |
| ~~`idempotency_key`~~ | — | — | **removed (U4 approved).** Idempotency ownership moved to `idempotency_records` (§4.13). Keeping it here would put the same key in two tables with two different lifetimes — 72 hours on the record, 90 days on the event — and a purge of one would either orphan or delete the other |
| ~~`dedupe_hash`~~ | — | — | **removed (U4 approved);** superseded by `idempotency_records.request_hash`, which is what decides replay vs 409. The revision-3 idea of an optional hash on `events` is gone so no request-fingerprint data is duplicated |
| ~~`destinations_snapshot`~~ | — | — | **removed (U33 approved).** The destination snapshot is stored per delivery in `deliveries` (§4.8); `events` keeps no destination collection, so there is no second copy that can drift from the first |
| `destination_count` | `integer` | NN, default `0`, CHK `>= 0` | **kept (U36 approved).** Number of `deliveries` rows created at accept time. `0` is a normal persisted outcome — no active endpoint matched the event type — not a failure state, and it is the only place the "matched nothing" fact is recorded now that `events.status` is gone |
| `headers` | `jsonb` | NULL | selected caller headers, allowlisted and credential-free |
| `api_key_id` | `uuid` | NULL, composite FK → `api_keys (organization_id, id)` `SET NULL (api_key_id)` | provenance; adopted by A10 and resolved by F3-B: the composite form keeps `events.organization_id` intact when a key is deleted |
| `source_ip` | `inet` | NULL | |
| `occurred_at` | `timestamptz` | NULL | caller's event time; `received_at` stays authoritative |
| `received_at` | `timestamptz` | NN, default `now()` | retention clock |
| ~~`status`~~ | — | — | **removed (U36 approved).** `events` carries no status column: no `discarded`, no `accepted/fanning_out/fanout_complete` lifecycle. Every accepted event is persisted, including those with zero matching endpoints, and **`deliveries.state` is the source of truth for processing state** (§5). "What is the overall progress of this event?" is an aggregate over `deliveries`, not a stored flag |
| `expires_at` | `timestamptz` | NN, CHK `> received_at` | end of the delivery window; also the retention anchor computed at insert |
| `created_at` / `updated_at` | `timestamptz` | NN, default `now()` | |

Constraint: `fk_events_org_api_key FOREIGN KEY (organization_id, api_key_id)
REFERENCES api_keys (organization_id, id) ON DELETE SET NULL (api_key_id)` — the nullable provenance
reference is tenant-safe, and deleting the key clears only `api_key_id`, preserving
`events.organization_id` (F3-B; §3.1).

Indexes: `uq_events_org_id UNIQUE (organization_id, id)` ← composite-FK target, required by both
`deliveries` and `idempotency_records`,
`idx_events_org_received ON events (organization_id, received_at DESC)`,
`idx_events_org_type ON events (organization_id, type, received_at DESC)`,
`idx_events_expires ON events (expires_at)` — the aging/retention scan; `expires_at` is computed once at
insert and never updated, so no partial predicate is useful here.
`uq_events_org_idem UNIQUE (organization_id, idempotency_key)` is **gone with the column** (U4): the
unique constraint that guarantees one event per key now lives on `idempotency_records`, and a second
unique index on `events` would be a duplicate source of truth rather than a safety margin.
Both event-status constructs from earlier revisions are gone with the column (U36): revision 2's
`idx_events_fanout ON events (received_at) WHERE status IN ('accepted','fanning_out')` was dropped
because eager fan-out leaves no backlog to sweep, and `WHERE status = ...` predicates are now
impossible because `events.status` does not exist. Event-level progress must be aggregated from
`deliveries`, which is indexed by `idx_deliveries_event`.
A GIN index on `payload` remains absent deliberately: it multiplies write cost on the hottest table for
a query pattern nobody has committed to.

Replay handling moved with the key: `events` no longer participates in idempotency at all. A duplicate
request is resolved by `idempotency_records` **before** any event row is written, so the fan-out work is
never paid for a request that will be replayed or rejected (§4.13.3). **U42 approved the wire contract** in
revision 10: a replay returns `202` with a byte-identical body and `HookFlow-Idempotent-Replay: true`, and
because `destination_count` and `received_at` are written once at accept and never updated, `events` can
answer a replay request without storing any response data (overview §5.5).

**Accepted `payload` values (U46 approved).** These rules apply to every ingestion request, not only to
the ones carrying a fingerprint — the parse boundary is shared, so `payload` and the fingerprinted content
can never disagree about what a legal document is:

1. An integer-valued JSON number must lie in the IEEE-754 safe integer range,
   `-9007199254740991 … 9007199254740991` (that is ±(2^53 − 1)). **"Integer-valued" is judged by the
   mathematical value, not by the JSON notation used to write it**: `9007199254740992`,
   `9007199254740992.0` and `9.007199254740992e15` are three spellings of one rejected value, and a check
   that inspects only syntax (no `.` / no `e`) would admit the last two into `payload` and into the
   fingerprint. This is a U46 requirement; U47 requirement 4 states the parser obligation it creates.
2. A magnitude beyond that range is a **client encoding error**: the value must be sent as a JSON string.
   Big ids, precision-preserving money amounts and nanosecond epoch counters belong in strings, which is
   also what keeps them intact through `jsonb` → delivery body → consumer.
3. An object with a **duplicate key** is rejected. RFC 8259 permits last-duplicate-wins, and the rejection
   exists because a payload the parser silently rewrites has no honest relationship to the digest computed
   over it — and because a fingerprint derived from a document whose meaning depends on parser policy is
   not a stable equality test.
4. Decimals are accepted and are canonicalized by **RFC 8785 §3.2.2 number serialization** — the shortest
   representation that round-trips through an IEEE-754 double. A decimal whose value is an integer is still
   governed by rule 1; fractional precision beyond a double is governed by U48, not by this rule.
5. Values that cannot be represented or serialized under the JSON/JCS model are rejected, explicitly
   including **non-finite numbers** (`NaN`, `Infinity`, `-Infinity`, which are not legal JSON to begin
   with).

**Document and `payload` shape (approved in revision 14 — overview §11 item 30; this is a separate approved
decision, not a sixth U46 rule).** U46's rules 1–5 above are quoted exactly as revision 9 approved them and are
not extended or reinterpreted by this paragraph.

* The **request document's** top level must be a JSON object. U41 and U46 already assumed that construct ("not
  valid JSON, not an object" is a `400`), and overview §5.1 step 3 states it.
* The **`payload` value** must also be a JSON object. An array, a string, a number, `true`/`false` or `null` is
  `400 invalid_json_document` **before** the key claim — the same existing code, no new one. Overview §15.2.2 row 14
  measured that both candidate parsers accept top-level scalars and canonicalize them without complaint, so this
  check cannot be delegated to a library, and `jsonb` would have stored any of them happily.
* **Unrecognised members inside `payload` are accepted.** `payload` is opaque to the platform — this table stores it
  as written — and no member name is reserved today. Reserving envelope names (`event_type`, `payload`, and any
  future ones) and rejecting the rest would be an explicit, versioned API-contract change, not a side effect of the
  parse order.
* Revision 14 closed the §5.1-versus-§15.5 contradiction in this one direction across all three documents. It adds
  no status code, no column, no constraint and no change to U41's formula or U46's wording.

Three implementation consequences that follow from *where* the rule has to be enforced, all recorded so
implementation planning does not rediscover them:

* **Validation must run before the value is decoded into `float64`.** A Go `map[string]interface{}` decode
  converts every number to `float64` *during* unmarshalling, so `9007199254740993` arrives already rounded
  to `9007199254740992` and the range check would pass on a value the client never sent. The parser has to
  see the literal — `json.Decoder` with `UseNumber()` (or an equivalent `json.Number` path) — which makes
  this a **U47** parser-selection requirement, not just a validation step.
* **Duplicate-key detection is not free either**: the standard decoder accepts duplicates and keeps the
  last one, so rejecting them takes a token-level pass or an equivalent check. Again a U47 requirement.
* **Value comparison needs the exponent decoded, not string-compared.** Rule 1's three spellings are
  rejected as one value, so the check parses the number's mathematical value (mantissa and exponent) rather
  than pattern-matching its text. A `json.Number` that is later handed to `Float64()` for comparison is
  back in double territory, which is exactly where `9007199254740992` and `9007199254740993` collapse
  together — so the comparison has to be done at a precision that keeps them distinct (big-int or decimal
  parsing of the literal) before the safe-range verdict is made. U47 requirement 4. **Revision 14 makes the
  procedure canonical** (overview §15.3 stages 2–4, §11 item 28): bounded decimal-string work on `value = D × 10^p`,
  with trailing zeros kept so integrality is decided correctly (`1.200e1` is the integer 12), a digit count compared
  against the 16-digit bound `9007199254740992`, and a lexicographic string compare only when the count is exactly
  16 — **no arbitrary-precision allocation on an attacker-controlled exponent**, which would turn rule 1 into a
  denial-of-service vector. The nine literals in overview §15.3.1 are the reference cases, and `9007199254740992.5`
  is the one this stage cannot see, which is why guard G1 runs after the conversion.

The residual case U46 did **not** settle was raised as **U48**: a decimal carrying more precision than a
double can hold (say `1.00000000000000000001`) is legal input, is stored by `jsonb` as written, and
canonicalizes to `1`. Two such payloads that differ only beyond the 17th significant digit therefore
**replay** rather than conflict, and the delivered body can differ from the bytes the digest covered. The
options were a significant-digit cap, storing the canonical form instead of the client's, or accepting the
asymmetry as documented behaviour. **Revision 14 approved the third: Option B plus the necessary post-conversion
guard G1, and nothing more** — guard G2 is documented but **deferred, not adopted**, so V1 neither implements nor
requires exact decimal-literal preservation
in [overview.md §15.1 and §15.1.1](./overview.md#151-u48-decimal-precision-beyond-the-double-round-trip). G2 was
declined because it would reject JSON that every compliant JCS canonicalizer accepts
(`1e-400` → `0`) and add a permanent `400` code, while the `jsonb` column keeps the client's digits either way. The
residue is therefore **counted**, not refused: `hookflow_ingest_number_inexact_total` is approved as an observability
counter (§11 item 31) and carries **no numeric threshold in the API contract** — alerting belongs to operations.
Revision 12 also retired the
digit-cap option on evidence: the real condition is "the canonical text denotes a different decimal value than the
literal", and `1e-400` (one digit, → `0`) and `9007199254740992.5` (→ an unsafe integer) both fall outside what a
digit count can see. **Nothing in those decisions changes a rule in this section:** U46's five rules above are the
revision-9 text, and G1 refuses a value rule 1 already refuses rather than adding a new prohibition.

**Phase 7 implementation note (2026-11-25).** Ingestion and fan-out are implemented against this unchanged
schema: version 13, dirty=false, no migration file, column, index, constraint or relationship changed. The
request body is read with a `1 MiB + 1` limit and rejected with `400 payload_too_large` when the extra byte
exists; only the validated, parsed document reaches this `jsonb` column, and no raw request body is retained
anywhere. PostgreSQL `jsonb` normalizes whitespace and object member order, so the delivered body is the parsed
value; the fingerprint is computed from RFC 8785 JCS of the validated document, never from raw request bytes
(§4.13.1). `events.received_at` is the single source of the `202` timestamp, and 90-day event retention and
72-hour idempotency retention are unchanged (§6).

### 4.8 `deliveries` — one row per (event, destination); the destination snapshot lives here

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | sent as `X-HookFlow-Delivery-Id` so consumers can dedupe — app-generated **UUIDv7**, no `DEFAULT` (U1 approved, revision 16) |
| `organization_id` | `uuid` | NN, FK → `organizations` CASCADE | |
| `event_id` | `uuid` | NN | composite-FK'd to `events (organization_id, id)` |
| `endpoint_id` | `uuid` | NN | composite-FK'd to `endpoints (organization_id, id)` |
| `endpoint_subscription_id` | `uuid` | NULL, composite FK → `endpoint_subscriptions (organization_id, id)` `SET NULL (endpoint_subscription_id)` | provenance pointer to the rule that matched; nullable and may be cleared when the subscription is deleted, which leaves the delivery snapshot and the delivery itself untouched (F3-C, U12) |
| `url` | `text` | NN | **snapshot column** — destination URL exactly as it stood when the event was accepted |
| `http_headers` | `jsonb` | NN, default `{}` | **snapshot column** — static headers as accepted |
| `secret_encrypted` | `text` | NN | **snapshot column** — encrypted signing secret as accepted (§4.5.1; copy-to-delivery approved by A8). It is immutable after acceptance and **must not be rewrapped** under the Owner-approved D6-D; the D6/U25 envelope/custody architecture is approved and implemented in development, while production custody is absent |
| `state` | `text` | NN, default `pending`, CHK `pending`/`retrying`/`succeeded`/`failed`/`expired` | |
| `dispatch_seq` | `integer` | NN, CHK ≥ 1, no default | generation of the dispatch intent; initial value 1, incremented per retry (A2) |
| `attempt_count` | `integer` | NN, default `0`, CHK ≥ 0 | single authoritative read for the retry scheduler |
| `next_attempt_at` | `timestamptz` | NULL | business due-time authority; NULL once terminal. `outbox_messages.available_at` is a derived relay gate (U8 approved; A3) |
| `last_attempt_at` | `timestamptz` | NULL | |
| `last_response_status` | `integer` | NULL, CHK 100–599 | convenience for the history list |
| `last_error` | `text` | NULL, CHK ≤ 500 | truncated, credential-scrubbed |
| `priority` | `smallint` | NN, default `0` | optional fan-out ordering hint |
| `created_at` / `updated_at` | `timestamptz` | NN, default `now()` | `created_at` is the retention clock |
| `completed_at` | `timestamptz` | NULL | |
| `dispatch_lease_token` | `uuid` | NULL | C-2 worker fencing token; not an entity id (A1) |
| `dispatch_lease_expires_at` | `timestamptz` | NULL | C-2 lease expiry; paired with the token, no default (A1) |

Constraints:
`uq_deliveries_event_endpoint UNIQUE (event_id, endpoint_id)` — fan-out idempotency,
`fk_deliveries_org_event` / `fk_deliveries_org_endpoint` — the same-organization rule (§3.1),
`fk_deliveries_org_endpoint_subscription FOREIGN KEY (organization_id, endpoint_subscription_id)
REFERENCES endpoint_subscriptions (organization_id, id) ON DELETE SET NULL (endpoint_subscription_id)` —
the nullable provenance pointer is cleared when its rule is deleted, while `organization_id` and the
snapshot payload columns are preserved (F3-C),
`uq_deliveries_org_id UNIQUE (organization_id, id)` — target for `delivery_attempts` and `outbox_messages`,
`chk_deliveries_completed CHECK ((state IN ('succeeded','failed','expired')) = (completed_at IS NOT NULL))`,
`chk_deliveries_worker_lease CHECK ((dispatch_lease_token IS NULL) = (dispatch_lease_expires_at IS NULL))`,
`chk_deliveries_scheduling CHECK ((state IN ('pending','retrying')) = (next_attempt_at IS NOT NULL))` —
only retryable states carry a due time. Both halves are needed: the naive
`state = 'pending' = (next_attempt_at IS NULL)` form rejects every completed row.

Indexes: `idx_deliveries_due ON deliveries (next_attempt_at) WHERE state IN ('pending','retrying')` —
the sweep reads only this partial index regardless of how many events exist,
`idx_deliveries_org_created ON deliveries (organization_id, created_at DESC)`,
`idx_deliveries_event ON deliveries (event_id)`,
`idx_deliveries_endpoint_state ON deliveries (endpoint_id, state, created_at DESC)`.

**Snapshot ownership (U33 approved).** `url`, `http_headers` and `secret_encrypted` are the
destination snapshot payload, immutable after acceptance, and they exist in exactly one place: this
table. `endpoint_subscription_id` is a nullable provenance pointer stored beside them; it may be set
to NULL when its subscription row is deleted (F3-C). Clearing the pointer does not change the snapshot
payload and does not cancel the delivery. `events` holds no destination collection, so there is nothing to keep in sync and no
possibility of a delivery disagreeing with its parent event about where it was supposed to go. The
"which destinations will this event hit?" question is answered by
`SELECT ... FROM deliveries WHERE event_id = $1`, served by `idx_deliveries_event`; the "what did this
delivery actually use?" question is answered by the row itself. The trade-off to accept knowingly is
that listing destinations for one event is now a multi-row read instead of one JSONB value, and that
an event matching zero live endpoints produces zero rows. With `events.status` removed (U36 approved),
`events.destination_count = 0` is the *only* record that nothing matched — which is precisely why the
counter is kept while the status column is not.

### 4.9 `delivery_attempts` — one row per completed HTTP call (append-only; U18)

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | **UUIDv7 generated by the application before the `INSERT`** — no column `DEFAULT` (U1 approved, revision 16) |
| `organization_id` | `uuid` | NN, FK → `organizations` CASCADE | |
| `delivery_id` | `uuid` | NN | composite-FK'd to `deliveries (organization_id, id)` |
| `attempt_number` | `integer` | NN, CHK ≥ 1 | 1-based, tracks `deliveries.attempt_count` |
| `request_method` | `text` | NN, default `POST` | |
| `request_url` | `text` | NN | URL actually used, for post-hoc forensics |
| `request_headers` | `jsonb` | NN, default `{}` | signature/timestamp headers kept; secret-bearing headers redacted before write |
| `response_status` | `integer` | NULL, CHK 100–599 | NULL = no response |
| `response_headers` | `jsonb` | NULL | `Retry-After` preserved for audit |
| `response_body_snippet` | `text` | NULL, CHK ≤ 2000 | truncated, only when capture is enabled |
| `outcome` | `text` | NN, CHK `success`/`retryable_failure`/`terminal_failure` | completion-only; no `pending` row (U18/A4) |
| `error_class` | `text` | NULL, CHK `timeout`/`connect`/`tls`/`dns`/`http_status`/`internal` | aggregatable failure taxonomy |
| `error_message` | `text` | NULL, CHK ≤ 500 | |
| `duration_ms` | `integer` | NULL, CHK ≥ 0 | |
| `started_at` | `timestamptz` | NN, default `now()` | retention clock; writers must supply the observed start time, not rely on the default |
| `finished_at` | `timestamptz` | NN | completion time (A4) |

Constraints: `uq_attempts_delivery_number UNIQUE (delivery_id, attempt_number)` — a retried attempt
cannot double-write; `fk_attempts_org_delivery FOREIGN KEY (organization_id, delivery_id) REFERENCES
deliveries (organization_id, id) ON DELETE CASCADE`; `idx_attempts_org_started ON delivery_attempts
(organization_id, started_at DESC)`.

A row is written once, at completion. U18 approved write-on-completion: no row is written at claim or
start, and a crash after the remote request but before commit may leave no row; absence of a row is not
proof that no HTTP request occurred. The insert supplies the observed start time explicitly.

### 4.10 `outbox_messages` — transactional outbox

Relay table for the **delivery** work queue. Attached to `deliveries`, not to `events`, and
explicitly one-to-many: a single delivery produces a dispatch message when it is created and a fresh
message for each subsequent retry, so message count tracks the delivery lifecycle rather than the
attempt count as a 1:1 identity.

**Which row a given `id` belongs to, stated once (revision-15 clarification).** "Retry" means two different
operations here, and they differ in identity, not just in timing. The worker's next-retry step **inserts a new
row** (overview §5.3, and the `kind='delivery.retry'` insert in §5 below), so that row's UUIDv7 is generated once
when the worker builds it, at its own `INSERT` — its prefix therefore dates from the re-drive, not from the moment
the delivery was accepted. Re-publishing a row that already exists is the other case: `outbox-publisher`
claims a `pending` row through `idx_outbox_claimable` and sets `status='publishing'`, increments
`attempts`, and writes `locked_at`, `locked_by` and a fresh `lease_token`; `published_at` and
`last_error` change only on successful publication. **The row keeps the `id` it was created with**,
because a row's own `id` is written at `INSERT` and no documented statement rewrites a primary key
afterwards. `failed` rows are re-armed to `pending` only by the Recovery Sweep (B2). This paragraph
records what the paragraph above and §5 already describe.

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | **UUIDv7 generated by the application before the `INSERT`** — no column `DEFAULT` (U1 approved, revision 16) |
| `organization_id` | `uuid` | NN, FK → `organizations` CASCADE | |
| `delivery_id` | `uuid` | NN | composite-FK'd to `deliveries (organization_id, id)`; **no unique constraint on `delivery_id`**; `UNIQUE (delivery_id, sequence)` governs retry identity |
| `sequence` | `integer` | NN, CHK ≥ 1, no default | generation of the dispatch intent; matches `deliveries.dispatch_seq` (A2) |
| `kind` | `text` | NN, CHK `delivery.dispatch`/`delivery.retry` | message type, so one relay serves all async delivery work |
| `topic` | `text` | NN | RabbitMQ exchange/routing-key target |
| `payload` | `jsonb` | NN | small dispatch intent (delivery id, attempt hint), not the authoritative body |
| `headers` | `jsonb` | NULL | AMQP headers/properties |
| `status` | `text` | NN, default `pending`, CHK `pending`/`publishing`/`published`/`failed` | |
| `attempts` | `integer` | NN, default `0`, CHK ≥ 0 | per-cycle relay claim count; T1 increments it, the Sweep re-arm resets it to 0; not a lifetime bound and not the broker delivery count (B1) |
| `available_at` | `timestamptz` | NN, no default | derived relay eligibility gate; written explicitly by the approved gate formulas (A3) |
| `locked_at` | `timestamptz` | NULL | persisted publisher lease start; expiry is computed as `locked_at + configured publisher lease duration` (a U22 value, not approved here); row locks end at transaction end (B1/B2; Revision 17 lease instruction) |
| `locked_by` | `text` | NULL | publisher instance id |
| `lease_token` | `uuid` | NULL, UK | fresh, unique-per-claim fencing token (Revision 17), enforced by `uq_outbox_lease_token UNIQUE (lease_token)` (F8); PostgreSQL's default NULLS DISTINCT lets every non-publishing row share `NULL`; cleared on every exit from `publishing` (B1). Fresh generation remains an application responsibility; the constraint is additional enforcement, not a replacement for fencing |
| `published_at` | `timestamptz` | NULL | |
| `last_error` | `text` | NULL, CHK ≤ 500 | |
| `created_at` / `updated_at` | `timestamptz` | NN, default `now()` | |

Constraints and indexes:
`fk_outbox_org_delivery FOREIGN KEY (organization_id, delivery_id) REFERENCES deliveries (organization_id, id)
ON DELETE CASCADE` — a queued command can never reference another tenant's delivery,
`uq_outbox_delivery_sequence UNIQUE (delivery_id, sequence)` — stops duplicate enqueue of the same
dispatch generation (A2),
`uq_outbox_lease_token UNIQUE (lease_token)` — enforces one live claim per token; `NULL`s are distinct by
default, so all non-publishing rows coexist while two active claims cannot share a token (F8),
`chk_outbox_lease_shape CHECK ((status = 'publishing' AND locked_by IS NOT NULL AND locked_at IS NOT NULL
AND lease_token IS NOT NULL) OR (status <> 'publishing' AND locked_by IS NULL AND locked_at IS NULL
AND lease_token IS NULL))` — the persisted publisher lease is all-or-nothing (B1),
`idx_outbox_claimable ON outbox_messages (available_at, id) WHERE status = 'pending'` —
the claim path, ordered by age,
`idx_outbox_delivery ON outbox_messages (delivery_id, created_at)` — "what has been queued for this delivery",
`idx_outbox_org_recent ON outbox_messages (organization_id, created_at DESC) WHERE status <> 'published'`.

T1 claim shape the publisher runs:

```sql
-- lock candidates first; then sample t = clock_timestamp() and revalidate on the locked rows
SELECT id FROM outbox_messages
WHERE status = 'pending' AND available_at <= clock_timestamp() AND attempts < $2
ORDER BY available_at, id
FOR UPDATE SKIP LOCKED
LIMIT $1;
-- UPDATE ... SET status='publishing', locked_by=$3, locked_at=t,
--   lease_token=<a distinct freshly generated token for each claimed row>,
--   attempts=attempts+1
```

`$2` is the configured per-cycle maximum `M` (a U22 value, not approved here). The claim token is per
row, not per batch: a multi-row claim must generate one fresh token for each claimed row, otherwise
`uq_outbox_lease_token` rejects the duplicate. Publication happens
outside the database transaction and waits for the broker confirm. T2 commits `published` fenced by id,
`publishing` status, `locked_by` and `lease_token`, and clears the lease; a zero-row T2 writes nothing and
does not re-drive. T3 atomically reaps an expired lease. A transport failure or expired lease uses
`GREATEST(A, t + B(c))`; the final permitted claim becomes `failed`, while a claim below the limit
returns to `pending` with backoff. The Sweep alone re-arms `failed` rows, resets `attempts` to 0, and
preserves the gate and `last_error`; `last_error` is cleared only on successful publication. There is no
lifetime publication bound. (B1/B2; Revision 17 `lease_token` instruction.)

### 4.11 `organization_subscriptions` — commercial plan and status

Separate from `organizations` per correction 2, and separate from `endpoint_subscriptions` — the
names are similar but the concerns are unrelated (billing vs. routing). Modelled as an append-only
history of subscription periods so plan changes stay auditable, with at most one open subscription.

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | **UUIDv7 generated by the application before the `INSERT`** — no column `DEFAULT` (U1 approved, revision 16) |
| `organization_id` | `uuid` | NN, FK → `organizations` CASCADE | |
| `plan` | `text` | NN, CHK `free`/`pro`/`enterprise` | the plan label, no longer on `organizations` |
| `status` | `text` | NN, CHK `trialing`/`active`/`past_due`/`canceled`/`incomplete` | subscription state |
| `current_period_start` | `timestamptz` | NULL | NULL for a planless free tier |
| `current_period_end` | `timestamptz` | NULL, CHK `> current_period_start` | billing window |
| `trial_ends_at` | `timestamptz` | NULL | |
| `canceled_at` | `timestamptz` | NULL | |
| `created_at` / `updated_at` | `timestamptz` | NN, default `now()` | |

Constraints: `uq_org_subs_org_id UNIQUE (organization_id, id)` (composite target),
`uq_org_subs_open UNIQUE (organization_id) WHERE status IN ('trialing','active','past_due','incomplete')` —
exactly one non-canceled subscription per organization, while canceled rows remain as history,
`idx_org_subs_org ON organization_subscriptions (organization_id, created_at DESC)`.

The columns deliberately stop short of a billing schema: no invoice, no payment-method, no
provider-object references. Whether quota columns (`max_endpoints`, `max_events_per_day`) or provider
references belong in V1 is unresolved (U19) — enforcement of plan limits is not part of the agreed
decision set.

### 4.12 `audit_logs` — administrative trail (append-only)

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | application-generated **UUIDv7**, no `DEFAULT` (A9, 2026-11-22; U1's rule applies unchanged). The U20 register row is unchanged, and the `bigserial` alternative is not adopted |
| `organization_id` | `uuid` | NN, FK → `organizations` CASCADE | required by the agreed `organizations` 1:N `audit_logs` relationship; V1 audit rows are organization-scoped (A9), so no platform-level NULL row is allowed |
| `actor_type` | `text` | NN, CHK `user`/`api_key`/`system`/`service` | |
| `actor_id` | `uuid` | NULL | polymorphic, intentionally without a FK; `system` actions have NULL |
| `action` | `text` | NN, CHK 1–120 | e.g. `endpoint.created`, `apikey.rotated`, `endpoint_subscription.created`, `organization_subscription.plan_changed` |
| `target_type` | `text` | NULL, CHK 1–60 | |
| `target_id` | `uuid` | NULL | |
| `ip` | `inet` | NULL | |
| `user_agent` | `text` | NULL, CHK ≤ 512 | |
| `metadata` | `jsonb` | NN, default `{}` | diff/summary; never secrets, never payload bodies |
| `occurred_at` | `timestamptz` | NN, default `now()` | no `updated_at` — append-only |

Indexes: `idx_audit_org_time ON audit_logs (organization_id, occurred_at DESC)`,
`idx_audit_target ON audit_logs (target_type, target_id, occurred_at DESC)`,
`idx_audit_action ON audit_logs (action, occurred_at DESC)`.
Grant posture: `INSERT`/`SELECT` for the application role; no `UPDATE`/`DELETE` except through a
dedicated retention role. `organization_id` stays `NOT NULL`: V1 records organization-scoped audit rows
only (A9, 2026-11-22), so platform-level actions are outside the V1 audit trail and no synthetic system
organization or nullable column is introduced.

### 4.13 `idempotency_records` — key → accepted event (U4, U41–U48 approved)

Holds the caller's `Idempotency-Key` for 72 hours: the key, the request fingerprint (§4.13.1), and the
event the accepted request produced. It is its own table because the two lifetimes differ — the key
expires in 72 hours and becomes reusable, while the event and its history live 90 days — and one row
cannot serve both windows honestly.

| Column | Type | | Description |
| --- | --- | --- | --- |
| `id` | `uuid` | PK, NN | app-generated **UUIDv7**, no `DEFAULT` (U1 approved, revision 16); record identity, so takeover can `UPDATE` in place |
| `organization_id` | `uuid` | NN, FK → `organizations` CASCADE | the scope of the key (U4 requirement 1) |
| `idempotency_key` | `text` | NN, CHK 1–200 | caller-supplied value; the header remains **required at the API boundary** |
| `request_hash` | `bytea` | NN, CHK `chk_idem_hash_len` (`octet_length(request_hash) = 32`) | the fingerprint of the request content (§4.13.1) — the replay-vs-409 discriminator |
| `hash_algorithm` | `text` | NN, default `'sha256'`, CHK `= 'sha256'` | explicit algorithm label; pinned to the one value U41 approved, so a future change arrives as a migration that widens the CHECK rather than as ambiguity between old and new rows |
| `event_id` | `uuid` | NULL in flight, **never NULL in a committed row** (U44 invariant), composite FK → `events (organization_id, id)` CASCADE | the event this key produced. Nullable because the claim must precede the event insert (U4 claim-first); `trg_idem_linked` below is what makes the window unobservable from outside the transaction |
| `created_at` | `timestamptz` | NN, default `now()` | start of the 72-hour window |
| `expires_at` | `timestamptz` | NN, CHK `> created_at` | `created_at + interval '72 hours'` — U4's retention window, written by the application so the policy is one constant rather than a per-row expression. **U43 approved the cleanup job that uses this column** (500 per batch, 1 minute, max 5 batches per tick, in `worker`) but left the 72 hours itself as policy with no configuration knob |

No `updated_at`: the row is inserted, has `event_id` filled in the same transaction, and is afterwards
either reset wholesale by a takeover (which rewrites `created_at`/`expires_at`) or deleted by cleanup.

Constraints and indexes:

| Object | Definition | Justification |
| --- | --- | --- |
| `uq_idem_org_key` | `UNIQUE (organization_id, idempotency_key)` | U4 requirements 1 and 5; also the serialization point for concurrent same-key requests |
| `chk_idem_hash_len` | `CHECK (octet_length(request_hash) = 32)` | U41: the column is `bytea` holding a SHA-256 digest, so exactly 32 bytes — no hex/character-length assumption survives into the schema |
| `chk_idem_hash_alg` | `CHECK (hash_algorithm = 'sha256')` | U41: the label stays on the row for explicitness, and pinning it means a stored digest can never claim an algorithm the code cannot reproduce |
| `fk_idem_org_event` | `FOREIGN KEY (organization_id, event_id) REFERENCES events (organization_id, id) ON DELETE CASCADE` | tenant integrity — a record can never point at another organization's event |
| `idx_idem_expiry` | `(expires_at)` | cleanup sweep (U4 requirement 6) |
| `idx_idem_event` | `(event_id)` | cascade lookup when an event is purged, plus reverse lookup "which key produced this event" |
| `trg_idem_linked` | `CONSTRAINT TRIGGER … AFTER INSERT OR UPDATE … DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION idem_assert_linked()` | U44: no committed row may carry a NULL `event_id`, enforced at commit by the database so no code path, backfill or operational write can bypass it. The one new DDL object of revision 10 (snippet below) |

Five points on those choices:

* **The expiry index cannot be partial.** `... WHERE expires_at <= now()` is rejected by PostgreSQL: an
  index predicate must be immutable and `now()` is not. A plain b-tree is the correct shape, and it stays
  small because the table only ever holds 72 hours of keys.
* **A NULL `event_id` skips FK validation — and U44 is what closes that gap.** PostgreSQL does not enforce
  a composite FK when any column is NULL, which is exactly what reservation-first needs: the claim is
  written before the event exists and filled in the same transaction. The same rule means an accidental
  committed NULL would also escape `fk_idem_org_event`, so the bug would be invisible *twice* — no link and
  no tenant check. `trg_idem_linked` is therefore a guard on both properties, not just on the column.
* **`idx_idem_event` is not redundant** despite the column being nullable: without an index on the child
  side, every 90-day event purge cascades into a sequential scan of this table.
* **`request_hash` needs no index.** It is only ever compared against the row the key lookup already
  returned — never searched by value — so adding a b-tree on it would cost writes for no reads. `bytea`
  also compares as raw bytes, so the equality check is collation-independent; a `text` hex column would
  depend on the database collation for every other operation on that value.
* **The trigger fires at commit, so its failure is a commit failure.** A claim that never got linked makes
  the whole accept transaction abort when it tries to commit: no partial event, delivery set or outbox
  batch can survive it, and the client sees an error rather than a `202` for work that does not exist. That
  is the intended posture — the invariant is cheap precisely because the alternative is a broken accept.

`trg_idem_linked` was illustrative DDL when this was written — it was not then in
`db/migrations/`. **Status (revision 17):** the function and trigger now exist in migration unit 13 and
are the artifacts of record (overview §16); the excerpt below remains the design explanation:

```sql
-- U44: a committed idempotency record always points at its event.
CREATE FUNCTION idem_assert_linked() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.event_id IS NULL THEN
    RAISE EXCEPTION 'idempotency_record % committed without event_id', NEW.id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;

CREATE CONSTRAINT TRIGGER trg_idem_linked
  AFTER INSERT OR UPDATE ON idempotency_records
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION idem_assert_linked();
```

Two details that matter when it is eventually written: **`OR UPDATE` is required**, because a takeover of an
expired record rewrites the row and must re-assert the same property; and a constraint trigger only fires
for rows the transaction touched, so it verifies the new state at commit without ever scanning the table.
**Status update from revision 16 (the owner's approval; revision 15 had recorded it prematurely):** U1 is approved with
identifiers generated in the application, so the condition
this paragraph anticipated is now met — an implementation *could* replace the trigger pair with
`event_id uuid NOT NULL`, and §4.13's own note that no backfill or table rewrite would be needed still holds,
because the trigger already guarantees no committed NULL exists. **This revision does not make that change.** U44's
approved guard stays as approved, the column stays nullable in the documented design, and dropping the trigger in
favour of `NOT NULL` is listed in overview §13's residual notes on U1 as a decision for the owner rather than a
consequence that arrived by implication. The application-generated id also means the value written at step d of §5 is
known before the event row exists; that is a convenience for the batched inserts, not a reason to reorder the
claim-first transaction, which U4 fixes as it is.

No route or method discriminator column exists, because V1 has exactly one idempotent endpoint
(`POST /v1/events`) and the key is scoped by organization alone, as approved. If a second idempotent
endpoint is ever added, the unique key must include a discriminator or the two endpoints would collide on
the same key space — worth remembering rather than discovering late.

#### 4.13.1 The request fingerprint (U41 approved, U46 verified)

```
request_hash = SHA-256( UTF-8( JCS( {"event_type": <events.type>, "payload": <request payload>} ) ) )
```

RFC 8785 (JSON Canonicalization Scheme) is applied to the combined content, and SHA-256 runs over the
canonical UTF-8 bytes. `hash_algorithm` records the `'sha256'` half of that pair; the RFC 8785 half is a
property of the code path, not of the row, so there is no `canonicalization_scheme` column — one legal
`hash_algorithm` value plus the CHECK is enough to make old rows unambiguous.

The input is exactly those two fields, wrapped in an object:

* **Wrapped in an object rather than concatenated.** `SHA-256(type || payload)` has a boundary ambiguity:
  `type="a"` + body starting `b{…}` hashes like `type="ab"` + `{…}`. `{"event_type":…,"payload":…}` under
  JCS removes the question entirely, and it also keeps the field set extensible — if a future decision
  adds a third input, the canonical object gains a member instead of growing a second hashing rule.
* **Transport metadata is excluded.** `Authorization`, `Idempotency-Key`, the request timestamp, and every
  other header stay out of the fingerprint. They vary across two legitimately identical retries, and
  including one would turn a replay into a spurious 409. The API key identity is likewise out: the same
  key replayed by a different credential in the same organization is the same request.
* **`event_type` participates directly**, so the same payload under two event types produces different
  fingerprints, as required.

How the required behaviours follow from the scheme. The first four are exactly the ones the approval
enumerated; the fifth is the branch that consumes the digest.

| Requirement | Mechanism |
| --- | --- |
| Key ordering must not matter | RFC 8785 §3.1 writes object members sorted by UTF-16 code unit, so `{"a":1,"b":2}` and `{"b":2,"a":1}` canonicalize to the same bytes |
| Insignificant formatting must not matter | RFC 8785 emits no whitespace between members or separators; two documents differing only in indentation, newlines or spacing produce the same canonical string |
| A different `event_type` must differ | It is a member of the hashed object; changing any string value changes the canonical bytes |
| A semantically different payload must differ | JCS is injective over the JSON data model (§3.2, §3.3), so two documents that differ in any member, value, or array element canonicalize differently — **for inputs the API accepts**; U46 removes the integer-precision counterexample by refusing it at validation, and U48 tracks the decimal one |
| Same org + key + fingerprint → replay; different fingerprint → 409 | §4.13.3, comparing `request_hash = $4` on the row already locked by `FOR UPDATE` |

Two properties of the target column, and the trap they avoid:

* `octet_length(request_hash) = 32` is the length rule, which is exactly the SHA-256 digest width and the
  reason the column is `bytea`. A hex `text` column holds 64 characters (4× the storage for the same
  value) and would need a *character* length CHECK plus an implicit character-set assumption; the earlier
  draft's "fixed length for the algorithm" was that ambiguity written down, and it is gone.
* The CHECK is a guard against an implementation bug (truncated slice, wrong digest length), not an
  encoding hint. Inserting 64 raw hex bytes into the `bytea` column fails it, which is the point.

Canonicalization input and failure handling (both explicit, since a fingerprint is only as stable as the
rules that produce it):

* **Hash the parsed JSON value, not `jsonb`.** PostgreSQL's `jsonb` is *not* RFC 8785: it preserves
  insertion order rather than sorting, and it keeps numbers as written. Canonicalizing
  `events.payload::text` server-side would make the fingerprint depend on storage behaviour the client
  never sent.
* **Invalid or unsupported JSON is rejected before the claim.** A body that is not valid JSON, is not a
  JSON object, repeats an object key, or carries a number U46 excludes (an integer **value** beyond
  ±(2^53 − 1) in any notation — `9007199254740992`, `9007199254740992.0`, `9.007199254740992e15` — or a
  non-finite value) returns **HTTP 400** with no `idempotency_records` row, no `events`
  row, and no fan-out. Revision 14 extended the same boundary — as its own approved decision, not as a change to
  this one — in two directions: a **`payload` that is not a JSON object** (array, string, number, boolean or
  `null`) is rejected the same way, and an integer value that only becomes unsafe **after** the binary64
  conversion is rejected by guard G1 (`9007199254740992.5`), both before the claim and both with existing status
  codes (§4.7, overview §11 items 28 and 30). **Never hash raw bytes as a fallback** — a
  raw-byte fallback makes the fingerprint depend on formatting the moment a client's serializer changes,
  which is precisely the equivalence U41 exists to guarantee. The fingerprint is defined only over content
  that has a canonical form.
* **Duplicate members are rejected, not resolved.** A request object containing the same key twice is
  **HTTP 400** (U46 approved). This closes what would otherwise be a parser-dependent hole: `jsonb` and
  Go's decoder both apply RFC 8259 last-duplicate-wins, so a document with duplicates has no single
  canonical form, and a digest computed over one would encode a rule nobody chose.
* **Numbers follow U46, which makes the double round-trip exact for everything accepted.** RFC 8785
  §3.2.2 serializes through IEEE-754 double precision, and integer values are confined to ±(2^53 − 1) at
  the API boundary, so no accepted integer can be silently re-emitted (§4.7). Decimals follow JCS's
  shortest-round-trip form. Beyond-double decimal precision is the one remaining asymmetry: it is U48, and
  **revision 14 decided it** as Option B with guard G1 approved and guard G2 deferred, so the asymmetry is
  documented and counted rather than refused (§4.7, overview §11 items 27–28, 31).

The fingerprint is computed **after the request body is validated and before `BEGIN`**. It must exist
before the claim (the reservation in §4.13.2 writes it as a parameter), and computing it outside the
transaction keeps JSON parsing and hashing off the row lock that serializes the key.

#### 4.13.2 Reservation-first protocol (U4 requirement 5)

After a `FOR SHARE` organization state recheck, the key is claimed **before** the event is created, inside
the one approved accept transaction owned by the application service (U5, U39/U40):

```sql
BEGIN;
  -- 0. recheck the organization under a share lock; abort unless it is active.
  --    Share locks are compatible with concurrent ingestion, so this does not serialize events.
  SELECT status FROM organizations WHERE id = $2 FOR SHARE;

  -- 1. claim the key. Blocks while a concurrent transaction holds the same (org, key).
  INSERT INTO idempotency_records
         (id, organization_id, idempotency_key, request_hash, hash_algorithm,
          event_id, created_at, expires_at)
  VALUES ($1, $2, $3, $4, $5, NULL, now(), now() + interval '72 hours')
  ON CONFLICT (organization_id, idempotency_key) DO NOTHING
  RETURNING id;

  -- 2. a returned row means this request owns the key: create the event, link it, fan out
  --    INSERT INTO events (...) RETURNING id;
  --    UPDATE idempotency_records SET event_id = $6 WHERE id = $1;   -- closes the U44 NULL window
  --    INSERT INTO deliveries  (...) ; INSERT INTO outbox_messages (...);   -- U5 eager fan-out
COMMIT;   -- 202 only after this (U5). If event_id were still NULL here, trg_idem_linked aborts the commit
          -- (U44), so a linked row is a property of the database, not of the code path.
```

If step 1 returns no row, the key is already held and the request takes the branch in §4.13.3 instead of
writing anything. Uniqueness is enforced by the index, so of N concurrent requests on one key exactly one
can produce an event: the others block on the index entry until the winner commits (then they read the
existing record) or aborts (then they may claim it). A loser never leaves partial rows, because its whole
transaction either rolls back or commits nothing.

Why reservation-first rather than "insert the event, then insert the record and let the conflict abort
it"? Both satisfy the approved guarantees, but the second pays for the entire fan-out before discovering
the duplicate — up to `1 + 100 + 100` rolled-back inserts per retry — which would put dead tuples on
`events`, `deliveries` and `outbox_messages`, the three hottest tables. A client retry storm would
convert an idempotency feature into a vacuum workload. Claiming first rejects duplicates before any
event-shaped write happens, which is also what keeps the 409 path cheap.

#### 4.13.3 Branching on the existing record

```sql
  SELECT event_id, request_hash, expires_at
  FROM idempotency_records
  WHERE organization_id = $2 AND idempotency_key = $3
  FOR UPDATE;
```

`FOR UPDATE` is required here, not decorative: two requests that both find an *expired* record must not
both take it over.

| Existing record | Request content | Behaviour | Writes |
| --- | --- | --- | --- |
| none | — | owner path (§4.13.2) | event + fan-out + record |
| unexpired (`expires_at > now()`) | `request_hash` equal | **replay: `HTTP 202` with the byte-identical original body** plus `HookFlow-Idempotent-Replay: true` (U42 approved) | nothing; commit read-only or roll back |
| unexpired | `request_hash` differs | **HTTP 409 Conflict**, RFC 9457 body with `code = "idempotency_key_conflict"` (U42 approved) | nothing; abort and release the row lock |
| expired (`expires_at <= now()`) | any | key reusable (requirement 6): `UPDATE` the row resetting `request_hash`, `event_id`, `created_at`, `expires_at`, then continue as the owner | full accept for this request |

Replay answers come from the row, not from a stored response: `event_id` selects the event and the body is
`{"event_id", "destination_count", "received_at"}` (overview §5.5). **U44 is what makes this path total** —
with the invariant holding, a committed record always names an event, so the replay branch has no NULL case
to invent behaviour for. `destination_count` and `received_at` are written once at accept and never updated,
which is why "byte-identical" is achievable years later rather than merely "similar".

Takeover **updates** rather than deletes-and-inserts, so the record keeps its identity and the unique
index never sees a gap. The previous event is untouched: its row, deliveries and attempts survive to the
end of the 90-day window, which is requirement 7 expressed in SQL. **U45 approved the consequence**: the
record describes the **latest** use of a key and no earlier use is retained — no history table, and no
per-conflict `audit_logs` row, so a conflict stays a response rather than an event in the audit trail
(§4.13.6).

The single comparison `expires_at > now()` is the entire distinction between "existing unexpired record"
and "expired record eligible for reuse", and it is evaluated while holding the row lock, so a cleanup run
cannot interleave with it. That makes the sweeper a space optimization rather than a correctness
prerequisite: a key becomes reusable at `expires_at` whether or not the job has run — the property U43
approved the job around.

#### 4.13.4 Expiry cleanup (U4 requirement 6, U43 approved)

```sql
-- one batch, one transaction, repeated at most $max_batches times per tick
WITH picked AS (
  SELECT id
  FROM idempotency_records
  WHERE expires_at <= now()          -- database clock, never application time
  ORDER BY expires_at
  FOR UPDATE SKIP LOCKED             -- never wait on an in-flight claim or another sweeper
  LIMIT $batch_size
)
DELETE FROM idempotency_records r
USING picked p
WHERE r.id = p.id
RETURNING r.id;
```

`DELETE` has no `LIMIT` clause in PostgreSQL, so the bounded form is a locking CTE plus `DELETE … USING`.
The CTE establishes the locked row set and the outer statement removes exactly those rows. Deleting a
record can never cascade to `events` (the FK points the other way), so U4 requirement 7 holds by
construction.

**U43 approved the job itself**, not merely the requirement:

| Setting | Value | Name |
| --- | --- | --- |
| Retention | 72 hours (U4 policy, unchanged) | one constant, not scattered literals |
| Host process | `worker` ticker — no additional binary (§3 has no fourth process) | — |
| Batch size | **500** rows | `HOOKFLOW_IDEMPOTENCY_SWEEP_BATCH_SIZE` |
| Interval | **1 minute** | `HOOKFLOW_IDEMPOTENCY_SWEEP_INTERVAL` |
| Max batches per tick | **5** | `HOOKFLOW_IDEMPOTENCY_SWEEP_MAX_BATCHES_PER_TICK` |
| Transaction scope | one transaction per batch, committed independently | — |
| Scan path | `idx_idem_expiry` | existing, no new index |
| Clock | `expires_at <= now()` evaluated by the database | a skewed replica cannot delete early |

Three operational properties follow, and they are the reason for the specific numbers:

* **A short batch ends the loop, a full batch means keep going.** `SKIP LOCKED` silently skips rows another
  sweeper or an in-flight claim is holding, so a batch under 500 does not strictly prove the table is
  drained — but continuing to grind would fight live work for nothing. Correctness never depends on it: a
  key becomes reusable at `expires_at` regardless (§4.13.3).
* **Ceiling and signal.** 500 × 5 × 1440 ticks ≈ 3.6M deletions/day per deployment, so the defaults are not
  a throughput risk; what matters is noticing drift. **Alert on consecutive full-batch streaks** (every
  batch of a tick returning exactly 500, repeatedly) rather than on an absolute backlog count.
* **Failure isolation.** A sweep error is logged and counted, never surfaced to ingestion or delivery, and
  never wrapped inside a delivery or retention transaction. If every `worker` replica is down, the table
  grows and nothing else changes.

**Not decided by U43:** whether the 72-hour window itself becomes configuration the way
`HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG` is. It stays a policy value with no configuration knob in V1.

#### 4.13.5 U47 and U48 — approved in revision 14, with the obligations they carried and the work still pending

**U41 closed in revision 8; U46 in revision 9; U42, U43, U44 and U45 in revision 10; U47 and U48 in revision 14.**
Nothing below reopens U4's policy, U41's contract, U46's validation rules, or the four revision-10 approvals.

* **U47 — JCS parser/canonicalizer selection: approved in revision 14 as path A**, the standard-library
  `encoding/json/jsontext` at the go1.27.4 baseline (overview §11 item 26). No third-party JSON dependency is
  adopted, and none is added here — there is still no `go.mod` to add one to, and this phase writes no manifest. The
  four requirements below were **not** discharged by the selection: they are now obligations of the chosen package
  plus HookFlow's own literal-validation layer, and a candidate that fails any of them breaks the fingerprint
  contract while satisfying every constraint in this document:
  1. **Detect duplicate object keys before the information is lost** — a decoder that resolves
     last-duplicate-wins cannot implement §4.7 rule 3.
  2. **Preserve enough numeric information from the original text to validate before conversion**, i.e.
     expose the literal (`json.Number`-style) rather than producing `float64` during decode.
  3. **Do not rely on a plain `map[string]interface{}` decode**, which silently rounds
     `9007199254740993` to `9007199254740992` and would validate a number the client never sent.
  4. **Judge numbers by mathematical value, not by textual form.** The safe-integer policy applies to the
     value, so all three of these are the same rejected value and must all produce `HTTP 400`:
     `9007199254740992`, `9007199254740992.0`, `9.007199254740992e15`. A check written on integer *syntax*
     passes the second and third and lets them reach `payload` and the digest. The converse error is just as
     real and was corrected in revision 12: **the presence of a `.` or an `e` does not make a value
     fractional.** `1.7e308`, `1E30` and `9223372036854775807` are integer values in exponent notation and are
     rejected by rule 1; `150.0` and `1.5e2` are the value 150 and are accepted; `0.1`, `2e-3` and `1e-27`
     are genuinely fractional and are outside rule 1's bound altogether. **Revision 13 corrects how that
     integrality test is computed**, because trailing zeros decide it: normalize to `D × 10^p` by string work
     (leading zeros stripped, trailing zeros kept), then the value is an integer iff `p ≥ 0` or the trailing-zero
     count of `D` is at least `−p`. On that rule `1.200e1` (`D=1200`, `p=−2`) is the integer 12 and `1.2001e1`
     (`D=12001`, `p=−3`) is not — revision 12's `e ≥ k − 1` formulation misclassified the first. Worked cases:
     overview §15.3.1, and **revision 14 approved this procedure as the canonical one** (overview §11 item 28), so an
     implementation that infers integrality from notation is a defect.
     Acceptance bar: the RFC 8785 §3.2.2 worked example, **Appendix B Table 1** number samples and the §3.2.3
     ordering vectors (the specification has no section 16; see overview §15.2.1), plus the value comparison
     above; a non-conformant canonicalizer makes replays unpredictable. **Revision 13 executed those vectors
     against both viable candidate paths with identical results (overview §15.2.2). Revision 14 approved the
     remaining part as a requirement, not a suggestion: the conformance set — Appendix B, §3.2.2/§3.2.4, §3.2.3, the
     Appendix I sample-value file, V8 cross-checks, fuzzing, and the idempotence/fixed-point assertions — runs in CI
     against the pinned implementation before merge and before production use (overview §11 item 32). It has not
     completed: the Appendix I file, V8 and fuzzing were never run, and everything executed used go1.27.3, not the
     approved go1.27.4 baseline.**
     **Phase 7 conformance correction (2026-11-25, documentation-only; no schema change):** "the Appendix I
     sample-value file" is not a file published by RFC 8785 — Appendix I is the **Development Portal**, a pointer to
     external test-data repositories, and that external corpus was not imported. The conformance inputs are the RFC's
     own §3.2.2, §3.2.3, §3.2.4 and Appendix B Table 1 vectors, now executed locally together with an independent
     Node.js `JSON.stringify`/Appendix A comparison (build tag `v8conf`) and a 60-second bounded `FuzzCanonicalize`
     gate. The dedicated `jcs-conformance` CI job runs all three and fails closed on a missing fixture or runtime;
     hosted execution for the pushed commit is identified in the Phase 7 closure report, and this is **not** a claim of
     exhaustive RFC conformance. See overview §16.11.
* **U48 — decimal precision beyond the double round-trip: approved in revision 14 as Option B, with guard G1 and
  nothing more** (overview §11 items 27–28). An accepted decimal with more
  significant digits than an IEEE-754 double can hold is stored by `jsonb` as written and canonicalizes to
  its rounded form, so two such requests **replay** rather than conflict and the delivered body can differ
  from what the digest covered. Of the three options on the table (cap significant digits, store the canonical
  form, or document the asymmetry permanently) the **third was chosen**, plus G1's rejection of the unsafe-integer
  case. **G2 is deferred, not adopted** — V1 neither implements nor requires exact decimal-literal preservation — and
  the residue is counted by the approved counter instead (§11 item 31). The obligation stays on producers where U46
  rule 2 put it: exact decimals are sent as JSON strings (§4.7, RFC 8785 §3.1 and Appendix D).
  **U46 was not stretched to cover it, and U46
  must not be reinterpreted later to resolve it** — note the boundary with U47 requirement 4, which is
  about integer *values* and is settled by U46, whereas U48 is about fractional precision and is now decided without
  touching it.
* **Document and `payload` shape — approved in revision 14** (overview §11 item 30, §4.7 above). The request
  document's top level and the `payload` value must both be JSON objects; an array, string, number, boolean or
  `null` `payload` is `400 invalid_json_document` before the claim, and unrecognised members inside `payload` are
  accepted because no member name is reserved. `jsonb` accepts every JSON value, so this is an API-boundary rule
  with no `CHECK` — exactly like U46's — and it is enforced at §15.3 step 3.
* **Inexactness counter — approved by name in revision 14** (overview §11 item 31, §7 there).
  `hookflow_ingest_number_inexact_total` is an observability counter incremented at parse stage 8 when the canonical
  decimal differs from the literal's; it is **not** a validation threshold, carries no numeric limit in the contract,
  and never becomes a column, a stored field or a response member.
* **Conformance gate — approved in revision 14 as a merge requirement** (overview §11 item 32). It is stated with
  requirement 4 above and in §4.13.7; what has run and what has not are listed there, and the CI mechanism itself is
  U30's still-open half.

**Revision 12–14 pointers — what revision 14 made canonical, and what these notes still do not decide.** Both
decisions were carried as proposals in
[overview.md §15](./overview.md#15-u47-parser-selection-u48-numeric-policy-and-the-ingestion-parse-order-revision-14-approvals-and-open-remainders),
and revision 14 approved them; the evaluation below is kept in full because it is the reason for the choices, not
because the choices are still open:

* **U47** — evaluated against the four requirements above (matrix, per-candidate documentation and cited evidence
  in overview §15.2 and §15.2.1). The structural result: **no candidate satisfies U46 by itself.**
  `encoding/json/jsontext` documents that `Value.IsValid` does not check whether numbers fit any numeric type,
  that `CanonicalizeRawInts` loses precision past ±2⁵³ (`1234567890123456789` → `1234567890123456800`), and that
  `CanonicalizeRawFloats` **saturates** anything beyond ±1.7976931348623157e+308 instead of rejecting it — and all
  three were **executed** in revision 12 against Go 1.27.3's stdlib (`1e400` canonicalized to
  `1.7976931348623157e+308` with `err=nil`; `1e-400` → `0`; `-0.0` → `0`; a duplicate name errored on both the
  token walk and `Canonicalize`). The cyberphone canonicalizer routes every number through `strconv.ParseFloat`
  before serializing; `ucarion/jcs` takes a `map[string]interface{}`, the decode path requirement 3 forbids. So
  the literal-level validation pass is HookFlow's own code under **every** option, and a canonicalizer that
  clamps `1e400` would otherwise forge a digest over a value rule 5 requires us to reject. Three requirements sit
  on top of the four (`-0` → `0`; raw-byte digest assembly with no `float64`/map in the path; one pinned
  canonicalizer gated by the conformance set in overview §15.2.1). A Go 1.24 pin is satisfiable today only through
  `github.com/goccy/go-json/jsontext` (`go 1.23`) or a hand-written serializer — overview §15.2 carries the
  toolchain evidence, which **informed U30**; revision 14 decided U30's toolchain half as **go1.27.4**, so neither
  1.24-compatible route was taken and neither is needed.
  **Revision 13 adds the executed comparison:** overview §15.2.2 ran fourteen case groups — the §3.2.2/§3.2.4 golden
  document, the §3.2.3 UTF-16 ordering vector, all of Appendix B Table 1, duplicates, raw-literal preservation,
  overflow/underflow, lone surrogates, string escaping, hostile-size inputs and document-level acceptance — against
  **both** the Go 1.27.3 stdlib package **and** `github.com/goccy/go-json/jsontext` v0.12.0, and the two produced
  byte-identical results and identical error text on every one. **Revision 14 approved path A, the
  standard-library package, together with U30's toolchain half as go1.27.4** (overview §11 items 25–26); the backport
  is recorded and **not adopted**, and no JSON dependency is added anywhere. Nothing is pinned in a manifest because
  no manifest exists, and the gate is now an approved merge requirement rather than a suggestion: it **has not
  completed** — the Appendix I sample file, V8 cross-checks and fuzzing were never executed here, and every executed
  case ran on go1.27.3, not on the approved baseline (overview §11 item 32).
* **U48** — the approved policy (revision 14) is Option B **with the single post-conversion guard G1**, because the
  literal-level rule alone is provably insufficient: `9007199254740992.5` is a fractional literal whose double is the
  unsafe integer
  `9007199254740992`, and it canonicalizes without error. So:
  * **G1 (approved in revision 14, and necessary):** reject when the converted double reaches `abs(f) ≥ 2^53`, reported as
    `integer_out_of_safe_range` — no new status code, and no DDL. Revision 12 also required the double to be
    integral; that clause is dropped in revision 13 because every binary64 at or above 2^53 already **is** an
    integer, so the two formulations decide the same set of values. G1 is stage 7 of the approved parse order.
  * **G2 (deferred — not adopted; revision 14 ratified the deferral):** additionally reject unless the canonical text denotes exactly the
    literal's decimal value, which would also catch `1e-400` → `0` and `1.234567890123456789` →
    `1.2345678901234567`. It would make the invariant "the digest covers exactly the decimal value `payload`
    stores" literally true, and revision 14 declined it: it breaks no approved rule, the `jsonb` column keeps the
    client's digits regardless, it rejects input every compliant JCS canonicalizer accepts, and it would add a
    permanent `400` code where a counter gives the same information reversibly (overview §15.1.1). **V1 neither
    implements nor requires exact decimal-literal preservation.**
  * **`payload` object rule (approved in revision 14, overview §11 item 30):** the accepted document must be a JSON
    object at its top level **and** at `payload`; an array, string, number, boolean or `null` `payload` is refused
    with the existing `invalid_json_document`, unrecognised members inside `payload` are accepted, and no `CHECK`
    expresses any of it — `jsonb` would store `"123"` or `[1,2]` without complaint (§4.7).
  Either way this section's shape is unchanged: `payload` stays `jsonb` holding the client's accepted document,
  `request_hash` stays the 32-byte SHA-256 of the canonical form, and the two are **not** textual inverses —
  notation may differ from the literal (`1.0` → `1`), and under the approved B+G1 the stored decimal may be more
  precise than the canonical one. Revision 11's ">15 significant digits" framing is withdrawn: it is a proxy, and `1e-400`
  (one digit) breaks it. **Revision 13 corrected how integrality is computed and revision 14 approved that
  procedure as canonical**, since it is what decides U46
  rule 1: normalize the literal to `D × 10^p` by string work only — `D` keeps its trailing zeros, `p` = written
  exponent − fraction digits — then the value is an integer iff `p ≥ 0` or `tz(D) ≥ −p`, with `len(D) + p` decimal
  digits. Revision 12's `e ≥ k − 1` test on a `0.d1…dk` form called `1.200e1` fractional; the corrected test and the
  nine worked literals are in overview §15.3 and §15.3.1, and the digit count is compared against the 16-digit
  bound `9007199254740992` without ever materializing a large integer.
* **What a reader must not infer from §15 or from these approvals.** None of the revision-14 approvals adds a
  column, constraint, index or table, and none names a dependency in a manifest.
  `idempotency_records` keeps its eight columns; `request_hash` stays `bytea` under `chk_idem_hash_len`;
  `payload` stays `jsonb`; the inexactness counter approved under U48 is a metric, not a stored field, and never a
  response member; and the go1.27.4 baseline exists **in documentation only** until a future `go.mod`, CI job and
  Dockerfile carry it — this phase wrote none of those. The ERD changed only comment lines and one attribute note
  for the same reason.

#### 4.13.6 What the revision-10 approvals changed in this schema

Recorded so the four approvals are traceable to concrete artifacts rather than to prose. U46 is included
because it was verified against its checklist in the same pass.

| Decision | Canonical home in this document | Schema effect |
| --- | --- | --- |
| **U42** HTTP response contract | §4.13.3 branch table; §5 flow step 6; overview §5.5 | **none.** The replay body comes from `events (id, destination_count, received_at)`, all immutable after accept, so no response is stored — storing one would put API shape into the data model and make a future body change unreadable against old rows. `updated_at` is excluded from the body for exactly this reason |
| **U43** Expired-record cleanup | §4.13.4 (SQL, settings table, operational properties); §6 retention row; §7 | **none.** `idx_idem_expiry` already serves the sweep; the batch shape replaced the earlier `WHERE id IN (SELECT … LIMIT …)` form, which is a query change, not a DDL change |
| **U44** Committed-NULL invariant | §4.13 constraints (`trg_idem_linked` + `idem_assert_linked()`), §4.13.2, §7 unit 13, §8 consequence 20 | **one function and one constraint trigger** inside migration unit 13. `event_id` stays nullable, and the composite-FK validation gap that a NULL column opens is closed by the trigger rather than by `NOT NULL` — because choosing `NOT NULL` would pre-decide U1 |
| **U45** No key history | §4.13.3 (latest-wins takeover), §8 consequence 14 | **none**, and explicitly no history table. The per-conflict `audit_logs` row floated in the proposal was **not adopted**, so `audit_logs` gains no action and the 409 path still writes nothing in any transaction — U4's approved semantics are untouched |
| **U46** JSON numeric and parsing policy | §4.7 rules 1–5, §4.13.1, §2 conventions | **none**, unchanged from revision 9 — verified in place: safe range `±9007199254740991`, strings for larger magnitudes, duplicate keys rejected, RFC 8785 decimal serialization, non-finite and non-representable values rejected, `HTTP 400` before any claim, no raw-body hash fallback, `jsonb` never the canonicalization source |

#### 4.13.7 What the revision-14 approvals changed in this schema

Same convention as §4.13.6: each approval traced to a concrete artifact rather than to prose. The honest headline is
that **all seven rows have no DDL effect** — every one of them lands on the API boundary or in the toolchain, which
is the same place U46 and U41's non-negotiables already live.

| Decision | Canonical home in this document | Schema effect |
| --- | --- | --- |
| **U30** toolchain half (go1.27.4) | §4.13.5 (path A depends on it), overview §11 item 25, §15.6 | **none, and nothing installed.** The pin exists in documentation; a future `go.mod` `go` directive, CI `go-version` and Dockerfile base image are where it will materialize, and none of those files exists or was created here. Whether golangci-lint works with this baseline is **unverified**, and U30's lint/CI half stays open |
| **U47** parser/canonicalizer | §4.13.5 requirement 4 and the acceptance bar, §4.7, overview §11 item 26, §15.2.2 | **none.** A standard-library package chosen instead of a module: `request_hash` stays `bytea` under `chk_idem_hash_len`, `hash_algorithm` stays pinned to `'sha256'`, and no dependency is named in any manifest. The four parser requirements plus the three extra ones survive the selection as obligations of the code path |
| **U48** Option B + **G1 approved**, **G2 deferred** | §4.7 (rules 1–5 unchanged, plus the new paragraphs), §4.13.5, overview §11 items 27–28, §15.1.1/§15.3/§15.3.1 | **none.** `payload` remains `jsonb` holding the client's accepted document; G1 reuses `integer_out_of_safe_range`; because G2 was not adopted, `number_not_exactly_representable` stays out of the contract and **no new status code or column exists** |
| `payload` must be a JSON object; unknown `payload` members accepted | §4.7 (the shape paragraph), §4.13.1, §4.13.5, overview §11 item 30, §5.1 step 3, §15.3 step 3 | **none deliberate.** `jsonb` accepts every JSON type, so the rule is boundary validation, not a constraint — the same reason U46 has no `CHECK`. Recorded here because the obvious tempting DDL (`CHECK (jsonb_typeof(payload) = 'object')`) would be **redundant** at best: nothing can be inserted that the API did not already accept, and a `CHECK` would suggest the database is the enforcement point when it is not |
| **`hookflow_ingest_number_inexact_total`** approved by name | §4.7, §4.13.5, overview §11 item 31, §7 there, §15.3 stage 8 | **none.** A counter is not a column, not a table and not a response member; it carries **no numeric threshold** in the contract, and no alert value was invented |
| **Parse order** (§15.3 steps 1–8) approved as normative | §4.7, §4.13.1, §4.13.5, overview §11 item 29, §5.1 there | **none.** Steps 1–7 stay before `BEGIN` and step 8 is the unchanged claim-first transaction (U4, U42, U43, U44, U45); `maxBodySize` remains **U17**'s unset value |
| **Conformance gate** approved as a merge requirement | §4.13.5 requirement 4 + acceptance bar, overview §11 item 32, §10 there | **none.** It is test machinery. Status of the evidence: executed on go1.27.3 for the vectors listed in overview §15.2.2; the Phase 7 package executes its implemented suite locally on go1.27.4; **pending** for the Appendix I sample file, V8 cross-checks, sustained fuzzing and hosted-CI execution — so the full suite has not passed and production use must wait for it |

## 5. Fan-out and message lifecycle

The transaction boundary below belongs to an **application service, not to a module** (U39 approved,
located in `internal/app/event` as `package eventapp` per U40/U28): one use case, one Unit of Work, one
`pgx.Tx`, with the event,
subscription, delivery and outbox repositories all handed that same transaction and none of them
calling `Begin`/`Commit`.

```text
POST /v1/events (Idempotency-Key required)
  handler -> eventapp.AcceptEvent opens ONE Unit of Work (U39/U40)
  0. auth + scope check; live-endpoint ceiling check is at endpoint create, not here (§4.5.2)
  0.5 parse + validate the JSON body, then request_hash = SHA-256(UTF-8(JCS({"event_type","payload"})))
      -- U41: done BEFORE BEGIN, so hashing never holds a row lock (§4.13.1)
      -- U46 validation gates the digest: valid JSON, an object at the top level, no duplicate keys,
      -- every integer value within +-2^53-1 judged by mathematical value - so 9007199254740992,
      -- 9007199254740992.0 and 9.007199254740992e15 are all rejected (larger magnitudes must arrive as
      -- strings), decimals per RFC 8785, no non-finite values. Each violation returns 400 here and
      -- creates no idempotency claim, no event, and no raw-byte fallback; number literals must be
      -- inspected before any float64 decode (§4.7)
      -- Approved in revision 14 (overview 11 items 29-30; overview 15.3 is the normative order):
      -- payload itself must also be a JSON object - an array, string, number, boolean or null payload
      -- is 400 invalid_json_document - and guard G1 runs AFTER the binary64 conversion, rejecting any
      -- accepted number whose abs(f) reaches 2^53 (9007199254740992.5) with integer_out_of_safe_range.
      -- Canonicalizer is encoding/json/jsontext at the go1.27.4 baseline (no JSON dependency added);
      -- hookflow_ingest_number_inexact_total increments on accepted-but-coarsened numbers and can
      -- never change a verdict. maxBodySize is still U17's unset value, so step 0.5 names no size.
      -- U1 (approved in revision 16): every id written below - this record's, the event's, each
      -- delivery's and each outbox row's - is a UUIDv7 value generated by the application BEFORE the
      -- INSERT. No id column carries a DEFAULT, so every INSERT names its own id explicitly, and an
      -- omitted id is a NOT NULL failure, not a database-generated surprise. ids are not ordering
      -- inputs: due-time scans use next_attempt_at and available_at, key reuse uses expires_at.
  BEGIN
    -- 1. recheck the organization under a share lock before any work:
    --    SELECT status FROM organizations WHERE id = $1 FOR SHARE
    --    missing or not active => deny and roll back, writing no idempotency claim and no event
    -- 2. claim the key FIRST, so a duplicate never pays for the fan-out (§4.13.2)
    INSERT idempotency_records (organization_id, idempotency_key, request_hash, hash_algorithm,
                                event_id, created_at, expires_at = now() + interval '72 hours')
      VALUES (..., <the bytea computed at step 0.5>, 'sha256', NULL, ...)
      ON CONFLICT (organization_id, idempotency_key) DO NOTHING RETURNING id
      -- no row returned => SELECT ... FOR UPDATE on the existing record, then branch (§4.13.3):
      --     unexpired + same request_hash      -> replay: read the event by event_id (never NULL, U44)
      --                                          -> 202 + byte-identical body + replay header, write
      --                                             nothing (U42)
      --     unexpired + different request_hash -> 409 Conflict (RFC 9457, code
      --                                          idempotency_key_conflict), roll back, write nothing (U42)
      --     expired                            -> UPDATE the record (new hash, new window) and continue
    -- A3: with the claim-row lock held, sample t = clock_timestamp() once; every later scheduling
    --     predicate and every scheduling value written below (deliveries.next_attempt_at and
    --     outbox_messages.available_at) uses that same t, never a second now() reading. The 72-hour
    --     claim window written above uses now(), and events.received_at / expires_at below use now()
    --     and now() + interval '90 days'; those are retention and response facts, not scheduling inputs
    -- 3. destination set resolved before the event row, so destination_count is known at insert
    SELECT s.id, e.id, e.url, e.http_headers, e.secret_encrypted
      FROM endpoint_subscriptions s JOIN endpoints e ON e.id = s.endpoint_id
      WHERE s.organization_id = $1 AND s.event_type = $2      -- exact match, approved (U16)
        AND s.is_active AND e.deleted_at IS NULL AND e.status = 'active'
    INSERT events (id, organization_id, type, payload, destination_count, received_at, expires_at, ...)
      -- id is the application's own UUIDv7 value (U1, revision 16); no DEFAULT exists to fall back on
      -- received_at = now() and expires_at = now() + interval '90 days' (event store): retention and
      -- response facts, deliberately not the A3 scheduling sample t
      -- no status column (U36) and NO idempotency_key column (U4): the key lives only in
      -- idempotency_records, and destination_count = rows found above (0 is a valid accepted value)
    UPDATE idempotency_records SET event_id = <new event id> WHERE id = <claimed record id>
      -- U44: this link is what the deferred trigger verifies at commit; an unlinkable claim cannot commit
    IF destination_count > 0:
      INSERT deliveries (event_id, endpoint_id, endpoint_subscription_id,
                         url, http_headers, secret_encrypted, state='pending',
                         dispatch_seq=1, next_attempt_at=t)
        -- one row per matched destination; ON CONFLICT (event_id, endpoint_id) DO NOTHING
        -- these columns ARE the destination snapshot (U33) - no copy is written to events
      INSERT outbox_messages (organization_id, delivery_id, kind='delivery.dispatch',
                              sequence=1, available_at=GREATEST(t, deliveries.next_attempt_at))
        -- one row per delivery created above; t is sampled after the claim-row lock and reused
    ELSE:
      -- zero matching endpoints: no deliveries, no outbox_messages, but the event row is committed
      -- and the caller still gets 202 (U36 approved)
  COMMIT                     <- key claim, event, link, fan-out and dispatch intent become visible together
                               <- trg_idem_linked runs here and aborts an unlinked claim (U44)
  RETURN 202 + {"event_id", "destination_count", "received_at"}   <- only after COMMIT (U5); body per U42
```

Nothing is published to RabbitMQ inside this transaction, and no HTTP call is made from the request
path: the client's `202` means the work is durably recorded, not that any destination has been
contacted. If the process crashes after `COMMIT` but before publishing, the rows are still `pending`
and the `outbox-publisher` picks them up — that is the whole point of the outbox.

U5 approved eager fan-out, so `deliveries` exists at accept time rather than being materialized
lazily by the worker. Two consequences are worth keeping in view: the request path now performs
`2 + N + N` inserts (one idempotency record, one event, and up to N destinations each for deliveries
and outbox messages, bounded by the live-endpoint ceiling of §4.5.2), and the destination set is
frozen the instant the event is accepted, which is what makes "subscription changes must not alter
already accepted events" a structural property of the row rather than a behaviour of the worker.

U4 approved the idempotency policy, and the diagram above is what makes requirements 3–5 atomic rather
than merely ordered. The key claim, the event, the record→event link, the fan-out and the outbox rows all
live in **one** PostgreSQL transaction, still owned by `eventapp.AcceptEvent` (U39/U40 unchanged): a
concurrent loser either blocks on `uq_idem_org_key` and then reads the committed winner, or aborts
without writing anything, so no partial event, delivery set or outbox batch can survive a lost race. The
replay and 409 branches write nothing at all — they are read-only paths through the same transaction,
which is why a rejected duplicate produces no dead tuples. **U45 approved keeping it that way**: no history
table and no per-conflict `audit_logs` row, so there is no side-channel write that would need an exception
to this sentence. The one thing deliberately **not** inside the
transaction is the response: `202` and the replay response are emitted after `COMMIT`, so a client can
never observe a key that later rolled back (§4.13.2).

U33 approved storing the snapshot per delivery, so the accept transaction above writes the resolved
destination columns directly into each `deliveries` row and writes nothing destination-shaped into
`events`.

U36 approved removing `events.status` entirely: acceptance is expressed by the row existing, and there
is no `discarded` state — an event that matched nothing is an ordinary accepted event with
`destination_count = 0`. Consequently `deliveries.state` is the only processing-state machine in the
system, and overall event progress is an aggregate over it (`idx_deliveries_event`). The equality
`events.destination_count = count(deliveries WHERE event_id = ...)` is now a stated invariant rather
than a convenience — the two are written in the same transaction, so a mismatch is a bug, and it
deserves a test.

U4 is approved, so the behaviour of a duplicate is fixed (replay when `request_hash` matches, 409 when it
differs); the status code and body are fixed too, by U42 (approved revision 10; §4.13.3). U6's storage
question is resolved: `deliveries.secret_encrypted` is a copy-to-delivery snapshot (A8, §4.5.1); the D6/U25
envelope and custody architecture is Owner-approved (2026-11-24) and implemented in development under a local
file KEK, while production KMS/Vault custody is not implemented. Rotation does not rewrap the snapshot (A8
immutability); endpoint envelopes may be rewrapped, and retirement is reference-based rather than calendar-based
(D6-D is **OWNER APPROVED** as architecture; production retirement tooling is **NOT IMPLEMENTED**).

**Recovery Sweep duties and order (B2, owner-approved 2026-11-22; overview §12.16).** The Sweep runs four
passes in this order: **(1) expire** eligible Deliveries, **(2) repair** missing Outbox messages, **(3)
re-arm** eligible failed Deliveries and their failed Outbox rows, and **(4) redispatch** stale generations.
It is the sole owner of re-arming failed Outbox/Delivery work and of resetting the publisher retry cycle;
every pass is idempotent, and a candidate found no longer eligible is committed with no write. Each pass
claims **one candidate per short transaction**, using the approved locking form (`FOR UPDATE … SKIP LOCKED`)
with a deterministic cursor: `(created_at, id)` for Delivery candidates and `(sequence, id)` when a
candidate's Outbox rows are locked. The Sweep locks the Delivery **before** its Outbox rows, samples fresh
`clock_timestamp()` after the lock, revalidates the time predicates against that sample, and follows the
RB-2 derivation below (a new repair/redispatch row uses `GREATEST(t, next_attempt_at)`; re-arming an
existing failed row uses `GREATEST(available_at, t, next_attempt_at)`). Approved rollback, defer and
conflict-revalidation behaviour, and the per-pass reserved-capacity budget, are preserved. Per-pass
candidate caps, per-pass and total duration budgets, and the stale-published threshold are explicit
required configuration with validation; their **numeric values remain unresolved** unless the owner
approves them elsewhere. Recovery may also expire a Delivery while a Worker still holds a valid lease —
the precedence rule is stated below the lifecycle diagram.

```text
outbox-publisher: pending outbox_messages --FOR UPDATE SKIP LOCKED--> RabbitMQ --> status 'published'
                  -- each pass claims a pending row in place: UPDATE of status/attempts/lease, its id never changes (§4.10)
worker:           consume -> load delivery + snapshot -> claim the worker lease (validate generation and
                  live state, then set dispatch_lease_token / dispatch_lease_expires_at)
                  -> decrypt secret
                  -- pre-HTTP abort (B4 insufficient fixed-lease budget or B5 decryption failure):
                  --   no HTTP, no delivery_attempts row, no attempt_count increment
                  -> HTTP POST
                  -> observe result (genuine HTTP outcome), then (one transaction, fenced) write:
                    INSERT delivery_attempts (attempt_number, response_status, duration_ms, outcome,
                                              started_at=<observed start>, finished_at=<observed end>)
                    attempt_count = attempt_count + 1   -- one per completed attempt
                    2xx                 -> state 'succeeded', completed_at=t, next_attempt_at=NULL,
                                           clear the worker lease
                    5xx/408/429/network -> state 'retrying', next_attempt_at=due
                                          dispatch_seq = dispatch_seq + 1
                                          INSERT outbox_messages (kind='delivery.retry',
                                            sequence=dispatch_seq,
                                            available_at=GREATEST(t, deliveries.next_attempt_at))
                                            -- a NEW row, not a re-stamp: id is the worker's own UUIDv7 for this INSERT (§4.10)
                                          clear the worker lease
                    other 4xx           -> state 'failed', completed_at=t, next_attempt_at=NULL,
                                           clear the worker lease   (terminal, no retry)
                    event TTL hit after HTTP -> state 'expired', completed_at=t, next_attempt_at=NULL,
                                           clear the worker lease   (events.expires_at is the horizon)
```

**Pre-HTTP aborts are not completions.** An insufficient fixed-lease budget (B4) or a decryption failure
(B5) starts no HTTP request and writes no `delivery_attempts` row or `attempt_count` increment; B5's
disposition is stated below. The `expired` branch shown is the post-HTTP event-TTL case.

**C-2 completion fence and fixed lease (B3/B4).** The worker lease is fixed rather than renewed: with
`L` the lease, `D` the bounded claim-commit/pre-HTTP time, `H` the enforced full HTTP deadline (connection
setup, execution and bounded response handling), `P` the bounded DB completion budget and `S` a margin,
`L > D + H + P + S`; there is no routine heartbeat or renewal, and the numeric value is not approved. The
worker must not start HTTP when insufficient lease budget remains, and cancellation on expiry is
best-effort — it is not proof that the remote operation stopped. Acquisition and expiry checks use a fresh
`clock_timestamp()` sample, not transaction-start `now()`. A completion is atomic and valid only for
the organization and delivery being completed, the current `dispatch_seq`, a matching
`dispatch_lease_token`, a live delivery state and
the event-expiry rules. The attempt insert, the attempt count, the delivery mutation, the lease clearing
and any retry outbox row commit together. A fenced worker writes nothing. When the completion decision is
made before the event expires, the HTTP result and retry policy apply. When it is made at or after expiry,
the genuine HTTP outcome is still persisted, but the delivery becomes `expired` and no retry row is
created; a terminal delivery is never rewritten. A genuine HTTP success is never converted into an HTTP
failure.

**Recovery event expiry takes precedence over an active worker lease (B2/B3; owner-approved 2026-11-23,
overview §12.18).** Recovery Sweep may transition a Delivery to `expired` when its Event has expired **even
if a Worker still holds a valid dispatch lease**, and event expiry outranks the existence of that lease. The
transition is the approved expiry pass: `state='expired'`, `completed_at` set, `next_attempt_at` cleared,
the paired worker-lease tuple cleared, **no `delivery_attempts` row, no `attempt_count` increment, no retry
Outbox message and no new dispatch generation**. A Worker that presents its now-stale claim to `Complete`
afterwards is rejected by fencing: it inserts no attempt, mutates nothing, and cannot revive the expired
Delivery. This does **not** remove B3's late-lease completion semantics: where the Sweep has **not** already
transitioned the Delivery and the Worker presents the matching valid fencing token under the existing
contract, B3 applies unchanged. The decisive distinction is whether Recovery has already performed the
expiry transition and invalidated the old claim.

**Decrypt failure (B5).** A decryption failure that happens before any HTTP call is a durable
processing-failure disposition, not an attempt: reacquire the row lock and validate the fence, then sample
fresh database time and read the **authoritative** `dispatch_lease_expires_at` from the locked row (never
the caller's claim or an application clock). Check the event horizon first and the worker lease second; if
either requires terminal disposition, persist the delivery as `expired` using the same fence, with no
attempt row, no `attempt_count`/`dispatch_seq` change and no retry/outbox row. Otherwise persist a message
in `deliveries.last_error` that is bounded to the existing 500-character limit after UTF-8-safe
normalisation (no split runes) and deterministic heuristic credential scrubbing; the scrubbing is
best-effort for structured secrets and is not a guarantee for arbitrary binary payloads. Insert no
`delivery_attempts` row and do not increment `attempt_count`. Apply positive processing pacing via
`next_attempt_at`. If the next paced retry would reach the event horizon, keep the delivery live with
`next_attempt_at = events.expires_at`, clear the worker lease and create no dispatch; the expiry pass
terminalizes it. Clear the worker lease, commit, and ACK only after the durable commit. This uses existing
columns and states only — no new table, column, status or kind — and no NACK.

The outbox stays transactional while carrying several messages per delivery across its retry life:
one `delivery.dispatch` from the accept transaction, then one `delivery.retry` per backoff step.

## 6. Retention (90-day agreed window, 72-hour idempotency window)

| Table | Retention | Basis | Notes |
| --- | --- | --- | --- |
| `events` | 90 days | `received_at` | purged oldest first |
| `deliveries` | 90 days | `created_at` | removed with their parent event |
| `delivery_attempts` | 90 days (inherits) | `started_at` | must be purged **before** `deliveries` |
| `outbox_messages` | short, e.g. 7 days | `created_at` | queue table, not history; no product reason to keep 90 days of published rows (U2) |
| `idempotency_records` | **72 hours** (U4 approved) | `expires_at` | deleted independently of event history; a delete here must never touch `events` (requirement 7). **U43 approved** the job: `worker` ticker, 500 per batch, 1-minute interval, at most 5 batches per tick, one transaction per batch (§4.13.4) |
| `audit_logs` | separate policy, proposed ≥ 365 days | `occurred_at` | compliance retention is a product decision (U2) |

The two windows are deliberately unrelated: a key expires and becomes reusable three days after it was
used, while the event it produced remains queryable for the full 90 days. That asymmetry is the reason
idempotency data no longer lives on `events`.

Purge order follows the cascade directions of §3.2:
`outbox_messages` → `delivery_attempts` → `deliveries` → `events`, with `idempotency_records` swept
separately on its own 72-hour clock (it is a child of `events`, so an event purge would cascade to it
anyway — harmless, since that row expired ~87 days earlier). Deleting in the reverse order
would rely on cascades to clean the children, which turns each purged event into an unbounded
multi-table cascade — the failure mode that makes a retention job lock a hot table.

V1 uses non-partitioned tables with a bounded, batched purge (A5, 2026-11-22); monthly range-partitioning
of `events`/`deliveries`/`delivery_attempts` is deferred. Partitioning would force the primary key to
`(id, created_at)`, which is exactly what breaks the `UNIQUE (organization_id, id)` composite-FK targets.
The purge deletes a bounded number of rows per table per batch and commits once per batch, so no single
transaction holds an unbounded set of locks. Which tables the purge covers and the batch-size values
remain open (U2). The deletion order above is about referential integrity, not about the Recovery Sweep's
lock order (B2; §4.10; Revision 20 §4.4); the two are independent.

**Retention is not permission to retire a KEK. [R] + [E]** `endpoints` does not appear in this table at all: it is
soft-deleted, and no endpoint purge is documented or implemented. A soft-deleted `endpoints` row retains
`secret_encrypted` (§4.5), so its envelope can reference a `kid` indefinitely. The 90-day Events/Deliveries window is
a horizon for snapshots, not a calendar on which a key may be destroyed. Under the Owner-approved reference-based
rule (D6-D, 2026-11-24), a KEK version remains decryptable while **any** retained endpoint envelope — including a
soft-deleted row — or delivery snapshot references it; retirement requires all such ciphertext actually purged and a
bounded reference audit at zero, with unknown/unparseable values blocking retirement. This repository has no
Events/Deliveries/Attempts purge (only the U43 idempotency sweep is wired) and no endpoint purge; the AAD/envelope
codec exists only in the development file-KEK runtime, and there is no production custody or retirement tooling, so
**KEK retirement is operationally blocked today**. See [`d6-u25-decision-proposal.md`](./d6-u25-decision-proposal.md)
§4.4 and the 2026-11-24 reconciliation note at the top of this document.

## 7. Migration build order

Each unit is authored as a matching pair of `up` and `down` files in the same step; a unit is not
complete with only one direction (owner requirement, 2026-11-22). This is the agreed inventory order, and
it is already a valid dependency order — `users` and `organizations` are independent of each other, and
every later table has its parents already defined:

```text
1  users
2  organizations
3  organization_members
4  api_keys
5  endpoints
6  endpoint_subscriptions
7  events
8  deliveries
9  delivery_attempts
10 outbox_messages
11 organization_subscriptions
12 audit_logs
13 idempotency_records
```

`idempotency_records` (13) is last among the DDL units because its composite FK needs `events` (7) to
exist; the original 12-table order is otherwise untouched, so the previously approved build sequence stays
valid.

Two ordering requirements to respect when writing the files: `api_keys` (4) references `users` (1)
and `organizations` (2); `events` (7) references `api_keys` (4) for the optional provenance column.
`organizations`, `endpoints`, `events` and `deliveries` must declare their `UNIQUE (organization_id, id)`
target **in the same migration that creates the table**, otherwise the dependent migration fails.

Three units changed shape after Revision 16 and are written to the current §4 text in the migration files
(they exist — see the implementation-status paragraph at the end of this section): `deliveries` (8)
carries `dispatch_seq` and the paired worker-lease columns with the paired-null CHECK, and no `cancelled`
state (A1/A2); `delivery_attempts` (9) records completion-only outcomes with `finished_at NOT NULL` (A4);
`outbox_messages` (10) carries `sequence` with `UNIQUE (delivery_id, sequence)` and `lease_token` with
`uq_outbox_lease_token UNIQUE (lease_token)` and the
lease-shape CHECK, and no `delivery.cancel` kind (A2/B1/F8). Unit 12 (`audit_logs`) follows the same
application-generated UUIDv7 rule as every other table (A9/U20). The U12 provenance columns adopted by A10
live in the units that own their tables, so they add no unit of their own. Their resolved foreign keys
also belong there: `fk_events_org_api_key` in unit 7 and `fk_deliveries_org_endpoint_subscription` in
unit 8 (F3-B/F3-C); the `users`→`api_keys` FKs stay single-column in unit 4 (F3-A).

**And, since the owner approved U1 in revision 16: no migration in this sequence may put a default on an id column.** Every unit
creates `id uuid PRIMARY KEY` with **no `DEFAULT`** — no `gen_random_uuid()`, no `uuidv7()`, no trigger and no
sequence — because the application generates the UUIDv7 value before the `INSERT` (§2, §5). The rule is stated here
as well as in §2 because a migration author reaching for the idiomatic `DEFAULT gen_random_uuid()` would introduce a
second, database-side identifier strategy that nothing in the application layer would know about, and the resulting
rows would still validate, so the mistake would be silent. One consequence follows for unit 13
(`idempotency_records`): its `event_id` column stays nullable and its deferred constraint trigger stays as U44
approved; U1's approval makes the `NOT NULL` simplification *possible*, not required, and overview §13 lists it as an
open item rather than an implemented one.

Revision 9 changed none of the above: U46 adds no DDL. **Revision 10 adds one object, not one unit:** U44's
`idem_assert_linked()` function and `trg_idem_linked` constraint trigger belong **inside unit 13**
(`idempotency_records`), because the invariant is about that table's rows — so the sequence stays **13 units
of table DDL plus the protocol notes**, the order is unchanged, and no new migration unit is needed.
`DEFERRABLE INITIALLY DEFERRED` is what lets the trigger live in the same unit as the table it guards: the
claim inserts a NULL, the link updates it, and only the commit-time check decides. U43 needs no migration at
all; `idx_idem_expiry` already exists to serve it, and U42 stores nothing.

**Implementation status (revision 17).** All 13 units exist in `db/migrations/` as paired `up`/`down` files
(`0001_users` … `0013_idempotency_records`), were applied in this order to PostgreSQL 18.7, and leave the
schema at migration version **13** with the 13 domain tables plus `schema_migrations`. Unit 13 contains
`idem_assert_linked()` and `trg_idem_linked` as described above. The migration files — not the DDL excerpts
in §4 — are the artifacts of record. The implemented migration runner is covered by an integration test that
asserts the database is already at version 13 and that `Up()` applies nothing further; overview §16 records
the executed verification and the one check, the race detector, that was not run.

## 8. Non-obvious consequences worth reviewing

1. `organization_id` duplication on child tables is intentional: it converts the same-tenant check
   into a foreign key instead of application code, and keeps tenant-scoped indexes narrow.
2. `UNIQUE (organization_id, id)` on four parent tables is not index padding; it is a required
   referential target, and each one costs a b-tree insert.
3. `deliveries.url` + `http_headers` + `secret_encrypted` are the immutable destination snapshot after
   U33, held in exactly one table; `endpoint_subscription_id` is the nullable provenance pointer beside
   them. They are not denormalization for read
   speed: dropping them would make in-flight work depend on live `endpoints` and
   `endpoint_subscriptions` rows and would silently break "subscription changes must not alter already
   accepted events". F3-C may clear only the pointer when its subscription is deleted; the snapshot
   payload and the delivery remain intact.
4. `attempt_count` duplicates data derivable from `delivery_attempts`, because the retry scheduler
   must answer "due and still in budget?" without an aggregate over the attempts table per poll.
5. Endpoint hard deletion is effectively prohibited by `deliveries → endpoints RESTRICT` while history
   exists — which is why `deleted_at` was chosen; §3.3 covers the organization-erase implication.
6. 90-day retention is only sound if `events.expires_at` is set at insert; otherwise an event can be
   purged while a long-tail retry is still scheduled, leaving a delivery whose parent event is gone.
7. Approved eager fan-out makes ingestion O(matched endpoints), and that width is now bounded by the
   approved live-endpoint ceiling (§4.5.2) rather than by tenant goodwill. A single accept writes
   `2 + N + N` rows (idempotency record, event, N deliveries, N outbox messages) and should be issued as
   four batched statements, not `2 + N + N` round trips.
8. Two tables now carry the name `subscription` with unrelated meanings. Every query, comment, and
   API surface should qualify them (`endpoint_subscription_id` vs `organization_subscription_id`) to
   avoid a class of bug that is otherwise invisible until a JOIN is wrong.
9. Under eager fan-out a retry cannot be enqueued out of band: the completion transaction writes the
   attempt row, the delivery state, the cleared worker lease and the `delivery.retry` outbox row together
   (B3), so either all of them or none of them commit. What the Recovery Sweep still covers is a crash
   before that commit — a delivery left in `pending` or `retrying` with an expired worker lease — and a publisher
   transport failure after a claim. The Sweep repairs those; it is not a fallback for a partially written
   retry, and the outbox remains the primary path.
10. After U36, `events.destination_count` is the only event-level fan-out field left, and it is
    derivable from `deliveries`. It is kept deliberately: with no status column, a value of `0` is the
    sole record of "accepted but nothing matched", and a counter is cheaper to read than a `NOT EXISTS`
    probe for fan-out-completeness monitoring. It is a denormalization, not a snapshot.
11. The `organizations` row is now a **lock anchor**, not merely a parent key: endpoint creation and
    `paused → active` activation both hold `FOR UPDATE` on it inside the count-and-write transaction
    (U38, §4.5.2). Reads are unaffected, but any future writer that also locks that row joins the same
    queue — relevant before adding an organization-level batch job that could hold the lock across
    network calls.
12. With `events.status` gone, "how much work is still in flight" is answerable only from
    `deliveries` (`idx_deliveries_due`, `idx_deliveries_org_created`). Any dashboard metric, lag alert
    or operator query must be written against `deliveries.state`; nothing on `events` can express it.
13. **Event ingestion must not take an exclusive organization lock.** U38 puts the `FOR NO KEY UPDATE`
    organization lock on endpoint creation/activation only; ingestion takes a `FOR SHARE` status recheck
    first, so it cannot race a state change, and then the idempotency claim locks a single
    `idempotency_records` row (or the unique-index entry). Share locks are compatible with one another, so
    concurrent events for one tenant do not serialize. Reusing the exclusive organization-row lock for
    ingestion would serialize every event a tenant sends — an easy "consistency" refactor that would
    silently destroy ingestion throughput.
14. `events` can no longer answer "which idempotency key created this event" without a join, and after
    72 hours the answer may be gone entirely because the record is deleted (U45). Operators correlating a
    support ticket by key must search within the retention window; past that, `audit_logs` and
    `events.id` are what remain.
15. The organization `FOR SHARE` recheck is the first statement of the accept transaction, and the
    idempotency claim is the first statement that can legitimately fail with "someone else won this key".
    Every later write is therefore speculative work that the claim has already protected, which is why the
    ordering in §5 is a correctness detail and not a style preference.
16. **The fingerprint is computed in the application, never by the database.** U41 fixes it as
    `SHA-256(UTF-8(JCS({"event_type","payload"})))`, and `jsonb` is not a JCS implementation: it preserves
    insertion order rather than sorting and keeps numerics as written. So no `md5()`/`sha256()` over a
    column expression can serve as a shortcut, and the digest must exist before the claim inserts it
    (§4.13.1, §5 step 0.5). One consequence for **U17**: because the fingerprint now covers the parsed
    `event_type` + `payload` only, a decision to store the raw request body cannot retroactively change
    what "same request content" means — U17 stays open, but it is no longer entangled with U41.
17. Pinning `hash_algorithm` to the single legal value `'sha256'` means the column is documentation as
    much as data. It costs a `CHECK` and buys the ability to migrate the scheme later without old rows
    becoming unreadable: widening `chk_idem_hash_alg` is the change, and replay comparison stays exact
    because `bytea` compares bytes rather than collation-sensitive text.
18. **U46's rules cannot be expressed in the database, so they cannot be backstopped by it.** `jsonb`
    stores numerics as `numeric` (arbitrary precision) and silently applies last-duplicate-wins when
    handed repeated keys, so both `9007199254740993` and `{"a":1,"a":2}` land in `events.payload` without
    complaint if the API lets them through. There is no `CHECK` that would catch a missing validation
    step, which makes the parse boundary the single point of failure for the rule — and makes the §4.7
    "inspect the literal before any `float64` decode" requirement a correctness issue rather than an
    optimization: decode-first would validate a rounded number the client never sent.
19. U46 and U41 now **reinforce each other**: the safe-integer bound is what makes the RFC 8785 double
    round-trip lossless for every accepted integer, so the fingerprint's injectivity claim in §4.13.1
    holds unconditionally on the integer side. The single asymmetry left is decimal precision beyond
    double round-trip, isolated in U48 rather than spread through the design. **Revision 14 decided U48 as Option B
    with guard G1 approved and G2 deferred, which keeps this statement as strong as it was**: G1 closes the
    fractional-spelling bypass (overview §11 item 28), so the integer side still has no hole, and the decimal side is
    now documented behaviour plus an approved counter instead of an open question.
20. **The U44 trigger moves the failure to commit, and that is the whole point.** A code path that claims a
    key and forgets to link now aborts at `COMMIT` instead of quietly persisting a broken row — the client
    sees a `500`, and nothing partial survives, because the same commit also carries the event, deliveries
    and outbox inserts. The cost is a deferred check that `sqlc` will not surface in generated code, so the
    migration and the trigger function must be reviewed as part of unit 13 rather than discovered later.
21. **U42 makes the `202` body frozen surface.** Because a replay must be *byte-identical* to a response
    that may have been produced 71 hours earlier, every field in it must come from a column that never
    changes after accept. Adding a volatile field (`updated_at`, a computed lag, a link with a request id,
    a per-request token) does not merely change the shape — it silently breaks replay equivalence, and the
    bug is invisible until two replicas disagree. Any future body change is therefore an API-version change,
    not a field addition.
22. **U43 puts a scheduled writer inside `worker`.** A delivery replica's loop now also performs periodic
    `DELETE`s, so a slow sweep tick shares that process's attention with delivery work: keep each batch its
    own short transaction, never hold a connection across a sleep, and remember that the sweep is the only
    idempotency write that is allowed to fail without touching acceptance (that isolation is a requirement,
    not an optimization). Multi-replica operation is safe by `SKIP LOCKED`, so no leader election is needed.
23. **The object rule and G1 are boundary checks with no database backstop, and that is deliberate.** Revision 14
    approved that the request document **and** `payload` must be JSON objects, and that an integer value which only
    reaches 2^53 **after** the binary64 conversion is refused. `jsonb` would happily accept `"payload": [1,2]`, and no
    `CHECK` can evaluate a value that becomes unsafe in a conversion the database never performs, so both rules live
    in the approved parse order (overview §15.3 step 3 and stage 7). Two consequences for reviewers: a
    `CHECK (jsonb_typeof(payload) = 'object')` is redundant rather than defensive, and adding it would misrepresent
    where enforcement actually sits; the test that matters proves a non-object `payload` and a post-conversion unsafe
    integer each return `400` while leaving **no row in any table** (§4.7, §4.13.5, overview §10).
24. **Application-generated ids remove the database's safety net, and the schema is quieter as a result.** U1 was
    approved by the owner in revision 16 with UUIDv7 values produced before the `INSERT` and **no `DEFAULT` on any id column**, so
    four things that a database default would have handled silently are now the application's responsibility, and each
    one is visible in a different place. An `INSERT` that forgets an id — application code, a `sqlc` query file, a
    hand-run `psql` statement, a data-fix script — fails on the `NOT NULL` violation rather than inventing a value, so
    the failure is loud but arrives at write time, and §5's batched fan-out is exactly where such an omission would
    first appear. Nothing in the database can tell a v7 id from a v4 id, so the *version* discipline lives only in the
    generator and its tests: a `CHECK` cannot inspect RFC 9562's version and variant bits usefully, and none is
    proposed. Composite-FK targets are unaffected — `UNIQUE (organization_id, id)` (§3.1) validates membership, not
    provenance — which is why an id from any process passes the same checks, the property §4.13.2's claim-first
    transaction relies on. And reads that want creation order must keep using the timestamp columns: `events.id` sorts
    approximately by minute, `deliveries.id` sorts approximately by the fan-out loop, and neither is a schedule, so
    `idx_deliveries_due` on `next_attempt_at` and `idx_events_expires` on `expires_at` stay the access paths (§6,
    overview §6).
25. **A UUIDv7 prefix is a weak signal that must not be upgraded into a fact.** The first 48 bits carry the
    generation millisecond, so an observer who sees two `event_id` values can say roughly when they were created — and
    nothing more. `worker` and `outbox-publisher` generate their own ids at their own clock, so an id's prefix reflects
    the moment a process **created a row**, which is *usually* the moment that row became true but is not guaranteed to
    be. The retry path is the case worth naming, and it is about a *new* row: the id stamped at re-drive time is the
    id of the fresh `outbox_messages` row that the worker's retry step inserts, **not** a re-stamping of the delivery's
    original dispatch row (§4.10). An existing row — including one the Recovery Sweep re-arms from
    `failed` to `pending` and the publisher later claims — keeps the id, and therefore the prefix, it was
    created with, because a row's own `id` is written at
    `INSERT` and no statement in this design rewrites a primary key. On top of that, a clock skew or rollback moves a prefix without
    moving any business fact, and two ids generated in the same millisecond have no guaranteed relative order (§2), so
    a prefix supports neither a sequence nor an identity claim. The approved posture is therefore
    explicit — creation time is accepted as an observable property of an id (U1 point 4), timestamps are the only
    ordering authority (U1 point 5), and an id is never an authorization token, a capability, a rate-limit key or a
    signing input (§3.1, overview §8).

## 9. Schema-local open questions

Closed by the review that produced revision 3: **U5** (eager fan-out, `202` after commit, endpoint
ceiling), **U16** (exact-match event types), **U33** (snapshot per delivery, no copy on `events`).

Closed by the review that produced revision 4: **U36** (`events.status` removed, `destination_count`
kept as a non-negative integer, zero-match events still persisted, no `discarded`), **U37**
(`HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG`, default 100, platform config not a column), **U38**
(`organizations` row `FOR UPDATE` inside the same transaction as count and write), **U39**
(application service + shared Unit of Work owns the transaction boundary).

Closed by the review that produced revision 5: **U40** (`internal/app/` is the application service
layer; `internal/app/event/` holds the event-acceptance use case; domain modules must not depend on
`internal/app/*`).

Closed by the review that produced revision 6: **U26** (module path `github.com/metamin44/hookflow`;
`go.mod` was not created in that revision — it exists now, see overview §16) and **U28** (`package eventapp`
at `internal/app/event`, `package event` at `internal/modules/event`, no renames; aliasing needed only on
stdlib/dependency collisions). Neither has a schema effect.

Closed by the review that produced revision 7: **U4** — keys unique per organization, **72-hour** record
retention, same key + same content replays the original event and creates nothing, same key + different
content returns **409**, concurrent same-key requests yield exactly one accepted event, an expired record
makes the key reusable, and expiry never deletes the event or its delivery history. This added the 13th
table `idempotency_records` (§4.13) and removed `events.idempotency_key` / `uq_events_org_idem`.

Closed by the review that produced revision 8: **U41** — the request fingerprint is
`SHA-256(UTF-8(RFC 8785 (JCS) of {"event_type", "payload"}))`, with `request_hash` as `bytea` under a
32-byte `CHECK` and `hash_algorithm` pinned to `'sha256'`. Transport headers are excluded, invalid JSON is
a `400` with no fallback to raw-body hashing, and the digest is computed before the transaction opens
(§4.13.1). No table or relationship changed; U46 (numeric policy inside `payload`) and U47 (deferred JCS
library selection) were created by it.

Closed by the review that produced revision 9: **U46** — accepted JSON integers stay within
`±9007199254740991`, larger magnitudes must arrive as strings, duplicate object keys are rejected, decimals
follow RFC 8785 number serialization, non-representable and non-finite values are rejected, and each
violation is **HTTP 400 before the key claim** with no record, event, delivery or outbox row (§4.7,
§4.13.1). **No DDL artifact**: the rule is a validation boundary because `jsonb` can store everything the
API must refuse. Revision 9 also drafts **U42–U45 as proposals** (§4.13.6, overview §14) — approved in
revision 10, and at the time explicitly not approved — and opens **U48** for the one asymmetry U46 leaves
(decimal precision beyond double round-trip).

Closed by the review that produced revision 10: **U42, U43, U44 and U45**, so the whole idempotency stack
(U4, U41–U46) was canonical at that point — revision 14 closed U47 and U48 as well, so it is U4 and U41–U48 now.

* **U42** — `202` for acceptance and for replay with a **byte-identical body**
  (`event_id`, `destination_count`, `received_at`), `HookFlow-Idempotent-Replay: true` on replays only,
  `409` on fingerprint mismatch, `400` on rejected input, and **RFC 9457 Problem Details** as the API error
  convention with stable machine-readable codes and no payload, hash, secret or internal detail (§4.13.3,
  overview §5.5). No DDL.
* **U43** — cleanup hosted by `worker` (no new binary), **500** rows per batch, **1-minute** interval, at
  most **5** batches per tick, one transaction committed per batch, locking CTE with
  `FOR UPDATE SKIP LOCKED` + `DELETE … USING`, existing `idx_idem_expiry` as the scan path, full-batch
  streaks alertable, failures isolated from acceptance and delivery, and all four values configuration
  rather than literals (§4.13.4). No DDL.
* **U44** — a committed `idempotency_records` row **never** has a NULL `event_id`, enforced by a
  `DEFERRABLE INITIALLY DEFERRED` constraint trigger on INSERT **and** UPDATE that validates the final row
  state at commit, with application transaction handling and tests retained as additional safeguards. This
  is the only schema change of the revision. **U1 is not decided or assumed by it** — the trigger is chosen
  precisely because it holds under either U1 outcome — and if U1 later selects application-generated event
  UUIDs, the guard may be replaced by `event_id uuid NOT NULL` (§4.13, §7).
* **U45** — the record is the latest use of a key, not a history: expired records may be deleted or reused,
  events/deliveries/attempts keep their own history, **no idempotency history table is added**, and the
  per-conflict `audit_logs` write considered during drafting was **rejected**, so the approved "a 409 writes
  nothing" semantics and the single accept transaction are untouched (§4.13.3, §5).

Closed by the review that produced revision 14: **U47** and **U48**, together with four approvals that were §15 items
rather than register rows. With them the idempotency and fingerprint machinery — U4, U41–U48 — is fully canonical.
**Their overview §13 rows are retained and marked Approved (revision 14) rather than deleted**, so the register stays
the historical record of every ID the review opened; that is why this section lists them as closed while the table in
overview §13 still shows them.

* **U47** — the JCS parser/canonicalizer is the standard-library package `encoding/json/jsontext`, chosen at the
  approved go1.27.4 baseline; `github.com/goccy/go-json/jsontext` is recorded as the alternative and **not adopted**,
  and **no JSON dependency is added** (§4.13.5, §4.13.7, overview §11 item 26). The four parser requirements and the
  three extra obligations survive as obligations of the code path; what they now oblige is a CI conformance run that
  has **not** happened yet — the Appendix I sample file, V8 cross-checks, fuzzing and every go1.27.4 execution are
  pending, and the executed evidence is go1.27.3.
* **U48** — Option B: standard RFC 8785 / ECMAScript binary64 number semantics, JSON strings for exact decimals,
  **guard G1 approved** (`math.Abs(f) ≥ 2^53` → `integer_out_of_safe_range`), and **guard G2 deferred rather than
  adopted**, so `number_not_exactly_representable` is not part of the contract and the residue is counted instead
  (§4.7, §4.13.5, overview §11 items 27–28, 31). U46's rules 1–5 are unchanged, and the corrected `D × 10^p`
  trailing-zero integrality procedure is canonical (overview §15.3.1).
* **`payload` must be a JSON object**, with unrecognised `payload` members accepted because no member name is
  reserved — overview §11 item 30, §4.7, consequence 23. No DDL, no new status code.
* **The eight-step parse order** (overview §15.3) is the normative ingestion order — §4.13.1, §4.13.5, §4.13.7:
  validation stays before the claim, the digest before `BEGIN`, and the claim-first transaction unchanged.
* **`hookflow_ingest_number_inexact_total`** is approved by name as an observability counter carrying **no numeric
  threshold** in the API contract — §4.13.5, §4.13.7.
* **The conformance gate** is approved as a merge requirement — §4.13.5, §4.13.7, where executed and pending are
  listed separately. No suite-wide pass is claimed anywhere in these documents.

Closed by the owner in revision 16: **U1** — UUID generation strategy. Approved as six parts (recorded in overview §11
item 33, approved in overview §11 item 34; revision 15 had labelled this closed on an approval that had not been given,
and overview §12.15 records that correction):
**UUIDv7 for every entity identifier**, `events.id` and `deliveries.id` — the publicly exposed ones — included;
generated **in the Go application layer before the `INSERT`**, so **no `id` column carries a `DEFAULT`** and the
alternatives `gen_random_uuid()` (v4) and native PostgreSQL 18 `uuidv7()` are **not** adopted; **one strategy across
`api`, `worker` and `outbox-publisher`**; the **approximate creation time accepted**, with the rule that an **id is not
an authorization credential** and access is decided by §3.1's same-organization invariant plus API scopes; **ordering
and scheduling on timestamp columns**, with id order never used as a business ordering key; and **no library or version selected**, so no manifest entry
appears anywhere. **Its overview §13 row is retained and marked Approved (revision 16)**, the same convention
revision 14 established for U47 and U48. **Schema shape: nothing.** No `DEFAULT` was removed because none existed; no
type, constraint, index or column changed; the ERD moves in comment lines only (§1, §2, the 13 `id` rows in §4, §7 and
consequences 24–25 are the documentation of it). And critically: **U3 stayed open at that point** — it is now resolved
to a PostgreSQL 18 target by A6 (overview §12.16), and this was never a PostgreSQL-version vote in U1 — and
**U44's `event_id` stays nullable** — the `NOT NULL` simplification that U1 makes possible is listed below
as an open item rather than applied here. Two wording corrections folded into this same round change no part of the
decision: §2 and consequences 24–25 now say plainly that **no strict monotonicity inside one process or one
millisecond is expected** — that is a property of whichever generator is chosen later, and none is chosen — and §4.10
plus §5 name **which row** an outbox id belongs to: a retry inserts a new row whose id is generated at its own
`INSERT`, while a row the Recovery Sweep re-arms keeps the id it was created with.

Still open after revision 15 and unchanged after revision 16 (historical record; the current status follows):
**U30 is only half approved** — revision 14 took its
Go-toolchain half (go1.27.4), while the linter set, complexity budgets, golangci-lint compatibility with that
baseline, `depguard`/`importas` configuration and the Actions versions (including the CI mechanism that will run the
approved gate) stay open in overview §13. So do **U3**, **U6**, **U17** and **U20**; no maximum payload size is written
down anywhere in these documents, and neither revision 15 nor revision 16 resolved any of them. **U1, which was on this
list since revision 2, is closed — by the owner's approval in revision 16**; revision 15 had written it up as closed
without an approval behind the claim, which is why the closure is attributed to revision 16 and not to that round.

**Current status (2026-11-22; F3/F8 resolved 2026-11-23).** The full record is in overview §12.16 and
§12.17; the register itself is the
unmodified Revision 16 table in
[overview.md §13](./overview.md#13-unresolved-decisions-requiring-approval) — **31 rows**, **28 open plus
the three rows approved and kept (U1, U47, U48, each labelled Approved)**. Later approvals do not change
any row there.

* **Settled by the 2026-11-22 approvals:** U3 — PostgreSQL 18 is the target, with the exact patch and
  tooling verified before any migration executes (A6); U6's storage shape — `deliveries.secret_encrypted`
  is a copy-to-delivery snapshot, while the D6/U25 envelope/custody architecture is Owner-approved and implemented
  in development (production custody not implemented) (A8). Rotation must not rewrap
  snapshots; endpoint envelopes may be rewrapped, and retirement is reference-based, not calendar-based
  (D6-D is **OWNER APPROVED**; production retirement tooling is **NOT IMPLEMENTED**); U8 — `next_attempt_at` is business scheduling authority and `available_at` a derived relay gate
  (previously evidenced approval); U18 — write-on-completion in §4.9 (same provenance); U9 — audit rows
  are organization-scoped in V1, and U20 — `audit_logs` uses UUIDv7 (both A9); U12 — the nullable provenance
  columns/FKs are adopted (A10), with the actions and same-organization forms resolved by F3-A/F3-B/F3-C
  (2026-11-23); U14 — `is_active` is retained (A10); U31 — soft deletion is endpoints-only
  (A10); U23 — `text` + `CHECK` (A7); U2 — non-partitioned tables with a bounded batched purge (A5).
* **Resolved by the 2026-11-23 F3/F8 entry:** F3-A keeps `users`→`api_keys` single-column with `SET NULL`
  and application membership validation; F3-B/F3-C add the tenant-safe composite provenance FKs on
  `events.api_key_id` and `deliveries.endpoint_subscription_id`; F8 adds
  `uq_outbox_lease_token UNIQUE (lease_token)` (overview §12.17).
* **Genuinely still open:** U2 purge coverage and batch values · U3 patch/tooling gate · U6 envelope,
  KMS custody and rotation · U7 organization erase semantics · U10 token model · U11 "at least one owner
  per organization" enforcement · U13 private/loopback endpoint URLs (SSRF) · U15 soft-deleted-endpoint
  guard · U17 payload size limit and raw-body storage · U19 quota/provider columns on
  `organization_subscriptions` · U30's linter and CI half.
* Closed across revisions 8–10 **and 14**: **U41** (§4.13.1) · **U46** (§4.7, §4.13.1) ·
  **U42–U45** (§4.13.3–§4.13.6) · **U47** and **U48**, plus the parse-order, `payload`-shape, counter and
  conformance-gate approvals (§4.13.5, §4.13.7, §4.7)
* **U40 closed in revision 5**: the application-service layer sits in `internal/app/`, with the
  event-acceptance use case in `internal/app/event`. Both directories were created then; `internal/app/event/`
  is still a `.gitkeep` placeholder, while `internal/app/delivery`, `disposition`, `idempotency`, `outbox`
  and `recovery` were implemented in M1-E Phase 2 (overview §16).
  This has no schema effect; it is recorded here because §4.5.2 and §5 name that layer as the owner of
  the ceiling protocol and the Unit of Work.
