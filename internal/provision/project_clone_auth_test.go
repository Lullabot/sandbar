package provision

import (
	"encoding/base64"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

// Run the shipped role against a real HTTPS Git server, with only guest-home,
// privilege, direnv and review-tool setup replaced by isolated host fixtures.
func TestProjectRoleGitLabAuthenticatedClone(t *testing.T) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook unavailable")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitRoot := t.TempDir()
	bare := filepath.Join(gitRoot, "group", "repo.git")
	run("", "git", "init", "--bare", bare)
	run("", "git", "--git-dir", bare, "config", "http.receivepack", "true")
	src := t.TempDir()
	run("", "git", "init", src)
	run(src, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Initial")
	run(src, "git", "push", bare, "HEAD:refs/heads/master")
	backend := filepath.Join(run("", "git", "--exec-path"), "git-http-backend")
	handler := &cgi.Handler{Path: backend, Root: "/", Env: []string{"GIT_PROJECT_ROOT=" + gitRoot, "GIT_HTTP_EXPORT_ALL=1"}}
	var authCount atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("oauth2:lab:@/?secret")) {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitLab"`)
			w.WriteHeader(401)
			return
		}
		authCount.Add(1)
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	contents, err := os.ReadFile("../../roles/project/tasks/main.yml")
	if err != nil {
		t.Fatal(err)
	}
	var role []map[string]any
	if err := yaml.Unmarshal(contents, &role); err != nil {
		t.Fatal(err)
	}
	block := role[0]["block"].([]any)
	tasks := []any{}
	for i, raw := range block {
		task := raw.(map[string]any)
		name := task["name"].(string)
		if i < 2 || name == "Approve the .env with direnv" || name == "Install the self-review skills into the project" {
			continue
		}
		task["become"] = false
		if nested, ok := task["block"].([]any); ok {
			for _, r := range nested {
				r.(map[string]any)["become"] = false
			}
		}
		tasks = append(tasks, task)
	}
	who, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	group, err := user.LookupGroupId(who.Gid)
	if err != nil {
		t.Fatal(err)
	}
	// The role assumes the user's primary group shares its name. Override the
	// fixture owner/group for systems where that isn't true.
	for _, raw := range tasks {
		task := raw.(map[string]any)
		for _, key := range []string{"ansible.builtin.copy", "ansible.builtin.file", "ansible.builtin.blockinfile"} {
			if v, ok := task[key].(map[string]any); ok {
				v["group"] = group.Name
			}
		}
	}
	play := []map[string]any{{"hosts": "localhost", "connection": "local", "gather_facts": false, "vars": map[string]any{"user_name": who.Username, "user_home": home, "project_clone_url": server.URL + "/group/repo.git", "project_clone_token": "lab:@/?secret", "project_clone_forge": "gitlab"}, "tasks": tasks}}
	payload, err := yaml.Marshal(play)
	if err != nil {
		t.Fatal(err)
	}
	playPath := filepath.Join(t.TempDir(), "play.yml")
	if err := os.WriteFile(playPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	run("", "ansible-playbook", "-i", "localhost,", playPath)
	host := strings.TrimPrefix(server.URL, "https://")
	checkout := filepath.Join(home, host, "group", "repo")
	if got := run(checkout, "git", "remote", "get-url", "origin"); got != server.URL+"/group/repo.git" {
		t.Fatalf("remote includes auth or differs: %q", got)
	}
	if authCount.Load() == 0 {
		t.Fatal("clone never authenticated to HTTPS Git server")
	}
	// New shell with no token env still fetches through the persistent helper.
	run(checkout, "git", "fetch", "origin")
	run(checkout, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Authenticated push")
	run(checkout, "git", "push", "origin", "HEAD:refs/heads/master")
	if got, want := run("", "git", "--git-dir", bare, "rev-parse", "master"), run(checkout, "git", "rev-parse", "HEAD"); got != want {
		t.Fatalf("push did not reach server: got %s want %s", got, want)
	}
	if out, err := os.ReadFile(filepath.Join(home, ".gitconfig")); err != nil || strings.Contains(string(out), "lab:@/?secret") {
		t.Fatalf("token leaked to gitconfig: %v", err)
	}
}
