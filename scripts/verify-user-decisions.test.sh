#!/bin/bash
set -euo pipefail
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
fixture_dir="$(mktemp -d)"
trap 'rm -rf "$fixture_dir"' EXIT
cat > "$fixture_dir/verify" <<'SH'
#!/bin/bash
set -euo pipefail
[[ "${MULTICA_USER_DECISION_TEST_DATABASE_URL:-}" == 'fixture-only-not-a-connection' ]]
[[ "$*" == '-test.run ^TestPostgres -test.v -test.timeout=120s' ]]
touch "$VERIFICATION_MARKER"
SH
chmod +x "$fixture_dir/verify"
export VERIFICATION_MARKER="$fixture_dir/called"
export DATABASE_URL='fixture-only-not-a-connection'
export MULTICA_USER_DECISION_VERIFY_ON_BOOT=false
export AONE_ENV_TYPE=production
bash "$repo_root/src/verify-user-decisions.sh" "$fixture_dir/verify"
[[ ! -e "$VERIFICATION_MARKER" ]]
export MULTICA_USER_DECISION_VERIFY_ON_BOOT=true
if bash "$repo_root/src/verify-user-decisions.sh" "$fixture_dir/verify"; then
 echo 'production unexpectedly executed verification'; exit 1
fi
[[ ! -e "$VERIFICATION_MARKER" ]]
export AONE_ENV_TYPE=staging
bash "$repo_root/src/verify-user-decisions.sh" "$fixture_dir/verify"
[[ -f "$VERIFICATION_MARKER" ]]
echo 'verification gate checks passed'
