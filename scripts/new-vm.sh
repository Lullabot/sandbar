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
INSTALL_URL="https://raw.githubusercontent.com/deviantintegral/claude-code-ansible/main/install.sh"

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
info() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# Print the command a user should run to re-invoke this tool with extra flags,
# matched to how they launched it. Under `curl ... | bash` the flags have to be
# threaded through `bash -s --` (a frequent gotcha — a bare `--recreate` after a
# pipe goes to bash, not to us), so spell out the whole command. install.sh sets
# CLAUDE_VM_CURL=1 when it bootstrapped us from a pipe; otherwise a missing
# SELF_DIR (script body piped straight to bash) is the fallback signal.
rerun_cmd() {
  if [ -z "${CLAUDE_VM_CURL:-}" ] && [ -n "${SELF_DIR:-}" ]; then
    printf './scripts/new-vm.sh %s' "$*"
  else
    printf 'curl -fsSL %s | bash -s -- %s' "$INSTALL_URL" "$*"
  fi
}

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
    Actions:        Read and write
    Contents:       Read and write
    Issues:         Read
    Pull requests:  Read
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
REBUILD=0
REF=""
BASE_NAME=""
NAME="" HOSTNAME_="" USER_NAME="" GIT_NAME="" GIT_EMAIL=""
CPUS="" MEMORY="" DISK="" LOCALE="" DOMAIN=""
DOCKER_PROXY_HOST=""
CLONE_URL="" CLONE_TOKEN=""

usage() {
  cat >&2 <<'EOF'
Usage: new-vm.sh [options]

Spins up a Claude Code development VM with Lima. With no options it prompts
interactively (using sensible autodetected defaults).

How it works: the heavy, identity-free setup (packages, Docker, Node, Claude
Code, …) is provisioned once into a stopped base image, then each VM is a cheap
`limactl clone` of it plus a light "finalize" pass (hostname, git identity, an
apt upgrade, optional repo clone). The base is built automatically the first
time; use --rebuild to refresh it.

Options:
  --name NAME              Lima instance name (default: claude)
  --hostname HOST          VM hostname (default: same as --name)
  --user USER              Primary VM user (default: current user, matching Lima)
  --git-name NAME          git user.name        (default: host git config)
  --git-email EMAIL        git user.email       (default: host git config)
  --cpus N                 vCPUs                 (default: half of host)
  --memory SIZE            RAM, e.g. 8GiB        (default: 8GiB)
  --disk SIZE              Disk size, e.g. 100GiB (default: 100GiB)
                           (cpus/memory/disk are set when the base image is
                           built; clones inherit them — pass with --rebuild to
                           change)
  --locale LOCALE          System locale         (default: host $LANG)
  --domain DOMAIN          Domain suffix         (default: lan)
  --docker-proxy-host HOST Docker registry pull-through proxy host (optional)
  --clone-url URL          HTTPS repo to clone into the VM (optional)
  --clone-token TOKEN      Token for the repo above (optional; GitHub uses it)
  --ref REF                Git tag/branch to use in standalone mode
  --recreate               If the named instance exists, delete and re-clone it
                           from the base image (a fast way to reset one VM)
  --rebuild                Delete and rebuild the base image first, then create
                           the VM (use after playbook/package changes)
  --base-name NAME         Base image instance name (default: claude-base)
  -y, --yes                Accept all defaults, never prompt
  -h, --help               Show this help

Required (prompted if absent): --git-name, --git-email

Passing flags over curl: a pipe sends stdin to bash, so flags must go after
`bash -s --`, not after the pipe. For example, to rebuild an existing VM:

  curl -fsSL https://raw.githubusercontent.com/deviantintegral/claude-code-ansible/main/install.sh | bash -s -- --recreate
EOF
}

while [ $# -gt 0 ]; do
  # Guard value-taking flags so a missing value gives a clear error instead of
  # an "unbound variable" crash from "$2" under `set -u`.
  case "$1" in
    --name|--hostname|--user|--git-name|--git-email|--cpus|--memory|--disk|--locale|--domain|--docker-proxy-host|--clone-url|--clone-token|--ref|--base-name)
      [ $# -ge 2 ] || die "$1 requires a value" ;;
  esac
  case "$1" in
    --name) NAME="$2"; shift 2;;
    --base-name) BASE_NAME="$2"; shift 2;;
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
    --rebuild) REBUILD=1; shift;;
    -y|--yes) ASSUME_YES=1; shift;;
    -h|--help) usage; exit 0;;
    *) die "unknown option: $1 (see --help)";;
  esac
done

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
command -v limactl >/dev/null 2>&1 || die "limactl not found. Install Lima: https://lima-vm.io/docs/installation/"
# Each VM is a `limactl clone` of a base image; bail early on a Lima too old for it.
limactl clone --help >/dev/null 2>&1 || die "your Lima is too old: 'limactl clone' is required. Upgrade Lima: https://lima-vm.io/docs/installation/"

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
: "${BASE_NAME:=claude-base}"
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
[ "$NAME" != "$BASE_NAME" ] || die "--name must differ from the base image name '$BASE_NAME' (set a different --name or --base-name)"
case "$CPUS" in
  ''|*[!0-9]*) die "cpus must be a positive integer (got: '$CPUS')";;
esac
[ "$CPUS" -ge 1 ] || die "cpus must be a positive integer (got: '$CPUS')"

# ---------------------------------------------------------------------------
# Build the Ansible vars file (written into the guest as /root/all.yml)
# ---------------------------------------------------------------------------
# phase is one of base|finalize|full and drives which tasks site.yml runs.
# The base image is identity-free, so the git identity and the project-clone
# vars (which include a token) are emitted only for the finalize/full phases —
# they are neither needed nor wanted baked into the long-lived base disk.
build_allyml() {
  local phase="$1" hostname="$2"
  printf 'user_name: %s\n'              "$(yaml_str "$USER_NAME")"
  printf 'base_hostname: %s\n'          "$(yaml_str "$hostname")"
  printf 'base_domain: %s\n'            "$(yaml_str "$DOMAIN")"
  printf 'base_locale: %s\n'            "$(yaml_str "$LOCALE")"
  printf 'provision_phase: %s\n'        "$phase"
  # Lima VMs have no host-home mount to share, so skip Samba.
  printf 'samba_enabled: false\n'
  if [ -n "$DOCKER_PROXY_HOST" ]; then
    printf 'devtools_docker_registry_proxy_enabled: true\n'
    printf 'devtools_docker_registry_proxy_host: %s\n' "$(yaml_str "$DOCKER_PROXY_HOST")"
  fi
  if [ "$phase" != "base" ]; then
    printf 'user_git_user_name: %s\n'   "$(yaml_str "$GIT_NAME")"
    printf 'user_git_user_email: %s\n'  "$(yaml_str "$GIT_EMAIL")"
    if [ -n "$CLONE_URL" ]; then
      printf 'project_clone_url: %s\n' "$(yaml_str "$CLONE_URL")"
      [ -n "$CLONE_TOKEN" ] && printf 'project_clone_token: %s\n' "$(yaml_str "$CLONE_TOKEN")"
    fi
  fi
}

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/claude-vm.XXXXXX")"
chmod 700 "$WORKDIR"

# ---------------------------------------------------------------------------
# Render the Lima overlay for the BASE image (heavy, identity-free install).
# Inherits the stock image only; clones reuse this baked config and disk.
# ---------------------------------------------------------------------------
render_base_overlay() {
  local outfile="$1"
  {
    cat <<'YAML'
# Generated by new-vm.sh — inherits the shipped Debian 13 image template so
# Lima manages image selection, arch, and caching. The default host-home
# mount is intentionally NOT inherited (these VMs run Claude unsupervised).
# The only mount is the playbook, read-only: there is NO writable host mount,
# so deleting a VM provably removes everything it produced. Move files in or
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
    build_allyml base "$BASE_NAME" | sed 's/^/    /'
    cat <<'YAML'
- mode: system
  script: |
    #!/bin/bash
    set -eux -o pipefail
    # Provision once. Lima re-runs this on every boot, so guard with a marker;
    # the marker is written only after a successful run (set -e aborts first on
    # failure, so a failed provision retries on the next start). Clones inherit
    # this marker, so the heavy install never re-runs on a cloned VM.
    marker=/var/lib/claude-vm/provisioned
    if [ -f "$marker" ]; then
      echo "Already provisioned; rm $marker and restart to re-provision."
      exit 0
    fi
    log=/var/log/claude-vm-provision.log
    rsync -a --delete /mnt/playbook/ /root/playbook/
    cd /root/playbook
    # Tee Ansible's output to a fixed log so new-vm.sh can show it on failure.
    # pipefail makes the playbook's exit status win, so a failed task still
    # aborts here and the success marker below is not written.
    if ! ansible-playbook -i localhost, --connection=local site.yml \
        --extra-vars @/root/all.yml 2>&1 | tee "$log"; then
      echo "claude-vm: PROVISIONING FAILED (see $log)" | tee -a "$log" >&2
      exit 1
    fi
    mkdir -p "$(dirname "$marker")"
    touch "$marker"
YAML
  } > "$outfile"
  chmod 600 "$outfile"
}

# ---------------------------------------------------------------------------
# Base image lifecycle helpers
# ---------------------------------------------------------------------------
instance_exists() { limactl list -q 2>/dev/null | grep -qx "$1"; }

instance_status() { limactl list "$1" --format '{{.Status}}' 2>/dev/null || true; }

ensure_stopped() {
  # `limactl clone` needs a quiescent source disk, so stop the base unless it is
  # already Stopped. Matching != "Stopped" (rather than == "Running") also
  # covers Paused/Broken states left by an interrupted run; the stop is
  # best-effort so a benign "already stopped" race never aborts the script.
  if [ "$(instance_status "$1")" != "Stopped" ]; then
    limactl stop "$1" 2>/dev/null || true
  fi
}

build_base() {
  info "Building base image '$BASE_NAME' (one-time, heavy install — first run takes a while)…"
  local overlay="$WORKDIR/base.yaml"
  render_base_overlay "$overlay"
  info "Rendered base config: $overlay"
  limactl start --name "$BASE_NAME" --tty=false "$overlay"

  # `limactl start` exits 0 even when a provision script fails; the marker is
  # written only after a fully successful run, so check it and surface the log.
  if ! limactl shell "$BASE_NAME" ls /var/lib/claude-vm/provisioned >/dev/null 2>&1; then
    warn "Base provisioning did NOT complete — the playbook failed partway through."
    warn "Last 40 lines of /var/log/claude-vm-provision.log (in the base VM):"
    limactl shell "$BASE_NAME" sudo tail -n 40 /var/log/claude-vm-provision.log >&2 2>/dev/null || true
    die "Base build failed (see the log above). Fix the cause, then retry: $(rerun_cmd --rebuild)"
  fi

  # Keep the base stopped: it is never used directly, only cloned. A clone needs
  # a quiescent source disk, so a stopped base is also a prerequisite for that.
  info "Base image '$BASE_NAME' is ready; stopping it (kept as the clone source)."
  limactl stop "$BASE_NAME"
}

# Run the light, per-clone finalize pass inside an already-started clone.
finalize_clone() {
  local name="$1"
  info "Finalizing '$name' (hostname, git identity, apt upgrade${CLONE_URL:+, repo clone})…"
  # Everything runs in ONE guest session, fed the per-clone vars over stdin so
  # the clone token never appears in argv / the process list. The vars go to
  # tmpfs (/dev/shm) and a trap removes them on exit, so the token never touches
  # the clone's persistent disk even if the playbook fails partway through.
  # We re-sync the playbook from the still-mounted host copy first, so finalize
  # reflects the current working tree even when the base image is older, then
  # run only the finalize-phase tasks (heavy roles are skipped by phase).
  if ! build_allyml finalize "$HOSTNAME_" | limactl shell "$name" sudo bash -c '
        set -eu -o pipefail
        umask 077
        vars=/dev/shm/claude-vm-finalize.yml
        trap "rm -f \"$vars\"" EXIT
        cat > "$vars"
        rsync -a --delete /mnt/playbook/ /root/playbook/
        cd /root/playbook
        ansible-playbook -i localhost, --connection=local site.yml \
          --extra-vars @"$vars" 2>&1 | tee /var/log/claude-vm-finalize.log'; then
    warn "Finalize did NOT complete — the playbook failed partway through."
    warn "Last 40 lines of /var/log/claude-vm-finalize.log (in the VM):"
    limactl shell "$name" sudo tail -n 40 /var/log/claude-vm-finalize.log >&2 2>/dev/null || true
    die "Finalize failed (see the log above). Fix the cause, then re-clone: $(rerun_cmd --recreate --name "$name")"
  fi
}

# ---------------------------------------------------------------------------
# Launch: ensure a base image exists, then clone + finalize the target VM
# ---------------------------------------------------------------------------
# Resolve the target instance FIRST, before any (expensive) base build/rebuild,
# so we never rebuild the base only to abort on an existing VM. A clone is cheap
# to recreate, so refuse an existing target unless asked to reset it — and
# --rebuild implies that reset, since it exists to rebuild the base for this VM.
if instance_exists "$NAME"; then
  if [ "$RECREATE" = "1" ] || [ "$REBUILD" = "1" ]; then
    info "Deleting existing instance '$NAME' to re-clone it…"
    limactl delete -f "$NAME"
  else
    die "instance '$NAME' already exists.
  Use it as-is:                limactl start $NAME
  Reset it (destroys the VM):  $(rerun_cmd --recreate --name "$NAME")"
  fi
fi

if [ "$REBUILD" = "1" ] && instance_exists "$BASE_NAME"; then
  info "Rebuilding base image '$BASE_NAME' (--rebuild)…"
  limactl delete -f "$BASE_NAME"
fi

if instance_exists "$BASE_NAME"; then
  info "Reusing base image '$BASE_NAME' (refresh it with $(rerun_cmd --rebuild))."
  ensure_stopped "$BASE_NAME"
else
  build_base
fi

info "Cloning '$BASE_NAME' → '$NAME'…"
limactl clone "$BASE_NAME" "$NAME"
info "Starting '$NAME'…"
limactl start "$NAME"
finalize_clone "$NAME"

# Bounce the VM so the first interactive shell starts cleanly: the finalize
# apt upgrade may have pulled a new kernel/libraries, and the hostname change
# takes full effect on a fresh boot. The base provision marker is inherited, so
# this restart is fast (no re-provision).
info "Restarting '$NAME' so the first shell picks up any kernel/library updates and the new hostname…"
limactl stop "$NAME"
limactl start "$NAME"

printf '\n' >&2
info "VM '$NAME' is up (cloned from '$BASE_NAME')."
cat >&2 <<EOF
  Shell in:     limactl shell $NAME
  Copy files:   limactl copy <src> $NAME:<dest>   (and the reverse)
  Stop / del:   limactl stop $NAME   |   limactl delete $NAME
EOF

cat >&2 <<EOF

The VM has no writable host mount, so 'limactl delete $NAME' removes
everything it produced. Use 'limactl copy' to move files in or out.

Each VM is a clone of the base image '$BASE_NAME'. To pick up playbook or
package changes, rebuild the base and re-clone:
  $(rerun_cmd --rebuild --name "$NAME")
EOF
