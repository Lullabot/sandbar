package provider

import (
	"context"
	"io"
	"os"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

// limaProvider is the local Lima provider: the default backend, behaviourally
// identical to sand's current direct use of *lima.Client. It composes the two
// pieces that already do the work and delegates to them —
//
//   - core (*lima.Client) drives limactl for discovery, power, and guest
//     transport, over the task-1 host-access seam;
//   - prov (*provision.Provisioner) owns the base-build / clone / finalize
//     lifecycle for Create / Recreate / Reset.
//
// The provisioner keeps depending on the lima core directly (prov.Lima), NOT on
// Provider, which is what keeps the dependency graph acyclic: consumers ->
// Provider -> { lima core, provisioner }, and provisioner -> lima core. A remote
// provider (plan 15 task 5) is this same shape configured with the SSH
// host-access implementation.
type limaProvider struct {
	core *lima.Client
	prov *provision.Provisioner
	// hostFiles is this provider's host-access handle — lima.LocalFiles() here
	// (local Lima IS the host limactl runs on); a remote provider embeds this
	// struct and overrides it with its SSHHost at construction (see
	// NewRemoteLima). See Provider.HostFiles.
	hostFiles lima.HostFiles
}

// NewLocalLima builds the local Lima provider from an already-constructed lima
// core and provisioner (the provisioner's Lima field should be the same core, as
// sand wires it today). It returns the Provider interface so callers depend on
// the seam, not the concrete type.
func NewLocalLima(core *lima.Client, prov *provision.Provisioner) Provider {
	return &limaProvider{core: core, prov: prov, hostFiles: lima.LocalFiles()}
}

// var _ Provider = (*limaProvider)(nil) is the compile-time proof that the local
// Lima provider satisfies Provider; see also NewLocalLima's Provider return type,
// which the compiler checks at the `return &limaProvider{…}` above.
var _ Provider = (*limaProvider)(nil)

// --- Discovery ---

func (p *limaProvider) List() ([]vm.VM, error)             { return p.core.List() }
func (p *limaProvider) Get(name string) (vm.VM, error)     { return p.core.Get(name) }
func (p *limaProvider) Status(name string) (string, error) { return p.core.Status(name) }

// --- Power ---

func (p *limaProvider) Start(name string) error              { return p.core.Start(name) }
func (p *limaProvider) Stop(name string) error               { return p.core.Stop(name) }
func (p *limaProvider) Delete(name string, force bool) error { return p.core.Delete(name, force) }

func (p *limaProvider) StartStreaming(ctx context.Context, name string, out io.Writer) error {
	return p.core.StartStreaming(ctx, name, out)
}

func (p *limaProvider) StopStreaming(ctx context.Context, name string, out io.Writer) error {
	return p.core.StopStreaming(ctx, name, out)
}

// --- Provisioning lifecycle ---

func (p *limaProvider) Create(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error {
	return p.prov.CreateVMWithOptions(ctx, cfg, opts, out)
}

func (p *limaProvider) Recreate(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error {
	return p.prov.RecreateWithOptions(ctx, cfg, opts, out)
}

func (p *limaProvider) Reset(ctx context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, out io.Writer) error {
	return p.prov.Reset(ctx, cfg, opts, out)
}

// --- Guest transport ---

func (p *limaProvider) Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	return p.core.Shell(ctx, name, stdin, out, argv...)
}

func (p *limaProvider) ShellStreamOut(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	return p.core.ShellStreamOut(ctx, name, stdin, out, argv...)
}

func (p *limaProvider) ShellOut(ctx context.Context, name string, argv ...string) ([]byte, error) {
	return p.core.ShellOut(ctx, name, argv...)
}

func (p *limaProvider) Copy(ctx context.Context, out io.Writer, recursive bool, src, dst string) error {
	return p.core.Copy(ctx, out, recursive, src, dst)
}

// --- Interactive attach & guest paths ---

// AttachArgv resolves the guest home from v.Dir itself (via lima.GuestHome) and
// hands both to lima.AttachArgv — the one place in sand that knows tmux exists —
// so the caller passes only the vm.VM and never constructs the Lima-shaped
// command. Reproduces exactly what the `S` verb and `sand shell` do today.
func (p *limaProvider) AttachArgv(v vm.VM) []string {
	return lima.AttachArgv(v.Name, lima.GuestHome(v.Dir), os.Getenv("COLORTERM"))
}

// HostUser returns this machine's user — for local Lima the limactl host IS this
// machine, and Lima creates a guest account matching it (see Provider.HostUser).
func (p *limaProvider) HostUser() string { return vm.HostUser() }

// HostResources returns the zero value: local Lima runs on THIS machine, so the
// board header keeps sampling the local host directly through its own platform
// probes (internal/ui/hostres_*.go). Only a remote provider needs to override
// this to report a different host's capacity. See Provider.HostResources.
func (p *limaProvider) HostResources() HostResources { return HostResources{} }

func (p *limaProvider) GuestHome(v vm.VM) string { return lima.GuestHome(v.Dir) }

func (p *limaProvider) GuestUser(v vm.VM) string { return lima.GuestUser(v.Dir) }

func (p *limaProvider) GuestPath(name, path string) string { return lima.GuestPath(name, path) }

// --- Preflight ---

func (p *limaProvider) Preflight() error { return p.core.Preflight() }

// --- Host access ---

// HostFiles returns the local filesystem: local Lima IS the host limactl runs
// on. See Provider.HostFiles.
func (p *limaProvider) HostFiles() lima.HostFiles { return p.hostFiles }
