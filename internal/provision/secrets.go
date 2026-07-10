package provision

import (
	"context"
	"io"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/secrets"
)

// applySecretsScript writes the streamed env file into the guest with the same
// in-guest hygiene as inGuestScript: the target file is created 0600 (via
// `install -m 600 /dev/null`) BEFORE any secret bytes are written to it, so there
// is never an instant at which a world-readable file holds a secret. The rendered
// body arrives over STDIN (cat), never argv — a secret must not appear in a host
// `ps` listing. The enclosing `sudo -H -u <user>` runs this as the target user
// with HOME set to their home, so the created dir/file are owned by them.
const applySecretsScript = `set -eu -o pipefail
d="$HOME/.config/sandbar"
f="$d/secrets.env"
install -d -m 700 "$d"
install -m 600 /dev/null "$f"
cat > "$f"
`

// clearSecretsScript removes the guest env file. It is used when a VM has no
// secrets, so a file written by a previous apply does not linger with stale
// values. `rm -f` is a no-op when the file is already absent.
const clearSecretsScript = `set -eu -o pipefail
rm -f "$HOME/.config/sandbar/secrets.env"
`

// ApplySecrets renders pairs and streams them into the guest's
// ~/.config/sandbar/secrets.env (mode 0600, parent dir 0700), owned by user. The
// rendered text is passed over STDIN only — never argv — so secrets never appear
// in a host process listing. When pairs is empty (or nil) the guest file is
// removed instead of writing an empty one.
//
// It runs the write as `sudo -H -u <user>`. -H sets HOME to the target user's
// home, so the file lands there and not in root's. Do NOT use `sudo -i` here:
// -i runs the target's *login shell*, which re-parses the command, so a
// multi-line script arrives mangled ("set: -c: invalid option") and the write
// silently fails. Reset's direnv step can use -iu only because its command is a
// single word with plain args. Verified against a live guest.
func ApplySecrets(ctx context.Context, cli *lima.Client, name, user string, pairs map[string]string, out io.Writer) error {
	if len(pairs) == 0 {
		return cli.Shell(ctx, name, nil, out, "sudo", "-H", "-u", user, "bash", "-c", clearSecretsScript)
	}
	body := secrets.Render(pairs)
	return cli.Shell(ctx, name, strings.NewReader(body), out, "sudo", "-H", "-u", user, "bash", "-c", applySecretsScript)
}
