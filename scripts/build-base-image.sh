#!/usr/bin/env bash
# Build a reusable Debian cloud image without booting it. The only persistent
# result is the compressed qcow2 at --out; all chroot mounts are temporary.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: sudo scripts/build-base-image.sh --arch amd64|arm64 --out PATH [--max-size-mib N] [--src-url URL_OR_FILE]

The output directory must have enough disk space for a 20 GiB working image.
EOF
}

die() { echo "build-base-image: $*" >&2; exit 1; }
log() { echo "build-base-image: $*" >&2; }

arch=''
out=''
src_url=''
max_size_mib=1900
while (($#)); do
  case "$1" in
    --arch|--out|--src-url|--max-size-mib)
      (($# >= 2)) || die "$1 needs a value"
      case "$1" in
        --arch) arch=$2 ;;
        --out) out=$2 ;;
        --src-url) src_url=$2 ;;
        --max-size-mib) max_size_mib=$2 ;;
      esac
      shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
[[ $arch == amd64 || $arch == arm64 ]] || die '--arch must be amd64 or arm64'
[[ -n $out ]] || die '--out is required'
[[ $max_size_mib =~ ^[0-9]+$ ]] && ((max_size_mib > 0)) || die '--max-size-mib must be a positive integer'
[[ $(uname -s) == Linux ]] || die 'qemu-nbd requires a Linux builder'
((EUID == 0)) || die 'run with sudo'
host_arch=$(uname -m)
case "$arch:$host_arch" in
  amd64:x86_64|arm64:aarch64) ;;
  *) die "cannot chroot an $arch image on $host_arch without binfmt emulation; use a native builder" ;;
esac

for cmd in qemu-img qemu-nbd modprobe blkid mount umount mountpoint udevadm \
           resize2fs chroot systemctl fstrim curl sha256sum; do
  command -v "$cmd" >/dev/null || die "missing $cmd (install qemu-utils and standard Ubuntu system tools)"
done
if ! command -v growpart >/dev/null; then
  # cloud-guest-utils is not installed on every Ubuntu image. The builder only
  # asks the host for qemu-utils; obtain this small partition-grow tool here.
  log 'installing cloud-guest-utils on the builder host for growpart'
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends cloud-guest-utils
fi

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
[[ -f $repo/site.yml && -d $repo/roles ]] || die "playbook files not found at $repo"
user_name=$(awk '/^user_name:/ { print $2; exit }' "$repo/roles/user/defaults/main.yml")
[[ $user_name =~ ^[a-z_][a-z0-9_-]*$ ]] || die 'invalid default user name in roles/user/defaults/main.yml'

out_dir=$(dirname -- "$out")
mkdir -p -- "$out_dir"
out_dir=$(cd -- "$out_dir" && pwd -P)
out="$out_dir/$(basename -- "$out")"
[[ ! -d $out ]] || die "output is a directory: $out"
work=$(mktemp -d "$out_dir/.sand-image-build.XXXXXXXX")
image="$work/work.qcow2"
mount_dir="$work/root"
nbd=/dev/nbd0
connected=0
mounted=0
resolv_saved=0
policy_saved=0
output_tmp="$work/output.qcow2"

cleanup() {
  local status=$?
  trap - EXIT
  set +e
  if ((mounted)); then
    # These files belong to the image; restore its original DNS policy and
    # remove the service-start guard even after a failed apt/playbook step.
    if ((resolv_saved)); then
      rm -f -- "$mount_dir/etc/resolv.conf"
      mv -- "$work/resolv.conf.original" "$mount_dir/etc/resolv.conf"
    fi
    rm -f -- "$mount_dir/usr/sbin/policy-rc.d"
    if ((policy_saved)); then
      mv -- "$work/policy-rc.d.original" "$mount_dir/usr/sbin/policy-rc.d"
    fi
    rm -rf -- "$mount_dir/root/playbook"
    for d in sys proc dev; do
      mountpoint -q "$mount_dir/$d" && umount "$mount_dir/$d"
    done
    umount "$mount_dir"
  fi
  if ((connected)); then
    if qemu-nbd --disconnect "$nbd"; then
      connected=0
    else
      status=1
    fi
  fi
  # If a mount remains busy, retain the work directory so its mountpoint and
  # image are visible for recovery instead of deleting under a live mount.
  if ((connected == 0)) && ! mountpoint -q "$mount_dir" && ! mountpoint -q "$mount_dir/dev" \
     && ! mountpoint -q "$mount_dir/proc" && ! mountpoint -q "$mount_dir/sys"; then
    rm -rf -- "$work"
  else
    log "cleanup incomplete; inspect mounts under $work and $nbd"
    status=1
  fi
  exit "$status"
}
trap cleanup EXIT

src_url=${src_url:-"https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-${arch}.qcow2"}
log "fetching $src_url"
if [[ -f $src_url ]]; then
  cp -- "$src_url" "$image"
else
  curl --fail --location --retry 3 --show-error --output "$image" "$src_url"
fi
qemu-img info -f qcow2 "$image" >/dev/null
qemu-img resize -f qcow2 "$image" 20G

# The original workflow uses nbd0. Refuse an occupied device rather than
# disconnecting a different build's image during our EXIT trap.
modprobe nbd max_part=16
[[ -b $nbd ]] || die "$nbd did not appear after modprobe"
[[ ! -s /sys/block/nbd0/pid ]] || die "$nbd is already connected"
qemu-nbd --connect="$nbd" --discard=unmap "$image"
connected=1

# Read superblocks directly: lsblk's udev-populated FSTYPE races partition
# discovery after qemu-nbd connects. Debian genericcloud has one ext4 root.
root=''
for _ in $(seq 1 20); do
  if command -v partprobe >/dev/null; then partprobe "$nbd" 2>/dev/null || true; fi
  udevadm settle 2>/dev/null || true
  root=$(blkid -o device -t TYPE=ext4 2>/dev/null | grep -E '^/dev/nbd0p[0-9]+$' | head -1 || true)
  [[ -n $root ]] && break
  sleep 1
done
lsblk -f "$nbd" || true
[[ -n $root ]] || die "no ext4 root partition found on $nbd"
part_num=${root#${nbd}p}
log "root partition: $root"
growpart "$nbd" "$part_num"
udevadm settle
resize2fs "$root"

mkdir -p "$mount_dir"
mount "$root" "$mount_dir"
mounted=1

# Package postinst scripts would try to start daemons without an init system.
# Keep the original file, if present, so the build does not alter that policy.
if [[ -e $mount_dir/usr/sbin/policy-rc.d || -L $mount_dir/usr/sbin/policy-rc.d ]]; then
  mv -- "$mount_dir/usr/sbin/policy-rc.d" "$work/policy-rc.d.original"
  policy_saved=1
fi
printf '#!/bin/sh\nexit 101\n' > "$mount_dir/usr/sbin/policy-rc.d"
chmod 0755 "$mount_dir/usr/sbin/policy-rc.d"
for d in dev proc sys; do mount --bind "/$d" "$mount_dir/$d"; done

# Genericcloud's resolv.conf can point into /run, which is intentionally not
# bound into the chroot. Use the builder's upstream DNS file during apt and
# restore the image's own resolv.conf before publishing it.
if [[ -e $mount_dir/etc/resolv.conf || -L $mount_dir/etc/resolv.conf ]]; then
  mv -- "$mount_dir/etc/resolv.conf" "$work/resolv.conf.original"
  resolv_saved=1
fi
dns_source=/etc/resolv.conf
[[ -f /run/systemd/resolve/resolv.conf ]] && dns_source=/run/systemd/resolve/resolv.conf
cp -- "$dns_source" "$mount_dir/etc/resolv.conf"

chroot "$mount_dir" /usr/bin/env DEBIAN_FRONTEND=noninteractive apt-get update
chroot "$mount_dir" /usr/bin/env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  ansible-core rsync curl gnupg ca-certificates python3-passlib

# This is the same fileset embedded, hashed, and sent to guests by provision.
mkdir -p "$mount_dir/root/playbook"
cp -a -- "$repo/site.yml" "$repo/ansible.cfg" "$repo/inventory" \
  "$repo/roles" "$repo/group_vars" "$mount_dir/root/playbook/"
chroot "$mount_dir" /usr/bin/env DEBIAN_FRONTEND=noninteractive \
  ANSIBLE_FORCE_COLOR=0 ANSIBLE_CONFIG=/root/playbook/ansible.cfg \
  ansible-playbook -i localhost, --connection=local /root/playbook/site.yml \
  -e '{"provision_phase":"base","sand_image_build":true,"samba_enabled":false}'

# Write wants/ symlinks to the offline root; systemctl inside the chroot has
# no running systemd manager to contact.
systemctl --root="$mount_dir" enable qemu-guest-agent.service
if [[ -e $mount_dir/lib/systemd/system/fstrim.timer || -e $mount_dir/usr/lib/systemd/system/fstrim.timer ]]; then
  systemctl --root="$mount_dir" enable fstrim.timer
fi
systemctl --root="$mount_dir" enable docker.socket
systemctl --root="$mount_dir" disable docker.service
systemctl --root="$mount_dir" enable sand-image-clipboard.service

# The image is a clone source, not a machine or an account with credentials.
chroot "$mount_dir" passwd -l "$user_name"
rm -f -- "$mount_dir"/etc/ssh/ssh_host_*
: > "$mount_dir/etc/machine-id"
mkdir -p "$mount_dir/var/lib/dbus"
rm -f -- "$mount_dir/var/lib/dbus/machine-id"
ln -s /etc/machine-id "$mount_dir/var/lib/dbus/machine-id"
chroot "$mount_dir" apt-get clean
find "$mount_dir/var/lib/apt/lists" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
find "$mount_dir/var/log" -type f -delete
find "$mount_dir/root" "$mount_dir/home" -maxdepth 2 -type f \
  \( -name '.bash_history' -o -name '.zsh_history' -o -name '.python_history' \) -delete
[[ ! -e $mount_dir/etc/dpkg/dpkg.cfg.d/99-sand-base-speed ]] || die 'dpkg force-unsafe-io remained after the base role'

# Drop staged material before trim. Discard reaches the qcow2 through nbd's
# --discard=unmap setting; conversion compresses the remaining allocated data.
rm -rf -- "$mount_dir/root/playbook"
rm -f -- "$mount_dir/etc/resolv.conf"
if ((resolv_saved)); then
  mv -- "$work/resolv.conf.original" "$mount_dir/etc/resolv.conf"
  resolv_saved=0
fi
rm -f -- "$mount_dir/usr/sbin/policy-rc.d"
if ((policy_saved)); then
  mv -- "$work/policy-rc.d.original" "$mount_dir/usr/sbin/policy-rc.d"
  policy_saved=0
fi
sync
fstrim -v "$mount_dir"
for d in sys proc dev; do umount "$mount_dir/$d"; done
umount "$mount_dir"
mounted=0
qemu-nbd --disconnect "$nbd"
connected=0

qemu-img convert -f qcow2 -O qcow2 -c -o compression_type=zstd "$image" "$output_tmp"
bytes=$(stat -c %s "$output_tmp")
limit=$((max_size_mib * 1024 * 1024))
if ((bytes > limit)); then
  die "compressed image is $bytes bytes, above ${max_size_mib} MiB; try zstd compression, safe trimming, or a direct host for the intact qcow2"
fi
mv -f -- "$output_tmp" "$out"
if [[ -n ${SUDO_UID:-} && -n ${SUDO_GID:-} ]]; then chown "$SUDO_UID:$SUDO_GID" "$out"; fi
printf 'Size: %s bytes (%s MiB)\n' "$bytes" "$(( (bytes + 1048575) / 1048576 ))"
printf 'SHA-256: '
sha256sum "$out"
