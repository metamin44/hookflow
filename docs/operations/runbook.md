# Operations runbook

## Scope and safety

This runbook covers the Bundle 1 PostgreSQL/RabbitMQ dispatch runtime, the Phase 6 management API and the Phase 7 ingestion API. The API serves health endpoints, `POST /v1/events`, and authenticated `/v1` endpoint/subscription management; organization management is absent. Local execution on 2026-11-24 passed PostgreSQL 18.7/RabbitMQ 4.4.1 and 4.3.8 integration, Linux unit race detection, and real blocked-AMQP-RPC race tests; Phase 7 added destination-policy, ingestion and pre-HTTP disposition coverage (2026-11-25). Hosted PR CI run `45681759235` passed for pre-remediation SHA `00a5a82cbc638411278b49f3bf57c55cde5bf2d8`; the current remediation working tree has not run hosted CI. The remediation working tree passed a fresh disposable down-13/up migration drill locally; production HA/outage drills remain pending. golangci-lint v2.15.0 reports advisory findings; lint cleanliness is not claimed.

The database is authoritative. RabbitMQ carries durable, identifiers-only pointers; the DLQ is telemetry, not a work queue or replay source. Delivery is at-least-once, so destinations must deduplicate.

## 1. Topology and policy

The Publisher and Worker create or verify this topology at startup and after reconnect. Declaration/policy mismatches fail closed; an existing same-name policy is never overwritten.

| Object | Required declaration |
| --- | --- |
| Exchange `hookflow.dispatch` | Durable, topic, non-auto-delete, non-internal, no arguments |
| Work queue `hookflow.dispatch.q` | Durable quorum, non-auto-delete, non-exclusive, exactly `x-queue-type=quorum` |
| Work binding | `hookflow.dispatch` -> `hookflow.dispatch.q`, routing key `delivery` |
| Exchange `hookflow.dispatch.dlx` | Durable, direct, non-auto-delete, non-internal, no arguments |
| DLQ `hookflow.dispatch.dlq` | Durable quorum, non-auto-delete, non-exclusive, exactly `x-queue-type=quorum` |
| DLQ binding | `hookflow.dispatch.dlx` -> `hookflow.dispatch.dlq`, routing key `delivery.failed` |
| Policy `hookflow-dispatch-dlq` | Pattern `^hookflow\.dispatch\.q$`, apply-to `queues`, priority `0` |

The policy definition must contain exactly:

```text
dead-letter-exchange     = hookflow.dispatch.dlx
dead-letter-routing-key  = delivery.failed
dead-letter-strategy     = at-most-once
delivery-limit           = 20
```

There is no delayed-retry exchange, TTL retry scheduler, automatic DLQ consumer or replay tool. At-most-once dead-lettering can lose the DLQ copy; the PostgreSQL Delivery/Outbox state remains authoritative.

The AMQP body has exactly `schema_version`, `organization_id`, `delivery_id`, `dispatch_seq` and `outbox_id`, plus the fixed publishing properties and four application headers. It never contains the event payload or secret. RabbitMQ may append `x-acquired-count` and `x-delivery-count` on quorum redelivery; the Worker accepts those broker-owned counters but still rejects additional application headers. Management queue counters are sampled; use passive declarations or `rabbitmqctl` for immediate ready/consumer counts.

### Verify topology

Run from the repository root with the local environment exported:

```bash
docker compose ps
docker compose exec -T rabbitmq rabbitmq-diagnostics -q ping
docker compose exec -T rabbitmq rabbitmqctl version
docker compose exec -T rabbitmq rabbitmqctl list_exchanges -p / name type durable
docker compose exec -T rabbitmq rabbitmqctl list_queues -p / name type durable arguments messages_ready messages_unacknowledged
docker compose exec -T rabbitmq rabbitmqctl list_bindings -p / source_name destination_name routing_key
docker compose exec -T rabbitmq rabbitmqctl list_policies -p / name pattern apply-to definition priority
```

Confirm both queues are `quorum`, both exchanges are durable with the required types, and no competing policy controls any dead-letter key for `hookflow.dispatch.q`. Use the vhost from `HOOKFLOW_RABBITMQ_URI` if it is not `/`. Do not print the URI while checking it.

## 2. Management API operations

### API keys

Keys are created, listed and revoked only through the operator CLI; there is no HTTP key management and no organization CRUD route.

```bash
go run ./cmd/apikey create -organization <org-uuid> -name <label> \
  -scopes endpoints:write,endpoints:secrets:rotate [-expires 720h]
go run ./cmd/apikey list -organization <org-uuid>
go run ./cmd/apikey revoke -organization <org-uuid> -id <key-uuid>
```

`create` prints the raw token once; treat it as a secret and never paste command output into tickets or logs. The database stores an Argon2id PHC verifier, an 8-character prefix and scopes; there is no recovery path, so revoke and reissue a lost key. The CLI needs the PostgreSQL variables only.

Authentication behaviour: missing or malformed `Authorization` returns `401`; a valid key without the required scope returns `403 insufficient_scope`; unknown prefixes still perform a dummy verification (no prefix oracle); revoked and expired keys fail verification. Suspended organizations may list/get, patch name/description, pause, soft-delete endpoints, deactivate subscriptions, rotate secrets and revoke API keys, but are denied endpoint creation, activation, URL/header changes, subscription create/reactivate and API-key creation. Archived organizations may read under the existing policy but are denied every tenant management write, including endpoint changes/soft-delete, subscription create/reactivate/deactivate, rotation and API-key creation/revocation.

Scopes: `endpoints:read` (list/get endpoints and subscriptions), `endpoints:write` (endpoint and subscription writes; implies read; does not grant rotation), `endpoints:secrets:rotate` (rotation only), and `events:write` (`POST /v1/events`; default when no scopes are supplied). Unknown and wildcard values are ignored.

### Ingestion API (`POST /v1/events`)

The organization is derived from the API key; there is no organization path. Required inputs: one
`Idempotency-Key` header (1–200 UTF-8 bytes, no control characters) and a JSON object body whose `payload`
member is also an object. Document-level `{"event_type": "...", "payload": {...}}` members follow the approved
schema in overview §5.1/§5.5.

| Observation | Meaning | Action |
| --- | --- | --- |
| `202` with `destination_count` | Committed; deliveries created for every active matching subscription. `0` is normal for a non-matching event. | None. |
| Replay `202` + `HookFlow-Idempotent-Replay: true` | Same key and same fingerprint within 72 h; no new rows. | None. |
| `409 idempotency_key_conflict` | Same key, different fingerprint. | Use a new key for the new body. Do not retry blindly. |
| `400 payload_too_large` | Body exceeded the fixed 1 MiB cap; `Connection: close` is set. | Reduce the body; the cap is not configurable. |
| `400 invalid_json_document` / `duplicate_json_key` / `integer_out_of_safe_range` / `non_finite_number` | Rejected before the idempotency claim. | Fix the document; no state was written. |
| `403 organization_suspended` / `organization_archived` | Tenant cannot accept events. | Resolve tenant state. |

The `202` body is byte-identical across replay and is returned only after commit. JSONB normalizes
whitespace/formatting, so the delivered body is the parsed value, not the original bytes. API keys are tenant
scoped: a key for another organization cannot address this one.

### Destination policy (OD-4) and private endpoints

Endpoint create/PATCH validates the URL syntax and rejects literal private, loopback, link-local, multicast,
unspecified, documentation and otherwise reserved addresses with `400 invalid_url`; HTTPS on port 443 only, and
no userinfo, fragment or IPv6 zone. A hostname also gets a bounded 2 s DNS preflight: a prohibited answer is
`400 invalid_url`; a DNS failure is allowed because the delivery attempt enforces the policy again.

At delivery time, the Worker resolves every A/AAAA answer with the request context, rejects any prohibited
answer (including a mixed public/private set) and any zone, pins an allowed literal address for the dial,
verifies the connected remote address, keeps the hostname for TLS SNI/verification, ignores proxy environment
variables, does not follow redirects and revalidates on every retry. A disallowed destination is a permanent
pre-HTTP failure: terminal `failed`, no attempt row, no `attempt_count` increment, no retry outbox row, and the
`hookflow_worker_destination_disallowed_total` metric. DNS failures stay transient (`dns` classification) and
are retried. Endpoints that already pointed at private addresses fail closed without snapshot rewriting; there
is no backfill. This application policy is not a substitute for network-level egress restriction.

### Endpoint writes and ETags

Reads return a strong `ETag` (`"hf1-…"`). PATCH and secret rotation require `If-Match`; `DELETE` is unconditional:

- absent header → `428 precondition_required`;
- malformed tag → `400 invalid_if_match`;
- stale tag or weak tag → `412 precondition_failed` with the current `ETag` header;
- `*` → unconditional write.

A PATCH whose fields equal the stored state performs no `UPDATE`, writes no audit row, ignores the ceiling and returns the current `ETag`. Concurrent writers surface as `412`, not as lost updates; re-read and retry. Unknown or reserved fields (`id`, `organization_id`, `secret`, `secret_encrypted`, `signing_scheme`, `timeout_ms`, `max_attempts`, timestamps) are rejected with `400`, as is a body above 1 MiB (`413 payload_too_large`).

### Active-endpoint ceiling

`HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG` (default `100`) is enforced inside one transaction: lock the `organizations` row `FOR NO KEY UPDATE`, count live `active` endpoints, then write. Only create and `paused -> active` can hit the cap and return `409 active_endpoint_cap`. That lock mode does not block delivery fan-out foreign-key checks (`FOR KEY SHARE`). Lowering the limit never pauses or deletes existing endpoints.

### Secret rotation

Creation and rotation return a `whsec_` token exactly once; the stored value is an `hfse1.` envelope wrapped by the current file KEK. Rotation reads the endpoint and validates `If-Match`, wraps the new secret **without holding database locks**, then re-opens the transaction, re-checks organization status and `If-Match`, and only then updates `secret_encrypted` and writes `endpoint.secret_rotated`. A concurrent change discards the wrapped envelope and returns `412`; never bypass that check. Rotating does not alter delivery snapshots: in-flight deliveries keep the secret captured at fan-out.

The API and Worker must use the same keyring and an allowlist that contains every kid still referenced by stored envelopes. Back up the keyring or stored secrets become unrecoverable; keep old kids allowlisted until their envelopes are actually replaced or purged.

### Audit and troubleshooting

Every successful management write appends an `audit_logs` row with `actor_type='api_key'`, the key id, a target, the request host and a bounded user agent; `metadata` contains only changed field names, `event_type` or `name`. Header values and secrets are never recorded.

```sql
SELECT occurred_at, action, actor_id, target_type, target_id, metadata
FROM audit_logs
WHERE organization_id = '<org-uuid>'
ORDER BY occurred_at DESC
LIMIT 50;
```

Tenant isolation is path-based: a key may only address its own organization, and cross-organization or non-UUID identifiers return `404` (never `403`, to avoid existence disclosure). There is no admin endpoint to repair state; use the CLI and SQL under change control.

## 3. Readiness and reconnect

### Readiness signals

### Readiness signals

```bash
curl -fsS http://127.0.0.1:8080/health/live
curl -fsS http://127.0.0.1:8080/health/ready
curl -fsS http://127.0.0.1:9091/metrics
curl -fsS http://127.0.0.1:9092/metrics
```

- `/health/live` means the API process is serving. `/health/ready` means its PostgreSQL ping succeeded; it says nothing about RabbitMQ.
- `hookflow_publisher_ready=1` means a Publisher channel generation is established.
- `hookflow_worker_ready=1` means a Worker channel generation/consumer path is established.
- Publisher `hookflow_publisher_reconnect_total`, `hookflow_publisher_unknown_total`, `hookflow_publisher_returned_total` and Worker `hookflow_worker_reconnect_total`, `hookflow_worker_rejected_total`, `hookflow_worker_stale_handle_total` help identify broker churn.
- Worker logs Recovery pass reports (`selected`, `applied`, `deferred`, `ineligible`, `oldest_candidate_age`, `duration`) and invocation summaries. There are no built-in alert routes or sweep-age alerts yet.

The Publisher and Worker use application-managed reconnect; amqp091-go automatic recovery is disabled. After loss they discard the connection/channel generation, wait full-jitter exponential backoff between the configured minimum and maximum, dial again, and re-verify topology/policy. Publisher confirm/return mappings and Worker delivery handles are generation-owned and are never moved to a new channel.

Worker QoS is per-consumer with `global=false`. RabbitMQ 4.3.8 and 4.4.1 accepted `global=true` but behaved per-consumer/ignored the global setting; the implementation deliberately uses `false`.

### Broker outage

1. Check broker health, queue/policy declarations and process logs without exposing credentials.
2. Initial startup fails closed if the broker/topology/policy cannot be verified. After startup, processes remain alive and reconnect with bounded backoff.
3. Publisher treats timeout, nack, return, close, cancellation or ambiguous confirmation as failure/unknown. It never commits T2 `published` without a positive confirm and post-confirm return drain. A broker may have accepted an ambiguous publish, so duplicates remain possible.
4. Worker waits within its work budget for in-flight database dispositions before reconnecting. If a live pointer arrives while its matching Outbox row is still `publishing` (before Publisher T2), it is held without ACK/reject/HTTP and requeued by channel close, not misclassified as a stale generation. Closing a channel requeues unacknowledged deliveries; stale generation handles cannot acknowledge a replacement channel. Independent connection deadline watchers interrupt blocked AMQP RPCs/socket writes without waiting for application ownership mutexes.
5. Transient database/infrastructure failures use `reject(requeue=true)`, not normal `basic.nack`. Rejections increment quorum delivery count; repeated rejects can reach the delivery limit and dead-letter the pointer.
6. After recovery, confirm ready metrics return to `1`, queue depth stops growing, and Recovery continues from database state. Do not replay the DLQ.

### PostgreSQL outage

- API readiness returns `503`; liveness can remain `200`.
- Publisher claim/finalisation failures leave fenced state for retry/T3 cleanup. If failure accounting cannot commit, the expired-lease reaper remains the cleanup path.
- Worker cannot establish a durable disposition and rejects/requeues. Restore the database before forcing broker changes.
- Recovery logs database-class errors and tries again on the next fixed-delay cadence; errors do not permanently stop the scheduler.

### HTTP/network uncertainty

A timeout or lost completion response is not proof that the destination did not receive the request or that the database did not commit. The Worker records an uncertain/stale outcome as appropriate and may requeue. Expect duplicate delivery and use `X-HookFlow-Delivery-Id` plus `X-HookFlow-Dispatch-Sequence` for receiver-side deduplication.

## 4. Recovery and generation N+1

Every Worker runs Recovery immediately, then waits a full cadence after each completed invocation. Invocations do not overlap within one process; multiple replicas are safe through `FOR UPDATE SKIP LOCKED`, deterministic ordering, short transactions and fences.

Pass order is fixed:

1. **Expire:** unexpired-work filter is inverted: an expired Event terminalises a pending/retrying Delivery even when its Worker lease is still valid. No attempt, count, retry or new generation is created; the late Worker is fenced.
2. **Repair:** for due, unexpired, unleased work whose current generation has no outbox row, insert a same-generation dispatch intent with conflict protection.
3. **Re-arm:** for due, unexpired, unleased work whose current-generation outbox row is `failed`, reset that row to `pending` with zero attempts and gated `available_at`.
4. **Stale:** for eligible expired-lease work, or due unleased work with a sufficiently old current-generation `published` outbox row, clear the lease, advance `dispatch_seq` to N+1 and insert a new N+1 outbox row. The old N row is preserved.

Default budgets: cadence `60s`, total `45s`, pass windows `12/10/10/8s`, candidate caps `500/250/250/100`, stale-published threshold `5m`. Repeated `oldest_candidate_age` growth, exhausted budgets, or persistent zero progress requires investigation; no automated paging exists yet.

A DLQ arrival alone does **not** create N+1. Recovery evaluates persisted Delivery/Outbox predicates. Never copy a DLQ body into the work queue to simulate recovery.

## 5. DLQ inspection and manual drain

### Inspect without removing messages

```bash
docker compose exec -T rabbitmq rabbitmqctl list_queues -p / name messages_ready messages_unacknowledged
```

The management image exposes container port `15672`; local Compose publishes it on `127.0.0.1:${HOOKFLOW_RABBITMQ_MANAGEMENT_PORT:-15673}`. The following read requeues inspected messages (`ack_requeue_true`) and truncates bodies; it does not replay them. `RABBITMQ_ADMIN_USER` and `RABBITMQ_ADMIN_PASSWORD` below are operator-provided management credentials, not HookFlow process variables. Do not enable shell tracing or paste the response into public logs.

```bash
curl --fail --silent --show-error --config - \
  "http://127.0.0.1:${HOOKFLOW_RABBITMQ_MANAGEMENT_PORT:-15673}/api/queues/%2F/hookflow.dispatch.dlq/get" \
  --header 'Content-Type: application/json' \
  --data '{"count":10,"ackmode":"ack_requeue_true","encoding":"auto","truncate":1024}' <<EOF
user = "${RABBITMQ_ADMIN_USER}:${RABBITMQ_ADMIN_PASSWORD}"
EOF
```

For each sampled message record only the identifiers needed for investigation: `outbox_id`, `delivery_id`, `dispatch_seq`, routing key, and dead-letter reason/count in `x-death`. Compare them with the corresponding PostgreSQL `deliveries` and `outbox_messages` rows. The body should contain no payload or secret; stop and investigate if it does.

### Manual drain (destructive, no replay)

1. Obtain operator approval and pause the affected Publisher/Worker as appropriate so new arrivals do not race the drain.
2. Export the identifier/death-reason inventory to an access-controlled incident record. Do not export payloads, secrets or credentials.
3. Reconcile each Delivery in PostgreSQL. Recovery, not the DLQ, decides whether a new generation is eligible.
4. Drain only the selected DLQ messages through the management API with `ack_requeue_false`:

```bash
curl --fail --silent --show-error --config - \
  "http://127.0.0.1:${HOOKFLOW_RABBITMQ_MANAGEMENT_PORT:-15673}/api/queues/%2F/hookflow.dispatch.dlq/get" \
  --header 'Content-Type: application/json' \
  --data '{"count":1000,"ackmode":"ack_requeue_false","encoding":"auto","truncate":1024}' <<EOF
user = "${RABBITMQ_ADMIN_USER}:${RABBITMQ_ADMIN_PASSWORD}"
EOF
```

5. Confirm `messages_ready=0`, resume processes, and verify Publisher/Worker readiness and database Recovery progress.

Draining permanently removes the broker copy but changes no Delivery or Outbox state. **Never republish a DLQ message to `hookflow.dispatch`.** There is no approved replay operation.

## 6. Shutdown and restart

Send `SIGTERM` (or `SIGINT` locally) to the process; do not use `kill -9` during normal operation.

- API uses a 10-second graceful HTTP shutdown by default, then closes its database pool.
- Publisher uses a 10-second shutdown wait. Uncommitted claims remain fenced/expire and are handled by T3 or later claims.
- Worker uses a 35-second component wait; in-flight work has a budget of `LEASE_DURATION - SHUTDOWN_RESERVE` (20 seconds at defaults). Every processor goroutine is tracked. Settlement admission and force-stop share one mutex-protected state transition: once force-stop wins, no new pre-HTTP database or broker settlement can start. Admitted operations remain tracked until they actually return, and broker settlement is admitted atomically. The Worker never issues a normal nack. If the budget is exhausted, it force-cancels work and allows one further bounded cancellation drain (at most 10 seconds). Closing the channel requeues still-unacknowledged work. Cancellation also interrupts prefetch-semaphore admission so a full window cannot bypass the drain deadline. Broker settlement I/O runs outside the generation state lock so bounded channel/connection cleanup cannot be blocked by a stuck ACK/reject. PostgreSQL is closed only after `Consumer.Run` and every database-using component (Recovery and the idempotency sweep) have returned and the consumer is quiescent, including on timeout paths. If tracked work is still active at the deadline, shutdown reports failure and the process must terminate without explicitly closing PostgreSQL underneath that work; do not treat this as a clean shutdown.
- Orchestration termination grace must exceed the Worker's worst-case drain and cleanup needs. A forced kill can cause redelivery and duplicate HTTP; receivers must deduplicate.

After restart, verify migrations (`go run ./cmd/migrate version`), PostgreSQL readiness, RabbitMQ topology/policy, Publisher/Worker ready metrics and Recovery logs. A dirty migration state is preserved for inspection; do not force it.

## 7. Secret and diagnostic redaction

- Supply secrets through a secret manager or a mode-restricted local `.env`; never commit or echo them. Keep AMQP URIs, DSNs, authorization headers and curl config out of logs.
- Structured process logs go to stdout as JSON. Do not raise HTTP execution to debug in production without reviewing identifier exposure.
- Broker messages and metrics contain no payloads/secrets and no entity-ID labels. API, Publisher and Worker metrics use separate optional listeners; no public API metrics route exists. Keep metric listeners on loopback or behind an approved network ACL/firewall policy.
- Endpoint API responses redact historical credential-bearing and unsupported custom headers while ETags continue to cover full stored state. New Delivery snapshots omit those headers, and the Worker filters recognized legacy credential headers before transmission without rewriting snapshots. Historical rows are not cleaned up by this remediation; real historical credentials require a separately authorized inventory and rotation exercise.
- Stored HTTP diagnostics retain only `Content-Type`, `Retry-After` and `Date` response headers plus a bounded, heuristically redacted response snippet. Arbitrary binary/unstructured content is not guaranteed to be fully scrubbed; restrict database access accordingly.
- A nonempty `secret_encrypted` value that fails decryption returns secret-unavailable before HTTP. The Worker performs fenced, fresh-time decrypt-failure handling with deterministic pacing and no attempt/count increment. Empty-secret snapshots are sent unsigned only when `HOOKFLOW_DEPLOYMENT_MODE=development` and `HOOKFLOW_ALLOW_UNSIGNED_DELIVERY=true`; production mode is rejected at startup by this build, so do not rely on a mode flag alone as a security boundary.
- API keys and `whsec_` endpoint secrets are credentials. Never log `Authorization` headers, key material, keyring contents or the `api_key:` CLI output. `cmd/api` logs request method, path and error type only.

## 8. Production blockers

Production activation is **not approved**. Resolve and verify all of the following first:

- **D6/U25:** the architecture (D6-A…D6-G, U25-A…U25-D) is **OWNER APPROVED** (2026-11-24) and the development runtime is **IMPLEMENTED**: `hfse1.` envelope under a local file KEK, HMAC-SHA256 Standard Webhooks v1 signing, rotation behind a provider boundary, and the unsigned-empty-secret gate. **Production custody and production readiness are NOT IMPLEMENTED / NOT READY**: there is no KMS/Vault custody or provider, no automated KEK rewrap tooling, and no signing-key rotation beyond kid allowlisting. `production` fails closed at startup because only the development file provider exists. Production activation requires separate Owner authorization and implementation of the approved production custody/rotation shape. See `docs/architecture/d6-u25-decision-proposal.md` Rev 3.
- **KEK retirement:** operationally blocked under the Owner-approved D6-D rule; production retirement/purge tooling is not implemented. `endpoints` rows are soft-deleted by the management API and outside the retention table; their `secret_encrypted` is not purged, and no endpoint purge exists. No Events/Deliveries/Attempts purge exists either. Every retained endpoint envelope (including a soft-deleted row) and every immutable delivery snapshot counts as a KEK reference; 90/90/72 are horizons, not a key-retirement calendar. Do not destroy an old KEK version until dependent ciphertext is actually purged and a documented reference audit reports zero (see `docs/architecture/d6-u25-decision-proposal.md` §4.4).
- **API/ingestion:** management routes, API-key authentication and `POST /v1/events` ingestion exist; organization creation and API-key HTTP management remain absent. There is no rate limiting, WAF or public-internet hardening for either API. The fixed 1 MiB body cap is not a DoS defense; enforce request limits upstream.
- **HA:** no approved production PostgreSQL/RabbitMQ HA topology, deployment manifests, failover drill or capacity benchmark. The shared queue has no tenant sharding/fairness guarantee.
- **SSRF/egress:** the development HTTP executor enforces the OD-4 destination policy (HTTPS/443, explicit + classified address blocking, pinned dial, connected-remote verification, TLS verification, no proxy, no redirects, per-attempt revalidation). Network-level egress control and an egress allowlist are still required defense in depth for production; DNS preflight is advisory and delivery-time resolution uses the platform resolver.
- **Observability:** process-local optional metrics exist, but no production scrape/alert topology, DLQ-growth alert or Recovery sweep-age alert is implemented. Management requests are logged without an access-audit pipeline beyond `audit_logs` writes.
- **Verification:** local PostgreSQL 18.7 integration, both approved RabbitMQ versions, generation/tag safety, duplicate/pre-T2 pointer handling, DLQ loss -> Recovery N+1, active-work shutdown and Linux race detection passed. Phase 6 added management authentication, lifecycle, ETag, ceiling, rotation, provider-I/O lock-release and subscription concurrency integration tests. Hosted PR CI run `45681759235` passed on pre-remediation SHA `00a5a82cbc638411278b49f3bf57c55cde5bf2d8`; the current remediation working tree still requires a new hosted run and independent review. Production failover/capacity drills are not covered by these development tests. Advisory lint still reports findings; lint cleanliness is not claimed.
