#!/usr/bin/env bash
# Inspect a distributable base image without booting or modifying it.
set -euo pipefail

die() { echo "check-base-image: $*" >&2; exit 1; }
log() { echo "check-base-image: $*" >&2; }

if (($# != 1)) || [[ $1 == -h || $1 == --help ]]; then
  echo 'Usage: sudo scripts/check-base-image.sh IMAGE.qcow2' >&2
  (($# == 1)) && [[ $1 == -h || $1 == --help ]] && exit 0
  exit 2
fi
((EUID == 0)) || die 'run with sudo'
[[ -f $1 ]] || die "image not found: $1"
image=$(realpath -e -- "$1")

for cmd in qemu-img qemu-nbd modprobe blkid mount umount mountpoint \
           udevadm chroot passwd find grep awk readlink flock; do
  command -v "$cmd" >/dev/null || die "missing host command: $cmd"
done
qemu-img info -f qcow2 "$image" >/dev/null || die "not a readable qcow2 image: $image"

# Two inspections choosing the same apparently free NBD device can race. /run
# is root-owned, so the lock path cannot be replaced by an unprivileged user.
exec 9>/run/sand-base-image-check.lock
flock -w 120 9 || die 'timed out waiting for another image inspection'

work=$(mktemp -d /var/tmp/sand-image-check.XXXXXXXX)
mount_dir="$work/root"
nbd=''
connected=0
mounted=0

cleanup() {
  local status=$?
  trap - EXIT
  set +e
  if ((mounted)); then
    if umount "$mount_dir"; then mounted=0; else status=1; fi
  fi
  if ((connected && !mounted)); then
    if qemu-nbd --disconnect "$nbd"; then connected=0; else status=1; fi
  fi
  if ((connected == 0 && mounted == 0)) && ! mountpoint -q "$mount_dir"; then
    rmdir -- "$mount_dir" "$work" 2>/dev/null || status=1
  else
    log "cleanup incomplete; inspect $work and $nbd"
    status=1
  fi
  exit "$status"
}
trap cleanup EXIT

modprobe nbd max_part=16
for sys in /sys/block/nbd*; do
  candidate="/dev/${sys##*/}"
  [[ -b $candidate && ! -s $sys/pid ]] || continue
  nbd=$candidate
  break
done
[[ -n $nbd ]] || die 'no free /dev/nbd device'
qemu-nbd --read-only --connect="$nbd" "$image" || die "cannot connect $image to $nbd"
connected=1

root_partition=''
for _ in $(seq 1 20); do
  udevadm settle 2>/dev/null || true
  root_partition=$(blkid -o device -t TYPE=ext4 2>/dev/null | grep -E "^${nbd}p[0-9]+$" | head -1 || true)
  [[ -n $root_partition ]] && break
  sleep 1
done
[[ -n $root_partition ]] || die "no ext4 root partition found on $nbd"
mkdir "$mount_dir"
# noload prevents an ext4 journal replay from writing to the source image.
mount -o ro,noload "$root_partition" "$mount_dir" || die "cannot mount $root_partition read-only"
mounted=1

[[ -f $mount_dir/etc/passwd ]] || die 'missing /etc/passwd'
mapfile -t users < <(awk -F: '$3 >= 1000 && $3 < 65534 && $6 ~ /^\/home\// {print $1}' "$mount_dir/etc/passwd")
((${#users[@]} > 0)) || die 'no non-system /home user found for password check'
for user in "${users[@]}"; do
  passwd_status=$(chroot "$mount_dir" passwd -S "$user") || die "cannot read password status for $user"
  status=$(awk '{print $2}' <<<"$passwd_status")
  [[ $status == L || $status == NP ]] || die "user password is not locked: $user (passwd -S status $status)"
done

if [[ -d $mount_dir/etc/ssh ]]; then
  found=$(find "$mount_dir/etc/ssh" -maxdepth 1 -name 'ssh_host_*' -print -quit) || die 'cannot inspect SSH host keys'
  [[ -z $found ]] || die "SSH host key remains: ${found#"$mount_dir"}"
fi

[[ -f $mount_dir/etc/machine-id && ! -s $mount_dir/etc/machine-id ]] || die '/etc/machine-id must exist and be empty'
dbus_id="$mount_dir/var/lib/dbus/machine-id"
if [[ -e $dbus_id || -L $dbus_id ]]; then
  [[ -L $dbus_id ]] || die '/var/lib/dbus/machine-id is not a symlink'
  [[ $(readlink "$dbus_id") == /etc/machine-id ]] || die '/var/lib/dbus/machine-id does not point to /etc/machine-id'
fi

if [[ -d $mount_dir/var/lib/apt/lists ]]; then
  found=$(find "$mount_dir/var/lib/apt/lists" -mindepth 1 \( -type f -o -type l \) ! -name lock -print -quit) || die 'cannot inspect APT lists'
  [[ -z $found ]] || die "APT package list remains: ${found#"$mount_dir"}"
fi
if [[ -d $mount_dir/var/cache/apt/archives ]]; then
  found=$(find "$mount_dir/var/cache/apt/archives" -type f -name '*.deb' -print -quit) || die 'cannot inspect APT archive cache'
  [[ -z $found ]] || die "APT .deb archive remains: ${found#"$mount_dir"}"
fi

if [[ -d $mount_dir/etc/dpkg/dpkg.cfg.d ]]; then
  if found=$(grep -rlE '^[[:space:]]*force-unsafe-io([[:space:]]|$)' "$mount_dir/etc/dpkg/dpkg.cfg.d"); then
    die "dpkg force-unsafe-io remains: ${found#"$mount_dir"}"
  else
    grep_status=$?
    ((grep_status == 1)) || die 'cannot inspect dpkg configuration'
  fi
fi

# Search home directories once. The mkcert directory check also catches CA
# material with an unfamiliar extension, and protects against a shared root CA
# or private key baked into every clone.
found=$(find "$mount_dir/root" "$mount_dir/home" \( -type f -o -type l \) \( \
  -name '.bash_history' -o -name '.zsh_history' -o -name '.python_history' -o \
  -name '.env' -o -name '.env.*' -o -name '*.pem' -o -name '*.key' -o \
  -name 'id_rsa' -o -name 'id_ed25519' -o -name 'id_ecdsa' -o -name 'id_dsa' -o \
  -name '.credentials.json' -o -name 'credentials.json' -o -name 'application_default_credentials.json' -o \
  -path '*/.local/share/mkcert/*' \) -print -quit) || die 'cannot inspect home directories for credentials'
if [[ -n $found ]]; then
  relative=${found#"$mount_dir"}
  case "$relative" in
    */.local/share/mkcert/*) die "mkcert CA material remains: $relative" ;;
    */.bash_history|*/.zsh_history|*/.python_history) die "shell history remains: $relative" ;;
    *) die "credential-like file remains: $relative" ;;
  esac
fi

log "PASS: $image is safe to distribute (passwords locked; no host keys, machine identity, build residue, or credential-shaped files)"
