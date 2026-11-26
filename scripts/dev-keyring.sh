#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd -P)"

usage() {
  cat <<'EOF'
Usage: scripts/dev-keyring.sh <command> [options]

Commands:
  ensure   Create the development File KEK keyring only if it is missing, then
           validate it. This is the default command.
  verify   Validate an existing keyring without creating or repairing it.

Options are passed to cmd/dev-keyring:
  -path FILE     Absolute path (default: HOOKFLOW_SECRET_KEYRING_PATH, usually
                 <home>/.hookflow/dev/keyring.json)
  -kid KID       Current KEK id (default: HOOKFLOW_SECRET_CURRENT_KID or kek-1)

The keyring is created with 0600 permissions outside the repository, is never
overwritten, and its key material is never printed. Add the printed
HOOKFLOW_SECRET_KEYRING_PATH value to .env.
EOF
}

main() {
  local command="${1:-ensure}"
  case "$command" in
    ensure)
      shift
      cd "$ROOT"
      # Toolchain resolution follows the repository's go.mod directive; do not
      # pin GOTOOLCHAIN here or a host with an older bundled toolchain fails.
      exec go run ./cmd/dev-keyring "$@"
      ;;
    verify)
      shift
      cd "$ROOT"
      exec go run ./cmd/dev-keyring -verify-only "$@"
      ;;
    help|-h|--help)
      usage
      ;;
    *)
      usage >&2
      printf 'error: unknown command: %s\n' "$command" >&2
      exit 1
      ;;
  esac
}

main "$@"
