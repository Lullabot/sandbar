package provision

import (
	"fmt"

	"github.com/lullabot/sandbar/internal/vm"
	"gopkg.in/yaml.v3"
)

// varItem is one ordered key/value pair in the generated all.yml.
type varItem struct {
	key   string
	value any
}

// BuildExtraVars renders the Ansible extra-vars (all.yml) for one provisioning
// phase, mirroring the original bash provisioner's build_allyml. The phase
// (base/finalize/full) drives which tasks site.yml runs.
//
// The base image is identity-free, so the git identity and the project-clone
// vars (which may include a token) are emitted only for non-base phases — they
// are neither needed nor wanted baked into the long-lived base disk. Scalars are
// marshaled with gopkg.in/yaml.v3, which replaces the script's hand-rolled
// yaml_str quoting.
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
			if cfg.CloneToken != "" {
				items = append(items, varItem{"project_clone_token", cfg.CloneToken})
			}
		}
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
