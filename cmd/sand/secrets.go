package main

import (
	"context"
	"fmt"
	"io"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
)

// secretStore is the narrow host-secrets surface settleSecrets drives, so the
// bookkeeping can be tested without touching a real store on disk.
// *secrets.Store satisfies it natively.
type secretStore interface {
	Get(vm string, connScope registry.Scope) map[string]string
	Set(vm string, connScope registry.Scope, pairs map[string]string) error
	GetAll(vm string, connScope registry.Scope) map[string]map[string]string
}

// guestSecretsApplier writes a VM's secrets into its guest. It is
// provision.ApplySecrets behind a package-level var — the seam pattern the rest
// of this package uses (listForProfile, provenanceOfForProfile) — so a test can
// see what WOULD reach the guest without a real VM. Spelled out rather than
// assigned directly because ApplySecrets' backend parameter is an unexported
// interface in that package, which a test in this one could not name.
var guestSecretsApplier = func(ctx context.Context, p provider.Provider, name, user string, scopes map[string]map[string]string, out io.Writer) error {
	return provision.ApplySecrets(ctx, p, name, user, scopes, out)
}

// loadSecretStore opens the host secrets store, reporting a load problem
// without failing the command: a store that could not be read is a reason to
// skip the secrets step, never to turn a VM that is already up into a reported
// failure.
func loadSecretStore(out io.Writer) secretStore {
	store, err := secrets.Load()
	if err != nil {
		fmt.Fprintln(out, "warning:", err)
	}
	if store == nil {
		return nil
	}
	return store
}

// settleSecrets is the host-secrets half of a finished create or reset, and it
// is the same work the TUI does in its provisionDoneMsg handler (internal/ui/
// model.go) — deliberately, because the two entrypoints having different
// answers here is what this fixes:
//
//   - The token passed for the clone becomes the VM's GH_TOKEN secret, so it can
//     be rotated later from the secrets editor without a rebuild. The TUI has
//     always done this with the create form's token; a `sand create
//     --clone-token` recorded nothing, so the same VM built headlessly had no
//     secret to edit.
//   - The VM's stored secrets are written into the guest NOW. Create and Reset
//     each end with their own start, so nothing else will apply them until the
//     VM's next start — and for a reset that is the sharp edge: the rebuilt
//     guest came up with none of the secrets the old one had, and `sand create
//     --recreate` left it that way until the user happened to start it from the
//     TUI.
//
// Every failure here is a warning, not an error. The VM is already up; a secret
// that could not be written is worth saying loudly and is not worth reporting as
// a failed create — which would invite a retry of the whole build.
func settleSecrets(ctx context.Context, p provider.Provider, scope registry.Scope, cfg vm.CreateConfig, out io.Writer) {
	store := loadSecretStore(out)
	if store == nil {
		return
	}
	settleSecretsIn(ctx, p, store, scope, cfg, out)
}

// settleSecretsIn is settleSecrets against an explicit store — the half that
// has decisions in it, split out so it can be tested without a real store on
// disk (loadSecretStore reaches for the user's XDG data dir).
func settleSecretsIn(ctx context.Context, p provider.Provider, store secretStore, scope registry.Scope, cfg vm.CreateConfig, out io.Writer) {
	if cfg.CloneToken != "" {
		// Get returns a defensive copy, so mutating it cannot corrupt the store
		// ahead of Set validating the result.
		pairs := store.Get(cfg.Name, scope)
		pairs["GH_TOKEN"] = cfg.CloneToken
		if err := store.Set(cfg.Name, scope, pairs); err != nil {
			fmt.Fprintln(out, "warning: VM ready, but the token could not be saved as a secret:", err)
		}
	}

	user := cfg.User
	if user == "" {
		user = vm.HostUser()
	}
	if err := guestSecretsApplier(ctx, p, cfg.Name, user, store.GetAll(cfg.Name, scope), io.Discard); err != nil {
		fmt.Fprintln(out, "warning: VM ready, but its secrets were not applied:", err)
	}
}
