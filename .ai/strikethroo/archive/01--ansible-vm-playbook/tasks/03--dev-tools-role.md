---
id: 3
group: "ansible-vm-playbook"
dependencies: [1]
status: "completed"
created: 2026-03-01
skills:
  - ansible-playbook-authoring
  - debian-apt-repository-management
---
# Implement Dev-Tools Role

## Objective
Create the dev-tools role that installs Docker CE, ddev, cloudflared, uv, configures mkcert CA, and sets up the Docker registry proxy.

## Skills Required
- Ansible playbook authoring (apt_key, apt_repository, apt, shell, template, handlers)
- Debian APT repository management (GPG keys, deb822 format, sources.list.d)

## Acceptance Criteria
- [ ] Docker CE installed from official Docker APT repo (deb822 format): docker-ce, docker-ce-cli, containerd.io, docker-buildx-plugin, docker-compose-plugin
- [ ] Docker GPG key at `/etc/apt/keyrings/docker.asc`, repo in `/etc/apt/sources.list.d/docker.sources`
- [ ] Docker service enabled and started
- [ ] `claude` user added to `docker` group after Docker installation
- [ ] ddev installed from official ddev APT repo: GPG key at `/etc/apt/keyrings/ddev.gpg`, repo in `/etc/apt/sources.list.d/ddev.list`
- [ ] cloudflared installed from Cloudflare's official APT repository
- [ ] uv installed via standalone installer as claude user (idempotent — checks for ~/.local/bin/uv)
- [ ] `mkcert -install` run as claude user (idempotent — checks for ~/.local/share/mkcert/rootCA.pem)
- [ ] Docker registry proxy CA cert downloaded from `http://docker-registry-proxy.lan:3128/ca.crt` to `/usr/local/share/ca-certificates/docker_registry_proxy.crt`
- [ ] `update-ca-certificates` run after CA cert placement
- [ ] Docker systemd drop-in at `/etc/systemd/system/docker.service.d/http-proxy.conf` with HTTP_PROXY and HTTPS_PROXY
- [ ] Docker restarted after proxy config (via handler, only when config changes)
- [ ] `roles/dev-tools/handlers/main.yml` includes handlers for daemon-reload and docker restart

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Docker APT repo format (deb822 in `.sources` file):
  ```
  Types: deb
  URIs: https://download.docker.com/linux/debian
  Suites: trixie
  Components: stable
  Signed-By: /etc/apt/keyrings/docker.asc
  ```
- ddev APT repo: `deb [signed-by=/etc/apt/keyrings/ddev.gpg] https://pkg.ddev.com/apt/ * *`
- Cloudflare APT repo: look up official Cloudflare repository setup for Debian
- uv installer: `curl -LsSf https://astral.sh/uv/install.sh | sh` (run as claude user via `become_user`)
- Docker registry proxy systemd drop-in content:
  ```
  [Service]
  Environment="HTTP_PROXY=http://docker-registry-proxy.lan:3128/"
  Environment="HTTPS_PROXY=http://docker-registry-proxy.lan:3128/"
  ```
- Use `ansible.builtin.get_url` for downloading GPG keys and CA certs
- Use `ansible.builtin.copy` with `content` for deb822 repo files
- Use `ansible.builtin.template` for docker-proxy.conf.j2
- Use `ansible.builtin.user` with `groups: docker` and `append: yes` to add docker group

## Input Dependencies
- Task 1: Project scaffolding and role directory structure

## Output Artifacts
- `roles/dev-tools/tasks/main.yml`
- `roles/dev-tools/handlers/main.yml`
- `roles/dev-tools/defaults/main.yml`
- `roles/dev-tools/templates/docker-proxy.conf.j2`

## Implementation Notes
- The `docker` group is created by the docker-ce package, so user group assignment must come after Docker installation
- Use `meta: reset_connection` after adding claude to docker group (or note this is for future SSH-based runs)
- uv and mkcert tasks should use `creates` parameter or `stat` check for idempotency
- mkcert -install needs `CAROOT` env var or runs in user context to place CA in `~/.local/share/mkcert/`
- Docker registry proxy handler chain: template changes → notify daemon-reload → notify restart docker
