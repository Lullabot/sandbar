package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
)

// runUpdates inspects one existing VM. It never reconciles the registry or
// changes the guest; absent provenance is reported as unknown history.
func runUpdates(args []string) error {
	fs := flag.NewFlagSet("updates", flag.ContinueOnError)
	profileName := fs.String("profile", "", "Connection profile owning NAME (default: resolve from managed VM records)")
	jsonOutput := fs.Bool("json", false, "Print machine-readable base and setup states")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: sand updates NAME [--profile NAME] [--json]")
		fmt.Fprintln(fs.Output(), "Read base and setup update status without changing the VM. Use sand reset NAME --preserve-home to apply updates explicitly.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderFlags(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("sand updates: need exactly one VM name")
	}
	name := fs.Arg(0)
	store, err := profiles.LoadReadOnly()
	if err != nil {
		return fmt.Errorf("sand updates: %w", err)
	}
	reg, err := registry.LoadReadOnly()
	if reg == nil {
		reg = registry.NewEmpty()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	target, err := resolveVMProfile(store, reg, name, *profileName)
	if err != nil {
		return fmt.Errorf("sand updates: %w", err)
	}
	p, _, err := providerForProfile(target)
	if err != nil {
		return fmt.Errorf("sand updates: %w", err)
	}
	return doUpdates(context.Background(), p, name, target.Name, *jsonOutput, os.Stdout)
}

func doUpdates(ctx context.Context, p provider.Provider, name, profileName string, jsonOutput bool, out io.Writer) error {
	if _, err := p.Get(name); err != nil {
		return fmt.Errorf("sand updates: %w", err)
	}
	status := provider.UpdateStatus{Base: provider.UpdateUnknown, Setup: provider.UpdateUnknown}
	if pv, ok := p.(provider.Provenancer); ok {
		marker, found, err := pv.ProvenanceOf(ctx, name)
		if err != nil {
			return fmt.Errorf("sand updates: reading %s provenance: %w", name, err)
		}
		if found {
			status = provider.CheckUpdates(ctx, p, marker)
		}
	}
	if jsonOutput {
		return json.NewEncoder(out).Encode(struct {
			Name    string `json:"name"`
			Profile string `json:"profile"`
			provider.UpdateStatus
		}{Name: name, Profile: profileName, UpdateStatus: status})
	}
	fmt.Fprintf(out, "%s [%s]\n  base: %s\n  setup: %s\n", name, profileName, updateLabel("base", status.Base), updateLabel("setup", status.Setup))
	if status.Base != provider.UpdateCurrent || status.Setup != provider.UpdateCurrent {
		fmt.Fprintf(out, "To apply updates, explicitly reset with: sand reset %s --profile %s --preserve-home\n", shellArg(name), shellArg(profileName))
	}
	return nil
}

func shellArg(s string) string {
	if s != "" {
		simple := true
		for _, c := range s {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				simple = false
				break
			}
		}
		if simple {
			return s
		}
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func updateLabel(kind string, state provider.UpdateState) string {
	switch state {
	case provider.UpdateCurrent:
		return "current"
	case provider.UpdateAvailable:
		return "update available"
	default:
		return kind + " version unknown"
	}
}
