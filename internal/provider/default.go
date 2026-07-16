package provider

import (
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
)

// NewDefault builds sand's default backend: the local Lima provider, wired
// exactly the way sand has always wired it — a lima core over the real
// execRunner, and a Provisioner over that same core with the embedded
// playbook located and extracted.
//
// This is the ONE place all three entrypoints (the TUI in cmd/sand/main.go,
// the headless `sand create` in cmd/sand/create.go, and `sand shell` in
// cmd/sand/shell.go) construct their provider, replacing what used to be
// three separate `lima.New(lima.NewExecRunner())` + `&provision.Provisioner{}`
// call sites — one per entrypoint, free to drift from each other. Centralising
// construction here is what makes AGENTS.md's "keep the three entrypoints
// from drifting" rule structural rather than a convention someone has to
// remember: every caller gets the identical backend.
//
// It resolves to LOCAL Lima only — an unconfigured `sand` behaves exactly as
// it does today. Provider SELECTION (choosing a remote target instead) is
// Resolve's job (select.go), which wraps this function as its local path;
// NewDefault is deliberately given a clean, argument-free signature so a later
// change can grow it (e.g. into one that reads configuration) without every
// call site changing shape again.
func NewDefault() (Provider, error) {
	core := lima.New(lima.NewExecRunner())
	// PlaybookDir is left empty: the Provisioner locates the embedded playbook
	// lazily, the first time a create/reset actually needs it. Locating it here
	// would make `sand shell` — which constructs a provider but never provisions
	// — pay for (and fail on) playbook extraction just to attach to a VM.
	prov := &provision.Provisioner{Lima: core}
	return NewLocalLima(core, prov), nil
}
