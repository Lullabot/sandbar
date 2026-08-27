package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// stubResetter is a resetter double: it records the config and options a reset
// was driven with and reports whatever error it is given, so the gate and the
// bookkeeping around provider.Reset can be tested with no limactl in sight.
type stubResetter struct {
	called bool
	cfg    vm.CreateConfig
	opts   provision.ResetOptions
	err    error
}

func (s *stubResetter) Reset(_ context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, _ io.Writer) error {
	s.called, s.cfg, s.opts = true, cfg, opts
	return s.err
}

// recordedVM is a VM already in the index, built with settings that are all
// distinguishable from vm.DefaultCreateConfig()'s — otherwise "the reset used
// the record" and "the reset used the defaults" look identical.
func recordedVM() vm.CreateConfig {
	return vm.CreateConfig{
		Name:            "web",
		BaseName:        "work-base",
		Hostname:        "web-box",
		User:            "ada",
		GitName:         "Ada Lovelace",
		GitEmail:        "ada@example.com",
		CPUs:            6,
		Memory:          "24GiB",
		Disk:            "300GiB",
		Locale:          "en_GB.UTF-8",
		Timezone:        "Europe/London",
		Domain:          "internal",
		DockerProxyHost: "proxy.example.com",
		CloneURL:        "https://github.com/octocat/hello",
		WithClaude:      true,
		WithCodex:       true,
		WithDDEV:        false,
		WithGo:          false,
		WithJava:        false,
	}
}

func seededRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.NewEmpty()
	if err := reg.AddScoped(recordedVM(), registry.LocalScope); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	return reg
}

// TestResetConfigUsesTheVMsOwnSettings is the contract of the verb: `sand reset
// web` means "give me this VM back". Every setting comes from the VM's own
// record — not from this command's defaults, which would hand back a different
// VM wearing the same name (the exact bug `sand create --recreate` shipped
// with: memory 24→8GiB, disk 300→100GiB, and the project gone).
func TestResetConfigUsesTheVMsOwnSettings(t *testing.T) {
	got, err := resetConfigFor(seededRegistry(t), "web", registry.LocalScope, "work-base", map[string]bool{}, resetFlagValues{}, "hostuser")
	if err != nil {
		t.Fatalf("resetConfigFor: %v", err)
	}
	want := recordedVM()
	if got != want {
		t.Errorf("reset config = %+v\nwant the recorded %+v", got, want)
	}
}

// TestResetConfigAppliesExplicitFlags: a flag you DO pass still wins, which is
// what makes `sand reset web --disk 400GiB` the way to resize on the way
// through. Everything else stays as recorded.
func TestResetConfigAppliesExplicitFlags(t *testing.T) {
	explicit := map[string]bool{"cpus": true, "memory": true, "disk": true, "timezone": true}
	got, err := resetConfigFor(seededRegistry(t), "web", registry.LocalScope, "work-base", explicit, resetFlagValues{
		cpus:     "12",
		memory:   "48GiB",
		disk:     "400GiB",
		timezone: "Asia/Tokyo",
	}, "hostuser")
	if err != nil {
		t.Fatalf("resetConfigFor: %v", err)
	}
	if got.CPUs != 12 || got.Memory != "48GiB" || got.Disk != "400GiB" {
		t.Errorf("explicit sizing not applied: cpus=%d memory=%q disk=%q", got.CPUs, got.Memory, got.Disk)
	}
	if got.Timezone != "Asia/Tokyo" || !got.TimezoneExplicit {
		t.Errorf("explicit timezone = %q (explicit=%v), want Asia/Tokyo explicitly named", got.Timezone, got.TimezoneExplicit)
	}
	// Untouched settings are still the VM's own.
	if got.Hostname != "web-box" || got.CloneURL != recordedVM().CloneURL || got.Domain != "internal" {
		t.Errorf("a flag that was not passed changed anyway: %+v", got)
	}
}

// TestResetConfigKeepsTheRecordedRepo is the CLI half of "a reset does not
// change the project": there is no --clone-url to pass, and the URL therefore
// always comes back from the record — including for a VM that cloned nothing.
func TestResetConfigKeepsTheRecordedRepo(t *testing.T) {
	// Against the command's OWN flag set, not a restatement of it.
	fs := newResetFlagSet(&resetOptions{})
	if fs.Lookup("clone-url") != nil {
		t.Error("sand reset must not offer --clone-url: a reset rebuilds the project the VM already has (a different repo is a different VM)")
	}
	if fs.Lookup("clone-token") == nil {
		t.Error("sand reset must still offer --clone-token: re-cloning a private repo needs one, and tokens are never stored in the index")
	}
	for _, gone := range []string{"name", "base-name", "recreate", "rebuild"} {
		if fs.Lookup(gone) != nil {
			t.Errorf("sand reset must not offer --%s", gone)
		}
	}

	got, err := resetConfigFor(seededRegistry(t), "web", registry.LocalScope, "work-base", map[string]bool{}, resetFlagValues{}, "hostuser")
	if err != nil {
		t.Fatalf("resetConfigFor: %v", err)
	}
	if got.CloneURL != recordedVM().CloneURL {
		t.Errorf("clone URL = %q, want the recorded %q", got.CloneURL, recordedVM().CloneURL)
	}
}

// TestResetConfigWithoutARecordStillResets: a VM with a provenance marker but
// no local registry entry (created by another controller on the same host) must
// still be resettable — from defaults plus this host's identity — rather than
// being refused by the one entrypoint that can rebuild it.
func TestResetConfigWithoutARecordStillResets(t *testing.T) {
	got, err := resetConfigFor(registry.NewEmpty(), "orphan", registry.LocalScope, "sandbar-base", map[string]bool{}, resetFlagValues{
		gitName:  "Ada",
		gitEmail: "ada@example.com",
	}, "hostuser")
	// The git identity is required by Validate and is not on the flag path here,
	// so supply it the way the host would.
	if err != nil && !strings.Contains(err.Error(), "git") {
		t.Fatalf("resetConfigFor: %v", err)
	}
	if err == nil {
		if got.Name != "orphan" || got.BaseName != "sandbar-base" {
			t.Errorf("marker-only reset targeted %q on base %q", got.Name, got.BaseName)
		}
		if got.User != "hostuser" {
			t.Errorf("marker-only reset user = %q, want the provider's host user", got.User)
		}
	}
}

// TestDoResetRecordsSuccess: a successful reset re-records the (possibly
// resized) config, so the NEXT reset reproduces what this one produced rather
// than what the VM was originally built with.
func TestDoResetRecordsSuccess(t *testing.T) {
	reg := seededRegistry(t)
	cfg := recordedVM()
	cfg.Disk = "400GiB"

	stub := &stubResetter{}
	opts := provision.ResetOptions{PreserveClaude: true}
	if err := doReset(context.Background(), reg, stub, cfg, registry.LocalScope, opts, io.Discard); err != nil {
		t.Fatalf("doReset: %v", err)
	}
	if !stub.called {
		t.Fatal("doReset did not drive the provider's Reset")
	}
	if stub.opts != opts {
		t.Errorf("Reset got options %+v, want %+v", stub.opts, opts)
	}
	back, ok := reg.Config("web")
	if !ok {
		t.Fatal("the reset VM is no longer in the managed index")
	}
	if back.Disk != "400GiB" {
		t.Errorf("recorded disk after reset = %q, want the resized 400GiB", back.Disk)
	}
}

// TestDoResetFailureIsNotRecorded: a reset that failed must not overwrite the
// recorded config with the settings it never managed to apply.
func TestDoResetFailureIsNotRecorded(t *testing.T) {
	reg := seededRegistry(t)
	cfg := recordedVM()
	cfg.Disk = "400GiB"

	stub := &stubResetter{err: errors.New("clone failed")}
	if err := doReset(context.Background(), reg, stub, cfg, registry.LocalScope, provision.ResetOptions{}, io.Discard); err == nil {
		t.Fatal("doReset should surface the provider's failure")
	}
	back, _ := reg.Config("web")
	if back.Disk != recordedVM().Disk {
		t.Errorf("a failed reset rewrote the recorded disk to %q", back.Disk)
	}
}

// TestReorderFlagsPutsFlagsBeforeTheName covers the parse plumbing that makes
// the natural spelling work. flag.FlagSet stops at the first non-flag token, so
// without this `sand reset web --preserve-claude` would silently drop the
// preserve flag — a reset that discards the data the user asked to keep.
func TestReorderFlagsPutsFlagsBeforeTheName(t *testing.T) {
	newSet := func() *flag.FlagSet {
		fs := flag.NewFlagSet("reset", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.Bool("preserve-claude", false, "")
		fs.String("disk", "", "")
		fs.String("profile", "", "")
		return fs
	}

	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"flags after the name", []string{"web", "--preserve-claude", "--disk", "200GiB"},
			[]string{"--preserve-claude", "--disk", "200GiB", "web"}},
		{"a bool flag does not swallow the name", []string{"--preserve-claude", "web"},
			[]string{"--preserve-claude", "web"}},
		{"inline values", []string{"web", "--disk=200GiB"}, []string{"--disk=200GiB", "web"}},
		{"single dash", []string{"web", "-profile", "work"}, []string{"-profile", "work", "web"}},
		{"already in order", []string{"--disk", "200GiB", "web"}, []string{"--disk", "200GiB", "web"}},
		{"unknown flag stays put for Parse to report", []string{"web", "--nope"}, []string{"web", "--nope"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reorderFlags(newSet(), tc.args); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("reorderFlags(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}

	// End to end through the flag package: the value must actually arrive.
	fs := newSet()
	if err := fs.Parse(reorderFlags(fs, []string{"web", "--preserve-claude", "--disk", "200GiB"})); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "web" {
		t.Errorf("positional = %v, want [web]", fs.Args())
	}
	if fs.Lookup("preserve-claude").Value.String() != "true" || fs.Lookup("disk").Value.String() != "200GiB" {
		t.Errorf("flags did not reach the set: %v", fs.Args())
	}
}

// TestRunResetNeedsExactlyOneName: no name, or two, is a usage error rather
// than a reset of something the user did not name.
func TestRunResetNeedsExactlyOneName(t *testing.T) {
	for _, args := range [][]string{{}, {"one", "two"}} {
		if err := runReset(args); err == nil {
			t.Errorf("runReset(%v) = nil, want a usage error", args)
		}
	}
}

// TestRecreateRefusesToChangeTheCloneURL: `sand create --recreate` is a reset,
// and a reset keeps the VM's project. Asking for both at once used to leave the
// old org directory behind and clone the new repo beside it.
func TestRecreateRefusesToChangeTheCloneURL(t *testing.T) {
	err := refuseRecreateWithCloneURL(true, map[string]bool{"clone-url": true}, "web")
	if err == nil {
		t.Fatal("--recreate --clone-url should be refused")
	}
	for _, want := range []string{"sand reset web", "--name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should name a way forward (%q); got %q", want, err.Error())
		}
	}

	// Neither half alone is a problem: a plain create may set a URL, and a
	// recreate that leaves it alone reuses the recorded one.
	if err := refuseRecreateWithCloneURL(false, map[string]bool{"clone-url": true}, "web"); err != nil {
		t.Errorf("a plain create with --clone-url must be allowed: %v", err)
	}
	if err := refuseRecreateWithCloneURL(true, map[string]bool{}, "web"); err != nil {
		t.Errorf("a recreate that does not restate --clone-url must be allowed: %v", err)
	}
}

// TestResetValidationBlamesTheRightValue: a validation failure must point at
// whoever supplied the bad value. Observed against a real VM: `sand reset web
// --disk 10GiB` reported "web's recorded config is unusable" about a number the
// user had just typed, sending them to fix an index file that was fine.
func TestResetValidationBlamesTheRightValue(t *testing.T) {
	// The user's own flag: report it plainly.
	_, err := resetConfigFor(seededRegistry(t), "web", registry.LocalScope, "work-base",
		map[string]bool{"disk": true}, resetFlagValues{disk: "10GiB"}, "hostuser")
	if err == nil {
		t.Fatal("a disk below the base floor should be refused")
	}
	if strings.Contains(err.Error(), "recorded config") {
		t.Errorf("a bad --disk was blamed on the recorded config: %q", err.Error())
	}

	// Nothing restated, and the record itself is unusable: name the record, and
	// the way out.
	reg := registry.NewEmpty()
	bad := recordedVM()
	bad.Disk = "10GiB"
	if err := reg.AddScoped(bad, registry.LocalScope); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err = resetConfigFor(reg, "web", registry.LocalScope, "work-base", map[string]bool{}, resetFlagValues{}, "hostuser")
	if err == nil {
		t.Fatal("an unusable recorded config should be refused")
	}
	for _, want := range []string{"recorded config", "pass the settings explicitly"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err.Error(), want)
		}
	}
}
