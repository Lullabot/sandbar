#!/usr/bin/env bash
#
# new-vm.sh — spin up a Claude Code development VM with Lima.
#
# Works two ways from a single code path:
#
#   * Run from a checkout (./scripts/new-vm.sh) -> mounts your working tree,
#     so uncommitted edits to the playbook provision the VM. This is the
#     dev loop for hacking on the playbook itself.
#
#   * Run with no checkout (curl ... | bash, or Homebrew) -> clones the repo
#     once to a cache dir and mounts that.
#
# Both paths converge on the same thing: mount a host copy of the playbook
# into the guest and run it with `ansible-playbook --connection=local`.
#
# The generated Lima config does NOT pin image digests. It inherits the
# shipped `template:_images/debian-13` (so Lima handles arch + image cache),
# and intentionally skips the default host-home mount.

set -euo pipefail

REPO_URL="https://github.com/deviantintegral/claude-code-ansible.git"
CACHE_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/claude-code-ansible"

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
info() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# Read a line from the controlling terminal even when the script is piped
# from curl (in which case stdin is the script body, not the keyboard).
read_tty() {
  local __ans=""
  if [ -r /dev/tty ]; then
    IFS= read -r __ans </dev/tty || __ans=""
  fi
  printf '%s' "$__ans"
}

# ask VAR "Prompt" "default"
ask() {
  local __var="$1" __prompt="$2" __default="${3:-}" __ans
  if [ "$ASSUME_YES" = "1" ]; then
    eval "$__var=\$__default"
    return
  fi
  if [ -n "$__default" ]; then
    printf '%s [%s]: ' "$__prompt" "$__default" >&2
  else
    printf '%s: ' "$__prompt" >&2
  fi
  __ans="$(read_tty)"
  [ -z "$__ans" ] && __ans="$__default"
  eval "$__var=\$__ans"
}

# ask_secret VAR "Prompt"  (no echo, optional, no default)
ask_secret() {
  local __var="$1" __prompt="$2" __ans=""
  if [ "$ASSUME_YES" = "1" ]; then eval "$__var=\"\""; return; fi
  printf '%s: ' "$__prompt" >&2
  if [ -r /dev/tty ]; then IFS= read -rs __ans </dev/tty || __ans=""; fi
  printf '\n' >&2
  eval "$__var=\$__ans"
}

# Print the recommended fine-grained GitHub token permissions.
github_token_help() {
  cat >&2 <<'TXT'
  Create a fine-grained token scoped to this repo at:
    https://github.com/settings/personal-access-tokens/new
  Recommended permissions (PRs/Issues stay read-only so the agent can't
  self-merge to main without human review):
    Contents:       Read and write
    Pull requests:  Read
    Issues:         Read
    Actions:        Read and write
    Workflows:      Read and write
TXT
}

# Quote an arbitrary string as a double-quoted YAML scalar.
yaml_str() {
  printf '"%s"' "$(printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')"
}

# ---------------------------------------------------------------------------
# Defaults / autodetection
# ---------------------------------------------------------------------------
default_cpus() {
  local n
  n="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)"
  n=$(( n / 2 ))
  [ "$n" -lt 2 ] && n=2
  printf '%s' "$n"
}

# ---------------------------------------------------------------------------
# CLI flags (all optional; anything not supplied is prompted for)
# ---------------------------------------------------------------------------
ASSUME_YES=0
RECREATE=0
REF=""
NAME="" HOSTNAME_="" USER_NAME="" GIT_NAME="" GIT_EMAIL=""
CPUS="" MEMORY="" DISK="" LOCALE="" DOMAIN=""
DOCKER_PROXY_HOST=""
CLONE_URL="" CLONE_TOKEN=""

usage() {
  cat >&2 <<'EOF'
Usage: new-vm.sh [options]

Spins up a Claude Code development VM with Lima. With no options it prompts
interactively (using sensible autodetected defaults).

Options:
  --name NAME              Lima instance name (default: claude)
  --hostname HOST          VM hostname (default: same as --name)
  --user USER              Primary VM user (default: current user, matching Lima)
  --git-name NAME          git user.name        (default: host git config)
  --git-email EMAIL        git user.email       (default: host git config)
  --cpus N                 vCPUs                 (default: half of host)
  --memory SIZE            RAM, e.g. 8GiB        (default: 8GiB)
  --disk SIZE              Disk size, e.g. 100GiB (default: 100GiB)
  --locale LOCALE          System locale         (default: host $LANG)
  --domain DOMAIN          Domain suffix         (default: lan)
  --docker-proxy-host HOST Docker registry pull-through proxy host (optional)
  --clone-url URL          HTTPS repo to clone into the VM (optional)
  --clone-token TOKEN      Token for the repo above (optional; GitHub uses it)
  --ref REF                Git tag/branch to use in standalone mode
  --recreate               If the instance exists, delete and rebuild it
                           (Lima bakes provisioning at creation, so this is the
                           only way to apply playbook/config changes to a VM)
  -y, --yes                Accept all defaults, never prompt
  -h, --help               Show this help

Required (prompted if absent): --git-name, --git-email
EOF
}

while [ $# -gt 0 ]; do
  # Guard value-taking flags so a missing value gives a clear error instead of
  # an "unbound variable" crash from "$2" under `set -u`.
  case "$1" in
    --name|--hostname|--user|--git-name|--git-email|--cpus|--memory|--disk|--locale|--domain|--docker-proxy-host|--clone-url|--clone-token|--ref)
      [ $# -ge 2 ] || die "$1 requires a value" ;;
  esac
  case "$1" in
    --name) NAME="$2"; shift 2;;
    --hostname) HOSTNAME_="$2"; shift 2;;
    --user) USER_NAME="$2"; shift 2;;
    --git-name) GIT_NAME="$2"; shift 2;;
    --git-email) GIT_EMAIL="$2"; shift 2;;
    --cpus) CPUS="$2"; shift 2;;
    --memory) MEMORY="$2"; shift 2;;
    --disk) DISK="$2"; shift 2;;
    --locale) LOCALE="$2"; shift 2;;
    --domain) DOMAIN="$2"; shift 2;;
    --docker-proxy-host) DOCKER_PROXY_HOST="$2"; shift 2;;
    --clone-url) CLONE_URL="$2"; shift 2;;
    --clone-token) CLONE_TOKEN="$2"; shift 2;;
    --ref) REF="$2"; shift 2;;
    --recreate) RECREATE=1; shift;;
    -y|--yes) ASSUME_YES=1; shift;;
    -h|--help) usage; exit 0;;
    *) die "unknown option: $1 (see --help)";;
  esac
done

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
command -v limactl >/dev/null 2>&1 || die "limactl not found. Install Lima: https://lima-vm.io/docs/installation/"

# ---------------------------------------------------------------------------
# Locate the playbook (repo mode vs standalone cache mode)
# ---------------------------------------------------------------------------
SELF_DIR=""
if [ -n "${BASH_SOURCE:-}" ] && [ -f "${BASH_SOURCE[0]}" ]; then
  SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi

PLAYBOOK_DIR=""
if [ -n "$SELF_DIR" ]; then
  TOP="$(git -C "$SELF_DIR" rev-parse --show-toplevel 2>/dev/null || true)"
  if [ -n "$TOP" ] && [ -f "$TOP/site.yml" ]; then
    PLAYBOOK_DIR="$TOP"
    info "Using the checkout at $PLAYBOOK_DIR (your working tree provisions the VM)."
  fi
fi

if [ -z "$PLAYBOOK_DIR" ]; then
  command -v git >/dev/null 2>&1 || die "git not found (needed to fetch the playbook)."
  if [ -d "$CACHE_DIR/.git" ]; then
    info "Updating cached playbook in $CACHE_DIR"
    git -C "$CACHE_DIR" fetch --tags --quiet || warn "fetch failed; using cached copy"
  else
    info "Cloning playbook to $CACHE_DIR"
    git clone --quiet "$REPO_URL" "$CACHE_DIR"
  fi
  # Pin to the newest release tag for stability; fall back to the default branch.
  if [ -z "$REF" ]; then
    REF="$(git -C "$CACHE_DIR" tag --list --sort=-v:refname | head -n1)"
  fi
  if [ -n "$REF" ]; then
    git -C "$CACHE_DIR" checkout --quiet "$REF" || warn "could not check out $REF"
    info "Using ref: $REF"
  else
    git -C "$CACHE_DIR" pull --ff-only --quiet || true
    warn "No release tags yet — tracking the default branch."
  fi
  PLAYBOOK_DIR="$CACHE_DIR"
fi

[ -f "$PLAYBOOK_DIR/site.yml" ] || die "playbook not found at $PLAYBOOK_DIR (no site.yml)"

# ---------------------------------------------------------------------------
# Gather configuration
# ---------------------------------------------------------------------------
: "${NAME:=claude}"
ask NAME "Instance name" "$NAME"
: "${HOSTNAME_:=$NAME}"
ask HOSTNAME_ "VM hostname" "$HOSTNAME_"
# Lima creates a guest user matching the host username by default; default to
# it so `limactl shell <vm>` lands directly in the fully-configured account.
LIMA_USER="$(id -un 2>/dev/null || echo "${USER:-claude}")"
: "${USER_NAME:=$LIMA_USER}"
ask USER_NAME "Primary VM user" "$USER_NAME"

[ -n "$GIT_NAME" ]  || GIT_NAME="$(git config --get user.name 2>/dev/null || true)"
ask GIT_NAME "git user.name" "$GIT_NAME"
[ -n "$GIT_EMAIL" ] || GIT_EMAIL="$(git config --get user.email 2>/dev/null || true)"
ask GIT_EMAIL "git user.email" "$GIT_EMAIL"

[ -n "$CPUS" ]   || CPUS="$(default_cpus)"
ask CPUS "vCPUs" "$CPUS"
[ -n "$MEMORY" ] || MEMORY="8GiB"
ask MEMORY "Memory" "$MEMORY"
[ -n "$DISK" ] || DISK="100GiB"
ask DISK "Disk size" "$DISK"
[ -n "$LOCALE" ] || LOCALE="${LANG:-en_US.UTF-8}"
[ -n "$DOMAIN" ] || DOMAIN="lan"

# Optional: Docker registry pull-through proxy (blank to skip).
ask DOCKER_PROXY_HOST "Docker registry proxy host (blank to skip)" "$DOCKER_PROXY_HOST"

# Optional: clone a project into the VM now (blank to skip — e.g. no repo
# access yet, or a non-HTTPS / unsupported host).
ask CLONE_URL "Clone a project? HTTPS repo URL (blank to skip)" "$CLONE_URL"
if [ -n "$CLONE_URL" ] && [ -z "$CLONE_TOKEN" ]; then
  case "$CLONE_URL" in
    *github.com*) github_token_help ;;
  esac
  ask_secret CLONE_TOKEN "Paste a token for this repo (blank for a public repo / set up later)"
fi

# Validate required values
[ -n "$GIT_NAME" ]  || die "git user.name is required (--git-name)"
[ -n "$GIT_EMAIL" ] || die "git user.email is required (--git-email)"
case "$CPUS" in
  ''|*[!0-9]*) die "cpus must be a positive integer (got: '$CPUS')";;
esac
[ "$CPUS" -ge 1 ] || die "cpus must be a positive integer (got: '$CPUS')"

# ---------------------------------------------------------------------------
# Build the Ansible vars file (written into the guest as /root/all.yml)
# ---------------------------------------------------------------------------
build_allyml() {
  printf 'user_name: %s\n'              "$(yaml_str "$USER_NAME")"
  printf 'base_hostname: %s\n'          "$(yaml_str "$HOSTNAME_")"
  printf 'base_domain: %s\n'            "$(yaml_str "$DOMAIN")"
  printf 'base_locale: %s\n'            "$(yaml_str "$LOCALE")"
  printf 'user_git_user_name: %s\n'     "$(yaml_str "$GIT_NAME")"
  printf 'user_git_user_email: %s\n'    "$(yaml_str "$GIT_EMAIL")"
  # Lima VMs have no host-home mount to share, so skip Samba.
  printf 'samba_enabled: false\n'
  if [ -n "$DOCKER_PROXY_HOST" ]; then
    printf 'devtools_docker_registry_proxy_enabled: true\n'
    printf 'devtools_docker_registry_proxy_host: %s\n' "$(yaml_str "$DOCKER_PROXY_HOST")"
  fi
  if [ -n "$CLONE_URL" ]; then
    printf 'project_clone_url: %s\n' "$(yaml_str "$CLONE_URL")"
    [ -n "$CLONE_TOKEN" ] && printf 'project_clone_token: %s\n' "$(yaml_str "$CLONE_TOKEN")"
  fi
}
ALLYML="$(build_allyml)"

# ---------------------------------------------------------------------------
# Render the Lima overlay (inherits the stock image only; adds our bits)
# ---------------------------------------------------------------------------
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/claude-vm.XXXXXX")"
chmod 700 "$WORKDIR"
OVERLAY="$WORKDIR/lima.yaml"

{
  cat <<'YAML'
# Generated by new-vm.sh — inherits the shipped Debian 13 image template so
# Lima manages image selection, arch, and caching. The default host-home
# mount is intentionally NOT inherited (this VM runs Claude unsupervised).
# The only mount is the playbook, read-only: there is NO writable host mount,
# so deleting the VM provably removes everything it produced. Move files in or
# out with `limactl copy`.
base:
- template:_images/debian-13
YAML
  printf 'cpus: %s\n' "$CPUS"
  printf 'memory: %s\n' "$(yaml_str "$MEMORY")"
  printf 'disk: %s\n' "$(yaml_str "$DISK")"
  printf 'mounts:\n'
  printf -- '- location: %s\n  mountPoint: /mnt/playbook\n  writable: false\n' "$(yaml_str "$PLAYBOOK_DIR")"
  printf 'provision:\n'
  cat <<'YAML'
- mode: dependency
  script: |
    #!/bin/bash
    set -eux -o pipefail
    # Lima re-runs provision scripts on every boot; skip the apt work once the
    # tools are present so restarts stay fast.
    if command -v ansible >/dev/null 2>&1 && command -v rsync >/dev/null 2>&1; then
      exit 0
    fi
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y ansible rsync
- mode: data
  path: /root/all.yml
  permissions: "0600"
  content: |
YAML
  printf '%s\n' "$ALLYML" | sed 's/^/    /'
  cat <<'YAML'
- mode: system
  script: |
    #!/bin/bash
    set -eux -o pipefail
    # Provision once. Lima re-runs this on every boot, so guard with a marker;
    # the marker is written only after a successful run (set -e aborts first on
    # failure, so a failed provision retries on the next start).
    marker=/var/lib/claude-vm/provisioned
    if [ -f "$marker" ]; then
      echo "Already provisioned; rm $marker and restart to re-provision."
      exit 0
    fi
    rsync -a --delete /mnt/playbook/ /root/playbook/
    cd /root/playbook
    ansible-playbook -i localhost, --connection=local site.yml \
      --extra-vars @/root/all.yml
    mkdir -p "$(dirname "$marker")"
    touch "$marker"
YAML
} > "$OVERLAY"
chmod 600 "$OVERLAY"

# ---------------------------------------------------------------------------
# Launch
# ---------------------------------------------------------------------------
# Lima bakes the merged config into the instance at creation; `limactl start`
# on an existing instance reuses that baked config and ignores this freshly
# rendered overlay. So changes only take effect on a brand-new instance.
if limactl list -q 2>/dev/null | grep -qx "$NAME"; then
  if [ "$RECREATE" = "1" ]; then
    info "Deleting existing instance '$NAME' to apply changes (--recreate)…"
    limactl delete -f "$NAME"
  else
    die "instance '$NAME' already exists — Lima won't apply config/playbook changes to it. Run 'limactl start $NAME' to use it as-is, or pass --recreate to delete and rebuild it (destroys the VM)."
  fi
fi

info "Starting Lima instance '$NAME' (this provisions the VM; first run takes a while)…"
info "Rendered config: $OVERLAY"
limactl start --name "$NAME" --tty=false "$OVERLAY"

printf '\n' >&2
info "VM '$NAME' is up."
cat >&2 <<EOF
  Shell in:     limactl shell $NAME
  Copy files:   limactl copy <src> $NAME:<dest>   (and the reverse)
  Stop / del:   limactl stop $NAME   |   limactl delete $NAME
EOF
# Static prose below: quoted heredoc so backticks/`$` are literal, not run as
# command substitution. Keep $NAME lines in the unquoted blocks above/below.
cat >&2 <<'EOF'

A typical workflow is:

 - Create a directory for your project or organization to store git checkouts
   in. For example, `mkdir -p github.com/lullabot`, and then check out projects
   there.
 - Create a fine-grained token in GitHub scoped to relevant repoitories with the
   following permissions:

   - Contents: Read and Write
   - Pull Requests: Read
   - Issues: Read
   - Actions: Read and Write
   - Workflows: Read and Write
 
 - Make sure branch protection rules are enabled to prevent merges without a
   pull request. Do not allow administrators to bypass the rules.
 - Save the generated token in the VM as GH_TOKEN in `github.com/<org>/.env`
   and run `direnv allow`.
 - Clone your project using the https URL.
 - cd into the project and run `claude`.
EOF
cat >&2 <<EOF

The VM has no writable host mount, so 'limactl delete $NAME' removes
everything it produced. Use 'limactl copy' to move files in or out.

Provisioning runs once; restarts are fast. To re-provision:
  limactl shell $NAME sudo rm -f /var/lib/claude-vm/provisioned
  limactl stop $NAME && limactl start $NAME
EOF
