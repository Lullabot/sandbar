package provision

// GeneralizeScript removes per-machine identity from a base guest before it
// becomes a clone source. Keep this shared by every backend that builds a base.
const GeneralizeScript = `set -eu
truncate -s 0 /etc/machine-id
truncate -s 0 /etc/hostname
if [ -e /var/lib/dbus/machine-id ] && [ ! -L /var/lib/dbus/machine-id ]; then
  rm -f /var/lib/dbus/machine-id
  ln -s /etc/machine-id /var/lib/dbus/machine-id
fi
`
