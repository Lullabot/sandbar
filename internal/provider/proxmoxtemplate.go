package provider

// proxmoxtemplate.go implements the golden-template operations PR #70 (golden VM
// templates) adds to the Provider seam: SnapshotTemplate, DeleteTemplate, and
// TemplateDiskBytes. It is kept in its own file, isolated from proxmox.go and
// proxmoxprovision.go, so the eventual merge with #70 is a small additive change
// rather than a conflict with this backend's existing lifecycle code.
//
// As of this writing #70 has not landed on main (the Provider interface carries
// none of these three methods), so they are implemented here on *proxmoxProvider
// only, NOT added to the interface — that keeps this change purely additive.
// Whoever lands #70 later adds three lines to provider.go; nothing here needs to
// move.
//
// A PVE template already *is* the primitive golden templates want (an
// un-bootable, clone-only VM), so there is no emulation layer: SnapshotTemplate is
// a stop (if needed) + full clone + convert-to-template, mirroring the base
// template build in proxmoxprovision.go.

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lullabot/sandbar/internal/pve"
)

// SnapshotTemplate clones source into a new PVE template named templateName,
// stopping source first if it was running and restarting it afterward — on
// EITHER outcome, success or failure. Cloning a running VM is possible in PVE
// but produces a crash-consistent disk, so a snapshot always stops the source
// first to match what #70 specifies.
//
// The source's power state is the one thing this method promises to preserve no
// matter how it returns: a snapshot that silently leaves the user's VM stopped
// (or that a cancelled context leaves stopped) is worse than a failed snapshot.
// That is why the restart is a defer using context.WithoutCancel — a cancelled
// ctx must not skip it.
func (p *proxmoxProvider) SnapshotTemplate(ctx context.Context, source, templateName string, out io.Writer) error {
	vmid, st, err := p.resolve(ctx, source)
	if err != nil {
		return fmt.Errorf("proxmox: resolving snapshot source %s: %w", source, err)
	}

	wasRunning := st.Status == pveRunning
	if wasRunning {
		progress(out, "Shutting %s down to snapshot it\n", source)
		p.invalidateGuest(source)
		// A GRACEFUL shutdown (ACPI), not a hard stop: a hard-powered-off guest
		// never flushes its filesystem, so the cloned disk would be crash-
		// consistent — the very thing snapshotting a stopped source is meant to
		// avoid, and a way to lose the user VM's recently-written data. This
		// matches the base build, which also shuts down gracefully before its
		// convert.
		upid, err := p.client.ShutdownVM(ctx, vmid)
		if err != nil {
			return fmt.Errorf("proxmox: shutting %s down before snapshotting it: %w", source, err)
		}
		if err := p.client.WaitTask(ctx, upid.Raw); err != nil {
			return fmt.Errorf("proxmox: shutting %s down before snapshotting it: %w", source, err)
		}
		// Restore the source's power state no matter how we leave: a snapshot
		// that silently leaves the user's VM stopped is worse than a failed
		// snapshot. context.WithoutCancel is load-bearing — if the caller
		// cancelled, the restart must still run, or cancelling a snapshot
		// leaves the VM off.
		defer func() { _ = p.restart(context.WithoutCancel(ctx), source) }()
	}

	// CloneVMWithNextID retries the "already exists" collision with a fresh id
	// rather than deleting the id it tried to use — which, in our own pool, would
	// be another creator's VM. A non-collision synchronous error created nothing,
	// so there is nothing to clean up on that path either.
	progress(out, "Cloning %s into template %s (VMID %d)\n", source, templateName, vmid)
	newid, cUPID, err := p.client.CloneVMWithNextID(ctx, vmid, pve.CloneVMOptions{
		Name: templateName,
		// Pool membership on the clone is as non-negotiable here as it is on
		// every other clone this backend makes: without it the new template is
		// not a pool member and every later token-scoped permission check
		// against it (and clones OF it) fails.
		Pool: p.pool,
		// A full (independent) clone, so the template does not depend on the
		// source VM's disk staying put.
		Full:    true,
		Storage: p.storage,
	})
	if err != nil {
		return fmt.Errorf("proxmox: cloning %s into template %s: %w", source, templateName, err)
	}
	p.setVMID(templateName, newid)
	if err := p.client.WaitTask(ctx, cUPID.Raw); err != nil {
		// The clone TASK started under newid and failed, so that partial VM is
		// ours to purge (a synchronous collision never reaches here).
		p.cleanupVM(ctx, newid, templateName, out)
		return fmt.Errorf("proxmox: cloning %s into template %s: %w", source, templateName, err)
	}

	progress(out, "Converting %s to a template\n", templateName)
	tUPID, err := p.client.ConvertToTemplate(ctx, newid)
	if err != nil {
		// The clone succeeded but the template conversion did not: delete the
		// partial clone rather than leave a non-template VM occupying the VMID
		// under the name the caller asked for a template.
		p.cleanupVM(ctx, newid, templateName, out)
		return fmt.Errorf("proxmox: converting %s to a template: %w", templateName, err)
	}
	if err := p.client.WaitTask(ctx, tUPID.Raw); err != nil {
		p.cleanupVM(ctx, newid, templateName, out)
		return fmt.Errorf("proxmox: converting %s to a template: %w", templateName, err)
	}

	progress(out, "%s is ready\n", templateName)
	return nil
}

// restart starts name back up, bounded by powerTimeout rather than ctx's own
// deadline — the caller passes a context.WithoutCancel(ctx) here specifically so
// a cancelled snapshot still restarts its source, and WithoutCancel strips any
// existing deadline along with the cancellation, so this call must impose its
// own bound rather than potentially block forever.
func (p *proxmoxProvider) restart(ctx context.Context, name string) error {
	cctx, cancel := context.WithTimeout(ctx, powerTimeout)
	defer cancel()
	return p.start(cctx, name, nil)
}

// DeleteTemplate removes a PVE template, but only after confirming the target
// actually IS a template (template:1 in its config). That guard is the entire
// reason this is not just an alias for Delete: without it, a mistyped or stale
// templateName could destroy a live, in-use VM instead of a template.
func (p *proxmoxProvider) DeleteTemplate(ctx context.Context, templateName string) error {
	vmid, _, err := p.resolve(ctx, templateName)
	if err != nil {
		return fmt.Errorf("proxmox: resolving template %s: %w", templateName, err)
	}

	cfg, err := p.client.GetConfig(ctx, vmid)
	if err != nil {
		return fmt.Errorf("proxmox: reading %s's configuration: %w", templateName, err)
	}
	if !isTemplateConfig(cfg) {
		return fmt.Errorf("proxmox: %s (VMID %d) is not a template; refusing to delete it as one", templateName, vmid)
	}

	upid, err := p.client.DeleteVM(ctx, vmid, true)
	if err != nil {
		return fmt.Errorf("proxmox: deleting template %s: %w", templateName, err)
	}
	if err := p.client.WaitTask(ctx, upid.Raw); err != nil {
		return fmt.Errorf("proxmox: deleting template %s: %w", templateName, err)
	}

	p.forget(templateName)
	// Best-effort: the local state directory describes a template that no
	// longer exists. Failing the delete over it would report failure for an
	// operation that succeeded on the only host that matters.
	_ = os.RemoveAll(p.instanceDir(templateName))
	return nil
}

// isTemplateConfig reports whether cfg's "template" field marks the VM as a PVE
// template. GetConfig decodes into a free-form map, so the field arrives as
// whatever shape encoding/json picked for PVE's value — a JSON number decodes to
// float64, but a boolean or a quoted "1" are tolerated too rather than assumed
// away, since this check is the only thing standing between a mistyped name and
// destroying a live VM.
func isTemplateConfig(cfg pve.VMConfig) bool {
	switch v := cfg["template"].(type) {
	case float64:
		return v == 1
	case string:
		return v == "1"
	case bool:
		return v
	default:
		return false
	}
}

// TemplateDiskBytes reports templateName's boot disk size in bytes, or -1 when
// it cannot be determined. It deliberately does NOT read maxdisk from
// status/current (that is the boot disk's *provisioned* size, not its actual
// allocation) and does NOT read the status endpoint's `disk` field (PVE
// hardcodes it to 0 for a QEMU guest — upstream literally writes
// "$d->{disk} = 0; # no info available"). The only honest source is the boot
// disk's own volid looked up in the storage content listing.
func (p *proxmoxProvider) TemplateDiskBytes(templateName string) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()

	vmid, _, err := p.resolve(ctx, templateName)
	if err != nil {
		return -1
	}
	cfg, err := p.client.GetConfig(ctx, vmid)
	if err != nil {
		return -1
	}

	device := bootDiskDevice(cfg)
	if device == "" {
		return -1
	}
	volid := volidFromDiskConfig(cfg[device])
	if volid == "" {
		return -1
	}
	if p.storage == "" {
		return -1
	}

	items, err := p.client.StorageContent(ctx, p.storage)
	if err != nil {
		return -1
	}
	for _, it := range items {
		if it.VolID == volid {
			return it.Size
		}
	}
	return -1
}

// bootDiskDevice extracts the first device named in cfg's "boot" field — the
// modern "order=scsi0;ide2" form this provider itself writes on create (see
// pve.CreateVMOptions.formValues), and the only shape it is asked to parse back.
// An empty or unrecognised value yields "", which TemplateDiskBytes treats as
// "cannot be determined" rather than guessing a device.
func bootDiskDevice(cfg pve.VMConfig) string {
	boot, _ := cfg["boot"].(string)
	order := strings.TrimPrefix(boot, "order=")
	if order == "" {
		return ""
	}
	device, _, _ := strings.Cut(order, ";")
	return device
}

// volidFromDiskConfig extracts the volid (the part before the first comma) from
// a disk config value like "local-lvm:vm-101-disk-0,size=32G,discard=on". A
// non-string value, or an empty one, yields "".
func volidFromDiskConfig(v any) string {
	s, _ := v.(string)
	if s == "" {
		return ""
	}
	volid, _, _ := strings.Cut(s, ",")
	return volid
}
