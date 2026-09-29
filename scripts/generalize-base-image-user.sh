#!/usr/bin/env bash
# Move the temporary playbook user's home setup into cloud-init's skeleton,
# then remove that account so each provider can create its own login at boot.
set -euo pipefail

die() { echo "generalize-base-image-user: $*" >&2; exit 1; }

(($# == 2)) || die 'expected mounted image root and build username'
root=$(realpath -e -- "$1") || die 'image root does not exist'
user=$2
[[ $root != / ]] && mountpoint -q "$root" || die 'image root must be a dedicated mount'
[[ $user =~ ^[a-z_][a-z0-9_-]*$ ]] || die 'invalid build username'
[[ -f $root/etc/passwd && -d $root/etc/skel && -d $root/home/$user ]] || \
  die "missing account, skeleton, or /home/$user in image root"

cp -a -- "$root/home/$user/." "$root/etc/skel/"
rm -rf -- "$root/etc/skel/.ansible"
# uv's receipt embeds /home/<build-user> as its install prefix. The binaries
# work for a new login, but copying the receipt would break `uv self update`.
rm -f -- "$root/etc/skel/.config/uv/uv-receipt.json"
chown -R root:root "$root/etc/skel"
# The builder bind-mounts host /proc for Ansible; userdel would see the host's
# processes at the same numeric UID and refuse even though none belong to this
# transient image account. Force only that process check, inside the chroot.
chroot "$root" /usr/sbin/userdel -f -r "$user"
if chroot "$root" /usr/bin/getent group "$user" >/dev/null; then
  chroot "$root" /usr/sbin/groupdel "$user"
fi
rm -f -- "$root/var/lib/systemd/linger/$user"
