#!/usr/bin/env bash
# build-base-image.sh — Build sandbar's all-tools base image.
#
# Turns an upstream Debian 13 (trixie) genericcloud qcow2 into a complete,
# generalized, compressed sandbar base image by running the FULL base-phase
# Ansible playbook (site.yml, provision_phase=base) inside a chroot over the
# image's own root filesystem, mounted via qemu-nbd. This is both what CI
# runs (.github/workflows/base-image.yml) and the local image build facility
# for sandbar's own development — see docs/contributing/image-build-audit.md.
#
# The guest is NEVER booted. Cloud-init's first-boot behaviour is untouched,
# and this needs no virtualization beyond a kernel NBD client — the same
# property .github/workflows/base-image.yml already relied on before this
# script existed. Do not reach for libguestfs/virt-* here: they are broken on
# the 24.04 GitHub runner (Debian #1086844), which is the whole reason this
# nbd+chroot approach exists.
#
# A chroot shares the host's network namespace, so apt, get_url, and every
# `curl | sh` vendor installer the playbook runs behave normally. What a
# chroot does NOT have is a running init: no systemd, no D-Bus, nothing
# listening on logind's or systemd's sockets. `docs/contributing/
# image-build-audit.md` catalogs every base/user/dev-tools task that needed
# an offline equivalent for that reason, behind the `sand_image_build` flag
# this script passes as an extra-var. Two things this script must never do,
# because the audit depends on them:
#   - bind-mount the host's /run into the chroot (Ansible's hostname module
#     would then see the CI runner's /run/systemd/system and try to shell out
#     to hostnamectl against a chroot with no reachable D-Bus);
#   - let `docker.socket`/`docker.service` reach systemd_service's
#     `state: started` (no live init to start anything) — enablement for
#     those units is instead done here, from OUTSIDE the chroot, with
#     `systemctl --root=`.
set -Eeuo pipefail

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
ARCH="amd64"
OUT=""
MAX_SIZE_MIB=1900
SRC_URL=""

usage() {
  cat <<'EOF'
Usage: build-base-image.sh --arch {amd64|arm64} --out PATH [options]

Builds a complete, generalized, compressed sandbar base image from an
upstream Debian 13 (trixie) genericcloud qcow2, by mounting it with
qemu-nbd and running the full base-phase Ansible playbook inside a chroot.
Never boots a guest.

Required:
  --arch {amd64|arm64}    Target architecture. Must match this host's own
                           architecture: this script chroots into the
                           image's own binaries and never cross-executes, so
                           build the amd64 image on an amd64 host/runner and
                           the arm64 image on an arm64 host/runner (e.g.
                           GitHub's ubuntu-24.04-arm).
  --out PATH               Where to write the finished, compressed qcow2.

Options:
  --max-size-mib N         Fail if the compressed image exceeds N MiB.
                            Default: 1900 (conservatively below GitHub's
                            2 GiB per-release-asset limit).
  --src-url URL_OR_PATH     Use this upstream image instead of downloading
                            the latest Debian genericcloud release. Accepts
                            an http(s):// URL or a local file path — handy
                            for fast local iteration on a fixed source image.
  -h, --help                Show this help.

Must be run as root (e.g. via sudo) on a Linux/amd64 or Linux/arm64 host.
Does not work on macOS: there is no usable qemu-nbd NBD-device export there.
Only qemu-utils is assumed preinstalled; this script installs any other
host-side tool it needs itself (parted, gdisk, cloud-guest-utils) via apt.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --arch)
      ARCH="$2"
      shift 2
      ;;
    --arch=*)
      ARCH="${1#*=}"
      shift
      ;;
    --out)
      OUT="$2"
      shift 2
      ;;
    --out=*)
      OUT="${1#*=}"
      shift
      ;;
    --max-size-mib)
      MAX_SIZE_MIB="$2"
      shift 2
      ;;
    --max-size-mib=*)
      MAX_SIZE_MIB="${1#*=}"
      shift
      ;;
    --src-url)
      SRC_URL="$2"
      shift 2
      ;;
    --src-url=*)
      SRC_URL="${1#*=}"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "error: unrecognized argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [ -z "$OUT" ]; then
  echo "error: --out is required" >&2
  usage >&2
  exit 2
fi
case "$ARCH" in
  amd64 | arm64) ;;
  *)
    echo "error: --arch must be 'amd64' or 'arm64', got '$ARCH'" >&2
    exit 2
    ;;
esac
case "$MAX_SIZE_MIB" in
  '' | *[!0-9]*)
    echo "error: --max-size-mib must be a positive integer, got '$MAX_SIZE_MIB'" >&2
    exit 2
    ;;
esac

# ---------------------------------------------------------------------------
# Host support checks
# ---------------------------------------------------------------------------
if [ "$(uname -s)" != "Linux" ]; then
  echo "error: build-base-image.sh only works on Linux. qemu-nbd needs a kernel" >&2
  echo "NBD client (the nbd module) to export a qcow2 as a /dev/nbdN block" >&2
  echo "device; macOS has no equivalent. Run this from a Linux host or CI runner." >&2
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "error: build-base-image.sh must run as root — it loads a kernel module," >&2
  echo "connects an NBD device, mounts a filesystem, and chroots into it." >&2
  echo "Re-run with sudo." >&2
  exit 1
fi

case "$ARCH" in
  amd64) want_uname="x86_64" ;;
  arm64) want_uname="aarch64" ;;
esac
host_uname="$(uname -m)"
if [ "$host_uname" != "$want_uname" ]; then
  echo "error: --arch $ARCH needs a $want_uname host: this script chroots" >&2
  echo "directly into the target image's own binaries and never cross-executes" >&2
  echo "(no qemu-user/binfmt support here, by design — the whole point of this" >&2
  echo "approach is to avoid ever needing an emulated CPU). This host is" >&2
  echo "$host_uname. Build amd64 on an amd64 host/runner and arm64 on an" >&2
  echo "arm64 host/runner (e.g. GitHub's ubuntu-24.04-arm)." >&2
  exit 1
fi

# Map the required host-side command to the Debian package that provides it,
# for commands NOT guaranteed present on a "plain Ubuntu runner with only
# qemu-utils installed" (the environment this script must work in — see the
# technical requirements this script was written against). Core tools
# (mount, umount, chroot, blkid, dd, sha256sum, systemctl, ...) are always
# present on any Linux host and are not in this list.
declare -A HOST_PKG_FOR_CMD=(
  [qemu-img]=qemu-utils
  [qemu-nbd]=qemu-utils
  [partprobe]=parted
  [sgdisk]=gdisk
  [growpart]=cloud-guest-utils
  [curl]=curl
  [rsync]=rsync
)
missing_pkgs=()
for cmd in "${!HOST_PKG_FOR_CMD[@]}"; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    missing_pkgs+=("${HOST_PKG_FOR_CMD[$cmd]}")
  fi
done
if [ "${#missing_pkgs[@]}" -gt 0 ]; then
  # Deduplicate.
  mapfile -t missing_pkgs < <(printf '%s\n' "${missing_pkgs[@]}" | sort -u)
  echo "==> Installing missing host-side tools: ${missing_pkgs[*]}" >&2
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq --no-install-recommends "${missing_pkgs[@]}"
fi
for cmd in modprobe blkid udevadm mount umount chroot systemctl sha256sum truncate find; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "error: required command '$cmd' not found on PATH" >&2
    exit 1
  fi
done

# ---------------------------------------------------------------------------
# Workspace and teardown
# ---------------------------------------------------------------------------
WORKDIR="$(mktemp -d /var/tmp/sandbar-build-base-image.XXXXXX)"
MOUNT="$WORKDIR/mnt"
NBD_DEV="/dev/nbd0"
WORK_QCOW2="$WORKDIR/work.qcow2"
BASE_DISK_SIZE="20G" # matches vm.BaseDiskFloor (internal/vm/vm.go) — clones only grow from here.
IMAGE_USER="claude"  # matches roles/user/defaults/main.yml's user_name default; the base image carries no per-host identity, so this is never overridden.

# Each variable below tracks whether the resource it names is currently held,
# so teardown() is safe to call more than once (it is: once explicitly before
# compression, once more from the EXIT trap for any earlier failure) and safe
# to run back-to-back across two separate invocations of this script — a
# stale /dev/nbd0 connection or mount left by an interrupted run must never
# block the next one.
nbd_connected=0
binds_mounted=0
root_mounted=0
policy_rc_installed=0
resolv_replaced=0
playbook_staged=0

# Idempotent teardown: unwinds exactly the resources still marked held, in
# reverse dependency order, and clears each flag once undone. Called both
# mid-script (right after generalization, so the qcow2 is fully closed
# before qemu-img convert touches it) and from the EXIT trap, so a failure at
# any point still leaves the host clean for the next run.
teardown() {
  if [ "$playbook_staged" = 1 ]; then
    rm -rf "$MOUNT/root/playbook" 2>/dev/null || true
    playbook_staged=0
  fi
  if [ "$policy_rc_installed" = 1 ]; then
    rm -f "$MOUNT/usr/sbin/policy-rc.d" 2>/dev/null || true
    policy_rc_installed=0
  fi
  if [ "$resolv_replaced" = 1 ]; then
    rm -f "$MOUNT/etc/resolv.conf" 2>/dev/null || true
    if [ -e "$MOUNT/etc/resolv.conf.sandbar-orig" ] || [ -L "$MOUNT/etc/resolv.conf.sandbar-orig" ]; then
      mv "$MOUNT/etc/resolv.conf.sandbar-orig" "$MOUNT/etc/resolv.conf"
    fi
    resolv_replaced=0
  fi
  if [ "$binds_mounted" = 1 ]; then
    umount "$MOUNT/dev" 2>/dev/null || true
    umount "$MOUNT/proc" 2>/dev/null || true
    umount "$MOUNT/sys" 2>/dev/null || true
    binds_mounted=0
  fi
  if [ "$root_mounted" = 1 ]; then
    umount "$MOUNT" 2>/dev/null || true
    root_mounted=0
  fi
  if [ "$nbd_connected" = 1 ]; then
    qemu-nbd --disconnect "$NBD_DEV" >/dev/null 2>&1 || true
    nbd_connected=0
  fi
}

cleanup_on_exit() {
  teardown
  rm -rf "$WORKDIR" 2>/dev/null || true
}
trap cleanup_on_exit EXIT

# Defensive pre-clean: disconnect anything a PREVIOUS, uncleanly-killed run
# (kill -9, an OOM, a runner timeout) left attached to our fixed nbd device,
# before we touch it ourselves. Unmount deepest-first so the device is free
# by the time we disconnect it. This, not anything in teardown() above (which
# only ever sees resources THIS run acquired), is what makes back-to-back
# runs of this script idempotent.
modprobe nbd max_part=16
stale_mounts="$(awk -v d="$NBD_DEV" '$1 ~ "^"d {print $2}' /proc/mounts | sort -r || true)"
if [ -n "$stale_mounts" ]; then
  echo "==> Clearing stale mount(s) left on $NBD_DEV by a previous run" >&2
  while IFS= read -r m; do
    if [ -n "$m" ]; then
      umount "$m" 2>/dev/null || true
    fi
  done <<<"$stale_mounts"
fi
qemu-nbd --disconnect "$NBD_DEV" >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
# Acquire the upstream image
# ---------------------------------------------------------------------------
CACHE_DIR="${SANDBAR_IMAGE_CACHE_DIR:-$HOME/.cache/sandbar/build-base-image}"
mkdir -p "$CACHE_DIR"

download_to_cache() {
  local url="$1" fname dest tmp
  fname="$(basename "${url%%\?*}")"
  dest="$CACHE_DIR/$fname"
  if [ -s "$dest" ]; then
    echo "==> Using cached upstream image: $dest" >&2
    SRC_PATH="$dest"
    return
  fi
  echo "==> Downloading upstream image: $url" >&2
  tmp="$dest.part"
  rm -f "$tmp"
  # Download to a .part sibling and rename only on full success, so a
  # partial download from an interrupted run is never mistaken for a
  # complete, cached image on the next one.
  curl -fSL --retry 3 --retry-delay 5 -o "$tmp" "$url"
  mv "$tmp" "$dest"
  SRC_PATH="$dest"
}

SRC_PATH=""
if [ -n "$SRC_URL" ]; then
  case "$SRC_URL" in
    http://* | https://*)
      download_to_cache "$SRC_URL"
      ;;
    *)
      if [ ! -f "$SRC_URL" ]; then
        echo "error: --src-url '$SRC_URL' is neither an http(s):// URL nor an existing local file" >&2
        exit 1
      fi
      SRC_PATH="$SRC_URL"
      ;;
  esac
else
  download_to_cache "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-${ARCH}.qcow2"
fi

echo "==> Preparing working copy of $SRC_PATH" >&2
cp "$SRC_PATH" "$WORK_QCOW2"

# ---------------------------------------------------------------------------
# Resize BEFORE mounting: stock genericcloud is a couple of GiB and the
# full toolset will not fit. Getting this wrong (or forgetting it) wastes a
# very long build on an ENOSPC failure deep inside the playbook.
# ---------------------------------------------------------------------------
echo "==> Resizing working image to $BASE_DISK_SIZE" >&2
qemu-img resize "$WORK_QCOW2" "$BASE_DISK_SIZE"

# ---------------------------------------------------------------------------
# Connect nbd, find the root partition, grow the partition and filesystem,
# mount. Lifted from .github/workflows/base-image.yml's proven scaffold: the
# blkid retry loop exists because racing udev-populated lsblk was unreliable.
# ---------------------------------------------------------------------------
echo "==> Connecting $WORK_QCOW2 to $NBD_DEV" >&2
qemu-nbd --connect="$NBD_DEV" "$WORK_QCOW2"
nbd_connected=1

root=""
for _ in $(seq 1 20); do
  partprobe "$NBD_DEV" 2>/dev/null || true
  udevadm settle 2>/dev/null || true
  root=$(blkid -o device -t TYPE=ext4 2>/dev/null | grep -E "^${NBD_DEV}" | head -1 || true)
  [ -n "$root" ] && break
  sleep 1
done
lsblk -f "$NBD_DEV" || true
echo "root partition: ${root:-<none>}" >&2
if [ -z "$root" ]; then
  echo "error: could not find an ext4 root partition on $NBD_DEV" >&2
  exit 1
fi
partnum="${root#"${NBD_DEV}"p}"

echo "==> Growing partition $partnum and its filesystem to fill $BASE_DISK_SIZE" >&2
# The qcow2 resize above grew the virtual DISK; the GPT and the partition
# inside it are still their original, small size. sgdisk -e relocates the
# GPT's backup header to the new end of the disk first — without it, growpart
# (and several other GPT tools) refuse to touch a table whose backup header
# no longer sits at the last sector.
sgdisk -e "$NBD_DEV"
partprobe "$NBD_DEV" 2>/dev/null || true
udevadm settle 2>/dev/null || true
growpart_out="$(growpart "$NBD_DEV" "$partnum" 2>&1)" || {
  if ! echo "$growpart_out" | grep -q "NOCHANGE"; then
    echo "$growpart_out" >&2
    echo "error: growpart failed to grow partition $partnum on $NBD_DEV" >&2
    exit 1
  fi
}
echo "$growpart_out" >&2
partprobe "$NBD_DEV" 2>/dev/null || true
udevadm settle 2>/dev/null || true
e2fsck -f -y "$root"
resize2fs "$root"

mkdir -p "$MOUNT"
mount "$root" "$MOUNT"
root_mounted=1
df -h "$MOUNT" >&2

# ---------------------------------------------------------------------------
# Prepare the chroot. Never bind-mount /run — see the header comment and
# docs/contributing/image-build-audit.md Finding 2.
# ---------------------------------------------------------------------------
printf '#!/bin/sh\nexit 101\n' >"$MOUNT/usr/sbin/policy-rc.d"
chmod +x "$MOUNT/usr/sbin/policy-rc.d"
policy_rc_installed=1

mount --bind /dev "$MOUNT/dev"
mount --bind /proc "$MOUNT/proc"
mount --bind /sys "$MOUNT/sys"
binds_mounted=1

# The image's own /etc/resolv.conf is a symlink into /run (systemd-resolved's
# stub), which does not exist inside an unbooted, un-bind-mounted chroot —
# resolving it would leave DNS broken even though the network itself works.
# Replace it with a plain copy of the HOST's resolved /etc/resolv.conf (the
# chroot shares the host's network namespace, so whatever resolver the host
# uses is reachable here too), then restore the original symlink in
# teardown() so the shipped image still lets systemd-resolved manage it on
# first real boot.
if [ -e "$MOUNT/etc/resolv.conf" ] || [ -L "$MOUNT/etc/resolv.conf" ]; then
  mv "$MOUNT/etc/resolv.conf" "$MOUNT/etc/resolv.conf.sandbar-orig"
fi
cp /etc/resolv.conf "$MOUNT/etc/resolv.conf"
resolv_replaced=1

# ---------------------------------------------------------------------------
# Bootstrap ansible-core and the playbook's own dependencies into the image
# root, matching what overlayProvision installs for a Lima base today
# (internal/provision/overlay.go).
# ---------------------------------------------------------------------------
echo "==> Installing ansible-core and playbook bootstrap deps into the image root" >&2
chroot "$MOUNT" env DEBIAN_FRONTEND=noninteractive apt-get update
chroot "$MOUNT" env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  ansible-core rsync curl gnupg ca-certificates python3-passlib

# ---------------------------------------------------------------------------
# Stage the playbook fileset — exactly site.yml, ansible.cfg, inventory,
# roles, group_vars, the same set internal/provision/baseversion.go's
# playbookFileset pins for the in-guest path (TestGuestSyncCopiesOnlyThePlaybook).
# ---------------------------------------------------------------------------
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST_PLAYBOOK="$MOUNT/root/playbook"
rm -rf "$DEST_PLAYBOOK"
mkdir -p "$DEST_PLAYBOOK"
rsync -a \
  "$REPO_ROOT/site.yml" \
  "$REPO_ROOT/ansible.cfg" \
  "$REPO_ROOT/inventory" \
  "$REPO_ROOT/roles" \
  "$REPO_ROOT/group_vars" \
  "$DEST_PLAYBOOK/"
playbook_staged=1

EXTRA_VARS_FILE="$DEST_PLAYBOOK/.sandbar-image-build-vars.yml"
cat >"$EXTRA_VARS_FILE" <<EOF
provision_phase: base
sand_image_build: true
samba_enabled: false
toolset_claude: true
toolset_codex: true
toolset_ddev: true
toolset_go: true
toolset_java: true
EOF

# ---------------------------------------------------------------------------
# Run the full base phase inside the chroot. Network works (shared host
# netns), so the apt repos registered by roles/base, and the claude-code/
# codex/uv/mkcert/glab/drupalorg installers in roles/claude-code, roles/codex
# and roles/dev-tools, all behave as they do in-guest.
# ---------------------------------------------------------------------------
echo "==> Running the base-phase playbook inside the chroot (this takes a long time)" >&2
chroot "$MOUNT" env -C /root/playbook \
  ansible-playbook -i localhost, --connection=local site.yml \
  --extra-vars "@$(basename "$EXTRA_VARS_FILE")"

# ---------------------------------------------------------------------------
# Unit enablement that only works from OUTSIDE the chroot (no live init to
# talk to inside it) — see docs/contributing/image-build-audit.md, "Interface
# for task 03".
# ---------------------------------------------------------------------------
echo "==> Enabling units on the offline root with systemctl --root=" >&2
systemctl --root="$MOUNT" enable docker.socket
systemctl --root="$MOUNT" disable docker.service
# qemu-guest-agent's own .deb postinst enablement is unreliable under chroot
# (no live systemd for it to talk to); make it explicit and verify it stuck,
# mirroring .github/workflows/base-image.yml's original handling of this
# exact package.
systemctl --root="$MOUNT" enable qemu-guest-agent
systemctl --root="$MOUNT" is-enabled qemu-guest-agent

# ---------------------------------------------------------------------------
# Generalize: this image is downloaded and cloned by everyone, so none of
# this is optional. See docs/contributing/image-build-audit.md Component 2
# and internal/provider/proxmoxprovision.go's generalizeScript.
# ---------------------------------------------------------------------------
echo "==> Generalizing the image" >&2

# Lock the account password. roles/user still generates and hashes a random
# password (needed for the direct, non-image `ansible-playbook site.yml`
# path), but a published image must never ship one live password shared by
# every sandbar user on earth — interactive access is by SSH key (Lima) or
# cloud-init-injected key (Proxmox), and sudo is already passwordless via
# /etc/sudoers.d/nopasswd-sudo. Locking here is the belt-and-braces backstop
# regardless of what roles/user does upstream.
chroot "$MOUNT" passwd -l "$IMAGE_USER"

# Remove the baked SSH host keys. Every VM cloned from this image would
# otherwise share one host identity. Debian regenerates them on first boot
# via ssh-keygen -A (an ExecStartPre of the ssh unit / a generator), so
# nothing else is needed here for them to come back.
rm -f "$MOUNT"/etc/ssh/ssh_host_*

# Truncate (never delete) machine-id and re-link the dbus copy if it is a
# regular file. Mirrors generalizeScript in internal/provider/proxmoxprovision.go
# verbatim: two clones sharing one machine-id derive the same RFC 4361 DHCP
# client identifier and fight over one lease.
: >"$MOUNT/etc/machine-id"
if [ -e "$MOUNT/var/lib/dbus/machine-id" ] && [ ! -L "$MOUNT/var/lib/dbus/machine-id" ]; then
  rm -f "$MOUNT/var/lib/dbus/machine-id"
  ln -s /etc/machine-id "$MOUNT/var/lib/dbus/machine-id"
fi

# Clear APT lists and caches, logs, and shell history — build residue that
# must never reach a widely distributed image.
rm -rf "$MOUNT"/var/lib/apt/lists/*
chroot "$MOUNT" apt-get clean
find "$MOUNT/var/log" -type f -delete
rm -f "$MOUNT/root/.bash_history"
rm -f "$MOUNT"/home/*/.bash_history

# Belt-and-braces: roles/base/tasks/main.yml already removes its own
# force-unsafe-io dpkg speed hack at the end of the base role (see
# "Restore safe dpkg IO before this image is used as a clone source"); make
# sure it is actually gone before this becomes a clone source.
rm -f "$MOUNT/etc/dpkg/dpkg.cfg.d/99-sand-base-speed"

# ---------------------------------------------------------------------------
# Sparsify while still mounted. ext4 does NOT zero a block's content when a
# file is deleted — only its allocation metadata changes — so every rm -rf
# above (apt lists/cache, logs, docs/man exclusions during the transaction,
# the resize2fs grow) leaves genuinely non-zero bytes sitting in now-free
# space. qemu-img convert -c only ever skips blocks it can SEE are zero; it
# has no way to know ext4 considers them free. Left alone, that garbage is
# real entropy to zstd, inflates the compressed image by a large and
# non-reproducible amount (measured: ~110 MiB of run-to-run variance on this
# exact image from this alone), and is exactly the kind of build residue
# Component 2's generalization is supposed to eliminate. Filling free space
# with real zero bytes (the manual equivalent of zerofree, needing no extra
# package) and deleting the filler makes every free block genuinely zero, so
# convert's own scan — no discard/TRIM plumbing through nbd required — finds
# it. The `|| true` is load-bearing: dd is EXPECTED to hit ENOSPC once free
# space is exhausted.
echo "==> Zero-filling free space before compression (sparsify)" >&2
dd if=/dev/zero of="$MOUNT/ZEROFILL" bs=1M status=none conv=fsync 2>/dev/null || true
rm -f "$MOUNT/ZEROFILL"

# ---------------------------------------------------------------------------
# Tear the chroot and disk down BEFORE compressing — qemu-img needs the qcow2
# file fully closed, and every step above needed the mount.
# ---------------------------------------------------------------------------
echo "==> Unmounting and disconnecting before compression" >&2
teardown

# ---------------------------------------------------------------------------
# Compress. zstd gives a materially better ratio than qcow2's zlib default
# and is read transparently by both QEMU/Lima and PVE. The sparsify step
# above is what makes convert's own zero-detection actually pay off here.
# ---------------------------------------------------------------------------
mkdir -p "$(dirname "$OUT")"
OUT_TMP="$OUT.partial"
rm -f "$OUT_TMP"
echo "==> Compressing to $OUT_TMP (qcow2, zstd)" >&2
qemu-img convert -O qcow2 -c -o compression_type=zstd "$WORK_QCOW2" "$OUT_TMP"
mv "$OUT_TMP" "$OUT"

size_bytes="$(stat -c%s "$OUT")"
size_mib="$((size_bytes / 1024 / 1024))"
sha256="$(sha256sum "$OUT" | awk '{print $1}')"

echo "==> Built $OUT"
echo "    size:   ${size_bytes} bytes (${size_mib} MiB)"
echo "    sha256: ${sha256}"

if [ "$size_mib" -gt "$MAX_SIZE_MIB" ]; then
  over=$((size_mib - MAX_SIZE_MIB))
  echo "" >&2
  echo "error: $OUT is ${size_mib} MiB, which exceeds the ${MAX_SIZE_MIB} MiB threshold by ${over} MiB." >&2
  echo "This threshold is set conservatively below GitHub's 2 GiB per-release-asset" >&2
  echo "limit. In preference order, the plan's fallbacks are:" >&2
  echo "  1. zstd compression (already applied here — if this still overflows," >&2
  echo "     the gap is larger than compression alone can close)" >&2
  echo "  2. Trim droppable bulk (golang-doc, Go's bundled src tree, similar)" >&2
  echo "  3. Split into parts reassembled by sand's own acquisition layer" >&2
  echo "     (neither Lima's images: block nor PVE's DownloadURL can fetch a" >&2
  echo "     split file directly)" >&2
  echo "  4. Publish via GHCR instead of GitHub Releases (no per-file ceiling)" >&2
  exit 1
fi
