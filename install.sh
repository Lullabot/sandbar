#!/usr/bin/env bash
#
# install.sh — one-line bootstrap for the Claude Code dev VM.
#
#   curl -fsSL https://raw.githubusercontent.com/deviantintegral/claude-code-ansible/main/install.sh | bash
#
# Clones (or updates) the playbook into a cache directory, then hands off to
# scripts/new-vm.sh, which prompts for config and starts the Lima VM. Any
# extra args are forwarded:
#
#   curl -fsSL .../install.sh | bash -s -- --name work --yes
#
set -euo pipefail

REPO_URL="https://github.com/deviantintegral/claude-code-ansible.git"
CACHE_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/claude-code-ansible"

command -v git >/dev/null 2>&1 || { echo "error: git is required" >&2; exit 1; }
command -v limactl >/dev/null 2>&1 || {
  echo "error: limactl not found. Install Lima first: https://lima-vm.io/docs/installation/" >&2
  exit 1
}

if [ -d "$CACHE_DIR/.git" ]; then
  git -C "$CACHE_DIR" fetch --tags --quiet || true
else
  git clone --quiet "$REPO_URL" "$CACHE_DIR"
fi

# Prefer the newest release tag; fall back to the default branch.
ref="$(git -C "$CACHE_DIR" tag --list --sort=-v:refname | head -n1)"
if [ -n "$ref" ]; then
  git -C "$CACHE_DIR" checkout --quiet "$ref"
else
  git -C "$CACHE_DIR" pull --ff-only --quiet || true
fi

exec bash "$CACHE_DIR/scripts/new-vm.sh" "$@"
