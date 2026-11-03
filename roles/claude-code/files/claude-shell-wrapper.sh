# One-time onboarding for bare, interactive Claude Code launches: Remote
# Control, then the default permission mode. Real subcommands pass through
# untouched, except `claude agents`, which receives the same permission flags
# as an ordinary session so the sessions it dispatches match it.

# Apply a jq filter to ~/.claude/settings.json in place, keeping its file mode
# and every key the filter does not touch. Arguments are passed to jq verbatim,
# so put --arg/--argjson before the filter.
_sandbar_claude_update_settings() {
  local settings="$HOME/.claude/settings.json"
  local tmp
  mkdir -p -- "$HOME/.claude" || return $?
  tmp=$(mktemp "$HOME/.claude/.settings.json.XXXXXX") || return $?
  if [ -f "$settings" ]; then
    if ! command jq "$@" "$settings" >"$tmp"; then
      rm -f -- "$tmp"
      return 1
    fi
    chmod --reference="$settings" "$tmp" || return $?
  else
    if ! printf '{}\n' | command jq "$@" >"$tmp"; then
      rm -f -- "$tmp"
      return 1
    fi
    chmod 0644 "$tmp" || return $?
  fi
  mv -f -- "$tmp" "$settings"
}

# The permission flag every interactive launch and `claude agents` receives.
# settings.json's permissions.defaultMode is the source of truth, so a default
# changed later through Claude's /config is followed rather than overridden.
#
# Bypass is passed as a flag as well as a setting because `claude agents`
# applies its own flags as the default for the sessions it dispatches; an agent
# view launched with no flag dispatched sessions that came up in manual mode
# with bypass absent from Shift+Tab. In every other mode the allow-flag only
# makes bypass reachable through Shift+Tab, without starting in it, so a user
# who chose auto mode can still switch one session to bypass.
_sandbar_claude_permission_arg() {
  local mode
  mode=$(command jq -r '.permissions.defaultMode // empty' "$HOME/.claude/settings.json" 2>/dev/null)
  if [ "$mode" = bypassPermissions ]; then
    printf '%s' --dangerously-skip-permissions
  else
    printf '%s' --allow-dangerously-skip-permissions
  fi
}

claude() {
  local sandbar_claude_state_dir="$HOME/.config/sandbar"
  local sandbar_claude_marker="$sandbar_claude_state_dir/claude-remote-control-onboarding-complete"
  local sandbar_claude_mode_marker="$sandbar_claude_state_dir/claude-permission-mode-onboarding-complete"
  local sandbar_claude_reply
  local sandbar_claude_enabled
  local sandbar_claude_mode

  case "${1-}" in
    agents)
      shift
      command claude agents "$(_sandbar_claude_permission_arg)" "$@"
      return $?
      ;;
    auth|auto-mode|config|doctor|gateway|install|mcp|plugin|plugins|project|setup-token|ultrareview|update|upgrade)
      command claude "$@"
      return $?
      ;;
  esac

  if [ "$#" -eq 0 ] && [ -t 0 ] && [ ! -f "$sandbar_claude_marker" ]; then
    printf '\nClaude Code Remote Control lets you continue this VM session from\n'
    printf 'claude.ai/code or the Claude mobile app, with push notifications\n'
    printf 'when a task finishes or needs your input. Code still runs in the VM.\n\n'
    printf 'Enable Claude Code Remote Control for every interactive session? [y/N] '
    IFS= read -r sandbar_claude_reply || return $?

    case "$sandbar_claude_reply" in
      y|Y) sandbar_claude_enabled=true ;;
      *) sandbar_claude_enabled=false ;;
    esac

    mkdir -p -- "$sandbar_claude_state_dir" || return $?
    chmod 0700 "$sandbar_claude_state_dir" || return $?
    _sandbar_claude_update_settings --argjson enabled "$sandbar_claude_enabled" \
      '.remoteControlAtStartup = $enabled | .isolatePeerMachines = true' || return $?
    (umask 077 && : >"$sandbar_claude_marker") || return $?

    if [ "$sandbar_claude_enabled" = true ]; then
      printf '\nRemote Control is enabled. Open this session from claude.ai/code\n'
      printf 'or the Claude app after signing in below.\n'
    else
      printf '\nRemote Control remains off. Run /remote-control in any session\n'
      printf 'if you want to enable it later.\n'
    fi
  fi

  # This question has its own marker rather than sharing Remote Control's, so a
  # VM whose user answered that one before this question existed (a preserved
  # home, or an in-place re-provision) is still asked once.
  if [ "$#" -eq 0 ] && [ -t 0 ] && [ ! -f "$sandbar_claude_mode_marker" ]; then
    printf '\nClaude Code starts in auto mode: a classifier reviews each action\n'
    printf 'and blocks risky ones, without asking you about routine work.\n'
    printf 'Bypass permissions mode skips every check instead. Either way, you\n'
    printf 'can switch a session between them with Shift+Tab.\n\n'
    printf 'Start sessions in bypass permissions mode instead of auto mode? [y/N] '
    IFS= read -r sandbar_claude_reply || return $?

    case "$sandbar_claude_reply" in
      y|Y) sandbar_claude_mode=bypassPermissions ;;
      *) sandbar_claude_mode=auto ;;
    esac

    mkdir -p -- "$sandbar_claude_state_dir" || return $?
    chmod 0700 "$sandbar_claude_state_dir" || return $?
    _sandbar_claude_update_settings --arg mode "$sandbar_claude_mode" \
      '.permissions = ((.permissions // {}) | .defaultMode = $mode)' || return $?
    (umask 077 && : >"$sandbar_claude_mode_marker") || return $?

    if [ "$sandbar_claude_mode" = bypassPermissions ]; then
      printf '\nSessions will start in bypass permissions mode.\n'
    else
      printf '\nSessions will start in auto mode.\n'
    fi
    printf 'Change the default later from /config.\n'
  fi

  # Claude Code can require a relaunch after its first login before the chosen
  # permission mode (and, when selected above, Remote Control) takes effect.
  # Show this once for interactive agent launches, but never interrupt scripts
  # or print mode.
  local sandbar_claude_notice="$sandbar_claude_state_dir/.claude-first-run-notice"
  if [ ! -f "$sandbar_claude_notice" ] && [ -t 0 ]; then
    printf '\n  ⚠ Heads up: after your first Claude Code sign-in, quit and run\n'
    printf "  'claude' again if the session opens in manual mode. If you opted\n"
    printf '  into Remote Control, relaunch if it is not active yet. Claude may\n'
    printf '  also ask once about its renderer.\n\n'
    mkdir -p -- "$sandbar_claude_state_dir" || return $?
    chmod 0700 "$sandbar_claude_state_dir" || return $?
    (umask 077 && : >"$sandbar_claude_notice") || return $?
  fi

  if ! command claude auth status >/dev/null 2>&1; then
    echo "Not signed in to Claude Code — starting sign-in…"
    command claude auth login || return $?
  fi
  command claude "$(_sandbar_claude_permission_arg)" "$@"
}
