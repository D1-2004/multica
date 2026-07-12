#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

setup_sandbox() {
  local tmp="$1"
  mkdir -p "$tmp/stub-bin" "$tmp/install-bin" "$tmp/home"

  cat >"$tmp/stub-bin/git" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'git %s\n' "$*" >> "$MULTICA_TEST_LOG"
if [ "${1:-}" = "clone" ]; then
  destination="${@: -1}"
  mkdir -p "$destination/.git" "$destination/server"
fi
if [[ "$*" == *"rev-parse --short HEAD"* ]]; then
  printf 'abc1234\n'
fi
STUB
  chmod +x "$tmp/stub-bin/git"

  cat >"$tmp/stub-bin/go" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'go cwd=%s args=%s\n' "$PWD" "$*" >> "$MULTICA_TEST_LOG"
if [ "${MULTICA_TEST_GO_FAIL:-}" = "1" ]; then
  exit 19
fi
output=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    output="$2"
    break
  fi
  shift
done
[ -n "$output" ] || exit 20
printf '#!/usr/bin/env bash\necho source-cli\n' > "$output"
chmod +x "$output"
STUB
  chmod +x "$tmp/stub-bin/go"
}

run_installer() {
  local tmp="$1"
  shift
  HOME="$tmp/home" \
    PATH="$tmp/stub-bin:/usr/bin:/bin" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_LOG="$tmp/commands.log" \
    bash "$ROOT_DIR/scripts/install.sh" "$@" >"$tmp/install.out" 2>"$tmp/install.err"
}

assert_contains() {
  local file="$1"
  local expected="$2"
  if ! grep -Fq -- "$expected" "$file"; then
    echo "expected $file to contain: $expected" >&2
    cat "$file" >&2 || true
    return 1
  fi
}

test_default_installs_fork_develop() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  setup_sandbox "$tmp"

  run_installer "$tmp"

  [ -x "$tmp/install-bin/multica" ]
  [ "$(tr -d '\r\n' < "$tmp/home/.multica/update-source")" = "fork" ]
  assert_contains "$tmp/commands.log" "git clone --filter=blob:none --no-checkout https://github.com/D1-2004/multica.git"
  assert_contains "$tmp/commands.log" "fetch --force --depth=1 origin develop"
  assert_contains "$tmp/commands.log" "go cwd=$tmp/home/.multica/source/fork/server"
}

test_official_source_defaults_to_main() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  setup_sandbox "$tmp"

  run_installer "$tmp" --source official

  [ "$(tr -d '\r\n' < "$tmp/home/.multica/update-source")" = "official" ]
  assert_contains "$tmp/commands.log" "git clone --filter=blob:none --no-checkout https://github.com/multica-ai/multica.git"
  assert_contains "$tmp/commands.log" "fetch --force --depth=1 origin main"
}

test_ref_override_is_used() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  setup_sandbox "$tmp"

  run_installer "$tmp" --source fork --ref feature/chat
  assert_contains "$tmp/commands.log" "fetch --force --depth=1 origin feature/chat"
}

test_build_failure_preserves_binary_and_source() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  setup_sandbox "$tmp"
  printf 'old binary' > "$tmp/install-bin/multica"
  chmod +x "$tmp/install-bin/multica"
  mkdir -p "$tmp/home/.multica"
  printf 'official\n' > "$tmp/home/.multica/update-source"

  if HOME="$tmp/home" \
    PATH="$tmp/stub-bin:/usr/bin:/bin" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_LOG="$tmp/commands.log" \
    MULTICA_TEST_GO_FAIL=1 \
    bash "$ROOT_DIR/scripts/install.sh" --source fork >"$tmp/install.out" 2>"$tmp/install.err"; then
    echo "installer unexpectedly succeeded with a failed source build" >&2
    return 1
  fi

  [ "$(cat "$tmp/install-bin/multica")" = "old binary" ]
  [ "$(tr -d '\r\n' < "$tmp/home/.multica/update-source")" = "official" ]
  assert_contains "$tmp/install.err" "existing CLI was not changed"
}

test_missing_tool_is_actionable() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  setup_sandbox "$tmp"
  mv "$tmp/stub-bin/go" "$tmp/stub-bin/go.hidden"

  if HOME="$tmp/home" \
    PATH="$tmp/stub-bin:/usr/bin:/bin" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_LOG="$tmp/commands.log" \
    bash "$ROOT_DIR/scripts/install.sh" >"$tmp/install.out" 2>"$tmp/install.err"; then
    echo "installer unexpectedly succeeded without go" >&2
    return 1
  fi
  assert_contains "$tmp/install.err" "Go is required"
}

test_default_installs_fork_develop
test_official_source_defaults_to_main
test_ref_override_is_used
test_build_failure_preserves_binary_and_source
test_missing_tool_is_actionable
echo "install.sh tests passed"
