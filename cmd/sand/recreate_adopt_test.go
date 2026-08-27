package main

import (
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

// recordedConfig is a VM as the managed registry remembers it: nothing like the
// flag defaults, so an adoption that silently does nothing cannot pass.
func recordedConfig() vm.CreateConfig {
	return vm.CreateConfig{
		Name:             "mybox",
		BaseName:         "sandbar-base",
		Hostname:         "mybox-host",
		User:             "recorded-user",
		GitName:          "Recorded Name",
		GitEmail:         "recorded@example.com",
		CPUs:             8,
		Memory:           "16GiB",
		Disk:             "200GiB",
		Locale:           "en_CA.UTF-8",
		Timezone:         "America/Toronto",
		TimezoneExplicit: true,
		Domain:           "internal",
		DockerProxyHost:  "proxy.example.com",
		CloneURL:         "https://github.com/lullabot/sandbar",
	}
}

// flagDefaultConfig is what a bare `sand create --recreate --name mybox` builds
// before adoption: this flag set's defaults, plus the host-derived identity.
func flagDefaultConfig() vm.CreateConfig {
	cfg := vm.DefaultCreateConfig()
	cfg.Name = "mybox"
	cfg.User = "host-user"
	cfg.GitName = "Host Name"
	cfg.GitEmail = "host@example.com"
	cfg.Timezone = "Etc/UTC"
	return cfg
}

// TestAdoptRecordedConfigRestoresTheVM is the regression test for a --recreate
// that returned a DIFFERENT VM: with no flags beyond --name, the memory and disk
// came back at the flag defaults and the clone URL was dropped, so the project
// checkout the VM existed for was simply gone.
func TestAdoptRecordedConfigRestoresTheVM(t *testing.T) {
	cfg := flagDefaultConfig()
	rec := recordedConfig()

	adoptRecordedConfig(&cfg, rec, map[string]bool{})

	if cfg.CloneURL != rec.CloneURL {
		t.Errorf("CloneURL = %q, want the recorded %q — the project would not be cloned back", cfg.CloneURL, rec.CloneURL)
	}
	if cfg.CPUs != rec.CPUs || cfg.Memory != rec.Memory || cfg.Disk != rec.Disk {
		t.Errorf("sizing = %d/%q/%q, want the recorded %d/%q/%q", cfg.CPUs, cfg.Memory, cfg.Disk, rec.CPUs, rec.Memory, rec.Disk)
	}
	if cfg.Hostname != rec.Hostname || cfg.User != rec.User {
		t.Errorf("identity = %q/%q, want the recorded %q/%q", cfg.Hostname, cfg.User, rec.Hostname, rec.User)
	}
	if cfg.GitName != rec.GitName || cfg.GitEmail != rec.GitEmail {
		t.Errorf("git identity = %q/%q, want the recorded %q/%q", cfg.GitName, cfg.GitEmail, rec.GitName, rec.GitEmail)
	}
	if cfg.Locale != rec.Locale || cfg.Domain != rec.Domain || cfg.DockerProxyHost != rec.DockerProxyHost {
		t.Errorf("locale/domain/proxy = %q/%q/%q, want the recorded values", cfg.Locale, cfg.Domain, cfg.DockerProxyHost)
	}
	if cfg.Timezone != rec.Timezone || !cfg.TimezoneExplicit {
		t.Errorf("timezone = %q (explicit %v), want %q (explicit true)", cfg.Timezone, cfg.TimezoneExplicit, rec.Timezone)
	}
}

// TestAdoptRecordedConfigLetsExplicitFlagsWin keeps --recreate usable as the way
// to resize or re-point a VM: a flag the user actually passed is never
// overwritten by the record. Without this, adoption would turn --recreate into a
// command that ignores its own flags.
func TestAdoptRecordedConfigLetsExplicitFlagsWin(t *testing.T) {
	cfg := flagDefaultConfig()
	cfg.Disk = "500GiB"
	cfg.CloneURL = "https://github.com/lullabot/other"
	cfg.Timezone, cfg.TimezoneExplicit = "Europe/Berlin", true

	adoptRecordedConfig(&cfg, recordedConfig(), map[string]bool{
		"disk": true, "clone-url": true, "timezone": true,
	})

	if cfg.Disk != "500GiB" {
		t.Errorf("Disk = %q, want the explicitly passed 500GiB", cfg.Disk)
	}
	if cfg.CloneURL != "https://github.com/lullabot/other" {
		t.Errorf("CloneURL = %q, want the explicitly passed one", cfg.CloneURL)
	}
	if cfg.Timezone != "Europe/Berlin" {
		t.Errorf("Timezone = %q, want the explicitly passed Europe/Berlin", cfg.Timezone)
	}
	// Everything NOT passed still adopts, or the test above would be the only
	// thing holding this together.
	if cfg.Memory != "16GiB" {
		t.Errorf("Memory = %q, want the recorded 16GiB", cfg.Memory)
	}
}

// TestAdoptRecordedConfigIgnoresAnEmptyRecord guards the pre-snapshot registry
// entry (recorded before configs were kept): adopting its zero values would
// blank the identity the flag layer just derived from the host, and Validate
// would then refuse a recreate that used to work.
func TestAdoptRecordedConfigIgnoresAnEmptyRecord(t *testing.T) {
	cfg := flagDefaultConfig()
	before := cfg

	adoptRecordedConfig(&cfg, vm.CreateConfig{Name: "mybox"}, map[string]bool{})

	if cfg.User != before.User || cfg.GitName != before.GitName || cfg.GitEmail != before.GitEmail {
		t.Errorf("an empty record blanked the host-derived identity: %+v", cfg)
	}
	if cfg.CPUs != before.CPUs || cfg.Memory != before.Memory || cfg.Disk != before.Disk {
		t.Errorf("an empty record blanked the sizing: %d/%q/%q", cfg.CPUs, cfg.Memory, cfg.Disk)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("config is unusable after adopting an empty record: %v", err)
	}
}

// TestAdoptRecordedConfigKeepsTheBaseImage covers the field a recreate cannot
// afford to default: --base-name. A VM built on its own base and recreated
// without restating it would be cloned from the DEFAULT base — a different
// image with a different tool-set — and, with no default base on the host yet,
// the recreate would quietly build a whole second one.
func TestAdoptRecordedConfigKeepsTheBaseImage(t *testing.T) {
	cfg := flagDefaultConfig()
	rec := recordedConfig()
	rec.BaseName = "work-base"

	adoptRecordedConfig(&cfg, rec, map[string]bool{})

	if cfg.BaseName != "work-base" {
		t.Errorf("BaseName = %q, want the recorded work-base — the VM would be cloned from a different image", cfg.BaseName)
	}

	// An explicitly passed --base-name still wins.
	cfg = flagDefaultConfig()
	cfg.BaseName = "other-base"
	adoptRecordedConfig(&cfg, rec, map[string]bool{"base-name": true})
	if cfg.BaseName != "other-base" {
		t.Errorf("BaseName = %q, want the explicitly passed other-base", cfg.BaseName)
	}
}
