# HookFlow — Architecture Overview

> **Status (documentation revision 21):** approved architecture, with the implementation status that follows
> M1-E Phase 2.1 recorded in §16. Revision 16 remains the last design-approval revision — it is the **U1
> approval round** (application-side UUIDv7; the owner approved it in §11 item 34 and the row is retained and
> marked **Approved (revision 16)**, while revision 15's premature claim is corrected in §11 items 33–34,
> §12.14 and §12.15). Revision 14's approvals stand unchanged — U47, U48, U30 **only** for its Go-toolchain
> half, G1, the ingestion parse order, the `payload` object rule, the inexactness counter and the conformance
> gate, §11 items 25–32 — revisions 3–10 approvals stand, and G2 stays deferred. The later owner approvals are
> recorded in §12.16 (A1–A10, B1–B6), §12.17 (F3-A/F3-B/F3-C/F8) and §12.18 (Recovery expiry precedence,
> 2026-11-23).
>
> **The repository is no longer documentation-only.** The Go module `github.com/metamin44/hookflow` exists
> (`go.mod`, `go.sum`), `db/migrations/` contains the 13 approved units as paired up/down files, and the
> M1-E Phase 2.1 Go implementation and its tests exist. §16 states exactly what is implemented, what is
> deliberately deferred, and what was and was not verified. Historical statements below that say "no Go
> source", "no migration" or "`go.mod` does not exist" describe the revision that wrote them, not the current
> tree.
> Companion documents: [database-schema.md](./database-schema.md), [erd.mmd](./erd.mmd).
>
> **Revision 2** corrected this document to the canonical 12-table inventory. The previous draft had
> invented a `subscriptions` + `subscription_destinations` routing pair, moved billing into
> `organizations.plan`, and attached the outbox table to `events` under the name `outbox_events`.
>
> **Revision 3** records the decisions approved in review: **U5** eager fan-out (event + all deliveries
> + initial outbox messages in one transaction, `202` only after commit, plus a configurable
> per-organization active-endpoint ceiling), **U16** exact-match event-type subscriptions only, and
> **U33** the destination snapshot stored per delivery rather than duplicated on `events`. Webhook
> secret storage/rotation (U6), PostgreSQL version (U3), UUID generation (U1) and idempotency
> retention/replay semantics (U4) stay unresolved by instruction. §11 lists what is closed, §12 every
> change, §13 what still needs approval.
>
> **Revision 4** (the header line for this was missed when the body was updated — corrected here)
> recorded **U36** `events.status` removed from the schema with `events.destination_count` kept as a
> non-negative integer and zero-match events still persisted, **U37** the endpoint ceiling as
> `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG` default 100, **U38** `organizations`-row `SELECT … FOR UPDATE`
> around count-and-write, and **U39** the application service layer with a shared Unit of Work owning
> the transaction boundary.
>
> **Revision 5** records **U40 approved**: the application service layer lives in `internal/app/`, with
> `internal/app/event/` for the event-acceptance use case. Both directories were created as empty
> placeholders, and the dependency rules in §4.1 now state that domain modules must not depend on
> `internal/app/*`.
>
> **Revision 6** records **U26** — module path `github.com/metamin44/hookflow`, matching the `origin`
> remote, with `go.mod` still deliberately absent — and **U28** — `internal/app/event` declares
> `package eventapp` while `internal/modules/event` stays `package event`, plus the naming and aliasing
> convention in §4.2, with no directory renames. §4.1 rule 4 now states the dependency direction edge by
> edge. No table, column, constraint or index changed in this revision.
>
> **Revision 7 closes U4 (idempotency)** and grows the inventory to **13 tables**: `idempotency_records`
> now owns the organization key, the request fingerprint and the event it produced, with **72-hour**
> retention, replay-on-match, **409** on content mismatch, exactly-one-accepted under concurrency, and
> key reuse after expiry without touching event history. Correspondingly `events.idempotency_key` and its
> unique constraint are removed. Eager fan-out (U5), the per-delivery snapshot (U33), the endpoint
> ceiling (U37/U38) and application-layer transaction ownership (U39/U40) are unchanged; the idempotency
> claim is added at the head of that same approved transaction.
>
> **Revision 8 closes U41 (request fingerprint canonicalization).** The fingerprint is
> `SHA-256(UTF-8(RFC 8785 (JCS) of {"event_type": …, "payload": …}))`: member order and insignificant
> formatting cannot change it, a different event type or a semantically different payload always does, and
> transport inputs (`Authorization`, `Idempotency-Key`, request timestamps) are excluded so two identical
> retries can never disagree. `idempotency_records.request_hash` becomes **`bytea`** under
> `CHECK (octet_length(request_hash) = 32)` and `hash_algorithm` is pinned to **`sha256`**; the earlier
> hex/`text` length assumption is gone. Invalid JSON is **HTTP 400 before the key is claimed**, with no
> fallback to raw-byte hashing. The digest is computed outside the transaction, which reorders §5.1.
> **This decision covers the fingerprint contract only: no Go JCS library is selected in this phase** —
> that belongs to implementation planning. Inventory stays **13 tables**; no relationship changed; U46
> (numeric policy inside `payload`) and U47 (JCS library choice) are opened by it and U42–U45 stay
> unresolved.
>
> **Revision 9 closes U46 and drafts U42–U45 as proposals.** U46 (approved, now canonical) fixes the JSON
> numeric and parsing policy: integers confined to `±9007199254740991`, larger magnitudes as strings,
> duplicate object keys rejected, decimals serialized per RFC 8785, non-representable and non-finite values
> rejected, all of it **HTTP 400 before an idempotency key is claimed** and none of it expressible as a
> `CHECK`. **U42, U43, U44 and U45 remain unapproved**: §14 records a recommendation, the trade-offs, the
> safeguards and the U1 dependency for each, and nothing in §5, §6, §7, §8 or the schema document adopts
> any part of it. U48 (decimal precision beyond double round-trip) is opened as the one gap U46 leaves.
> Inventory still **13 tables**, relationships unchanged, no column or index added or removed in this
> revision.
>
> **Revision 10 finalizes the idempotency stack: U42, U43, U44 and U45 are approved**, and the §14 proposal
> block is retired into a decision record. Canonical effect:
>
> * **U42** — new §5.5 fixes the HTTP contract: `202` for acceptance and for replay with a byte-identical
>   body (`event_id`, `destination_count`, `received_at`), `HookFlow-Idempotent-Replay: true` on replays
>   only, `409` on fingerprint mismatch, `400` on rejected input, and **RFC 9457 Problem Details adopted as
>   the API's error convention** — which is an API-wide commitment, not an idempotency-only one.
> * **U43** — §7 fixes cleanup: `worker`-hosted, batch **500**, interval **1 minute**, max **5** batches per
>   tick, one transaction per batch, locking CTE with `FOR UPDATE SKIP LOCKED` + `DELETE … USING`, existing
>   `idx_idem_expiry`, full-batch streaks alerted, failures isolated, values kept as configuration.
> * **U44** — a committed `idempotency_records` row never has a NULL `event_id`, guarded by a deferred
>   constraint trigger on INSERT and UPDATE plus application tests. **U1 stays open and undecided**; this
>   guard was chosen because it holds either way, and the schema may simplify to `NOT NULL` later.
> * **U45** — no key-use history table, and the per-conflict `audit_logs` write is **rejected**, so the
>   approved "a 409 writes nothing" transaction semantics stay exactly as U4 fixed them.
>
> **U46 is re-verified in place and unchanged.** U47 and U48 stay open: U47 now carries four explicit parser
> requirements, including rejecting `9007199254740992`, `9007199254740992.0` and `9.007199254740992e15`
> alike; U48 remains its own future policy decision and U46 is not stretched to cover it. Schema shape:
> **one function and one constraint trigger in migration unit 13, nothing else** — still 13 tables, 14
> relationships, no new column, index or entity.
>
> **Revision 11 evaluates U47 and U48 and approves nothing.** It adds §15: a U48 recommendation (**Option B** —
> standard RFC 8785 number semantics, JSON strings for exact decimals, no digit-count rejection in V1, plus an
> optional >15-significant-digit observation counter), a verified Go candidate evaluation for U47, and a concrete
> eight-step ingestion parse order that puts every `400` before the idempotency claim. The headline finding is
> that **no candidate implements U46's numeric rules**: `encoding/json/jsontext`, its `goccy` backport and the
> cyberphone canonicalizer all treat numbers as `float64` at or before serialization, and one of them *saturates*
> an overflowing value rather than rejecting it — so a HookFlow-owned literal validation layer is required under
> every option. Everything in §15 is labelled **Proposed — Pending Owner Approval**; U47 and U48 remain rows in
> §13, U41 and U46 are quoted unchanged, and the schema shape of this revision is **nothing at all** — still 13
> tables, 14 agreed relationships, and an ERD that moved only in comment lines.
>
> **Revision 12 corrects revision 11 on three points and approves nothing.** (1) **U48/U46 consistency:** U46
> rule 1 binds a number's **mathematical value**, so `1.7e308`, `1E30` and `9223372036854775807` are unsafe
> integers written with an exponent and are **rejected** — revision 11 accepted `1.7e308` by mistaking notation
> for integrality. Measuring the boundary then showed the literal-only rule is **insufficient**: `9007199254740992.5`
> is fractional yet canonicalizes to the unsafe integer `9007199254740992` with **no error** from the library, so a
> post-conversion guard (**G1**) is necessary, and an optional exact-round-trip guard (**G2**) removes the residue
> entirely (§15.1.1). The ">15 significant digits" proxy is withdrawn — `1e-400` is one digit and still inexact.
> (2) **Order and construction:** §15.3 now fixes the eight-stage numeric validation order with bounded
> decimal-string analysis (no arbitrary-precision allocation for `1e999999999`), the `maxBodySize + 1` oversize
> probe that rejects instead of parsing a truncated body, and a wrapper whose `event_type` goes through a real
> JSON **string encoder** rather than concatenation, with `payload` sourced from validated raw JSON and never
> `jsonb`. (3) **Citations:** RFC 8785 has **no section 16**; the conformance material is Appendix B Table 1,
> §3.2.2's worked example, §3.2.3's ordering vectors and Appendix I's sample-value file (§15.2.1). Boundary
> behaviour was **executed** against Go 1.27.3's stdlib `encoding/json/jsontext` in a throwaway module outside
> the repository — no candidate dependency was run, so no runtime conformance is claimed, and U47/U48 stay
> **Proposed — Pending Owner Approval** with the register at 31 rows.
>
> **Revision 13 closes the evaluation with a single recommendation set and still approves nothing.** (1) **U48:**
> the guard recommendation is now **G1 only** for V1, and G2 is **deferred, not recommended** — G2 is unnecessary
> because no approved invariant is broken by a value that stays inside the safe-integer band, and it is overly
> restrictive because it rejects documents a compliant JCS emitter produces (`1e-400`, `1.234567890123456789`)
> with a new `400` code that RFC 8785 itself solves with Appendix D's string wrapping (§15.1.1). G1 also lost a
> redundant test: `math.Abs(f) ≥ 2^53` alone is equivalent to "integral and at least 2^53", because every binary64
> at or above 2^53 **is** an integer. (2) **The integrality algorithm in §15.3 was wrong about trailing zeros** —
> revision 12's `e ≥ k − 1` test on a normalized `0.d1…dk × 10^e` form called `1.200e1` fractional — and the
> corrected form (`D × 10^p`, integer iff `p ≥ 0` or `tz(D) ≥ −p`) is now stated, implemented and **executed** over
> all nine boundary literals (§15.3.1). (3) **U47:** both viable paths were measured against the same conformance
> battery — the stdlib package (path A) and `github.com/goccy/go-json/jsontext` v0.12.0 (path B) — and they agree
> on every case executed, including the §3.2.4 byte-exact golden document, the §3.2.3 UTF-16 key order and
> duplicate-name rejection, so the choice is toolchain coupling rather than conformance; **path A is recommended,
> conditional on U30** (§15.2.2). (4) **U30's toolchain half is proposed: Go 1.27.4** as the V1 baseline, on the
> official release/support data and on the check that nothing in the approved design needs Go 1.24 (§15.6).
> (5) §15.7 is the consolidated decision table. U41 and U46 are still quoted unchanged, the register is still
> **31 rows**, §11 still ends at item 24, and the schema shape is still nothing: 13 tables, 14 agreed
> relationships, ERD 13 blocks / 17 lines / 152 attributes.
>
> **Revision 14 is the owner's approval round, and it approves a defined set — nothing beyond it.** (1) **U30's
> toolchain half: go1.27.4 is the V1 baseline**; the linter set, golangci-lint compatibility with that baseline,
> complexity budgets, `depguard`/`importas` rules and Actions versions stay open, so U30 remains a §13 row.
> (2) **U47: path A — the standard-library `encoding/json/jsontext`** — approved; `github.com/goccy/go-json` is not
> adopted and no JSON dependency is added. The executed comparison behind that choice ran on **go1.27.3**, so this
> records a selection, not a 1.27.4 test result. (3) **U48: Option B with guard G1** (`math.Abs(f) ≥ 2^53` →
> `integer_out_of_safe_range`) is approved and **G2 stays deferred** — V1 does not implement or require exact
> decimal-literal preservation. U41's formula and U46's five rules are re-verified unchanged, as is the corrected
> `D × 10^p` trailing-zero integrality procedure and the nine literals of §15.3.1. (4) **`payload` must be a JSON
> object** — arrays, strings, numbers, booleans and `null` are `400 invalid_json_document` before the claim, and
> unrecognised members inside `payload` are accepted because nothing is reserved by name; this resolves the §5.1
> versus §15.5 contradiction in one direction across all three documents. (5) **`hookflow_ingest_number_inexact_total`**
> is approved as an observability counter with **no** numeric limit in the API contract; alerting thresholds belong to
> operational alerting (§7). (6) **The §15.3 eight-step parse order and the conformance gate are approved**, with
> executed versus pending evidence stated explicitly: the Appendix I sample file, V8 cross-checks, fuzzing and any
> go1.27.4 run are still outstanding, so **no claim is made that the full suite has passed**, and CI enforcement is
> required before merge while the Actions implementation itself remains open. U1, U3, U6 and U17 stay open, and no
> maximum payload size is invented. **Schema shape: still nothing** — 13 tables, 14 agreed relationships, ERD 13
> blocks / 17 lines / 152 attribute lines. The register stays at **31 rows**, because revision 14 **keeps the U47 and
> U48 rows** and marks them **Approved (revision 14)** — approved decision records are not deleted from §13 — so the
> split is now **29 open + 2 approved**, with **19 approved IDs** in §11 and U30 retained as the partially approved
> row. The only
> ERD edit is the **note text** on the `events.payload` attribute line, which now records the object rule; no entity,
> relationship or attribute line was added or removed.
>
> **Revision 15 recorded one approval — U1, the UUID generation strategy — and no other decision, and it is the
> round this revision corrects: the approval it recorded had not been given by the owner, so revision 15's status for
> U1 was premature and must not be read as the moment U1 was approved.** The strategy described in points (1)–(6)
> below is the one the owner approved in revision 16 (§11 item 34, §12.15); everything else in this block is
> unchanged history. (1)
> **UUIDv7 is used for every entity identifier in HookFlow**, including the publicly exposed `event_id` in the `202`
> body and the delivery id carried in `X-HookFlow-Delivery-Id`. (2) **Identifiers are generated in the Go application
> layer before insertion; no database-generated UUID default exists anywhere**, so all 13 `id` columns stay
> `uuid PRIMARY KEY` **without** a `DEFAULT`, and the same is true of `event_id` on `idempotency_records`. (3) **One
> strategy across all three processes** — `api`, `worker` and `outbox-publisher` — so a row created by one service is
> indistinguishable in shape from a row created by another. Each id belongs to one row: a retry step inserts a **new**
> `outbox_messages` row and generates its id then, while a row that already exists keeps the id it was created with
> through publish retries and re-drives (§5.1, §5.3, schema §4.10). (4) The **approximate creation-time information** a v7
> identifier carries is **accepted**, together with the explicit rule that an **identifier is not an authorization
> credential**: scope checks and the `organization_id` tenancy invariant (§4.1, §6, schema §3.1) decide access, never
> the guessability or ordering of an id. (5) **Business ordering and scheduling stay on timestamp columns** —
> `received_at`, `created_at`, `next_attempt_at`, `available_at`, `expires_at` — and UUID order is **never** a
> substitute for any of them. Nothing in this round claims that UUIDv7 values are strictly monotonic within a process
> or within a millisecond either: RFC 9562 §5.7 makes those sub-fields optional, so monotonicity is a property of
> whichever generator is eventually chosen, not something U1 promises — §10 tests v7 validity and uniqueness only.
> (6) **This revision names no UUID library and selects no dependency version.** Whether
> an implementation uses the `uuid` package that Go 1.27 adds or something else belongs to the dependency-baseline
> question that §13 already records as unchosen (with U30's remaining lint/CI half), and nothing here adds a module,
> an import or a manifest entry. **Register: still 31 rows**, now **28 open + 3 approved-and-retained** (U1, U47,
> U48), with **20 approved IDs** and §11 ending at item **33** — the same totals revision 16 keeps, because U1 is
> approved now rather than then: revision 16 adds §11 item 34 and still leaves **31 rows = 28 open + 3 retained** with
> **20 approved IDs**, only U1's row label moving to **Approved (revision 16)**. **Schema shape: still nothing** — 13
> tables, 14 agreed
> relationships, ERD 13 blocks / 17 relationship lines / 152 attribute lines; the ERD moves in comment lines only, no
> column, index, constraint or default is added, removed or retyped, and the id columns are documented rather than
> declared. U3, U6, U17, U20 and U30's lint/CI half stay open, and **U44's `event_id` stays nullable**: it is
> revision 16's approval that
> satisfies the condition U44 recorded for a *later* simplification to `NOT NULL`, and this round does not apply that
> simplification — see the residual notes in §13.
>
> **Revision 16 records one owner approval — U1, the UUID generation strategy — and corrects the historical record of
> revision 15; it approves no other decision.** (1) **U1 is Approved**, effective from this owner approval. The
> approved decision, authoritative as written: **all entity ids are generated in the Go application as UUIDv7 values**;
> **the strategy applies to every entity id without exception**, the publicly exposed `event_id` in the `202` body and
> the delivery id in `X-HookFlow-Delivery-Id` included; **ids are produced before the `INSERT`**, so a row's id is in
> hand when it is written; **no database-generated UUID default is used**, and none may be added to any of the 13 `id`
> columns; **PostgreSQL's native `uuidv7()` is not used for id generation**, which is declined as an implementation
> route for U1 rather than voted on as a PostgreSQL capability; **the approximate creation-time disclosure carried by
> a public UUIDv7 value is accepted**; **a UUID is an identifier, not an authorization control** — authorization and
> tenant isolation stay independently enforced by scopes and by the `organization_id` invariant (§4.1, §6, §8, schema
> §3.1); and **business ordering uses explicit timestamp columns** (`received_at`, `created_at`, `next_attempt_at`,
> `available_at`, `expires_at`), never UUID order. (2) **Rationale, recorded as the owner stated it:** an
> application-generated id is available **before** the `INSERT`, which is what lets the claim→event→fan-out→outbox
> transaction in §5.1 name every id in one statement; it works **identically in `api`, `worker` and
> `outbox-publisher`, and in batch and backfill writes**, where a column default would leave each writer to invent its
> own; and it **does not couple id generation to a PostgreSQL feature or to an SQL parser's or catalog's compatibility
> with one**, so the strategy holds whichever major version U3 eventually selects and whatever tooling U30 and the
> dependency baseline settle on. (3) **History correction, not a reversal:** revision 15 wrote the six parts and the
> schema-shaped statements it presented as an owner approval; no owner approval had been given, so revision 15's U1
> status was premature. The strategy it described is the one approved here, **unchanged in substance**; what changed is
> who approved it and when. (4) **Nothing else moved**: schema shape stays **13 tables / 14 agreed relationships /
> ERD 13 blocks / 17 relationship lines / 152 attribute lines**, no DDL, no `DEFAULT`, no column, index, constraint or
> migration is created or changed; **U3 stays open and independently decidable — its register wording was corrected
> within this same revision to drop an obsolete claim that the PostgreSQL version drives UUID generation, which
> selects, recommends and approves no version**; **U6's
> encryption and AAD decisions are unchanged in this historical revision**, and where AAD is built from ids it used
> the canonical lowercase UUID text derived from the stored `uuid` values — a historical proposal now superseded for
> current guidance by the 2026-11-24 reconciliation note above (binary 64-byte AAD, now **OWNER APPROVED** as
> D6-B and implemented in the development runtime; production custody is not implemented — Revision 20);
> **U8, U17, U20, U25, U30's lint/CI half, U34, U35 and U44's nullable
> `event_id` all stay exactly as they were**, and no new decision ID is created or approved by this round. This
> approval is a decision record only: **it is not authorization to begin implementation**, and no file outside the
> three architecture documents was touched.
>
> **Revision 17 is a documentation-only reconciliation and approves nothing new.** It (1) records the actual
> implementation status after M1-E Phase 2.1 in §16 — implemented database cores, hardening items B-1/M-2/M-3/L-2/L-1,
> explicit limitations and the verification that was and was not executed; (2) records the owner-approved
> **Recovery event expiry precedence** decision of 2026-11-23 in §12.18 and states it in the dispatch flow (§5.3);
> and (3) corrects status statements that still presented the repository as documentation-only (§4, §4.1, §4.2,
> §10, §11 items 16 and 25, §14.3). It creates no decision ID, changes no approved contract, and changes no table,
> column, index, constraint, relationship, numeric value or Git state.
>
> **Revision 18 (2026-11-24) records the owner-approved Bundle 1 implementation and D7 reconciliation.** It adds
> the approval/implementation record in §12.19 and updates §16: amqp091-go v1.17.0, application-managed reconnect,
> RabbitMQ 4.4.1 primary/4.3.8 floor, topic/quorum dispatch plus direct-DLX/quorum-DLQ policy, N=1 Publisher,
> exact-pointer Worker with fixed-lease real HTTP/retry ladder, generation-owned handles, worker Recovery scheduling,
> optional process metrics, and D7 CI/operations documentation. The API remains health-only. D6/U25, production HA
> and SSRF controls remain unimplemented/unapproved; nonempty `secret_encrypted` fails closed without HTTP. Empty-secret
> snapshots execute unsigned HTTP **unconditionally today**; no deployment-mode gate exists (D6-F proposed, **NOT
> APPROVED**). **The dated execution evidence in §16.7 supersedes
> pre-execution pending labels:** local build/vet/unit, PostgreSQL/RabbitMQ 4.4.1/4.3.8 and Linux race checks passed;
> lint has advisory findings, while hosted CI and its fresh down/up drill remain unexecuted. Historical revision
> statements below continue to describe the revision that wrote them.
>
> **D6/U25 cross-document reconciliation (2026-11-24, documentation-only; approves nothing).** D6 and U25 remain
> **NOT APPROVED** and unimplemented. *(Superseded for current status by Revision 20 below: the Owner approved
> D6-A…D6-G and U25-A…U25-D as architecture on 2026-11-24 and the development implementation exists; the
> unimplemented part is production custody/readiness. This paragraph is retained as the pre-approval record.)* Two earlier proposal statements are superseded for current guidance while
> retained as history: **(1) AAD representation.** The revision-16 wording that AAD uses "canonical lowercase UUID
> text" is historical and, in the original formula, internally inconsistent with a 16-byte id field. The single
> current proposed byte definition is the 64-byte construction in
> [`d6-u25-decision-proposal.md`](./d6-u25-decision-proposal.md) §4.3 — 18-byte `hookflow.secret.v1`, `alg`,
> `purpose`, 12-byte data nonce, raw 16-byte organization UUID, raw 16-byte endpoint UUID — with the key slot
> excluded for endpoint rewrap. No AAD builder or envelope codec exists, so neither the historical nor the proposed
> form is implemented. **(2) KEK retirement.** Calendar-only retirement and delivery-snapshot rewrap are superseded
> by reference-based accounting: every persisted endpoint envelope (including a soft-deleted endpoint row whose
> ciphertext has not been purged) and every immutable delivery snapshot counts as a reference; A8 forbids rewrapping
> snapshots; 90/90/72 are retention horizons, not permission to delete a key. No endpoint purge or Events/Deliveries/
> Attempts purge exists, so **KEK retirement is operationally blocked** until cleanup and reference accounting exist.
> See schema §4.5/§6 and proposal §4.3/§4.4. (D6-B/D6-D were later **OWNER APPROVED** on 2026-11-24 and
> implemented in development; production custody remains absent. See Revision 20.)
>
> **Revision 19 (2026-11-24) records Phase 6 implementation and current runtime status. It approves nothing and
> changes no approved contract.** (1) Phase 6 adds the authenticated management API (endpoint lifecycle with
> Contract A strong ETags, subscriptions, `whsec_` secret rotation behind a KEK provider), database-backed Argon2id
> API keys with scopes, the `cmd/apikey` operator CLI, audit logging, and U37/U38 ceiling enforcement using
> `FOR NO KEY UPDATE` on the organization row with organizations -> endpoints -> endpoint_subscriptions lock order.
> (2) The Phase 5 development runtime now contains the local-file KEK provider, the `hfse1.` envelope codec/AAD builder
> and Standard Webhooks v1 signing; this supersedes the paragraph above's "no AAD builder or envelope codec exists"
> as a current-state statement, while every D6/U25 proposal decision remains **NOT APPROVED** and no production
> provider or production activation exists. (Superseded by Revision 20: the Owner approved D6-A…D6-G and
> U25-A…U25-D as architecture on 2026-11-24; the development implementation exists, and only production custody
> and readiness remain absent.) (3) §16.8 records the Phase 6 evidence. The API is no longer health-only;
> event ingestion, fan-out and organization management remain unimplemented (Phase 7).
>
> **Revision 20 (2026-11-24, Phase 6.1) reconciles D6/U25 statuses and the archived/suspended policy. It approves no
> new contract and changes no code, schema, migration or configuration.** (1) The Owner approved D6-A through D6-G
> and U25-A through U25-D as architecture on 2026-11-24. Rev 18/19 statements that every D6/U25 proposal decision
> remains "NOT APPROVED" are superseded for current status; they remain the pre-approval record. (2) Statuses are
> reported on three separate surfaces: D6/U25 architecture = **OWNER APPROVED**; development implementation =
> **IMPLEMENTED** (development-only file KEK, `hfse1.` envelope/AAD, Standard Webhooks v1 signing, deployment-mode
> gate, management secret lifecycle, verified by Phase 6.1); production KMS/Vault custody and production readiness =
> **NOT IMPLEMENTED / NOT READY**. Absent production providers do not un-approve the architecture. (3) The archived
> policy is corrected: archived organizations deny **all** tenant management writes, including subscription
> deactivation, endpoint soft-delete and API-key creation/revocation. The suspended policy records the approved
> endpoint soft-delete and API-key revocation alongside reads, rotation, metadata updates, pausing and subscription
> deactivation. (4) §16.9 records the Phase 6.1 verification evidence; §16.8 remains the Phase 6 record.
>
> **Revision 21 (2026-11-25, Phase 7) records the Owner-approved Phase 7 decisions and their development
> implementation. It creates no new decision ID and changes no previously approved contract, table, column,
> index, constraint, migration version or Git state.** (1) OD-4 approves and this build implements the
> SSRF/destination policy: HTTPS on port 443 only, no userinfo/fragment/IPv6 zone, literal-address rejection
> by an explicit range table plus standard address classification, a 2 s advisory hostname preflight at
> create/update, delivery-time resolution of every A/AAAA answer with IPv4-mapped unmapping, rejection of any
> prohibited answer (including mixed public/private sets), literal pinning for the dial, connected-remote
> verification, preserved TLS SNI/verification, no proxy, no redirect following and per-attempt revalidation.
> Disallowed destinations are permanent pre-HTTP failures (`destination_disallowed`) with no retry outbox row.
> Network-level egress control remains the recommended defense in depth. (2) OD-5 fixes the ingestion body cap
> at exactly 1 MiB: read `max+1`, reject the extra byte with `400 payload_too_large`, use `Content-Length` only
> for an early refusal, never parse or hash a truncated document, close the request connection, never
> decompress, store only canonical `jsonb` and never a raw request body. The cap is a compiled constant, not
> configuration, and there is no production bypass. (3) U47 Path A (`encoding/json/jsontext`, standard
> library, no new dependency) and the U41/U42/U46 parse order are implemented in the ingestion service.
> (4) The authenticated ingestion route `POST /v1/events` exists with `events:write`, claim-first idempotency
> (72 h), byte-identical replay bodies and RFC 9457 errors. §16.10 records the Phase 7 evidence. Production
> deployment, production secret-provider custody, hosted CI and production readiness remain absent, and the
> process still fails closed outside the development file-KEK provider.

---

## 1. Purpose

HookFlow is a **reliable webhook delivery platform**. It accepts business events through an
authenticated HTTP ingestion API, persists them transactionally, resolves the destinations that
subscribed to that event type, and delivers to each destination with **at-least-once** semantics
using durable, jittered retries — while retaining verifiable history (events, deliveries, attempts,
audit trail) for debugging and support.

The product promise is durability and observability of delivery, not exactly-once processing.
Consumers are expected to deduplicate using the delivery identity headers HookFlow sends.

## 2. V1 scope and non-goals

In scope: organization-scoped API keys, endpoint CRUD with soft deletion under a
**platform-configured maximum number of active endpoints per organization**
(`HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG`, default 100, U37 approved), **`endpoint_subscriptions`
as the event-routing model** with **exact-match event types only** — no wildcard or glob matching in
V1 (approved) — event ingestion with a **mandatory idempotency key** held in `idempotency_records` for
**72 hours** (replay on identical content, **409** on different content, exactly one accepted event under
concurrent use of a key, key reusable after expiry) and eager fan-out in one transaction (approved),
transactional outbox of per-delivery messages, RabbitMQ-based dispatch (RabbitMQ 4.3.x remains the minimum
compatibility target, B6; owner-approved Bundle 1 pins 4.4.1 as primary and 4.3.8 as floor/fallback), worker-driven HTTP
delivery with retries, 90-day retained history, audit logging, and a **simple
`organization_subscriptions` plan/status record**.

Out of scope for V1 (recorded so it is not quietly assumed): multi-region or sharded deployment,
per-endpoint ordering guarantees, dead-letter replay UI, endpoint health scoring/auto-pause, payload
transformation, invoicing or payment processing beyond a plan/status label, an event-type catalog
with schema validation, gRPC/SQS transport, and the React dashboard.

## 3. Process topology

Three independently deployable processes share one codebase and one PostgreSQL database. Each is a
distinct `main` package under `cmd/`; shared logic lives in `internal/modules/*`.

| Process | Role | Responsibilities | Failure posture |
| --- | --- | --- | --- |
| `api` | Modular monolith HTTP service (Echo) | AuthN/AuthZ, API key lifecycle, organization/endpoint/routing-rule CRUD (including the live-endpoint ceiling check on create), subscription plan reads, **event ingestion: claim the idempotency key, then insert `events` + derived `deliveries` + `outbox_messages` in one transaction, respond `202` only after commit**, history reads | Horizontally scalable, stateless; no background work in the request path |
| `outbox-publisher` | Transactional outbox relay | Claims `pending` rows only (`FOR UPDATE SKIP LOCKED`) under a per-cycle budget, publishes to RabbitMQ, commits `published` under the lease fence, and reaps its own expired leases; the Recovery Sweep alone re-arms `failed` rows | Crash-safe: nothing is acknowledged before the broker confirms; unpublished rows stay claimable, and a fenced zero-row update writes nothing |
| `worker` | Delivery consumer | Loads the delivery and its snapshot, executes the HTTP call, records `delivery_attempts`, updates `deliveries` state, **enqueues the next retry as a new `outbox_messages` row**, and — since U43 — runs the **expired-idempotency-record sweep** on a 1-minute ticker (batch 500, max 5 batches per tick, its own short transaction per batch) | Idempotent per `(event_id, endpoint_id)`; unacked messages are redelivered, so at-least-once is accepted. Sweep failure is logged and counted and never affects acceptance or delivery |

There is **no fourth scheduler process** in V1: retry sweeps and retention purges run on tickers
inside the `worker` and `outbox-publisher`, because neither can assume the other is alive. U43 keeps that
rule — the idempotency sweep went to `worker` rather than becoming a new binary — and it is safe to run in
every replica because the batch claim uses `FOR UPDATE SKIP LOCKED`, so no leader election is needed. The
implementation package for the ticker is an implementation-phase choice; **no directory is added in this
phase**.

**Implementation status (2026-11-24, Phase 6; extended by Phase 7):** the Publisher and Worker process loops exist,
including application-managed reconnect, generation fencing and worker-hosted Recovery scheduling. The API serves
health, the authenticated management API (endpoint/subscription CRUD, secret rotation, API-key auth) and, since
Phase 7, the authenticated `POST /v1/events` ingestion route with transactional fan-out (§16.10). The Worker decrypts
allowlisted endpoint secrets and signs requests with Standard Webhooks v1; empty-secret snapshots send unsigned HTTP
only when the development-only unsigned gate is enabled (`HOOKFLOW_DEPLOYMENT_MODE=development` +
`HOOKFLOW_ALLOW_UNSIGNED_DELIVERY=true`). Nonempty secrets that fail decryption are dispositioned without HTTP.
See §16.8 and §16.10.

## 4. Repository layout

```text
cmd/                    main packages: api/, worker/, outbox-publisher/   (implemented; migrate/ also exists — §16)
internal/
  app/                  application service / use-case layer (U39 + U40 approved): cross-module
                        orchestration and the shared Unit of Work. Implemented subset since M1-E:
                        delivery/, disposition/, idempotency/, outbox/, publisher/, recovery/, worker/ (§16)
    event/              package eventapp (U28) - event acceptance use case: event insert,
                        subscription lookup, delivery creation and outbox insert in ONE transaction;
                        also the owner of the endpoint ceiling lock-count-write protocol
                        (implemented in Phase 7 with the authenticated `POST /v1/events` route;
                        overview §16.10)  modules/              feature-oriented, horizontally independent packages
    auth/               identity, token issuance/validation, RBAC enforcement
    organization/       tenants, membership
    apikey/             issuance, hashing, rotation, revocation, scoping
    endpoint/           destination registry, soft delete, encrypted secret custody
    event/              package event - ingestion validation, idempotency keys, destination
                        resolution and the repositories the use case drives (no status column on
                        events - U36 approved); orchestrated from package eventapp, never the reverse
                        (§4.1 rule 4)
    subscription/       BOTH routing rules (endpoint_subscriptions) and the commercial
                        subscription record (organization_subscriptions)
    delivery/           delivery state machine — the only processing-state machine in the system
                        (U36), attempts, retry policy, HTTP executor, retry-message enqueue
    audit/              append-only audit trail writer/reader
  platform/             technical concerns, no business rules
    database/           pgx pool, migration runner wiring, Unit of Work / Tx helpers (U39)
    messaging/          RabbitMQ connection, topology, policy, publish/consume helpers
    config/             environment-driven configuration loading and validation, incl.
                        HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG (U37)
    observability/      process-local, low-cardinality metrics and optional listener
    logger/             structured logging and request context
    http/               Echo server, middleware, error mapping, health endpoints
  shared/               cross-cutting primitives (ids, time, errors)
db/
  migrations/           paired up/down SQL migrations (all 13 units exist — §16)
  queries/              sqlc query files              (still empty)
docs/architecture/      this documentation set
deployments/            manifests                     (still empty)
scripts/                developer/operational scripts  (implemented: dev-db.sh, migrate.sh)
test/integration/       Bundle 1 cross-process integration tests (package integration tests also live beside packages)
.github/workflows/      CI definitions                (D7 quality/integration/race/advisory-lint jobs)
```

The tree above is the approved target layout, not a claim that every directory is populated. `internal/app/`
and `internal/app/event/` were added by U40; since M1-E Phase 2.1 the implemented packages are the subset
listed in §16, and `internal/app/event/`, `internal/modules/*`, `internal/platform/logger` and
`internal/shared` remain `.gitkeep` placeholders. `internal/platform/messaging` and `observability` are now implemented. Empty directories are held in Git with a `.gitkeep`
placeholder only. All paths above sit under the module path `github.com/metamin44/hookflow` (U26);
package names are given where they differ from the directory name (§4.2).

### 4.1 Package and dependency rules

1. `cmd/*` may import everything; nothing imports `cmd/*`. `cmd/*` is the **composition root**: it
   builds the pgx pool, the AMQP connection, the module adapters, and the **application services with
   their dependencies**, then wires them into the HTTP handlers and consumers.
2. `internal/modules/*` may import `internal/platform/*` and `internal/shared`, and **must not import
   one another's internal implementation packages**. Collaboration between modules happens through
   ports (interfaces) declared at the boundary that needs them and satisfied by the other side in the
   composition root (U39 approved).
3. `internal/platform/*` must not import `internal/modules/*`.
4. **Dependency direction, stated per edge (U39, U40 approved; clarified in revision 6).** Cross-module
   orchestration belongs **exclusively** in `internal/app/*`. A domain module must not import another
   module's internal implementation packages, and must not import `internal/app/*`. Where a module needs
   a capability it does not own, it declares a port (interface) at its own boundary and the implementation
   is supplied at the composition root. The resulting arrow is one-directional:
   `cmd/*` → `internal/app/*` → `internal/modules/*` → `internal/platform/*` + `internal/shared`, with
   `internal/shared` importable by all of them. This restates rules 1–3 edge by edge rather than adding a
   new direction: `internal/app` sits strictly above the modules and nothing reaches down into it. If a
   module appears to need an application service, either that work is single-module and belongs in the
   module, or it needs a port. Enforce with `depguard` plus `importas` rather than review memory (U30).
5. **Transaction ownership is the application layer's, not the modules'.** A use case opens one Unit of
   Work — one `pgx.Tx` — and passes it to every repository it calls; repositories accept the
   transaction handle and never `Begin`, `Commit` or `Rollback` themselves (U39 approved). Because
   `pgxpool.Pool` and `pgx.Tx` share `Query`/`Exec` signatures (and `sqlc` accepts either), the same
   repository code runs with or without an enclosing transaction.
6. Modules must not each open their own transaction for one use case: the approved eager fan-out
   requires `events`, `deliveries` and `outbox_messages` to commit atomically, so independent
   transactions would break the invariant rather than merely being inelegant.
7. **Application-layer location and package name (U40, U28 approved).** `internal/app/` is the
   application service layer, and `internal/app/event/` holds the event-acceptance use case that
   coordinates event persistence, subscription lookup, delivery creation and outbox message creation.
   Both directories were created in revision 5. `internal/app/event/` now implements the event-acceptance
   service (Phase 7, §16.10) and the other `internal/app/*` packages listed in §16 were added by M1-E Phase 2.
   The directory names are unchanged, but the package declared in `internal/app/event` is
   **`eventapp`**, while `internal/modules/event` stays **`event`** — so the two can be imported in the
   same file without any alias, which is the reason the suffix was chosen. See §4.2.
8. Two tables are named `*_subscriptions` with unrelated meanings (routing vs. billing). Column and
   variable names must be qualified (`endpoint_subscription_id`, `organization_subscription_id`);
   a bare `subscription_id` in either module is a defect waiting to happen.
9. `internal/platform/http` keeps its directory name and declares `package http` (dir == package).
   Because that shadows the standard-library identifier, any file importing both it and `net/http` must
   alias the platform package — convention in §4.2 (U28 approved; no rename).
10. **This is a modular monolith, deliberately.** U39 and U40 do not license splitting services: no
    separate fan-out service, no per-module deployment, and no new process beyond `api`, `worker` and
    `outbox-publisher`. `internal/app/` is a package layer inside the same binaries, not a service. If
    a use case feels too large to own one transaction, that is a signal to move code between packages,
    not to introduce more binaries.

### 4.2 Module path and package naming (U26 and U28 approved)

**Module path (U26):** `github.com/metamin44/hookflow`, matching the configured `origin` remote
(`https://github.com/metamin44/hookflow.git`). Every internal import is therefore of the form
`github.com/metamin44/hookflow/internal/...`. **`go.mod` now exists** (revision 6's "not created in this
phase" described that revision); the path decision is unchanged and the module compiles at it. See §16.

**Naming rule:** a package name matches its directory name, except where two same-named packages must be
used in one file — in which case the application-layer package carries an `app` suffix while its
directory stays exactly as approved.

| Import path | Package name | Collides with | Explicit alias required? |
| --- | --- | --- | --- |
| `internal/modules/event` | `event` | — | no |
| `internal/app/event` | `eventapp` | would have been `event` | **no** — the names differ, so both import cleanly into the composition root |
| `internal/platform/http` | `http` | stdlib `net/http` | **yes**, in any file that needs both |
| other `internal/modules/*` and `internal/platform/*` | matches directory | — | no, unless it collides with a stdlib or dependency name in that file |

**When an explicit alias is required:**

1. A file importing both `internal/platform/http` and `net/http`. Convention: leave `net/http` unaliased
   (it appears in far more files) and alias the platform package —
   `platformhttp "github.com/metamin44/hookflow/internal/platform/http"`.
2. Any local package whose name would collide with a stdlib or third-party identifier used in the same
   file (for example a configuration library against `internal/platform/config`). Alias the **local**
   import so the well-known name keeps its usual meaning.
3. A test file needing both a domain package and its application-layer counterpart when the two share a
   directory name — already removed for `event` by the `eventapp` rule.

Never alias merely to shorten a path: a gratuitous alias hides which package a symbol came from and
defeats `importas` enforcement.

**Enforcement and accepted costs:** `importas` pins path→alias pairs (including `platformhttp`) and
`depguard` enforces the rule-4 direction — both are lint configuration, which is what remains open in
U30. Two consequences of the deliberate dir≠package choice for `internal/app/event` are accepted
knowingly: tooling configured to require directory-equals-package needs an explicit exception for that
single path, and editor package-rename operations behave less predictably than for a matched pair.

## 5. Core flows

### 5.1 Event ingestion and fan-out (one transaction, owned by an application service)

Approved in review: fan-out is eager and the `202` is emitted only after `COMMIT` (U5); the transaction
is opened by a use-case layer above the modules, not by any module (U39); an event that matches no
endpoint is still a normal accepted event with `destination_count = 0` (U36); the idempotency key is
claimed inside that same transaction before any event is written (U4); the response shape of that `202`,
the replay and the 409 is fixed by **U42 (§5.5)**; and the claim is guaranteed to end linked, because
**U44**'s deferred trigger aborts a commit whose `event_id` is still NULL.

Steps 3 and 4 are one ordered parse, not two independent checks: size-bounded read → duplicate-aware token walk
→ envelope and `payload` shape assertion → numeric validation **on the literals**, then the binary64 guard →
raw-byte RFC 8785 canonicalization → SHA-256. The *rules* those two steps enforce are approved (U41, U46, U42),
and **so is the concrete order**: §15.3 is the normative parse order as of revision 14 (§11 items 29–30), the
parser is the standard-library `encoding/json/jsontext` (U47), and `payload` itself must be a JSON object.
Nothing in that order adds a status code — every rejection maps to a row of §5.5.

```text
POST /v1/events
  1. auth middleware   -> resolve API key by key_prefix, verify hash, bind organization_id + scopes
  2. idempotency       -> Idempotency-Key header REQUIRED (non-empty, length-bounded)
  3. validate          -> event type syntax, payload size (U17) read as a maxBodySize+1 probe (a truncated
                          body is REJECTED, never parsed), and the U46 document rules: a JSON object
                          at the top level, no duplicate keys, integer VALUES within +-9007199254740991
                          judged by mathematical value, so 9007199254740992 / 9007199254740992.0 /
                          9.007199254740992e15 / 1.7e308 are all rejected while 150.0 and 1.5e2 (both 150)
                          are accepted (schema 4.7 rule 1). Approved in revision 14: payload itself must be
                          a JSON object (an array, string, number, boolean or null is rejected), guard G1
                          runs AFTER the binary64 conversion and rejects abs(f) >= 2^53 - the case
                          9007199254740992.5 - and the order of all of it is the normative one in 15.3,
                          using encoding/json/jsontext (U47) with no third-party JSON dependency
  4. fingerprint       -> request_hash = SHA-256(UTF-8(RFC 8785 JCS of {"event_type", "payload"}))
       (U41 approved)     <- computed BEFORE the transaction opens, so hashing holds no row lock
       <- not valid JSON / not an object (document top level or payload) / duplicate key / out-of-range
          integer value / non-finite value / unsafe integer after conversion (G1)
          => HTTP 400 here, RFC 9457 body with a stable code, no idempotency claim and no event, and
          NEVER a fallback to hashing the raw body bytes (U41 + U46 + U42 approved)
  5. eventapp.AcceptEvent (internal/app/event) begins ONE Unit of Work  -> BEGIN
       a0. SELECT status FROM organizations WHERE id = $1 FOR SHARE
          <- organization state recheck before the claim: missing or not active => deny and roll
             back, writing no claim and no event (the HTTP layer's earlier check is not the lock)
       a. INSERT idempotency_records (organization_id, idempotency_key, request_hash,
                                      event_id = NULL, created_at, expires_at = now() + 72h)
          ON CONFLICT (organization_id, idempotency_key) DO NOTHING RETURNING id
          <- no row returned => SELECT ... FOR UPDATE the existing record and branch (U4 approved):
               unexpired + same request_hash   -> replay: read events by event_id (never NULL - U44),
                                                  return the ORIGINAL 202 status and byte-identical body
                                                  plus HookFlow-Idempotent-Replay: true, write nothing (U42)
               unexpired + different hash      -> HTTP 409 Conflict, RFC 9457 with
                                                  code=idempotency_key_conflict, write nothing (U42/U45)
               expired                         -> UPDATE the record (new hash/window), continue as owner
       b. SELECT endpoint_subscriptions JOIN live endpoints
          WHERE organization_id = $1 AND event_type = $2   <- exact match (U16 approved)
       c. INSERT events (id, organization_id, type, payload, destination_count, received_at, expires_at)
          <- id supplied by the application as a UUIDv7 value before the INSERT (U1 approved, revision 16);
             no column DEFAULT exists on it or on any other id in this transaction
          <- received_at = now() and expires_at = now() + interval '90 days' (retention/response facts,
             not the A3 scheduling sample t used for next_attempt_at/available_at)
          <- no status column (U36); NO idempotency_key column (U4); destination_count = rows from b,
             and 0 is a valid accepted value
       d. UPDATE idempotency_records SET event_id = <event from c> WHERE id = <claim from a>
          <- U44: this link is what the deferred trigger verifies at COMMIT; an unlinked claim aborts the
             whole transaction instead of committing a broken row
       e. IF destination_count > 0: INSERT deliveries (…one row per matched destination,
          carrying the destination snapshot columns…)      <- U33 approved
          ELSE: no deliveries and no outbox_messages created — the event row still commits
       f. IF destination_count > 0: INSERT outbox_messages (…one row per created delivery,
          kind='delivery.dispatch'…)
     COMMIT             <- a–f become visible atomically; trg_idem_linked runs here (U44)
  6. RETURN 202 + {"event_id", "destination_count", "received_at"}   <- strictly after COMMIT (U5);
             exact body, headers and error shapes for every branch: §5.5 (U42 approved)
```

Steps a–f are the transactional-outbox contract: the key claim, the event, its fan-out, and its dispatch
intent become visible atomically, so a crash between "accepted" and "queued" cannot lose work and a
duplicate request cannot create a second dispatch chain. Nothing is published and no HTTP call is made
inside the request path — `202` means durably recorded, not delivered. The fan-out width of steps e/f
is bounded by the approved ceiling of `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG` (default 100, §7), so
the worst case is `2 + 100 + 100 = 202` rows in one transaction, and the inserts should be batched.

**Every `id` in steps a, c, e and f is an application-generated UUIDv7 value (U1 approved, revision 16).** The
identifier exists before the row does, which is why the batched inserts can be built in memory and why step d can
link the claim to the event it produced without reading a generated value back first. No `id` column in the schema
carries a database default, so an `INSERT` that omits one fails on `NOT NULL` instead of silently acquiring an id
the application never saw — a loud failure is the intended behaviour for a hand-written or break-glass statement.
The same generation rule applies to the rows `worker` creates — a `delivery_attempts` row and the **fresh**
`outbox_messages` row that each retry step inserts (§5.3) — and to any row `outbox-publisher` claims, which
keeps the id it already has. There is one identifier strategy in the system, not three. Which row an id belongs to matters, and naming it is not a new
rule: a row's **own** `id` is written once, at its own `INSERT`, and nothing in this design rewrites a primary key
afterwards. Step d above is the deliberate contrast — `idempotency_records.event_id` is a *link* to another row's
key, and `UPDATE`ing it is exactly what U44's deferred trigger checks. A publisher-side re-drive of an unpublished
row therefore keeps that row's original `id`, updating `status`, `attempts` and the lease columns (schema §4.10),
while the delivery's *next* retry is a distinct row that is given its own id when it is created. An
`outbox_messages` id therefore stamps the creation of that row, never the moment its delivery was first accepted.
Ordering, scheduling and replay
comparison still read timestamp columns: `events.received_at` is what freezes the `202` body (§5.5, U42),
`deliveries.next_attempt_at` is the business due-time authority and `outbox_messages.available_at` the
derived relay gate (U8, approved; A3), `idempotency_records.expires_at` decides
key reuse — **none of them is derived from an identifier**, and the approximate creation time a v7 value carries is
an accepted property of the id, not a scheduling input.

Claiming the key **first** is what makes duplicates cheap and races safe: a second concurrent request on
the same key blocks on `uq_idem_org_key` until the first commits, then reads the record and takes the
replay or 409 branch without writing, so exactly one event exists per key and a loser leaves no partial
event, delivery or outbox rows (schema document §4.13.2–§4.13.3). Because the fingerprint is
RFC 8785 + SHA-256 over `{"event_type", "payload"}` (U41), the replay and 409 branches compare digests of
*content*, not of bytes on the wire: a client that reformats its own retry still replays, and a client
that changes the event type or any payload value still conflicts.

### 5.2 Destination snapshotting

Approved in review (U33): the snapshot is stored **per delivery**, not as a destination collection on
the event. Each `deliveries` row carries the destination configuration captured when its event was
accepted — the immutable payload fields `url`, `http_headers` and `secret_encrypted`, plus the nullable
provenance pointer `endpoint_subscription_id` — and `events` holds
no destination JSONB at all, so there is exactly one copy and nothing to keep in sync.

* Adding, disabling, or deleting an `endpoint_subscription` afterwards cannot change the snapshot
  payload an already-accepted event delivers to, because the rows were already written. Deleting the
  subscription row may clear only `deliveries.endpoint_subscription_id` (F3-C); it does not change the
  payload and does not cancel the delivery.
* Soft-deleting an endpoint does not orphan or alter existing deliveries.
* "Which destinations will this event hit?" is `SELECT ... FROM deliveries WHERE event_id = $1`
  (indexed by `idx_deliveries_event`); "what did this delivery use?" is the row itself.
* The snapshot is the audit-grade answer to "why did this destination get this payload".

Two consequences of putting the snapshot on the delivery rather than the event: an event that matched
zero live destinations produces no delivery rows at all, so `events.destination_count = 0` is the only
trace of "nothing matched" — which is why that column survives while `events.status` was removed
(U36 approved) — and a destination's encrypted secret is duplicated into a 90-day-retained table. The
copy-to-delivery snapshot is the chosen storage shape (A8). The D6/U25 envelope and custody architecture is
Owner-approved (2026-11-24), the development file-KEK provider implements it, and production KMS/Vault custody
remains absent.
For rotation and retirement, the 2026-11-24 proposal distinguishes the two paths: **endpoint envelopes may be
rewrapped** under a rotated KEK, but **delivery snapshots must not be rewrapped** because A8 makes the snapshot
columns immutable after acceptance. A KEK remains available while any retained endpoint envelope — including a
soft-deleted endpoint row — or delivery snapshot references it; the 90-day window is not a retirement calendar.
D6-D is **OWNER APPROVED** as architecture (proposal §4.4; 2026-11-24); production retirement/purge tooling is
**NOT IMPLEMENTED**.

### 5.3 Dispatch, delivery, retry

```text
outbox-publisher : pending outbox_messages --FOR UPDATE SKIP LOCKED--> RabbitMQ --> 'published'
                   <- each pass claims a pending row in place: status/attempts/lease columns move, its id does not (schema §4.10)
worker           : consume -> load delivery (which carries its own snapshot columns)
                   -> pre-HTTP abort: insufficient fixed-lease budget (B4) or decrypt failure (B5)
                      = no HTTP, no delivery_attempts row, no attempt_count increment
                   -> HTTP POST
                   INSERT delivery_attempts (attempt_number, response, duration, outcome, finished_at) at completion
                     2xx                 -> deliveries.state='succeeded', completed_at=t,
                                            next_attempt_at=NULL, clear the worker lease
                     5xx/408/429/net err -> state='retrying', next_attempt_at=due
                                           dispatch_seq = dispatch_seq + 1
                                           INSERT outbox_messages (kind='delivery.retry',
                                             sequence=dispatch_seq,
                                             available_at=GREATEST(t, deliveries.next_attempt_at))
                                             <- a NEW row, not a re-stamp: the worker generates its UUIDv7 for this INSERT
                                           clear the worker lease
                     other 4xx           -> state='failed', completed_at=t, next_attempt_at=NULL,
                                            clear the worker lease   (terminal, no retry)
                     event TTL hit (post-HTTP) -> state='expired', completed_at=t, next_attempt_at=NULL,
                                            clear the worker lease
```

**Pre-HTTP aborts are not completions.** An insufficient fixed-lease budget (B4) or a decryption failure
(B5) starts no HTTP request and writes no `delivery_attempts` row or `attempt_count` increment. The
`expired` branch above is the post-HTTP event-TTL case; a genuine HTTP outcome at or after
`events.expires_at` is persisted and no retry row is created.

**Recovery event expiry takes precedence over an active worker lease (B2/B3; owner-approved 2026-11-23,
§12.18).** Recovery Sweep may transition a Delivery to `expired` when its Event has expired even if a Worker
still holds a valid dispatch lease, and event expiry outranks the existence of that lease. That transition
sets `state='expired'`, sets `completed_at`, clears `next_attempt_at`, clears the paired worker-lease tuple,
and creates no `delivery_attempts` row, no `attempt_count` increment, no retry Outbox message and no new
dispatch generation. A Worker that later presents its stale claim to `Complete` is rejected by fencing: it
inserts no attempt, mutates nothing and cannot revive the Delivery. This does **not** withdraw B3's
late-lease completion rule: if the Sweep has not already transitioned the Delivery and the Worker presents
the matching valid fencing token under the existing completion contract, B3 applies unchanged. The decisive
distinction is whether Recovery has already performed the expiry transition and invalidated the old claim.

**Gate derivation (A3/B1/B2, owner-approved 2026-11-22).** After the required row locks are held, the
writer samples `t = clock_timestamp()`, revalidates time predicates against `t`, and uses that same `t`
for the associated writes. With `N = deliveries.next_attempt_at`, `A = outbox_messages.available_at`
and `B(c)` the configured publish backoff for consumed claim count `c`: a new initial, retry, repair or
redispatch row uses `GREATEST(t, N)`; re-arming an existing failed row uses `GREATEST(A, t, N)`; a
publisher transport failure or expired publisher lease uses `GREATEST(A, t + B(c))`. A claim, a
successful publication or a fenced zero-row update changes no gate. An existing row's gate never moves
backward, and a replacement row does not inherit a deleted row's gate.

A delivery therefore produces one dispatch message plus one retry message per backoff step — several
`outbox_messages` rows over its life, which is exactly the one-to-many relationship in the agreed
design and the reason no unique constraint is placed on `outbox_messages.delivery_id`; uniqueness is
on the generation-bearing pair `(delivery_id, sequence)` instead. Each of those rows carries
the `id` it was created with for as long as it exists: a row's own primary key is written at its `INSERT`, and no
documented statement updates one, so "each retry is its own row" and "a re-armed row keeps its identity" are the
same rule seen from two sides (schema §4.10 states both). The only `UPDATE` of a `uuid` identifier in these flows is
`idempotency_records.event_id` at §5.1 step d, which sets a **link** to another row's key rather than rewriting a key.

`delivery_attempts` is append-only, but a row exists only after an HTTP result has been observed and
durably persisted: no row is written at claim or start, and a crash after the remote request but before
commit may leave no row. Absence of a row is not evidence that no HTTP request occurred. `deliveries`
holds only current state and scheduling columns. (U18, approved.)

### 5.4 Retry policy (proposed defaults, unresolved)

Doubling backoff with full jitter, roughly 10s, 30s, 2m, 5m, 15m, 1h, 4h, 12h for a default budget of
8 attempts, honouring `Retry-After` on 429 up to a cap. Per-destination isolation is automatic because
each destination has its own delivery row, so a persistently failing endpoint cannot block others.
These numbers are illustrative and are not approved: U24's retry classification and values remain
unresolved.

### 5.5 Ingestion response and error contract (U42 approved, revision 10)

**Success responses.** Both a first acceptance and a replay return `202 Accepted` with the same body:

```json
{ "event_id": "0198…", "destination_count": 3, "received_at": "2026-11-22T09:15:00Z" }
```

`202`, not `200`, because the approved requirement is that a replay returns the *original Event result* —
letting the status vary would make the status itself a signal the caller has to have remembered, and U5
already fixed `202` for acceptance. **The body is byte-identical**, which is achievable only because every
field comes from a column written once at accept and never updated (`events.id`, `.destination_count`,
`.received_at`); no response is stored anywhere. Consequence to respect forever: adding a field to this
body is an API-version change, not a field addition, because a volatile field silently breaks replay
equivalence rather than visibly failing.

**Replay marker.** `HookFlow-Idempotent-Replay: true` is sent **only** on replays; on initial acceptance the
header is absent (an implementation sending `false` is equally correct — both mean "not a replay"). A
header was chosen over a body member `"replayed": true` on purpose: a body field would force the first
acceptance to carry `false` to stay uniform, which widens the `202` contract, and it would make the replay
body differ from the original — contradicting the rule above. Headers are also outside the U41 fingerprint
inputs, so the marker can never influence replay detection, and it comes free as a replay-rate signal in
access logs.

**Error responses.** RFC 9457 *Problem Details* (`application/problem+json`) is adopted for the rejection
paths, with a stable machine-readable `code` per reason:

| Case | Status | `code` |
| --- | --- | --- |
| Header missing or failing the 1–200 length rule | `400` | `missing_idempotency_key` / `invalid_idempotency_key` |
| Body is not valid JSON, or is not an object — at the **request document's** top level or at **`payload`** (the `payload` half approved in revision 14, §11 item 30) | `400` | `invalid_json_document` |
| Object repeats a key | `400` | `duplicate_json_key` |
| Integer **value** outside ±(2^53 − 1), in any notation — including a fractional literal whose binary64 conversion reaches 2^53 (guard **G1**, approved in revision 14) | `400` | `integer_out_of_safe_range` |
| Non-finite or otherwise non-representable value | `400` | `non_finite_number` |
| Payload over the size cap | `400` | `payload_too_large` (OD-5 approved and implemented: exactly 1 MiB, see §16.10) |
| Same key, different fingerprint | `409` | `idempotency_key_conflict` |

**Never** in an error body: the request payload, the stored `request_hash` or any digest prefix, another
request's `event_id`, secret material, or internal identifiers. `detail` is a **static** string per code —
interpolating request content into it is the leak path §8 forbids, and static text is what makes the codes
groupable in logs. A 409 deliberately does not reveal whether the stored request was older, larger, or from
a different API key.

**Scope note, deliberately recorded:** adopting RFC 9457 here **establishes the error convention for the
whole API**, not only for `POST /v1/events`. These documents previously defined no error envelope at all, so
this is the moment the convention is chosen; the other endpoints (organization, endpoint, subscription and
API-key routes) are to be implemented against RFC 9457 with the same `code` discipline, and any deviation
should be raised rather than assumed. Auth failures (`401`/`403`), rate limiting (`429`) and the delivery-side
response contract (what HookFlow *sends to subscribers*, U25) are separate surfaces and are **not** decided
here beyond the shared envelope.

**Mapping to the parse order:** §15.3 — **approved as normative in revision 14** (§11 item 29) — assigns each of
these codes to a step of the ingestion parse and states where each check must run so that a rejected document
never reaches the idempotency claim. Under the approved shape (U48 Option B + guard G1) it adds **no** status
code, no body field and no `detail` text to this table: G1 reuses `integer_out_of_safe_range`, because the case
it closes is an unsafe integer value arriving through a fractional spelling, and the `payload`-must-be-object rule
reuses `invalid_json_document` for the same reason. **G2 is deferred**, so `number_not_exactly_representable` is
**not** part of this contract (§15.1.1, §15.4); the residue it would have refused is counted by
`hookflow_ingest_number_inexact_total` instead, which is never a response field. The table above is therefore the
complete code list. It also keeps the
parser's JSON-pointer position information in the **log**, not in the response, because `detail` is fixed to
static text per code above.

## 6. Delivery semantics and guarantees

| Concern | Guarantee | Mechanism |
| --- | --- | --- |
| Ingestion durability | Accepted events are never lost; `202` implies durability | `events` + `deliveries` + `outbox_messages` committed, then the response is sent (U5 approved) |
| Duplicate ingestion (same key, same content) | Replays the original event — same `202`, byte-identical body, `HookFlow-Idempotent-Replay: true` — and creates no new event, delivery or outbox row | `idempotency_records` `UNIQUE (organization_id, idempotency_key)` + `request_hash` match, where "same content" means identical `SHA-256(RFC 8785 JCS of {"event_type","payload"})` — key order and formatting cannot break the match (U4 + U41 + U42 approved) |
| Same key, different content | **HTTP 409 Conflict**, RFC 9457 body with `code=idempotency_key_conflict`, nothing written anywhere | `request_hash` mismatch against an unexpired record; a different event type or any semantically different payload value changes the digest (U4 + U41 + U42 approved) |
| Replay response identity | The replayed body equals the original, indefinitely | Every field comes from a column frozen at accept (`events.id`, `.destination_count`, `.received_at`); no response is stored, and `updated_at` is excluded precisely because it changes (U42 approved) |
| Rejection bodies | Stable `code`, static `detail`; never a payload, digest, secret or internal identifier | RFC 9457 `application/problem+json` for the API's error convention, adopted with U42 and to be applied consistently across endpoints |
| Committed idempotency record | `event_id` is never NULL in a committed row | `trg_idem_linked` (`DEFERRABLE INITIALLY DEFERRED`, INSERT and UPDATE) validates at commit, plus application tests. **The column stays nullable in V1.** U1 was decided application-side in revision 16, which satisfies the condition U44 recorded for a possible later simplification; that simplification is a separate DDL decision and is **not** applied here (U44 approved) |
| Identifier generation | Every entity id is a **UUIDv7** value produced by the application before the `INSERT`, in all three processes; an id authorizes nothing on its own | One application-side generator, **no `DEFAULT` on any of the 13 `id` columns**, so a missing id is a `NOT NULL` failure rather than a silent database-side value; access is decided by the `organization_id` tenancy invariant and scope checks, never by the id's shape, order or guessability (U1 approved, revision 16; §4.1, §8, schema §3.1) |
| Key-use history | None retained: the record is the latest use only | Takeover `UPDATE`s in place; **no history table and no per-conflict `audit_logs` row**, so the 409 path writes nothing in any transaction (U45 approved) |
| Expired-record cleanup | Storage reclamation only; reuse is decided by `expires_at`, never by the job | `worker` ticker, 500 per batch, 1 minute, max 5 batches per tick, `FOR UPDATE SKIP LOCKED` + `DELETE … USING`, one transaction per batch, failures isolated and alertable on full-batch streaks (U43 approved) |
| Body the JSON/JCS model cannot represent (invalid JSON, not an object, duplicate key, integer value outside ±(2^53−1) in any notation, non-finite value) | **HTTP 400**, no idempotency claim, no event | Validation and fingerprinting precede `BEGIN`; raw-byte hashing is explicitly **not** a fallback, and `jsonb` cannot enforce any of it (U41 + U46 + U42 approved) |
| Concurrent requests on one key | Exactly one accepted event | claim `INSERT … ON CONFLICT DO NOTHING` blocks the loser on the unique index; the loser's transaction writes nothing (U4 approved) |
| Key lifetime | 72 hours, then reusable; event history unaffected | `expires_at` + batched sweeper; the FK points record → event, so expiry cannot delete history (U4 approved) |
| Dispatch vs. broker | No publish-before-commit, no orphan publish | Transactional outbox + publisher confirms + row state |
| End-to-end | **At-least-once** | Redelivery on unack, retry loop, no transactional HTTP |
| Duplicate delivery to consumer | Possible and expected | Consumers dedupe on `X-HookFlow-Delivery-Id` / event id |
| Ordering | Not guaranteed | Concurrent workers; no per-endpoint partitioning in V1; and under U1 (approved, revision 16) business ordering never consults identifier order — ordering needs a timestamp column, which is what `created_at`, `next_attempt_at` and `available_at` are for. A deterministic tie-breaker after a timestamp (schema §4.10's outbox claim) is not business ordering. Nor is id order guaranteed: with no generator selected (U1 point 6) even two ids from one process in one millisecond have no promised relative order (§10) |
| Zero-destination events | Accepted and persisted, never rejected | `destination_count = 0`, no `deliveries`/`outbox_messages` rows, still `202` (U36 approved) |
| Processing state | `deliveries.state` is the single source of truth | `events` has no status column (U36 approved); event progress is an aggregate over deliveries |
| Endpoint count | Hard ceiling per organization, race-free | `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG` (default 100) enforced under `SELECT … FOR UPDATE` on the `organizations` row (U37, U38 approved) |
| Routing/endpoint mutation safety | In-flight work unaffected | Per-delivery snapshot columns written at accept time (U33 approved) |
| Tenant isolation | Enforced by the database | Composite FKs on `deliveries`, `delivery_attempts`, `outbox_messages`, `endpoint_subscriptions`, `idempotency_records` |
| History | 90 days for events + deliveries; 72 hours for idempotency records | Retention windows and purge order (§7, schema §6, U2) |

## 7. Reliability, scaling, operations

* **Broker distributes work; the database is the source of truth.** Outbox payloads carry ids, and
  the worker reads the row — redelivery is harmless and broker loss is recoverable.
* **SKIP LOCKED polling** lets multiple publisher instances run without leader election. Batch size,
  poll interval, and whether to add a `LISTEN/NOTIFY` fast path are open (U22).
* **Eager fan-out cost (approved, U5)**: ingestion inserts `2 + N + N` rows (idempotency record, event,
  N deliveries, N outbox messages) where N is the number of matching live endpoints, so it must batch the
  writes. N is capped by `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG` (U37 approved, default **100**), making
  the worst case 202 rows in one transaction. Raising or lowering the cap is a configuration redeploy,
  never a migration.
* **Idempotency sweep (U4 requirement 6, U43 approved).** `idempotency_records` is cleaned on its own
  72-hour clock from `idx_idem_expiry` by a ticker **inside `worker`** — no new binary. Approved shape:
  batch size **500**, interval **1 minute**, at most **5 batches per tick**, each batch its own transaction,
  selected by a locking CTE (`FOR UPDATE SKIP LOCKED` + `DELETE … USING`) so concurrent replicas never
  block each other and no leader election is needed. `expires_at <= now()` is compared by the **database
  clock**. Correctness never depends on the job: a key becomes reusable at `expires_at` whether or not the
  sweep has run, a failure is logged and counted and never reaches acceptance or delivery, and if every
  replica is down the only consequence is that the table grows. **Alert on consecutive full-batch streaks**
  — that is the observable signal that the backlog is outrunning the job; an absolute row count is not
  needed. All three numbers are configuration, not literals:
  `HOOKFLOW_IDEMPOTENCY_SWEEP_BATCH_SIZE`, `HOOKFLOW_IDEMPOTENCY_SWEEP_INTERVAL`,
  `HOOKFLOW_IDEMPOTENCY_SWEEP_MAX_BATCHES_PER_TICK`. The **72-hour window itself is not configuration** —
  U4 fixed it as policy and U43 deliberately did not add a knob for it.
* **Endpoint creation is serialized per organization (U38 approved).** Creation and `paused → active`
  activation take `SELECT … FOR UPDATE` on the parent `organizations` row, then count active
  endpoints, then write — all inside one transaction. That is what makes the cap race-free; it also
  means two creates for the same tenant run sequentially. Different organizations never contend, but
  no future job should hold that row lock across network calls.
* **One transaction boundary per use case (U39 approved).** The application service in
  `internal/app/event` (U40) opens the Unit of Work and hands the same `Tx` to every repository. A
  handler or worker that begins its own transaction around the same rows would double-lock and defeat
  the atomicity guarantee, so the pattern has to be uniform rather than per-module.
* **Backpressure**: shed load with 429 before outbox lag crosses a threshold; the metric and limit are
  unspecified (U19 covers whether plan limits participate).
* **Observability hooks**: `/healthz` and `/readyz` per process, outbox depth and age, delivery
  success rate, attempt latency histogram; structured logs carry `organization_id`, `event_id`,
  `delivery_id`, `attempt` — never secrets and never payload bodies. Metric labels stay low-cardinality:
  entity ids appear in structured logs, never as metric labels (RB-6). Added with U43: rows deleted per sweep
  tick and the consecutive-full-batch streak (the backlog signal), plus sweep errors. The U42 replay header
  also makes replay-vs-fresh acceptance countable from access logs at no extra cost.
* **Ingestion inexactness counter (approved, revision 14 — §11 item 31).** `hookflow_ingest_number_inexact_total`
  is incremented at §15.3 stage 8 whenever an **accepted** number's canonical decimal differs from the literal's
  own decimal value (`1e-400` → `0`, `1.234567890123456789` → `1.2345678901234567`). It is metrics/logs only — never
  a response field, never a column, never a request-path decision. It exists because **G2 was deferred**: with the
  exact-round-trip guard declined, this counter is the only instrument watching the residue, and it is what makes
  that deferral reversible (§15.1.1). **It is not a validation threshold and no numeric limit is fixed here** — none
  was invented. Any alert is an operational configuration choice alongside the U43 streak alert, made against
  observed traffic once real producers exist; if it ever shows non-negligible inexact traffic from first-party
  clients, that is evidence for a versioned API change (reviving G2), not for silently tightening the boundary.
* **Retention purge** must run child-first (`outbox_messages` → `delivery_attempts` → `deliveries` →
  `events`) in bounded batches. Non-partitioned tables and the bounded batched purge are approved (A5);
  table coverage and batch/window values remain unresolved (U2).
* **Retention is not a KEK-retirement calendar.** `endpoints` is outside the retention table and uses soft
  deletion, so a soft-deleted endpoint row keeps `secret_encrypted` indefinitely unless a separate purge removes
  it; no such endpoint purge exists today. Every persisted endpoint envelope (live or soft-deleted, until its
  ciphertext is actually purged) and every delivery snapshot counts as a KEK reference. A KEK may be retired only
  after all such ciphertext is purged and a bounded reference audit reports zero references; an unreadable value
  blocks retirement. Because neither the endpoint purge nor the Events/Deliveries/Attempts purge exists in this
  repository, **KEK retirement is operationally blocked** under the Owner-approved D6-D rule; the production
  retirement/purge tooling needed to unblock it is **NOT IMPLEMENTED**.

## 8. Security posture

* **API keys are hashed, never encrypted.** Only a salted verifier lives in
  `api_keys.secret_hash` (+ `hash_algorithm`); lookup is by non-secret `key_prefix`, verification is a
  constant-time operation, and the plaintext is shown exactly once at creation.
* **Endpoint webhook secrets are reversibly encrypted**, because the worker must reproduce the HMAC
  of a delivery. `endpoints.secret_encrypted` holds reversible ciphertext. The sketch
  `v<n>:aes-256-gcm:…` is illustrative only, not an approved envelope or key-version format; A8 approves
  only the encrypted copy-to-delivery snapshot. The DEK is configured outside the database and is never a
  column, a log field, or a fixture. Under the approved U33 snapshot each `deliveries` row also carries an
  encrypted copy, which widens where a decryptable secret exists. The copy-to-delivery snapshot is the
  chosen storage shape (A8, 2026-11-22); envelope format, key-version representation, DEK custody and the
  rotation policy remain **open under U6**.
* **These two models must not be conflated.** A reviewer seeing "secret column" on both tables should
  find the reason stated in the schema document, not discover it in code.
* **An identifier is not a credential (U1 approved, revision 16).** Every entity id is an
  application-generated **UUIDv7** value: a 48-bit millisecond timestamp in the most-significant bits plus
  at least 62 random bits, so the *creation time is approximately public* while the value itself stays
  unguessable. That is an accepted property, not a leak to mitigate, and it is why ids appear freely in the
  `202` body (`event_id`), in `X-HookFlow-Delivery-Id`, and in structured logs (§7). What it must never become
  is an authorization input: access is decided by the API key's scopes and by the `organization_id` tenancy
  invariant (§4.1, §6, schema §3.1), every query is scoped by organization, and no code path may treat "the
  caller supplied a plausible id" as evidence of permission. Timestamp-ordered ids are also not a sequence, a
  rate limit, a nonce, a capability token or a signing input — the delivery signature is HMAC under the
  endpoint's own secret (U25), and the ingestion digest is over `{"event_type", "payload"}` (U41), which
  contains no id at all. Whether the delivery-side canonical string names an id is U25's open question, and
  nothing about U1 pre-decides it.
* **Signature scheme**: HMAC over a timestamp plus body digest, with signature and timestamp headers
  so consumers can reject **delivery** replays — a different concern from the ingestion idempotency key
  below, and the two must not be conflated in code or docs. Header names, canonical string, and rotation
  overlap unresolved (U25).
* **Idempotency stores a fingerprint, not a body.** `idempotency_records.request_hash` is
  `SHA-256(UTF-8(RFC 8785 JCS of {"event_type", "payload"}))` (U41 approved), so replay detection never
  requires retaining raw request payloads outside the `events` row. The input deliberately excludes
  transport inputs — `Authorization`, `Idempotency-Key`, request timestamps, and every other header — so a
  digest can never become a fingerprint of *who* retried or *when*, and two identical retries cannot
  disagree because of serialization differences. **Invalid or unsupported JSON is a `400` with no idempotency
  claim written, and the implementation must not fall back to hashing raw request bytes** — the fallback
  would silently convert a formatting change into a false 409. **U46 closes the remaining parser holes at
  the same boundary**: duplicate object keys are rejected rather than resolved last-wins, integer values
  outside ±(2^53 − 1) are rejected so a magnitude the client could not round-trip never reaches a digest,
  and non-finite values are rejected — which also means the API must inspect number literals before any
  `float64` decode, because decoding first validates an already-rounded value (schema §4.7). The 409 response must not echo the stored
  hash or the original payload: doing so would let a caller test whether an unseen request body was
  previously submitted under a known key. Keys are already scoped by `organization_id`, so cross-tenant
  probing is blocked by the unique index itself.
* **Key reuse after expiry is intentional, not a defect.** A tenant that recycles key values gets a new
  event after 72 hours rather than a silent replay of a three-day-old one, because the expired branch
  resets the record (schema §4.13.3). The prior event survives untouched for support and audit purposes.
* **Authentication of users**: password hashes plus organization membership roles
  (`owner`/`admin`/`member`/`viewer`) for the future dashboard.
* **Destination URL policy (OD-4 approved and implemented in the development build; see §16.10).** HTTPS
  on port 443 only, host required, and no userinfo, fragment or IPv6 zone. Literal addresses are rejected
  immediately by an explicit IPv4/IPv6 range table plus the standard `netip` classification methods
  (`IsPrivate`, `IsLoopback`, link-local unicast/multicast, multicast, unspecified and interface-local
  multicast). Hostnames get an advisory bounded DNS preflight at endpoint create/update: a prohibited answer
  is `400 invalid_url`, while a DNS failure is allowed and never substitutes for delivery-time enforcement.
  At delivery the Worker resolves every A/AAAA answer with the request context, unmaps IPv4-mapped IPv6,
  rejects **any** prohibited answer (which also rejects mixed public/private sets) and any zone, selects an
  allowed literal address, dials that literal, never re-resolves within the attempt, verifies the connected
  remote address, keeps the hostname for TLS SNI and certificate verification, never disables verification,
  ignores proxy environment variables, does not follow redirects and revalidates on every retry (connection
  reuse is disabled). A disallowed destination is a permanent pre-HTTP failure (`destination_disallowed`):
  state `failed`, no attempt row, no `attempt_count` increment, no retry outbox row, cleared lease and a
  distinct metric. Private destinations that already exist fail closed; there is no backfill, pause or
  snapshot rewrite. URL syntax validation alone is not treated as an SSRF control, and network-level egress
  restriction remains the recommended defense in depth.
* **Least-privilege database roles**: the application role gets `INSERT`/`SELECT` on `audit_logs` and
  no `UPDATE`/`DELETE`; retention uses a separate role.
* **Data handling**: response bodies and errors are truncated before persistence; credential-bearing
  request headers are redacted before an attempt row is written. **U42** fixes the same rule on the way
  out: an RFC 9457 body carries a stable `code` and a static `detail`, never a payload, a digest, another
  request's `event_id`, or secret material. **U45** closes the one remaining leakage route by rejecting the
  per-conflict `audit_logs` write — a 409 stores nothing anywhere, so a probing caller cannot leave a
  searchable trace of what it guessed.

## 9. Canonical 13-table inventory

PostgreSQL, UUID primary keys — every one a **UUIDv7 value generated in the application**, with no database
default on any of them (U1 approved, revision 16) — snake_case plural names, `timestamptz` throughout. Storage is
unaffected by that decision: the column type stays `uuid`, 16 bytes with the RFC 4122 lowercase hyphenated text form,
identical for v4 and v7 values. **U3 is resolved to PostgreSQL 18** (owner approval, 2026-11-22); the
exact patch and tooling compatibility are verified before migration execution.
Rows 1–12 are the
originally approved inventory in its approved order; row 13 was added when **U4** was approved and is
last in the migration order because its composite FK needs `events`.

| # | Table | Owner module | Purpose |
| --- | --- | --- | --- |
| 1 | `users` | auth | Dashboard identities and password verifiers |
| 2 | `organizations` | organization | Tenant root; carries no plan column |
| 3 | `organization_members` | auth/organization | Users ↔ organizations with role |
| 4 | `api_keys` | apikey | Machine credentials, **hashed**, scoped, rotatable |
| 5 | `endpoints` | endpoint | Destinations; **reversibly encrypted** secret; soft-deleted via `deleted_at` |
| 6 | `endpoint_subscriptions` | subscription | Routing rule: one endpoint ↔ one `event_type` |
| 7 | `events` | event | Accepted events and destination count. **No status column** (U36); **no idempotency key column** (U4) |
| 8 | `deliveries` | delivery | One row per (event, endpoint) — state, scheduling, and the destination snapshot |
| 9 | `delivery_attempts` | delivery | One append-only row per completed HTTP call (U18) |
| 10 | `outbox_messages` | delivery/platform | Transactional outbox, **N rows per delivery** |
| 11 | `organization_subscriptions` | subscription | Commercial plan + subscription status per organization |
| 12 | `audit_logs` | audit | Append-only administrative trail |
| 13 | `idempotency_records` | event (written by `eventapp.AcceptEvent`; swept by `worker`) | Organization key → request fingerprint (`bytea`, RFC 8785 + SHA-256, U41) → accepted event, **72-hour** life (U4, cleanup per U43). Committed rows never have a NULL `event_id` (U44 trigger); the row records the latest use only (U45) |

Agreed relationships (implemented in schema §3.2): users↔organizations many-to-many through
`organization_members`; organizations one-to-many to `api_keys`, `endpoints`, `events`,
`organization_subscriptions`, `audit_logs` and — since U4 — `idempotency_records`; endpoints one-to-many
to `endpoint_subscriptions` and to `deliveries`; events one-to-many to `deliveries` and, since U4, to
`idempotency_records` (nullable child column, `ON DELETE CASCADE`); deliveries one-to-many to
`delivery_attempts` and to `outbox_messages`. That is 12 relationships from the original design plus 2
added with U4, for 14 total. **Revisions 8, 9 and 10 changed none of it**: 13 tables, 14 agreed
relationships, and no speculative entity, column or edge was introduced for the response contract (U42),
the cleanup job (U43), the NULL guard (U44) or the history question (U45) — the only new DDL object anywhere
is U44's constraint trigger on the existing table. The three nullable U12 provenance relationships
(`users→api_keys` creator/revoker, `api_keys→events`, `endpoint_subscriptions→deliveries`) are adopted by
A10 (2026-11-22): the ERD therefore has 17 relationship lines, while the original 14 keep their status and
only those three are newly approved. Their actions and same-organization forms were resolved on
2026-11-23 (F3-A/F3-B/F3-C, §12.17); that changes constraint detail, not the relationship inventory.

## 10. Verification and quality plan (for the next phases)

**Implementation status (Phase 7, 2026-11-25).** This section originated as the plan. §16 records the
implemented database, Publisher, Worker, messaging, Recovery, metrics, configuration and Phase 7 ingestion
tests, including PostgreSQL and RabbitMQ integration test files; §16.10 holds the Phase 7 evidence. The
acceptance and ingestion tests described below are implemented where §16.10 or the named test file records an
implementation, and remain plan material only where no implementation is recorded. D7 defines pinned
PostgreSQL 18 and RabbitMQ 4.4.1/4.3.8 GitHub service-container jobs, but no hosted run has executed them and
no pass is claimed.

Unit tests for pure logic: idempotency branch resolution (replay / 409 / expired takeover), request
fingerprint canonicalization (U41 approved, so the cases are now fixed: same members in a different
**order** → same digest; added indentation/newlines → same digest; an equivalent number written
differently → same digest for accepted values (integers are bounded by U46; fractional precision beyond a
double is U48's open question, not a licence to assume equality); a different `event_type` → different
digest; any changed
payload member, value, or array element → different digest; adding/removing a **header** such as
`Authorization` or `Idempotency-Key` → same digest, since headers are not inputs), snapshot column construction, backoff
computation, signature construction, envelope encrypt/decrypt round-trips. Invalid JSON and a
non-object body must each yield `400` with **no** `idempotency_records` row and no event — a test that also
proves the implementation did not fall back to hashing raw bytes. Negative digest tests belong here too:
`octet_length(request_hash) = 32` rejects a 64-byte hex encoding of the same value, which is the schema's
way of catching an implementation that hex-encoded before storing. Integration tests in
`test/integration/` against real PostgreSQL and RabbitMQ (U34 implemented for CI with pinned GitHub service containers; local Compose remains the development environment).
Each of these invariants gets a named test: key uniqueness per organization on
`idempotency_records`; cross-tenant composite-FK rejection for `deliveries`, `delivery_attempts`,
`outbox_messages`, `endpoint_subscriptions`, and `idempotency_records` (a `record.event_id` belonging to
another organization must be rejected even though both columns individually exist); routing change after
accept does not alter existing deliveries; soft-deleted
endpoint excluded from new fan-out; one open `organization_subscriptions` row per organization; more
than one `outbox_messages` row per delivery; retention purge child-first order. Added in revision 3:
eager fan-out is atomic (a forced failure after the `deliveries` insert leaves **no** event row, and a
client that received `202` implies all three sets of rows exist); `events` stores no destination
columns at all; exact-match routing never matches a prefix; the live-endpoint ceiling rejects the
`limit + 1`-th create. Added in revision 4: an event matching zero endpoints still persists, with
`destination_count = 0`, no `deliveries`, no `outbox_messages`, and a `202` response (U36); a schema
assertion that `events` has no `status` column (U36); `events.destination_count` equals the actual
`deliveries` row count for every accepted event (U36); **20 concurrent endpoint creates against a
low test limit end at exactly the limit**, which is the regression test for the U38 organization-row
lock; a `paused → active` activation over the limit is rejected through the same protocol, proving the
rule is a shared protocol and not a handler detail (U38); and the accept use case runs in one Unit of
Work, so a repository that commits independently fails the test (U39). Added in revision 7: N concurrent
requests carrying the same key produce exactly one event and one record (U4 requirement 5); the replay
branch writes **no** rows; the content-mismatch branch returns 409 and writes no rows; taking over an
expired record leaves the original event, its deliveries and its attempts intact (U4 requirement 7); the
sweeper deletes only rows with `expires_at <= now()` and its delete cascades to nothing; and a schema
assertion confirms `events` carries neither `idempotency_key` nor `dedupe_hash` (U4). Added in revision 8:
two bodies differing only in member order or whitespace replay rather than conflict (U41); the same key
with a different `event_type`, or with one changed payload value, returns 409 (U41); the same key retried
with different `Authorization`/`Idempotency-Key`/timestamp inputs still replays, proving headers stay out
of the digest (U41); a malformed or non-object body returns 400 and leaves **no** `idempotency_records`
row, which is the test that would catch a raw-byte fallback (U41); inserting a 64-byte hex encoding of a
digest fails `chk_idem_hash_len`, and any `hash_algorithm` other than `sha256` fails `chk_idem_hash_alg`;
and the digest is computed before `BEGIN`, so a slow canonicalizer cannot hold a row lock. Added in
revision 9 (U46 approved): `{"a":1,"a":2}` returns 400 with no claim; `9007199254740992` and
`-9007199254740992` (±2^53) are rejected while `9007199254740991` is accepted, so the boundary is tested on
both sides; a magnitude sent as a string is accepted and survives into the delivered body unchanged;
`NaN`/`Infinity` literals are rejected; a deep document and an array of numbers hit the same rules;
and the regression test that decode-first implementations fail — an out-of-range integer must be refused
**on the literal**, so a fixture asserting `9007199254740993` produces 400 rather than a stored
`9007199254740992`. Added in revision 10 (U42–U45 approved):

* **U42** — a replay returns `202` with a body **byte-identical** to the original acceptance (assert on the
  serialized bytes, not on parsed equality), `HookFlow-Idempotent-Replay` is absent on the first response
  and `true` on the replay, the replay branch still writes **no** row in any table, and `destination_count`
  of `0` replays identically too (U36 + U42 together). Error tests: each rejection produces the documented
  `code` with `application/problem+json`, and a leak assertion proves no 409 or 400 body contains the
  payload, any digest prefix, another request's `event_id`, or secret material.
* **U43** — two sweepers running concurrently never block each other (`SKIP LOCKED`), a sweep of a table
  with in-flight claims leaves those rows alone, acceptance is unchanged when the sweeper is stopped or
  failing, the batch never exceeds the configured 500, and a tick stops at 5 batches. Because
  `SKIP LOCKED` can legitimately return a short batch while rows remain contended, the test asserts the
  *streak* signal rather than a row count.
* **U44** — a transaction that claims a key and commits without linking it **fails at COMMIT** (the
  trigger fires deferred), leaving no record, event, delivery or outbox row; an expired-record takeover that
  sets `event_id` back to NULL in the same statement it rewrites also fails; and the invariant means the
  replay branch never needs a NULL path.
* **U45** — after takeover, the earlier fingerprint is gone and no history table exists; a conflict is
  asserted to leave **no** `audit_logs` row, which is the regression test for the rejected audit-write
  option (§14.4).

**Added in revision 14** (U30's toolchain half, U47, U48 + G1, the parse order, the `payload` object rule, the
counter and the conformance gate approved). These are the tests the approvals are obliged to bring, and the
distinction between what has already run and what has not is part of the contract:

* **Canonicalizer conformance, in CI, before merge** (§11 item 32): RFC 8785 §3.2.2 and §3.2.4 serialization
  vectors compared byte-for-byte, the §3.2.3 UTF-16 ordering vector, Appendix B Table 1 in full, the
  **Appendix I large sample-value file**, **V8 cross-checks**, **fuzzing** over generated documents, and the
  idempotence/fixed-point assertions (canonicalizing twice yields identical bytes, and canonicalizing an
  already-canonical document changes nothing). Revisions 12–13 executed the first group — golden document,
  ordering vector, Appendix B, duplicates, raw literals, overflow/underflow, lone surrogates, escapes,
  hostile sizes, document-level acceptance — against **both** candidate implementations on **go1.27.3**,
  outside the repository. **Phase 7 implements the selected path in `internal/platform/jcs` and executes,
  on go1.27.4 locally, the §3.2.2 golden document with a fixed-point re-run, the §3.2.3 ordering vector,
  the Appendix B Table 1 rows that have a JSON representation (non-finite rows are refused before
  serialization), duplicate-name rejection at the top level and nested, string escaping, invalid UTF-8,
  lone surrogates, negative zero, the §15.3 boundary literals, G1 and the inexactness counter.** **Still
  pending: the Appendix I file, V8 cross-checks, a sustained fuzzing campaign (the fuzz target runs only its
  seed corpus under `go test`), and execution of this suite by hosted CI.** The full approved gate has
  therefore not passed, and nothing in this document may be read as claiming that it has. Earlier statements
  in this document that no go1.27.4 run exists are superseded for that narrow fact by this local Phase 7 run;
  they remain accurate about the unexecuted gate items.
* **Parser identity:** canonicalization runs on `encoding/json/jsontext` from the pinned standard library
  (U47 approved; §11 item 26) with **no third-party JSON dependency** in the fingerprint path — swapping to
  the goccy backport would be a decision change, not an implementation detail.
* **The numeric boundary set** as one table-driven test over the §15.3.1 literals: `1.200e1`, `1.2000e2` and
  `1200e-2` accepted as the integers 12, 120 and 12 (the trailing-zero cases revision 12 misclassified);
  `1.2001e1` accepted as `12.001`; `9007199254740992.0` refused at the literal stage; `9007199254740992.5`
  refused **only** by G1; `1.7e308`, `1E30`, `9223372036854775807` and `1e400` refused before conversion;
  `0.1` accepted with the counter silent; `1e-400` accepted, canonicalized to `0` and **counted**. Plus the
  hostile-size pair — a 21-digit exponent and a 200 000-digit literal — which must not allocate work
  proportional to the exponent.
* **Document shape:** a top-level `123`, `"x"`, `null`, `true` or `[]` is `400 invalid_json_document`; a
  `payload` written as an array, a string, a number, a boolean or `null` is the same code, **before** the claim
  and with no row in any table; an unrecognised member inside `payload` is accepted and changes the digest like
  any other content change (§11 item 30). `{}{}`, trailing garbage and a leading BOM stay rejected.
* **Counter hygiene:** an inexact accepted number increments `hookflow_ingest_number_inexact_total` once per
  number, and the same request yields the identical `202`/replay result whether the counter is at 0 or at 1 —
  the test that proves the metric cannot influence acceptance.
* **Order, not just outcome:** a fixture asserting the stage-4 verdict arrives **before** any `float64`
  conversion is consulted (a decode-first implementation passes `9007199254740993`), and that steps 1–7 all run
  before `BEGIN`. No new configuration key is introduced by any of this, and `maxBodySize` remains unset until
  **U17** decides it.

**Added in revision 15** (U1's strategy — application-generated UUIDv7, no database-generated defaults, one strategy
across all processes; the owner approved it in **revision 16**, §11 item 34 and §12.15, and revision 15's claim to have
approved it was premature). These are the tests the approval obliges, and none of them requires a dependency to be named
or a PostgreSQL patch or tooling combination to be verified (U3's target is PostgreSQL 18):

* **There is no default to lean on:** an `INSERT` that omits `id` must fail on the `NOT NULL` violation instead of
  receiving a database-generated value — the check that no migration ever adds a `DEFAULT` clause to an id column.
* **One strategy, three processes:** ids written by `api` (`events`, `deliveries`, `outbox_messages`,
  `idempotency_records`) and ids written by `worker` (`delivery_attempts`, and the **new** `outbox_messages` row each
  retry step inserts — see §5.3) come from the same generator, so a row created by one process is indistinguishable in
  id shape from a row created by another.
* **v7 shape and uniqueness:** the version nibble and the variant bits are those of RFC 9562 v7, the 48-bit timestamp
  field decodes to the millisecond the value was generated, and a large sample from one generator contains no
  duplicate value — validity and uniqueness are what U1 commits to. Both are asserted on the generator interface the
  three processes share, which is also what lets fixtures substitute a fixed sequence and makes byte-identical replay
  bodies and golden digests testable without a clock. **Strictly increasing order within a process, or within one
  millisecond, is deliberately not asserted.** RFC 9562 §5.7 fills the 74 bits after the timestamp with random data by
  default and makes the sub-millisecond-precision and counter sub-fields an optional *MAY*, "to guarantee additional
  monotonicity within a millisecond", so same-millisecond ordering is something a generator *may* provide rather than
  something UUIDv7 means. No generator is selected (U1 point 6), so a test that required monotonicity would constrain
  the dependency decision instead of checking this one. If the implementation that is eventually chosen explicitly
  guarantees it, a test may assert it then and record it as a property of that generator, not as a U1 requirement. The
  timestamp assertion is read the same way: §6.1 of the RFC "makes no requirement or guarantee about how close the
  clock value needs to be to the actual time", so the test checks the field's shape and rough agreement, and defers
  any precision, smearing or clock-handling policy to the generator that is eventually chosen.
* **Business ordering never comes from an id:** two rows whose id order and `created_at` / `next_attempt_at` order disagree
  must be scheduled by the timestamps, which is the regression guard for §6's ordering row and for U1 point 5. It is
  also why the absence in the bullet above costs nothing: no read, index scan or schedule in HookFlow uses an id as a
  business ordering key, so no monotonicity guarantee is needed from the generator either. A deterministic tie-breaker
  such as the outbox claim's `ORDER BY available_at, id` (schema §4.10) uses the id only after the timestamp and is not
  business ordering.
* **An id authorizes nothing:** a fixture that presents a well-formed, current UUIDv7 value belonging to another
  organization and asserts that the tenant-scoping rules refuse it, independent of whatever status codes the
  read surfaces later settle — the enforcement of the §8 statement, not a restatement of it.
* **No manifest effect:** U1 selects a strategy, not a library. Neither revision 15 nor revision 16 adds an import
  path, a module or a version. (Repository status after M1-E: `go.mod` exists with only
  `github.com/golang-migrate/migrate/v4` and `github.com/jackc/pgx/v5` as direct dependencies — no UUID library was
  added by these decisions; see §16.)

`/go-quality` was invoked and **could not be executed when this section was written**: there was no Go source
and no `go.mod`, so `gofmt`/`go vet`/`golangci-lint` had nothing to analyze. **Status after M1-E Phase 2.1:**
Go sources and `go.mod` now exist, and `gofmt -l cmd internal`, `go build ./...`, `go vet ./...` and the unit
and integration suites were executed on 2026-11-23 (§16); the `golangci-lint` configuration itself remains
U30's open half. Its prerequisites were nearly
gone at that time — the module path is fixed (U26), package naming is settled (U28), and **U47 is selected** (revision 14:
the standard-library `encoding/json/jsontext`, no third-party JSON dependency), so what is left is **U30's
configuration half**, which revision 14 deliberately did **not** approve: the `.golangci.yml` linter set, the
complexity budgets, `depguard` for the §4.1 rule-4 direction, `importas` for the §4.2 alias convention, the
GitHub Actions versions, and — recorded as unverified rather than as a risk to assume away — whether the
golangci-lint version available here is compatible with the go1.27.4 baseline. **U47's four parser
requirements** (duplicate-key detection before information is lost, literal-preserving numerics, no plain
`map[string]interface{}` decode, value-based integer judgment; schema §4.13.5) survive the selection unchanged:
they are now obligations of the chosen package plus HookFlow's own literal layer, not a shopping list. `/security-best-practices` was invoked and applied
to §8 of this document; its `references/` directory contains no Go or PostgreSQL files, so the review used
the skill's inlined general advice (random rather than sequential public identifiers — satisfied by UUID
primary keys, including for `deliveries.id` which is caller-visible) plus standard Go/PostgreSQL
practice. No report file was written because the instruction for this step was to modify only three
existing files.

## 11. Resolved decisions (canonical design plus review approvals)

Recorded so these are not re-litigated in the next phase. Items 1–6 come from the canonical inventory;
items 7–10 were approved in the review that produced revision 3 (U5 twice — the fan-out and the ceiling it
also introduced — plus U16 and U33), 11–14 in revision 4, 15 in revision 5, 16–17 in revision 6, 18 in
revision 7, 19 in revision 8, 20 in revision 9, 21–24 in revision 10, 25–32 in revision 14, **33 recorded in revision
15** — whose approval claim was premature — and **34 in revision 16**, which is the owner's actual approval of U1. The
**20** approved decision IDs are U1, U4, U5, U16, U26, U28, U33, U36, U37, U38, U39, U40, U41, U42, U43, U44,
U45, U46, **U47** and **U48** — 20 IDs across **34** items, because U5 occupies items 7 and 8, U48 occupies
items 27 and 28, and items 25 and 29–32 record approvals that were never standalone register IDs: item 25 is
the **toolchain half of U30** (the row stays open in §13), and items 29–32 are the parse order, the
`payload` object rule, the inexactness counter and the conformance gate. **The idempotency stack
(U4, U41–U48) is complete and canonical.** Nothing is left from the mechanics that stack generated; what
remains open is listed in §13, and none of it is re-opened by these items. Item 33 is the exception to the last
sentence in one respect only, and item 34 is the reason it must be read that way: **U1 was one of the rows §13 has
carried as open since revision 2; revision 15 recorded it as approved when no owner approval had been given, and the
owner's approval is §11 item 34, in revision 16.**

1. The inventory is the agreed one (§9) — 12 tables originally, **13 since U4 added
   `idempotency_records`**; `subscriptions`, `subscription_destinations` and
   `outbox_events` no longer exist in the design.
2. Routing is `endpoint_subscriptions` (endpoint ↔ event_type); billing is `organization_subscriptions`;
   `organizations` carries no plan.
3. The outbox table is `outbox_messages` and hangs off `deliveries`, one-to-many.
4. `Idempotency-Key` remains required and unique per organization.
5. Endpoint deletion remains soft via `deleted_at`; retention stays 90 days; API keys stay hashed;
   endpoint secrets become reversibly encrypted; composite FKs still enforce same-organization
   deliveries.
6. Fan-out reads `endpoint_subscriptions` at accept time and the snapshot protects accepted events from
   later routing changes.
7. **U5 — eager fan-out approved.** `events`, all selected `deliveries`, and their initial
   `outbox_messages` are created in the same PostgreSQL transaction, and the API returns `202` only
   after that transaction commits.
8. **U5 — endpoint ceiling approved.** A configurable maximum number of active endpoints per
   organization exists. Where the value lives and how it is enforced were left open at revision 3 and
   were closed in revision 4 as U37 and U38 (items 12 and 13 below).
9. **U16 — exact-match subscriptions approved.** No wildcard or glob matching in the initial
   implementation.
10. **U33 — per-delivery snapshot approved.** The destination configuration captured at accept time is
    preserved on each delivery; `events` holds no `destinations_snapshot`, and the same snapshot is not
    duplicated in both tables.
11. **U36 — event bookkeeping approved.** `events.status` is removed from the initial schema and is not
    replaced by a `discarded` value. `events.destination_count` stays as a non-negative integer. Every
    accepted event is persisted, including one whose subscription lookup matched zero active endpoints —
    in that case `destination_count = 0` and no `deliveries` or `outbox_messages` rows are created.
    `deliveries.state` is the source of truth for delivery processing state.
12. **U37 — endpoint ceiling configuration approved.** `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG`, initial
    default **100** active endpoints per organization, platform-level configuration rather than an
    organization column, changeable without a database migration. Plan-based limits may replace or extend
    it later (out of V1 scope, see U19).
13. **U38 — ceiling enforcement protocol approved.** Endpoint creation and `paused → active` transitions
    take `SELECT … FOR UPDATE` on the parent `organizations` row before counting active endpoints, and
    the count check plus the write happen in the same PostgreSQL transaction. Every code path that can
    raise the active count follows this same protocol. Consequence: endpoint activation and creation are
    serialized per organization, which is exactly what prevents a concurrent overshoot.
14. **U39 — transaction ownership approved.** An application service / use-case layer sits above the
    domain modules and orchestrates event acceptance through a shared Unit of Work, coordinating event
    persistence, subscription lookup, delivery creation and outbox message creation in one PostgreSQL
    transaction. Modules never import each other's internal implementation packages; ports are declared
    at the boundaries that need them and concrete wiring stays in the composition root. No module opens
    or commits its own transaction, and the modular monolith is not split into services.
15. **U40 — application layer location approved (revision 5).** `internal/app/` is the application
    service layer and `internal/app/event/` is the location of the event-acceptance use case. Both
    directories were created, containing only `.gitkeep` placeholders — no Go source was written at
    revision 5. *(Status after M1-E Phase 2.1: `internal/app/event/` is still a placeholder; the other
    `internal/app/*` packages listed in §16 are implemented.)* The
    rule that domain modules must not depend on `internal/app/*` is recorded in §4.1 rule 4.
16. **U26 — Go module path approved (revision 6).** `github.com/metamin44/hookflow`, matching the
    configured `origin` remote, so all imports are `github.com/metamin44/hookflow/internal/...`.
    `go.mod` was not created in revision 6; it exists now and the module compiles at this path (§16).
17. **U28 — package naming and aliasing approved (revision 6).** `internal/app/event` declares
    `package eventapp` and `internal/modules/event` declares `package event`; no directory was renamed.
    Directory-equals-package is the default and that pair is the only planned exception. An explicit
    import alias is required only when a local name collides with a stdlib or dependency name in the same
    file — in practice `internal/platform/http` against `net/http`, where `net/http` stays unaliased and
    the local import is aliased `platformhttp`. Full table and rules: §4.2.
18. **U4 — idempotency policy approved (revision 7).** Keys are unique within an organization; records
    live **72 hours**; a repeat of the same key with the same request content returns the original Event
    result and creates no new Event, Delivery or Outbox message; the same key with different content
    returns **HTTP 409 Conflict**; concurrent requests on one key yield exactly one accepted Event; after
    expiry the record is cleanup-eligible and the key reusable; expiring a record never deletes the
    associated Event or its delivery history. Implemented by the 13th table `idempotency_records`, with
    `events.idempotency_key`, `uq_events_org_idem` and `events.dedupe_hash` removed so no idempotency data
    is duplicated. What remained open at revision 7 was mechanics only — **U42–U45**, which revision 10
    then approved (§14), plus **U47** and **U48**, opened by revisions 8 and 9 and approved in revision 14.
    *(Status note, revision 17: the "awaiting approval" phrasing this item carried was revision 7's; the
    decisions themselves have been approved since revision 10/14 and are not open.)*
19. **U41 — request fingerprint canonicalization approved (revision 8).** The fingerprint is
    `SHA-256(UTF-8(RFC 8785 (JCS) of {"event_type": …, "payload": …}))`. JSON member ordering and
    insignificant formatting cannot change it; a different `event_type` or a semantically different payload
    always does. Same organization + key + fingerprint replays (requirement 3), same organization + key +
    different fingerprint returns **HTTP 409 Conflict** (requirement 4). Transport inputs — `Authorization`,
    `Idempotency-Key`, request timestamps and all other headers — are excluded. A body with no canonical
    form (invalid JSON, not an object, or content RFC 8785 cannot represent) is rejected with **HTTP 400
    before the key is claimed**, and **raw-byte hashing is not a fallback**. Storage consequence:
    `request_hash` is `bytea` with `CHECK (octet_length(request_hash) = 32)` and `hash_algorithm` is pinned
    to `'sha256'`. **Boundary respected: this decides the contract only — no Go JCS library is selected in
    this phase**, and library choice is deferred to implementation planning and recorded as **U47**. Full
    definition: schema document §4.13.1.
20. **U46 — JSON numeric and parsing policy approved (revision 9).** Accepted JSON integers stay inside
    `[-9007199254740991, 9007199254740991]` (±(2^53 − 1)); anything larger must be sent as a JSON
    **string**. An object with **duplicate keys is rejected**. Decimals follow RFC 8785 / JCS number
    serialization. Anything the accepted JSON/JCS model cannot represent or serialize is rejected,
    explicitly including **non-finite numbers**. Every one of these failures returns **HTTP 400 before an
    idempotency key is claimed** — no `idempotency_records` row, no Event, no Delivery, no Outbox message.
    The two boundaries U41 set still hold: **no raw-body hashing fallback**, and **PostgreSQL `jsonb` is
    never the canonicalization input**. Schema effect: none — `jsonb` stores arbitrary-precision numerics
    and applies last-duplicate-wins silently, so this is an API-boundary rule with no `CHECK` backstop,
    which is also why validation has to read number literals before any `float64` decode (schema §4.7).
    Revision 10 added one clarification, not a change: **integer values are judged mathematically**, so
    `9007199254740992`, `9007199254740992.0` and `9.007199254740992e15` are one rejected value (§5.5,
    schema §4.7 rule 1).
21. **U42 — HTTP response contract approved (revision 10).** First acceptance returns `202 Accepted`. A
    replay (same organization + `Idempotency-Key` + fingerprint) returns the **same status and a
    byte-identical body**, containing `event_id`, `destination_count` and `received_at`, and creates no
    further Event, Delivery or Outbox message. Replays are identified by `HookFlow-Idempotent-Replay: true`,
    absent (or `false`) on initial acceptance. Same key with a different fingerprint is `409 Conflict`;
    invalid JSON/JCS input is `400 Bad Request`. Errors use **RFC 9457 Problem Details** with stable
    machine-readable codes and never expose payloads, request hashes, secrets or internal details — and
    because these documents previously defined no error envelope, **this is the API-wide error convention**
    and the other endpoints must apply it consistently (§5.5).
22. **U43 — expired idempotency record cleanup approved (revision 10).** Retention stays **72 hours**, and
    expired keys become reusable on `expires_at` alone: **correctness never depends on cleanup**, which is
    storage reclamation. The job runs inside the existing `worker` process — no additional binary — with
    batch size **500**, interval **1 minute**, and at most **5 batches per tick**, all kept as configuration
    rather than inline constants. Batching uses a locking CTE with `FOR UPDATE SKIP LOCKED` and
    `DELETE … USING`, committing each batch independently, scanning the existing `idx_idem_expiry`.
    Repeated full-batch runs alert, and a cleanup failure cannot affect event acceptance or delivery
    processing (§7, schema §4.13.4).
23. **U44 — preventing a committed NULL `event_id` approved (revision 10).** The invariant is that **a
    committed idempotency record never has a NULL `event_id`**. V1 enforces it with a deferred PostgreSQL
    **constraint trigger covering both INSERT and UPDATE**, validating the final row state at transaction
    commit, with application-level transaction handling and tests retained as additional safeguards.
    **U1 is not resolved or assumed by this choice** — the trigger was selected precisely because it holds
    whichever way event UUIDs are generated. If U1 later selects application-generated Event UUIDs and the
    Event ID can be inserted into the idempotency record immediately, the schema **may be simplified to
    `event_id NOT NULL`**, potentially dropping the trigger. No trigger or migration is written in this
    documentation-only phase (schema §4.13, §7). **Status note, revision 16:** U1 has since been approved with
    application-side generation, so the condition named above is met — and the simplification is still **not** applied.
    U44's approved guard remains the deferred trigger plus tests, `event_id` stays nullable, and dropping the trigger is
    the owner's separate DDL decision, listed in §13's residual notes.
24. **U45 — no idempotency key history in V1 approved (revision 10).** `idempotency_records` represents the
    **latest** active/recent use of a key; expired records may be deleted or reused; Events, Deliveries and
    Delivery Attempts keep their own independent history. **No idempotency history table is added, and no
    separate `audit_logs` write is made per idempotency conflict** — a conflict returns the defined `409`
    without creating an Event, Delivery or Outbox message, which leaves U4's approved single-transaction
    semantics untouched ("a 409 writes nothing"). Future audit or history requirements are evaluated as a
    separate feature (§14.4, schema §4.13.3).
25. **Go toolchain baseline approved (revision 14) — the toolchain half of U30 only.** The V1 Go baseline is
    **go1.27.4**, chosen on the official release and support evidence recorded in §15.6: newest patch of the
    newest supported line, the only line whose standard library carries `encoding/json/jsontext` (what U47
    path A needs), and nothing approved through revision 13 requires Go 1.24. **U30 is not closed** — the
    `.golangci.yml` linter set, golangci-lint compatibility with this baseline, the complexity budgets, the
    `depguard`/`importas` rules and the GitHub Actions versions remain open in §13, and no `go.mod`, workflow
    or container file existed or was touched by this revision. *(Repository status after M1-E: `go.mod` and
    `compose.yaml` now exist; no workflow file existed at that point. D7 later adds the workflow/lint config
    (§12.19), with compatibility analysis still pending — §16.6.)*
26. **U47 — JCS parser/canonicalizer selection approved (revision 14).** **Path A: the standard-library
    package `encoding/json/jsontext`** at the approved baseline. `github.com/goccy/go-json/jsontext` is
    **not** adopted and no JSON dependency is added to the design; canonicalization stays in the standard
    library unless this item is reopened. The four parser requirements plus the three extra requirements
    travel unchanged (schema §4.13.5), and the HookFlow literal-validation layer stays mandatory because no
    library implements U46. Evidence, stated precisely: the executed comparison in §15.2.2 ran on
    **go1.27.3**, so approving this item does not claim a 1.27.4 run — item 32 is what closes that gap.
27. **U48 — decimal policy approved (revision 14): Option B.** Accepted numbers follow standard RFC 8785 /
    ECMAScript binary64 number semantics; exact-decimal quantities must arrive as JSON strings (U46 rule 2,
    RFC 8785 §3.1's RECOMMENDATION and Appendix D's string-wrapping rule); the significant-digit caps stay
    withdrawn on the evidence in §15.1.1; and the residue left by the double round-trip is documented and
    measured rather than refused. U41's formula and U46's five rules are unchanged and were not
    reinterpreted to reach this outcome.
28. **U48 guard G1 approved; G2 deferred (revision 14).** After the binary64 conversion,
    `math.Abs(f) ≥ 2^53` is rejected as `integer_out_of_safe_range` — no new status code, because the case it
    closes is an unsafe integer **value** arriving through a fractional spelling. **G2 is deferred: V1 neither
    implements nor requires exact decimal-literal preservation**, so `number_not_exactly_representable` is not
    in §5.5 and the two revival conditions in §15.1.1 are the only route back. The integrality procedure that
    decides U46 rule 1 is the corrected revision-13 one and is canonical now: `value = D × 10^p` with
    trailing zeros kept, integral iff `p ≥ 0` or `tz(D) ≥ −p`, range decided from the digit count `len(D) + p`
    with a lexicographic compare only at exactly 16 digits, and no arbitrary-precision allocation on an
    attacker-controlled exponent. §15.3.1's nine worked literals are the reference cases.
29. **Ingestion parse order approved (revision 14).** The eight steps of §15.3 are the normative order for
    `POST /v1/events` and are what §5.1 steps 3–4 now cite: bounded `maxBodySize + 1` read → duplicate-aware
    token parse → envelope assertion → the eight-stage numeric validation → wrapper built by a real JSON
    string encoder over validated raw bytes → one canonicalization → SHA-256 → the approved transaction. The
    order and the boundaries are part of the decision: every rejection is a `400` **before** the key claim, the
    digest is computed **before** `BEGIN`, and the claim-first transaction (U4, U42, U43, U44, U45) is neither
    weakened nor extended. `maxBodySize` is still **U17**'s value and is deliberately not chosen here.
30. **`payload` must be a JSON object (revision 14).** Approved shape: the request document's top level and
    the `payload` value are **both** JSON objects. A `payload` written as an array, a string, a number,
    `true`/`false` or `null` is `400 invalid_json_document` before the claim — §15.2.2 row 14 measured that
    such documents are legal JSON that both candidate paths canonicalize without complaint, so the check is
    the application's and no parser performs it for us. Unrecognised members **inside `payload`** are
    accepted: `payload` is opaque to the platform and no member name is reserved today. If a future API
    contract reserves envelope names, rejecting them becomes an explicit contract change rather than an
    implicit one. This closes the §5.1/§15.5 contradiction and adds no status code and no rule to U46.
31. **`hookflow_ingest_number_inexact_total` approved (revision 14).** A counter incremented when the
    canonical decimal differs from the literal's own decimal value on an **accepted** number (§15.3 stage 8).
    It is an observability instrument, **not** an API validation threshold: no numeric limit is part of the
    contract, any alert is configured in operational alerting (§7) rather than in the request path, and the
    counter is never a response field or a column. The name is fixed; the label set is an implementation
    detail.
32. **Conformance gate approved (revision 14).** Before production use, and enforced **in CI before merge**,
    the pinned implementation must pass: RFC 8785 §3.2.2 and §3.2.4 serialization vectors, the §3.2.3 UTF-16
    ordering vector, Appendix B Table 1, the Appendix I large sample-value file, V8 cross-checks, fuzzing over
    generated documents, the idempotence and fixed-point assertions, and the boundary cases of §15.1.1 and
    §15.3.1. What has actually been executed (revisions 12–13, outside the repository, on go1.27.3) is the
    first set — golden document, ordering vector, Appendix B, duplicates, raw literals, overflow/underflow,
    lone surrogates, escapes, hostile sizes, document-level acceptance — byte-identical across both candidate
    paths. **Still pending: the Appendix I sample file, V8 cross-checks, fuzzing, and any run on go1.27.4.**
    The full suite has not passed and no claim is made that it has; the workflow that will run it is U30's
    open CI half.
33. **UUID generation strategy recorded (revision 15) — U1.** The decision is six parts. **Correction, recorded in
    revision 16: this item presented the six parts as an owner approval, and the owner had not approved U1 at that
    time, so the approval claim belonging to revision 15 is void — item 34 is the approval, effective in revision 16.**
    The six parts themselves stand exactly as written below, and they are what revision 16 approves. (1) **UUIDv7 for
    every entity identifier**, publicly exposed ones included: the
    `event_id` in the `202` body, the delivery id sent as `X-HookFlow-Delivery-Id`, and the id of every other
    row in the 13-table inventory. (2) **Generated in the Go application layer before the `INSERT`** — there is
    **no database-generated UUID default anywhere**, so no `id` column carries a `DEFAULT` and the earlier
    alternatives (`gen_random_uuid()` for v4, native `uuidv7()` on PostgreSQL 18) are not adopted. (3) **The
    same strategy in `api`, `worker` and `outbox-publisher`**, one generator behind one interface rather than
    three local conventions. (4) **The approximate creation-time information is accepted**, with the explicit
    rule that **an identifier is not an authorization credential**: scopes and the `organization_id` tenancy
    invariant decide access (§4.1, §6, §8, schema §3.1), never the id's order or guessability. (5) **Ordering
    and scheduling use timestamp columns** — `received_at`, `created_at`, `next_attempt_at`, `available_at`,
    `expires_at` — and UUID order is never a substitute for one. (6) **No library and no version is selected
    here**; whether the implementation uses the `uuid` package Go 1.27 adds is a dependency-baseline question
    that §13 already records as unchosen and that sits alongside U30's remaining half. Schema shape: **nothing**
    — no column, type, constraint, index or default is added, removed or retyped; `idempotency_records.event_id`
    stays nullable, because U44's deferred trigger is the approved guard and this approval only satisfies the
    condition U44 documented for a *possible later* simplification. U3, U6, U17, U20 and U30's lint/CI half stay
    open, and U1's approval resolves none of them by implication. Two wording corrections were folded into this same
    round rather than opened as a new one, and neither alters the six parts: §10's tests assert v7 validity and
    uniqueness and state explicitly that strict per-process or same-millisecond monotonicity is **not** required —
    that is a property of whichever generator is eventually chosen, which point 6 leaves unselected — and §5.1, §5.3
    and schema §4.10/§8 now name which row carries which identifier, so a retry is a **new** `outbox_messages` row
    stamped at its own `INSERT` while an existing row keeps its id through every re-drive, because a row's own key is
    written at its `INSERT` and never rewritten — the `UPDATE` in §5.1's step d fills
    `idempotency_records.event_id`, a link to another row's key, which is a different thing and is what U44's
    trigger checks.
34. **U1 approved by the owner (revision 16) — application-generated UUIDv7, no database defaults.** This item is the
    approval itself, and it is what makes U1's status **Approved**; it also corrects the record, because revision 15 had
    written the same strategy up as approved before any owner approval existed (item 33 now says so, and §12.15 records
    the correction). The approved decision, authoritative as written: **(1)** every HookFlow entity id is a **UUIDv7
    value generated in the Go application**; **(2)** the rule covers **all entity ids**, the publicly exposed `event_id`
    in the `202` body and the delivery id carried in `X-HookFlow-Delivery-Id` included; **(3)** ids are generated
    **before the `INSERT`**, so the writer always holds the value it is about to store; **(4)** **no database-generated
    UUID defaults** — no `id` column in the 13-table inventory carries or may carry a `DEFAULT`, and an omitted id fails
    on `NOT NULL` rather than being filled in by the database; **(5)** PostgreSQL's native **`uuidv7()` is not used for
    id generation**, declined as an implementation route for U1, which is not a vote on PostgreSQL and does not touch
    U3; **(6)** the **approximate creation-time disclosure** a public UUIDv7 value carries is **accepted**, alongside the
    rule that **a UUID is an identifier, not an authorization control** — authorization and tenant isolation are
    enforced independently by scope checks and the `organization_id` invariant (§4.1, §6, §8, schema §3.1); **(7)**
    **business ordering uses explicit timestamp columns** (`received_at`, `created_at`, `next_attempt_at`,
    `available_at`, `expires_at`) and never UUID order; and **(8)** the **rationale, as the owner recorded it** — an
    application-generated id exists before the `INSERT`, which is what §5.1's single claim→event→fan-out→outbox
    transaction requires; one generator serves `api`, `worker`, `outbox-publisher` and any batch or backfill write, where
    a column default would leave each writer to invent its own convention; and the strategy is **not coupled to a
    PostgreSQL capability, nor to an SQL parser's or catalog's compatibility with one**, so it holds whatever major
    version U3 eventually selects and whatever dependency baseline U30's remaining half settles. The sixth part of the
    revision-15 statement survives this approval unchanged: **no library, import path or version is named here** —
    naming one is a dependency-baseline act and no manifest exists. **Numbering note:** the cross-references in §6,
    §8, §10 and §13 to "U1 point 5" and "U1 point 6" use item 33's six-part numbering and keep their meaning — point 5
    is timestamp-only ordering, point 6 is that no generator is selected; this item's own (1)–(8) numbering is a
    restatement of the same decision and is not a renumbering of those references. Schema shape: **nothing** — 13 tables, 14 agreed
    relationships, ERD 13 blocks / 17 relationship lines / 152 attribute lines, and no column, type, constraint, index,
    default or migration is created or changed. **U3, U6, U8, U17, U20, U25, U30's lint/CI half, U34, U35 and U44 are
    unchanged in status and substance**, and none of them is approved, narrowed or decided by implication — with one
    **wording** exception inside this same revision: U3's register rationale was corrected to drop the obsolete claim
    that the PostgreSQL version drives UUID generation, which chooses no version, recommends nothing, and leaves U3
    **Open** (§12.15). `idempotency_records.event_id` stays
    nullable under U44's approved deferred constraint trigger, and the `NOT NULL` simplification application-generated
    ids make *possible* remains the owner's separate DDL decision in §13's residual notes. **This approval is a decision
    record only; it is not authorization to begin implementation.**

## 12. Changes made in revisions 2 through 16

### 12.1 Revision 2 — alignment with the canonical inventory

| Area | Revision 1 | Revision 2 |
| --- | --- | --- |
| Routing tables | `subscriptions` + `subscription_destinations` | single `endpoint_subscriptions` (endpoint ↔ event_type) |
| Pattern matching | `event_type_patterns text[]` glob list | `event_type text` exact value (wildcard question reopened as U16) |
| Billing | `organizations.plan` | dedicated `organization_subscriptions` with status, period, partial-unique open row |
| Outbox table | `outbox_events`, FK to `events` | `outbox_messages`, composite FK to `deliveries`, no unique on `delivery_id` |
| Fan-out timing | worker created deliveries lazily | created in the accept transaction (§5.1), since only `deliveries` may own outbox rows |
| Event status | `accepted/dispatching/dispatched/failed/discarded` | `accepted/fanning_out/fanout_complete/discarded` to match per-delivery truth in `deliveries` |
| Endpoint secret | `secret_ref` (ambiguous pointer) | `secret_encrypted` reversible ciphertext; envelope/key-version format open (§4.5.1 of the schema doc, U6) |
| API key hash | `secret_hash` only | adds `hash_algorithm` for verifier migration |
| `users` provenance FKs | `created_by_api_key_id` on `api_keys` | dropped; `created_by_user_id`/`revoked_by_user_id` kept as single-column `SET NULL` FKs, with organization membership validated by the application (F3-A/U12) |
| Audit tenant link | `organization_id` nullable | `NOT NULL` per the agreed organizations 1:N audit_logs (U9) |
| Table order | started at `organizations` | agreed inventory order, which is also a valid migration order |
| Decision register | D1–D24 with invented options | U1–U35, with §11 recording what is now closed |
| Line endings | reported as CRLF from an invalid check | verified **LF** for all three files with `od -c` and an awk CR scan. The revision-2 claim that these files were converted from CRLF is **retracted**: it came from `grep -c $'\r'`, which returned exactly the line count for every file — an empty-pattern match, not evidence of carriage returns. Whether the original write was CRLF is unknown; the files are LF now regardless. A `.gitattributes` policy is still recommended (U27) |

### 12.2 Revision 3 — recording the approved decisions

Only the three approved items and their consequences changed. The 12-table inventory, the relationship
set, and everything under U1/U3/U4/U6 were left as they were.

| Area | Revision 2 | Revision 3 | Reason |
| --- | --- | --- | --- |
| Fan-out timing | eager, but recorded as inferred and unresolved (U5) | **approved**: `events` + all `deliveries` + initial `outbox_messages` in one transaction; `202` strictly after `COMMIT` | U5 approval |
| Endpoint ceiling | not present | new service-layer rule: configurable max active endpoints per organization (§4.5.2 of the schema doc) | U5 approval |
| Event-type matching | exact match written, but open as U16 | **approved**: exact match only; the `CHECK` character class is stated to exclude `*`/`%`/prefix syntax | U16 approval |
| Destination snapshot | `events.destinations_snapshot` JSONB **plus** copied columns on `deliveries` | **approved**: `destinations_snapshot` removed from `events`; `deliveries.url`/`http_headers`/`secret_encrypted` are the immutable snapshot payload and `endpoint_subscription_id` a nullable provenance pointer (F3-C) | U33 approval |
| `events.status` | `accepted`/`fanning_out`/`fanout_complete`/`discarded` | reduced to `accepted`/`discarded` — the intermediate states are unreachable once fan-out commits with the event | consequence of U5 |
| `events` indexes | included `idx_events_fanout WHERE status IN ('accepted','fanning_out')` | that index is dropped (nothing would read it); `idx_events_expires` is now non-partial because `status` never transitions after insert | consequence of U5/U33 |
| `events.destination_count` | "written with the snapshot" | re-scoped to a fan-out-completeness counter derivable from `deliveries`; keep-or-drop now open as U36 | consequence of U33 |
| §5 flow text | ended with "whether a deferred variant is acceptable is recorded as U5" | replaced with the approved sequence, an explicit commit-before-response note, and a note that U4/U6 remain unspecified in it | U5 |
| Guarantees table | "Snapshot selected at accept time" | "Per-delivery snapshot columns written at accept time (U33 approved)" | U33 |
| §10 test invariants | 7 invariants | 4 added: fan-out atomicity, no event-level destination columns, exact-match-only routing, ceiling enforcement under concurrency | U5/U16/U33 |
| Decision register | U1–U35 open | U5, U16, U33 closed; U36, U37, U38, U39 opened; register is now U1–U39 | this review |

### 12.3 Revision 4 — recording the second batch of approvals

Inventory unchanged (still 12 tables); one **column** was removed from `events`.

| Area | Revision 3 | Revision 4 | Reason |
| --- | --- | --- | --- |
| `events.status` | `accepted` / `discarded` | **column removed entirely**; no replacement lifecycle, no `discarded` value | U36 |
| `events.destination_count` | keep-or-drop recorded as U36 | **kept**, `integer NOT NULL DEFAULT 0 CHECK (>= 0)`; `0` is a normal accepted outcome | U36 |
| Zero-destination events | expressible only via status | event persisted with `destination_count = 0`, no `deliveries`/`outbox_messages` rows, still `202` | U36 |
| Event progress | split between `events.status` and deliveries | `deliveries.state` is the single processing-state source; progress is an aggregate | U36 |
| `idx_events_expires` rationale | justified by "status is written once" | reworded — no status column exists to predicate on | U36 |
| Endpoint ceiling value | storage location open (U37), no default set | `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG`, default 100, platform config, migration-free to change | U37 |
| Ceiling enforcement | three candidate mechanisms, unresolved (U38) | `SELECT … FOR UPDATE` on the `organizations` row, count + write in the same transaction, protocol mandatory for all writers | U38 |
| `organizations` role | parent key only | documented as the lock anchor for endpoint count mutations | U38 |
| Worst-case fan-out size | "bounded by the ceiling" | concrete: `1 + 100 + 100 = 201` rows per accept at defaults | U37 |
| Accept transaction | owner undecided (U39), listed as an open risk in §4.1 rule 6 | application service + shared Unit of Work; repositories receive the `Tx`; composition root wires; explicit no-split-into-services clause | U39 |
| §4.1 dependency rules | 6 rules | 9 rules (transaction ownership, no per-module commits, composition root, monolith guard) | U39 |
| Directory tree | no slot for a use-case layer | `internal/app/` added as **proposed only**, gated on U40 — not created in this phase | U39 |
| §10 test invariants | ended with "the test that U38 decides" | 6 added: zero-match persistence, no-status assertion, `destination_count` equality with delivery rows, 20-way concurrent create lands exactly on the limit, `paused → active` uses the same protocol, one Unit of Work per use case | U36–U39 |
| Decision register | U1–U39 | U36–U39 closed; U40 opened; 33 rows remain open (U1–U40 minus the 7 approved) | this review |

### 12.4 Revision 5 — U40 approved (application layer location)

No table, column or relationship changed. This revision adds two directories and the dependency rule
that goes with them.

| Area | Revision 4 | Revision 5 | Reason |
| --- | --- | --- | --- |
| Directory tree | `internal/app/` listed as **proposed only**, not created | `internal/app/` and `internal/app/event/` exist, each held by a single `.gitkeep`; the rest of the phase-1 tree is untouched | U40 |
| §4.1 dependency rules | 9 rules, of which rule 8 said the location was unresolved | 10 rules: new rule 4 forbids `internal/modules/*` importing `internal/app/*` and states the arrow `cmd → app → modules → platform/shared`; rule 7 records the approved paths and that no Go source exists yet; rule 10 clarifies the layer is packages inside the same binaries, not a service | U40 |
| Composition root wording | "builds pool, connection, adapters" | explicitly also wires **application services and their dependencies** (rule 1) | U40 |
| §5.1 flow + §7 operations | referred to an unnamed "app service" | name `internal/app/event` as the owner of the Unit of Work and of the ceiling lock-count-write protocol | U40 |
| Lint note | transaction/layering rules relied on review memory | rule 4 says to enforce the module↛app direction with a dependency lint rule, which feeds U30 | U40 |
| Header status line | read "revision 3" — the revision-4 bump had been missed in this file | corrected to revision 5, and a revision-4 paragraph was added so the history is complete | found while making this change |
| Decision register | 33 rows open | 32 rows open (U1–U40 minus the 8 approved: U5, U16, U33, U36, U37, U38, U39, U40) | this review |
| New side effect found | not recorded | `internal/app/event` and `internal/modules/event` would both be `package event`; U28 was widened to cover the alias convention (no path change) | U40 |

### 12.5 Revision 6 — U26 and U28 approved (module path and package naming)

Purely a code-layout revision: no table, column, constraint, index or relationship changed, and no
directory was renamed or created.

| Area | Revision 5 | Revision 6 | Reason |
| --- | --- | --- | --- |
| Header status line | "the module path is intentionally **not** defined in this phase (U26)" | module path decided; `go.mod` still absent | U26 |
| New §4.2 | did not exist | module path, the dir-vs-package table, the three alias cases, and the accepted costs of the dir≠package exception | U26, U28 |
| `internal/app/event` package | unnamed; §4.1 rule 7 flagged a `package event` collision needing per-call-site aliasing | declares **`eventapp`**, so the composition root needs **no** alias for it | U28 |
| `internal/modules/event` package | implied `event` | explicitly **`event`** | U28 |
| `internal/platform/http` | rule 9 said "Naming decision open (U28)" | keeps `package http`; alias `platformhttp` required only in files that also use `net/http` | U28 |
| §4.1 rule 4 | stated the app-direction ban plus the arrow | restated edge by edge: orchestration exclusively in `internal/app`, no module→module internals, no module→app, ports for foreign capabilities, and an explicit note that it adds no new direction | clarification requested |
| §4.1 rule 7 / rule 9 | collision warning + open decision | resolved names, pointing to §4.2 | U28 |
| §5.1 flow line | `app service "AcceptEvent" in internal/app/event` | `eventapp.AcceptEvent (internal/app/event)` | U28 |
| §10 quality note | listed two prerequisites (U30, U28) | U28 removed; U30 remains and now has concrete content (`depguard` + `importas`) | U26, U28 |
| Layout tree + note | `event/` undecorated under both parents | package names annotated; note states all paths live under the module path | U26, U28 |
| Decision register | 32 rows open | **30 rows open** (U1–U40 minus the 10 approved: U5, U16, U26, U28, U33, U36, U37, U38, U39, U40) | this review |

### 12.6 Revision 7 — U4 approved (idempotency), inventory grows to 13

This is the first revision to add a table. No previously approved decision was altered: U5's eager
fan-out, U33's per-delivery snapshot, U36's status removal, U37/U38's ceiling and U39/U40's transaction
ownership all still hold, with the key claim inserted at the head of the same transaction.

| Area | Revision 6 | Revision 7 | Reason |
| --- | --- | --- | --- |
| Table inventory | 12 tables | **13 tables**: `idempotency_records` added | U4 |
| `events` columns | `idempotency_key NOT NULL`, `dedupe_hash` | both **removed** | U4, "do not duplicate idempotency data in events" |
| Key uniqueness location | `uq_events_org_idem` on `events` | `uq_idem_org_key` on `idempotency_records` | U4 requirement 1 |
| Composite-FK target on `events` | required by `deliveries` | reviewed and **reused as-is** — no new unique constraint was needed | U4 review item |
| Retention model | 90 days | 90 days for history **plus an independent 72-hour** key window | U4 requirements 2 and 6 |
| Accept flow | steps a–d (4 writes) | steps a–f (6 writes): claim key, …, link record to event | U4 requirements 3–5 |
| Concurrency story | relied on the event-level unique index | claim-first: `INSERT … ON CONFLICT DO NOTHING`, then `SELECT … FOR UPDATE` branching; losers write nothing | U4 requirement 5 |
| Worst-case rows per accept | `1 + N + N` = 201 | `2 + N + N` = 202 | U4 |
| Guarantees table | one duplicate-ingestion row | four rows (replay, 409, concurrency, lifetime) | U4 |
| New schema section | — | §4.13 with sub-sections .1–.4 in the schema document | U4 |
| Index design note | — | records that a partial index on `expires_at <= now()` is illegal (predicates must be immutable), so `idx_idem_expiry` is a plain b-tree | U4 |
| Tenant-isolation row | 4 composite-FK tables listed | 5, including `idempotency_records` | U4 |
| §10 test list | ended with the single-Unit-of-Work test | 6 idempotency tests added | U4 |
| Decision register | 30 rows open | **34 rows open**: U4 closed, U41–U45 opened as implementation mechanics (11 approvals total) | this review |

### 12.7 Revision 8 — U41 approved (request fingerprint canonicalization)

One column's type and constraints, one new schema subsection, and a flow-ordering change. **No table added
or removed, no relationship changed**, and U4's approved policy is untouched — this supplies the definition
U4 deliberately left open.

| Area | Revision 7 | Revision 8 | Reason |
| --- | --- | --- | --- |
| Fingerprint rule | "the likely answer is a digest over the raw request body plus the event type, but it must be decided" | **RFC 8785 (JCS) → SHA-256** over the canonical UTF-8 bytes of `{"event_type", "payload"}` | U41 |
| `request_hash` type | `text`, "CHK fixed length for the algorithm" (hex-vs-raw unresolved) | **`bytea`** + `chk_idem_hash_len CHECK (octet_length(request_hash) = 32)` | U41, "prefer BYTEA / enforce exactly 32 bytes" |
| `hash_algorithm` | `NOT NULL DEFAULT 'sha256'` | also `chk_idem_hash_alg CHECK (hash_algorithm = 'sha256')` — explicit label, one legal value | U41, "keep `hash_algorithm` as `sha256`" |
| Fingerprint inputs | unspecified | exactly `event_type` + `payload`, wrapped in an object; `Authorization`, `Idempotency-Key`, request timestamps and all other headers excluded | U41, "do not include unrelated transport headers" |
| Invalid JSON | not defined | **HTTP 400 before the claim**, no record, no event; **no raw-byte fallback** | U41, "do not silently fall back to raw-body hashing" |
| §5.1 flow | step 2 folded "Idempotency-Key header REQUIRED; fingerprint body" together | step 3 validates parseability, step 4 computes the digest **before** `BEGIN`; former step 4 became 5 and the response step 6 | the digest must exist before the claim inserts it |
| Schema doc sections | §4.13.1–§4.13.4 | §4.13.1 is the new fingerprint contract; reservation/branching/cleanup/open-details renumbered to .2/.3/.4/.5 | U41 |
| §2 type conventions | no digest rule | `bytea` + exact `octet_length` CHECK for fixed-width digests, algorithm kept in a separate `text` label | U41 |
| Consequences (§8) | 15 items | 17: the digest is computed in Go, never by the database (and `jsonb` is not JCS); pinning `hash_algorithm` keeps stored digests unambiguous | U41 |
| ERD | `text request_hash "… U41 open"` | `bytea request_hash`, note names JCS + SHA-256; `hash_algorithm` note pinned to sha256 | U41 |
| Tests (§10) | fingerprint "once U41 is decided" | 6 named cases: order-invariance, whitespace-invariance, equivalent-number, different-event-type→409, header-only change→replay, malformed→400 with no claim; plus CHECK negatives | U41 |
| Library choice | — | **explicitly not made**: no Go JCS dependency named, added or assumed; selection is recorded as U47 and belongs to implementation planning | U41 boundary |
| New open items | — | **U46** — RFC 8785 serializes numbers through IEEE-754 doubles, so `payload` values beyond 2^53 need an accepted-value policy (consequence of U41, not assumed). **U47** — JCS library selection, deferred by instruction | U41 |
| Decision register | **34 rows open** (11 approvals) | **35 rows open**: U41 closed, U46 and U47 opened (12 approvals total) | this review |

### 12.8 Revision 9 — U46 approved, U42–U45 drafted as proposals

Two different things happened in one pass, and the documents keep them apart deliberately: **U46 is
canonical**, while **U42–U45 are recommendations awaiting owner approval** and changed nothing that is
already agreed.

| Area | Revision 8 | Revision 9 | Reason |
| --- | --- | --- | --- |
| Accepted JSON numbers | "raised as U46 and left open" | integers confined to ±(2^53 − 1); larger magnitudes must be strings; decimals follow RFC 8785 §3.2.2 | **U46 approved** |
| Duplicate object keys | followed the parser's last-duplicate-wins | **rejected** with 400 | U46 requirement 3 — this **revised** the revision-8 wording in schema §4.13.1 |
| Non-finite values | not mentioned | rejected (along with anything the JSON/JCS model cannot represent) | U46 requirement 5 |
| `payload` column note | "size capped by the API before insert (U17)" | same, plus "accepted value domain fixed by U46" and a new §4.7 rules block | U46 — the rules apply to every ingestion, not only fingerprinted ones |
| DDL effect | — | **none.** `jsonb` stores arbitrary-precision numerics and resolves duplicates silently, so no `CHECK` could express U46; the API boundary is the only enforcement point | U46 |
| Fingerprint injectivity | caveated: "integers beyond 2^53 can be re-emitted differently" | unconditional for accepted integers, because U46 refuses them; the decimal case isolated as **U48** | U46 closes U41's caveat |
| §5.1 flow step 3–4 | "not valid JSON / not an object / unrepresentable under JCS" | names each rejected shape: duplicate key, out-of-range integer, non-finite | U46 |
| §6 guarantees | one validation row | that row now enumerates the U46 cases and states `jsonb` cannot enforce them | U46 |
| §10 tests | 6 fingerprint cases | 6 more: duplicate key, `±2^53` boundary on both sides, magnitude-as-string accepted, `NaN`/`Infinity`, deep/array fixtures, and the decode-first regression (`9007199254740993` → 400, not a rounded store) | U46 |
| §14 | did not exist | **new section: Proposed — pending owner approval (U42–U45)**, with a decision summary, recommendations, trade-offs, safeguards, the U1 dependency for U44, and the tests each proposal would add | this review |
| Schema §4.13.6 | did not exist | mirrors §14 as a DDL-effect table only, so the two documents cannot drift on wording | this review |
| Canonical sections | — | **unchanged by the proposals**: §5 flow, §6 guarantees, §7 operations, §8 security, §9 inventory, schema §4.13.1–§4.13.5, and the ERD all still describe approved behaviour only | instruction |
| §7 sweep bullet | "placement, batch size and cadence are open (U43)" | "proposed in §14.2, not approved (U43)" | this review |
| ERD | 13 blocks / 17 relationships / 152 attributes | **identical**; only the header comment block and the `events.payload` note gained a U46 pointer | U46 changes no data model |
| Decision register | **35 rows open** (12 approvals) | **35 rows open**: U46 closed, U48 opened (13 approvals total) | this review |

### 12.9 Revision 10 — U42, U43, U44 and U45 approved (idempotency stack complete)

The four §14 proposals went to the owner and came back approved, so they moved from a proposal block into
the canonical sections. **U46 was re-verified against its checklist and left unchanged**, and U47/U48 stay
open.

| Area | Revision 9 | Revision 10 | Reason |
| --- | --- | --- | --- |
| §5.5 | did not exist | **new canonical section**: `202` + byte-identical body + `HookFlow-Idempotent-Replay` header, RFC 9457 codes table, non-disclosure rule, API-wide convention note | U42 |
| Response body fields | undecided ("response shape open, U42") | `event_id`, `destination_count`, `received_at` — each frozen at accept, which is what makes byte-identity possible | U42 |
| Replay indicator | undecided | header, not a body member; the body-field alternative is recorded as considered and **rejected** (§14.1) | U42 |
| Error convention | none documented | RFC 9457 `application/problem+json` with stable `code`s (`missing_idempotency_key`, `invalid_json_document`, `duplicate_json_key`, `integer_out_of_safe_range`, `non_finite_number`, `payload_too_large`, `idempotency_key_conflict`) | U42 |
| §6 guarantees | 3 idempotency rows | 7: replay identity, rejection-body content, committed-NULL invariant, no-history, cleanup posture added | U42–U45 |
| §3 process topology | `worker` = delivery consumer only | `worker` also hosts the 1-minute idempotency sweep ticker; the "no fourth scheduler process" rule is stated as the reason, and no new package/directory is created in this phase | U43 |
| §7 operations | "placement, batch size, cadence and locking are proposed… not approved" | approved values and names: batch **500**, interval **1 minute**, max **5** batches/tick, three `HOOKFLOW_IDEMPOTENCY_SWEEP_*` config keys, per-batch commit, database clock, full-batch-streak alert, failure isolation | U43 |
| Schema §4.13.4 | `DELETE … WHERE id IN (SELECT … LIMIT $batch)` + "repeat until zero" | **locking CTE + `FOR UPDATE SKIP LOCKED` + `DELETE … USING`**, `RETURNING`, bounded at 5 batches/tick, plus the settings table and three operational properties | U43 (the `WHERE id IN` form is replaced, not extended) |
| §4.13 objects | 6 constraints/indexes | **7**: `trg_idem_linked` (+ `idem_assert_linked()`) — `DEFERRABLE INITIALLY DEFERRED`, `AFTER INSERT OR UPDATE` | U44 |
| `event_id` column | "NULL, composite FK … NULL only transiently" | "NULL in flight, **never NULL in a committed row**" — the nullable column is now paired with the commit-time guard, and the composite-FK-skip-on-NULL gap is closed by it | U44 |
| Migration order | 13 units + 2 protocol notes | **unchanged count and order**: the trigger and function belong inside unit 13 | U44 |
| U1 | open | **still open, untouched** — the trigger was chosen because it holds under either U1 outcome, and the possible later simplification to `NOT NULL` is documented, not assumed | instruction |
| §4.13.3 takeover | "whether past uses must be kept is proposed, not approved (U45)" | latest-wins is canonical: no history table, **and the per-conflict `audit_logs` write is rejected** so "a 409 writes nothing" stays literally true in every transaction | U45 |
| §8 consequences | 19 items | 22: commit-time failure posture, the `202` body as frozen surface, and a scheduled writer inside `worker` | U42–U44 |
| §10 tests | U42–U45 tests excluded from the plan | canonical test list added for all four, including the byte-identity, streak-signal, commit-abort and no-audit-row assertions | U42–U45 |
| §14 | "Proposed — pending owner approval (U42–U45)" | retitled **Decision record — U42–U46 finalized (revision 10)**: rationale and rejected alternatives retained, proposal banner removed | this review |
| Schema §4.13.6 | "Proposed — pending owner approval" DDL-effect table | replaced by **"What the revision-10 approvals changed in this schema"** | this review |
| Data model | 13 tables / 14 relationships | **identical** — no table, column, index or relationship added, removed or retyped; the only new DDL object is a trigger. ERD stays 13 blocks / 17 relationship lines / 152 attributes with comment-line edits only | verification |
| Decision register | **35 rows open** (13 approvals) | **31 rows open**: U42–U45 closed (17 approvals total). U47 re-scoped with four parser requirements; U48 explicitly still open with a "do not stretch U46" note | this review |

### 12.10 Revision 11 — U47 and U48 evaluated, proposals drafted (nothing approved)

An evaluation turn. The owner asked for a U48 recommendation, a verified library evaluation for U47, and a
concrete parse pipeline; all three are written up as **Proposed — Pending Owner Approval**, and the register
counts are deliberately unchanged because no decision was closed.

| Area | Revision 10 | Revision 11 | Status |
| --- | --- | --- | --- |
| §15 | did not exist | **new section**: U48 option analysis, U47 candidate matrix with cited evidence, eight-step ingestion parse order, "what changes if approved", residual questions | **Proposed — Pending Owner Approval** |
| U48 | open, three options named in prose (cap / store canonical / document) | **Option B recommended**: standard JCS number semantics + strings for exact decimals + optional >15-digit observation counter. Accepted/rejected sets, notation→fingerprint equality, the money-as-strings rule and the U46 interaction are each stated explicitly (§15.1) | **Proposed**, U46 unchanged |
| U47 | open, four requirements, "no library named" | still open; a verified 7-row candidate matrix (§15.2), the finding that **no candidate implements U46**, three proposed extra requirements, and an explicit "stdlib-`jsontext`-shaped API is the migration-cheap choice" conclusion | **Proposed**, no dependency named in a manifest |
| §5.1 steps 3–4 | validation and fingerprint listed as rules | cross-referenced to §15.3 as the proposed normative parse order; the rule text itself is untouched | pointer only |
| §5.5 | 7 stable `code`s | **same 7 codes** — no code added, and the JSON-pointer-in-response idea is explicitly *not* proposed because U42 fixes `detail` to static text | unchanged |
| §13 register | 31 rows / 17 approvals | **still 31 rows / 17 approvals** — U47 and U48 remain rows, each now pointing at its §15 proposal, and a revision-11 paragraph states that a recommendation exists without approving anything | unchanged |
| Data model | 13 tables / 14 agreed relationships | **identical**; no table, column, constraint, index or relationship is added or retyped, and B′-observe is a metric rather than a stored field | verification |
| ERD | 13 blocks / 17 relationship lines / 152 attribute lines | **identical**, plus a revision-11 comment block explaining why an evaluation turn changes nothing structural | verification |
| Docs hygiene | — | duplicate `## 5. Fan-out and message lifecycle` heading in the schema document collapsed to one; §4.7's U48 illustration corrected to say the literal canonicalizes to `1`, not `1.0` (the value is unchanged; the example was imprecise) | this review |

### 12.11 Revision 12 — U48/U46 consistency corrected, validation order and wrapper construction specified

A focused correction pass on revision 11, requested by review. **Nothing was approved**, U47 and U48 stay rows in
§13, and the register remains **31 rows / 17 approvals**.

| Area | Revision 11 | Revision 12 | Why |
| --- | --- | --- | --- |
| §15.1 distinctions | 4 rows; "fractional" inferred from notation | 5 rows; **integrality judged on the mathematical value**, with `.`/`e` explicitly non-evidence | the review flagged that `1.7e308` is an integer |
| `1.7e308` | "**accepted**, because U46's bound is on integer values only" | **rejected** — `1.7e308`, `1E30` and `9223372036854775807` are integer values outside ±(2^53 − 1) in exponent notation | correction of a factual error |
| `1e300` in §15.3 | used as an accepted-vs-rejected illustration alongside a wrong claim | kept, with the surrounding sentence rewritten so integrality is derived from the value | consistency |
| U48 accepted set | "fractional values of any magnitude inside the finite double range" | literals whose **value** is either non-integral, or integral within ±(2^53 − 1), plus a finite double | same correction |
| Numeric residue | framed as "more than 15 significant digits" | reframed as **canonical-decimal ≠ literal-decimal**, with the executed `1e-400` case (1 digit, still inexact) proving the digit proxy invalid | evidence |
| U48 guards | B′-observe / cap15 / cap17 variants | **G1** (necessary post-conversion rejection of integral doubles ≥ 2^53, reusing `integer_out_of_safe_range`) and **G2** (optional exact-round-trip acceptance, one new `400` code); cap15/cap17 withdrawn | review point 2: the literal-only rule is insufficient |
| §15.1.1 | did not exist | **new**: executed boundary table (Go 1.27.3 stdlib `encoding/json/jsontext` + `strconv.ParseFloat`) and the sufficiency answer | review point 2 |
| §15.2 | candidate matrix + risks | added **§15.2.1**: the five documented items for the leading candidate, each tagged [doc] or [run], plus the corrected conformance sources | review point 5 |
| RFC 8785 citation | "§16 test vectors" in §13, §14.5 and §15 | **§16 does not exist**; replaced by Appendix B Table 1, §3.2.2's worked example, §3.2.3's ordering vectors and Appendix I's sample-value file, quoted verbatim where used | citation error found while answering point 5 |
| Body-size step | "read under a hard limit … if the extra byte is reachable" | explicit **`maxBodySize + 1` probe with mandatory rejection**, `Content-Length` never authoritative, decompressed-byte rule, and the reason a truncated read is dangerous rather than merely untidy | review point 3 |
| Wrapper step | "`{"event_type":<raw>,"payload":<raw>}`" | `event_type` **must be encoded by a JSON string encoder** (`AppendQuote`/`String`, executed), concatenation forbidden, and `payload` sourced from validated raw JSON — never `jsonb`, never a re-marshalled Go value | review point 4 |
| goccy as leading candidate | asserted from docs | kept, with its `go 1.23` directive, default-unique-names behaviour, raw-byte `ReadValue`, `Canonicalize` = RFC 8785, documented saturation, and the explicit statement that **the backport itself was not executed** | review point 5 |
| Conformance claim | "run §16 vectors later" | no runtime conformance is claimed for any candidate; the required suite is enumerated and the gate is requirement 7 in CI | review point 5 |
| Data model / ERD | 13 tables / 14 relationships; 13 / 17 / 152 | **unchanged** | verification |
| U46, U41, U1, U3, U6 | canonical / held open | **unchanged**; the corrections narrow a proposal, never an approval | instruction |

### 12.12 Revision 13 — G1 only, corrected integrality test, both parser paths executed, U30 baseline proposed

The final evaluation turn requested by review. **Nothing was approved**; U30, U47 and U48 stay rows in §13 and the
register remains **31 rows / 17 approvals**.

| Area | Revision 12 | Revision 13 | Why |
| --- | --- | --- | --- |
| U48 guard set | G1 "necessary", G2 "**recommended closure of the whole class**" | **G1 recommended; G2 deferred and not recommended for V1**, with the necessity/restrictiveness arguments written out (§15.1.1) | review: "my recommended direction is G1 only for V1. Explain why G2 is unnecessary and overly restrictive." |
| G1 test | `f == math.Trunc(f)` **and** `abs(f) ≥ 2^53` | `abs(f) ≥ 2^53` alone, and the equivalence is stated (every binary64 at or above 2^53 is an integer) | simpler, identical verdict set, no truncation call |
| §15.3 stage 3 | integrality iff `e ≥ k − 1` on `±0.d1…dk × 10^e` | **wrong for trailing zeros**: `1.200e1` = integer 12 was classified fractional. Now `D × 10^p` with the trailing-zero count `tz(D)`: integer iff `p ≥ 0` or `tz(D) ≥ −p`; digit count `len(D) + p` | review: "Review the decimal-string normalization algorithm in §15.3. Ensure it handles trailing zeros correctly" |
| §15.3.1 | did not exist | **new**: the nine required literals worked through stages 2–7 with the executed `Canonicalize` outputs, and the three checks kept visibly separate — (a) U46 original-value range, (b) finite/overflow/underflow, (c) post-binary64 G1 | review points 1–2 |
| Conformance evidence | stdlib only; "no candidate dependency was executed" | **both viable paths executed** on the same battery: Go 1.27.3 stdlib `encoding/json/jsontext` and `github.com/goccy/go-json/jsontext` v0.12.0 — §15.2.2 records 14 result groups, identical on every one | review: "Do not claim conformance without executing the required tests against the selected implementation" |
| goccy risk | documented `AppendFloat` non-finite divergence from the stdlib (`+Inf` vs `Infinity`) | **not reproduced**: both packages wrote `-0`, `+Inf`, `-Inf` for those bit patterns, so both diverge from Appendix B in the same three places; the conclusion (never format a non-finite value; reject first) is unchanged but is now measured | executed evidence |
| U47 candidate | goccy = "leading candidate for a Go 1.24–1.26 toolchain" | **path A = stdlib package, recommended, conditional on U30**; path B = goccy backport kept as the documented fallback for an older pin; the two are behaviourally indistinguishable on what was executed (§15.2.2) | review point 3 |
| U30 | "informed by §15.2, not decided" | toolchain half **proposed: Go 1.27.4** (§15.6), with the official release/support data, the check that nothing in the approved design needs Go 1.24, and the CI/Docker consequences recorded without creating any workflow or container file. U30's lint/CI half stays open | review point 2 |
| §15.7 | did not exist | **new**: one consolidated decision table for U30 / U47 / U48 / G1 / G2 / metric / pipeline / conformance gate, every row pending owner approval | review point 4 |
| §15.0, §13, §14.5 | G1+G2 framing, goccy-first wording | updated to the G1-only recommendation and the executed A/B result; row counts unchanged (31) | consistency |
| Data model / ERD | 13 tables / 14 relationships; 13 / 17 / 152 | **unchanged** — G1, the corrected integrality test and the toolchain choice add no column, constraint, index, table or relationship | verification |
| U46, U41, U1, U3, U6 | canonical / held open | **unchanged**; U46's five rules and U41's formula are quoted as before, and G1 only narrows the acceptance boundary | instruction |
| Conformance gate | requirement 7 in CI | **still requirement 7**: the Appendix I large sample-value file, V8 cross-checks and fuzzing were **not** executed here, so no production-conformance claim is made | honesty |

### 12.13 Revision 14 — the approval round: U30 (toolchain half), U47, U48 + G1, the parse order, the payload object rule, the counter and the gate

The owner approved a defined set; this revision records exactly that set and nothing further. Register:
**31 rows / 17 approvals → 31 rows / 19 approvals**, i.e. **29 open rows plus U47 and U48 retained as Approved
records** — the two rows are **kept in §13 and relabelled**, not deleted, because they carry the evaluation history
behind the decisions (correction applied in the revision-14 follow-up after the first draft had dropped them). U30
stays an open row because only its Go-toolchain half was approved. §11 moves from 24 items to **32**. The schema shape
is unchanged for the fourth revision running: **13 tables / 14 agreed relationships**, ERD **13 blocks / 17
relationship lines / 152 attribute lines**, no column, constraint, index, function or trigger added or altered.

| Area | Revision 13 | Revision 14 | Why |
| --- | --- | --- | --- |
| §13 register rows | 31 rows, all **open** (the older convention removed an ID from the table once it was approved) | **31 rows: 29 open + U47 and U48 retained and marked Approved**; U30 stays an open row with only its Go-toolchain half approved; the sentence above the table states the split explicitly | owner instruction — approved historical decision records must not be deleted from the register |
| Status of §15 | "Proposed (pending owner approval)" | retitled: **approved decisions with open remainders**; every label updated per item rather than globally | approval round |
| U30 | toolchain half **proposed**: go1.27.4 | toolchain half **approved** (§11 item 25); linter set, golangci-lint compatibility with this baseline, complexity budgets, `depguard`/`importas`, Actions versions **remain open** and the row stays in §13 | owner approved the toolchain only, and explicitly not the whole row |
| U47 | path A recommended conditional on U30 | **path A approved** — stdlib `encoding/json/jsontext`; the goccy backport is **not** adopted and no JSON dependency is added (§11 item 26) | U30's approval removes the condition |
| Evidence honesty | comparison executed on go1.27.3 | **unchanged and now load-bearing:** the executed run is still go1.27.3, so go1.27.4 conformance is listed as **pending**, not tested | "Do not claim Go 1.27.4 has already been tested" |
| U48 | Option B recommended | **Option B approved** (§11 item 27): RFC 8785 / ECMAScript binary64 semantics, strings for exact decimals, digit caps still withdrawn | approval |
| G1 | recommended, `abs(f) ≥ 2^53` | **approved** (§11 item 28) as stage 7 of the normative order; still no new status code | approval |
| G2 | deferred, not recommended | **stays deferred**: not implemented and not required in V1; `number_not_exactly_representable` is not in §5.5; revival conditions unchanged | approval round declined it |
| Integrality algorithm | corrected to `D × 10^p` + `tz(D)`, executed | **canonical** now (same text, same nine literals in §15.3.1); implementation must match it, and the revision-12 `e ≥ k − 1` form is explicitly not implementable | preserved per instruction |
| `payload` shape | open question in §15.5, while §5.1 already asserted an object at the document top level | **resolved and consistent across all three documents**: request document and `payload` must both be JSON objects; array/string/number/boolean/null → `400 invalid_json_document`; unrecognised `payload` members accepted, none reserved (§11 item 30, §5.1, §5.5, schema §4.7/§4.13.5, ERD `payload` note) | the contradiction had to be closed in one direction |
| Metric | proposed, "part of the recommendation" | **approved by name** as an observability counter; **no numeric threshold in the API contract**, alerting belongs to operations, and no limit was invented (§11 item 31, §7) | approval |
| Parse order | §15.3 proposed | **approved as normative**, cited by §5.1 steps 3–4; order and transaction boundaries preserved exactly (§11 item 29) | approval |
| Conformance gate | requirement 7, partially executed locally | **approved**: vectors + Appendix I + V8 + fuzzing + boundary cases required in CI before merge; §10 now lists executed vs pending side by side, and no suite-wide pass is claimed (§11 item 32) | approval, with the Actions implementation left to U30 |
| U17 | `maxBodySize` stays open | **still open** — no maximum payload size chosen anywhere | instruction |
| U1, U3, U6 | held open by instruction | **still held open**; §15.6's "Not decided here" list and the §13 instruction paragraph are unchanged | instruction |
| Data model / files | 13 / 14; docs only | **unchanged**; no Go source, SQL, migration, `go.mod`, container, workflow or dependency created or modified, and nothing staged or committed | scope restriction |

### 12.14 Revision 15 — U1's six parts recorded: application-generated UUIDv7, no database defaults (**the approval claimed in this round was premature — see §12.15**)

One decision recorded, six parts, no schema shape — and the round's own headline was wrong: revision 15 presented U1 as
owner-approved when the owner had not approved it, so every cell below that says "Approved (revision 15)" or "owner
approval" describes what this round *wrote*, not what happened. The strategy itself is unchanged and was approved in
revision 16 (§11 item 34, §12.15); this table is kept as the record of the documentation deltas. The register keeps
**31 rows** under the retention convention revision
14 established: U1's row is **retained and marked Approved**, not deleted, so the split moves from
**29 open + 2 approved** to **28 open + 3 approved** and the §11 approved-ID count from **19** to **20**. Two wording
corrections from the review of this round are folded into the same revision rather than opened as a new one, because
they change no decision: the monotonicity test expectation is withdrawn (U1 point 6 selects no generator, and no
generator guarantees same-millisecond ordering by virtue of being UUIDv7), and the outbox re-drive sentence names
which row carries which id (U1 point 2 unchanged). Both are recorded in the last three rows below.

| Area | Revision 14 | Revision 15 | Why |
| --- | --- | --- | --- |
| U1 | open — three options (app-side v7, `gen_random_uuid()` v4, native `uuidv7()` needing PG 18) | **recorded as Approved (revision 15)**: application-generated **UUIDv7**, no database defaults, one strategy in all three processes (§11 item 33) — **premature: the owner had not approved it. The same six parts were approved in revision 16 (§11 item 34, §12.15)** | written up as owner approval; corrected one round later |
| §11 | ends at item 32, 19 approved IDs | ends at item **33**, **20** approved IDs | the approval claim is recorded canonically — void as a claim, see §12.15 |
| §13 register | 29 open + U47/U48 approved | **28 open + U1/U47/U48 approved**, still 31 rows | retention convention, not deletion |
| §5.1 flow | `INSERT events (organization_id, type, payload, destination_count)` | `id` named in the column list and annotated as an application-supplied UUIDv7 value; a prose paragraph covers batched inserts, the `NOT NULL` failure mode and timestamp-driven ordering | point 2 has a visible effect on the accept path |
| §6 guarantees | "U1 stays open, and the guard holds under either outcome" | new **Identifier generation** row; the U44 row rewritten; the **Ordering** row now names timestamps explicitly | points 2, 4, 5 |
| §8 posture | secrets and headers only | new bullet **"An identifier is not a credential"** — creation time is approximately public, unguessability comes from the random bits, and no id may act as a capability | point 4 |
| §9 inventory | "PostgreSQL, UUID primary keys" | UUIDv7, generated in the application, **no defaults** | points 1, 2 |
| §10 tests | revision-14 conformance/boundary block | new revision-15 block: no default to lean on, one strategy across three processes, **v7 shape and uniqueness**, ordering never from an id, ids authorize nothing, **no manifest effect** | the tests the approval obliges |
| §10 monotonicity bullet | the first draft of this round asserted "a single process's values increase within a run" as a test | **assertion removed**; the block keeps validity, uniqueness and the shared-interface assertions, and says in terms that strict per-process or same-millisecond monotonicity is *not* required | review point 1. RFC 9562 §5.7 fills the 74 bits after the timestamp with random data by default and makes the sub-millisecond-precision and counter sub-fields an optional *MAY* "to guarantee additional monotonicity within a millisecond" — so monotonicity is a property a generator may provide, and U1 point 6 selects no generator. A test requiring it would constrain the dependency decision rather than check this one. If the chosen generator later documents it, the test can be added as a property of that generator |
| Which row an outbox id belongs to | §5.1's paragraph said "the rows `worker` and `outbox-publisher` create (`delivery_attempts`, retry `outbox_messages`)"; schema consequence 25 said a retry row's id "is stamped when the re-drive happens" | both now name the row: a retry step **inserts a new** `outbox_messages` row (§5.3) and that row's id is generated once, at its own `INSERT`, while a row that already exists **keeps the id it was created with** through publish retries and lease reaping, because nothing in this design rewrites a row's own primary key | review point 2. Clarification of behaviour §5.3 and schema §4.10 already documented — **no new replay design, no schema shape change** |
| ERD | comment block + `events.payload` note | **comment lines only** — 13 entity blocks, 17 relationship lines, 152 attribute lines unchanged | no column, key or edge moves |
| §15.2, §15.6 | "U1 stays open by instruction"; "Not decided here: U1" | status text updated to approved. **The `uuid` package is still not an argument for U47** — that sentence was about parser selection and it stands | consistency without reopening U47 |
| §15.7 | item 32, 29 open | closing paragraph now item **33**, **28 open + 3 retained**; the revision-14 outcome table is left as the record of that round | counts must agree with §11 and §13 |
| Schema doc | `id uuid PK` rows with empty notes; §2 "Primary key" convention | every `id` note now names application-generated UUIDv7; §2 gains the generation convention; §1 carries a new agreed-decision row; §4.13, consequence 24 and §9 record the status change | points 1–3 |
| ERD | comment block + `events.payload` note | **comment lines only** — 13 entity blocks, 17 relationship lines, 152 attribute lines unchanged | no column, key or edge moves |
| U44 / `event_id` | nullable, guarded by the deferred constraint trigger | **unchanged**. U1's approval satisfies the condition U44 documented for a later `NOT NULL` simplification; it does not apply it, and the simplification is listed as a residual item in §13 | preserve an approved decision |
| U3 / U6 / U17 / U20 / U30's other half | open | **open** — none of them is resolved by this approval, and no dependency version, library or PostgreSQL major is named anywhere | instruction |
| Files | docs only | **docs only** — no Go source, SQL, migration, `go.mod`, container, workflow or dependency touched; nothing staged or committed | scope restriction |

### 12.15 Revision 16 — U1 approved by the owner; revision 15's approval record corrected

One owner approval, and one historical correction. Revision 15 wrote U1 up as approved; the owner had not approved it,
and that claim is void. The owner's approval is given in revision 16 and is recorded canonically as §11 item 34, so
U1's status is **Approved** — not open, not rescinded — effective from this round. Nothing in the approved strategy
differs from what revision 15 documented; the strategy is adopted as written, which is why this table moves status and
attribution rather than design. No other decision was approved, narrowed, reopened or created.

| Area | Revision 15 | Revision 16 | Why |
| --- | --- | --- | --- |
| U1 status | labelled **Approved (revision 15)** on a claim that no owner approval existed to support | **Approved (revision 16)**, §11 item 34 | the approval is given now, not then |
| U1 content | six parts: UUIDv7 for every id; application-side before `INSERT`; no database defaults; one strategy across `api`/`worker`/`outbox-publisher`; creation time accepted + an id is not a credential; ordering on timestamps — and no library named | **unchanged**, restated as the owner's eight points, which add nothing and drop nothing: native `uuidv7()` is named explicitly as **not used for id generation**, and the rationale is recorded as its own item | approval of the same decision, documented more precisely |
| Rationale | implicit in the option comparison | recorded: ids exist **before** the `INSERT`; **one generator serves all three processes plus batch and backfill writes**; **no coupling to a PostgreSQL feature or to an SQL parser/catalog** | the owner's stated reasons are now part of the record |
| Revision-15 record | header block, §11 item 33, §12.14 and §13 each asserted an owner approval | each carries a correction pointing here; the revision-15 text and tables stay as history rather than being rewritten | correct the record without erasing it |
| Current-state citations | "(U1 approved, revision 15)" in §5.1, §6, §8, §9, §10, §14.3, §15.x | every **current-state** citation now reads "(U1 approved, revision 16)"; per-revision history rows keep their own round labels | a citation is not a history entry |
| §11 | ends at item 33 | ends at item **34**; still **20** approved IDs, because U1 was already counted — the count changes, the round label does not | counts must agree with §13 |
| §13 register | 31 rows: 28 open + U1/U47/U48 retained | **still 31 rows: 28 open + 3 retained**, U1's row now marked **Approved (revision 16)** | retention convention, not deletion |
| U3 | open, untouched | **open, and independently decidable** — declining `uuidv7()` for ids is not a PostgreSQL vote, and no version is chosen here; the row's own rationale still carried an obsolete `uuidv7()` dependency, corrected in the next row | instruction; scope restriction |
| U3 register rationale | §13's U3 row opened "drives `uuidv7()`, generated columns, partitioning, `MERGE`" | **obsolete dependency removed as a wording fix**: ids are application-generated under U1 and native `uuidv7()` is not used, so the PostgreSQL version neither enables nor is needed for HookFlow id generation, and `uuid` storage accepts those values on any version; generated columns, partitioning and `MERGE` stay legitimate parts of U3's question. **U3 remains Open; no version is selected or recommended** | correction pass inside revision 16 |
| U6 | open | **open** — envelope, key custody, AAD and copy-vs-pointer remain undecided. The historical revision-16 wording said AAD used the **canonical lowercase UUID text**; that proposal is retained as history and is **superseded for current guidance** by the 2026-11-24 reconciliation note: the proposed D6-B AAD uses raw 16-byte UUIDs and a 64-byte layout, and **D6-B is now OWNER APPROVED (2026-11-24) and implemented in the development runtime; production custody is not implemented** | instruction; the `uuid` type and its text form do not depend on the id strategy |
| U8, U17, U20, U25, U30's lint/CI half, U34, U35 | open | **open** — none is resolved by implication, and no new decision ID is created or approved here | instruction |
| U44 / `event_id` | nullable, trigger-guarded | **unchanged**: app-generated ids make the `NOT NULL` simplification *possible*, and it stays the owner's separate DDL decision in the residual notes | preserve an approved decision |
| Schema shape / files | 13 tables; docs only | **unchanged** — 13 tables / 14 agreed relationships / ERD 13 blocks / 17 relationship lines / 152 attribute lines; no DDL, SQL, Go source, `go.mod`, migration, container, workflow or script created or modified; nothing staged, committed or pushed | scope restriction |
| Implementation | not started | **not started** — this approval is not authorization to begin implementation | scope restriction |

### 12.16 Owner approval history — 2026-11-22 (A1–A10, B1–B6)

This section records approvals given after revision 16. It is an approval-history entry, not a new
architectural revision: no decision ID is created or removed, and no settled design is reopened. The
§13 register remains the unmodified Revision 16 register; its statuses are not changed by this section.
U1, U47 and U48 remain as recorded there.

**New approvals — owner authorization of 2026-11-22.** The owner approved the Gate A recommendations and
the fully specified, non-parameterized Gate B contracts in the corrected Owner Approval Record and
Implementation Handoff:

| ID | Approved scope |
| --- | --- |
| A1 | C-2 selected, including the two nullable worker lease fields and paired-null CHECK; no lease duration. |
| A2 | RB-1 generation amendment: explicit initial generation 1, no `dispatch_seq` default, positive `dispatch_seq`/`sequence` constraints. |
| A3 | RB-2 gate/time amendment: all gate-writer formulas, fresh post-lock database time, no `available_at` default. |
| A4 | Completed-attempt DDL: completion-only outcomes and non-null `finished_at`; U18 unchanged. |
| A5 | U2 structural choice: non-partitioned tables and bounded purge jobs; outbox/audit coverage and values remain open. |
| A6 | U3 target: PostgreSQL 18; exact patch and tooling verified before migration execution. |
| A7 | U23: text columns with CHECK constraints. |
| A8 | U6 storage: encrypted-secret snapshot on `deliveries.secret_encrypted`; envelope, KMS custody and rotation remain open. |
| A9 | U9: organization-scoped audit rows only in V1; U20: `audit_logs` uses UUIDv7 under U1. |
| A10 | U12: currently proposed nullable provenance columns/FKs adopted; U14: `endpoint_subscriptions.is_active` retained; U31: endpoints-only soft deletion. |
| B1 | Publisher per-cycle claim accounting (Revision 20 §4.3). |
| B2 | Detailed Recovery Sweep contract (Revision 20 §4.4). |
| B3 | C-2 acquisition, fencing, completion and event-expiry semantics (Revision 20 §4.5). |
| B4 | Fixed-lease approach and enforcement requirements (Revision 20 §4.6); numeric values and operational readiness are not approved. |
| B5 | RB-4 decryption-failure amendment (Revision 20 §4.7). |
| B6 | RabbitMQ 4.3.x minimum compatibility target; the exact patch and verification are not approved. |

**Previously evidenced approvals, recorded without a status change.** U8 (RB-2 scheduling authority and
gate contract), U18 (write-on-completion attempt semantics) and U21 (minimal durable quorum-queue
requirements) were approved in the Revision 17 owner-decision attachment, in its RB-2/U8, U18 and U21
sections. Those approvals predate this entry, are not re-approved by A1–A10/B1–B6, and do not change the
§13 rows: U8, U18 and U21 remain marked Open there because register maintenance has not been authorized.

**Not approved by this entry.** No parameterized Gate B or Gate C item is approved by implication,
including U24, U25, U17, U13, U10/U11/U15/U32, U29/U35, U30's CI half, U27, U34, U22, U2's coverage and
values, U7, U19, dependency pins, C-2 numeric durations, the RabbitMQ exact patch, and optional
residuals.

**Shape after this alignment.** 13 tables; 17 relationship lines, of which only the three U12 provenance
lines are newly approved by A10 and the other 14 keep their prior recorded status; 157 ERD attribute
lines. No migration, source, SQL, manifest, workflow, database operation or commit is authorized by this
entry.

### 12.17 Owner approval history — 2026-11-23 (F3-A, F3-B, F3-C, F8)

This entry records four resolutions of the U12 provenance details and the publisher-token constraint.
It is an approval-history entry, not a revision: no revision number is created, no decision ID is added
or removed, and the §13 statuses remain the unmodified Revision 16 register.

| ID | Approved scope |
| --- | --- |
| F3-A | `users`→`api_keys`: keep the single-column `created_by_user_id` / `revoked_by_user_id` FKs to `users(id)` with `ON DELETE SET NULL`. `users` is global and has no `organization_id`, so no composite same-organization FK applies; creator/revoker organization-membership validation is enforced by the application. No trigger, new table or new membership schema. |
| F3-B | `api_keys`→`events`: add `FOREIGN KEY (organization_id, api_key_id) REFERENCES api_keys (organization_id, id) ON DELETE SET NULL (api_key_id)`, preserving `events.organization_id` when a key is deleted. |
| F3-C | `endpoint_subscriptions`→`deliveries`: add `FOREIGN KEY (organization_id, endpoint_subscription_id) REFERENCES endpoint_subscriptions (organization_id, id) ON DELETE SET NULL (endpoint_subscription_id)`, preserving `deliveries.organization_id` and the sendable snapshot. `url`, `http_headers` and `secret_encrypted` are immutable after acceptance; `endpoint_subscription_id` is a nullable provenance pointer that may be set to NULL when its subscription is deleted, without changing the snapshot payload or cancelling the delivery. |
| F8 | `outbox_messages.lease_token`: add `CONSTRAINT uq_outbox_lease_token UNIQUE (lease_token)`. The lease-shape CHECK remains: the token is populated exactly while `status = 'publishing'` and cleared on every exit from publishing. PostgreSQL's default NULLS DISTINCT behavior is intended. Fresh per-claim token generation remains an application responsibility; the constraint is additional enforcement, not a replacement for fencing semantics. |

These decisions complete the referential details A10 left open; they do not reopen U1, U8, U18, U21,
U22, the publisher fencing design or any other approved decision. The three U12 relationship lines
remain the only newly approved relationship lines; the ERD still has 13 entities, 17 relationship lines
and 157 attributes. No migration, SQL, database operation, source change or commit is authorized by
this entry.

### 12.18 Owner decision — Recovery event expiry takes precedence over an active worker lease (2026-11-23)

This entry records one owner-approved decision given after revision 16. It is an approval-history entry,
not a revision and not a proposal: no decision ID is invented, no existing ID is added or removed, and no
settled design is reopened. The §13 register statuses remain unchanged. The decision is stated in the
dispatch contract in §5.3 and in the schema document's B2/B3 section (§5).

**Title:** *Recovery Event Expiry Takes Precedence Over an Active Worker Lease.*

**Approved decision.** Recovery Sweep may transition a Delivery to `expired` when its Event has expired,
**even if a Worker still holds a valid dispatch lease**. Event expiry takes precedence over the existence
of a valid Worker lease. When Recovery performs this transition:

* the Delivery becomes `expired`;
* the Worker lease tuple is cleared according to the existing implementation;
* no `delivery_attempts` row is created;
* `attempt_count` is not incremented;
* no retry Outbox message is created;
* no new dispatch generation is created.

If the original Worker later calls `Complete` using its stale claim, the operation must be rejected by
fencing: it must not insert a delivery attempt, must not mutate the Delivery, and must not revive or
otherwise change the expired Delivery.

**Important distinction — B3 is not withdrawn.** If Recovery has **not** already transitioned the
Delivery and the Worker presents the matching valid fencing token under the existing completion contract,
the previously approved B3 late-lease completion semantics apply unchanged. The decisive distinction is
whether Recovery has already performed the expiry transition and invalidated the old claim.

**Implementation status.** The decision is implemented by the existing Sweep expiry pass, whose candidate
query intentionally has no live-worker-lease filter, and by the worker's terminal-state fence. A
deterministic integration test seeds a Delivery with a valid Worker lease and an expired Event, runs the
Sweep, asserts the `expired` transition with the lease cleared and no attempt/outbox row, then calls
`Complete` with the still-matching stale claim and asserts the `Fenced` result with no mutation. This is
consistent with B2's expiry-first pass order (expire → repair → re-arm → stale) and with B3's fence; it
adds no lock-order change and no new priority rule. The Worker runtime loop is now implemented and schedules
Recovery; Bundle 1 runtime verification remains pending (§16.6).

### 12.19 Owner approval and Bundle 1 implementation record (2026-11-24)

This entry records the owner-approved Bundle 1 implementation and the authorized D7 CI/documentation
reconciliation. It creates no decision ID and does not rewrite earlier approval history.

* **Approved and implemented:** amqp091-go v1.17.0; RabbitMQ 4.4.1 primary with 4.3.8 as floor/fallback;
  application-managed reconnect; durable topic `hookflow.dispatch` with routing `delivery` to shared quorum
  `hookflow.dispatch.q`; direct `hookflow.dispatch.dlx` with routing `delivery.failed` to quorum
  `hookflow.dispatch.dlq`; policy `hookflow-dispatch-dlq` with at-most-once and delivery-limit 20; strict
  identifiers-only envelopes; bounded N=1 Publisher T1/T2/T3; exact-pointer fixed-lease Worker with pre-HTTP
  guard, real HTTP and retry ladder; generation-owned handles; worker-hosted Recovery scheduling; optional
  Publisher/Worker metrics; and the owner-approved numeric runtime defaults.
* **D7 authorized and implemented:** Go 1.27.4 quality checks, non-mutating tidy diff, pinned PostgreSQL 18 and
  RabbitMQ 4.4.1/4.3.8 service-container integration with down-13/up proof, Ubuntu CGO race, advisory
  golangci-lint v2.15.0, configuration reference and operations runbook. Actions are pinned by verified full
  commit SHA. Branch protection is not changed.
* **Status at Bundle 1 approval (historical; superseded for D6/U25 by Revision 20 and for ingestion by
  Phase 7):** the record below described D6/U25 as not approved. The Owner approved D6-A…D6-G and U25-A…U25-D
  as architecture on 2026-11-24, and the development runtime now implements envelope encryption/decryption,
  the deployment-mode gate and Standard Webhooks v1 signing under a local file KEK; **production** KMS/Vault
  custody and production activation remain **NOT IMPLEMENTED / NOT READY**. Event-ingestion API, production HA
  and SSRF/egress controls also remained unimplemented at Bundle 1 approval; the ingestion API was implemented
  in Phase 7 (§16.10). The original wording is retained in the list item that follows for the record.
* **Still not approved/implemented (as recorded at Bundle 1 approval):** D6/U25 encryption, decryption, KMS and signing; event-ingestion API;
  production HA and SSRF/egress controls; production activation. Nonempty `secret_encrypted` fails closed with
  no HTTP; empty-secret snapshots execute unsigned HTTP unconditionally, and no deployment-mode gate exists
  (D6-F proposed, **NOT APPROVED**).
* **Evidence status:** implementation/code inspection and CI/config validation are not application-test
  evidence. The new workflow, RabbitMQ matrix, migration down/up proof, race and lint runs remain pending.
* **Shape:** still 13 tables and the existing 13 migration units; no schema, ERD, migration or module-manifest
  change is introduced by this reconciliation.

## 13. Unresolved decisions requiring approval

> **Register status notice (updated 2026-11-23).** The statuses in this section are the unmodified Revision 16
> register. Later owner approvals — A1–A10 and B1–B6 plus the previously evidenced U8/U18/U21 approvals
> in §12.16, the F3-A/F3-B/F3-C/F8 decisions in §12.17, the 2026-11-23 Recovery expiry-precedence
> decision in §12.18, and the Bundle 1/D7 record in §12.19 — do not change any row here. No status in this
> table has been updated. Prose elsewhere in this section also records Revision 16 status; §12.16-§12.19 are
> authoritative for what the later approvals settle, without altering a row.

**Approved in the revision-3 review and therefore no longer open:** U5 (eager fan-out, `202` after
commit, configurable per-organization active-endpoint ceiling), U16 (exact-match subscriptions only),
U33 (destination snapshot stored per delivery, never duplicated on `events`).

**Approved in the revision-4 review and therefore no longer open:** U36 (`events.status` removed from
the schema, `events.destination_count` kept as a non-negative integer, every accepted event persisted
including zero-match events with no `deliveries`/`outbox_messages` rows, no `discarded` value, and
`deliveries.state` remains the source of truth for processing state), U37 (the active-endpoint limit is
platform configuration `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG`, default 100, changeable without a
migration; plan-based limits are future work), U38 (`SELECT … FOR UPDATE` on the parent
`organizations` row, with count and write in one transaction, mandatory on every path that can raise
the active count — endpoint creation/activation is therefore serialized per organization), and U39 (an
application service layer with a shared Unit of Work owns the transaction boundary; modules do not
import each other's internals, do not open their own transactions, and the monolith is not split).

**Approved in the revision-5 review and therefore no longer open:** U40 — the application service layer
lives in `internal/app/`, and the event-acceptance use case lives in `internal/app/event/`. Domain
modules must not depend on `internal/app/*`; the composition root in `cmd/` wires application services
and their dependencies; transaction ownership remains with the application use case through the shared
Unit of Work. Both directories were created in revision 5 containing only `.gitkeep` placeholders.

**Approved in the revision-6 review and therefore no longer open:** U26 (Go module path
`github.com/metamin44/hookflow`; the module path is the decision, and `go.mod` was not created in that
revision; it exists now, §16) and U28 (`package eventapp` in
`internal/app/event`, `package event` in `internal/modules/event`, no directory renames, aliasing needed
only for stdlib/dependency collisions — convention in §4.2).

**Approved in the revision-7 review and therefore no longer open:** U4 — idempotency policy, comprising
organization-scoped key uniqueness, a **72-hour** record lifetime, replay of the original Event result on
identical content with no new Event/Delivery/Outbox rows, **HTTP 409 Conflict** on differing content,
exactly one accepted Event under concurrent same-key requests, key reuse once expired, and no deletion of
the Event or its delivery history when a record expires. It added the 13th table `idempotency_records`
and removed `events.idempotency_key`, `uq_events_org_idem` and `events.dedupe_hash`.

**Approved in the revision-8 review and therefore no longer open:** U41 — the request fingerprint is
`SHA-256(UTF-8(RFC 8785 (JCS) of {"event_type", "payload"}))`, which makes member order and insignificant
JSON formatting invisible to replay detection while any change of event type or payload meaning changes the
digest. Transport headers (`Authorization`, `Idempotency-Key`, request timestamps) are excluded; invalid or
unrepresentable JSON is **HTTP 400 before any key claim**, with no fallback to raw-byte hashing;
`idempotency_records.request_hash` is `bytea` under a 32-byte `CHECK` and `hash_algorithm` is pinned to
`'sha256'`. The decision covers the **contract only** — no Go JCS library is selected in this phase, and
U41 had been in the U4 mechanics list since revision 7. It opened U46 (numeric policy inside `payload`,
closed in revision 9) and U47 (JCS library choice, open from here until revision 14 closed it), and did not change
the table inventory or
any relationship.

**Approved in the revision-9 review and therefore no longer open:** U46 — the JSON numeric and parsing
policy. Accepted integer values stay within `[-9007199254740991, 9007199254740991]`; larger magnitudes must
be sent as JSON strings; duplicate object keys are rejected; decimals follow RFC 8785 / JCS number
serialization; values that cannot be represented or serialized under the accepted JSON/JCS model —
including non-finite numbers — are rejected; and invalid input returns **HTTP 400 before an idempotency key
is claimed**, creating no record, Event, Delivery or Outbox message. It explicitly preserved both U41
boundaries: no raw-body hashing fallback, and no canonicalization of PostgreSQL `jsonb`. **It added no
column, constraint, index or table** — `jsonb` cannot express these rules, so they are API-boundary
validation (§11 item 20, schema §4.7). Revision 10 re-verified every clause of it and changed none, adding
only the clarification that the integer bound is judged by **value**, not by notation.

**Approved in the revision-10 review and therefore no longer open:** U42, U43, U44 and U45 — the four
decisions that were proposals in revision 9.

* **U42** — `202 Accepted` for first acceptance, and for a replay with the same organization + key +
  fingerprint the **same status and a byte-identical body** of `event_id`, `destination_count`,
  `received_at`, creating no further Event, Delivery or Outbox message; replays marked by
  `HookFlow-Idempotent-Replay: true`, absent or `false` otherwise; `409 Conflict` on a differing
  fingerprint; `400 Bad Request` on invalid JSON/JCS input; stable machine-readable codes with no payload,
  request hash, secret or internal detail exposed; **RFC 9457 Problem Details adopted as the API-wide error
  convention**. Canonical home: §5.5 (details in §14.1).
* **U43** — 72-hour retention unchanged; expired keys reusable on `expires_at` regardless of cleanup;
  cleanup is storage reclamation and correctness never depends on it; the job is hosted by the existing
  `worker` process with no new binary; **batch 500, interval 1 minute, max 5 batches per tick**; locking CTE
  with `FOR UPDATE SKIP LOCKED` and `DELETE … USING`; each batch commits independently; the existing
  `expires_at` index serves the scan; repeated full-batch runs alert; failures cannot affect acceptance or
  delivery. Values stay configuration, not hardcoded (details in §14.2).
* **U44** — a committed idempotency record **never** has a NULL `event_id`, guarded in V1 by a deferred
  PostgreSQL constraint trigger over INSERT **and** UPDATE that validates final row state at commit, with
  application-level transaction handling and tests retained as additional safeguards. **U1 is neither
  resolved nor assumed by this decision** — that was true as approved, and it is why the guard was chosen;
  **status after revision 16:** U1 has since been approved with application-side generation, so the condition named
  below is met, and **the simplification is still not applied** — U44's deferred trigger and the nullable column are
  unchanged, and dropping the trigger stays the owner's separate DDL decision in §13's residual notes. If U1 later selects application-generated Event UUIDs, the schema may simplify to
  `event_id NOT NULL` and the trigger may be dropped. No trigger or migration is written in this phase
  (details in §14.3).
* **U45** — the table is the latest/recent use of a key, not a history; expired records may be deleted or
  reused; Events, Deliveries and Delivery Attempts retain their own history; **no idempotency history table
  and no per-conflict `audit_logs` write**; a conflict returns `409` without creating an Event, Delivery or
  Outbox message, and the approved transaction semantics stay intact (details in §14.4).

**Approved in the revision-14 review and therefore no longer open** (their rows stay in the table below, marked
**Approved (revision 14)** rather than removed, so the register keeps the whole record): **U47** — the JCS
parser/canonicalizer is
the standard-library package `encoding/json/jsontext` at the approved toolchain baseline, with no third-party
JSON dependency adopted and the goccy backport left as a recorded, unadopted alternative (§11 item 26, §15.2.2);
and **U48** — decimal precision beyond the double round-trip, resolved as **Option B** (standard RFC 8785 /
ECMAScript binary64 semantics, JSON strings for exact decimals) **with guard G1 approved and guard G2
deferred** (§11 items 27–28, §15.1/§15.1.1). The same round also approved five things that were §15 items rather
than register rows: **U30's toolchain half** (go1.27.4 — the row itself stays below because its lint/CI half is
still open), the **§15.3 parse order** as normative, the rule that **`payload` must be a JSON object** with
unrecognised `payload` members accepted, the **`hookflow_ingest_number_inexact_total`** counter, and the
**conformance gate** (§11 items 25, 29–32). None of it reopened U4, U5, U16, U26/U28, U33, U36–U40, U41–U46.

**Approved by the owner in revision 16, and therefore no longer open:** **U1** — UUID generation strategy, approved as
six parts, recorded in §11 item 33 and approved in §11 item 34: UUIDv7 for **every** entity identifier including the
publicly exposed `event_id` and delivery id; generated in the **Go application layer before insertion**, with **no
database-generated UUID defaults anywhere** and **PostgreSQL's native `uuidv7()` not used for id generation**; the
**same strategy in `api`, `worker` and `outbox-publisher`**; the **approximate creation-time
information accepted**, paired with the rule that an **identifier is not an authorization credential** and that
authorization and tenant isolation are enforced independently by scopes and the `organization_id` invariant;
**business ordering and scheduling on timestamp columns**, with id order never used as a business ordering key; and **no library or version selected**,
because dependency selection is a separate open question. The rationale recorded with the approval is that an
application-generated id exists **before** the `INSERT`, behaves identically in all three processes and in batch
writes, and couples id generation to **no PostgreSQL feature and to no SQL parser or catalog capability**. Its register
row is **retained and marked Approved
(revision 16)**, the same convention revision 14 established, so the table still holds **31 rows**. **One historical
correction belongs to this row: revision 15 labelled it Approved before the owner had approved anything, and that
claim was premature — revision 15's blocks and table now say so, and §12.15 is the correction record. U1 is currently
Approved; it is neither open nor rescinded.** The schema shape
of this approval is **nothing**: no `DEFAULT` clause is added or removed, no column type changes, and
`idempotency_records.event_id` stays nullable under U44's approved deferred constraint trigger. The approval also
**does not** touch U3 (and therefore does not adopt native `uuidv7()`, which was one of U1's declined
alternatives — its availability is a PostgreSQL-version matter that an application-side id strategy simply does not
use, so **U3 stays open and independently decidable, with no version implied by U1**), U6 (whose
envelope, key-custody and AAD decisions were unchanged in that historical revision; its canonical-lowercase-UUID-text
AAD statement is retained as history and is superseded for current guidance by the 2026-11-24 reconciliation note —
proposed binary 64-byte AAD, **later OWNER APPROVED as D6-B and implemented in development; production custody not
implemented** (Revision 20)), U8, U17, U20, U25, U30's lint/CI half, U34 or U35. Reading
revision 14's "not approved in this round" list below is therefore a statement about **that** round: U1 was among the
IDs revision 14 declined, revision 15 recorded it as approved too early, and revision 16 is the round in which the
owner approved it.

**Explicitly NOT approved in the revision-14 round, and not to be inferred from the approvals above:** **G2**
(deferred: V1 neither implements nor requires exact decimal-literal preservation, and
`number_not_exactly_representable` stays out of §5.5); any **numeric alert threshold** for the inexactness
counter; the **rest of U30** — linter set, golangci-lint compatibility verification against the go1.27.4
baseline, complexity budgets, `depguard`/`importas` configuration and GitHub Actions versions; the **`Location`
header**, the **72-hour-window-as-configuration** question and RFC 9457 on the subscriber surface (the residual
notes below); and **U1, U3, U6, U17**, which that round left unchanged (U1 was approved in revision 16, after revision
15 recorded it prematurely; U3, U6 and U17 are still open).

**Proposed in the revision-11 review, corrected in revision 12, consolidated in revision 13 and approved in
revision 14:** U47 (parser/canonicalizer selection) and U48 (decimal precision beyond the double round-trip) are
**approved**, and their register rows are **retained and labelled Approved instead of deleted** — the evaluation
history they carry (candidate matrix, executed A/B conformance evidence, the corrected numeric boundary, the recorded
G2 deferral) stays where a reader of §13 will find it. §15 is now a decision record rather than a proposal — each §15
item carries its own status, and the parts that were **not** approved (G2, U30's lint/CI half, the counter's
threshold question, the `Location` header, the 72-hour knob, RFC 9457 on the subscriber surface) are labelled as
open where they appear. Revisions 11–13 changed no canonical rule and approved nothing; revision 14 approved the
set listed above and still names no dependency in any manifest, because no manifest exists.

**Held open by review instruction, and now partially discharged:** **U1 (UUID generation strategy) was in this list
from revision 2 until the owner approved it in revision 16** — the approval is what the instruction asked for, not a
circumvention of it, and the approved strategy is recorded in §11 items 33 and 34. Revision 15 had entered the same
strategy as approved without an owner approval behind it, and that premature record is corrected in §12.15 rather than
repeated. **U3 (PostgreSQL version) and U6 (webhook
secret storage and rotation semantics) stay on the list unchanged**, and nothing in U1's approval narrows them: U3 in
particular is untouched, so the native `uuidv7()` alternative that U1 declined does **not** become a PostgreSQL
decision by implication. U4 was in this list until revision 7 and is now closed. **U44 closed in revision 10 without
touching U1**, and now that U1 has landed application-side, the guard U44 approved is still the guard: `event_id`
stays nullable, and the `NOT NULL` simplification that revision 10 foresaw is listed below as a residual item rather
than applied here.

Nothing in the open rows below is settled by me; each one is a real fork in the design. The table holds **31 rows**:
**28 still open** (U1–U48 minus the 20 approved IDs, with U30 among them because only its Go-toolchain half is
approved — §11 item 25) **plus the three rows approved and kept** — **U1** (revision 16), **U47** and **U48**
(revision 14), each leading its
Notes cell with **Approved (revision …)** so they cannot be misread as pending. Keeping approved rows here is a
convention change made on purpose, by owner instruction, because those IDs carry evaluation history (a candidate
matrix, executed conformance evidence, a corrected numeric boundary and a recorded deferral — and for U1, the three
alternatives it chose between) that belongs in the register rather than only in §15; the pre-revision-14 approvals
(U4, U5, U16, U26/U28, U33, U36–U46) remain recorded
in §11, in the approval paragraphs above and in §12, and are not restated as rows. No other approved ID appears in
this table.

**Residual sub-questions that revision 10 deliberately left unset** (recorded here rather than as new IDs,
so they cannot be lost and cannot be mistaken for approvals):

* Whether the **72-hour window** itself becomes configuration the way `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG`
  is. U43 configured the *job*, not the *policy*; there is no knob today (schema §4.13.4).
* A `Location` header on the `202`. It appeared in the revision-9 draft recommendation and was **not**
  included in the approval, so §5.5 does not promise it.
* Whether RFC 9457 extends to the delivery-side signature/response surface that subscribers see. U42 settled
  the ingestion API; the subscriber contract is U25's scope.

**Residual sub-questions the revision-14 approval round left unset** (same convention — notes, not new IDs, and
not approvals):

* **How** the approved conformance gate runs: merge-blocking job versus scheduled/periodic run, which container
  image, which Actions versions and which fuzzing configuration. The requirement is approved (§11 item 32);
  the mechanism was U30's still-open CI half at revision 14, when no workflow existed. D7 now supplies a
  service-container workflow; the approved ingestion-conformance suite itself remains unimplemented.
* Whether `hookflow_ingest_number_inexact_total` needs labels (for example event type or organization) and what
  its alert threshold is in operational alerting. The counter is approved; a **numeric limit is not**, none was
  invented, and none may be read into §5.5 or the request path.
* Whether a future API contract **reserves** top-level envelope member names. `payload` contents stay opaque and
  unrecognised members are accepted today (§11 item 30); rejecting a reserved name would be an explicit,
  versioned contract change rather than a side effect of the parse order.
* Whether the golangci-lint build available here works with a go1.27.4 baseline — recorded as **unverified**,
  not as a solved prerequisite, because no lint configuration exists to run.

**Residual sub-questions U1's approval left unset** (same convention — notes, not new IDs, and not
approvals):

* **`idempotency_records.event_id NOT NULL`.** U1's approval satisfies the precondition U44 documented for dropping
  the deferred constraint trigger in favour of `event_id uuid NOT NULL`, and schema §4.13 notes that no backfill or
  table rewrite would be needed. **The simplification is not applied by this revision**: U44's trigger is the approved
  guard, changing it is a DDL decision on its own, and it stays open here rather than arriving as a side effect of
  approving U1.
* **`audit_logs` id type (U20).** This table's primary key was raised as UUID vs `bigserial`, and U1's approval
  says nothing about it. If U20 selects `uuid`, the approved U1 rule applies to it unchanged — application-generated
  UUIDv7, no default; if U20 selects `bigserial`, that column is the one documented exception to "every id is a
  UUIDv7 value", and it must be written down as such rather than assumed.
* **Which implementation supplies UUIDv7 values.** U1 approved the strategy, not the package: the `uuid` type and
  its v7 constructor are already in the toolchain the project pinned in revision 14, but naming an import path, a
  module or a version is a dependency-baseline act and belongs with the open items §13 already lists as unchosen
  (alongside U30's remaining half). No manifest exists, so nothing is named.
* **Clock behaviour at generation, and the monotonicity the revision-15 tests do not require.** A v7 value's
  timestamp prefix comes from the system clock, and §10's tests assert v7 validity and uniqueness only: RFC 9562
  §5.7 makes the sub-millisecond-precision and counter sub-fields an optional *MAY*, so same-millisecond ordering is a
  generator's choice rather than what UUIDv7 means, and no generator is selected. Nothing in HookFlow may depend on it
  — §6's ordering row and U1 point 5 forbid reading id order as business order — so the mitigation is that scheduling
  never reads ids, not a claim that clocks or generators are monotonic. If a generator that documents monotonicity is
  later chosen, that belongs to the dependency decision; it is not a reopening of U1. Any future feature that wants
  per-organization sequencing needs a real sequence or a timestamp column, and that would be a new decision rather
  than an extension of U1.
* **Whether the label question the counter already has applies to ids too.** §7 lists `organization_id`,
  `event_id` and `delivery_id` as structured-log fields, and the revision-14 note above leaves
  `hookflow_ingest_number_inexact_total`'s label set open. U1 changes nothing about either: an approved id strategy
  is not an approved metric label set, and no cardinality rule is written down anywhere.

| ID | Decision | Notes | Blocks |
| --- | --- | --- | --- |
| U1 | UUID generation | **Approved (revision 16) — a decision, no longer an open question, effective from the owner's approval in this revision. This row is retained deliberately, under the convention revision 14 established: approving a decision does not delete its register entry. Historical correction, so the row cannot mislead: revision 15 carried this as Approved (revision 15) although no owner approval had been given at that time; that claim was premature, §11 item 33 now records it as such, and §12.15 is the correction record. The approved strategy itself did not change — U1 is Approved, not open and not rescinded.** Approved as six parts — recorded in §11 item 33, approved by the owner in §11 item 34: **UUIDv7 for every entity identifier**, the publicly exposed `event_id` and `X-HookFlow-Delivery-Id` value included; **generated in the Go application layer before the `INSERT`**, so **no database-generated UUID default exists on any of the 13 `id` columns** and an omitted id fails on `NOT NULL` rather than being filled in by PostgreSQL; **the same strategy in `api`, `worker` and `outbox-publisher`**, one generator behind one interface; the **approximate creation-time information is accepted**, with the explicit rule that **an identifier is not an authorization credential** — scopes plus the `organization_id` tenancy invariant (§4.1, §6, §8, schema §3.1) decide access, never the id's order or guessability; **business ordering and scheduling stay on timestamp columns** (`received_at`, `created_at`, `next_attempt_at`, `available_at`, `expires_at`) and UUID order is never a substitute for one; and **no UUID library, import path or version is selected by this decision** (point 6), which leaves dependency selection where §13 and line "Echo, pgx and sqlc are not yet chosen" already put it. **Rationale, as the owner recorded it with the approval:** an application-generated id is available before the `INSERT`; it works identically in `api`, `worker`, `outbox-publisher` and in batch or backfill writes, where a column default would leave each writer to invent its own; and it keeps id generation **uncoupled from any PostgreSQL capability and from any SQL parser or catalog compatibility question**, which is precisely why this row decides nothing about U3 and why U3 stays independently decidable. The alternatives it chose between stay on the record: app-side UUIDv7 (**adopted**), `gen_random_uuid()` producing v4 with a column default (declined — random ids pay a B-tree penalty on the PK and every composite-FK target, and the default would hide generation inside one process while leaving `worker` to invent its own), and native PostgreSQL 18 `uuidv7()` (declined — it would have made U1 decide U3 by implication and put identifier generation in a database feature the tooling had not been verified against). U3 is **not** approved by this row and still carries no version. Related approvals this decision left untouched: **U44** (the deferred constraint trigger stays as the approved guard; `event_id` stays nullable, with the `NOT NULL` simplification recorded as a residual note above rather than applied), **U20** (`audit_logs` PK type, still open), **U2**, **U3**, **U6**, **U17**, **U30**'s lint/CI half. Schema shape of this approval: **nothing** — 13 tables, 14 agreed relationships, ERD 13 blocks / 17 relationship lines / 152 attribute lines, and no migration, `go.mod` or dependency file exists. Two wording corrections were folded into revision 15 before it was recorded, and neither changes the six parts: §10 asserts v7 validity and uniqueness but **not** monotonicity (RFC 9562 §5.7 fills the 74 bits after the timestamp with random data by default and makes the counter and sub-millisecond sub-fields an optional *MAY*, and point 6 selects no generator), and §5.1/§5.3 with schema §4.10 now name which row carries which outbox id — a retry step inserts a **new** row whose id is generated at that `INSERT`, while an existing row keeps the id it was created with, because nothing in this design rewrites a row's own primary key (`idempotency_records.event_id` is a link, not a key) | ~~PK defaults~~ **PK generation site — now fixed as application-side**; the `id` columns stay `uuid` with **no default**, so nothing in `db/migrations` depends on this row any more |
| U2 | Retention mechanism + coverage | batched purge vs monthly partitions; do `outbox_messages`/`audit_logs` share the 90-day window | migrations, purge job |
| U3 | PostgreSQL version target | **Open — no version chosen, and U1 no longer depends on this row.** The earlier rationale led with `uuidv7()`, which is obsolete: identifier generation is **application-side under U1 (approved, revision 16)** and PostgreSQL's native `uuidv7()` is **not used**, so no PostgreSQL version enables, is required for, or has any bearing on how HookFlow produces its ids, and `uuid` columns hold application-generated UUIDv7 values the same way whatever version is chosen. What this decision genuinely governs is the remainder of what the row named — generated columns, partitioning, `MERGE` — plus any later feature choice. Nothing here recommends or selects a version | everything DDL |
| U6 | Secret handling | envelope format, DEK custody (env vs KMS), copy vs pointer on `deliveries`, and the rotation/retirement split: endpoint envelopes may be rewrapped; delivery snapshots are not (A8); retirement is reference-based, not calendar-based (**D6-D is OWNER APPROVED as architecture, 2026-11-24; production retirement tooling is not implemented**) | `endpoints`, `deliveries`, worker |
| U7 | Organization erase | archive-only vs switching `deliveries → endpoints` to CASCADE | retention, admin API |
| U8 | Retry authority | keep both `deliveries.next_attempt_at` and `outbox_messages.available_at`? add `(delivery_id, sequence)` unique? | `deliveries`, `outbox_messages` |
| U9 | Platform audit rows | `organization_id NOT NULL` leaves nowhere for system actions | `audit_logs` |
| U10 | Token model | short-lived access tokens only, since no `refresh_tokens` table exists | auth module |
| U11 | "At least one owner" | deferred trigger vs application transaction | `organization_members` |
| U12 | Provenance FKs beyond the agreed list | `api_keys.created_by_user_id`/`revoked_by_user_id`, `events.api_key_id`, `deliveries.endpoint_subscription_id` | several tables |
| U13 | Destination URL policy | allow private/loopback IPs or enforce SSRF filtering | `endpoints`, worker |
| U14 | `endpoint_subscriptions.is_active` | not in the agreed spec — keep or drop | `endpoint_subscriptions` |
| U15 | Live-endpoint guard | service-layer check vs `BEFORE INSERT` trigger (a FK cannot see `deleted_at`) | `endpoint_subscriptions` |
| U17 | Payload limits & raw storage | size cap; keep raw bytes for exact signature replay | `events`, API |
| U18 | Attempt write count | write `pending` then outcome, or one write at completion | `delivery_attempts`, worker |
| U19 | Subscription quotas/provider fields | add `max_endpoints`/`max_events_per_day`/provider ids to `organization_subscriptions`, and enforce them? | billing, middleware |
| U20 | `audit_logs` PK type | UUID vs `bigserial`; ids are never exposed to clients | `audit_logs` |
| U21 | RabbitMQ topology | exchanges/queues/DLQ naming, per-endpoint fairness vs shared queue, publisher confirms | `messaging` |
| U22 | Outbox relay tuning | batch size, poll interval, `LISTEN/NOTIFY`, lease duration for stale `locked_by` | publisher |
| U23 | Enum representation | `text` + `CHECK` (written here) vs native PG enum types | all status columns |
| U24 | Retry budget | confirm ladder, default 8 attempts, which 4xx are terminal | `deliveries`, worker |
| U25 | Signing scheme | header names, canonical string, secret rotation overlap. **U25-A…U25-D are OWNER APPROVED as architecture (2026-11-24) and implemented in the development Worker (Standard Webhooks v1); signing-key rotation beyond the keyring allowlist is not implemented** | worker, API docs |
| U27 | Line-ending policy | add `.gitattributes` (`* text=auto eol=lf`) to protect a Go repo from CRLF drift | first commit |
| U29 | `updated_at` maintenance | DB trigger vs explicit in every `sqlc` mutation | migrations, queries |
| U30 | Lint/CI configuration | `.golangci.yml` linter set, complexity budgets, Go version, Actions versions, plus `depguard` for the §4.1 rule-4 direction and `importas` for the §4.2 alias convention (`platformhttp`); an exception for the single dir≠package path `internal/app/event`. **Revision 14 approves the Go-version half of this row and nothing else: the baseline is go1.27.4 (§11 item 25; official release/support evidence and the CI/Docker consequences in §15.6; U47 path A depends on it). Still open in this row, and not decided by that approval: the `.golangci.yml` linter set, the complexity budgets, whether the available golangci-lint build is compatible with a go1.27.4 baseline (never verified here), the `depguard`/`importas` configuration, and the Actions versions. No lint configuration, workflow or container file exists, so no part of this half has been executed or tested** | `.github/workflows`, lint config |
| U31 | Soft-delete scope | only `endpoints` agreed — confirm `api_keys`/`endpoint_subscriptions` need none | several tables |
| U32 | Tenant rate limiting | per-organization quotas in the API layer (independent of U19) | middleware |
| U34 | Integration test infrastructure | testcontainers vs compose-managed services | `test/integration` |
| U35 | sqlc layout | one `db/queries/<module>.sql` per module and generated-code destination directory | `db/queries` |
| U47 | JCS parser/canonicalizer implementation choice | **Approved (revision 14) — a decision, no longer an open question. This row is retained deliberately so the register stays the single historical record of every ID the review opened; removal would have erased it.** Selected implementation: the **standard-library package `encoding/json/jsontext`**, at the approved **go1.27.4** toolchain baseline — **no additional JSON dependency** (§11 item 26, §15.0, §15.2.2, §15.7); `github.com/goccy/go-json/jsontext` v0.12.0 remains documented and **not adopted**. Rationale carried forward from the evaluation: the choice was **deferred by the U41 review instruction** to implementation planning and had to be settled before the first Go source file; **four requirements travel with it** (schema §4.13.5) — detect duplicate object keys before the information is lost; preserve enough numeric information from the original text to validate before conversion; never rely on a plain `map[string]interface{}` decode that can silently round large integers; and judge numbers by **mathematical value, not notation**, so `9007199254740992`, `9007199254740992.0` and `9.007199254740992e15` — and `1.7e308`, `1E30`, `9223372036854775807` — are unsafe integer values rejected under U46. Acceptance bar: RFC 8785 §3.2.2/§3.2.4 vectors, the §3.2.3 ordering vector and Appendix B Table 1; a non-conformant canonicalizer silently breaks the replay/409 contract (§14.5). Revisions 11–13 produced the candidate matrix and **executed both viable paths**, byte-identical on every case run (§15.2.2); **no candidate implements U46's numeric rules, so HookFlow's literal layer in §15.3 is mandatory either way**. Evidence limits stay on the record: every executed case used **go1.27.3**, and the Appendix I sample file, V8 cross-checks, fuzzing and any go1.27.4 run are **pending** under the approved merge gate (§11 item 32). Still no module named in any manifest, because no manifest exists | app service, `go.mod` (both future; neither exists) |
| U48 | Decimal precision beyond double round-trip | **Approved (revision 14) — Option B: standard RFC 8785 / ECMAScript binary64 number semantics, plus the requirement that exact-decimal quantities arrive as JSON strings** (§11 item 27). Retained as a record, not as a fork. It exists because U46 does not settle it: an accepted decimal with more significant digits than a double holds is stored by `jsonb` as written but canonicalizes to its rounded form, so two such requests **replay** rather than conflict and the delivered body can differ from what the digest covered. **U46 was not stretched to cover U48 and must not be reinterpreted later** — the boundary holds: integer *values* outside ±(2^53 − 1), in any notation, are U46's and are rejected; fractional precision beyond a double is U48's. Path to the decision: revision 11 proposed Option B, revision 12 corrected integrality to the **mathematical value** and proved guard **G1** necessary (`9007199254740992.5` canonicalizes to an unsafe integer with no library error), revision 13 simplified G1 to `math.Abs(f) ≥ 2^53`, corrected the trailing-zero arithmetic and deferred G2, and **revision 14 approved Option B + G1 while ratifying the deferral of G2** (§11 item 28, §15.1, §15.1.1, §15.3 stages 2–7, §15.3.1, §15.7). Consequences: G2 stays **Deferred — not approved, not implemented and not required in V1**, so `number_not_exactly_representable` is not part of §5.5; the residue is **counted** by the approved `hookflow_ingest_number_inexact_total` (§11 item 31), which carries no numeric threshold in the API contract; the corrected `D × 10^p` integrality procedure is canonical; **U46's five rules and U41's formula are unchanged** | API contract, `payload` validation |

---

## 14. Decision record — U42–U46 finalized (revision 10)

> **All four decisions in this section were approved in revision 10**, and the canonical rules now live in
> §3, §5.1, §5.5, §6, §7, §8, §10, §11 and the schema document. This section keeps what a canonical section
> should not carry: **why each option was chosen, which alternatives were rejected, and what the
> implementation is obliged to preserve.** Reading a rule's justification without its canonical statement is
> not enough — the sections above govern.

### 14.0 Decision summary

| ID | Question | Approved decision | Canonical home | Status |
| --- | --- | --- | --- | --- |
| **U42** | Replay/409/400 wire contract | `202` for acceptance and replay with a **byte-identical body** (`event_id`, `destination_count`, `received_at`) + `HookFlow-Idempotent-Replay: true` on replays; `409`/`400` as RFC 9457 with stable codes and no request content | §5.5, §6, schema §4.13.3 | **Approved (rev 10)** |
| **U43** | Expired-record cleanup job | `worker` ticker, batch **500**, interval **1 minute**, max **5** batches per tick, locking CTE + `FOR UPDATE SKIP LOCKED` + `DELETE … USING`, commit per batch, full-batch streaks alerted, failures isolated, all configurable | §7, schema §4.13.4 | **Approved (rev 10)** |
| **U44** | Preventing a committed NULL `event_id` | deferred **constraint trigger** over INSERT and UPDATE validating final row state at commit, plus application transaction handling and tests. **U1 untouched**; `event_id NOT NULL` is a possible later simplification | schema §4.13, §7 (unit 13), §6 | **Approved (rev 10)** |
| **U45** | Key-reuse history | latest-wins only: **no** history table and **no** per-conflict `audit_logs` row, so a `409` writes nothing and U4's transaction semantics stand | §6, schema §4.13.3 | **Approved (rev 10)** |
| **U46** | JSON numeric and parsing policy | safe integer range by **value**, strings for larger magnitudes, duplicate keys rejected, RFC 8785 decimals, non-finite/non-representable rejected, `400` before any claim, no raw-byte fallback, `jsonb` never canonicalized | schema §4.7, §4.13.1, §5.5 | **Approved (rev 9), verified rev 10** |
| U47 | JCS parser/canonicalizer choice | deferred on instruction; four requirements now fixed (§14.5) | §13, schema §4.13.5 | Open |
| U48 | Decimal precision beyond doubles | deferred; must not be resolved by reinterpreting U46 (§14.5) | §13, schema §4.13.5 | Open |

### 14.1 U42 — HTTP response contract (approved; canonical text in §5.5)

**Status codes.** First successful acceptance stays `202 Accepted` (unchanged, U5: emitted only after
`COMMIT`). A replay returns the *same* `202`, not `200`: the approved requirement is "returns the original
Event result", and returning a different code for a replay would make the status itself a signal that the
first response had to be remembered to interpret. Same key + different fingerprint is `409 Conflict`; body
violating U41/U46 validation is `400 Bad Request`.

**Response body.** The approved body carries `event_id`, `destination_count` and `received_at`:

```json
{ "event_id": "0198…", "destination_count": 3, "received_at": "2026-11-22T09:15:00Z" }
```

Every field must come from an **immutable persisted column** (`events.id`, `.destination_count`,
`.received_at`) — that constraint is what makes replay identity achievable at all, and it is why
`updated_at` and any server-generated timestamp are excluded. A stored response body was the alternative,
and it stays rejected: it would put API shape into the data model and make a future body change unreadable
against old rows. **A `Location` header appeared in the revision-9 draft but was not part of the approval,
so §5.5 does not promise it**; adding one later is a contract change, not a detail.

**Replay indicator — the three options weighed, and the one chosen.**

* **A. Identical body, no indicator.** Satisfies "same status and body" literally, but a client cannot
  distinguish a fresh acceptance from a replay, so retry storms and dedupe effectiveness are unmeasurable
  from the client side and invisible in server metrics. **Not adopted.**
* **B. Identical body + `HookFlow-Idempotent-Replay: true` on replays only. — APPROVED.** The success schema
  never forks, so a client that hashes or caches the body sees one shape forever; headers are not part of
  the U41 fingerprint inputs, so the marker can never influence replay detection; the header also yields a
  replay-vs-fresh counter for free; and clients that ignore it are unaffected. Absent (or `false`) on
  initial acceptance, exactly as approved.
* **C. A `"replayed": true` body member.** **Rejected.** To stay uniform it would have to appear as `false`
  on first acceptance too, which widens the `202` contract U5 already fixed and makes the replay response
  *not* identical to the original — contradicting the requirement it exists to serve.

**Error contract and RFC 9457.** Before this decision these documents defined **no** error envelope anywhere
(the only established rules were §8's: never echo the stored hash or payload, never expose secrets or
internal details), so adopting RFC 9457 *Problem Details* **creates** the API's error convention rather than
joining one — and that is what revision 10 approved, knowingly. It fits the stack: Echo's error handler is
replaceable, and a machine-readable `code` member is exactly what "stable machine-readable error codes"
needs. Two consequences are recorded rather than glossed: the convention applies to **all** endpoints, not
just ingestion, so the other routes implement against §5.5; and `application/problem+json` is a second media
type on a surface that otherwise emits `application/json`. The approved code set is the table in §5.5
(`missing_idempotency_key`, `invalid_idempotency_key`, `invalid_json_document`, `duplicate_json_key`,
`integer_out_of_safe_range`, `non_finite_number`, `payload_too_large` — cap value still pending U17 — and
`idempotency_key_conflict`). `detail` stays a **static** string per code, because interpolating request
content into it is the leak path §8 forbids and static text is what makes codes groupable in logs. A 409
discloses nothing about whether the stored request was larger, older, or from another API key, and the
conflict body **omits the original `event_id`** — if support ergonomics later demand it, that is an explicit
amendment to U42, not an implementation choice.

**Outside U42:** replaying after a record expires is not a replay at all (that path creates a new event, U4
requirement 6); the `409`/`400` semantics themselves came from U4 and U41/U46 and are not re-decided here;
and what HookFlow sends to *subscribers* on a failed delivery is U25's surface, untouched.

### 14.2 U43 — Expired idempotency record cleanup (approved; canonical text in §7 and schema §4.13.4)

**The proposal was assessed as sound, and approved with the adjustments below folded in.** The 72-hour
window is U4 policy and unchanged; key reuse is decided by `expires_at` under a row lock (schema §4.13.3),
so the job is storage reclamation and nothing more; hosting it in `worker` matches §3's approved rule that
**there is no fourth scheduler process** — tickers live inside `worker` and `outbox-publisher` — and
`SKIP LOCKED` is already the house pattern for multi-instance claims (§7, outbox polling), so no new
concurrency idiom arrives with it.

**Adjustments that became part of the approval:**

* **`DELETE` has no `LIMIT` clause in PostgreSQL.** The batched form is a data-modifying CTE that locks then
  deletes — `WITH picked AS (SELECT id FROM idempotency_records WHERE expires_at <= now() ORDER BY
  expires_at LIMIT $batch FOR UPDATE SKIP LOCKED) DELETE FROM idempotency_records r USING picked p WHERE
  r.id = p.id` — and that is now what schema §4.13.4 carries. The earlier `WHERE id IN (SELECT … LIMIT …)`
  shape was **replaced**, not extended: it could not coordinate with a second replica's sweep.
* **Commit per batch, and bound the loop at 5 batches per tick.** "Repeat until zero" is unbounded after an
  outage, so the tick yields after `HOOKFLOW_IDEMPOTENCY_SWEEP_MAX_BATCHES_PER_TICK` (**5** — the draft
  floated 10; the approved value is 5) and the next tick continues. Short lock holding also matters for
  ingestion: a concurrent claim on the same `(organization_id, idempotency_key)` waits on the unique index
  entry, so long-held locks would surface as ingestion latency spikes for exactly the keys being swept.
* **Throughput is not the risk; noticing drift is.** 500 × 5 × 1440 ticks is roughly 3.6M deletions/day, so
  the alert that matters is the **consecutive full-batch streak**, not an absolute row count.
* **A short batch ends the loop, with a caveat worth knowing.** `SKIP LOCKED` silently skips rows another
  sweeper or an in-flight claim holds, so a batch under 500 does not strictly prove the table is drained —
  grinding on would only fight live work for space that is not urgent. Correctness never depends on it.
* **Database clock only.** `expires_at <= now()` is compared by PostgreSQL, never by application time, so a
  skewed replica cannot delete a still-live key early and make a replay turn into a fresh acceptance.
* **Failure isolation is a requirement, not a preference.** A sweep error is logged and counted and never
  reaches the accept path or the delivery loop, and it never shares a transaction with retention or delivery
  work. If every `worker` replica is down, the table grows and nothing else changes.
* **Every replica may run it.** No leader election, because the locking CTE makes concurrent sweeps safe —
  which is also how the "no process may assume another is alive" rule in §3 stays satisfied.

**Not part of U43:** making the **72-hour window itself** configuration the way the endpoint ceiling is. The
job is configurable; the retention policy is not, and no knob exists for it in V1.

### 14.3 U44 — Preventing a committed NULL `event_id` (approved, depends on nothing; U1 was open at approval and is approved application-side in revision 16)

The invariant is: **a committed `idempotency_records` row always has a non-NULL `event_id`.** The NULL
window is a direct consequence of the approved claim-first ordering (U4, schema §4.13.2): the claim is
inserted before the event exists, so the column must be nullable *during* the transaction while never being
nullable *after* it.

| Option | Guarantee | Cost | U1 dependency |
| --- | --- | --- | --- |
| 1. Preallocate the event UUID, write it into the claim row | strongest and cheapest at runtime — the column simply becomes `NOT NULL`, no trigger, and PostgreSQL can no longer skip `fk_idem_org_event` validation (a NULL column disables composite-FK checking, so today an accidental committed NULL also escapes the tenant check) | requires the event id to exist before the claim, which means **application-side generation for `events`**. Adopting it here would decide U1 by side effect, and if U1 later lands database-side for the other tables the codebase ends up with two id strategies | **decides U1** — not available in this pass |
| 2. `DEFERRABLE INITIALLY DEFERRED` constraint trigger on INSERT **and** UPDATE | database-enforced at commit, independent of U1 and of which code path writes the row; a violation aborts the whole accept transaction, so no partial event can survive either | a trigger function that `sqlc` does not generate and a reviewer will not see in a query file; the failure appears at `COMMIT` as a server error rather than at insert; per-row overhead (negligible next to the `2 + N + N` inserts) | none |
| 3. Application enforcement + tests only | zero DDL, zero runtime cost | the invariant becomes a convention: a future second writer (backfill, admin tool, a new use case) can break it silently, and the composite-FK validation gap from option 1 applies in reverse | none |

**Approved: option 2 for V1, with option 3's tests retained as additional safeguards.** It is the only option
that holds *now* without touching U1, and the only one whose guarantee survives a new writer appearing. The
trigger validates the **final row state at transaction commit** and covers both INSERT and UPDATE, because an
expired-record takeover rewrites the row. **U1 is not resolved or assumed anywhere in this decision** — the
nullable column and the deferred guard are exactly what a U1-agnostic enforcement looks like.

The follow-up is recorded as an explicit possibility, not a hedge: **if U1 later selects application-generated
Event UUIDs and the Event ID can be placed into the idempotency record immediately, the schema may be
simplified to `event_id NOT NULL` and the trigger dropped** — a plain `ALTER TABLE … SET NOT NULL` with no
backfill, since the trigger already guarantees no committed NULL exists. That outcome is strictly better than
what V1 ships, and worth saying plainly: option 2 buys the invariant today and buys U1 the time it needs.
Note also that the trigger closes a second hole, since PostgreSQL skips composite-FK validation on a NULL
column — an accidental committed NULL would have escaped `fk_idem_org_event` as well (schema §4.13).

**Status after revision 16:** U1 was approved with application-side UUIDv7 generation, so the precondition this
paragraph named is now met — option 1's mechanics are available, and the id is in hand before the claim row is
written. **The simplification is still not applied.** U44's approved guard is the trigger plus tests, `event_id`
stays nullable in the documented design, and replacing the trigger with `NOT NULL` is recorded as an open item in
§13's residual notes on U1 for the owner to decide on its own merits rather than as something U1's approval
did quietly.

**Nothing is implemented in this phase:** no trigger SQL, no migration file, no `db/migrations/` content. The
DDL fragment in schema §4.13 is illustrative material for the eventual unit 13 file. *(Status note,
revision 17: that described revision 10. Unit 13 now exists in `db/migrations/` with
`idem_assert_linked()` and `trg_idem_linked`, applied and tested on PostgreSQL 18.7; the migrations are the
DDL artifacts of record — §16.)*

### 14.4 U45 — Idempotency key reuse history (approved; canonical text in schema §4.13.3)

**Approved as scoped, and it was already the operating behaviour.** The latest-wins record (schema §4.13.3)
*updates* on takeover, so earlier uses leave no row behind; a history table would have been new scope rather
than a clarification. V1 keeps: one row per live key; expired records may be deleted or reused; and
`events`, `deliveries` and `delivery_attempts` retain their own independent history.

**Operational implications, accepted with the decision.** "Which key produced this event?" is answerable only
inside the 72-hour window (schema §8 consequence 14), and after a takeover the previous fingerprint is gone,
so "did this caller previously send different content under this key?" cannot be answered from
`idempotency_records` at all. Support workflows must treat a key older than 72 hours as unresolvable and fall
back to `events.id`, `audit_logs` and the delivery history.

**The per-conflict `audit_logs` write floated during drafting was REJECTED, and that rejection is part of the
decision.** It would have emitted a row with `action='event.idempotency_conflict'` per 409, and two
properties of the approved design are why it is out: the insert **cannot live in the accept transaction** —
that transaction rolls back on the 409 path, which is precisely what makes the path write nothing — so it
would have needed its own transaction, an exception to U4's semantics, and a rate limiter to stop a client
bug becoming write amplification. V1 instead documents conflicts as **deliberately unaudited**: the response
is the only record, and the observability signal is the metric on 409s, not a stored row. If audit needs
later demand it, that is a separate feature decision (§13 residual notes), not a tuning of this one.

### 14.5 What the implementation is now obliged to satisfy

* **U42**: byte-identity of replay bodies (assert on serialized bytes), header presence rules, one stable
  `code` per rejection reason, `application/problem+json`, and a leak test proving no 409/400 body carries a
  payload, digest, foreign `event_id` or secret. Because the body must stay identical across a replay that
  may arrive 71 hours later, **no field in it may be computed at response time** — only read from columns
  frozen at accept. RFC 9457 applies to the rest of the API surface as well (§5.5 scope note).
* **U43**: `worker`-hosted ticker with the three config keys
  (`HOOKFLOW_IDEMPOTENCY_SWEEP_BATCH_SIZE`, `HOOKFLOW_IDEMPOTENCY_SWEEP_INTERVAL`,
  `HOOKFLOW_IDEMPOTENCY_SWEEP_MAX_BATCHES_PER_TICK`) and **no inline literals**; one transaction per batch;
  database-clock comparison; the consecutive-full-batch streak alert; and tests that acceptance is unaffected
  with the sweeper stopped, that two sweepers never block each other, and that a claim racing a sweep still
  resolves correctly.
* **U44**: the function and constraint trigger inside migration **unit 13** (not a new unit), `OR UPDATE`
  coverage for the takeover path, a test that an unlinked claim fails at `COMMIT` with no residue in any
  table, and a build-order note that revision 16's U1 approval has now made available (drop trigger, `SET NOT NULL`) —
  the switch itself stays the owner's separate DDL decision, listed in §13's residual notes.
* **U45**: no history table, no per-conflict audit row, and a test asserting both — the absence is a
  requirement now, so a future contributor adding the audit write should be stopped by a failing test rather
  than by a review comment.
* **U47 (approved in revision 14 — stdlib `encoding/json/jsontext`, §11 item 26)**: the parser must detect
  duplicate object keys before the information is lost; preserve enough numeric information from the original
  input to validate before conversion; not rely on a plain `map[string]interface{}` decode that can silently
  round large integers; and distinguish numbers by
  **mathematical value rather than textual representation**, so all of `9007199254740992`,
  `9007199254740992.0` and `9.007199254740992e15` are rejected as unsafe integer values under U46 regardless of
  JSON notation. Acceptance evidence: RFC 8785 §3.2.2 serialization examples plus Appendix B Table 1 and the §3.2.3 ordering vectors (the RFC has no section 16; corrected in revision 12, §15.2.1), executed in revision 13 against both candidate implementations with byte-identical results (§15.2.2). **The selection is made: the standard library, with no third-party JSON dependency. The obligation is not discharged — requirement 7's conformance set is now an approved merge gate (§11 item 32), and the Appendix I sample file, V8 cross-checks, fuzzing and any go1.27.4 run are still pending, because what executed ran on go1.27.3.**
* **U48 (approved in revision 14 — Option B + G1, G2 deferred, §11 items 27–28)**: decimal precision and values that cannot safely round-trip through the JCS/IEEE-754
  number model need their own explicit policy decision. **U46 was not expanded to cover U48 and must not be
  weakened to do it** — the split is: integer *values* outside ±(2^53 − 1), in any notation, are decided by
  U46 and rejected; fractional precision beyond a double is U48 and is now decided as Option B — accepted, canonicalized per RFC 8785, counted by `hookflow_ingest_number_inexact_total`, with G2 declined for V1
  (§15.1.1, §15.7).
* **All four, together**: no table, column or index was added, so the canonical inventory stays **13 tables**
  with **14** agreed relationships, and the ERD changed only in comment lines. None of them reopened U4, U5,
  U16, U26/U28, U33, U36, U37/U38, U39/U40, U41 or U46 — and the one new DDL object anywhere in the revision
  (U44's constraint trigger) sits inside the existing `idempotency_records` migration unit.

---

## 15. U47 parser selection, U48 numeric policy and the ingestion parse order (revision 14 approvals and open remainders)

> **Revision 14 approved part of this section; what is left in it is evidence, a decision record, or an explicitly
> open remainder — not a proposal.** Approved and canonical now (§11 items 25–32): **U30's toolchain half**
> (go1.27.4), **U47 path A** (stdlib `encoding/json/jsontext`, no third-party JSON dependency), **U48 Option B**,
> **guard G1**, the **§15.3 parse order**, the **`payload`-must-be-object** rule, the
> **`hookflow_ingest_number_inexact_total`** counter and the **conformance gate**. Not approved: **G2**, which stays
> **deferred** — V1 neither implements nor requires exact decimal-literal preservation, so
> `number_not_exactly_representable` remains outside §5.5 — and **U30's other half** (linter set, golangci-lint
> compatibility with this baseline, complexity budgets, `depguard`/`importas`, Actions versions), which is why U30
> is still a row in §13. Still open by instruction or by omission: **U3, U6, U17** (no maximum payload size was
> chosen), the counter's label set and any alert threshold, how the CI gate is implemented, and the residual notes
> in §13. **U1 was on this list when revision 14 closed and is no longer — it was approved in revision 16** (§11
> item 34; revision 15 recorded it as approved prematurely, and §12.15 holds that correction), by its own decision
> round and not by anything in this section: nothing here decides identifier
> generation, and nothing in U1's approval changes a parser, a numeric rule or the parse order.
>
> **What was actually executed, restated because the approvals lean on it.** No project Go source, SQL, migration,
> dependency, `go.mod`, container or workflow file was created or modified in any revision, and nothing was staged
> or committed. Two throwaway Go programs ran **outside** the repository, in a system temp module that was deleted
> afterwards: revision 12 against the **Go 1.27.3** stdlib, and revision 13 against both the stdlib and
> `github.com/goccy/go-json` v0.12.0 — which means that in revision 13 that module **was downloaded into this
> machine's Go module cache** to be measured, while the repository itself gained no dependency, no manifest entry
> and no file (§15.2.2 says exactly what was executed and what was not). **Both runs used go1.27.3. Approving the
> go1.27.4 baseline and path A therefore makes no conformance claim about go1.27.4: that run is pending, and §11
> item 32 is what requires it before merge.** The canonical text of **U41** (§11 item 19, schema §4.13.1) and
> **U46** (schema §4.7 rules 1–5) is unchanged by these approvals, and nothing here widens, narrows or
> reinterprets U46 in order to answer U48 — the two questions are kept separate throughout.
>
> **Revision 12's correction, stated up front because it was an error and not a refinement, and canonical since
> revision 14:** U46 rule 1 binds the
> **mathematical value** of a number, so `1.7e308`, `1E30` and `9223372036854775807` are integers and are
> **rejected**; revision 11 treated a literal with an exponent as "fractional" and accepted `1.7e308`. The related
> consequence — that a genuinely fractional literal can still land on an unsafe integer after the binary64
> conversion — is why §15.1.1 requires guard **G1**, and why revision 14 approved it. RFC 8785 also has **no §16**;
> the conformance sources
> revisions 10 and 11 cited that way are named properly in §15.2.1.

### 15.0 Approval summary — what revision 14 decided, and what it left open

| Item | Decision or proposal | Status after the revision-14 approval round |
| --- | --- | --- |
| **U48** | **Option B** — standard RFC 8785 (JCS) number semantics, plus a documented requirement that exact-decimal quantities be sent as JSON strings. **Revision 12 correction:** integrality is judged on the **mathematical value**, so `1.7e308`, `1E30` and `9223372036854775807` are unsafe integers and are **rejected**; revision 11 accepted `1.7e308` and was wrong | **Approved (revision 14)** — §11 item 27; U46 unchanged |
| **U48 guard G1** | **Adopted in revision 14, and still the necessary minimum.** After the binary64 conversion, reject when `abs(f) ≥ 2^53` (reported as `integer_out_of_safe_range`), because a fractional literal such as `9007199254740992.5` otherwise puts an unsafe integer into the canonical form and the payload. One test, not two: every binary64 at or above 2^53 **is** an integer, so the `math.Trunc` check revision 12 wrote is redundant — executed evidence in §15.1.1 and §15.3.1 | **Approved (revision 14)** — §11 item 28; it is §15.3 stage 7, and it still adds **no** status code |
| **U48 guard G2** | **Deferred — not adopted for V1 (revision 14 ratified the revision-13 deferral).** Rejecting any literal whose canonical text denotes a different decimal value (`1e-400` → `0`, `1.234567890123456789` → `1.2345678901234567`) closes a residue that breaks **no approved invariant, costs a new `400` code, and rejects output that RFC 8785's own model produces**. §15.1.1 states the four reasons and what would revive it | **Deferred — not adopted for V1 (revision 14)**; §11 item 28 records the two conditions that would revive it, and `number_not_exactly_representable` stays out of §5.5 |
| **U48 metric** | Count **canonical-decimal ≠ literal-decimal** occurrences (`hookflow_ingest_number_inexact_total`, metrics/logs only). Replaces revision 11's ">15 significant digits" proxy, which `1e-400` proves was measuring the wrong thing. With G2 deferred this counter is the whole answer to the residue, so it is part of the approved set rather than an optional extra | **Approved (revision 14) by name** — §11 item 31, §7. An observability counter, **not** an API validation threshold: no numeric limit was invented, and the label set and any alert threshold stay open (§13 residual notes) |
| **U47** | **Path A — stdlib `encoding/json/jsontext` on Go 1.27.** **Path B — `github.com/goccy/go-json/jsontext` v0.12.0** — the recorded alternative, **not adopted** by revision 14. Both were **executed** against the same conformance battery in §15.2.2 and agree on every case; no candidate implements U46, so the HookFlow literal layer in §15.3 is required either way | **Approved (revision 14) — path A** — §11 item 26. Path B is recorded and **not adopted**; no JSON dependency is added, no module is named in any manifest, and the executed comparison ran on go1.27.3, so a go1.27.4 conformance run is still pending (§11 item 32) |
| **U30 (toolchain half)** | **Go 1.27.4** as the V1 baseline: newest stable patch (released 2026-11-10), on a supported line, the only line with the stdlib JSON packages this design wants, and nothing in the approved set requires Go 1.24. Evidence and consequences in §15.6; **U30's lint/CI/Actions half stays open**, and no `go.mod`, workflow or Dockerfile is written in this phase | **Approved (revision 14) — the toolchain half only**: baseline go1.27.4 (§11 item 25). The linter set, golangci-lint compatibility with this baseline, complexity budgets, `depguard`/`importas` configuration and Actions versions **remain open**, so U30 stays a row in §13; still no `go.mod`, workflow or Dockerfile exists |
| **Pipeline** | The eight-step ingestion parse order in §15.3: oversized-body detection, an **eight-stage** numeric validation order whose stage-3/4 decimal test was **corrected in revision 13** to handle trailing zeros (§15.3.1), and a wrapper built through a real JSON string encoder | **Approved (revision 14) as the normative order** — §11 item 29, cited by §5.1 steps 3–4; the order and the transaction boundaries are part of the decision |
| **Payload shape** | `payload` must be a JSON **object**; a top-level `payload` written as an array, string, number, `true`/`false` or `null` is refused, while unrecognised members **inside** `payload` are accepted because no member name is reserved | **Approved (revision 14)** — §11 item 30. Closes the §5.1/§15.5 contradiction in one direction across all three documents, reuses `invalid_json_document`, and adds no status code, no column and no U46 rule |
| Conformance gate | Requirement 7: the §15.2.2 battery plus the Appendix I sample-value file, V8 cross-checks, idempotence and fixed-point assertions must run **in CI against the pinned implementation** before production use. Revision 13 executed a large part of it locally against both paths; it did **not** execute the Appendix I file or any fuzzing, and claims nothing about a version it did not run | **Approved (revision 14)** — §11 item 32: required **before merge**, in CI. Executed versus pending are separated explicitly in §10 and §11, **no claim is made that the suite has passed**, and the Actions implementation itself remains U30's open half |
| Data model | **No change.** No table, column, constraint, index or relationship is added, removed or retyped by any decision in this section; the inventory stays 13 tables / 14 agreed relationships and the ERD stays 13 blocks / 17 lines / 152 attributes | factual, not a decision — re-verified after the revision-14 approvals |

### 15.1 U48: decimal precision beyond the double round-trip

**The distinctions, stated separately, because conflating them is what makes this decision feel vague.** The four
the review brief asked for are rows 1–3 and 5; row 4 was split out in revision 12 because revision 11's error was
exactly a failure to keep "has a decimal point" separate from "has a fractional value".

| # | Distinction | What it actually means | Who decides | Outcome under Option B |
| --- | --- | --- | --- | --- |
| 1 | Finite and valid under JCS | The literal is legal JSON number syntax and its value converts to a **finite** IEEE-754 binary64, so RFC 8785 §3.2.2.3 has a defined serialization for it | **U46** rules 4–5 (approved) | Accepted |
| 2 | The literal's **mathematical value is an integer**, whatever notation it uses | Integrality is a property of the value, **not** of the presence of a `.` or an `e`: `150.0`, `1.5e2`, `1E30`, `1.7e308`, `9223372036854775807` and `9007199254740992.0` are all integers. Only genuinely fractional values — `0.1`, `2e-3`, `1e-27`, `1.234567890123456789` — are not | **U46** rule 1 (approved) | Integral values inside ±(2^53 − 1) accepted (`150.0`, `1.5e2`); integral values outside it rejected in **every** notation (`9007199254740992.0`, `9.007199254740992e15`, **`1.7e308`**, `1E30`) |
| 3 | Not exactly representable in binary64 | The literal names a decimal that no double equals exactly — `0.1`, `19.99`, `0.30000000000000004` | **U46** rule 4 accepts decimals; the precision consequence is **U48** | Accepted under B. RFC 8785 serializes the nearest double; a literal of **≤15 significant digits** always round-trips back to its own digits, so ordinary money and quantity values are unaffected |
| 4 | Canonical text differs from the literal text while the value is the same | `1.0` → `1`, `1E2` → `100`, `1.500` → `1.5`, `-0.0` → `0`, `150` ↔ `1.5e2` | **U41** (approved): JCS is value-based by construction | Accepted, and the fingerprint is **identical** — this is the behaviour U41 was chosen to get |
| 5 | Canonical text differs in **value**, not just notation | `1.234567890123456789` → `1.2345678901234567`; `1e-400` → `0`; `9007199254740992.5` → `9007199254740992` | **U48** — this was the open question; revision 14 approved Option B + G1 and **declined** G2 | Refused only in part: the unsafe-integer case is rejected by G1, the rest is **accepted and counted** by the approved counter (§11 items 27–28, 31) |

**The five questions the decision has to answer explicitly.**

1. **Which decimal values are accepted.** A number literal is accepted when it is legal JSON, its value converts
   to a **finite** double, and — per **U46 rule 1, unchanged** — its **mathematical value is not an integer
   outside ±(2^53 − 1)**. Integrality is judged on the value: `150.0` and `1.5e2` are 150 and accepted, while
   `1.7e308`, `1E30` and `9223372036854775807` are integers far outside the safe range and are **rejected** even
   though they carry a decimal point or an exponent. Genuinely fractional values are not magnitude-bounded:
   `2e-3` and `1e-27` are accepted, and under plain B so are decimals with 16, 17 or 40 significant digits.
2. **Which values are rejected.** The approved U46 set, in this precedence: an integer **value** outside
   ±(2^53 − 1) in any notation ⇒ `integer_out_of_safe_range` (decided on the literal, so `1e400` reports this
   code and not `non_finite_number`, because its value is an integer and integrality is tested first); a
   **non-integral** literal whose value overflows the finite double range ⇒ `non_finite_number`; a document with
   a duplicate member ⇒ `duplicate_json_key`; a non-object top level ⇒ `invalid_json_document`; an over-cap body
   ⇒ `payload_too_large`. To that, revision 12 adds one **necessary** rejection (guard **G1** in §15.1.1): a
   literal whose double lands on an integer outside the safe range.
3. **Do decimal and exponent notation of the same value produce the same fingerprint? Yes — by design, and this
   is already approved.** `{"a":1}`, `{"a":1.0}`, `{"a":1E0}` and `{"a":0.1e1}` all canonicalize to
   `{"a":1}`, so they share one digest and **replay**; `{"a":150}`, `{"a":1.5e2}` and `{"a":1500e-1}` likewise all
   collapse to `{"a":150}`. Any policy that made notation visible to the digest would contradict U41's approved
   rationale and U4's "same content replays" rule.
4. **Must clients use strings for money and other exact decimals? Yes — required by contract, already approved
   in substance.** U46 rule 2 says big ids, precision-preserving money amounts and nanosecond epoch counters
   belong in strings, and RFC 8785 says the same thing independently: §3.1 recommends that numbers needing
   higher precision "or longer integers" be represented as JSON strings, and Appendix D states that numbers
   without a natural place in the JSON number model **MUST** be wrapped in a string — its own examples being a
   giant exponent, `26000.33` as monetary data, and `int64Max`. U48-B supplies the *reason* for the API docs:
   the digest canonicalizes through a double while `payload` is `jsonb`, which stores the decimal as written, so
   a value the double cannot hold exactly can reach a subscriber differently from the text the digest covered.
   The API **cannot detect** that a field is money, so this stays a documented producer rule plus the observability
   counter in §15.1.1 — not a type system.
5. **How U46's safe-integer restriction interacts with decimal and exponent notation.** Rule 1 binds the number's
   **mathematical value**, so notation is irrelevant in both directions: an out-of-range integer value is rejected
   however it is written (`9007199254740992`, `9007199254740992.0`, `9.007199254740992e15`, **`1.7e308`**,
   `1E30`, `9223372036854775807`), and a value inside the range is accepted however it is written (`150`, `150.0`,
   `1.5e2`). What rule 1 does **not** do is bound the magnitude of a value that is genuinely fractional (`2e-3`,
   `1e-27`), and it imposes no digit-count limit — that gap is U48's, and revision 11's claim that `1.7e308` was
   acceptable was wrong precisely because it read notation as integrality. Conversely U48 must never be used to
   *relax* rule 1 for `.0`/exponent spellings of an out-of-range integer value.

#### 15.1.1 Boundary cases, and why the literal-level rule alone is not sufficient

Revision 12 replaced reasoning with measurement for this question, and revision 13 repeated every measurement
against **both** viable parser paths: Go 1.27.3's stdlib `encoding/json/jsontext` and
`github.com/goccy/go-json/jsontext` v0.12.0, using `(*Value).Canonicalize` plus `strconv.ParseFloat`, outside the
repository in a throwaway module — no project file, dependency or `go.mod` was created or changed, and the module
was deleted afterwards. **Every `Canonicalize` result in this table was byte-identical between the two paths** (the
comparison itself is §15.2.2).

| Literal | Literal's mathematical value | `ParseFloat` | `Canonicalize` result | Verdict |
| --- | --- | --- | --- | --- |
| `150.0`, `1.5e2` | integer 150 | 150 | `150` | accepted (safe integer value); notation differences are invisible to the digest, as U41 requires |
| `9007199254740992.0`, `9.007199254740992e15` | integer 2^53 | 9.007199254740992e+15 | `9007199254740992` | rejected by U46 rule 1 (integral, unsafe) |
| **`1.7e308`** | **integer** 1.7 × 10^308 | 1.7e+308, no error | `1.7e+308` | **rejected** — correction to revision 11, which wrongly accepted it. `1E30` → `1e+30` and `9223372036854775807` → `9223372036854776000` are the same class |
| `0.1`, `2e-3`, `1e-27` | fractional | exact shortest repr | `0.1`, `0.002`, `1e-27` | accepted; canonical text equals the literal's value |
| `9007199254740992.5` | fractional (2^53 + ½) | 9.007199254740992e+15, **no error** | `9007199254740992` | **slips past a literal-only rule**: the literal is not an integer, yet the double is an unsafe integer |
| `9007199254740993.5` | fractional | 9.007199254740994e+15 | `9007199254740994` | same class; RFC 8785 Appendix B note (4) documents this round-to-even behaviour in the spec itself |
| `100000000000000000000000000.5` | fractional | 1e+26 | `1e+26` | same class, and the fraction is invisible to any double-based check |
| `1e400`, `-1e400` | integer 10^400 | ±Inf, `ErrRange` | **`1.7976931348623157e+308`, `err=<nil>`** | rejection is mandatory and must happen **before** canonicalization: saturation is silent, and RFC 8785 note (3) says out-of-range values are not permitted in JSON |
| `1e-400` | fractional | **0, `err=nil`** (no underflow error at all) | `0` | survives U46 rule 1 (not an integer value) **and** G1 (zero is safe), so it is accepted and counted: a one-significant-digit literal whose double is a *different value* — the digit-count proxy misses it entirely, and this is the residue revision 13 chooses to measure rather than reject |
| `1.234567890123456789` | fractional | 1.2345678901234567 | `1.2345678901234567` | the canonical text denotes a different decimal value than the literal; accepted under G1, counted by the metric, and the reason U46 rule 2 tells producers to use strings |
| `123456789012345678901234567890.5` | fractional (31 digits, `.5`) | 1.2345678901234568e+29, no error | `1.2345678901234568e+29` | **rejected by G1** — a new revision-13 measurement: the literal is genuinely fractional, so stage 4 of §15.3 cannot see the problem, and the double is an unsafe integer |
| `-0.0` | integer zero | -0 | `0` | accepted; RFC 8785 Appendix B lists `8000000000000000` → `0` ("Minus zero"), and `AppendFloat(-0.0)` returns `-0` — the canonicalize path must be used, not float formatting |
| `9007199254740991` | integer 2^53 − 1 | itself | `9007199254740991` | accepted; RFC 8785 Appendix B note (1) states the same SHOULD-range, so U46's bound is the spec's own recommendation |

**Conclusion, stated as the answer to the question asked: the mathematical-value rule on the literal alone is
not sufficient; a post-conversion guard is necessary.** A fractional literal can land on an integer double
outside the safe range (`9007199254740992.5`, `123456789012345678901234567890.5`), and no inspection of the
literal's syntax or decimal expansion can see it, because the collapse happens in the binary64 conversion that
RFC 8785 mandates. Two candidate guards follow. **Revision 14 approves G1 and ratifies the deferral of G2**
(§11 item 28), and **neither edits U46's canonical wording**: each only narrows what the
boundary accepts, so rule 1 keeps meaning "integer values, in any notation, inside ±(2^53 − 1)".

* **G1 — approved in revision 14 (§11 item 28), and the necessary minimum.** After converting to float64, reject when
  `math.Abs(f) ≥ 2^53`. One comparison, allocation-free. Revision 12 wrote this as "the double is integral **and**
  `abs(f) ≥ 2^53`"; the first clause is redundant, because every binary64 value at or above 2^53 has an exponent
  large enough that its ulp is ≥ 1, so it is necessarily an integer — which also means no fractional literal can
  escape the guard by rounding upward. Measured: `9007199254740992.5` → `9.007199254740992e+15` → rejected;
  `123456789012345678901234567890.5` → `1.2345678901234568e+29` → rejected; `1.2001e1` → `12.001` → accepted. Code:
  `integer_out_of_safe_range`, so **no new status code is needed**, and it cannot weaken U46 — it only refuses
  what U46 already refuses, reached through a fractional spelling.
* **G2 — deferred, not adopted (revision 14 ratified that deferral; §11 item 28).** The rule would additionally reject any literal whose canonical decimal
  differs from the literal's own decimal value (`1e-400` → `0`, `1.234567890123456789` → `1.2345678901234567`),
  which does produce the tidy invariant "for every accepted number the digest covers exactly the decimal value the
  `jsonb` payload stores", notation may differ but value may not. Revision 12 called that the recommended closure of
  the whole class. Revision 13 withdrew that recommendation, for four reasons that revision 14 accepted:
  1. **Nothing approved requires it.** U46 bounds *integer* safety, not decimal exactness; U41 defines the digest as
     JCS-of-the-document, and JCS is a value-based canonical form by construction — the point of choosing it was
     that `150` and `1.5e2` share a digest. A 17-digit decimal that rounds inside the safe-integer band violates no
     stated rule; it is only a surprise to a client that expected the platform to preserve its digits.
  2. **The data loss it prevents does not happen.** `payload` is `jsonb`, which stores the decimal as written, so a
     subscriber still receives `1.234567890123456789`; what the digest covered is the rounded form. G2 therefore
     does not protect the delivered body — it polices the *fingerprint's* fidelity for a case the platform has
     already told producers to send as a string (U46 rule 2, and RFC 8785 §3.1's RECOMMENDATION plus Appendix D's
     "**MUST** be wrapped using the JSON string type", whose own examples include `26000.33` as monetary data).
  3. **It is the more restrictive of the two on legal input.** G1 rejects only values U46 already rejects; G2
     rejects well-formed JSON numbers that every compliant JCS canonicalizer in the world accepts and canonicalizes
     identically, including underflowed values such as `1e-400` and any producer that emits more digits than the
     shortest round-trip form (`0.1000000000000000000000000001`). A `400` is irreversible for the client, and this
     one would be issued by a platform whose documented contract is "IEEE-754 double semantics".
  4. **It buys the guarantee with new machinery and a new permanent contract line.** The comparator must be bounded
     (decimal-string comparison, no arbitrary-precision growth on attacker exponents — the same DoS class §15.3
     stage 4 exists to avoid), and `number_not_exactly_representable` would enter the §5.5 table for the life of the
     API. The inexactness counter gives the same information as an observable instead of a rejection, which keeps
     the decision reversible: if the counter shows real producers being coarsened, G2 becomes an evidence-backed,
     versioned API change rather than a guess made during design.
  **What would revive G2:** a business requirement that the fingerprint be an exact commitment to the stored decimal
  (for example, if a signature or a dispute process ever relies on digest equality as proof of the client's literal
  digits), or metric data showing non-negligible inexact traffic from first-party producers. Both are outside V1,
  and revision 14 recorded **neither** condition as met — G2 was declined, not forgotten.
* **Observability — approved with G1 in revision 14 (§11 item 31).** Count the *inexactness* condition (canonical decimal ≠ literal
  decimal) rather than raw significant-digit counts — the `1e-400` row above shows a 1-digit literal can be
  inexact, so the revision-11 digit-count proxy was the wrong instrument. Metric name
  `hookflow_ingest_number_inexact_total`, logs/metrics only, never a response field or a column. With G2 deferred
  this is the only mechanism touching the residue, so revision 14 approved it as part of the set — as a
  **counter**, with no numeric threshold in the API contract and any alert left to operational configuration (§7).

**Option A — strict exactness (reject when the canonical text differs from the literal).** Rejected.
The usual objection to A is mis-stated, so to be precise: A does **not** reject `0.1`, because the shortest
round-trip representation of the double nearest `0.1` *is* `"0.1"`. What A rejects is `1.0`, `1E2`, `1.500`,
`-0.0` — i.e. every harmless re-spacing of a value. That makes replay detection depend on the client's
serializer rather than on the content: a JavaScript client sending `1` and a Python client sending `1.0`
(`json.dumps(1.0)`) would disagree about whether the same event was already accepted, which is a direct
contradiction of U41's approved purpose and of U4's replay rule, and it would produce `400`s and spurious
`409`s for semantically identical documents. It also costs more than B: A must canonicalize *and* text-compare
every number token. No variant of A is recommended.

**Option B — standard JCS semantics + strings for exact decimals.** **Approved in revision 14 (§11 item 27)**,
and it does **not** conflict
with RFC 8785 or with any approved requirement:

* RFC 8785 defines how the **value** is serialized; it says nothing about how many digits an *accepting* party
  must tolerate in its input, so accepting everything finite is a compliant reading, not a relaxation.
* U41 is already "RFC 8785 followed by SHA-256"; B is that sentence applied to numbers, not a change to it.
* U46 rule 4 explicitly says decimals are accepted and canonicalized by §3.2.2, and rule 2 already pushes exact
  decimals into strings. B is the policy those two rules were written to be consistent with.
* The owner's stated preference is therefore upheld in substance, with one addition revision 12 forced:
  "strings for values requiring exact high-precision decimal preservation" presumes the boundary can tell an
  exact value from a collapsing one. It can, but only by **testing the round-trip** (§15.1.1), and at least the
  `|f| ≥ 2^53` case must be rejected or U46's own bound is bypassed through a fractional spelling.

**The residual asymmetry, stated more precisely than revision 11 did.** It is not "more than 15 significant
digits" — that was a proxy, and `1e-400` (one digit) breaks it. The condition is **the canonical text denotes a
different decimal value than the literal**. Where that happens, `jsonb` keeps what the client wrote, the digest
covers the double's shortest form, a same-key retry differing only in the lost digits **replays** instead of
conflicting, and a subscriber can receive a decimal the fingerprint never covered. With G1 the worst surviving
case is a value change strictly *inside* the safe-integer band (`1.234567890123456789`); with G2 it is empty.
**Revision 14 accepted that residue deliberately** — Option B plus G1 approved, G2 deferred — and paired it with the
counter that makes it visible instead of silent (§11 items 27–28, 31).

**The variants that were on the table, and what revision 14 chose:**

| Variant | Rule | What it buys | What it costs |
| --- | --- | --- | --- |
| **B + G1 + metric** (**approved in revision 14** — §11 items 27–28, 31) | Accept every finite literal; reject when the converted double reaches 2^53 in absolute value; count canonical-decimal ≠ literal-decimal | U46's bound can no longer be bypassed by a fractional spelling; **no new status code**; the remaining residue is measured rather than assumed | One comparison after conversion plus one counter. Over-precise decimals are still accepted and still documented as "use strings" |
| **B + G2 + metric** | As above, and reject unless the canonical text denotes the literal's exact decimal value | For every accepted number the digest covers exactly the stored value; rejects no compliant emitter's shortest-round-trip output (unlike a digit cap) | One new `400` code (`number_not_exactly_representable`), a bounded decimal-string comparator in our code, and rejections of JSON that every compliant JCS implementation accepts (`1e-400`). **Deferred in revision 13 and declined by revision 14** for the four reasons in §15.1.1 |
| **B-plain** (revision 11 as written) | Accept everything finite, reject nothing extra | Simplest | Leaves the `9007199254740992.5` bypass live: an unsafe integer reaches the canonical form and the payload through a legal fractional literal, which contradicts U46 rule 1's stated purpose. **Not adopted** |
| ~~cap15 / cap17~~ | Reject by significant-digit count | Superficially simple | Rejected on evidence: `1e-400` is 1 digit and still inexact, while Python's `0.30000000000000004` is 17 digits and exact. A digit count measures the wrong thing |

**Approved for V1 (revision 14, §11 items 27–28, 31): Option B with guard G1 and the inexactness metric — G1
only.** G2 remains documented as the stronger closure and is deliberately **not** adopted: it enforces a
guarantee no approved decision asks for, it
rejects input that RFC 8785 itself canonicalizes, and the counter makes its absence visible instead of silent (§15.1.1).
The accepted/rejected sets are fully specified above, the fingerprint stays value-based, and **no approved U46
wording moves**.

### 15.2 U47: evaluated Go candidates (verified against primary sources, not names)

Every row below was read from the module's own pkg.go.dev page, its `go.mod`, or its source; each claim quotes or
paraphrases what that source says, and the last section notes what could **not** be verified in a documentation-only
phase.

| Candidate | Version / published | License | `go` directive | Duplicate-key detection | Number literals at parse time | RFC 8785 canonicalization | Verdict against the four requirements |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `github.com/goccy/go-json/jsontext` | v0.12.0, 2026-10-29 | MIT | **`go 1.23`** (read from `go.mod`) | **Yes, by default** — `ErrDuplicateName` "only reported if `AllowDuplicateNames` is false", wrapped in a `*SyntacticError` whose `JSONPointer` locates the member | **Yes** — `Decoder.ReadValue()` "holds the exact bytes of the input"; `Token.String()` "returns its raw JSON text" for a number token | **Yes** — `(*Value).Canonicalize(opts…)` documented as "the canonical form of the JSON Canonicalization Scheme (JCS) of RFC 8785" | **Meets 1–4** given our own numeric layer. Fails nothing structurally |
| stdlib `encoding/json/jsontext` (+ `encoding/json/v2`) | go1.27.4 docs; `AppendFloat`/`Float32` marked "added in go1.27.0" | BSD-3 (Go) | n/a (toolchain) | **Yes, by default** (package operates on RFC 7493: "requires that object names be unique") | **Yes** — `ReadValue()` exact input bytes | **Yes** — `Value.Canonicalize`, `CanonicalizeRawInts/Floats` "according to RFC 8785, section 3.2.2.3 … the number -0 is canonicalized as 0" | **Meets 1–4**, but **unavailable on Go 1.24**: experiment-only in 1.25 (`GOEXPERIMENT=jsonv2`), stable in 1.27 |
| `github.com/go-json-experiment/json` | v1.27.2, 2026-11-07 | BSD-3 | **requires `go 1.27`** (read from `go.mod`) | Yes, same defaults as v2 | Yes | Yes | Same window as stdlib, as a *tagged* module — attractive only if the owner wants a module rather than a toolchain feature |
| `github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer` | pseudo-version `v0.0.0-20261011152517-32a48195b9b2`, 2026-10-11 | Apache-2.0 | **no valid `go.mod` at the module root**, untagged, not v1-stable | **Yes** — hand-written parser over raw UTF-8; source sets `"Duplicate key: " + …` | **No** — `Transform(jsonData []byte)` exposes no token stream, and each number goes through `strconv.ParseFloat(value, 64)` before serialization | Yes (it is a dedicated JCS implementation) | **Fails 2 and 4 as-is**: by the time the library has looked at a number it is already a `float64`, so the ±(2^53 − 1) verdict and the 16+ digit case cannot be made from its output. Usable only if HookFlow re-parses the document itself for validation, which duplicates the parser |
| `github.com/ucarion/jcs` | v0.1.2, 2020-09-29 | MIT | n/a (single-file module) | **No** — input is a Go value | **No** — `Format(v interface{})` accepts only `bool, float64, string, []interface{}, map[string]interface{}, nil` | Yes for its supported types | **Fails 1, 2 and 3 outright**: the approved design forbids routing the digest input through a `map[string]interface{}`, where duplicates are already resolved and large integers already rounded |
| `github.com/json-iterator/go` | v1.1.12, **2021-09-11** | MIT | valid `go.mod`, tagged, "stable" | **No** — the published `Config` struct has `SortMapKeys`, `UseNumber`, `ValidateJsonRawMessage`, `DisallowUnknownFields`, `CaseSensitive` and friends; the package documentation contains **no** duplicate-name option and no occurrence of "duplicate" at all | Partial (`Decoder.UseNumber`, `ReadNumber() json.Number`) | **No JCS support whatsoever** | Not a candidate for this job. Also: unmaintained — 5+ years since the last release, and third-party guidance (goccy's own comparison table) states it "hasn't been supported for a long time" |
| stdlib `encoding/json` + a HookFlow-written RFC 8785 serializer | toolchain | BSD-3 | n/a | Requires our own token pass (`Decoder.Token()` + a seen-name set per object) | `Decoder.UseNumber()` gives `json.Number` literals | Requires implementing §3.2.2.2 (minimal string escapes), §3.2.2.3 (ECMA-262 `Number::toString`, including the `-0` → `0` case) and §3.2.3 (sort by UTF-16 code unit) ourselves | **Technically meets 1–4 with zero dependencies**, and is the fallback if the owner refuses all of the above. Cost: we own a spec-conformant float formatter and must port RFC 8785 Appendix B Table 1, the §3.2.2 example and the §3.2.3 ordering vectors as our own conformance test — the exact risk class this decision was meant to avoid |

**The single most important finding: no candidate implements U46.** Every option above, including the standard
library, treats JSON numbers as doubles *before or while* serializing them. The documentation is explicit:
`Value.IsValid` "doesn't check whether the numbers fit in any numeric type (e.g., float64, int64 or uint64)";
`CanonicalizeRawInts` warns that "integers beyond ±2⁵³ will lose their precision: `1234567890123456789` is
formatted as `1234567890123456800`"; and `CanonicalizeRawFloats` says a number beyond ±1.7976931348623157e+308
"**is saturated at that value**" rather than rejected. So:

* **A JCS library never solves duplicate-key detection or numeric validation by itself.** A HookFlow-owned
  validation layer over raw literals is required under every option, and U47's requirements 1–4 are about
  whether a candidate *permits* that layer to exist, not about whether it provides one.
* **Saturation is a silent-corruption path we must close ourselves.** A canonicalizer that clamps `1e400` to
  `1.797e308` would happily produce a digest for a value U46 requires us to reject — so the range/finite check
  must happen on the literal, before canonicalization, in our code.
* **The `-0` case is a real trap, not a curiosity.** `jsontext.AppendFloat` and `Token.Float` are documented to
  write `-0` as `-0`, while RFC 8785 §3.2.2.3 (and `CanonicalizeRawInts`/`CanonicalizeRawFloats`) write it as
  `0`. Any path that builds the digest input by converting a value to a Go `float64` and letting a token
  constructor format it can therefore diverge from the JCS path that `Canonicalize` takes. Requirement 5 below
  exists to make that a test rather than a footnote.

**Toolchain evidence (relevant to U30, which stays open).** Go 1.27.0 was released 2026-08-19 and its release
notes state that `encoding/json/v2` and `encoding/json/jsontext` are now available packages, that `encoding/json`
"is now backed by the v2 implementation", and that `GOEXPERIMENT=nojsonv2` is the temporary opt-out. Go 1.25's
notes show the same packages as **experiment-only** behind `GOEXPERIMENT=jsonv2`. Go 1.24 has neither. The Go
release policy supports each major release "until there are two newer major releases", and the release history
shows Go 1.24's last point release as go1.24.13 (2026-02-04, just before go1.26.0) — i.e. **Go 1.24 no longer
receives fixes**, and the currently supported lines are 1.25, 1.26 and 1.27. Consequences for this decision,
stated before U30 was decided, and left as written because the analysis itself is unchanged (revision 14 approved
U30's toolchain half as go1.27.4, §11 item 25):

* "Compatible with Go 1.24" is satisfiable **today** only through `github.com/goccy/go-json/jsontext` (`go 1.23`
  directive) or through the stdlib-`encoding/json` + own-serializer fallback.
* **Revision 14 outcome: U30 pinned Go 1.27.4, so path A is the selection (§11 items 25–26) and the goccy
  backport is not adopted.** The migration path that made the backport-shaped option attractive — an
  import-path change in our own code, because the API is documented as a drop-in replacement of
  `encoding/json/jsontext` — is now moot for V1, and it stays recorded because it is the reason cyberphone's
  incompatible `Transform([]byte) []byte` surface was rejected rather than merely deprioritised.
* This evidence became a proposal in revision 13 (**§15.6**, the toolchain half of U30 as **Go 1.27.4**) and an
  approval in revision 14. The two decisions remain coupled: **path A depends on the toolchain pin**, and a
  Go 1.24 pin — which revision 14 did **not** take — would have been a deliberate choice to stay on an
  unsupported toolchain line.
* One adjacent fact, recorded so nobody "discovers" it mid-implementation: Go 1.27 also adds a new `uuid`
  package. It **did not** decide U1 and was not used as an argument anywhere in this section — the sentence was
  about parser selection, and it still is: **U47's approval neither relied on nor implied anything about U1**.
  U1 was approved separately in revision 16 (§11 item 34), and that approval named **no** library, so this section
  is unchanged in substance by that approval.

**Risks worth naming for the two front-runners.** For goccy: pre-v1 (`pkg.go.dev` marks it *not* a stable
version), the `jsontext` subpackage shows `Imported by: 0` — the backport is new and largely unexercised in
public code; its `jsontext` types are **not aliases** of the stdlib ones, so `MarshalJSONTo`/`UnmarshalJSONFrom`
methods must be written against one or the other; it uses `unsafe` and runtime-internals-adjacent optimizations
for speed; and its documentation claims a non-finite formatting difference from the stdlib (`+Inf` vs `Infinity`)
— **revision 13 measured both packages and they behave the same way here** (each wrote `-0`, `+Inf`, `-Inf` for the
Appendix B rows `8000000000000000`, `7ff0000000000000`, `fff0000000000000`), so the documented difference did not
reproduce and the real finding is that *both* diverge from RFC 8785's own table on the float-formatting path, which
our pipeline never uses because non-finite input is rejected earlier. For stdlib v2/jsontext: it is a toolchain
upgrade plus a behaviour review of `encoding/json` error text, not a dependency choice. For cyberphone: untagged, no
root `go.mod`, and a `float64`-centric core, which is the structural blocker rather than a maintenance one.

**§15.2.1 What U47 must state about the leading candidate, item by item.** Recorded for
`github.com/goccy/go-json/jsontext` v0.12.0, with the source of each claim marked **[doc]** (the module's own
published documentation) or **[run]** (executed in this revision — and in revision 13, **executed against both**
the Go 1.27.3 stdlib twin **and** the goccy module itself, with identical results; the side-by-side table is
§15.2.2):

| Required item | Documented behaviour | Evidence |
| --- | --- | --- |
| Duplicate-name detection configuration | Operates as **RFC 7493 by default**, so object member names **must be unique**; `AllowDuplicateNames(true)` is the opt-**out** and HookFlow must never set it. A violation surfaces as `*SyntacticError` with `Err == ErrDuplicateName`, and its `JSONPointer` names the offending member. Confirmed reachable both token-by-token (`Decoder.ReadToken`) and on a whole `Value` (`Canonicalize` returned `jsontext: duplicate object member name "a"`) | [doc] + [run] |
| Raw number preservation | `Decoder.ReadValue()` returns the value with "the exact bytes of the input"; a number token has `Kind() == '0'` and `Token.String()` "returns its raw JSON text"; the lossy doors are `Token.Float()/Int()/Uint()`, which must not be used for validation verdicts | [doc] |
| Canonicalization behaviour | `(*Value).Canonicalize(opts…)` = `Format` with `CanonicalizeRawInts(true)`, `CanonicalizeRawFloats(true)`, `ReorderRawObjects(true)`, no whitespace, i.e. RFC 8785 §3.2.2.3 number serialization and §3.2.3 sorting by UTF-16 code unit; strings use the minimal form of §3.2.2.2 (the same rule `AppendQuote` implements); `-0` canonicalizes to `0`, while `AppendFloat`/`Token.Float` write `-0` | [doc] + [run] |
| Overflow / saturation, and the pre-validation it forces | `CanonicalizeRawFloats` documents that a number beyond ±1.7976931348623157e+308 "is saturated at that value"; `CanonicalizeRawInts` documents precision loss past ±2⁵³; `Value.IsValid` "doesn't check whether the numbers fit in any numeric type". Executed: `1e400` → `1.7976931348623157e+308` with **`err=nil`**, `1e-400` → `0` with `err=nil`, `9007199254740992.5` → `9007199254740992`, `9223372036854775807` → `9223372036854776000`. Therefore the U46 literal check **and** the §15.1.1 guard must run **before** `Canonicalize`, in HookFlow code, under this or any other candidate | [doc] + [run] |
| Conformance tests required before production use | See the list below. **Revision 13 executed them against the goccy module itself** (v0.12.0, in a throwaway module outside the repository) as well as against the stdlib, with identical outcomes (§15.2.2) — but the **Appendix I large sample-value file, V8 cross-checks and fuzzing were not run**, and the gate is a CI run against the *pinned* version, which no local run substitutes for | [doc] + [run] |

**Corrected citation, and it matters:** RFC 8785 has **no section 16**. Its structure is §1–§6 plus Appendices
A–I, and revisions 10 and 11 of these documents both referred to a "§16 test vector suite". The real conformance
material, quoted from the specification, is:

* **Appendix B, Table 1** — "ECMAScript-Compatible JSON Number Serialization Samples", which is the number
  serializer's vector set: `0000000000000000` → `0` (Zero), `8000000000000000` → **`0` (Minus zero)**,
  `0000000000000001` → `5e-324`, `7fefffffffffffff` → `1.7976931348623157e+308`,
  `4340000000000000` → `9007199254740992` ("Max pos int", footnote 1), `4430000000000000` →
  `295147905179352830000` ("~2\*\*68", footnote 2), `7fffffffffffffff`/`7ff0000000000000` → NaN/Infinity
  (footnote 3: "Values out of range are not permitted in JSON"), the `44b52d02c7e14af5..f7` and
  `444b1ae4d6e2ef4e..50` runs around `1e+23` and `1e+21`, `333333333.3333332` … `333333333.33333343` around
  `333333333.3333333`, `-0.0000033333333333333333`, and `1424953923781206.2` with footnote 4 ("exactly
  1424953923781206.25 but … rounded to the closest even value" — the spec's own instance of §15.1.1's collapse).
  Footnote 1 is also the spec's own support for U46: values "to be interpreted as true integers SHOULD be in the
  range -9007199254740991 to 9007199254740991".
* **§3.2.2's worked example** — the object with `[333333333.33333329, 1E30, 4.50, 2e-3,
  0.000000000000000000000000001]` and the `\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/` string, expected to
  serialize as `{"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],…}`; it exercises exponent rewriting,
  trailing-zero removal and the string-escape rules of §3.2.2.2 together.
* **§3.2.3's sorting test data** — the seven-key object (`\u20ac`, `\r`, `\ufb33`, `1`,
  `\ud83d\ude00`, `\u0080`, `\u00f6`) with the expected order Carriage Return, One, Control, Latin Small Letter
  O With Diaeresis, Euro Sign, Emoji Grinning Face, Hebrew Letter Dalet With Dagesh. This is the UTF-16
  code-unit sort, and the lone-surrogate rule in §3.2.2.2 ("occurrences of such data MUST cause a compliant JCS
  implementation to terminate with an appropriate error").
* **Appendix I's development portal** — the spec's own pointer for "a more exhaustive validation of a JCS number
  serializer": the large sample-value file in `github.com/cyberphone/json-canonicalization` ("JCS source code and
  extensive test data"), optionally cross-checked against V8 as a live reference, as Appendix B's closing
  paragraph suggests.
* **HookFlow's own set**, from §15.1.1's executed table, plus: canonicalization idempotence
  (`[run]`: `{"b":[1.0,1E2,-0.0,{"z":1,"a":2}],"a":0.1}` → `{"a":0.1,"b":[1,100,0,{"a":2,"z":1}]}` and a second
  pass is byte-identical), a fixed-point/fuzz pass over generated documents, and a test that no accepted document
  produces a canonical text containing an integer outside ±(2^53 − 1).

**What was and was not executed in these revisions.** A throwaway Go program outside the repository (system temp
directory, deleted after use; the repo still has no `go.mod`, no Go file and no project dependency) ran **Go 1.27.3's
stdlib `encoding/json/jsontext`** in revision 12, and in revision 13 ran **both** that package and
**`github.com/goccy/go-json/jsontext` v0.12.0** over the same battery: the §15.1.1 literals, the RFC §3.2.2/§3.2.4
golden document, the §3.2.3 ordering vector, all of Appendix B Table 1, duplicate-name handling, raw-token
preservation, malformed-document handling, string escaping, negative zero and the bounded-work cases (§15.2.2). What
was still **not** executed: the Appendix I large sample-value file, any V8 cross-check, any fuzzing, and anything
against `github.com/go-json-experiment/json` or the cyberphone canonicalizer. So the runtime-conformance claim is
limited to exactly the cases listed in §15.2.2, and requirement 7 below — approved in revision 14 as a merge gate
(§11 item 32) — remains a gate rather than a completed task. **Revision 14 executed nothing new:** it is a
documentation-only approval round, so the lists above are unchanged, no measurement exists on the approved go1.27.4
baseline, and no file outside these three documents was run, created or modified.

**Acceptance bar for U47 — approved together with the selection in revision 14; it extends, and does not
replace, the four requirements already recorded in
schema §4.13.5:** requirements 1–4 stand as written, plus

5. **`-0` must canonicalize to `0`**, pinned by a test, because the token/float formatting path writes `-0`
   while the JCS path writes `0` — measured in this revision, and RFC 8785 Appendix B lists it.
6. **The digest input must be assembled from raw input bytes.** No Go `float64`, no `map[string]interface{}`, no
   `json.Number → Float64()` conversion may appear anywhere between reading the body and hashing it; verdicts on
   numeric range are made on the literal's decimal expansion (mantissa and exponent), not on a double.
7. **One canonicalizer instance, pinned and run against the conformance set above** — Appendix B
   Table 1, §3.2.2's example, §3.2.3's ordering vectors, the Appendix I sample-value file, plus idempotence and
   fixed-point assertions — in CI **before merge** and before production use, so a future dependency or toolchain
   bump cannot silently change replay semantics. With U47 approved as path A the pin is the **toolchain version**
   (go1.27.4, §11 items 25–26) rather than a module entry, because no JSON module is added. What has been run is
   listed in §11 item 32 and §10; the Appendix I file, V8 cross-checks, fuzzing and any go1.27.4 run are
   **outstanding**, and the exact CI mechanism is U30's open half.

#### 15.2.2 Path A versus path B, executed on the same battery in revision 13

Both programs ran in the same throwaway module outside the repository (Go 1.27.3, deleted afterwards; the repo gained
no `go.mod`, no Go file and no dependency). Path A is the standard-library package, path B the goccy backport at the
version named in §15.2. "identical" below means byte-identical output **and** identical error text in every case
executed.

| # | Case group | Path A: stdlib `encoding/json/jsontext` | Path B: `goccy/go-json/jsontext` v0.12.0 | What it settles |
| --- | --- | --- | --- | --- |
| 1 | §3.2.2 sample document → §3.2.4 canonical bytes | byte-exact match against the RFC's own published output; re-canonicalizing the result is a fixed point | identical | the JCS core is sound on both paths, verified against the specification's own byte string rather than against a hand-written expectation |
| 2 | §3.2.3 UTF-16 code-unit key order (7 keys; the astral emoji `U+1F600` must sort **before** `U+FB33`) | PASS | PASS | both convert to UTF-16 code units instead of sorting Go bytes — the exact trap this vector exists to catch, since UTF-8 byte order would put the emoji last |
| 3 | Appendix B Table 1 through `Canonicalize` (24 finite rows) | 24/24 fixed points | 24/24 | number serialization matches the spec table on both |
| 4 | Appendix B Table 1 through `AppendFloat` (27 rows) | 24/27: writes `-0` for `8000000000000000` (RFC: `0`), `+Inf`/`-Inf` for the infinity rows (RFC: `Infinity`/`-Infinity`) | identical 24/27, same three rows | the float-formatting path is **not** the JCS path; requirement 5 (`-0` → `0` pinned by a test) stands, and §15.2's earlier note about an A/B difference here did not reproduce |
| 5 | Duplicate member names, default options | rejected: `jsontext: duplicate object member name "a"`; nested case reported `… "b" within "/a"`; empty key case rejected too | identical | U46 rule 3 is implementable on both, with a JSON pointer available for the log |
| 6 | `AllowDuplicateNames(true)` | accepted, output not canonical | identical | confirms this option must never be set by HookFlow code |
| 7 | Raw literal text via `ReadToken`/`Token.String()` | `1.200e1`, `9007199254740992.0`, `1e400`, `1e999999999999999999999` and a 31-digit literal all returned verbatim | identical | §15.3 stage 1 ("inspect the literal, never a decoded Go number") is possible on both |
| 8 | `Token.Float()` on overflow | returns `+Inf` **with** `value out of range` for `1e400` and for the 21-nines exponent | identical | a second, reliable overflow signal the implementation may use in stage 6 |
| 9 | `Token.Float()`/`ParseFloat` on underflow (`1e-400`) | `0`, no error | identical | underflow is invisible to error values, so only the decimal analysis can see it — which is why §15.3 keeps stages 2–3 |
| 10 | `Canonicalize` on the dangerous numerics | `1e400` → `1.7976931348623157e+308` with `err=nil`; `9007199254740992.5` → `9007199254740992`; `9223372036854775807` → `9223372036854776000` | identical | silent saturation is a property of *both* paths, so U46 validation and G1 must run in HookFlow code before canonicalization, whichever package is chosen |
| 11 | §3.2.2.2 lone surrogates (`\ud800` as a value and as a key) | rejected: `invalid surrogate pair … within "/a"` | identical | the spec's MUST ("terminate with an appropriate error") holds on both, so the pipeline maps it to `invalid_json_document`/`400` without extra machinery |
| 12 | §3.2.2.2 string escaping and invalid UTF-8 | `AppendQuote` matched an independent ECMAScript oracle for `"`, `\`, tab, newline, `U+0000`–`U+001F`, `U+007F`, `U+0080`, `€`, astral emoji and `/`; a raw invalid-UTF-8 byte sequence returned the error `jsontext: invalid UTF-8` | identical | step 5's encoder choice is safe on both, and that error must be fatal rather than discarded |
| 13 | Bounded work under hostile input | `1e999999999999999999999` canonicalized in ~0 ms; a 200 000-digit literal in 1.03 ms; 200 001-deep arrays and 50 000-deep objects rejected with `exceeded max depth` at offset 10000 | identical decisions, 0.52–0.55 ms for the two heavy cases | no allocation blow-up on either path; both already carry a depth limit (10000), which answers the Appendix F.2 concern without our own counter |
| 14 | Document-level acceptance | top-level scalars (`123`, `"x"`, `null`, `[]`) are accepted by the parser, so HookFlow's own object check is required; `{}{}`, trailing garbage and a leading BOM are rejected; leading/trailing whitespace is tolerated; an empty body is `unexpected EOF` | identical | step 3 stays ours; "exactly one document per request" is already enforced by both |

**Verdict on U47.** The two paths were **behaviourally indistinguishable on everything executed**, so this is not a
conformance decision. It was three other decisions, and revision 14 decided them (§11 item 26):

* **Dependency surface.** Path A adds nothing to `go.mod`; path B adds a pre-v1 module that uses `unsafe` and whose
  `jsontext` subpackage has `Imported by: 0`. For a fingerprint whose value *is* the replay contract, fewer moving
  parts is the safer default.
* **Toolchain coupling.** Path A needs Go ≥ 1.27, i.e. it required approving U30 first — **and revision 14
  approved it (§11 item 25)**. Path B was declared to work
  from Go 1.23 in its `go.mod`, but **only the Go 1.27.3 case was executed here** — the older-line claim stays a
  documentation reading, not a measurement.
* **Migration direction.** B → A is an import-path change; A → B is a dependency addition with type-identity
  consequences (`MarshalJSONTo`/`UnmarshalJSONFrom` are not interchangeable across the two packages). A → B is
  what revision 14 declined to take on.

**Approved in revision 14 (§11 item 26): path A, the standard-library package `encoding/json/jsontext`, at the
go1.27.4 baseline approved as U30's toolchain half.** Path B — `github.com/goccy/go-json/jsontext` v0.12.0 — is
recorded and **not adopted**, and no other JSON library is selected: **no JSON dependency is added to the
design**, and no module is named in any manifest because none exists. Requirement 7
(the full conformance set, including the Appendix I sample file, V8 cross-checks and fuzzing, in CI against the
pinned implementation before merge) travels with the approval as §11 item 32 and has **not** completed: every
executed result above comes from a **go1.27.3** run outside the repository, and nothing here tests go1.27.4.



### 15.3 The ingestion parsing pipeline (U41, U42, U46, U47, U48 in one order) — approved in revision 14

The order below is the whole point of the exercise: it is what makes "HTTP 400 **before** the key claim" an
implementable sequence instead of a slogan. **Revision 14 approved it as the normative order (§11 item 29), and
§5.1 steps 3–4 cite it.** Under the approved shape (Option B + **G1**) every step's failure
mode maps to an existing §5.5 code, so **nothing here adds an error contract**. The deferred **G2** variant was the
only thing that would have added `number_not_exactly_representable`; revision 14 did not adopt G2, so that
code stays out of §5.5 (§15.1.1).

1. **Read the body with an explicit oversize check, never a silent truncation.** Read at most
   `maxBodySize + 1` bytes — `io.LimitReader(body, maxBodySize+1)` — and if that **extra byte exists** the request
   is rejected `400 payload_too_large`; the document is parsed **only** when the read finished inside the limit.
   The `+1` probe is the point: an `io.ReadAll` over a truncated body would otherwise hand the parser a document
   that is either a syntax error (mislabelled `invalid_json_document`) or, with a permissive parser, a
   **silently shortened payload whose digest is computed as if it were the client's** — which is exactly the
   class of bug U41's "no fallback" rule is meant to prevent. Three supporting rules: `Content-Length` may be
   consulted early as a cheap refusal but is **never** the authority (it can be wrong or absent under chunked
   encoding); the limit must be applied to the **decompressed** bytes if request compression is ever enabled;
   and after a rejected oversize read the connection may need closing rather than reuse, since the unread remainder
   is still on the wire. `maxBodySize` itself is **U17**'s value and stays open — revision 14 approved the probe
   shape, not the number, so this step names a configuration and no maximum payload size is written down anywhere.
2. **Parse at token level with duplicate detection on** (RFC 7493 defaults). A `*SyntacticError` whose `Err` is
   `ErrDuplicateName` maps to `400 duplicate_json_key`; the error carries the member's `JSONPointer`, which is what
   the log line in §15.3's closing note refers to. `Value.IsValid`-style checks are *not* enough for the numeric
   rules — measured on both paths, `{"a":1e400}` and `{"a":9007199254740992}` are both "valid" and both canonicalize
   without an error — and no step below is allowed to have decoded the document into `map[string]interface{}`.
   Both paths also enforce a nesting depth limit (10000) and reject a second top-level value, so `{}{}` cannot slip
   a second document past the digest (§15.2.2 rows 13–14).
3. **Assert the envelope.** Top level must be an object (`400 invalid_json_document` otherwise), carrying the
   members the U41 formula names: `event_type`, subject to the existing `events.type` syntax rules (lowercase,
   dotted, bounded), and `payload`. This check is ours: measured on both candidate paths, a top-level `123`, `"x"`,
   `null`, `true` or `[]` is perfectly acceptable JSON to the parser and canonicalizes without complaint (§15.2.2
   row 14). **Revision 14 decided the two open halves of this step (§11 item 30), and they are now requirements,
   not questions:** `payload` itself **must be a JSON object** — a `payload` written as an array, a string, a
   number, `true`/`false` or `null` is `400 invalid_json_document` before the claim — and unrecognised members
   **inside** `payload` are **accepted**, because `payload` is opaque to the platform and no member name is
   reserved today. Rejecting a reserved envelope name would be an explicit, versioned API-contract change, so
   nothing here invents one. The §5.1-versus-§15.5 contradiction that used to sit in this step is closed in this
   direction in all three documents, and the closure adds no status code, no column and no U46 rule.
4. **Validate numbers in this exact order, on the literal first and the double second.** For every number token,
   in document order, short-circuiting on the first failure. Revision 13 **rewrote stages 2–4**: revision 12
   normalized the literal to `±0.d1…dk × 10^e` and tested integrality with `e ≥ k − 1`, which **keeps the trailing
   zeros inside `k` and so misclassified `1.200e1` (= 12) as fractional**. A misclassification here is a wrong U46
   verdict, so the procedure below is the one that was implemented and executed against all nine literals
   (§15.3.1), and **revision 14 approved it as canonical (§11 item 28) — the revision-12 `e ≥ k − 1` formulation
   must not be implemented.** It is written as `value = D × 10^p`, where `D` keeps its trailing zeros on purpose.

   1. **Inspect the original literal** (`Token.String()` on a number token, or the raw value slice) — never a
      decoded Go number. Measured available on both candidate paths (§15.2.2 row 7).
   2. **Split the literal into `D` and `p` by string work only.** `D` = integer digits followed by fraction digits,
      **leading zeros stripped, trailing zeros kept**; `p` = written exponent − fraction digit count. The exponent is
      parsed as a bounded integer and **clamped** (more than 7 exponent digits means "far past any range decision we
      can make", and only its sign is retained), so no attacker-controlled exponent can allocate work.
   3. **Decide integrality of the mathematical value, regardless of notation.** Let `tz(D)` be the number of
      trailing zeros of `D`. The value is an integer **iff `p ≥ 0`, or `p < 0` and `tz(D) ≥ −p`** — i.e. the shift
      can be paid for with digits that are actually zeros. Otherwise the value is fractional. This is the step that
      replaces the `e ≥ k − 1` test, and it is what makes `1.200e1` (`D="1200"`, `p=−2`, `tz=2`) integral while
      `1.2001e1` (`D="12001"`, `p=−3`, `tz=0`) is not. Integrality is **never** inferred from the absence of `.` or
      `e`.
   4. **If integral, enforce U46's range before any lossy conversion.** The integer's decimal digit count is
      `len(D) + p` (computed, never materialized). Compare against `len("9007199254740992") = 16`: count < 16 →
      safe; count > 16 → unsafe; count = 16 → **only then** build the 16-character string and compare it
      lexicographically against `9007199254740992`, unsafe if greater or equal. That is the bounded-parsing
      requirement the review asked for: `1e999999999` is decided by reading `p`, and a `big.Int` decimal shift for it
      would allocate hundreds of megabytes per request, turning a validation rule into a denial-of-service vector.
      Verdict: `integer_out_of_safe_range`, which is also the code `1.7e308`, `1E30`, `9223372036854775807` and
      `1e400` get — U46's integer rule is evaluated **before** the overflow rule, deliberately and deterministically,
      so a client sees one stable code per input rather than whichever check happened to fire first.
   5. **Convert under the JCS/IEEE-754 model** — `ParseFloat` to binary64, the conversion RFC 8785 §3.1 and
      §3.2.2.3 presuppose. This is the first point precision can be lost, and stages 2–4 have already run.
   6. **Reject non-finite results** (`math.IsInf` / `math.IsNaN`, or an `ErrRange`) with `non_finite_number`. Two
      measured facts fix the shape of this stage: Go's `ParseFloat` reports `ErrRange` on **overflow** but returns
      `0, nil` on **underflow** (`1e-400`), and the library's own `Token.Float()` does report `value out of range` on
      overflow while staying silent on underflow — so an error value alone can never be the gate for both classes, and
      stage 3 remains the only place an underflowed literal is visible.
   7. **Apply guard G1.** If `math.Abs(f) ≥ 2^53`, reject as `integer_out_of_safe_range`. No `math.Trunc` test is
      needed: every binary64 at or above 2^53 is an integer, so the guard already covers exactly the "fractional
      literal collapsed onto an unsafe integer" class (`9007199254740992.5`,
      `123456789012345678901234567890.5`) and `Canonicalize` reports no error for any of them. **G2 is deliberately
      absent here** — it was deferred with reasons in §15.1.1, and revision 14 **ratified the deferral** (§11 item 28),
      which keeps
      `number_not_exactly_representable` out of §5.5.
   8. **Accept the rest**, incrementing `hookflow_ingest_number_inexact_total` when the canonical decimal differs from
      the literal's own decimal value (the comparison is the same `D`/`p` normalization plus the float's shortest
      round-trip text — no big integers, and it runs only on the accept path for the counter), and **never** calling a
      `Float()`/`Int()`/`Uint()` accessor to produce a verdict — that is the collapse point where
      `9007199254740992` and `9007199254740993` become one number. The counter itself is approved (§11 item 31) as
      an **observability instrument only**: it carries no numeric limit, it can never cause a rejection, and it is
      never a response field or a stored column.

5. **Build the wrapper through a real JSON string encoder, then take the digest input from raw bytes.** The
   canonicalization input is `{"event_type":<encoded string>,"payload":<raw payload bytes>}`:

   * `event_type` **must be encoded as a JSON string** by an encoder — `jsontext.String(s)` (whose `Token`
     serializes with the §3.2.2.2 minimal-escape rules) or `jsontext.AppendQuote(nil, s)` — never by writing
     `"\"event_type\":\"" + eventType + "\""`. String concatenation of an unvalidated or unescaped value would
     let a quote, backslash, control character or `</script>`-style payload alter the document structure the
     digest covers, which is a signature-reading bug, not a formatting nit. HookFlow's own `events.type` syntax
     rules make the escape set empty in practice, and that is still not a reason to hand-build JSON: the encoder
     is the guarantee, the syntax rule is only a filter above it. Six of these were measured against **both**
     candidate paths: `AppendQuote("a\"b")` → `"a\"b"`, `AppendQuote("tab<TAB>here")` → `"tab\there"`,
     `AppendQuote("€")` → `"€"` (non-ASCII emitted literally, per §3.2.2.2), `U+007F`/`U+0080` emitted literally,
     and control characters as lowercase `\u00xx`. **Invalid UTF-8 was measured too, and it matters more than the
     escapes:** a Go string holding a raw invalid byte sequence makes `AppendQuote` return the error
     `jsontext: invalid UTF-8` while the bytes it already wrote contain U+FFFD substitutions — so that error must be
     treated as fatal and never discarded, or the digest would be computed over mangled bytes the client did not
     send. The same rule applies to the lone-surrogate case in step 2, which both paths reject outright.
   * `payload` must come from the **validated raw JSON representation** already parsed in steps 2–4 — the exact
     input bytes, per `Decoder.ReadValue()` — and never from PostgreSQL `jsonb`, and never by re-marshalling a
     Go value. That boundary rule is approved U41/U46 text and revision 12 repeats it because the wrapper step is
     where it could be quietly violated: a `jsonb` round-trip gets insertion order and `numeric` formatting
     instead of RFC 8785, and a `map[string]interface{}` round-trip reintroduces both `float64` rounding and
     already-resolved duplicate keys. Either way two identical requests could hash differently.
6. **Canonicalize once** with the RFC 8785 rules (member order by UTF-16 code unit, minimal string escapes,
   §3.2.2.3 number serialization, `-0` → `0`, no whitespace). Executed on both candidate paths in revision 13: the
   §3.2.2 → §3.2.4 golden document matched the RFC's published bytes, the §3.2.3 UTF-16 ordering vector came out in
   the spec's order, and a second canonicalization pass was byte-identical (§15.2.2 rows 1–3). This step must not
   receive a value that skipped stages 1–7: `1e400` and `9007199254740992.5` both canonicalize happily, which is the
   whole reason the order is fixed.
7. **Hash**: `request_hash = SHA-256(UTF-8(canonical))`, exactly 32 bytes — the value `chk_idem_hash_len`
   requires. Steps 1–7 all run **before** `BEGIN`, so the digest never holds a row lock (approved in revision 8)
   and a rejected request leaves no trace in any table (U4, U42, U45).
8. **Then the approved transaction**: claim-first `INSERT … ON CONFLICT DO NOTHING RETURNING id`, and the
   replay / `409` / expired-takeover branches, unchanged, with `202` only after `COMMIT` (U5, U42, U43, U44,
   U45). The pipeline neither weakens nor extends any of them.

Two properties this ordering guarantees, worth stating because they are the reason the order is fixed: **a
document that fails U46 can never reach the claim**, and **the bytes that produced the digest are the bytes that
were validated** — there is no intermediate Go representation where they could diverge.

On error reporting: `duplicate_json_key` and the numeric codes have an exact document position available (the
`JSONPointer` on the syntax error, and the member path walked in step 4). **That position must stay in the log,
not the response** — U42 fixes `detail` to a static string per code and forbids request content in a body, and a
pointer contains the client's own member names. Echoing the pointer would be a deliberate widening of U42 and is
**not** proposed here; if the owner wants it, it is a separate decision.

#### 15.3.1 The nine boundary literals, worked through the stages and re-executed

This is the algorithm applied to the literals the review listed, then run against both candidate paths on Go 1.27.3.
**Revision 14 approved the procedure and this table as the canonical reference cases (§11 item 28); nothing here ran
on the approved go1.27.4 baseline, which is why §11 item 32 keeps the conformance run open.**
"`Canonicalize`" is the library's output for `{"a":<literal>}` **without** HookFlow's checks — it is shown precisely
because the library alone decides nothing.

| Literal | `D`, `p` | stage 3: integral? | stage 4: U46 verdict | `float64` | stage 7: G1 | `Canonicalize` (A and B) | Final verdict |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `1.200e1` | `1200`, `−2` | yes — `tz(D)=2 ≥ 2`, 2 digits | safe | `12` | no | `{"a":12}` | **accepted**, canonical `12` (revision 12's `e ≥ k − 1` test wrongly called this fractional) |
| `1.2000e2` | `12000`, `−2` | yes — `tz(D)=3 ≥ 2`, 3 digits | safe | `120` | no | `{"a":120}` | **accepted**, canonical `120` |
| `1200e-2` | `1200`, `−2` | yes — `tz(D)=2 ≥ 2`, 2 digits | safe | `12` | no | `{"a":12}` | **accepted**; same value and same digest as `1.200e1`, which is U41's point |
| `1.2001e1` | `12001`, `−3` | **no** — `tz(D)=0 < 3` | not applicable | `12.001` | no | `{"a":12.001}` | **accepted** as a genuine fraction inside the safe band |
| `9007199254740992.0` | `90071992547409920`, `−1` | yes — `tz(D)=1 ≥ 1`, 16 digits | **unsafe** — the 16-digit string equals `9007199254740992` | 9.007199254740992e+15 | would also reject | `{"a":9007199254740992}` | **rejected at stage 4**, `integer_out_of_safe_range`, before any conversion |
| `9007199254740992.5` | `90071992547409925`, `−1` | **no** — `tz(D)=0 < 1` | not applicable | 9.007199254740992e+15, no error | **rejects** | `{"a":9007199254740992}` | **rejected at stage 7** — the case that proves a post-conversion guard is necessary, and the case stage 4 alone cannot see |
| `1.7e308` | `17`, `307` | yes — `p ≥ 0`, 309 digits | **unsafe** — 309 > 16, no string built | 1.7e+308, no error | would also reject | `{"a":1.7e+308}` | **rejected at stage 4**; `1E30` (31 digits) and `9223372036854775807` (19 digits) behave the same way |
| `0.1` | `1`, `−1` | no — `tz(D)=0 < 1` | not applicable | 0.1 | no | `{"a":0.1}` | **accepted**; canonical text equals the literal's decimal value, so the counter does not fire |
| `1e-400` | `1`, `−400` | no — `tz(D)=0 < 400`, decided by counts with no digit growth | not applicable | `0`, `err=nil` | no (zero is safe) | `{"a":0}` | **accepted and counted** — `jsonb` stores `1E-400` while the digest covers `0`; under G1-only this is the documented residue (§15.1.1), and the metric is what makes it visible |

Three separations the review asked to keep distinct, and where each lives:

* **(a) U46's original-value integer-range validation** — stages 2–4, decimal strings, before any conversion. It
  rejects `9007199254740992.0`, `1.7e308`, `1E30`, `9223372036854775807`, `1e400` and `295147905179352830000`.
* **(b) post-binary64 G1** — stage 7. It rejects only what (a) cannot see: fractional literals whose double is an
  unsafe integer, e.g. `9007199254740992.5` and `123456789012345678901234567890.5`.
* **(c) finite / overflow / underflow handling** — stage 6, with the measured asymmetry that overflow raises
  `ErrRange` (from `ParseFloat` and from `Token.Float()`) while underflow returns `0` with no error at all, which is
  why `1e-400` reaches the accept path and the counter rather than an error.

None of the three edits U46: (a) is U46 read correctly, (b) refuses a value U46 already refuses, and (c) is U46
rules 4–5 as approved.

### 15.4 What revision 14 changed in these documents, and what it deliberately left alone

* **Changed here (documentation only):** §11 gained items **25–32**; §13 keeps **31 rows** and relabels them —
  **29 open**, plus **U47 and U48 retained as Approved records** (the first revision-14 draft had deleted those two
  rows; restoring them was a required correction, because the register is the historical record of every ID the review
  opened), and **U30 stays open** with its Go-toolchain half annotated as approved; **19 approved IDs** are now listed
  in §11 (that count is revision 14's own: **revision 15 recorded U1 as item 33 on a premature approval claim, and
  revision 16 added the owner's approval as item 34, taking the count to 20 approved IDs, 28 open
  rows and the same 31 register rows**, with §12.14 as that round's table);
  §5.1 steps 3–4 now cite §15.3 as the normative order and §5.5 records that the `payload`-object rejection and G1
  reuse existing codes; §15 is retitled from a proposal section into a decision record with open remainders; §7
  carries the approved counter; §10 lists the approved gate with executed and pending material separated; and schema
  §4.7, §4.13.5, §4.13.6, §9 plus the ERD comments state the same statuses. The eight-stage integrality procedure,
  the nine worked literals in §15.3.1 and the parse order in §15.3 are canonical now, so a deviation is a bug rather
  than a variation.
* **What still has to change in code and infrastructure — not in this revision:** the pinned version has to appear
  in three places that **do not exist yet** — the `go` directive of a future `go.mod`, the `go-version` of a future
  CI job, and the base image of a future Dockerfile. None of those files was created, edited, staged or committed
  here, and no Go source, SQL, migration, container, workflow or dependency changed. Approving a toolchain in
  documentation is a decision, not an upgrade, and no implementation or runtime verification is claimed by it.
* **Did not change:** the 13-table inventory, every column, constraint, index and relationship; U41's formula;
  U46's five rules and their canonical wording; U42's status codes, body and `detail` discipline — **status codes:
  none added**, because G1 reuses `integer_out_of_safe_range` and the `payload`-object rule reuses
  `invalid_json_document`, and because G2 was not adopted, `number_not_exactly_representable` stays outside §5.5;
  U43's three configuration keys; U44's trigger; U45's "no history, no per-conflict audit row"; **U1, U3, U6 and
  U17**, which stay open — no maximum payload size was chosen; and U30's lint/CI half. The approved counter adds a
  metric name and nothing else — **not** a column, not a table, not a response field, and **not** a numeric
  threshold — which is why the ERD counts are unchanged: 13 blocks / 17 relationship lines / 152 attribute lines.

### 15.5 Questions this section used to leave open, and where the revision-14 round landed

Recorded as notes, not as new IDs, so they cannot be mistaken for approvals. Four of the six were closed by the
approval round; two remain open.

* **Accept the measured residue, or reject it (G1 vs G2) — CLOSED in revision 14.** The owner ratified **G1 only**:
  the platform deliberately tolerates a same-key retry that differs only beyond double precision replaying as
  identical, and counts it instead of refusing it. §15.1.1 keeps the four reasons and the two revival conditions as
  the record of *why*, and G2 stays **deferred** rather than rejected-on-principle.
* **Metric name and threshold — name CLOSED, threshold deliberately not set (revision 14).**
  `hookflow_ingest_number_inexact_total` is approved (§11 item 31) as a counter, **not** as a validation threshold.
  No numeric limit exists in the API contract and none was invented; unlike the U43 sweep numbers, which are
  configuration by decision, there is nothing for the request path to read here. What stays open is operational —
  the label set and any alert threshold — and is recorded in the §13 residual notes.
* **Whether `payload` must be a JSON object — CLOSED in revision 14 (§11 item 30).** An object is the only accepted
  type: an array, string, number, boolean or `null` is `400 invalid_json_document` before the claim, and
  unrecognised members **inside** `payload` are accepted because no member name is reserved. This is the single
  direction in which the §5.1 contradiction was resolved, and §5.1, §5.5, §15.3 step 3, schema §4.7/§4.13.5 and the
  ERD note now say the same thing.
* **Whether the conformance run is extended and made a merge gate — CLOSED as a requirement, still open as work
  (revision 14, §11 item 32).** The gate is approved: Appendix I sample values, V8 cross-checks, fuzzing,
  idempotence/fixed-point assertions and the boundary cases, run in CI **before merge**. Revisions 12–13 executed
  part of that set on go1.27.3; the rest has not run anywhere, **so the suite has not passed**, and production use
  must not precede it. The mechanism — job layout, trigger, container image, Actions versions — is U30's still-open
  CI half.
* **U30's other half — STILL OPEN.** Linter set, complexity budgets, `depguard`/`importas` rules, whether the
  available golangci-lint build works with a go1.27.4 baseline (never verified here), and Actions versions. §15.6
  and §11 item 25 decide the toolchain only.
* **The revision-10 residuals — UNTOUCHED.** The 72-hour window as configuration, a `Location` header on the `202`,
  and RFC 9457 on the subscriber surface stay exactly as §13 records them.

### 15.6 U30 toolchain decision: go1.27.4 as the approved V1 baseline

**Approved in revision 14 (§11 item 25) — the toolchain half of U30 only.** The lint/CI configuration half of the
same register row stays open, and no `go.mod`, workflow file or Dockerfile is created or edited in this phase. The
pin therefore exists **in documentation only**: this machine still runs go1.27.3, no toolchain was upgraded, and no
behaviour of go1.27.4 has been executed or measured here (§11 items 26 and 32 record exactly what is and is not
evidence).

| Question | Verified answer | Source |
| --- | --- | --- |
| What is the current stable line and its newest patch? | **go1.27.4**, released **2026-11-10**, marked `stable: true` in the download index; the 1.27 series runs 1.27.0 (2026-08-19), 1.27.1 (2026-09-01), 1.27.2 (2026-10-08), 1.27.3 (2026-10-19), 1.27.4 | go.dev release history and `/dl/?mode=json` |
| Is that line supported, and for how long? | The published policy is "each major Go release is supported until there are two newer major releases", so with 1.27 current the supported lines are **1.25, 1.26 and 1.27**; the newest 1.26 patch is go1.26.11 (2026-11-10) and the newest 1.25 patch listed is go1.25.14 (2026-08-19) | go.dev "Release Policy" |
| What about Go 1.24, which earlier turns assumed? | **Unsupported.** Its last release is go1.24.13 (2026-02-04); nothing has shipped for the 1.24 or 1.23 lines since (1.23 ended at go1.23.12, 2025-08-06). Pinning 1.24 means shipping on a toolchain that receives no security fixes | go.dev release history |
| Does the security posture of 1.27.4 matter here? | Yes, and it is on-topic: go1.27.4 "includes security fixes to cgo, the `go` command, the compiler, the linker, and the `net/http` package", and 1.27.1/1.27.2 fixed `encoding/json` and `encoding/json/v2` — the very packages this design depends on. Pinning the **patch**, not just the minor line, is the point | go.dev release history |
| Does the standard library do what U47 path A needs? | Yes, and only from 1.27: `encoding/json/jsontext` and `encoding/json/v2` are ordinary documented packages (1.25 has them only behind `GOEXPERIMENT=jsonv2`; 1.24 has neither), and `encoding/json` is now backed by the v2 implementation with `GOEXPERIMENT=nojsonv2` as the temporary opt-out | Go 1.27 release notes |
| Is anything in the approved design tied to Go 1.24? | **No.** Nothing in U1–U46 names a Go version; the only couplings found are U47 path A (wants ≥ 1.27) and U26's module path (version-independent). Echo, pgx and sqlc are not yet chosen, so their floors are **unverified** and must be checked when the manifest is written | §13, §15.2 |
| What does it cost in CI and containers? | Nothing new to install: the official images for this exact patch already exist (`1.27.4`, `1.27.4-alpine`, `1.27.4-alpine3.23`, `1.27.4-alpine3.24`, `1.27.4-trixie`, `1.27.4-windowsservercore`, `1.27.4-nanoserver`, all pushed 2026-11-10/16), and the local tooling already runs on this line — the machine's toolchain is go1.27.3 with `GOTOOLCHAIN=auto`, golangci-lint **2.15.0** is built with go1.27.3, and govulncheck **v1.10.0** runs here. No workflow or container file is written in this phase, so this row is a statement about availability, not about a configuration decision | Docker Hub `library/golang` tag listing; local `go version` / tool `--version` runs |

**Why 1.27.4 rather than 1.26 or 1.25.** 1.27 is the only supported line with the stdlib JSON packages this
fingerprint depends on, so choosing it makes U47 path A available with **zero added dependencies**; 1.26 and 1.25
would force path B (a third-party module) or the hand-written serializer. It is also the newest patch of that line —
**one patch ahead of go1.27.3, which is the version every measurement in §15 was actually made on**, which is exactly
why §11 item 32 requires a conformance run on the pinned baseline rather than treating these results as if they
were produced by it.

**Risks worth recording with the choice.** (1) `encoding/json` becomes v2-backed, so error strings, `omitempty`
edge cases and number formatting should be re-read when real code exists — flagged in §15.2 and not yet reviewable
because there is no code. (2) A pinned patch means a maintenance duty: the project should follow the roughly monthly
patch cadence, which is a phase-2 dependency-management decision still inside U30's open CI half. (3) The 1.27 JSON
packages were still being patched in 1.27.1 and 1.27.2, which is normal for a first release of a package but is a
reason the conformance gate (requirement 7, approved in revision 14 as a merge gate) is permanent CI machinery and
not a one-off — and the Phase 7 package has since run locally on go1.27.4, while the gate's remaining items (Appendix
I, V8 cross-checks, sustained fuzzing and hosted-CI execution) are still pending (§10, §16.10). (4) Contributor and deployment images must move to
1.27.4; on this machine that is a patch bump from go1.27.3, not a line change, **and nothing was bumped here**:
this revision changed documentation only, and whether the golangci-lint build available here works with a 1.27.4
baseline is recorded as unverified rather than assumed.

**Not decided here:** U1 (Go 1.27 adds a `uuid` package; recorded in §15.2 as explicitly *not* an argument — and
**U1 was approved in revision 16 by its own §11 item 34, with no dependency named, so this section still decides
nothing about it**), U3
(PostgreSQL version), U6 (secret storage), **U17** (no maximum payload size is implied by a toolchain choice, and
none is written down), and U30's linter/complexity/`depguard`/`importas`/Actions half — including the CI mechanism
that will run the approved conformance gate.

### 15.7 Consolidated decision table — the revision 14 outcomes

The single consolidated table. Every row now carries the status the owner's approval round produced; **a row is
approved exactly as written here, approving one approved no other**, and the rows marked open are open on purpose.
No file outside these three documents changed.

| ID | Decision | Outcome (revision 14) | Alternatives on the table | Blocks | Overreach check |
| --- | --- | --- | --- | --- | --- |
| **U30** (toolchain half) | Go baseline for V1 | **Approved: go1.27.4** (§11 item 25, §15.6) — newest patch, supported line, the only line with the stdlib JSON packages, nothing needs 1.24 | 1.26.11 or 1.25.x (both supported, both force U47 path B); staying on 1.24 (unsupported) — not chosen | formerly gated U47 path A; feeds a future `go.mod`, CI job and Dockerfile base image | No. U30's lint/CI/Actions half stays open, and U1/U3/U6/U17 are untouched |
| **U30** (other half) | `.golangci.yml` linter set, complexity budgets, `depguard`/`importas`, golangci-lint compatibility with this baseline, Actions versions | **Open — explicitly not approved** (§13, §15.5) | none chosen; nothing configured | the first Go file's lint/CI setup, and the mechanism that will run the approved gate | Approving the toolchain was not read as approving the configuration, and no config file was written |
| **U47** | JCS parser/canonicalizer | **Approved: path A — stdlib `encoding/json/jsontext`** (§11 item 26); executed conformance in §15.2.2 was identical to path B on every case run, and path B (`github.com/goccy/go-json/jsontext` v0.12.0) is recorded and **not adopted** — **no JSON dependency is added** | `go-json-experiment` v1.27.2 (needs go 1.27 anyway); stdlib + own serializer (zero deps, we own a spec-conformant float formatter); cyberphone (fails U46 structurally); ucarion/jcs and json-iterator (rejected outright) | first Go source file; §15.3 steps 2 and 6 | No. Naming a standard-library package adds nothing to any manifest, and the go1.27.4 run is still pending |
| **U48** | Decimal policy beyond the double round-trip | **Approved: Option B** (§11 item 27) — accept every finite literal, canonicalize per RFC 8785, and require exact-decimal quantities as JSON strings by contract (§15.1) | Option A (strict text equality — breaks U41's purpose); B-plain (leaves the U46 bypass); cap15/cap17 (withdrawn on evidence) | API docs, `payload` validation | No. U46's wording is unchanged and was not stretched |
| **U48 / G1** | Post-conversion guard | **Approved** (§11 item 28): reject `math.Abs(f) ≥ 2^53` after conversion, reported as `integer_out_of_safe_range`; it is §15.3 stage 7 (§15.1.1) | none that still satisfies U46 rule 1 — dropping G1 leaves `9007199254740992.5` accepted | inside the approved parse order | No new status code, no schema change, and no runtime verification claimed |
| **U48 / G2** | Exact-round-trip guard | **Deferred — not approved, not implemented, not required in V1** (§11 item 28; four reasons and two revival conditions in §15.1.1) | Adopt it and add `number_not_exactly_representable` to §5.5 | nothing in V1; §5.5 only if a revival condition is met and the owner approves | Deferring keeps the client-visible contract smaller than revision 12 proposed, and the round did not revive it |
| **U48 / metric** | Inexactness observability | **Approved by name** (§11 item 31): `hookflow_ingest_number_inexact_total`, canonical-decimal ≠ literal-decimal, metrics/logs only | none paired with a G2 deferral | §7 operations, §10, §11 | No column, no response field — and **no numeric threshold or limit anywhere in the API contract**; alerting is operational configuration |
| **Payload shape** | top-level `payload` type, and unknown members | **Approved** (§11 item 30): `payload` must be a JSON **object**; an array, string, number, `true`/`false` or `null` is `400 invalid_json_document` before the claim; unrecognised members inside `payload` are **accepted** and no name is reserved | accepting any JSON value (the question §15.5 used to leave open — closed); rejecting unknown `payload` members (declined: it would widen the contract without a decision about reserved names) | API docs, `payload` validation, §15.3 step 3 | No new status code, no column, no U46 rule; reserving envelope names stays a future explicit change |
| **Pipeline** | §15.3 as the normative parse order | **Approved** (§11 item 29) — steps 1–8 with the corrected eight-stage numeric order and §15.3.1's worked cases; §5.1 steps 3–4 cite it and the transaction boundaries are unchanged | the revision-12 stage-3 test, which is wrong on trailing zeros and must not be implemented | §5.1, schema §4.13.5 | No. Every rejection it adds is already an approved code |
| **Gate** | Conformance in CI | **Approved** (§11 item 32): requirement 7 permanently — §15.2.2's battery plus the Appendix I sample file, V8 cross-checks, fuzzing, idempotence and fixed-point assertions, run against the pinned implementation **before merge** | treating the local go1.27.3 run as sufficient — explicitly not what was approved | production use; the CI mechanism itself is U30's open half | A test plan, not a contract. **The suite has not passed**, and no go1.27.4 run exists |

**How they were taken.** U30's toolchain half and U47 were approved together, because path A depends on the pin;
U48 was approved as Option B with G1, the counter and the parse order, with **G2 left unapproved**; the conformance
gate was approved as a merge requirement. An owner approving this set gets a pinned parser, a numeric policy, an
ordered boundary, one metric name and a test gate — which is the coherent minimum the revision-13 table described,
and it is **not** the whole of U30: the linter set, budgets, golangci-lint compatibility and Actions versions are
still open, as are U1, U3, U6 and U17. **(That last list is revision 14's own record: U1 was approved in revision 16
and no longer belongs to it; U3, U6 and U17 do.)**

**Documented, not implemented.** §11 now ends at item **34**, and §13 holds **31 rows**: **28 open** (U30 among them,
partially approved) plus **U1, U47 and U48 retained and marked Approved**, because approving a decision does not erase the
record that the register carried it. The schema is unchanged at **13 tables / 14 agreed relationships** with the ERD at
**13 blocks / 17 relationship lines / 152 attribute lines** — and that is true of revisions 15 and 16 alike: U1 fixes *where*
identifiers are produced, which adds no column, no type, no constraint, no index and no default. Revisions 11–13 produced recommendations and
measurements; revision 14 produced **decisions on paper only** — no Go source, SQL, migration, `go.mod`, container,
workflow or dependency file was created or changed in that round, nothing was staged or committed, and no
implementation or runtime verification was claimed for go1.27.4, the parse order, the counter or the gate.
*(That is revision 14–16 status; the M1-E Phase 2.1 implementation that exists now is recorded in §16.)*

## 16. Implementation status — M1-E Phase 2.1 and Bundle 1 (2026-11-24)

This section states what the repository actually contains. It records status only: it approves nothing,
reopens nothing, and changes no contract in §1–§15. Earlier owner decisions are in §12.16-§12.18; the Bundle 1
implementation and D7 authorization are recorded in §12.19. Bundle 1 verification is pending as stated in §16.6.

### 16.1 Repository state

* `go.mod` and `go.sum` exist for module `github.com/metamin44/hookflow`, Go 1.27.4. Direct dependencies are
  `github.com/golang-migrate/migrate/v4 v4.20.1`, `github.com/jackc/pgx/v5 v5.13.0` and
  `github.com/rabbitmq/amqp091-go v1.17.0`. HTTP uses the standard library; there is no encryption/KMS SDK.
* `db/migrations/` contains the 13 approved units as 26 paired up/down files. Unit 13 carries
  `idem_assert_linked()` and the deferred constraint trigger `trg_idem_linked` (U44). `db/queries/` is
  still empty.
* Implemented packages: `cmd/api`, `cmd/migrate`, `cmd/outbox-publisher`, `cmd/worker`, `cmd/apikey`,
  `cmd/dev-keyring`; `internal/app/delivery`, `internal/app/disposition`, `internal/app/idempotency`,
  `internal/app/management`, `internal/app/accesskeys`, `internal/app/outbox`, `internal/app/publisher`,
  `internal/app/recovery`, `internal/app/worker`; `internal/modules/apikey`, `audit`, `endpoint`, `organization`,
  `subscription`; `internal/platform/config`, `database`, `http`, `kek`, `messaging`, `observability`, `secrets`,
  `signing`. `internal/app/event`, `internal/platform/logger` and `internal/shared` remain placeholders. The D6/U25
  development runtime is present; no production provider, ingestion or fan-out code exists.
* D7 adds `.github/workflows/ci.yml`, `.golangci.yml`, `README.md` and operations documentation. The workflow
  uses full-SHA action pins and pinned PostgreSQL/RabbitMQ service images; it has not yet run.
* Git auditability is still open: the repository history is the docs-only HEAD, and all implementation
  files are untracked. Nothing was staged or committed by this phase, so the Git history does **not**
  represent the implementation.

### 16.2 Implemented cores and Bundle 1 runtime

* Publisher T1/T2/T3 database core: fenced claim, publish finalisation, failure accounting with
  backoff/final failure, and expired-lease reaping (`internal/app/outbox`).
* Recovery Sweep duties and bounded execution: expire → repair → re-arm → stale passes with explicit
  per-pass candidate/duration budgets and a total budget (`internal/app/recovery`).
* Worker exact-pointer lease acquisition, pre-HTTP budget guard, fenced completion, and decryption-failure
  disposition (`internal/app/delivery`).
* Bundle 1 broker runtime: application-managed reconnect, fail-closed topology/policy verification, N=1
  confirm/return Publisher, generation-owned Worker consumer, unsigned empty-secret HTTP executor, retry ladder,
  and worker-hosted Recovery scheduler (`internal/platform/messaging`, `internal/app/publisher`,
  `internal/app/worker`, `internal/app/recovery`).
* Optional process-local Publisher/Worker metrics (`internal/platform/observability`).
* Broker-independent delivery disposition policy (`internal/app/disposition`).
* Idempotency-record cleanup sweep and store scaffolding (`internal/app/idempotency`).
* Runtime configuration parsing and validation, pgx pool and migration runner, and the health HTTP server
  (`internal/platform/*`).
* Lightweight structured logging via `log/slog` at the process seams; unit tests plus PostgreSQL and RabbitMQ
  integration test files exist for the implemented areas (`-tags=integration`, serialised with `-p 1`). Their
  Bundle 1 execution is pending (§16.6).

### 16.3 Earlier Phase 2.1 correctness hardening

* **B-1** — `RecordDecryptFailure` validates the fence first, samples fresh database time, reads the
  authoritative `dispatch_lease_expires_at` from the locked row, checks the event horizon first and the
  worker lease second. An expired lease terminalises the delivery as `expired` with no attempt row, no
  `attempt_count` or `dispatch_seq` change and no retry/outbox row. Tests cover a live event, an
  already-expired event, wrong token/generation/organization/event, a missing delivery and repeated
  invocation.
* **M-2** — one overflow-safe `config.ValidateDurationBudget` invariant is used by both
  `config.Recovery.Validate` and `recovery.Budget.Validate`, so loader and Sweep constructor cannot
  disagree; the per-pass durations must sum to at most the total. No defaults were added in that phase;
  owner-approved Bundle 1 defaults are recorded in the configuration reference.
* **M-3** — diagnostic truncation is rune-based and never splits UTF-8, within the existing
  500-character `last_error` limit; control characters are normalised and deterministic heuristic
  credential scrubbing is applied before truncation. The caller contract states that arbitrary
  binary/unstructured payloads are not guaranteed to be fully scrubbed.
* **L-2** — configuration integer/duration parsers trim surrounding whitespace before parsing and still
  fail closed on empty or whitespace-only values, naming the environment variable in the error. No
  defaults were added in that phase; later optional runtime loaders default only when a key is unset.
* **L-1** — publisher, worker and Recovery fencing tests were expanded, including a deterministic
  Sweep-expiry-versus-valid-worker-lease case: the expiry pass wins and the late `Complete` is fenced
  (§12.18).

### 16.4 Explicit limitations and deferred work

> **Checkpoint record (Phase 2.1).** The bullets below describe that checkpoint and are retained as history.
> Current status is in §16.8–§16.9 and Revision 20: the D6/U25 development implementation exists (local file KEK,
> `hfse1.` envelope/AAD, deployment-mode gate, Standard Webhooks v1 signing), the pinned broker matrix executed,
> and production KMS/Vault custody plus hosted CI remain unexecuted/not ready.

* RabbitMQ client, topology/policy, Publisher and Worker runtime loops are implemented. The pinned 4.4.1/4.3.8
  broker matrix and live failure/reconnect verification have not yet run; no broker-test pass is claimed.
* Real bounded HTTP exists, but D6/U25 do not: nonempty `secret_encrypted` fails closed through durable
  decrypt-failure handling and never reaches HTTP. Empty-secret snapshots execute unsigned HTTP unconditionally;
  no deployment-mode gate exists (D6-F proposed, **NOT APPROVED**).
* No encryption, decryption, signing, KMS custody or rotation is implemented; those decisions remain open.
* The API remains health-only. Event acceptance, authentication, endpoint/subscription management and fan-out
  are not implemented.
* Optional process-local metrics exist for Publisher/Worker, but there is no production scrape/alert topology,
  dashboard, DLQ-growth alert or Recovery sweep-age alert.
* Owner-approved Publisher/Worker/Recovery/RabbitMQ defaults are implemented with cross-field validation.
  Production HA, SSRF/egress controls and production activation remain blockers.
* The race detector is configured on Ubuntu with CGO in D7 but has not been run by this reconciliation. The
  earlier Windows CGO/GCC limitation is unchanged for local runs.
* Git auditability remains open: implementation files are untracked, and this document does not claim that
  Git history represents the complete implementation.

### 16.5 Verification executed on 2026-11-23

`gofmt -l cmd internal` produced no output; `go build ./...`, `go vet ./...`, `go test ./... -count=1` and
`go test -tags=integration ./... -count=1 -p 1` all passed (12/12 packages each); `git diff --check`
reported nothing. The integration database was PostgreSQL 18.7 with migration version 13; all test-owned
rows were removed, and no destructive database operation was performed. These are the earlier Phase 2.1
results; they do not verify Bundle 1. Race testing was not executed in that phase (§16.4).

### 16.6 D7 documentation-reconciliation checkpoint (2026-11-24, before execution)

**Historical checkpoint; superseded for local execution by §16.7.** No application test was run by the D7 documentation-only subtask. The new CI workflow defines:

- Go 1.27.4 empty `gofmt`, vet, build, unit tests, `go mod verify` and non-mutating `go mod tidy -diff`;
- PostgreSQL 18 plus RabbitMQ 4.4.1/4.3.8 integration, serialized with `-p 1`, followed by down-13/up proof;
- Ubuntu `CGO_ENABLED=1` race tests; and
- advisory golangci-lint v2.15.0 with `continue-on-error: true`.

Action commit SHAs were verified through the official GitHub API, and the lint configuration schema was validated
with golangci-lint v2.15.0. Those checks do not establish workflow success, lint cleanliness, broker compatibility,
race safety or migration down/up behavior. Operational outage, shutdown and DLQ drills are also pending; see
[the runbook](../operations/runbook.md).

### 16.7 Bundle 1 execution evidence (2026-11-24)

This same-date execution addendum records actual verification under the Owner-approved Bundle 1 order. It does not create a new architecture revision or approve D6/U25/production activation. Earlier §16.4/§16.6 pending statements describe the checkpoint before this execution, not the current local runtime.

| Check | Observed result |
| --- | --- |
| Go 1.27.4 formatting, build and vet | PASS; no formatting/build/vet diagnostics |
| Unit tests | PASS, 17/17 packages |
| PostgreSQL 18.7 + RabbitMQ 4.4.1 integration | PASS, 18/18 packages, no skipped tests |
| PostgreSQL 18.7 + RabbitMQ 4.3.8 integration | PASS, 18/18 packages, no skipped tests |
| Linux Go 1.27.4, CGO=1 unit race detector | PASS, 17/17 packages |
| Real blocked channel.open/close RPC watchdogs | PASS on both brokers; also PASS under the Linux race detector |
| Module integrity/tidiness | `go mod verify` PASS; `go mod tidy -diff` empty |
| golangci-lint v2.15.0 | Tool ran with Go 1.27.4; 32 advisory findings: cyclop 9, errcheck 16, gocognit 6, nestif 1 |
| Hosted GitHub Actions / fresh disposable down-13/up | Defined, not executed; no commit/push was authorized |
| PostgreSQL schema | Version 13, dirty=false; 26 migration-file hashes unchanged |

The real end-to-end test proves T1 -> N publication -> exact Worker leases -> 21 rejects -> delivery-limit DLQ with x-death -> lease expiry -> Recovery N+1 -> Publisher -> one successful N+1 HTTP attempt. A separate missing-DLX-route test proves at-most-once copy loss and database-only N+1 creation. Duplicate publication completes once; a matching `publishing` pointer delivered before T2 is held/requeued without ACK, reject or HTTP. A terminal Delivery's NULL next_attempt_at is decoded safely.

Generation replacement reuses numeric tag 1 while old Handle.Ack/Reject return ErrStaleHandle; the new pointer remains unsettled and requeues on shutdown. Real active-work shutdown commits/ACKs completed HTTP while requeuing prefetched work without NACK. AMQP setup, publishing, settlement and teardown now have independent connection deadline aborts; component failure joins other Worker components, and components/metrics/pool share the overall shutdown deadline. Fatal cleanup timeout is surfaced rather than hidden; a component that disregards cancellation cannot be guaranteed graceful completion.

Development dispatch is verified, not a production-ready ingestion service. The API remains health-only; D6/U25, SSRF/egress, HA/capacity/failover and production alerting remain open. Optional process metrics do not constitute a production monitoring deployment. See the operations runbook and final execution report for scope and remaining gates.

### 16.8 Phase 6 evidence (2026-11-24)

Phase 6 implements the management surface against the unchanged 13-table schema (version 13, dirty=false; no
migration file changed). The §16.7 sentence "the API remains health-only" and §16.4's blanket "D6/U25 do not exist"
bullet describe their own checkpoint; the current tree implements both the management API and the development
security runtime.

| Area | Implemented / observed |
| --- | --- |
| Management routes | `POST/GET /v1/organizations/{organization_id}/endpoints`, `GET/PATCH/DELETE …/{endpoint_id}`, `POST …/secret/rotate`, `GET/POST …/subscriptions`, `DELETE …/subscriptions/{subscription_id}`; RFC 9457 errors; 405 with `Allow`; `/v1/` catch-all 404 |
| Authentication | 43-char base64url bearer token, 8-char prefix lookup, Argon2id PHC verification, dummy verification for unknown prefixes, revoked/expired rejection, scopes (`endpoints:read`, `endpoints:write`, `endpoints:secrets:rotate`, `events:write` default) |
| Endpoint contract | Create/PATCH allowlists reject reserved fields (`timeout_ms`, `max_attempts`, `signing_scheme`, secret, ids, timestamps); header keys canonicalized; HTTPS-only URL; Contract A strong ETag with `If-Match` 428/400/412 semantics |
| Ceiling and policy | U37 default 100 via `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG`; U38 count-then-write under `FOR NO KEY UPDATE`; suspended organizations keep reads, rotation and safe metadata updates and may pause, soft-delete endpoints, deactivate subscriptions and revoke API keys, while create/activation/URL/header changes and subscription creation are denied; archived organizations lose all tenant management writes, including subscription deactivation, endpoint soft-delete and API-key creation/revocation |
| Secret rotation | Two-phase: validate + `If-Match`, wrap with no database lock, re-lock and re-check `If-Match`/status, then update; 412 discards the new envelope |
| Audit | `endpoint.created/updated/paused/activated/deleted/secret_rotated`, `subscription.*`, `apikey.created/revoked`; actor `api_key`; metadata limited to changed field names/name/event type/scopes |
| Operator CLI | `cmd/apikey create/list/revoke`; raw token printed once; `scripts/dev-keyring.sh ensure` for the development KEK |
| Verification | `gofmt`/`go vet`/`go build` clean; unit 25/25 test-bearing packages; integration 26/26 packages (PostgreSQL 18.7, RabbitMQ 4.4.1); whole-module Linux CGO race pass; provider-I/O lock-release, concurrent activation and subscription races covered; lint 17 advisory findings (one new cognitive-complexity finding in `PatchEndpoint`, the rest pre-existing) |
| Not verified / absent | Hosted CI, production provider/KMS/Vault, organization or key HTTP management, ingestion/fan-out, rate limiting, SSRF controls, HA/failover/capacity drills; Git history still does not contain the implementation |

Phase 6 evidence approved no D6/U25 proposal row at the time. The Owner subsequently approved all eleven rows on
2026-11-24 (Revision 20); the development implementation remains subject to the Phase 6.1 evidence in §16.9, and
production provider/readiness remains not implemented.

### 16.9 Phase 6.1 verification evidence (2026-11-24)

Phase 6.1 added tests and documentation only. It changed no application behavior, SQL, migration, runtime
configuration, `.env`, Compose resource or Git state. Statuses remain separate: D6/U25 architecture **OWNER
APPROVED**; development implementation **IMPLEMENTED**; production KMS/Vault custody and readiness **NOT
IMPLEMENTED / NOT READY**.

Executed on the existing development PostgreSQL 18.7 and RabbitMQ 4.4.1 environment, Go 1.27.4 with
`GOTOOLCHAIN=auto`:

| Check | Observed result |
| --- | --- |
| `gofmt -l .` | empty, exit 0 |
| `go vet ./...` and `go vet -tags=integration ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `go test ./... -count=1` | exit 0; 25 ok, 4 no test files |
| `go test -tags=integration ./... -count=1 -p 1 -v` | exit 0; 28 ok, 2 no test files, 0 FAIL, 2 SKIP (Windows-only POSIX-permission tests) |
| Focused new management tests | 11/11 top-level tests PASS (both-order soft-delete/subscription races, lock serialization, status-transition races, cap-1 creation, lock-order stress, archived/suspended policy, Cache-Control/no-leak) |
| `cmd/apikey` integration tests | PASS (raw key once, prefix+PHC only, no verifier/token output, policy/tenant/expiry/audit) |
| Linux `golang:1.27.4` container `go test -race ./... -count=1` | exit 0; 27 ok, 2 no test files, no data race |
| `go mod verify` / `go mod tidy -diff` / `git diff --check` | all verified / empty / exit 0 |

Schema remains version 13, dirty=false; container, volume, `.env` and Compose timestamps are unchanged; HEAD remains
`c6db32fd50ee8d7beb3108674b4cf46443d2aba4` with nothing staged. The management integration suite now has 23 top-level
tests (12 Phase 6 + 11 Phase 6.1). Non-critical finding: `accesskeys.List` returns the stored PHC verifier inside
`apikey.Record` to its caller; the CLI never prints it and no HTTP route exposes it. Hosted CI was not run and remains
NOT VERIFIED; lint was not rerun in Phase 6.1; production remains NOT READY. See the Phase 6.1 closure report for the
full command log and the documentation corrections.

### 16.10 Phase 7 evidence (2026-11-25)

Phase 7 implements the Owner-approved OD-4 destination policy, OD-5 ingestion body boundary, the U47/U41/U42/U46
parse and idempotency pipeline, and the authenticated ingestion route. **No migration, schema, migration version,
Compose file, `.env`, keyring, queue, container, volume or existing database row was changed**; schema remains
version 13, dirty=false, and HEAD remains `c6db32fd50ee8d7beb3108674b4cf46443d2aba4` with nothing staged.

| Check | Observed result |
| --- | --- |
| `gofmt -l .` | no output, exit 0 |
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `go test ./... -count=1` | exit 0; 31 packages `ok`, 2 `no test files` |
| `go test -tags integration ./... -count=1 -p 1` | exit 0; 31 packages `ok`, 2 `no test files`; `test/integration` 46.9 s; 2 Windows-only POSIX-permission skips, 0 failures |
| Linux `golang:1.27.4` container `go test -race ./... -count=1` | exit 0; 31 packages `ok`, 2 `no test files`; no data race |
| `go mod verify` | `all modules verified`, exit 0 |
| `go mod tidy -diff` | empty, exit 0 |
| `git diff --check` | exit 0; **scope is tracked changes only** (the pre-existing `.gitignore` modification). The implementation tree is untracked and is not covered by this check. |
| Hosted GitHub Actions | NOT RUN |
| golangci-lint v2.15.0 (`run ./...`) | executed after the final edits: exit 1 with 17 advisory findings — 6 `cyclop` and 6 `gocognit` complexity findings in pre-existing packages (`outbox`, `config`, `messaging`, `delivery`, `management`, `publisher`, `recovery`), 3 `gosec` G115 conversions in `internal/modules/apikey/phc.go`, and 2 `govet` `inline` hints in Phase 6 boundary tests. No finding references Phase 7 code; lint cleanliness is not claimed. |

**Destination policy (OD-4).** `internal/platform/destination` implements `ParseURL` (HTTPS only, host required,
no userinfo/fragment/zone, port 443 only, immediate literal check), an explicit IPv4/IPv6 prohibited-range table
combined with the standard `netip` classification methods, `ResolveAllowed` (every answer checked, mapped IPv6
unmapped, mixed sets rejected, first allowed address selected, DNS errors preserved as transient), a bounded 2 s
advisory `Preflight` for management create/PATCH, and an `http.Transport` that ignores proxy environment variables,
disables keep-alives, disables redirect following at the executor, dials only the validated literal, and verifies the
connected remote address. TLS SNI and certificate verification use the original hostname and `InsecureSkipVerify` is
never set. The executor maps both the stored-URL rejection and the dial-time `ErrProhibited` to the approved
permanent pre-HTTP reason `destination_disallowed` with error class `connect`; DNS failures remain retryable `dns`.
No new database error class or schema constraint was introduced.

Tests cover the full prefix table and classification cases, mapped IPv6, mixed answers, DNS rebinding (the second
lookup returns loopback and only the previously public address is dialed), per-attempt revalidation, non-443,
scheme/userinfo/fragment/zone rejection, proxy variables set while a direct verified TLS request still succeeds,
redirect non-following, a valid public HTTPS delivery through an injected resolver/listener fixture, the absence of
a production-configurable bypass, and the terminal disposition for a pre-existing private destination. The management
integration test `TestManagementRejectsProhibitedDestinationsIntegration` exercises the public API boundary for 15
prohibited forms and a rejected PATCH that leaves the stored URL unchanged.

**Pre-HTTP disposition matrix.** `TestWorkerPreHTTPDispositionMatrixIntegration` drives the real Worker, PostgreSQL
and broker through 14 cases, including the new *existing private destination terminalizes without a retry outbox
row*: HTTP 0, state `failed`, 0 attempt rows, `attempt_count` 0, `completed_at` set, `next_attempt_at` NULL, lease
cleared, generation unchanged, one pre-seeded outbox row, ACK 1, `hookflow_worker_destination_disallowed_total` 1 and
the static diagnostic *not permitted by policy*. The other cases retain their approved behavior: transient provider
pacing, missing/invalid/disallowed secret handling, signing construction failure without unsigned fallback, budget
refusal, event expiry, lease-loss expiry, unsigned delivery under the development gate only, and stale-generation ACK
without mutation. `TestPreHTTPPermanentFailuresTerminalizeIntegration` includes `destination_disallowed` in the
permanent set.

**Ingestion boundary (OD-5).** The handler reads at most `1 MiB + 1`; the extra byte returns
`400 payload_too_large` with `Connection: close`; `Content-Length` only produces an early refusal; truncated bodies
are never parsed or hashed; no decompression is offered; only canonical `jsonb` is stored, never a raw body. The
existing ingestion integration suite covers fresh claim, byte-stable replay, 409 conflict, in-place expired takeover,
single-winner concurrency, sweeper race, zero destinations, maximum fan-out, organization isolation, rollback with no
partial rows, body-limit rejection and the 202/409 error contract. There is no new `ingestion_e2e`,
`ingestion_concurrency_integration` or `ingestion_rollback_integration` file because the named coverage already
exists in `test/integration/ingestion_integration_test.go` and `ingestion_api_integration_test.go`; the genuinely
missing SSRF API evidence was added as `ingestion_ssrf_integration_test.go`.

**Residual risks and gates.** The 1 MiB cap is not an upstream DoS defense; network-level request limits and egress
restriction remain deployment responsibilities. The DNS preflight is advisory and delivery-time resolution uses the
platform resolver. There is no rate limiting, WAF, production HA, production KMS/Vault custody, hosted CI evidence or
production readiness. Lint remains advisory only: its 17 findings are pre-existing and none is in Phase 7 code, but
two functions this phase extended (`Executor.Execute` and `Runtime.Process`) were split into behavior-preserving
helpers to stay inside the configured complexity budgets. None of these test results establish production deployment.

### 16.11 Phase 7 final completion and conformance (2026-11-25, dated record)

This record supersedes the "pending" wording of §10, §11 item 32 and §16.10 **only for the items it names**. No
approval, contract, retry semantic, lock order, schema, migration, configuration key, `.env`, keyring entry or
dependency changed.

**Appendix I citation corrected.** RFC 8785 **Appendix I is "Development Portal"**: a pointer to external test-data
repositories, not a sample-value file published by the RFC. Earlier statements in this document and in
database-schema.md §4.13.5 that refer to "the Appendix I large sample-value file" describe an external corpus this
project never imported. The conformance inputs actually used are the RFC's own vectors:

| RFC 8785 material | Local evidence |
| --- | --- |
| §3.2.2 worked example (document) | `testdata/rfc8785/section-3.2.2-input.json` → `-canonical.json` byte comparison |
| §3.2.3 UTF-16 ordering vector | `testdata/rfc8785/utf16-order-input.json` → `-canonical.json` |
| §3.2.4 UTF-8 byte vector | `TestRFC8785Section324UTF8Bytes` (hex decoded from the RFC text) |
| Appendix B Table 1 | `testdata/rfc8785/appendix-b-numbers.tsv`, every row with a JSON representation |
| §3.2.2.3 NaN/Infinity MUST-error | `TestAppendixBNonFiniteRowsAreRejected` (non-JSON spellings and overflow literals) |
| §3.2.2.2 lone-surrogate MUST-error | `TestAppendStringRejectsInvalidUTF8` and the duplicate/escape tests |

Added in this round: escaped duplicate-name rejection (`"\u0061"` vs `a`, including nested and inside arrays),
canonicalization fixed-point over four documents, an explicit serialization-versus-input-policy test (Appendix B
values such as `9007199254740992` serialize correctly while U46 refuses them as ingestion input), the remaining §15.3.1
boundary literals (`1E30`, `-1e-400`, `5e-324`, `9.007199254740993e15`, `9007199254740991.0`, `-0.0`), and structural
rejections (two top-level values separated by whitespace, whitespace-only input, truncated object, bad escape, trailing
array comma).

**Independent ECMAScript comparison.** `v8_conformance_test.go` (build tag `v8conf`, excluded from `go test ./...`)
compares HookFlow against Node.js, using `JSON.stringify` for §3.2.2.2/§3.2.2.3 serialization and the canonicalizer
published verbatim in RFC 8785 Appendix A for whole documents. Inputs: the 24 Appendix B values with a JSON
representation, 2 000 finite IEEE 754 values from a fixed splitmix64 stream (seed `0x87855eed20261125`, documented for
reproducibility), 48 valid-Unicode strings (the §3.2.2 sample, all C0 controls, DEL, U+2028/U+2029, the §3.2.3 property
names, astral and combining characters), and three documents. Output is compared as exact text. A missing Node.js
runtime, missing script, missing fixture, non-finite reference value, or process failure **fails** the test; a silent
skip is impossible (verified locally by running the compiled test with Node.js removed from `PATH`: exit 1 with
`v8 conformance requires Node.js on PATH`). This comparison covers the serialization contract only. HookFlow's stricter
input policy (duplicate names, lone surrogates, safe-integer range) is deliberately outside it and covered by unit
tests, so no claim is made that HookFlow accepts every document ECMAScript would serialize.

**Dedicated CI job.** `.github/workflows/ci.yml` gains a `jcs-conformance` job with four individually reported steps:
fixture/runtime preflight (`node --version` plus `test -f` for every vector and the reference script), the unit and
fixture suite, the `v8conf` comparison, and a **60-second bounded** `FuzzCanonicalize` run whose JSON output is parsed
so that zero executed cases fail the job (Go itself only warns when `-fuzz` matches no target), with any crasher files
`cat`ed on failure. The general `go test ./...`, integration (both RabbitMQ versions), race and advisory lint jobs are
unchanged. Hosted execution for the pushed commit is the authoritative CI evidence and is identified in the Phase 7
closure report; this document does not assert its outcome.

**Verified versus not verified.** Verified: the vectors and behaviors listed above, locally and in the revised job
definition. **Not verified:** an exhaustive RFC conformance claim (selected published vectors are not the whole
specification), the external development-portal corpus (not imported), other third-party implementations (not used),
and any value beyond the 60-second fuzz budget. The fingerprint path still uses only `encoding/json/jsontext`; no
dependency was added.

**F6 — destination whitespace/control consistency.** The management contract (`hasSpacesOrControls`) and the
`chk_endpoints_url` CHECK already reject ASCII whitespace and control bytes, so a stored URL cannot reach the Worker
with them; `destination.ParseURL` now enforces the same rule as defense in depth and returns `ErrProhibited`, keeping
DNS/IP validation, port 443, TLS SNI, remote-address verification, and the terminal `destination_disallowed`
disposition unchanged. Focused tests cover leading/trailing/inner space, tab, newline, carriage return, NUL, DEL and
U+001F.

**F7 — Bearer challenge.** Every `401` now carries `WWW-Authenticate: Bearer`. The challenge text is identical for a
missing, malformed, unknown, revoked and expired key, so it reveals nothing about token validation; authorization and
scope behavior are unchanged. Tests assert the header at the authenticator boundary and on the rendered HTTP responses
for the unauthenticated and invalid-token paths.

**Lint.** Re-measured after the fixes: **12 advisory findings — 6 `cyclop` and 6 `gocognit`** in files this round did
not modify (`outbox`, `config`, `messaging`, `delivery`, `management/service.go`, `publisher`, `recovery`). The three
`gosec` G115 conversions in `internal/modules/apikey/phc.go` are fixed with explicit lower/upper bound checks at the
conversion site (no suppression), and the two `govet` `reflect.Ptr` hints are fixed. The lint job remains
`continue-on-error: true`: a green lint job therefore does **not** mean zero findings, and no complexity refactor of
approved transaction code was performed.

**R-1 and R-2 remain as recorded.** The composite Publisher/Worker lifecycle test and the
counter-budget-across-restarts invariant in `docs/operations/configuration.md` are unchanged by this round.

