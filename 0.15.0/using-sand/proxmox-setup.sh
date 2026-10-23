#!/usr/bin/env bash
#
# sandbar — one-shot Proxmox VE setup.
#
# Creates the dedicated pool, the least-privilege roles, the user and API
# token, and every ACL described in the "Proxmox VE" page of the sandbar docs.
# Run it ON the Proxmox host (or any machine with `pveum`/`pvesh` and cluster
# access) as a full admin — usually root@pam.
#
# Re-running is safe: the pool, roles and ACLs are converged in place. The one
# exception is the API token, whose secret Proxmox reveals exactly once — an
# existing token is left alone unless you set SAND_TOKEN_RECREATE=1.
#
# It builds ONE pool per run, named by SAND_POOL. A second, fully isolated pool
# — for the opt-in e2e suite, or anything else that must never be able to touch
# your day-to-day VMs — is the SAME run with a different name, not a different
# procedure: the user, the token and every ACL are derived from the pool, so two
# runs produce two scopes that cannot see each other.
#
#   ./proxmox-setup.sh                            # the pool sand uses: sandbar
#   SAND_DISK_STORAGE=tank ./proxmox-setup.sh
#   SAND_POOL=sandbar-test ./proxmox-setup.sh     # an isolated pool for the e2e suite

set -euo pipefail

# --------------------------------------------------------------- settings --
# Everything you might need to change lives in this block.

# The PVE node name — the identifier in /nodes/<node>/… paths, which is not
# always the same string as the host you point sand at.
SAND_NODE="${SAND_NODE:-$(hostname -s)}"

# The dedicated pool. Every VM sand creates lands in it, and the token is scoped
# to it — this name is the whole isolation boundary, and it is the ONE setting
# you change to build a second, unrelated scope (e.g. sandbar-test for the e2e
# suite).
SAND_POOL="${SAND_POOL:-sandbar}"

# The user that owns the token, and the token's id. The user DEFAULTS TO THE
# POOL NAME, and that default is what makes two runs genuinely isolated rather
# than two names for one set of rights: the confined ACLs are granted to the
# user, so pointing a second pool's run at the same user would hand that one
# user both pools. Override it only if you know you want that.
SAND_USER="${SAND_USER:-${SAND_POOL}@pve}"
SAND_TOKEN_ID="${SAND_TOKEN_ID:-prov}"

# Storage backing VM disks (must support content type "images"), and the
# file-based storage the cloud image is downloaded to (must support "import" —
# dir/NFS/CIFS only). Set both to the same name if one storage serves both.
SAND_DISK_STORAGE="${SAND_DISK_STORAGE:-local-lvm}"
SAND_IMAGE_STORAGE="${SAND_IMAGE_STORAGE:-local}"

# The Linux bridge VMs attach to, and optionally the VLAN tag the profile uses
# (leave empty for untagged — the grant is then made on the bridge itself).
SAND_BRIDGE="${SAND_BRIDGE:-vmbr0}"
SAND_BRIDGE_VLAN="${SAND_BRIDGE_VLAN:-}"

# Where to write the token file, if you are running this on the same machine
# that runs sand. Empty (the default) just prints the line to copy across.
SAND_TOKEN_OUT="${SAND_TOKEN_OUT:-}"

# Set to 1 to delete and recreate an existing token — the only way to learn a
# secret again, and it invalidates the old one everywhere it is configured.
SAND_TOKEN_RECREATE="${SAND_TOKEN_RECREATE:-0}"

# Role names. Change these only if the names collide with roles you already
# have — the privilege sets below are what sand actually needs.
#
# They are deliberately NOT derived from the pool: a role is a privilege set and
# grants nothing until an ACL binds it to a user at a path, so every pool can
# share one definition. A second run simply reconverges them.
SAND_ROLE="${SAND_ROLE:-SandbarProv}"
SAND_NET_ROLE="${SAND_NET_ROLE:-SandbarNet}"
SAND_NODE_ROLE="${SAND_NODE_ROLE:-SandbarNode}"

# The minimum-privilege set: create a base VM from a cloud image, clone it,
# resize, configure cloud-init, power on and off, snapshot, read node stats,
# and run a guest-agent command. Nothing more. Drop
# VM.GuestAgent.Unrestricted if you never need `sand` to exec via the agent.
SAND_PRIVS="${SAND_PRIVS:-\
VM.Allocate VM.Clone VM.Audit VM.PowerMgmt VM.Snapshot \
VM.Config.Disk VM.Config.CPU VM.Config.Memory VM.Config.Network \
VM.Config.Options VM.Config.Cloudinit VM.Config.HWType VM.Config.CDROM \
VM.GuestAgent.Audit VM.GuestAgent.Unrestricted \
Datastore.AllocateSpace Datastore.AllocateTemplate Datastore.Audit Pool.Audit}"

# ---------------------------------------------------------------- helpers --

step() { printf '\n==> %s\n' "$*"; }
info() { printf '    %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# json_str <key> — pull one string field out of PVE's compact JSON on stdin.
# Avoids a jq dependency, which Proxmox does not install by default.
json_str() {
  sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -n1
}

# storage_supports <storage> <content> — true if that storage advertises the
# content type (PVE filters `pvesm status` by it).
storage_supports() {
  pvesm status --content "$2" 2>/dev/null | awk 'NR>1 {print $1}' | grep -qx "$1"
}

ensure_role() { # ensure_role <role> <privs>
  if pveum role add "$1" --privs "$2" >/dev/null 2>&1; then
    info "role $1 created"
  else
    pveum role modify "$1" --privs "$2"
    info "role $1 updated"
  fi
}

ensure_pool() { # ensure_pool <pool> <comment>
  if pveum pool add "$1" --comment "$2" >/dev/null 2>&1; then
    info "pool $1 created"
  else
    info "pool $1 already exists"
  fi
}

ensure_user() { # ensure_user <user>
  if pveum user add "$1" --comment "sandbar automation" >/dev/null 2>&1; then
    info "user $1 created"
  else
    info "user $1 already exists"
  fi
}

# token_exists <user> <token-id> — true if that API token is already present.
# PVE reveals a token's secret only at creation, so this is what decides between
# "created it and here is the value" and "it is already there, reuse your file".
token_exists() {
  pveum user token list "$1" --output-format json 2>/dev/null |
    grep -q "\"tokenid\"[[:space:]]*:[[:space:]]*\"$2\""
}

grant() { # grant <path> <role> <user>
  pveum acl modify "$1" --roles "$2" --users "$3"
  info "$2 on $1"
}

# ------------------------------------------------------------- preflight ---

step "Checking the host"

command -v pveum >/dev/null 2>&1 ||
  die "pveum not found — run this on the Proxmox host, as an admin (root@pam)"

pve_major="$(pveversion | sed -n 's|^pve-manager/\([0-9]\{1,\}\)\..*|\1|p')"
if ! printf '%s' "$pve_major" | grep -q '^[0-9]\{1,\}$' || [ "$pve_major" -lt 9 ]; then
  die "sand needs Proxmox VE 9.0 or newer (found: $(pveversion))"
fi
info "$(pveversion)"

pvesh get "/nodes/$SAND_NODE/status" >/dev/null 2>&1 ||
  die "no such node '$SAND_NODE' — set SAND_NODE to the name shown in the PVE tree"
info "node $SAND_NODE"

storage_supports "$SAND_DISK_STORAGE" images ||
  die "storage '$SAND_DISK_STORAGE' does not exist or does not hold disk images — set SAND_DISK_STORAGE"
info "disk storage $SAND_DISK_STORAGE (images)"

if storage_supports "$SAND_IMAGE_STORAGE" import; then
  info "image storage $SAND_IMAGE_STORAGE (import)"
else
  warn "storage '$SAND_IMAGE_STORAGE' does not advertise content type 'import', so the
         one-time cloud-image download will fail. If it is a directory/NFS/CIFS
         storage, add the type — keeping whatever it already lists — with:

           pvesm set $SAND_IMAGE_STORAGE --content iso,vztmpl,backup,import

         Otherwise point SAND_IMAGE_STORAGE at a file-based storage. Continuing."
fi

ip -o link show "$SAND_BRIDGE" >/dev/null 2>&1 ||
  warn "bridge '$SAND_BRIDGE' not found on this node — continuing, but check SAND_BRIDGE"

# ----------------------------------------------------------------- roles ---
# Shared by every pool this script sets up, so they are created once.

step "Creating the least-privilege roles"
ensure_role "$SAND_ROLE" "$SAND_PRIVS"
ensure_role "$SAND_NET_ROLE" "SDN.Use"
ensure_role "$SAND_NODE_ROLE" "Sys.AccessNetwork Sys.Audit"

# ----------------------------------------------------------------- scope ---

# ----------------------------------------------------------------- scope ---
# The pool, its user, its token, and the ACLs that confine that user to it.
# Everything below is derived from SAND_POOL, so a second run under a different
# name builds a second scope that shares nothing with this one but the roles.

full_token_id="${SAND_USER}!${SAND_TOKEN_ID}"

step "Pool $SAND_POOL"
ensure_pool "$SAND_POOL" "sandbar-managed VMs"

# Add the VM disk storage to the pool so the role's Datastore privileges apply
# to it. Errors if it is already a member, which is fine — and two pools may
# share one storage: a pool holds VMIDs and whole storages, so this grants the
# role's Datastore privileges on that storage, never a pool-private slice of it.
if pveum pool modify "$SAND_POOL" --storage "$SAND_DISK_STORAGE" >/dev/null 2>&1; then
  info "storage $SAND_DISK_STORAGE added to the pool"
else
  info "storage $SAND_DISK_STORAGE already in the pool"
fi

step "User and API token"
ensure_user "$SAND_USER"

token_value=""
if token_exists "$SAND_USER" "$SAND_TOKEN_ID"; then
  if [ "$SAND_TOKEN_RECREATE" = 1 ]; then
    pveum user token remove "$SAND_USER" "$SAND_TOKEN_ID" >/dev/null
    info "removed the existing token $full_token_id"
  else
    warn "token $full_token_id already exists and its secret cannot be read back.
         Keeping it — reuse the token file you saved when it was created, or
         re-run with SAND_TOKEN_RECREATE=1 to replace it (which invalidates
         the old secret everywhere it is configured)."
  fi
fi

if ! token_exists "$SAND_USER" "$SAND_TOKEN_ID"; then
  # --privsep 0: the token inherits the user's rights. With separation on, a
  # token's rights are the INTERSECTION of its own and its user's — and this
  # user exists solely to own the token, so that intersection would be empty.
  # The confined ACLs below are granted to the user for that reason.
  token_value="$(pveum user token add "$SAND_USER" "$SAND_TOKEN_ID" --privsep 0 \
    --output-format json | json_str value)"
  [ -n "$token_value" ] || die "could not read the new token's value for $full_token_id"
  info "token $full_token_id created"
fi

step "Granting the confined ACLs to $SAND_USER"
# The pool: this is the whole isolation boundary.
grant "/pool/$SAND_POOL" "$SAND_ROLE" "$SAND_USER"

# Three things a pool cannot hold, granted at the narrowest path that works.
# A plain Linux bridge lives under the synthetic SDN zone "localnetwork"; with
# a VLAN tag the path gains the tag as a further segment.
sdn_path="/sdn/zones/localnetwork/$SAND_BRIDGE"
if [ -n "$SAND_BRIDGE_VLAN" ]; then
  sdn_path="$sdn_path/$SAND_BRIDGE_VLAN"
fi
grant "$sdn_path" "$SAND_NET_ROLE" "$SAND_USER"
grant "/nodes/$SAND_NODE" "$SAND_NODE_ROLE" "$SAND_USER"

# Storage privileges on both storages sand uses — the disk storage to allocate
# VM disks, the image storage to download the cloud image.
grant "/storage/$SAND_DISK_STORAGE" "$SAND_ROLE" "$SAND_USER"
if [ "$SAND_IMAGE_STORAGE" != "$SAND_DISK_STORAGE" ]; then
  grant "/storage/$SAND_IMAGE_STORAGE" "$SAND_ROLE" "$SAND_USER"
fi

step "Verifying the scope of $full_token_id"
perms="$(pveum user permissions "$full_token_id" --output-format json)"
printf '%s\n' "$perms"
case "$perms" in
'{}' | '')
  warn "the token has NO effective permissions. This is almost always a
         privilege-separated (--privsep 1) token whose ACLs were granted to the
         token instead of the user. Re-run with SAND_TOKEN_RECREATE=1."
  ;;
esac
if printf '%s' "$perms" | grep -q '"/"[[:space:]]*:'; then
  warn "a permission is granted at '/' for $SAND_USER — the isolation guarantee
         does NOT hold. Find and remove that over-broad grant:
         pveum acl list"
fi
# The pool must be the ONLY pool this token can see. A second scope's pool
# showing up here means both runs were pointed at one user (see SAND_USER).
if printf '%s' "$perms" | grep -o '"/pool/[^"]*"' | grep -qv "\"/pool/$SAND_POOL\""; then
  warn "this token can see a pool other than $SAND_POOL. Two scopes must not
         share a user — re-run the other pool with its own SAND_USER."
fi

if [ -n "$token_value" ] && [ -n "$SAND_TOKEN_OUT" ]; then
  (
    umask 077
    printf '%s=%s\n' "$full_token_id" "$token_value" >"$SAND_TOKEN_OUT"
  )
  chmod 600 "$SAND_TOKEN_OUT"
  info "token file written to $SAND_TOKEN_OUT (mode 600)"
fi

# --------------------------------------------------------------- summary ---
#
# The pool this run built can serve either purpose, and the script has no way to
# know which you meant — so it prints the mapping for both. Take the half you
# need and ignore the other.

# Display text only — this string is pasted into the reader's own shell, which
# is what expands the tilde. The path this script may WRITE to is
# SAND_TOKEN_OUT, which is used verbatim.
# shellcheck disable=SC2088
token_file="~/.config/sandbar/${SAND_POOL}.token"

step "Done — finish the setup on the machine that runs sand"

if [ -n "$token_value" ]; then
  cat <<EOF

The token secret below is shown ONCE and cannot be retrieved again. Save it on
the machine that runs sand — the file is named after the POOL, so a second
scope's token never overwrites this one:

  mkdir -p ~/.config/sandbar
  ( umask 077; printf '%s\n' '${full_token_id}=${token_value}' > ${token_file} )
  chmod 600 ${token_file}
EOF
else
  cat <<EOF

No new token was created, so there is no secret to print. Reuse the token file
you saved when the existing token was created.
EOF
fi

cat <<EOF

If this pool is for everyday use, add it to your profiles.yaml
(~/.config/sandbar/profiles.yaml) — id is yours to choose, and must be unique
across profiles:

  profiles:
    - id: ${SAND_NODE}
      name: proxmox
      type: proxmox
      enabled: true
      host: $(hostname -f 2>/dev/null || hostname)
      node: ${SAND_NODE}
      pool: ${SAND_POOL}
      storage: ${SAND_DISK_STORAGE}
      image_storage: ${SAND_IMAGE_STORAGE}
      bridge: ${SAND_BRIDGE}
      token_file: ${token_file}
      identity_path: ~/.ssh/id_ed25519   # the key sand installs and connects with
      # insecure: true                   # only if the PVE cert is self-signed

If it is the isolated pool for the opt-in e2e suite, this is its environment:

  export PROXMOX_E2E=1
  export PROXMOX_E2E_HOST=$(hostname -f 2>/dev/null || hostname)
  export PROXMOX_E2E_NODE=${SAND_NODE}
  export PROXMOX_E2E_POOL=${SAND_POOL}
  export PROXMOX_E2E_STORAGE=${SAND_DISK_STORAGE}
  export PROXMOX_E2E_IMAGE_STORAGE=${SAND_IMAGE_STORAGE}
  export PROXMOX_E2E_BRIDGE=${SAND_BRIDGE}
  export PROXMOX_E2E_TOKEN_FILE=${token_file}
  export PROXMOX_E2E_SSH_USER=debian
  export PROXMOX_E2E_SSH_IDENTITY=~/.ssh/id_ed25519

To build the OTHER one, run this again with a different pool name — same steps,
same script, a scope that shares nothing with this one:

  SAND_POOL=<the other pool> $0
EOF
