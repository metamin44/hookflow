#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
SCRIPT="$ROOT/scripts/dev-db.sh"

bash -n "$SCRIPT"

if grep -q 'compose down' "$SCRIPT"; then
  printf 'dev-db.sh still uses broad project shutdown\n' >&2
  exit 1
fi

count="$(grep -c 'compose rm --stop --force postgres' "$SCRIPT")"
if [ "$count" -ne 2 ]; then
  printf 'dev-db.sh must use service-scoped PostgreSQL shutdown in down and reset (found %s)\n' "$count" >&2
  exit 1
fi

grep -q 'RabbitMQ was left running' "$SCRIPT"
grep -q 'without deleting the named volume\|preserve its volume' "$SCRIPT"

printf 'dev-db shutdown static checks passed.\n'
