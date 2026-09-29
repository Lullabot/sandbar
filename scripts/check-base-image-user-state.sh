#!/usr/bin/env bash
# Inspect an already-mounted image root. cloud-init must be free to create the
# Lima or Proxmox login at its chosen name and UID, with the baked home setup.
set -euo pipefail

die() { echo "check-base-image: $*" >&2; exit 1; }

(($# == 1)) || die 'expected mounted image root'
root=$1
[[ -f $root/etc/passwd && -f $root/etc/group ]] || die 'missing account database'

uid1000=$(awk -F: '$3 == 1000 {print $1; exit}' "$root/etc/passwd")
[[ -z $uid1000 ]] || die "UID 1000 is occupied by $uid1000; cloud-init needs it for the host login"
gid1000=$(awk -F: '$3 == 1000 {print $1; exit}' "$root/etc/group")
[[ -z $gid1000 ]] || die "GID 1000 is occupied by $gid1000; cloud-init needs it for the host login"

normal_user=$(awk -F: '$3 >= 1000 && $3 < 65534 {print $1; exit}' "$root/etc/passwd")
[[ -z $normal_user ]] || die "baked login $normal_user remains; cloud-init must create the provider login"
normal_group=$(awk -F: '$3 >= 1000 && $3 < 65534 {print $1; exit}' "$root/etc/group")
[[ -z $normal_group ]] || die "baked group $normal_group remains; cloud-init must create the provider group"
[[ -d $root/home ]] || die 'missing /home'
home_entry=$(find "$root/home" -mindepth 1 -maxdepth 1 -print -quit)
[[ -z $home_entry ]] || die "baked home remains: ${home_entry#"$root"}"

for relative in .local/bin/uv .local/bin/uvx .tmux.conf .ssh/rc .config/direnv/direnv.toml; do
  [[ -f $root/etc/skel/$relative ]] || die "missing reusable home setup: /etc/skel/$relative"
done
[[ ! -e $root/etc/skel/.config/uv/uv-receipt.json ]] || \
  die 'uv install receipt still names the transient build home'
