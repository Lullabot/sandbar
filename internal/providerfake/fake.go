// Package providerfake is a backend-agnostic test double for provider.Provider:
// one struct with a function field per interface method, so a consumer test
// (internal/ui, internal/browse, cmd/sand — anything that depends on
// provider.Provider rather than a concrete backend) drives exactly the
// behaviour it cares about without composing a real lima.Client over a fake
// lima.Runner (the heavier route internal/ui's testProvider/newTestModelWithCli
// still take when a test genuinely needs limactl-shaped plumbing underneath)
// and without embedding a nil Provider and overriding one method — a pattern
// that compiles but panics with a nil-pointer dereference the moment a test
// forgets to override a method it ends up calling.
//
// Every field defaults to nil. An unset field returns the same "nothing here"
// zero value a real backend would report for an empty/absent result (a nil
// slice, an empty string, a nil error) rather than panicking, so a test that
// only cares about one method can construct a bare &Fake{SomeFunc: ...} and
// trust every other call to be an inert no-op instead of a crash.
package providerfake

import (
	"context"
	"io"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

// Provider is the function-field double. See the package doc for the defaulting
// contract.
type Provider struct {
	ListFunc   func() ([]vm.VM, error)
	GetFunc    func(name string) (vm.VM, error)
	StatusFunc func(name string) (string, error)

	StartFunc          func(name string) error
	StopFunc           func(name string) error
	DeleteFunc         func(name string, force bool) error
	StartStreamingFunc func(ctx context.Context, name string, out io.Writer) error
	StopStreamingFunc  func(ctx context.Context, name string, out io.Writer) error

	CreateFunc   func(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error
	RecreateFunc func(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error
	ResetFunc    func(ctx context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, out io.Writer) error

	ShellFunc          func(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error
	ShellStreamOutFunc func(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error
	ShellOutFunc       func(ctx context.Context, name string, argv ...string) ([]byte, error)
	CopyFunc           func(ctx context.Context, out io.Writer, recursive bool, src, dst string) error

	AttachArgvFunc func(v vm.VM) []string
	GuestHomeFunc  func(v vm.VM) string
	GuestUserFunc  func(v vm.VM) string
	GuestPathFunc  func(name, path string) string

	PreflightFunc func() error
}

// Compile-time proof the fake satisfies the whole seam.
var _ provider.Provider = (*Provider)(nil)

// --- Discovery ---

func (f *Provider) List() ([]vm.VM, error) {
	if f.ListFunc != nil {
		return f.ListFunc()
	}
	return nil, nil
}

func (f *Provider) Get(name string) (vm.VM, error) {
	if f.GetFunc != nil {
		return f.GetFunc(name)
	}
	return vm.VM{}, nil
}

func (f *Provider) Status(name string) (string, error) {
	if f.StatusFunc != nil {
		return f.StatusFunc(name)
	}
	return "", nil
}

// --- Power ---

func (f *Provider) Start(name string) error {
	if f.StartFunc != nil {
		return f.StartFunc(name)
	}
	return nil
}

func (f *Provider) Stop(name string) error {
	if f.StopFunc != nil {
		return f.StopFunc(name)
	}
	return nil
}

func (f *Provider) Delete(name string, force bool) error {
	if f.DeleteFunc != nil {
		return f.DeleteFunc(name, force)
	}
	return nil
}

func (f *Provider) StartStreaming(ctx context.Context, name string, out io.Writer) error {
	if f.StartStreamingFunc != nil {
		return f.StartStreamingFunc(ctx, name, out)
	}
	return nil
}

func (f *Provider) StopStreaming(ctx context.Context, name string, out io.Writer) error {
	if f.StopStreamingFunc != nil {
		return f.StopStreamingFunc(ctx, name, out)
	}
	return nil
}

// --- Provisioning lifecycle ---

func (f *Provider) Create(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error {
	if f.CreateFunc != nil {
		return f.CreateFunc(ctx, cfg, opts, out)
	}
	return nil
}

func (f *Provider) Recreate(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error {
	if f.RecreateFunc != nil {
		return f.RecreateFunc(ctx, cfg, opts, out)
	}
	return nil
}

func (f *Provider) Reset(ctx context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, out io.Writer) error {
	if f.ResetFunc != nil {
		return f.ResetFunc(ctx, cfg, opts, out)
	}
	return nil
}

// --- Guest transport ---

func (f *Provider) Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	if f.ShellFunc != nil {
		return f.ShellFunc(ctx, name, stdin, out, argv...)
	}
	return nil
}

func (f *Provider) ShellStreamOut(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	if f.ShellStreamOutFunc != nil {
		return f.ShellStreamOutFunc(ctx, name, stdin, out, argv...)
	}
	return nil
}

func (f *Provider) ShellOut(ctx context.Context, name string, argv ...string) ([]byte, error) {
	if f.ShellOutFunc != nil {
		return f.ShellOutFunc(ctx, name, argv...)
	}
	return nil, nil
}

func (f *Provider) Copy(ctx context.Context, out io.Writer, recursive bool, src, dst string) error {
	if f.CopyFunc != nil {
		return f.CopyFunc(ctx, out, recursive, src, dst)
	}
	return nil
}

// --- Interactive attach & guest paths ---

func (f *Provider) AttachArgv(v vm.VM) []string {
	if f.AttachArgvFunc != nil {
		return f.AttachArgvFunc(v)
	}
	return nil
}

func (f *Provider) GuestHome(v vm.VM) string {
	if f.GuestHomeFunc != nil {
		return f.GuestHomeFunc(v)
	}
	return ""
}

func (f *Provider) GuestUser(v vm.VM) string {
	if f.GuestUserFunc != nil {
		return f.GuestUserFunc(v)
	}
	return ""
}

func (f *Provider) GuestPath(name, path string) string {
	if f.GuestPathFunc != nil {
		return f.GuestPathFunc(name, path)
	}
	return name + ":" + path
}

// --- Preflight ---

func (f *Provider) Preflight() error {
	if f.PreflightFunc != nil {
		return f.PreflightFunc()
	}
	return nil
}
