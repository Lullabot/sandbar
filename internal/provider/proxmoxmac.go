// proxmoxmac.go keeps a reset VM's MAC addresses.
//
// A Proxmox reset deletes the VM and clones a fresh one from the template, and
// PVE assigns every clone brand-new random MACs. That is right for a create and
// wrong for a reset: a reset's whole promise is "this VM again, rebuilt" — same
// name, same project, same sizing — and the MAC is the identity the NETWORK
// knows it by. A DHCP reservation, a firewall rule, a switch port ACL or an
// address book entry keyed to the old MAC all silently stop matching, and none
// of that lives anywhere sand can put back afterwards: it is on the router.
//
// So the MAC is read off the VM before it is destroyed and written onto the
// clone before it first boots. Neither half is allowed to fail the reset. The
// VM that comes back with a fresh MAC is a working VM with an inconvenience;
// aborting a rebuild over it — or worse, purging the just-built clone — would
// turn a cosmetic problem into a destroyed one, so both halves warn and carry
// on.
//
// It is unconditional rather than a flag. The alternative reading, "a reset
// should look like a new machine to the network", is not a thing anyone has
// asked a reset for, and a user who genuinely wants a new MAC can delete the VM
// and create it again — which is exactly the verb that means "a different VM".
package provider

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/lullabot/sandbar/internal/pve"
)

// netDeviceKey matches the config keys that are NICs (net0, net1, …) and
// nothing else. PVE's config map mixes them in with everything from scsi0 to
// smbios1, and a prefix test on "net" alone would also match "nets"-shaped keys
// a future PVE could add.
var netDeviceKey = regexp.MustCompile(`^net\d+$`)

// nicMACs reads the MAC of every NIC on the named VM, keyed by device
// ("net0" → "bc:24:11:aa:bb:cc").
//
// A VM that is not there any more yields an empty map and no error: a reset can
// legitimately run against a name whose VM has already been destroyed (that is
// the shape `sand create --recreate` takes when the guest is gone), and there is
// no MAC to keep in that case — not a failure to report.
func (p *proxmoxProvider) nicMACs(ctx context.Context, name string) (map[string]string, error) {
	vmid, _, err := p.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	cfg, err := p.client.GetConfig(ctx, vmid)
	if err != nil {
		return nil, err
	}
	return nicMACsFromConfig(cfg), nil
}

// nicMACsFromConfig pulls the netN MACs out of a PVE config map.
func nicMACsFromConfig(cfg pve.VMConfig) map[string]string {
	macs := map[string]string{}
	for key, raw := range cfg {
		if !netDeviceKey.MatchString(key) {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			continue
		}
		if mac := net0MAC(value); mac != "" {
			macs[key] = mac
		}
	}
	return macs
}

// applyNICMACs writes the remembered MACs onto the freshly cloned VM, and
// reports what it changed on out.
//
// Only devices the clone ACTUALLY HAS are touched, and each one keeps every
// other field PVE gave it (bridge, firewall, mtu, vlan tag): the MAC is
// substituted into the clone's own net line rather than the line being rebuilt
// from what sand thinks a NIC should look like. An operator who tagged a VLAN on
// the template would otherwise have it quietly dropped by a reset.
//
// A device the old VM had and the clone does not is skipped rather than created.
// Restoring a NIC that no longer exists would mean inventing its bridge and
// model too, which is a different feature ("put the network layout back") and
// one with a much larger blast radius than keeping an address.
//
// Every failure here is a warning, never an error — see this file's doc comment.
func (p *proxmoxProvider) applyNICMACs(ctx context.Context, vmid int, macs map[string]string, out io.Writer) {
	if len(macs) == 0 {
		return
	}
	cfg, err := p.client.GetConfig(ctx, vmid)
	if err != nil {
		progress(out, "Warning: could not read the new VM's network configuration, so its MAC address(es) will be newly generated: %v\n", err)
		return
	}

	form := url.Values{}
	var changed []string
	for device, mac := range macs {
		current, ok := cfg[device].(string)
		if !ok {
			continue // the clone has no such NIC; see the doc comment
		}
		if net0MAC(current) == mac {
			continue // already right (a clone that inherited it, or a re-run)
		}
		form.Set(device, netWithMAC(current, mac))
		changed = append(changed, fmt.Sprintf("%s=%s", device, mac))
	}
	if len(form) == 0 {
		return
	}
	// Sorted so the progress line — and the test that reads it — does not depend
	// on map iteration order.
	sort.Strings(changed)

	if err := p.client.SetConfigSync(ctx, vmid, form); err != nil {
		progress(out, "Warning: could not restore the previous MAC address(es) (%s); the VM keeps its newly generated one(s), which may break a DHCP reservation or firewall rule: %v\n", strings.Join(changed, " "), err)
		return
	}
	progress(out, "Kept the previous MAC address(es): %s\n", strings.Join(changed, " "))
}

// netWithMAC returns a PVE netN config value with its MAC replaced by mac,
// leaving every other field untouched and in place.
//
// PVE writes the MAC as the value of the MODEL key ("virtio=BC:24:…,bridge=…")
// but accepts and sometimes writes a separate "macaddr=…" instead, so both
// spellings are handled the same way net0MAC reads them: whichever field holds
// something MAC-shaped is the one rewritten. A value with no MAC at all gains a
// macaddr field rather than being left alone, since the caller's whole purpose
// is that this device ends up with that address.
//
// The MAC is written UPPER CASE, which is how PVE renders it in its own config
// and UI; PVE accepts either, and matching it keeps a later diff of the config
// free of case-only noise.
func netWithMAC(net, mac string) string {
	want := strings.ToUpper(mac)
	fields := strings.Split(net, ",")
	for i, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok || !isMACAddress(value) {
			continue
		}
		fields[i] = key + "=" + want
		return strings.Join(fields, ",")
	}
	return net + ",macaddr=" + want
}
