# One-time remote-control onboarding for bare, interactive Codex launches.
# Subcommands, options, and non-interactive calls always reach the upstream
# executable unchanged.
codex() {
  local sandbar_codex_state_dir="$HOME/.config/sandbar"
  local sandbar_codex_marker="$sandbar_codex_state_dir/codex-remote-control-onboarding-complete"
  local sandbar_codex_reply

  if [ "$#" -ne 0 ] || [ ! -t 0 ] || [ -f "$sandbar_codex_marker" ]; then
    command codex "$@"
    return $?
  fi

  printf 'Enable Codex remote control support? [y/N] '
  IFS= read -r sandbar_codex_reply || return $?

  case "$sandbar_codex_reply" in
    y|Y)
      command codex login --device-auth || return $?
      command codex app-server daemon bootstrap --remote-control || return $?
      command systemctl --user enable sandbar-codex-app-server.service || return $?
      command codex remote-control pair || return $?

      mkdir -p -- "$sandbar_codex_state_dir" || return $?
      chmod 0700 "$sandbar_codex_state_dir" || return $?
      (umask 077 && : >"$sandbar_codex_marker") || return $?

      printf '\nRemote control is ready.\n'
      printf 'Run `codex agents` to connect locally.\n'
      printf 'Run `codex remote-control pair` to connect more devices.\n\n'
      ;;
    *)
      mkdir -p -- "$sandbar_codex_state_dir" || return $?
      chmod 0700 "$sandbar_codex_state_dir" || return $?
      (umask 077 && : >"$sandbar_codex_marker") || return $?
      ;;
  esac

  command codex
}
