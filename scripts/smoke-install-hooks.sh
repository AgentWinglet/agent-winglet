#!/usr/bin/env bash
# Smoke-tests install.sh/uninstall.sh hook wiring in isolated temp HOME/CODEX_HOME
# directories. It stubs `go install` so the test validates installer behavior
# without fetching private modules or touching the real machine.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/agent-winglet-install-smoke.XXXXXX")"

cleanup() {
  rm -rf "$TMP_ROOT"
}
trap cleanup EXIT

fail() {
  echo "smoke-install-hooks: $*" >&2
  exit 1
}

make_fake_go() {
  case_dir="$1"
  mkdir -p "${case_dir}/bin" "${case_dir}/gobin" "${case_dir}/gopath/bin"
  cat > "${case_dir}/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ge 2 ] && [ "$1" = "env" ]; then
  case "$2" in
    GOBIN) printf '%s\n' "${GOBIN:-}" ;;
    GOPATH) printf '%s\n' "${GOPATH:-${HOME}/go}" ;;
    *) exit 1 ;;
  esac
  exit 0
fi

if [ "$#" -ge 2 ] && [ "$1" = "install" ]; then
  target="${@: -1}"
  binary="${target%@*}"
  binary="${binary##*/}"
  install_dir="${GOBIN:-${GOPATH:-${HOME}/go}/bin}"
  mkdir -p "$install_dir"
  {
    printf '%s\n' '#!/usr/bin/env bash'
    printf 'echo "%s stub"\n' "$binary"
  } > "${install_dir}/${binary}"
  chmod +x "${install_dir}/${binary}"
  exit 0
fi

echo "fake go: unsupported invocation: $*" >&2
exit 1
EOF
  chmod +x "${case_dir}/bin/go"
}

new_case() {
  name="$1"
  case_dir="${TMP_ROOT}/${name}"
  mkdir -p "${case_dir}/home" "${case_dir}/codex-home" "${case_dir}/project"
  make_fake_go "$case_dir"
  printf '%s\n' "$case_dir"
}

run_in_case() {
  case_dir="$1"
  work_dir="$2"
  shift 2
  (
    cd "$work_dir"
    HOME="${case_dir}/home" \
    CODEX_HOME="${case_dir}/codex-home" \
    GOBIN="${case_dir}/gobin" \
    GOPATH="${case_dir}/gopath" \
    PATH="${case_dir}/bin:${PATH}" \
    "$@"
  )
}

command_count() {
  file="$1"
  binary="$2"
  if [ ! -f "$file" ]; then
    printf '0\n'
    return
  fi
  jq '[.hooks // {} | .. | objects | select(has("command")) | .command | select((split("/") | last) == $binary)] | length' \
    --arg binary "$binary" \
    "$file"
}

assert_count() {
  file="$1"
  binary="$2"
  want="$3"
  got="$(command_count "$file" "$binary")"
  [ "$got" = "$want" ] || fail "expected ${want} ${binary} entries in ${file}, got ${got}"
}

assert_absent_or_zero() {
  file="$1"
  binary="$2"
  assert_count "$file" "$binary" "0"
}

assert_event_has_binary() {
  file="$1"
  event="$2"
  binary="$3"
  got="$(jq -r '
    [.hooks[$event][]? | .hooks[]? | .command | select((split("/") | last) == $binary)] | length
  ' --arg event "$event" --arg binary "$binary" "$file")"
  [ "$got" = "1" ] || fail "expected one ${binary} entry for ${event} in ${file}, got ${got}"
}

file_mtime() {
  file="$1"
  if stat -f %m "$file" >/dev/null 2>&1; then
    stat -f %m "$file"
  else
    stat -c %Y "$file"
  fi
}

if ! command -v jq >/dev/null 2>&1; then
  fail "jq is required"
fi

global_case="$(new_case global)"
global_out="$(run_in_case "$global_case" "$REPO_ROOT" "${REPO_ROOT}/install.sh" --hook-only 2>&1)"
case "$global_out" in
  *"open Settings > Hooks and trust the agent-winglet codex-hook"*) ;;
  *) fail "Codex trust reminder missing from global install output" ;;
esac
assert_count "${global_case}/home/.claude/settings.json" "claude-hook" "5"
assert_count "${global_case}/codex-home/hooks.json" "codex-hook" "7"
assert_event_has_binary "${global_case}/codex-home/hooks.json" "SubagentStart" "codex-hook"
assert_event_has_binary "${global_case}/codex-home/hooks.json" "SubagentStop" "codex-hook"

touch -t 202001010101 "${global_case}/codex-home/hooks.json"
codex_before_update_mtime="$(file_mtime "${global_case}/codex-home/hooks.json")"
run_in_case "$global_case" "$REPO_ROOT" "${REPO_ROOT}/install.sh" --hook-only >/dev/null
codex_after_update_mtime="$(file_mtime "${global_case}/codex-home/hooks.json")"
[ "$codex_after_update_mtime" = "$codex_before_update_mtime" ] || fail "Codex update should preserve hooks.json mtime"
assert_count "${global_case}/home/.claude/settings.json" "claude-hook" "5"
assert_count "${global_case}/codex-home/hooks.json" "codex-hook" "7"
assert_event_has_binary "${global_case}/codex-home/hooks.json" "SubagentStart" "codex-hook"
assert_event_has_binary "${global_case}/codex-home/hooks.json" "SubagentStop" "codex-hook"

run_in_case "$global_case" "$REPO_ROOT" "${REPO_ROOT}/uninstall.sh" --hook-only >/dev/null
assert_absent_or_zero "${global_case}/home/.claude/settings.json" "claude-hook"
assert_absent_or_zero "${global_case}/codex-home/hooks.json" "codex-hook"

claude_case="$(new_case claude-only)"
run_in_case "$claude_case" "$REPO_ROOT" "${REPO_ROOT}/install.sh" --hook-only --claude-only >/dev/null
assert_count "${claude_case}/home/.claude/settings.json" "claude-hook" "5"
assert_absent_or_zero "${claude_case}/codex-home/hooks.json" "codex-hook"

codex_case="$(new_case codex-only)"
run_in_case "$codex_case" "$REPO_ROOT" "${REPO_ROOT}/install.sh" --hook-only --codex-only >/dev/null
assert_absent_or_zero "${codex_case}/home/.claude/settings.json" "claude-hook"
assert_count "${codex_case}/codex-home/hooks.json" "codex-hook" "7"
assert_event_has_binary "${codex_case}/codex-home/hooks.json" "SubagentStart" "codex-hook"
assert_event_has_binary "${codex_case}/codex-home/hooks.json" "SubagentStop" "codex-hook"
run_in_case "$codex_case" "$REPO_ROOT" "${REPO_ROOT}/uninstall.sh" --hook-only --codex-only >/dev/null
assert_absent_or_zero "${codex_case}/codex-home/hooks.json" "codex-hook"

codex_first_case="$(new_case codex-first-existing-file)"
printf '%s\n' '{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/tmp/other-hook"}]}]}}' > "${codex_first_case}/codex-home/hooks.json"
touch -t 202001010101 "${codex_first_case}/codex-home/hooks.json"
codex_first_before_mtime="$(file_mtime "${codex_first_case}/codex-home/hooks.json")"
run_in_case "$codex_first_case" "$REPO_ROOT" "${REPO_ROOT}/install.sh" --hook-only --codex-only >/dev/null
codex_first_after_mtime="$(file_mtime "${codex_first_case}/codex-home/hooks.json")"
[ "$codex_first_after_mtime" != "$codex_first_before_mtime" ] || fail "Codex first install should update hooks.json mtime"
assert_count "${codex_first_case}/codex-home/hooks.json" "codex-hook" "7"
assert_count "${codex_first_case}/codex-home/hooks.json" "other-hook" "1"

local_case="$(new_case local)"
run_in_case "$local_case" "${local_case}/project" "${REPO_ROOT}/install.sh" --hook-only --local >/dev/null
assert_count "${local_case}/project/.claude/settings.json" "claude-hook" "5"
assert_count "${local_case}/project/.codex/hooks.json" "codex-hook" "7"
assert_event_has_binary "${local_case}/project/.codex/hooks.json" "SubagentStart" "codex-hook"
assert_event_has_binary "${local_case}/project/.codex/hooks.json" "SubagentStop" "codex-hook"
assert_absent_or_zero "${local_case}/home/.claude/settings.json" "claude-hook"
assert_absent_or_zero "${local_case}/codex-home/hooks.json" "codex-hook"
run_in_case "$local_case" "${local_case}/project" "${REPO_ROOT}/uninstall.sh" --hook-only --local >/dev/null
assert_absent_or_zero "${local_case}/project/.claude/settings.json" "claude-hook"
assert_absent_or_zero "${local_case}/project/.codex/hooks.json" "codex-hook"

conflict_case="$(new_case conflicts)"
if run_in_case "$conflict_case" "$REPO_ROOT" "${REPO_ROOT}/install.sh" --hook-only --app-only >/dev/null 2>&1; then
  fail "--hook-only --app-only should fail"
fi
if run_in_case "$conflict_case" "$REPO_ROOT" "${REPO_ROOT}/install.sh" --claude-only --codex-only >/dev/null 2>&1; then
  fail "--claude-only --codex-only should fail"
fi
if run_in_case "$conflict_case" "$REPO_ROOT" "${REPO_ROOT}/uninstall.sh" --hook-only --app-only >/dev/null 2>&1; then
  fail "uninstall --hook-only --app-only should fail"
fi
if run_in_case "$conflict_case" "$REPO_ROOT" "${REPO_ROOT}/uninstall.sh" --claude-only --codex-only >/dev/null 2>&1; then
  fail "uninstall --claude-only --codex-only should fail"
fi

echo "smoke-install-hooks: ok"
