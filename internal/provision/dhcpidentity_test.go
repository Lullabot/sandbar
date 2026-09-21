package provision

import (
	"strings"
	"testing"
)

// Two tasks in roles/base decide what a guest tells its DHCP server about
// itself: which client identity it presents, and under which name. Both fail
// silently and off-box — the guest is fine, the router's answer is wrong — so
// nothing inside a VM ever catches a regression here. These read the embedded
// playbook directly (baseTasks, fstune_test.go); the molecule suite converges
// `base` in a container, which has neither networkd nor a DHCP server.

// TestDHCPIdentityFollowsTheMAC pins the networkd drop-in that makes a guest's
// DHCP client identity derive from its MAC address rather than its machine-id.
// The default (DUIDType=vendor) derives it from /etc/machine-id, which the
// Proxmox template build deliberately empties so each clone mints a fresh one
// — so a rebuild that restores the VM's previous MAC would still present a new
// identity, be handed a different address, and lose the lease and DNS name
// that preserving the MAC exists to keep. The two settings are a pair: whoever
// removes this file must revisit that MAC preservation, not just this task.
func TestDHCPIdentityFollowsTheMAC(t *testing.T) {
	const wantDest = "/etc/systemd/networkd.conf.d/10-sand-dhcp-identity.conf"

	var found bool
	for _, task := range baseTasks(t) {
		copyArgs, _ := task["ansible.builtin.copy"].(map[string]any)
		if dest, _ := copyArgs["dest"].(string); dest != wantDest {
			continue
		}
		found = true
		name, _ := task["name"].(string)
		content, _ := copyArgs["content"].(string)
		if !strings.Contains(content, "DUIDType=link-layer") {
			t.Errorf("the %q task writes %s without DUIDType=link-layer (content: %q).\nWithout it networkd"+
				" falls back to a machine-id-derived DUID, and a rebuild with a preserved MAC still loses its"+
				" lease.", name, wantDest, content)
		}
		if !strings.Contains(content, "[DHCPv4]") {
			t.Errorf("the %q task writes %s with no [DHCPv4] section header, so networkd ignores the"+
				" setting entirely (content: %q)", name, wantDest, content)
		}
		if when, ok := task["when"]; ok {
			t.Errorf("the %q task is gated by `when: %v`.\nIf that gate excludes the finalize phase, a VM"+
				" cloned from a base image built before this drop-in existed never receives it — and its"+
				" every rebuild keeps drawing a new address. It is one small file; re-paying it per clone"+
				" is why it is ungated.", name, when)
		}
	}
	if !found {
		t.Fatalf("no task in %s writes %s. Task names:\n%s", baseTasksPath, wantDest, taskNames(baseTasks(t)))
	}
}

// TestHostnameIsAnnouncedToDHCP guards the renew that follows the rename. A
// guest's lease is taken before it has a name — the template ships no
// /etc/hostname, deliberately, so that the base's name is not what gets
// registered — and renaming the guest afterwards tells the DHCP server
// nothing. Without this task the correct name reaches DNS only at T1, half a
// lease later.
//
// It must use `renew` rather than `reconfigure`: reconfigure tears the link
// down, and the provisioning ssh session is on it. And it must stay
// unconditional — as a handler on the hostname task it would not fire at all
// on the common path, where cloud-init has already applied the right name from
// the PVE VM name and Ansible's own hostname task reports no change.
func TestHostnameIsAnnouncedToDHCP(t *testing.T) {
	var found bool
	for _, task := range baseTasks(t) {
		// The task uses shell's `cmd:` form, not its free-form one: the awk
		// braces in the script read as an unbalanced Jinja block to Ansible's
		// argument splitter and fail the whole play at parse time.
		shellArgs, _ := task["ansible.builtin.shell"].(map[string]any)
		script, _ := shellArgs["cmd"].(string)
		if !strings.Contains(script, "networkctl") {
			continue
		}
		found = true
		name, _ := task["name"].(string)
		if strings.Contains(script, "networkctl reconfigure") {
			t.Errorf("the %q task uses `networkctl reconfigure`, which drops the interface's address —"+
				" and the provisioning ssh session is on that address. `networkctl renew` re-announces"+
				" the name while keeping the lease.", name)
		}
		if !strings.Contains(script, "networkctl renew") {
			t.Errorf("the %q task no longer runs `networkctl renew`; the new hostname then reaches the"+
				" DHCP server only at T1 (script: %q)", name, script)
		}
		if failed, ok := task["failed_when"].(bool); !ok || failed {
			t.Errorf("the %q task is not `failed_when: false` (got %v).\nEvery failure here is cosmetic —"+
				" a name that corrects itself at the next renewal — and must never fail a provision.",
				name, task["failed_when"])
		}
		if when, ok := task["when"]; ok {
			t.Errorf("the %q task is gated by `when: %v`; the rename it announces happens on every"+
				" phase, including a clone's finalize, which is the one that matters most.", name, when)
		}
		if _, ok := task["notify"]; ok {
			t.Errorf("the %q task is wired as a notify/handler pair; on the common path cloud-init has"+
				" already set the right hostname, Ansible's hostname task reports no change, and the"+
				" handler never fires", name)
		}
	}
	if !found {
		t.Fatalf("no task in %s announces the hostname over DHCP. Task names:\n%s", baseTasksPath, taskNames(baseTasks(t)))
	}
}
