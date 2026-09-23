#!/bin/bash
set -euo pipefail

# Default-off; never run database fixtures in a production image.
case "${MULTICA_USER_DECISION_VERIFY_ON_BOOT:-false}" in
  false|0|"") exit 0 ;;
  true|1) ;;
  *) echo "[multica][verification] invalid user decision verification setting"; exit 1 ;;
esac
case "${AONE_ENV_TYPE:-}" in
  staging|prepub) ;;
  *) echo "[multica][verification] user decision verification requires a preproduction image"; exit 1 ;;
esac
if [[ -z "${DATABASE_URL:-}" || $# -ne 1 || ! -x "$1" ]]; then
  echo "[multica][verification] user decision verification prerequisites unavailable"
  exit 1
fi
# Only the child test process receives this credential; no config file is written.
echo "[multica][verification] user decision PostgreSQL checks starting"
MULTICA_USER_DECISION_TEST_DATABASE_URL="$DATABASE_URL" "$1" -test.run '^TestPostgres' -test.v -test.timeout=120s
echo "[multica][verification] user decision PostgreSQL checks passed"
