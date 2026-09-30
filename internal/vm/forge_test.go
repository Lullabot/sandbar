package vm

import (
	"strings"
	"testing"
)

func TestCloneForgeValidation(t *testing.T) {
	for _, tc := range []struct{ url, selection, forge, key string }{
		{"https://github.com/acme/repo", "", "github", "GH_TOKEN"},
		{"https://gitlab.com/acme/team/repo", "auto", "gitlab", "GITLAB_TOKEN"},
		{"https://gitlab.example.com/acme/repo", "gitlab", "gitlab", "GITLAB_TOKEN"},
		{"https://example.com/acme/repo", "", "", ""},
	} {
		got, err := ResolveCloneForge(tc.url, tc.selection)
		if err != nil || got != tc.forge || CloneTokenKey(tc.url, tc.selection) != tc.key {
			t.Fatalf("%+v: forge=%q err=%v", tc, got, err)
		}
	}
	for _, tc := range []struct{ url, selection string }{{"https://github.com/acme/repo", "gitlab"}, {"https://gitlab.com/acme/repo", "github"}, {"https://git.example.com/acme/repo", "github"}, {"https://github.com/acme/repo", "other"}, {"https://%zz/repo", "auto"}} {
		if _, err := ResolveCloneForge(tc.url, tc.selection); err == nil {
			t.Errorf("accepted mismatched or invalid forge input: %+v", tc)
		}
		if key := CloneTokenKey(tc.url, tc.selection); key != "" {
			t.Errorf("invalid forge produced token key %q", key)
		}
	}
	cfg := DefaultCreateConfig()
	cfg.Name = "test"
	cfg.GitName = "Test"
	cfg.GitEmail = "test@example.com"
	cfg.CloneToken = "secret"
	for _, u := range []string{"https://example.com/org/repo", "http://gitlab.com/org/repo", "https://user:pass@gitlab.com/org/repo", "https://gitlab.com/org/../repo", "https://gitlab.com/org/repo?x=1", "https://[::1]/org/repo"} {
		cfg.CloneURL = u
		if err := cfg.Validate(); err == nil {
			t.Errorf("accepted token URL %q", u)
		} else if strings.Contains(err.Error(), cfg.CloneToken) {
			t.Fatal("leaked token")
		}
	}
	cfg.CloneURL = "https://git.example.com/org/repo"
	cfg.CloneForge = "gitlab"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
