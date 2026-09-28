#!/usr/bin/env bash

set -euo pipefail

role_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
wrapper="$role_dir/files/claude-shell-wrapper.sh"
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT

fake_bin="$test_root/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/claude" <<'EOF'
#!/usr/bin/env bash
{
  printf 'call'
  for arg in "$@"; do
    printf ' %q' "$arg"
  done
  printf '\n'
} >>"$CLAUDE_TEST_LOG"
EOF
chmod +x "$fake_bin/claude"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_file_equals() {
  local expected=$1
  local file=$2
  local actual
  actual=$(cat "$file")
  [[ $actual == "$expected" ]] || fail "$file contents differed
expected:
$expected
actual:
$actual"
}

assert_contains() {
  local needle=$1
  local file=$2
  grep -Fq -- "$needle" "$file" || fail "$file did not contain: $needle"
}

assert_not_contains() {
  local needle=$1
  local file=$2
  if grep -Fq -- "$needle" "$file"; then
    fail "$file unexpectedly contained: $needle"
  fi
}

new_case() {
  local name=$1
  CASE_HOME="$test_root/$name/home"
  CASE_LOG="$test_root/$name/calls.log"
  CASE_OUTPUT="$test_root/$name/output.log"
  mkdir -p "$CASE_HOME/.claude"
  printf '{"theme":"dark","isolatePeerMachines":false,"remoteControlAtStartup":false}\n' >"$CASE_HOME/.claude/settings.json"
  chmod 0600 "$CASE_HOME/.claude/settings.json"
  : >"$CASE_LOG"
  : >"$CASE_OUTPUT"
}

run_interactive() {
  local reply=$1
  local invocation=${2:-claude}
  printf '%s\n' "$reply" |
    HOME="$CASE_HOME" \
    PATH="$fake_bin:$PATH" \
    CLAUDE_TEST_LOG="$CASE_LOG" \
    CLAUDE_WRAPPER_UNDER_TEST="$wrapper" \
    script -qefc \
      "bash --noprofile --norc -c '. \"\$CLAUDE_WRAPPER_UNDER_TEST\"; $invocation'" \
      /dev/null >"$CASE_OUTPUT" 2>&1
}

marker_path() {
  printf '%s/.config/sandbar/claude-remote-control-onboarding-complete' "$CASE_HOME"
}

# An affirmative answer advertises the useful remote surfaces, preserves the
# rest of settings.json, enables auto-connect, records the answer, and starts
# the normal bypass-permissions session.
new_case affirmative
run_interactive y
assert_file_equals $'call auth status\ncall --dangerously-skip-permissions' "$CASE_LOG"
[[ -f $(marker_path) ]] || fail "affirmative onboarding did not create its marker"
[[ $(jq -r .remoteControlAtStartup "$CASE_HOME/.claude/settings.json") == true ]] || fail "affirmative onboarding did not enable remote control"
[[ $(jq -r .isolatePeerMachines "$CASE_HOME/.claude/settings.json") == true ]] || fail "affirmative onboarding disabled peer isolation"
[[ $(jq -r .theme "$CASE_HOME/.claude/settings.json") == dark ]] || fail "affirmative onboarding discarded custom settings"
[[ $(stat -c %a "$CASE_HOME/.claude/settings.json") == 600 ]] || fail "affirmative onboarding changed settings permissions"
assert_contains "claude.ai/code" "$CASE_OUTPUT"
assert_contains "Claude mobile app" "$CASE_OUTPUT"
assert_contains "push notifications" "$CASE_OUTPUT"

# A decline is remembered, leaves an explicit false in place, and does not
# prompt again on the next bare interactive invocation.
new_case decline
run_interactive n
[[ -f $(marker_path) ]] || fail "decline did not create its marker"
[[ $(jq -r .remoteControlAtStartup "$CASE_HOME/.claude/settings.json") == false ]] || fail "decline enabled remote control"
: >"$CASE_OUTPUT"
run_interactive ""
assert_not_contains "Enable Claude Code Remote Control" "$CASE_OUTPUT"
assert_file_equals $'call auth status\ncall --dangerously-skip-permissions\ncall auth status\ncall --dangerously-skip-permissions' "$CASE_LOG"

# Existing onboarding state suppresses the prompt and preserves the user's
# current setting, including a later manual change through Claude's /config.
new_case existing
mkdir -p "$(dirname "$(marker_path)")"
: >"$(marker_path)"
jq '.remoteControlAtStartup = true' "$CASE_HOME/.claude/settings.json" >"$CASE_HOME/.claude/settings.tmp"
mv "$CASE_HOME/.claude/settings.tmp" "$CASE_HOME/.claude/settings.json"
run_interactive ""
assert_not_contains "Enable Claude Code Remote Control" "$CASE_OUTPUT"
[[ $(jq -r .remoteControlAtStartup "$CASE_HOME/.claude/settings.json") == true ]] || fail "existing choice was overwritten"

# Real subcommands and non-terminal bare calls retain the wrapper's existing
# pass-through/auth behavior without changing first-run state.
new_case subcommand
run_interactive "" 'claude auth status'
assert_file_equals $'call auth status' "$CASE_LOG"
assert_not_contains "Enable Claude Code Remote Control" "$CASE_OUTPUT"
[[ ! -e $(marker_path) ]] || fail "subcommand created an onboarding marker"

new_case noninteractive
HOME="$CASE_HOME" PATH="$fake_bin:$PATH" CLAUDE_TEST_LOG="$CASE_LOG" \
  bash --noprofile --norc -c '. "$1"; claude' bash "$wrapper" </dev/null >"$CASE_OUTPUT" 2>&1
assert_file_equals $'call auth status\ncall --dangerously-skip-permissions' "$CASE_LOG"
assert_not_contains "Enable Claude Code Remote Control" "$CASE_OUTPUT"
[[ ! -e $(marker_path) ]] || fail "non-interactive call created an onboarding marker"

printf 'PASS: Claude shell wrapper remote-control onboarding and pass-through behavior\n'
