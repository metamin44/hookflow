#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
ENV_FILE="$ROOT/.env"

[ -f "$ENV_FILE" ] || { printf 'error: .env not found\n' >&2; exit 1; }
set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

for name in HOOKFLOW_DB_PASSWORD HOOKFLOW_RABBITMQ_USER HOOKFLOW_RABBITMQ_PASSWORD HOOKFLOW_RABBITMQ_URI HOOKFLOW_RABBITMQ_MANAGEMENT_URL; do
  [ -n "${!name:-}" ] || { printf 'error: required variable %s is not set\n' "$name" >&2; exit 1; }
done

docker compose --project-directory "$ROOT" -f "$ROOT/compose.yaml" up -d --wait postgres rabbitmq
printf 'PostgreSQL and RabbitMQ are healthy. Run scripts/migrate.sh up explicitly before starting runtimes.\n'
