# Configuration reference

HookFlow reads process configuration from the environment. `.env.example` contains development PostgreSQL/RabbitMQ examples and all approved Publisher, Worker, Recovery, management-API and delivery-security defaults; required connection settings still have no application default. Export `.env` before running a binary; the binaries do not load that file themselves.

## Loading rules

- All six `HOOKFLOW_DB_*` values and both RabbitMQ connection values are required. Missing or empty values fail startup.
- `cmd/api` and `cmd/worker` also require the delivery-security block (`HOOKFLOW_DEPLOYMENT_MODE`, `HOOKFLOW_SECRET_*`); it fails closed when unset or contradictory.
- Publisher, Recovery, delivery-Worker and RabbitMQ timing/count values use the defaults below when **unset**. A present empty or whitespace-only value is invalid.
- API and metrics addresses treat unset, empty or whitespace-only values as their documented default/disabled state.
- The endpoint ceiling uses its default only when unset; a present empty or whitespace-only value is invalid. The idempotency sweep values use their defaults when unset, empty or whitespace-only.
- Durations use Go duration syntax (`500ms`, `30s`, `5m`, `12h`). Counts are positive base-10 integers.
- Validation errors name variables but must not include credential values. Keep URIs and passwords out of command traces, logs and tickets.

## PostgreSQL — all processes and `cmd/migrate`

| Variable | Default | Required | Validation / invariant |
| --- | --- | --- | --- |
| `HOOKFLOW_DB_NAME` | None | Yes | Nonempty database name. |
| `HOOKFLOW_DB_USER` | None | Yes | Nonempty login role. |
| `HOOKFLOW_DB_PASSWORD` | None | Yes | Nonempty secret; supplied separately to pgx, never embedded in the constructed connection URL. |
| `HOOKFLOW_DB_HOST` | None | Yes | Nonempty host; Compose development example is `127.0.0.1`. |
| `HOOKFLOW_DB_PORT` | None | Yes | Decimal integer `1..65535`; Compose development example is `5433`. |
| `HOOKFLOW_DB_SSLMODE` | None | Yes | One of `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full`. Use TLS verification in production. |

`cmd/migrate` uses only these settings. API, Publisher and Worker also require them before opening a pool.

## API

| Variable | Applies to | Default | Validation / invariant |
| --- | --- | --- | --- |
| `HOOKFLOW_API_ADDR` | `cmd/api` | `:8080` | Listen `host:port`; port `1..65535`; host must not contain whitespace or `/`. An empty host binds all interfaces. |
| `HOOKFLOW_MAX_ACTIVE_ENDPOINTS_PER_ORG` | `cmd/api` | `100` when unset | Present value must be a positive integer; empty or whitespace-only is invalid. Per-organization ceiling for live `active` endpoints (U37), enforced under the organization row lock (U38). Lowering it later does not pause or delete existing endpoints. |

Routes: `GET /health/live`, `GET /health/ready`, the Phase 7 ingestion route `POST /v1/events`, the
Phase 6 management API under `/v1/organizations/{organization_id}/endpoints` (create/list, get/PATCH/DELETE,
secret rotation, subscriptions), and the Phase 8A organization subscription at
`/v1/organizations/{organization_id}/subscription` with append-only history at `…/subscriptions`. Every `/v1/`
route except ingestion requires an API key; `POST /v1/events` requires `events:write`, and organization
subscription routes require `billing:read` or `billing:write`. Unknown routes return RFC 9457 `404`, wrong methods
`405` with `Allow`.
Readiness pings PostgreSQL; it does not imply RabbitMQ or delivery readiness.

`cmd/api` also loads the delivery-security block below because secret rotation wraps envelopes with the same KEK
provider, and it fails startup when that block is missing or contradictory.

### Custom endpoint-header policy

Endpoint create and PATCH accept only the following explicit, case-insensitive allowlist:

- `X-Tenant`
- `X-Tenant-Id`
- `X-Trace-Id`
- `X-Event-Version`
- `X-Token-Count`
- `X-Tokenizer-Version`

Names must still be RFC 7230 tokens with valid bounded values. Unsupported names, application-managed
`webhook-*`, `X-HookFlow-*`, content, transport, routing and signature headers are rejected with
`400 invalid_headers`; rejected values are never echoed or logged. The same policy is revalidated at the
persistence boundary.

A defense-in-depth credential classifier normalizes case and hyphen/underscore/dot segment boundaries. It
recognizes authorization, proxy-authorization, cookie/set-cookie, `X-Auth`, `X-Authentication`, `X-Bearer`,
`X-JWT`, API/access/auth/private/secret/signing/encryption/HMAC keys, auth/access/refresh/ID/session/security
tokens, client secrets, password/passwd/pwd variants and credentials while allowing benign metadata such as `X-Cookie-Count`, `X-Password-Expiry`,
`X-Credential-Type` and `X-Token-Count`. The allowlist remains authoritative.

Historical endpoint rows are not rewritten. GET/LIST/create/PATCH responses expose only safe allowlisted
values; recognized credential-bearing and unsupported historical values are omitted. Contract A ETags still
cover the full unredacted stored state. Historical case-insensitive duplicate names use deterministic raw-name
ordering for ETag canonicalization; normal canonical tags are unchanged. New Delivery snapshots copy only safe allowlisted endpoint headers.
Existing immutable Delivery snapshots are also not rewritten, but the Worker filters recognized
credential-bearing custom headers before every transmission and preserves application-generated identity and
signature headers. Filtering emits only the fixed-cardinality
`hookflow_worker_legacy_credential_headers_filtered_total` counter.

Name validation is not a content scanner: a secret deliberately placed in the value of an allowed benign
header cannot be detected reliably. If real credentials may have been stored historically, a separately
authorized inventory and external credential-rotation exercise is still required. This remediation does not
prove that historical credentials were never exposed.

## Ingestion boundary (OD-5, fixed — not configuration)

`POST /v1/events` accepts a JSON object document with exactly one `Idempotency-Key` header (1–200 UTF-8 bytes,
no control characters) and a `payload` object member. The request body cap is exactly **1 MiB (1,048,576 bytes)**,
compiled into the API. There is no environment variable, startup flag or production bypass for it.

- A `Content-Length` above the cap is refused early; otherwise the handler reads at most cap+1 bytes with
  `io.LimitReader`. The presence of the extra byte returns `400 payload_too_large` and sets `Connection: close`.
  A truncated document is never parsed, hashed or stored.
- Requests are not decompressed, so `Content-Encoding` offers no larger allowance.
- Accepted payloads are stored only as canonical PostgreSQL `jsonb` in `events.payload`; there is no raw-body
  column or table and no response-body storage. JSONB formatting is normalized, so the delivered body is the
  parsed value, not the original byte spelling.
- `202` is returned only after the transaction commits. The body is `event_id`, `destination_count`,
  `received_at`; replay is byte-identical and adds `HookFlow-Idempotent-Replay: true`. A same-key different-body
  request returns `409 idempotency_key_conflict` with a static RFC 9457 detail.
- Idempotency retention is exactly 72 hours; after expiry the key may be reused in place. Event retention
  remains 90 days. The sweeper only deletes expired `idempotency_records` rows.

The residual risk is upstream ingress: a deployment must enforce its own request-size and connection limits and
must not rely on this cap as a denial-of-service defense. This 400 behavior is specific to ingestion; management
request bodies keep their separate `413 payload_too_large` limit.

## RabbitMQ — Publisher and Worker

| Variable | Default | Required | Validation / invariant |
| --- | --- | --- | --- |
| `HOOKFLOW_RABBITMQ_URI` | None | Yes | Absolute `amqp` or `amqps` URI with host and nonempty username/password. No fragment and no `heartbeat` or `connection_timeout` query parameters. The vhost is derived from the once-decoded URI path after removing its URI separator, matching amqp091-go; `/production` means `production`, `/%2F` means `/`, and `/%25` means `%`. An absent or root-only path means `/`. |
| `HOOKFLOW_RABBITMQ_MANAGEMENT_URL` | None | Yes | Absolute HTTP(S) management base URL with host; no user information, query or fragment. Used to create/verify the DLQ policy and management-visible topology. |
| `HOOKFLOW_RABBITMQ_HEARTBEAT` | `10s` | No | Positive duration. |
| `HOOKFLOW_RABBITMQ_CONNECT_TIMEOUT` | `5s` | No | Positive duration; bounds TCP dial and AMQP handshake. |
| `HOOKFLOW_RABBITMQ_RECONNECT_MIN_BACKOFF` | `500ms` | No | Positive duration; lower bound for full-jitter reconnect delay. |
| `HOOKFLOW_RABBITMQ_RECONNECT_MAX_BACKOFF` | `10s` | No | Positive duration; must be greater than or equal to the minimum. |

There is no independent vhost variable. URL-encode credentials in the URI; for the default vhost use `%2F`. The management URL must not contain credentials. The AMQP credentials are reused for management API basic authentication.

### RabbitMQ local Compose service

These variables are consumed by `compose.yaml`, not by the Go configuration loaders. They must agree with the process URI/URL above.

| Variable | Default | Required | Validation / invariant |
| --- | --- | --- | --- |
| `HOOKFLOW_RABBITMQ_USER` | None | Yes for local Compose | Nonempty `RABBITMQ_DEFAULT_USER`; use the same URL-decoded username in `HOOKFLOW_RABBITMQ_URI`. |
| `HOOKFLOW_RABBITMQ_PASSWORD` | None | Yes for local Compose | Nonempty `RABBITMQ_DEFAULT_PASS`; use the same URL-encoded password in `HOOKFLOW_RABBITMQ_URI`. |
| `HOOKFLOW_RABBITMQ_AMQP_PORT` | `5673` | No | Host port published on `127.0.0.1`; use a free port `1..65535` and the same port in the AMQP URI. |
| `HOOKFLOW_RABBITMQ_MANAGEMENT_PORT` | `15673` | No | Host port published on `127.0.0.1`; use a free port `1..65535` and the same port in the management URL. |

Library recovery is disabled. Each process owns reconnect, topology/policy re-verification and channel-generation disposal.

## Publisher

| Variable | Default | Validation / invariant |
| --- | --- | --- |
| `HOOKFLOW_PUBLISHER_BATCH_SIZE` | `200` | Positive integer; T1 claim batch size. |
| `HOOKFLOW_PUBLISHER_MAX_CLAIMS` | `5` | Positive integer; maximum claim/attempt count for an outbox row, independent of batch size. |
| `HOOKFLOW_PUBLISHER_LEASE_DURATION` | `30s` | Positive duration; T1 lease/fence window. |
| `HOOKFLOW_PUBLISHER_POLL_INTERVAL` | `500ms` | Positive duration; fixed delay after an idle/non-full tick. Full batches may trigger another bounded round immediately. |
| `HOOKFLOW_PUBLISHER_REAP_INTERVAL` | `10s` | Positive duration; T3 ticker; must be shorter than the Publisher lease. |
| `HOOKFLOW_PUBLISHER_BACKOFF_BASE` | `2s` | Positive duration; base for capped exponential backoff with full jitter. |
| `HOOKFLOW_PUBLISHER_BACKOFF_CAP` | `60s` | Positive duration; backoff upper bound. |
| `HOOKFLOW_PUBLISHER_BACKOFF_MIN` | `100ms` | Positive duration; minimum backoff/full-jitter lower bound. |
| `HOOKFLOW_PUBLISHER_CONFIRM_TIMEOUT` | `5s` | Positive duration; must be at most one sixth of the lease duration. |

Cross-field invariants:

- `BACKOFF_MIN <= BACKOFF_BASE <= BACKOFF_CAP`.
- `CONFIRM_TIMEOUT <= LEASE_DURATION / 6`.
- `REAP_INTERVAL < LEASE_DURATION`.
- The V1 Publisher is serialized with one in-flight publish (`N=1`). A positive confirm plus the bounded post-confirm return drain is required before T2 `published`; return, nack, timeout, close, cancellation and unknown outcomes fail closed and invalidate the generation.
- At most four full claim rounds run in one poll tick (800 rows at defaults).

## Delivery Worker

| Variable | Default | Validation / invariant |
| --- | --- | --- |
| `HOOKFLOW_WORKER_LEASE_DURATION` | `30s` | Positive duration; fixed dispatch lease. |
| `HOOKFLOW_WORKER_HTTP_DEADLINE` | `10s` | Positive duration; end-to-end HTTP request deadline. |
| `HOOKFLOW_WORKER_HANDSHAKE_BUDGET` | `2s` | Positive duration; reserved for connection/TLS/header setup. |
| `HOOKFLOW_WORKER_POST_PROCESS_BUDGET` | `3s` | Positive duration; reserved for durable completion. |
| `HOOKFLOW_WORKER_SHUTDOWN_RESERVE` | `10s` | Positive duration; reserved during graceful shutdown. |
| `HOOKFLOW_WORKER_SECRET_RESOLUTION_BUDGET` | `2s` | Positive duration; bounds KEK resolution before HTTP and is included in the pre-resolution lease requirement. |
| `HOOKFLOW_WORKER_CONCURRENCY` | `24` | Positive integer no greater than `65535`; maximum in-flight pointer processing. |
| `HOOKFLOW_WORKER_PREFETCH` | `24` | Positive integer no greater than `65535`; per-consumer AMQP prefetch. |
| `HOOKFLOW_WORKER_POOL_SIZE` | `30` | Positive integer no greater than `math.MaxInt32`; pgx pool maximum for the Worker. |
| `HOOKFLOW_WORKER_MAX_ATTEMPTS` | `8` | Integer `1..8`; selects the prefix of the fixed retry ladder. |
| `HOOKFLOW_WORKER_RETRY_HORIZON` | `12h` | Positive duration no greater than `12h`; caps scheduled retry delay. |
| `HOOKFLOW_WORKER_DECRYPT_FAILURE_PACING` | `30s` | Positive duration; base pacing for durable secret-unavailable handling, with deterministic `0..20%` added jitter. |
| `HOOKFLOW_WORKER_RESPONSE_BODY_LIMIT` | `65536` | Positive integer bytes; at most this many response-body bytes are retained for the diagnostic snippet. |

Cross-field invariants:

- `PREFETCH == CONCURRENCY`.
- `POOL_SIZE >= CONCURRENCY`.
- `LEASE_DURATION > HTTP_DEADLINE + HANDSHAKE_BUDGET + POST_PROCESS_BUDGET + SHUTDOWN_RESERVE + SECRET_RESOLUTION_BUDGET` (default `30s > 10s + 2s + 3s + 10s + 2s = 27s`). This five-component sum is the minimum remaining lease required before secret resolution and HTTP; the post-resolution executor check remains the four-component `25s` sum, preserving the approved 3-second slack and no-HTTP-if-insufficient-lease behavior.
- QoS uses `global=false`. RabbitMQ 4.3.8/4.4.1 accepted `global=true` but behaved per-consumer/ignored the global setting; HookFlow does not rely on global QoS.
- Retry ladder bases are `10s`, `30s`, `2m`, `5m`, `15m`, `1h`, `4h`, `12h`, with deterministic jitter from half the base through the base. A valid `429 Retry-After` overrides the ladder, capped at one hour and then by the configured horizon.
- A nonempty `secret_encrypted` value never reaches HTTP unless it decrypts under an allowlisted KEK and kid. A decrypt failure is persisted with fresh database time, fence/expiry checks and pacing; it creates no delivery-attempt row, does not increment `attempt_count` and never falls back to unsigned. Empty-secret snapshots execute unsigned HTTP only when `HOOKFLOW_DEPLOYMENT_MODE=development` **and** `HOOKFLOW_ALLOW_UNSIGNED_DELIVERY=true`; otherwise they are dispositioned without HTTP. Signed requests carry `webhook-id`, `webhook-timestamp` and `webhook-signature` (`v1`, HMAC-SHA256 over `id.timestamp.body`).

## Counter-budget changes across restarts

**Do not decrease `HOOKFLOW_PUBLISHER_MAX_CLAIMS` or `HOOKFLOW_WORKER_MAX_ATTEMPTS` across restarts without first assessing persisted live records and performing an explicitly planned reconciliation.**

This is an operational invariant, not a change to the approved retry architecture. Both budgets are compared against counters already persisted in live records, not against a per-process allowance:

- Publisher T1 selects only `pending` rows that are due **and** whose persisted `outbox_messages.attempts` is below the configured `HOOKFLOW_PUBLISHER_MAX_CLAIMS`. A decrease does not rewrite those counters, so a live row already at or above the reduced limit is never claimed again by the normal loop. Publisher lease expiry (T3) and Recovery repair/re-arm are bounded and conditional: T3 rewrites only expired `publishing` rows, repair skips a delivery whose current-generation outbox row exists, and re-arm selects only a `failed` current-generation row. A pending row that merely exceeds the reduced limit is not made claimable by those passes, and lease reaping/re-arm does not necessarily restore claimability.
- Worker pointer acquisition requires `deliveries.attempt_count` to be below `HOOKFLOW_WORKER_MAX_ATTEMPTS`. If a due, published pointer arrives for a delivery whose persisted count already equals or exceeds a reduced limit, acquisition classifies it as obsolete and ACKs it from durable state without HTTP and without a terminal delivery transition. The retry policy also uses the configured maximum, so a lower value can stop scheduling retries for existing work earlier than its recorded history would otherwise allow.
- A stable configuration across restarts does not create this condition. It arises only when the configured budget is lower than counters already persisted in live records. Terminal records (`succeeded`, `failed`, `expired`) that happen to carry counters equal to a limit are not live stranded work and must not be treated as such merely because the numbers match.

No automated reconciliation tool is provided. Decreasing either budget is an operational change that requires an explicit assessment of the live `outbox_messages.attempts` and `deliveries.attempt_count` values and a planned reconciliation.

## Delivery security — `cmd/api` and `cmd/worker`

These values are non-secret selectors and paths; the keyring file itself holds KEK material and must stay outside the repository with mode-restricted access. This build implements only the development local-file provider, so production activation fails closed at startup.

| Variable | Default | Required | Validation / invariant |
| --- | --- | --- | --- |
| `HOOKFLOW_DEPLOYMENT_MODE` | None | Yes | `development` or `production`. `production` is rejected by this build because no production KEK provider exists. |
| `HOOKFLOW_ALLOW_UNSIGNED_DELIVERY` | `false` | No | Boolean. `true` is accepted only in `development`; it permits unsigned HTTP solely for snapshots whose stored secret is empty. |
| `HOOKFLOW_SECRET_PROVIDER` | None | Yes | Implemented provider selector; `file` is the only value in this build and must appear in the provider allowlist. |
| `HOOKFLOW_SECRET_KEYRING_PATH` | None | Yes | Absolute native path to the local keyring, created by `scripts/dev-keyring.sh ensure`. Missing, unreadable or malformed keyrings fail startup. |
| `HOOKFLOW_SECRET_CURRENT_KID` | None | Yes | Kid used for new wraps; must be in `HOOKFLOW_SECRET_ALLOWED_KIDS`. |
| `HOOKFLOW_SECRET_ALLOWED_KIDS` | None | Yes | Comma-separated kid allowlist; empty or blank entries are invalid. Old kids stay allowed while their envelopes exist. |
| `HOOKFLOW_SECRET_ALLOWED_PROVIDERS` | None | Yes | Comma-separated provider allowlist; every entry must be implemented, and it must contain the selected provider. |

Startup fails closed when the mode is absent or unknown, when unsigned delivery is requested outside development, when the provider is unimplemented or not allowlisted, or when the keyring path, current kid or allowlists are empty or contradictory. No KEK material is read from environment variables.

## Recovery — Worker-hosted

| Variable | Default | Validation / invariant |
| --- | --- | --- |
| `HOOKFLOW_RECOVERY_CADENCE` | `60s` | Positive duration; fixed delay after each completed invocation; must exceed total duration. |
| `HOOKFLOW_RECOVERY_TOTAL_DURATION` | `45s` | Positive duration; total invocation deadline. |
| `HOOKFLOW_RECOVERY_EXPIRE_MAX_CANDIDATES` | `500` | Positive integer; expire-pass candidate cap. |
| `HOOKFLOW_RECOVERY_EXPIRE_MAX_DURATION` | `12s` | Positive duration; expire-pass reserved window. |
| `HOOKFLOW_RECOVERY_REPAIR_MAX_CANDIDATES` | `250` | Positive integer; repair-pass candidate cap. |
| `HOOKFLOW_RECOVERY_REPAIR_MAX_DURATION` | `10s` | Positive duration; repair-pass reserved window. |
| `HOOKFLOW_RECOVERY_REARM_MAX_CANDIDATES` | `250` | Positive integer; re-arm-pass candidate cap. |
| `HOOKFLOW_RECOVERY_REARM_MAX_DURATION` | `10s` | Positive duration; re-arm-pass reserved window. |
| `HOOKFLOW_RECOVERY_STALE_MAX_CANDIDATES` | `100` | Positive integer; stale-pass candidate cap. |
| `HOOKFLOW_RECOVERY_STALE_MAX_DURATION` | `8s` | Positive duration; stale-pass reserved window. |
| `HOOKFLOW_RECOVERY_STALE_PUBLISHED_AFTER` | `5m` | Positive duration; minimum age for the unleased, current-generation published-outbox stale branch. |

Cross-field invariants:

- All four pass durations must sum to at most `TOTAL_DURATION` (default `40s <= 45s`), with overflow-safe validation.
- `CADENCE > TOTAL_DURATION`, preventing overlap within one Worker process. Every Worker replica may run the scheduler; `SKIP LOCKED` and fences make concurrent candidates safe.
- Pass order is expire -> repair -> re-arm -> stale. Expiry wins over an active Worker lease.
- For the unleased stale-published branch, operationally keep `STALE_PUBLISHED_AFTER` greater than the Worker lease plus one Recovery cadence. This is a safety recommendation, not an additional loader check.

## Idempotency cleanup — Worker-hosted

| Variable | Default | Validation / invariant |
| --- | --- | --- |
| `HOOKFLOW_IDEMPOTENCY_SWEEP_BATCH_SIZE` | `500` | Positive integer; maximum rows deleted per batch transaction. |
| `HOOKFLOW_IDEMPOTENCY_SWEEP_INTERVAL` | `1m` | Positive duration; ticker interval. |
| `HOOKFLOW_IDEMPOTENCY_SWEEP_MAX_BATCHES_PER_TICK` | `5` | Positive integer; maximum independently committed batches per tick. |

Cleanup affects expired `idempotency_records` only; it does not delete event/delivery history and is not required for key-expiry correctness.

## Metrics — API, Publisher and Worker

| Variable | Default | Validation / invariant |
| --- | --- | --- |
| `HOOKFLOW_METRICS_ADDR` | Empty (disabled) | Optional explicit `host:port`; port `1..65535`; empty/wildcard hosts, unspecified IPv4/IPv6 (including mapped forms), whitespace and `/` are rejected. Each process needs its own port. |

The endpoint serves Prometheus text at `/metrics`, accepts GET only, and uses fixed low-cardinality metric names.
`cmd/api`, `cmd/outbox-publisher` and `cmd/worker` all support the optional separate listener; no metrics route is
added to the public API mux. The API registry collects the ingestion inexact-number counter. Loopback binding
is recommended. Any explicit non-loopback bind requires an approved scrape topology plus network ACL/firewall
restrictions; a private network is not automatically trusted. The shared metrics server also verifies the actual
bound TCP address and refuses an unspecified/wildcard address even if a hostname resolves to one.

## Management API authentication

API keys are created, listed and revoked with the operator CLI, not over HTTP:

```bash
go run ./cmd/apikey create -organization <uuid> -name <name> \
  -scopes endpoints:write,endpoints:secrets:rotate [-expires 720h]
go run ./cmd/apikey list -organization <uuid>
go run ./cmd/apikey revoke -organization <uuid> -id <uuid>
```

`create` prints the raw token exactly once (`api_key: …`, 43-character unpadded base64url); it is stored only as an
Argon2id PHC verifier, so a lost token must be revoked and replaced. Send it as `Authorization: Bearer <token>`.

| Scope | Grants |
| --- | --- |
| `events:write` | `POST /v1/events` ingestion for the key's own organization. Granted by default when `-scopes` is empty; a key with no explicit scopes is ingestion-only. |
| `endpoints:read` | List/get endpoints and subscriptions. |
| `endpoints:write` | Endpoint create/list/get/patch/delete and subscription create/reactivate/deactivate; implies `endpoints:read`. It does **not** grant secret rotation. |
| `endpoints:secrets:rotate` | `POST …/secret/rotate` only; it does not imply metadata read/write or any other write. |
| `billing:read` | `GET …/subscription` and `GET …/subscriptions` for the key's own organization. |
| `billing:write` | `PUT`/`DELETE …/subscription`; implies `billing:read`. |

Unknown or wildcard scope strings are ignored. A key is valid until revoked or `expires_at`.

Organization policy (all tenant writes are denied for archived organizations):

- **Suspended:** allowed — reads, secret rotation, `name`/`description` updates, pausing, endpoint soft-delete,
  subscription deactivation, organization-subscription cancellation, and API-key revocation. Denied
  (`403 organization_suspended`) — endpoint creation, `url`/`http_headers` changes, activation, subscription
  create/reactivate, organization-subscription create/replace, and API-key creation.
- **Archived:** allowed — reads under the existing policy. Denied (`403 organization_archived`) — every tenant
  management write, including endpoint changes and soft-delete, subscription create/reactivate/deactivate,
  organization-subscription create/replace/cancellation, secret rotation, and API-key creation/revocation.

Cross-organization paths return `404`.

### Management contract summary

- PATCH and secret rotation require `If-Match` with the strong ETag returned by create and GET (`428` when absent, `412`
  with the current ETag for a stale or weak tag, `400` for a malformed tag). `*` is accepted as an unconditional request.
  DELETE is unconditional after tenant/archive checks.
- A PATCH that changes nothing performs no update, no audit row and no ceiling check, and returns the current ETag.
- Secret create/rotate returns the `whsec_` token once. The stored value is an `hfse1.` envelope; the API never returns
  ciphertext. Rotation re-checks `If-Match` after provider I/O and discards the wrapped secret on conflict.
- The ceiling applies to create and to `paused -> active` only; it returns `409 active_endpoint_cap`.
- Endpoint `url` values are HTTPS only, port 443 only, with no userinfo, fragment or IPv6 zone. Literal
  private, loopback, link-local, multicast, unspecified, documentation or otherwise reserved addresses are
  rejected as `400 invalid_url` on create and PATCH. A hostname is also checked with a bounded 2 s DNS
  preflight: a prohibited answer is `400 invalid_url`; a DNS failure is allowed, because delivery-time
  enforcement (not this advisory preflight) is authoritative and re-runs on every attempt.
- The organization subscription is replaced as a whole with `PUT`; when an open period exists it requires `If-Match`
  (`428`/`412` semantics are identical to PATCH). A plan change appends a period and cancels its predecessor, while a
  same-plan status or period change rewrites the open row. `DELETE` cancels the current period and retains history.
- All management writes append `audit_logs` rows with the acting API-key id and the changed field names only.
