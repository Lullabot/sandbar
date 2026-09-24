#!/usr/bin/env bash

set -euo pipefail

role_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
wrapper="$role_dir/files/codex-shell-wrapper.sh"
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT

fake_bin="$test_root/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/codex" <<'EOF'
#!/usr/bin/env bash
{
  printf 'call'
  for arg in "$@"; do
    printf ' %q' "$arg"
  done
  printf '\n'
} >>"$CODEX_TEST_LOG"

if [[ -n ${CODEX_TEST_FAIL_ON:-} && $CODEX_TEST_FAIL_ON == "$*" ]]; then
  exit "${CODEX_TEST_FAIL_STATUS:-42}"
fi
EOF
chmod +x "$fake_bin/codex"

cat >"$fake_bin/systemctl" <<'EOF'
#!/usr/bin/env bash
{
  printf 'systemctl'
  for arg in "$@"; do
    printf ' %q' "$arg"
  done
  printf '\n'
} >>"$CODEX_TEST_LOG"

if [[ -n ${SYSTEMCTL_TEST_FAIL_ON:-} && $SYSTEMCTL_TEST_FAIL_ON == "$*" ]]; then
  exit 43
fi
EOF
chmod +x "$fake_bin/systemctl"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

[[ -r $wrapper ]] || fail "wrapper under test is missing: $wrapper"

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
  mkdir -p "$CASE_HOME"
  : >"$CASE_LOG"
  : >"$CASE_OUTPUT"
}

run_interactive() {
  local reply=$1
  local invocation=${2:-codex}
  printf '%s\n' "$reply" |
    HOME="$CASE_HOME" \
    PATH="$fake_bin:$PATH" \
    CODEX_TEST_LOG="$CASE_LOG" \
    CODEX_WRAPPER_UNDER_TEST="$wrapper" \
    script -qefc \
      "bash --noprofile --norc -c '. \"\$CODEX_WRAPPER_UNDER_TEST\"; $invocation'" \
      /dev/null >"$CASE_OUTPUT" 2>&1
}

marker_path() {
  printf '%s/.config/sandbar/codex-remote-control-onboarding-complete' "$CASE_HOME"
}

# Affirmative onboarding executes the upstream workflow in order, records
# completion only after pairing, explains both follow-up commands, then opens
# the ordinary TUI.
new_case affirmative
run_interactive y
assert_file_equals $'call login --device-auth\ncall app-server daemon bootstrap --remote-control\nsystemctl --user enable sandbar-codex-app-server.service\ncall remote-control pair\ncall' "$CASE_LOG"
[[ -f $(marker_path) ]] || fail "affirmative onboarding did not create its marker"
assert_contains "codex agents" "$CASE_OUTPUT"
assert_contains "codex remote-control pair" "$CASE_OUTPUT"

# A decline is remembered, launches the TUI immediately, and does not prompt
# again on the next interactive bare invocation.
new_case decline
run_interactive n
assert_file_equals "call" "$CASE_LOG"
[[ -f $(marker_path) ]] || fail "decline did not create its marker"
: >"$CASE_OUTPUT"
run_interactive ""
assert_file_equals $'call\ncall' "$CASE_LOG"
assert_not_contains "Enable Codex remote control" "$CASE_OUTPUT"

# A failed setup command propagates its status, does not launch the TUI or
# write completion, and leaves the prompt available on the next attempt.
new_case retry
export CODEX_TEST_FAIL_ON='app-server daemon bootstrap --remote-control'
set +e
run_interactive y
status=$?
set -e
[[ $status -eq 42 ]] || fail "setup failure returned $status, want 42"
assert_file_equals $'call login --device-auth\ncall app-server daemon bootstrap --remote-control' "$CASE_LOG"
[[ ! -e $(marker_path) ]] || fail "failed onboarding created its marker"
assert_contains "Enable Codex remote control" "$CASE_OUTPUT"
unset CODEX_TEST_FAIL_ON
: >"$CASE_OUTPUT"
run_interactive y
assert_contains "Enable Codex remote control" "$CASE_OUTPUT"
assert_file_equals $'call login --device-auth\ncall app-server daemon bootstrap --remote-control\ncall login --device-auth\ncall app-server daemon bootstrap --remote-control\nsystemctl --user enable sandbar-codex-app-server.service\ncall remote-control pair\ncall' "$CASE_LOG"

# If boot registration fails, onboarding must not claim completion or open the
# TUI; the user can retry when the user systemd manager is available.
new_case boot_registration_failure
export SYSTEMCTL_TEST_FAIL_ON='--user enable sandbar-codex-app-server.service'
set +e
run_interactive y
status=$?
set -e
[[ $status -eq 43 ]] || fail "boot registration failure returned $status, want 43"
assert_file_equals $'call login --device-auth\ncall app-server daemon bootstrap --remote-control\nsystemctl --user enable sandbar-codex-app-server.service' "$CASE_LOG"
[[ ! -e $(marker_path) ]] || fail "boot registration failure created its marker"
unset SYSTEMCTL_TEST_FAIL_ON

# Invocations with arguments bypass onboarding even on a terminal.
new_case interactive_args
run_interactive "" 'codex --version'
assert_file_equals $'call --version' "$CASE_LOG"
assert_not_contains "Enable Codex remote control" "$CASE_OUTPUT"
[[ ! -e $(marker_path) ]] || fail "argument pass-through created an onboarding marker"

# Non-terminal calls preserve every argv element exactly, including whitespace,
# while a non-terminal bare call reaches the ordinary upstream command without
# prompting or changing first-run state.
new_case noninteractive
HOME="$CASE_HOME" PATH="$fake_bin:$PATH" CODEX_TEST_LOG="$CASE_LOG" \
  bash --noprofile --norc -c '. "$1"; shift; codex "$@"' bash "$wrapper" exec 'two words' --flag </dev/null >"$CASE_OUTPUT" 2>&1
HOME="$CASE_HOME" PATH="$fake_bin:$PATH" CODEX_TEST_LOG="$CASE_LOG" \
  bash --noprofile --norc -c '. "$1"; codex' bash "$wrapper" </dev/null >>"$CASE_OUTPUT" 2>&1
assert_file_equals $'call exec two\\ words --flag\ncall' "$CASE_LOG"
assert_not_contains "Enable Codex remote control" "$CASE_OUTPUT"
[[ ! -e $(marker_path) ]] || fail "non-interactive pass-through created an onboarding marker"

printf 'PASS: Codex shell wrapper onboarding and pass-through behavior\n'
