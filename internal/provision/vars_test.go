package provision

import (
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
	"gopkg.in/yaml.v3"
)

// fullConfig is a CreateConfig with every identity/optional field populated, so
// the phase-gating assertions prove keys are omitted by phase, not merely
// because the source field was empty.
func fullConfig() vm.CreateConfig {
	return vm.CreateConfig{
		Name:       "claude",
		BaseName:   "sandbar-base",
		User:       "andrew",
		GitName:    "Andrew Berry",
		GitEmail:   "andrew@example.com",
		Memory:     "8GiB",
		Disk:       "100GiB",
		Domain:     "lan",
		Locale:     "en_US.UTF-8",
		CPUs:       4,
		CloneURL:   "https://github.com/example/repo.git",
		CloneToken: `tok"en\with-specials`,
		WithClaude: true,
		WithDDEV:   true,
		WithGo:     true,
		WithJava:   true,
	}
}

func parseVars(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("extra-vars is not valid YAML: %v\n%s", err, data)
	}
	return m
}

func TestBuildExtraVars_BasePhase(t *testing.T) {
	cfg := fullConfig()
	data, err := BuildExtraVars(cfg, "base", "sandbar-base", false)
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)

	// Always-present keys.
	if m["user_name"] != "andrew" {
		t.Errorf("user_name = %v, want andrew", m["user_name"])
	}
	if m["base_hostname"] != "sandbar-base" {
		t.Errorf("base_hostname = %v, want sandbar-base", m["base_hostname"])
	}
	if m["provision_phase"] != "base" {
		t.Errorf("provision_phase = %v, want base", m["provision_phase"])
	}
	// samba_enabled must be present AND false (a missing key would also read as
	// nil, so assert the type/value explicitly).
	v, ok := m["samba_enabled"]
	if !ok {
		t.Fatalf("samba_enabled missing; want false")
	}
	if b, ok := v.(bool); !ok || b {
		t.Errorf("samba_enabled = %v (%T), want bool false", v, v)
	}

	// The base image is identity-free: no git identity or project-clone keys,
	// even though the config carries them.
	for _, k := range []string{"user_git_user_name", "user_git_user_email", "project_clone_url", "project_clone_token"} {
		if _, ok := m[k]; ok {
			t.Errorf("base phase unexpectedly emitted %q", k)
		}
	}

	// The tool-set booleans are emitted for the base phase — that is where the
	// tools are installed — as real bools, unconditionally (the samba_enabled
	// precedent), not gated on being non-default like the docker-proxy vars.
	for key, want := range map[string]bool{
		"toolset_claude": true,
		"toolset_ddev":   true,
		"toolset_go":     true,
		"toolset_java":   true,
		"toolset_codex":  false,
	} {
		v, ok := m[key]
		if !ok {
			t.Fatalf("%s missing; want %v", key, want)
		}
		if b, ok := v.(bool); !ok || b != want {
			t.Errorf("%s = %v (%T), want bool %v", key, v, v, want)
		}
	}
}

// TestBuildExtraVars_ToolsetReflectsConfig proves the emitted booleans track
// cfg, not a hardcoded true — a config with one tool deselected must emit
// that one as false while the others stay true.
func TestBuildExtraVars_ToolsetReflectsConfig(t *testing.T) {
	cfg := fullConfig()
	cfg.WithJava = false
	data, err := BuildExtraVars(cfg, "base", "sandbar-base", false)
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)

	if v, ok := m["toolset_java"].(bool); !ok || v {
		t.Errorf("toolset_java = %v, want false", m["toolset_java"])
	}
	if v, ok := m["toolset_claude"].(bool); !ok || !v {
		t.Errorf("toolset_claude = %v, want true", m["toolset_claude"])
	}
	if v, ok := m["toolset_ddev"].(bool); !ok || !v {
		t.Errorf("toolset_ddev = %v, want true", m["toolset_ddev"])
	}
	if v, ok := m["toolset_go"].(bool); !ok || !v {
		t.Errorf("toolset_go = %v, want true", m["toolset_go"])
	}
}

// TestBuildExtraVars_ClaudeCanBeDeselected: toolset_claude=false is what gates
// the claude-code role off in site.yml, so if this var stopped being emitted
// the role would fall back to its default (true) and install Claude Code onto
// the base of a user who explicitly asked for their own agent instead.
func TestBuildExtraVars_ClaudeCanBeDeselected(t *testing.T) {
	cfg := fullConfig()
	cfg.WithClaude = false
	data, err := BuildExtraVars(cfg, "base", "sandbar-base", false)
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)

	if v, ok := m["toolset_claude"].(bool); !ok || v {
		t.Errorf("toolset_claude = %v, want false", m["toolset_claude"])
	}
	if v, ok := m["toolset_ddev"].(bool); !ok || !v {
		t.Errorf("toolset_ddev = %v, want true", m["toolset_ddev"])
	}
}

// TestBuildExtraVars_ToolsetOmittedOnFinalize: the tools are installed only
// in the base phase, so emitting the toolset vars for finalize too would be
// dead weight at best and, if any role read them without checking phase, a
// source of drift at worst.
func TestBuildExtraVars_ToolsetOmittedOnFinalize(t *testing.T) {
	cfg := fullConfig()
	data, err := BuildExtraVars(cfg, "finalize", "myhost", false)
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)
	for _, k := range []string{"toolset_ddev", "toolset_go", "toolset_java", "toolset_codex"} {
		if _, ok := m[k]; ok {
			t.Errorf("finalize phase unexpectedly emitted %q", k)
		}
	}
}

// TestBuildExtraVars_CodexCanBeSelected: the inverse of
// TestBuildExtraVars_ClaudeCanBeDeselected — codex defaults off, so this
// proves toolset_codex=true is emitted (and the other tools are unaffected)
// when a user opts in, gating the codex install task on in site.yml.
func TestBuildExtraVars_CodexCanBeSelected(t *testing.T) {
	cfg := fullConfig()
	cfg.WithCodex = true
	data, err := BuildExtraVars(cfg, "base", "sandbar-base", false)
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)

	if v, ok := m["toolset_codex"].(bool); !ok || !v {
		t.Errorf("toolset_codex = %v, want true", m["toolset_codex"])
	}
	if v, ok := m["toolset_claude"].(bool); !ok || !v {
		t.Errorf("toolset_claude = %v, want true", m["toolset_claude"])
	}
}

func TestBuildExtraVars_FinalizePhase(t *testing.T) {
	cfg := fullConfig()
	cfg.DockerProxyHost = "proxy.lan:5000"
	data, err := BuildExtraVars(cfg, "finalize", "myhost", false)
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)

	if m["base_hostname"] != "myhost" {
		t.Errorf("base_hostname = %v, want myhost", m["base_hostname"])
	}
	if m["provision_phase"] != "finalize" {
		t.Errorf("provision_phase = %v, want finalize", m["provision_phase"])
	}

	// Git identity appears for non-base phases.
	if m["user_git_user_name"] != "Andrew Berry" {
		t.Errorf("user_git_user_name = %v, want Andrew Berry", m["user_git_user_name"])
	}
	if m["user_git_user_email"] != "andrew@example.com" {
		t.Errorf("user_git_user_email = %v, want andrew@example.com", m["user_git_user_email"])
	}

	// Project clone vars appear, and the token (which contains a double quote and
	// a backslash) round-trips exactly — proving yaml.v3 quoted it correctly.
	if m["project_clone_url"] != "https://github.com/example/repo.git" {
		t.Errorf("project_clone_url = %v", m["project_clone_url"])
	}
	if got := m["project_clone_token"]; got != cfg.CloneToken {
		t.Errorf("project_clone_token = %q, want %q (quoting must round-trip)", got, cfg.CloneToken)
	}

	// Docker proxy vars are gated on DockerProxyHost.
	if v, ok := m["devtools_docker_registry_proxy_enabled"].(bool); !ok || !v {
		t.Errorf("devtools_docker_registry_proxy_enabled = %v, want true", m["devtools_docker_registry_proxy_enabled"])
	}
	if m["devtools_docker_registry_proxy_host"] != "proxy.lan:5000" {
		t.Errorf("devtools_docker_registry_proxy_host = %v", m["devtools_docker_registry_proxy_host"])
	}
}

func TestBuildExtraVars_NoDockerProxyByDefault(t *testing.T) {
	cfg := fullConfig() // DockerProxyHost empty
	data, err := BuildExtraVars(cfg, "finalize", "myhost", false)
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)
	for _, k := range []string{"devtools_docker_registry_proxy_enabled", "devtools_docker_registry_proxy_host"} {
		if _, ok := m[k]; ok {
			t.Errorf("unexpected %q when DockerProxyHost is empty", k)
		}
	}
}

// TestBuildExtraVars_AptUpgradeOmittedByDefault: a plain base build/re-apply
// (aptUpgrade=false) must never emit base_apt_upgrade — roles/base/tasks/main.yml
// defaults it to false, but this pins the Go side of that contract too, so a
// cold base build never gets a second apt pass (task 3's "one apt pass" property).
func TestBuildExtraVars_AptUpgradeOmittedByDefault(t *testing.T) {
	cfg := fullConfig()
	data, err := BuildExtraVars(cfg, "base", "sandbar-base", false)
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)
	if _, ok := m["base_apt_upgrade"]; ok {
		t.Errorf("base_apt_upgrade unexpectedly emitted when aptUpgrade=false: %v", m["base_apt_upgrade"])
	}
}

// TestBuildExtraVars_AptUpgradeEmittedOnRefresh: the base self-refresh run
// (aptUpgrade=true, base phase) must emit base_apt_upgrade: true — the only
// signal roles/base/tasks/main.yml's "Upgrade all apt packages" task is gated
// on.
func TestBuildExtraVars_AptUpgradeEmittedOnRefresh(t *testing.T) {
	cfg := fullConfig()
	data, err := BuildExtraVars(cfg, "base", "sandbar-base", true)
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)
	if v, ok := m["base_apt_upgrade"].(bool); !ok || !v {
		t.Errorf("base_apt_upgrade = %v, want bool true", m["base_apt_upgrade"])
	}
}

// TestBuildExtraVars_AptUpgradeIgnoredOnFinalize: aptUpgrade must have no
// effect outside the base phase — a clone's finalize pass never upgrades, even
// if a caller passed aptUpgrade=true by mistake.
func TestBuildExtraVars_AptUpgradeIgnoredOnFinalize(t *testing.T) {
	cfg := fullConfig()
	data, err := BuildExtraVars(cfg, "finalize", "myhost", true)
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)
	if _, ok := m["base_apt_upgrade"]; ok {
		t.Errorf("finalize phase unexpectedly emitted base_apt_upgrade: %v", m["base_apt_upgrade"])
	}
}
