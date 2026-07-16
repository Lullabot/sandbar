# Web Servers and Ports

How to reach a web server running inside a `sand` VM — a ddev project,
`npm run dev`, `python3 -m http.server`, anything listening on a TCP port.
The answer depends on where the VM lives: on the machine you're sitting at
(the **Local** profile), or on a remote host (a `remote-ssh`
[Connection Profile](connection-profiles.md)).

## Local VMs: ports appear on localhost

Lima automatically forwards every TCP port that starts listening inside the
guest to `127.0.0.1` on your machine, same port number. `sand` doesn't add
or restrict anything here — it inherits Lima's default behavior — so a
server started in the guest:

```console
claude@myvm$ python3 -m http.server 8000
```

is immediately reachable at `http://localhost:8000` on your machine. No
configuration, no flags: forwarding follows the guest's listening sockets
automatically and goes away when the server stops.

Two properties worth knowing:

- **Loopback only.** Forwarded ports bind `127.0.0.1`, never a LAN
  interface. Running a server in a VM does not expose it to your network —
  see the [security model](../reference/security-model.md).
- **Privileged ports on Linux hosts.** Lima runs unprivileged, and on a
  Linux host an unprivileged process cannot bind ports below 1024 — a guest
  server on port 80 or 443 is silently skipped (Lima logs a warning in the
  instance's `ha.stderr.log`). macOS hosts don't have this restriction.

### ddev URLs

[ddev](https://ddev.com/) project URLs work from your host browser as-is on
a macOS host: `*.ddev.site` resolves to `127.0.0.1` in public DNS, ddev's
router listens on ports 80/443 in the guest, and Lima forwards them to your
loopback — so `https://myproj.ddev.site` just works.

On a **Linux host**, ports 80/443 hit the privileged-port rule above.
Either allow unprivileged binds on the host:

```console
$ sudo sysctl net.ipv4.ip_unprivileged_port_start=80   # persist in /etc/sysctl.d/
```

or move ddev's router to high ports inside the guest and keep everything
unprivileged:

```console
claude@myvm$ ddev config global --router-http-port=8080 --router-https-port=8443
claude@myvm$ ddev restart
```

after which the project URL is `https://myproj.ddev.site:8443`.

!!! note "Expect a certificate warning"
    HTTPS certificates are minted by `mkcert` **inside the guest**, so the
    guest trusts them (Claude's own requests succeed) but your host browser
    does not. Either click through the warning, or trust the guest's root
    CA on your machine: `mkcert -CAROOT` in the guest names the directory
    holding `rootCA.pem`; copy it out (the `g` verb, or the download CLI —
    see [Shells and Files](files-and-shells.md)) and add it to your OS or
    browser trust store.

## Remote VMs: tunnel out with cloudflared

On a remote profile the same Lima forwarding happens — but to `127.0.0.1`
**on the remote host**. That's a machine you can SSH into, not the one your
browser runs on, so a guest web server is reachable from nowhere except the
remote host itself. `sand` adds no port forwarding of its own over the
profile's SSH connection, and the
[security model](../reference/security-model.md) assumes remote hosts sit
on an isolated network behind whatever firewalling you already have.

The supported way in is outward: run
[cloudflared](https://github.com/cloudflare/cloudflared) — preinstalled in
every VM — **inside the guest**, and let it hand you a URL:

```console
claude@myvm$ cloudflared tunnel --url http://localhost:3000
```

This starts a [Quick Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/)
— no Cloudflare account, no configuration — and prints a random
`https://….trycloudflare.com` URL that works from any browser, anywhere,
for as long as the process runs.

ddev's router picks the project by the request's `Host` header, so for a
ddev project point cloudflared at the router and pin the header:

```console
claude@myvm$ cloudflared tunnel --url https://127.0.0.1:443 \
    --http-host-header myproj.ddev.site --no-tls-verify
```

`--no-tls-verify` is needed because the router presents the guest-minted
mkcert certificate, which cloudflared doesn't trust; the tunnel's public
side still gets a real Cloudflare certificate. (ddev's built-in
[`ddev share`](https://docs.ddev.com/en/stable/users/topics/sharing/) is an
ngrok-based alternative.)

!!! warning "A quick tunnel is a public URL"
    Anyone who has the URL can reach the server — there is no
    authentication in front of it. The URL is random and unlisted, but
    treat it as public: don't leave a tunnel running unattended, and
    remember that everything a `sand` VM serves may be agent-written.
    Quick Tunnels are also rate-limited and intended for testing, not
    hosting.

Nothing stops you from using cloudflared on a local VM too — it's the
easiest way to show in-progress work to someone who isn't at your desk.

## What about SSH port forwarding?

For a private path to a remote VM — no public URL — plain OpenSSH
forwarding works today, using the connection details already in your
[profile](connection-profiles.md#profilesyaml):

```console
$ ssh -N -L 8443:127.0.0.1:8443 -p <port> -i <identity_path> <user>@<host>
```

While that runs, `localhost:8443` on your machine reaches the remote host's
loopback port 8443 — that is, a guest port Lima forwarded there. For the
high-ports ddev configuration above, `https://myproj.ddev.site:8443` then
works from your browser: `*.ddev.site` still resolves to `127.0.0.1`, where
the tunnel now listens, and the router still sees the project's name in the
`Host` header. The mkcert certificate warning applies exactly as with a
local VM.

The catches, and why this recipe is manual:

- **The remote end must exist.** The remote hosts `sand` targets are Linux,
  so the privileged-port rule applies *there*: out of the box Lima has not
  forwarded a guest's port 80/443 to the remote loopback at all. Move ddev
  to high router ports (easiest), or raise
  `net.ipv4.ip_unprivileged_port_start` on the remote host.
- **The local end has the same rule.** Binding your *local* port 443 (to
  make a bare `https://myproj.ddev.site` work) needs root on a Linux
  laptop; macOS allows it unprivileged. A high local port with the `:8443`
  URL suffix avoids the question entirely.
- **One `-L` per port, one process to babysit.** The forward lives and dies
  with that `ssh` process.

Connection profiles have no field for declaring port forwards — `sand`
neither creates nor manages SSH tunnels. If a guest port matters to your
workflow, script the `ssh -L` invocation yourself, or prefer cloudflared,
which needs nothing from the machine you're browsing on.
