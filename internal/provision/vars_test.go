package provision

import (
	"os"
	"testing"

	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
	"gopkg.in/yaml.v3"
)

// TestMain isolates every test in this package from the real host secrets
// store: BuildExtraVars now calls secrets.Load(cfg.Name) for every non-base
// phase, and secrets.Load resolves its path under XDG_DATA_HOME. Point that
// at a throwaway temp dir for the whole test binary run so these tests never
// read (or, via a test that writes one, pollute) a developer's real secrets
// file.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sand-provision-test-secrets-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_DATA_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

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
	for _, k := range []string{"user_git_user_name", "user_git_user_email", "project_clone_url"} {
		if _, ok := m[k]; ok {
			t.Errorf("base phase unexpectedly emitted %q", k)
		}
	}

	// The base image must also stay secret-free: no secrets_* keys at all
	// (not even empty ones) for the base phase, mirroring the git-identity
	// gating above.
	for _, k := range []string{"secrets_global", "secrets_github", "secrets_dir_env"} {
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

	// project_clone_url still appears; the old project_clone_token var is gone
	// entirely — the clone token is no longer passed to the project role as an
	// Ansible var (see RecordCloneTokenSecret: it is recorded as a secrets_github
	// entry in the host store instead, and picked up below).
	if m["project_clone_url"] != "https://github.com/example/repo.git" {
		t.Errorf("project_clone_url = %v", m["project_clone_url"])
	}
	if _, ok := m["project_clone_token"]; ok {
		t.Errorf("finalize phase must not emit project_clone_token (retired in favor of the secrets store): %v", m["project_clone_token"])
	}

	// With no secrets recorded in the host store for this VM (fresh, isolated
	// XDG_DATA_HOME — see TestMain), the three secrets_* vars are still present
	// (roles/secrets always gets a well-formed value) but empty.
	for _, k := range []string{"secrets_global", "secrets_github", "secrets_dir_env"} {
		v, ok := m[k]
		if !ok {
			t.Fatalf("finalize phase missing %q", k)
		}
		if list, ok := v.([]any); !ok || len(list) != 0 {
			t.Errorf("%s = %#v, want an empty list", k, v)
		}
	}

	// Docker proxy vars are gated on DockerProxyHost.
	if v, ok := m["devtools_docker_registry_proxy_enabled"].(bool); !ok || !v {
		t.Errorf("devtools_docker_registry_proxy_enabled = %v, want true", m["devtools_docker_registry_proxy_enabled"])
	}
	if m["devtools_docker_registry_proxy_host"] != "proxy.lan:5000" {
		t.Errorf("devtools_docker_registry_proxy_host = %v", m["devtools_docker_registry_proxy_host"])
	}
}

// TestSecretVars_MapsStoreToAnsibleVarShapes is the focused unit test on the
// store -> secrets_* mapping function itself (task 5's `sync` reuses
// SecretVars directly), independent of the surrounding YAML encoding.
func TestSecretVars_MapsStoreToAnsibleVarShapes(t *testing.T) {
	s := &secrets.Store{}
	s.SetSecret(secrets.CategoryGlobal, "", "MY_VAR", "global-value")
	s.SetSecret(secrets.CategoryGitHub, "", "", "default-token")
	s.SetSecret(secrets.CategoryGitHub, "github.com/acme", "", "acme-token")
	s.SetSecret(secrets.CategoryDirEnv, "github.com/acme", "SOME_VAR", "dir-env-value")

	got := SecretVars(s)

	global, ok := got["secrets_global"].([]globalVar)
	if !ok || len(global) != 1 || global[0] != (globalVar{Name: "MY_VAR", Value: "global-value"}) {
		t.Fatalf("secrets_global = %#v, want a single {MY_VAR global-value}", got["secrets_global"])
	}

	github, ok := got["secrets_github"].([]githubVar)
	if !ok || len(github) != 2 {
		t.Fatalf("secrets_github = %#v, want 2 entries", got["secrets_github"])
	}
	if github[0] != (githubVar{Scope: "", Token: "default-token"}) {
		t.Errorf("secrets_github[0] = %+v, want default-scope entry", github[0])
	}
	if github[1] != (githubVar{Scope: "github.com/acme", Token: "acme-token"}) {
		t.Errorf("secrets_github[1] = %+v, want acme-scoped entry", github[1])
	}

	dirEnv, ok := got["secrets_dir_env"].([]dirEnvVar)
	if !ok || len(dirEnv) != 1 || dirEnv[0] != (dirEnvVar{Scope: "github.com/acme", Name: "SOME_VAR", Value: "dir-env-value"}) {
		t.Fatalf("secrets_dir_env = %#v, want a single acme SOME_VAR entry", got["secrets_dir_env"])
	}
}

// TestSecretVars_EmptyOrNilStoreYieldsEmptyNonNilLists guards the "always
// well-formed" contract: roles/secrets should always see three lists (never
// null), whether the store is freshly zero-valued or nil.
func TestSecretVars_EmptyOrNilStoreYieldsEmptyNonNilLists(t *testing.T) {
	for name, s := range map[string]*secrets.Store{"zero-value": {}, "nil": nil} {
		t.Run(name, func(t *testing.T) {
			got := SecretVars(s)
			for _, k := range []string{"secrets_global", "secrets_github", "secrets_dir_env"} {
				v, ok := got[k]
				if !ok {
					t.Fatalf("%s missing from SecretVars output", k)
				}
				switch list := v.(type) {
				case []globalVar:
					if list == nil || len(list) != 0 {
						t.Errorf("%s = %#v, want empty non-nil slice", k, v)
					}
				case []githubVar:
					if list == nil || len(list) != 0 {
						t.Errorf("%s = %#v, want empty non-nil slice", k, v)
					}
				case []dirEnvVar:
					if list == nil || len(list) != 0 {
						t.Errorf("%s = %#v, want empty non-nil slice", k, v)
					}
				default:
					t.Fatalf("%s has unexpected type %T", k, v)
				}
			}
		})
	}
}

// TestBuildExtraVars_MapsHostStoreSecretsIntoVars is the AC2 integration
// test: with secrets pre-recorded on disk for cfg.Name, BuildExtraVars must
// load them fresh and map them onto secrets_global/secrets_github/
// secrets_dir_env exactly, and a value containing YAML-special characters
// (a double quote and a backslash) must round-trip exactly — proving
// yaml.v3 quotes secret values correctly, the same guarantee the old
// project_clone_token test used to cover for the clone token.
func TestBuildExtraVars_MapsHostStoreSecretsIntoVars(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := fullConfig()
	cfg.Name = "secrets-mapping-vm"

	store, err := secrets.Load(cfg.Name)
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	const trickyValue = `tok"en\with-specials`
	store.SetSecret(secrets.CategoryGlobal, "", "MY_VAR", "global-value")
	store.SetSecret(secrets.CategoryGitHub, "", "", trickyValue)
	store.SetSecret(secrets.CategoryGitHub, "github.com/acme", "", "acme-token")
	store.SetSecret(secrets.CategoryDirEnv, "github.com/acme", "SOME_VAR", "dir-env-value")
	if err := store.Save(cfg.Name); err != nil {
		t.Fatalf("secrets.Save: %v", err)
	}

	data, err := BuildExtraVars(cfg, "finalize", "myhost")
	if err != nil {
		t.Fatalf("BuildExtraVars: %v", err)
	}
	m := parseVars(t, data)

	global, ok := m["secrets_global"].([]any)
	if !ok || len(global) != 1 {
		t.Fatalf("secrets_global = %#v, want 1 entry", m["secrets_global"])
	}
	g0 := global[0].(map[string]any)
	if g0["name"] != "MY_VAR" || g0["value"] != "global-value" {
		t.Errorf("secrets_global[0] = %+v", g0)
	}

	github, ok := m["secrets_github"].([]any)
	if !ok || len(github) != 2 {
		t.Fatalf("secrets_github = %#v, want 2 entries", m["secrets_github"])
	}
	gh0 := github[0].(map[string]any)
	if gh0["scope"] != "" || gh0["token"] != trickyValue {
		t.Errorf("secrets_github[0] = %+v, want default-scope token to round-trip %q exactly", gh0, trickyValue)
	}
	gh1 := github[1].(map[string]any)
	if gh1["scope"] != "github.com/acme" || gh1["token"] != "acme-token" {
		t.Errorf("secrets_github[1] = %+v", gh1)
	}

	dirEnv, ok := m["secrets_dir_env"].([]any)
	if !ok || len(dirEnv) != 1 {
		t.Fatalf("secrets_dir_env = %#v, want 1 entry", m["secrets_dir_env"])
	}
	d0 := dirEnv[0].(map[string]any)
	if d0["scope"] != "github.com/acme" || d0["name"] != "SOME_VAR" || d0["value"] != "dir-env-value" {
		t.Errorf("secrets_dir_env[0] = %+v", d0)
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
