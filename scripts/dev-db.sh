#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd -P)"
ENV_FILE="$ROOT/.env"
COMPOSE_FILE="$ROOT/compose.yaml"
VOLUME_NAME="hookflow-postgres-data"
RESET_CONFIRMATION="DELETE HOOKFLOW DEVELOPMENT DATA"

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: scripts/dev-db.sh <command>

Commands:
  up       Start PostgreSQL and wait until it is healthy
  down     Stop and remove only PostgreSQL; preserve its volume and leave RabbitMQ running
  status   Show Compose status and PostgreSQL health
  logs     Show PostgreSQL logs
  psql     Open an interactive psql session inside the PostgreSQL container
  reset    Delete the HookFlow development volume after explicit confirmation

The reset command only targets the named volume:
  hookflow-postgres-data

It never runs a broad prune and never deletes another project's volume.
EOF
}

require_var() {
  local name="$1"
  if [ -z "${!name:-}" ]; then
    die "required variable $name is not set in .env"
  fi
}

load_env() {
  [ -f "$ENV_FILE" ] || die ".env not found at $ENV_FILE"
  if grep -q $'\r' "$ENV_FILE"; then
    die ".env contains CR characters; convert it to LF line endings"
  fi
  set -a
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  set +a

  require_var HOOKFLOW_DB_NAME
  require_var HOOKFLOW_DB_USER
  require_var HOOKFLOW_DB_PASSWORD
  require_var HOOKFLOW_DB_HOST
  require_var HOOKFLOW_DB_PORT
  require_var HOOKFLOW_DB_SSLMODE

  if [ "$HOOKFLOW_DB_PASSWORD" = "replace-with-a-random-local-password" ]; then
    die "HOOKFLOW_DB_PASSWORD still contains the placeholder value"
  fi

  case "$HOOKFLOW_DB_PORT" in
    ''|*[!0-9]*) die "HOOKFLOW_DB_PORT must be a positive integer" ;;
  esac
  if [ "$HOOKFLOW_DB_PORT" -lt 1 ] || [ "$HOOKFLOW_DB_PORT" -gt 65535 ]; then
    die "HOOKFLOW_DB_PORT must be between 1 and 65535"
  fi
}

compose() {
  docker compose --project-directory "$ROOT" -f "$COMPOSE_FILE" "$@"
}

postgres_container_id() {
  compose ps -q postgres
}

wait_healthy() {
  local cid status i
  cid="$(postgres_container_id)"
  [ -n "$cid" ] || die "PostgreSQL container was not created"

  for i in $(seq 1 60); do
    status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || true)"
    case "$status" in
      healthy)
        printf 'PostgreSQL container is healthy.\n'
        return 0
        ;;
      unhealthy|exited|dead)
        compose logs --no-color --tail=200 postgres >&2 || true
        die "PostgreSQL failed to become healthy (state: $status)"
        ;;
    esac
    sleep 2
  done

  compose logs --no-color --tail=200 postgres >&2 || true
  die "timed out waiting for PostgreSQL to become healthy"
}

cmd_up() {
  load_env
  compose up -d postgres
  wait_healthy
}

cmd_down() {
  load_env
  compose rm --stop --force postgres
  printf 'PostgreSQL container removed; named volume %s was preserved; RabbitMQ was left running.\n' "$VOLUME_NAME"
}

cmd_status() {
  load_env
  compose ps
  local cid
  cid="$(postgres_container_id)"
  if [ -n "$cid" ]; then
    printf 'container_health='
    docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid"
    if [ "$(docker inspect -f '{{.State.Running}}' "$cid")" = "true" ]; then
      compose exec -T postgres pg_isready -U "$HOOKFLOW_DB_USER" -d "$HOOKFLOW_DB_NAME" || true
    fi
  else
    printf 'PostgreSQL container is not present.\n'
  fi
}

cmd_logs() {
  load_env
  compose logs --no-color --tail=200 --follow postgres
}

cmd_psql() {
  load_env
  compose exec postgres psql -U "$HOOKFLOW_DB_USER" -d "$HOOKFLOW_DB_NAME"
}

verify_volume_ownership() {
  if ! docker volume inspect "$VOLUME_NAME" >/dev/null 2>&1; then
    printf 'Volume %s does not exist; nothing to remove.\n' "$VOLUME_NAME"
    return 0
  fi

  local project volume_key
  project="$(docker volume inspect -f '{{ index .Labels "com.docker.compose.project" }}' "$VOLUME_NAME")"
  volume_key="$(docker volume inspect -f '{{ index .Labels "com.docker.compose.volume" }}' "$VOLUME_NAME")"

  [ "$project" = "hookflow" ] || die "volume $VOLUME_NAME belongs to Compose project '$project', not 'hookflow'"
  [ "$volume_key" = "$VOLUME_NAME" ] || die "volume $VOLUME_NAME has unexpected Compose volume label '$volume_key'"
}

cmd_reset() {
  load_env
  [ -t 0 ] || die "reset requires an interactive terminal"

  printf 'WARNING: this permanently deletes the Docker volume:\n  %s\n' "$VOLUME_NAME"
  printf 'This removes the HookFlow development database and all its data.\n'
  printf 'Type exactly the following to continue:\n  %s\n> ' "$RESET_CONFIRMATION"
  local answer
  IFS= read -r answer
  [ "$answer" = "$RESET_CONFIRMATION" ] || die "confirmation did not match; no data was deleted"

  verify_volume_ownership
  compose rm --stop --force postgres
  if docker volume inspect "$VOLUME_NAME" >/dev/null 2>&1; then
    docker volume rm "$VOLUME_NAME" >/dev/null
    printf 'Removed volume %s.\n' "$VOLUME_NAME"
  fi

  compose up -d postgres
  wait_healthy
  printf 'PostgreSQL was recreated with an empty database; RabbitMQ was left running. Run scripts/migrate.sh up.\n'
}

main() {
  local command="${1:-}"
  case "$command" in
    up)
      [ $# -eq 1 ] || die "up takes no arguments"
      cmd_up
      ;;
    down)
      [ $# -eq 1 ] || die "down takes no arguments"
      cmd_down
      ;;
    status)
      [ $# -eq 1 ] || die "status takes no arguments"
      cmd_status
      ;;
    logs)
      [ $# -eq 1 ] || die "logs takes no arguments"
      cmd_logs
      ;;
    psql)
      [ $# -eq 1 ] || die "psql takes no arguments"
      cmd_psql
      ;;
    reset)
      [ $# -eq 1 ] || die "reset takes no arguments"
      cmd_reset
      ;;
    help|-h|--help|'')
      usage
      ;;
    *)
      usage >&2
      die "unknown command: $command"
      ;;
  esac
}

main "$@"
