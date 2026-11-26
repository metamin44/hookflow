#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd -P)"
ENV_FILE="$ROOT/.env"
COMPOSE_FILE="$ROOT/compose.yaml"
# Keep this relative: golang-migrate prepends file:// to -path, and an absolute
# Windows drive path would be parsed as a URL host with an invalid port.
MIGRATIONS_REL="db/migrations"
MIGRATE_VERSION="v4.20.1"

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: scripts/migrate.sh <command>

Commands:
  up          Apply all pending migrations
  down N      Roll back N migrations (N must be a positive integer)
  down all    Roll back all applied migrations
  version     Show the current migration version and dirty state

The runner is pinned to golang-migrate v4.20.1 and is executed with:
  go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@v4.20.1

No force command is exposed. A dirty database is preserved for inspection.
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

container_psql() {
  compose exec -T postgres psql \
    -U "$HOOKFLOW_DB_USER" \
    -d "$HOOKFLOW_DB_NAME" \
    -v ON_ERROR_STOP=1 \
    -Atc "$1"
}

assert_not_dirty() {
  local dirty
  if ! dirty="$(container_psql "SELECT dirty::text FROM public.schema_migrations LIMIT 1" 2>/dev/null)"; then
    return 0
  fi
  if [ "$dirty" = "true" ] || [ "$dirty" = "t" ]; then
    die "migration state is dirty; the database has been preserved for inspection (no automatic force or reset)"
  fi
}

run_migrate_command() {
  local dsn
  dsn="postgres://${HOOKFLOW_DB_USER}@${HOOKFLOW_DB_HOST}:${HOOKFLOW_DB_PORT}/${HOOKFLOW_DB_NAME}?sslmode=${HOOKFLOW_DB_SSLMODE}"
  export PGPASSWORD="$HOOKFLOW_DB_PASSWORD"
  export GOTOOLCHAIN=local

  (
    cd "$ROOT"
    exec go run -tags postgres \
      "github.com/golang-migrate/migrate/v4/cmd/migrate@${MIGRATE_VERSION}" \
      -path "$MIGRATIONS_REL" \
      -database "$dsn" \
      "$@"
  )
}

run_migrate() {
  if ! run_migrate_command "$@"; then
    printf 'error: migration command failed; the database was preserved for inspection.\n' >&2
    printf 'Run scripts/migrate.sh version before retrying. Do not force a version without approval.\n' >&2
    exit 1
  fi
}

cmd_up() {
  [ $# -eq 0 ] || die "up takes no arguments"
  assert_not_dirty
  run_migrate up
}

cmd_down() {
  [ $# -eq 1 ] || die "down requires either a positive integer N or 'all'"
  local target="$1"
  if [ "$target" = "all" ]; then
    assert_not_dirty
    printf 'Applying all down migrations; application tables will be removed, but the persistent volume is not deleted.\n' >&2
    printf 'y\n' | run_migrate down
    return 0
  fi
  case "$target" in
    ''|*[!0-9]*|0)
      die "down count must be a positive integer or 'all'"
      ;;
  esac
  assert_not_dirty
  run_migrate down "$target"
}

cmd_version() {
  [ $# -eq 0 ] || die "version takes no arguments"
  local output rc
  set +e
  output="$(run_migrate_command version 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -eq 0 ]; then
    printf '%s\n' "$output"
    return 0
  fi
  if printf '%s' "$output" | grep -q 'no migration'; then
    printf 'No migration has been applied.\n'
    return 0
  fi
  printf '%s\n' "$output" >&2
  die "version command failed; the database was preserved for inspection"
}

main() {
  local command="${1:-}"
  load_env
  case "$command" in
    up)
      shift
      cmd_up "$@"
      ;;
    down)
      shift
      cmd_down "$@"
      ;;
    version)
      shift
      cmd_version "$@"
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
