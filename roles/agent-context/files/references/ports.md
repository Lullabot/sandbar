# Reaching guest ports from the workstation

How the user's browser reaches a server listening in this guest depends on
the VM's connection profile type, recorded in [this-vm.md](this-vm.md).

## `local`

Lima forwards every TCP port that starts listening in the guest to
`127.0.0.1` on the workstation, with the same port number, automatically.
Give the user `http://localhost:PORT`. The forward appears when the server
starts listening and disappears when it stops; nothing needs configuring.

On a Linux workstation, ports below 1024 are not forwarded, because Lima
runs unprivileged there. Use a port of 1024 or above, or for ddev move the
router: `ddev config global --router-http-port=8080 --router-https-port=8443`
then `ddev restart`. macOS workstations have no such limit.

## `proxmox`

Nothing is forwarded. The guest has its own address on the Proxmox network,
which `hostname -I` shows; give the user `http://<that address>:PORT`. The
server must listen on that address or on all interfaces, not only on
`127.0.0.1`. Anything on that network can reach the port, not just the
user. `*.ddev.site` names resolve to `127.0.0.1`, so they need a hosts entry
on the workstation pointing at the guest address.

## `remote-ssh`

Lima forwards guest ports to `127.0.0.1` on the remote Lima host, not on the
user's workstation. The user needs a tunnel, run on their workstation, using
the host, port, user, and identity from that connection profile:

    ssh -N -L PORT:127.0.0.1:PORT -p SSH_PORT -i IDENTITY USER@HOST

Ports below 1024 are not forwarded to that Linux host, so use a high port.
Alternatively, `cloudflared tunnel --url http://localhost:PORT` in the guest
prints a public `trycloudflare.com` URL. Anyone with that URL can reach the
server, so say so and do not leave it running unattended.

More: <https://lullabot.github.io/sandbar/latest/using-sand/web-servers/>
