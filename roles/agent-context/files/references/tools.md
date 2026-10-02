# Tools and boundaries

Sandbar's standard base includes Docker with Compose and Buildx, `ddev`,
Node.js, Go, Python 3 with `uv`, a headless JDK, `gh`, `glab`, `tmux`,
`direnv`, and `jq`. Custom or older images and user changes can differ.
Check with `command -v` and a version command before relying on a tool in
the live VM.
Docker starts through socket activation, so an inactive `docker.service`
before first use does not by itself indicate a failure; try `docker info`.

The guest is disposable and separate from the workstation. Use guest `sudo`
when an authorized task requires packages or system configuration. Do not
assume a guest path is mounted from the host or that guest commands can access
workstation files. Sandbar's host-side `sand paste-image` command can send an
image into the guest's image-only clipboard slot. Text and credentials are
not copied from the workstation clipboard into that slot.
