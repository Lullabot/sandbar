package vm

import (
	"fmt"
	"net/url"
	"strings"
)

// ResolveCloneForge identifies public forges automatically. Self-hosted GitLab
// needs an explicit selection because its hostname cannot identify the service.
func ResolveCloneForge(cloneURL, selection string) (string, error) {
	selection = strings.ToLower(strings.TrimSpace(selection))
	if selection != "" && selection != "auto" && selection != "github" && selection != "gitlab" {
		return "", fmt.Errorf("clone forge must be auto, github, or gitlab")
	}
	u, err := url.Parse(cloneURL)
	if err != nil {
		return "", fmt.Errorf("invalid clone URL")
	}
	host := strings.ToLower(u.Hostname())
	inferred := ""
	if host == "github.com" {
		inferred = "github"
	}
	if host == "gitlab.com" {
		inferred = "gitlab"
	}
	if selection == "" || selection == "auto" {
		return inferred, nil
	}
	if cloneURL != "" && ((selection == "github" && host != "github.com") || (inferred != "" && inferred != selection)) {
		return "", fmt.Errorf("clone forge %s does not match repository host", selection)
	}
	return selection, nil
}

// CloneTokenKey returns the conventional token name for the selected forge.
func CloneTokenKey(cloneURL, selection string) string {
	forge, err := ResolveCloneForge(cloneURL, selection)
	if err != nil {
		return ""
	}
	switch forge {
	case "github":
		return "GH_TOKEN"
	case "gitlab":
		return "GITLAB_TOKEN"
	}
	return ""
}

func (c CreateConfig) validateCloneToken() error {
	forge, err := ResolveCloneForge(c.CloneURL, c.CloneForge)
	if err != nil {
		return err
	}
	if c.CloneToken == "" {
		return nil
	}
	u, err := url.Parse(c.CloneURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(c.CloneURL, "\r\n\t\\") {
		return fmt.Errorf("clone token requires an HTTPS repository URL without embedded credentials, query, or fragment")
	}
	if strings.Contains(u.Hostname(), ":") {
		return fmt.Errorf("clone token requires a DNS hostname or IPv4 address; IPv6 literal clone URLs are not supported")
	}
	for _, component := range strings.Split(u.Path, "/") {
		if component == "." || component == ".." {
			return fmt.Errorf("clone URL must not contain relative path components")
		}
	}
	if len(strings.Split(strings.Trim(u.Path, "/"), "/")) < 2 {
		return fmt.Errorf("clone token requires a repository URL with a namespace and repository")
	}
	if forge == "" {
		return fmt.Errorf("cannot detect repository forge; for self-hosted GitLab select GitLab or pass --clone-forge gitlab")
	}
	return nil
}
