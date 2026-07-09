package provision

import (
	"fmt"

	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
	"gopkg.in/yaml.v3"
)

// varItem is one ordered key/value pair in the generated all.yml.
type varItem struct {
	key   string
	value any
}

// globalVar, githubVar, and dirEnvVar mirror the roles/secrets Ansible var
// contract (roles/secrets/defaults/main.yml) item shapes exactly, with yaml
// tags fixing both the field names and their rendered order.
type globalVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type githubVar struct {
	Scope string `yaml:"scope"`
	Token string `yaml:"token"`
}

type dirEnvVar struct {
	Scope string `yaml:"scope"`
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

// SecretVars maps a host secrets.Store (internal/secrets, task 1's schema)
// into the secrets_global / secrets_github / secrets_dir_env Ansible var
// values defined by roles/secrets' contract (task 2). It is exported so
// cmd/sand's `sand secret sync` command (task 5) can render the SAME mapping
// against a running VM — via the same stdin/runProvision channel — without
// going through a full create/reset.
//
// A nil store maps to three empty (non-nil) slices, so the caller always gets
// well-formed, JSON/YAML-safe values rather than null.
func SecretVars(s *secrets.Store) map[string]any {
	global := make([]globalVar, 0)
	github := make([]githubVar, 0)
	dirEnv := make([]dirEnvVar, 0)

	if s != nil {
		for _, g := range s.Global {
			global = append(global, globalVar{Name: g.Name, Value: g.Value})
		}
		for _, g := range s.GitHub {
			github = append(github, githubVar{Scope: g.Scope, Token: g.Token})
		}
		for _, d := range s.DirEnv {
			dirEnv = append(dirEnv, dirEnvVar{Scope: d.Scope, Name: d.Name, Value: d.Value})
		}
	}

	return map[string]any{
		"secrets_global":  global,
		"secrets_github":  github,
		"secrets_dir_env": dirEnv,
	}
}

// BuildExtraVars renders the Ansible extra-vars (all.yml) for one provisioning
// phase, mirroring the original bash provisioner's build_allyml. The phase
// (base/finalize/full) drives which tasks site.yml runs.
//
// The base image is identity-free, so the git identity, the project-clone
// URL, and the secrets_* vars are emitted only for non-base phases — they are
// neither needed nor wanted baked into the long-lived base disk. On every
// non-base phase the host's secrets.Store for cfg.Name is loaded fresh and
// mapped via SecretVars, so create/finalize AND Reset always re-render the
// host store's current contents (the store, not the VM, is authoritative).
// Scalars are marshaled with gopkg.in/yaml.v3, which replaces the script's
// hand-rolled yaml_str quoting.
func BuildExtraVars(cfg vm.CreateConfig, phase, hostname string) ([]byte, error) {
	items := []varItem{
		{"user_name", cfg.User},
		{"base_hostname", hostname},
		{"base_domain", cfg.Domain},
		{"base_locale", cfg.Locale},
		{"provision_phase", phase},
		// Lima VMs have no host-home mount to share, so skip Samba.
		{"samba_enabled", false},
	}

	if cfg.DockerProxyHost != "" {
		items = append(items,
			varItem{"devtools_docker_registry_proxy_enabled", true},
			varItem{"devtools_docker_registry_proxy_host", cfg.DockerProxyHost},
		)
	}

	if phase != "base" {
		items = append(items,
			varItem{"user_git_user_name", cfg.GitName},
			varItem{"user_git_user_email", cfg.GitEmail},
		)
		if cfg.CloneURL != "" {
			items = append(items, varItem{"project_clone_url", cfg.CloneURL})
		}

		// The host secrets store is authoritative: load it fresh (never cached)
		// so every create/finalize/Reset re-renders its current contents,
		// including a clone token recorded via RecordCloneTokenSecret. Values
		// stay off argv end-to-end — they flow into this YAML blob, which the
		// caller streams over stdin (see runProvision).
		store, err := secrets.Load(cfg.Name)
		if err != nil {
			return nil, fmt.Errorf("load secrets store for %q: %w", cfg.Name, err)
		}
		sv := SecretVars(store)
		items = append(items,
			varItem{"secrets_global", sv["secrets_global"]},
			varItem{"secrets_github", sv["secrets_github"]},
			varItem{"secrets_dir_env", sv["secrets_dir_env"]},
		)
	}

	// Build an ordered mapping node so output is stable and yaml.v3 handles all
	// scalar quoting (notably for a token containing special characters).
	mapping := &yaml.Node{Kind: yaml.MappingNode}
	for _, it := range items {
		key := &yaml.Node{}
		if err := key.Encode(it.key); err != nil {
			return nil, fmt.Errorf("encode key %q: %w", it.key, err)
		}
		val := &yaml.Node{}
		if err := val.Encode(it.value); err != nil {
			return nil, fmt.Errorf("encode value for %q: %w", it.key, err)
		}
		mapping.Content = append(mapping.Content, key, val)
	}

	out, err := yaml.Marshal(mapping)
	if err != nil {
		return nil, fmt.Errorf("marshal extra-vars: %w", err)
	}
	return out, nil
}
