# HookFlow

HookFlow is a transactional-outbox webhook delivery system. Bundle 1 implements the PostgreSQL/RabbitMQ dispatch path: an application-managed AMQP connection, bounded Publisher, exact-pointer Worker, fixed-lease HTTP execution, and worker-hosted Recovery. Phase 6 adds the authenticated endpoint-management API, API-key authentication, secret rotation and subscription management on top of the existing schema. Phase 7 adds the authenticated `POST /v1/events` ingestion path (claim-first idempotency, fan-out and outbox in one transaction), a fixed 1 MiB request-body cap, and the OD-4 destination policy that blocks SSRF targets at create/PATCH and again on every delivery attempt.

**Current scope:** dispatch, ingestion, management and development security. Production activation is not implemented. The API serves health, `POST /v1/events`, and the `/v1` management routes; endpoint-secret readback and organization management are not exposed. Development builds implement envelope encryption/decryption under a local file KEK and Standard Webhooks v1 signing for the Worker; `HOOKFLOW_DEPLOYMENT_MODE=production` fails closed because no production KEK provider exists. Empty-secret snapshots send unsigned HTTP only with the development-only `HOOKFLOW_ALLOW_UNSIGNED_DELIVERY=true` opt-in; otherwise they are dispositioned without HTTP. Network-level egress restriction, rate limiting, production KMS/Vault custody and production readiness remain outstanding; CI security automation is defined in `.github/workflows`, and nightly execution is not yet verified on GitHub. See `docs/operations/runbook.md`.

## Prerequisites

- Go **1.27.4**
- Docker with Compose v2
- A local PostgreSQL 18 and RabbitMQ 4.4.1 environment (RabbitMQ 4.3.8 is the compatibility floor)

## Quickstart

### 1. Configure local secrets

```bash
test -f .env || cp .env.example .env
```

Edit `.env` and replace every placeholder. Confirm the RabbitMQ service/process settings, URL-encoding the password inside the AMQP URI:

```dotenv
HOOKFLOW_RABBITMQ_USER=hookflow
HOOKFLOW_RABBITMQ_PASSWORD=replace-with-a-random-local-password
HOOKFLOW_RABBITMQ_AMQP_PORT=5673
HOOKFLOW_RABBITMQ_MANAGEMENT_PORT=15673
HOOKFLOW_RABBITMQ_URI=amqp://hookflow:replace-with-a-random-local-password@127.0.0.1:5673/%2F
HOOKFLOW_RABBITMQ_MANAGEMENT_URL=http://127.0.0.1:15673
```

The user/password variables configure the local `rabbitmq` service; the URI/URL configure HookFlow processes and must contain the same credentials and ports. `%2F` denotes the default `/` vhost. `.env.example` includes every approved runtime default. Never commit `.env`, print credential-bearing URIs or publish `docker compose config` output.

The API and Worker also require the delivery-security block. Generate the development file KEK keyring once; it is created outside the repository with restricted permissions and is never overwritten:

```bash
./scripts/dev-keyring.sh ensure
```

Paste the absolute path it prints into `HOOKFLOW_SECRET_KEYRING_PATH` in `.env`. The keyring contains key material: do not commit it, print it or copy it into the repository.

Export the file into each terminal that runs a HookFlow process:

```bash
set -a
. ./.env
set +a
```

### 2. Start dependencies and migrate explicitly

```bash
./scripts/dev.sh
# Equivalent: docker compose up -d --wait postgres rabbitmq
go run ./cmd/migrate up
go run ./cmd/migrate version
```

`up` applies all pending migrations; the current schema has 13 units. The API does not migrate on startup. Compose pins PostgreSQL 18 and RabbitMQ 4.4.1 by digest, uses persistent named volumes, binds ports to loopback and waits for real health checks. Migration execution remains explicit.

### 3. Run the processes

Use separate terminals, with the exported environment in each:

```bash
go run ./cmd/api
go run ./cmd/outbox-publisher
go run ./cmd/worker
```

- API: `HOOKFLOW_API_ADDR`, default `:8080`; serves `GET /health/live`, `GET /health/ready` and the authenticated `/v1` management routes.
- Publisher: validates topology/policy, claims pending outbox rows, publishes identifiers-only messages with mandatory confirms, and applies T1/T2/T3 fencing.
- Worker: consumes exact dispatch pointers, runs Recovery and the idempotency sweep, decrypts allowlisted endpoint secrets and signs the HTTP request; only empty-secret snapshots with the development unsigned gate enabled skip signing.

Check API readiness (database ping) and liveness:

```bash
curl -fsS http://127.0.0.1:8080/health/ready
curl -fsS http://127.0.0.1:8080/health/live
```

Publisher and Worker startup fails closed when required database, broker, topology, policy or delivery-security checks fail. They reconnect after startup using bounded exponential backoff; old channel generations are never reused.

### 4. Create an API key and manage endpoints

Management routes are authenticated by database-backed API keys; there is no HTTP route to create organizations or keys. Seed an `organizations` row for the tenant, then use the operator CLI:

```bash
go run ./cmd/apikey create -organization <org-uuid> -name local-admin \
  -scopes endpoints:write,endpoints:secrets:rotate
```

The command prints the raw token once on the `api_key:` line (43-character unpadded base64url). Store it in a secret manager or a mode-restricted file, never in Git or logs; only an Argon2id verifier is stored in PostgreSQL. Use it as `Authorization: Bearer <token>`:

```bash
export HOOKFLOW_TOKEN='<token>'
curl -fsS -X POST "http://127.0.0.1:8080/v1/organizations/<org-uuid>/endpoints" \
  -H "Authorization: Bearer $HOOKFLOW_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"orders","url":"https://example.test/webhooks","http_headers":{"x-tenant":"demo"}}'
```

The `201` response contains the `whsec_` secret exactly once and an `ETag`. PATCH and secret rotation need `If-Match` with the current strong tag (`428` when missing, `412` when stale, `400` when malformed; `*` is unconditional), and successful writes return the new tag. DELETE takes no validator and returns `204`. Rotate the secret with `POST …/endpoints/{endpoint-id}/secret/rotate` and the same `If-Match` rule. Subscriptions use `GET|POST …/endpoints/{endpoint-id}/subscriptions` and `DELETE …/subscriptions/{subscription-id}` with exact-match event types. List and revoke keys with `go run ./cmd/apikey list|revoke …`; see the [configuration reference](docs/operations/configuration.md) for scopes and limits.

### 5. Optional process metrics

`HOOKFLOW_METRICS_ADDR` enables a separate process-local Prometheus text endpoint on the API, Publisher or Worker. Empty/unset disables it; no metrics route is added to the public API mux. Use a different port per process:

```bash
HOOKFLOW_METRICS_ADDR=127.0.0.1:9090 go run ./cmd/api
HOOKFLOW_METRICS_ADDR=127.0.0.1:9091 go run ./cmd/outbox-publisher
HOOKFLOW_METRICS_ADDR=127.0.0.1:9092 go run ./cmd/worker
curl -fsS http://127.0.0.1:9090/metrics
curl -fsS http://127.0.0.1:9091/metrics
curl -fsS http://127.0.0.1:9092/metrics
```

An explicit host is required; wildcard binding is rejected. Bind to loopback or protect the listener with an approved network ACL/firewall policy. Metrics use fixed, low-cardinality names and must never contain entity IDs, payloads, credentials or secrets.

## Tests

With PostgreSQL and RabbitMQ running and the environment exported:

```bash
gofmt -l .
go vet ./...
go build ./...
go test ./... -count=1
go mod verify
go mod tidy -diff
go run ./cmd/migrate up
go test -tags=integration ./... -count=1 -p 1
```

`go mod tidy -diff` is verified for Go 1.27.4 and does not modify `go.mod` or `go.sum`. Integration packages must run serially because they require an isolated database. On Linux with CGO and GCC available:

```bash
CGO_ENABLED=1 go test -race ./... -count=1
```

CI defines the same integration suite against pinned PostgreSQL 18 and RabbitMQ 4.4.1/4.3.8 service containers, fails if any required test is skipped, and includes a down-13/up migration proof in its disposable database. Hosted runs `45826121102` (push) and `45826131425` (pull request) passed for merged SHA `1128f1042e3d951265aa7a8479b2d42c19a7975d`; PR #1 merged as `2fa7eae66e3835c455bba6e4ca325ddae21e342e`. `.github/workflows/nightly.yml` reuses the CI matrix daily at 02:17 UTC and by manual dispatch; it has not yet run on GitHub. A pinned `govulncheck` v1.10.0 job fails on reachable vulnerabilities and publishes non-reachable package/module findings without editing `go.mod` or `go.sum`.

Local execution evidence (2026-11-24, Phase 6 and Phase 6.1): Go 1.27.4 build/vet and unit tests passed (25/25 test-bearing packages, 29 total); PostgreSQL 18.7 plus RabbitMQ 4.4.1 and 4.3.8 integration passed (28/28 test-bearing packages, 0 failures, 2 Windows-only POSIX-permission skips); Linux CGO race tests passed for the whole module (27/27 test-bearing packages, no data races). The management suite has 23 top-level tests (12 Phase 6 + 11 Phase 6.1). `go mod verify` passed and `go mod tidy -diff` was empty. golangci-lint v2.15.0 ran in Phase 6 as a tool and reported 17 advisory findings (13 pre-existing cyclomatic/cognitive-complexity findings, one `PatchEndpoint` cognitive-complexity finding, and 3 pre-existing `gosec` integer-conversion findings); lint cleanliness is not claimed and lint was not rerun in Phase 6.1. No new migration, local schema rollback or container recreation was needed or performed. See the dated architecture evidence and operations runbook for the remaining nightly-CI and production gates.

Phase 7 verification (2026-11-25) repeated the full suite after the Stage 5 documentation edits: 31 unit packages passed, 31 integration packages passed with 0 failures and the same 2 Windows-only skips, Linux container race passed with no data race, and `go mod verify`/`go mod tidy -diff` were clean. golangci-lint v2.15.0 reported 17 advisory findings, all in pre-existing code (none in Phase 7 code); lint cleanliness is still not claimed. No migration, schema change or infrastructure mutation was performed. The later hosted PR CI run `45681759235` passed on SHA `00a5a82cbc638411278b49f3bf57c55cde5bf2d8`; that is historical evidence for that exact pre-remediation SHA. Stage 8B subsequently passed hosted runs `45826121102` and `45826131425` for merged SHA `1128f1042e3d951265aa7a8479b2d42c19a7975d`.

## Runtime contract and limits

- Dispatch exchange: durable topic `hookflow.dispatch`, routing key `delivery`.
- Work queue: durable shared quorum `hookflow.dispatch.q`.
- DLX/DLQ: durable direct `hookflow.dispatch.dlx` -> durable quorum `hookflow.dispatch.dlq`, routing key `delivery.failed`.
- Policy `hookflow-dispatch-dlq`: at-most-once dead-lettering, delivery limit 20; no delayed retry or replay.
- Broker bodies contain only schema version, organization/delivery/outbox UUIDs and dispatch sequence. Payloads and secrets remain in PostgreSQL.
- Retries are database-scheduled. Recovery may repair a missing outbox row, re-arm a failed current row, or create generation N+1 for eligible stale work. It never replays the DLQ.
- Management API: `/v1/organizations/{org}/endpoints` with create/list/get/PATCH/DELETE, `POST …/secret/rotate`, and endpoint subscriptions. All `/v1` routes require a bearer API key; no route starts with `/v1/` without authentication.
- Endpoint writes use Contract A strong ETags and `If-Match`; a no-op PATCH is side-effect free. The active-endpoint ceiling defaults to 100 and is enforced under the organization row lock for create and `paused -> active`.
- Secrets are stored as `hfse1.` envelopes under the development file KEK and returned as `whsec_` tokens only at create/rotate time. The Worker decrypts and signs with Standard Webhooks v1 headers; it never returns secret material.
- D6/U25 architecture (D6-A…D6-G, U25-A…U25-D) is Owner-approved (2026-11-24) and the development implementation is in place. Production KMS/Vault custody and production readiness are **not implemented / not ready**: no production key custody, no KMS/Vault provider, no signing-key rotation beyond the keyring allowlist. Production mode fails closed at startup.

See [configuration reference](docs/operations/configuration.md) and [operations runbook](docs/operations/runbook.md). Production blockers, outage handling and safe DLQ inspection/drain procedures are documented in the runbook.
