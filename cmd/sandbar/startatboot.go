package main

import (
	"fmt"

	"github.com/lullabot/sandbar/internal/vm"
)

// startAtBootUsage is the --start-at-boot help, shared by create and reset.
const startAtBootUsage = "Proxmox host boot behaviour `MODE`: if-running (start only if the VM was left running; sandbar's start and stop set it), always, or never"

// startAtBootExplicit reports whether either start-at-boot flag was passed.
func startAtBootExplicit(explicit map[string]bool) bool {
	return explicit["start-at-boot"] || explicit["no-start-at-boot"]
}

// startAtBootFromFlags resolves the start-at-boot mode the user passed.
// --no-start-at-boot predates --start-at-boot and stays as a shorthand: set
// true it means never, and set false (re-enabling startup on a reset) it
// means always. Passing both is accepted only when they agree.
func startAtBootFromFlags(explicit map[string]bool, mode string, noStart bool) (string, error) {
	legacy := ""
	if explicit["no-start-at-boot"] {
		legacy = vm.StartAtBootAlways
		if noStart {
			legacy = vm.StartAtBootNever
		}
	}
	if !explicit["start-at-boot"] {
		return legacy, nil
	}
	var probe vm.CreateConfig
	if err := probe.SetStartAtBoot(mode); err != nil {
		return "", fmt.Errorf("--start-at-boot: %w", err)
	}
	if legacy != "" && legacy != mode {
		return "", fmt.Errorf("--start-at-boot=%s conflicts with --no-start-at-boot=%t", mode, noStart)
	}
	return mode, nil
}
