package provision

import (
	"fmt"
	"strings"

	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
)

// githubHostPrefix is the leading segment cloneOrgRelDir emits for a
// github.com clone URL (see cloneOrgRelDir in staging.go): host + "/" + org.
// Since cloneOrgRelDir splits host on the first "/", this prefix can only
// match a URL whose host is exactly "github.com".
const githubHostPrefix = "github.com/"

// RecordCloneTokenSecret reshapes the `--clone-token` flow: instead of
// passing a token to the project role as an Ansible var (the old
// project_clone_token/GH_TOKEN path), it derives the GitHub org scope from
// cfg.CloneURL and records {scope, token} as a CategoryGitHub secret in
// cfg.Name's host secrets store, BEFORE provisioning runs. BuildExtraVars
// then picks it up via secrets.Load + SecretVars on the next phase that
// isn't "base", and roles/secrets renders the corresponding file-backed git
// credential that the project role's clone authenticates through.
//
// It is a no-op when CloneURL or CloneToken is empty, or when CloneURL is not
// a github.com URL with an org component — mirroring the old code's
// GitHub-only gating (other hosts clone without a token).
func RecordCloneTokenSecret(cfg vm.CreateConfig) error {
	if cfg.CloneURL == "" || cfg.CloneToken == "" {
		return nil
	}
	orgRel, ok := cloneOrgRelDir(cfg.CloneURL)
	if !ok || !strings.HasPrefix(orgRel, githubHostPrefix) {
		return nil
	}

	store, err := secrets.Load(cfg.Name)
	if err != nil {
		return fmt.Errorf("load secrets store for %q: %w", cfg.Name, err)
	}
	store.SetSecret(secrets.CategoryGitHub, orgRel, "", cfg.CloneToken)
	if err := store.Save(cfg.Name); err != nil {
		return fmt.Errorf("save secrets store for %q: %w", cfg.Name, err)
	}
	return nil
}
