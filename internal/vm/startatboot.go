package vm

import (
	"fmt"
	"strings"
)

// Start-at-boot modes for a Proxmox VM. They are the one user-facing name for
// the pair of fields CreateConfig records (DisableStartAtBoot and
// BootFollowsPower), which stay separate so registry records and provenance
// markers written before the mode existed keep their meaning.
const (
	// StartAtBootAlways starts the VM whenever the Proxmox host boots. It is
	// the default and the behaviour of every VM recorded before the modes.
	StartAtBootAlways = "always"
	// StartAtBootIfRunning starts the VM at host boot only if it was running:
	// sandbar's own start and stop turn PVE's onboot flag on and off.
	StartAtBootIfRunning = "if-running"
	// StartAtBootNever never starts the VM at host boot.
	StartAtBootNever = "never"
)

// StartAtBootModes lists the modes in the order the create and reset forms
// cycle through them.
var StartAtBootModes = []string{StartAtBootIfRunning, StartAtBootAlways, StartAtBootNever}

// StartAtBoot returns the config's start-at-boot mode. DisableStartAtBoot wins
// over BootFollowsPower, matching what the provider does with the pair.
func (c CreateConfig) StartAtBoot() string {
	switch {
	case c.DisableStartAtBoot:
		return StartAtBootNever
	case c.BootFollowsPower:
		return StartAtBootIfRunning
	}
	return StartAtBootAlways
}

// SetStartAtBoot records mode in the config's two fields. Both are always
// written, so switching modes never leaves a stale half behind.
func (c *CreateConfig) SetStartAtBoot(mode string) error {
	switch mode {
	case StartAtBootAlways:
		c.DisableStartAtBoot, c.BootFollowsPower = false, false
	case StartAtBootIfRunning:
		c.DisableStartAtBoot, c.BootFollowsPower = false, true
	case StartAtBootNever:
		c.DisableStartAtBoot, c.BootFollowsPower = true, false
	default:
		return fmt.Errorf("invalid start-at-boot mode %q: use %s", mode, strings.Join(StartAtBootModes, ", "))
	}
	return nil
}
