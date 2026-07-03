package provision

import (
	"testing"

	"github.com/lullabot/sandbar/tui/internal/vm"
	"gopkg.in/yaml.v3"
)

// fullConfig is a CreateConfig with every identity/optional field populated, so
// the phase-gating assertions prove keys are omitted by phase, not merely
// because the source field was empty.
func fullConfig() vm.CreateConfig {
	return vm.CreateConfig{
		Name:       "claude",
		BaseName:   "claude-base",
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
	data, err := BuildExtraVars(cfg, "base", "claude-base")
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)

	// Always-present keys.
	if m["user_name"] != "andrew" {
		t.Errorf("user_name = %v, want andrew", m["user_name"])
	}
	if m["base_hostname"] != "claude-base" {
		t.Errorf("base_hostname = %v, want claude-base", m["base_hostname"])
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
}

func TestBuildExtraVars_FinalizePhase(t *testing.T) {
	cfg := fullConfig()
	cfg.DockerProxyHost = "proxy.lan:5000"
	data, err := BuildExtraVars(cfg, "finalize", "myhost")
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
	data, err := BuildExtraVars(cfg, "finalize", "myhost")
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
