# One-time remote-control onboarding for bare, interactive Claude Code launches.
# Real subcommands continue to pass through untouched; ordinary sessions retain
# sand's authentication and bypass-permissions setup.
claude() {
  local sandbar_claude_state_dir="$HOME/.config/sandbar"
  local sandbar_claude_marker="$sandbar_claude_state_dir/claude-remote-control-onboarding-complete"
  local sandbar_claude_settings="$HOME/.claude/settings.json"
  local sandbar_claude_reply
  local sandbar_claude_enabled
  local sandbar_claude_tmp

  case "${1-}" in
    agents|auth|auto-mode|config|doctor|gateway|install|mcp|plugin|plugins|project|setup-token|ultrareview|update|upgrade)
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

    mkdir -p -- "$HOME/.claude" "$sandbar_claude_state_dir" || return $?
    chmod 0700 "$sandbar_claude_state_dir" || return $?
    sandbar_claude_tmp=$(mktemp "$HOME/.claude/.settings.json.XXXXXX") || return $?
    if [ -f "$sandbar_claude_settings" ]; then
      if ! command jq --argjson enabled "$sandbar_claude_enabled" \
        '.remoteControlAtStartup = $enabled | .isolatePeerMachines = true' \
        "$sandbar_claude_settings" >"$sandbar_claude_tmp"; then
        rm -f -- "$sandbar_claude_tmp"
        return 1
      fi
      chmod --reference="$sandbar_claude_settings" "$sandbar_claude_tmp" || return $?
    else
      if ! printf '{}\n' | command jq --argjson enabled "$sandbar_claude_enabled" \
        '.remoteControlAtStartup = $enabled | .isolatePeerMachines = true' \
        >"$sandbar_claude_tmp"; then
        rm -f -- "$sandbar_claude_tmp"
        return 1
      fi
      chmod 0644 "$sandbar_claude_tmp" || return $?
    fi
    mv -f -- "$sandbar_claude_tmp" "$sandbar_claude_settings" || return $?
    (umask 077 && : >"$sandbar_claude_marker") || return $?

    if [ "$sandbar_claude_enabled" = true ]; then
      printf '\nRemote Control is enabled. Open this session from claude.ai/code\n'
      printf 'or the Claude app after signing in below.\n'
    else
      printf '\nRemote Control remains off. Run /remote-control in any session\n'
      printf 'if you want to enable it later.\n'
    fi
  fi

  # Claude Code can require a relaunch after its first login before bypass mode
  # (and, when selected above, Remote Control) takes effect. Show this once for
  # interactive agent launches, but never interrupt scripts or print mode.
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
  command claude --dangerously-skip-permissions "$@"
}
