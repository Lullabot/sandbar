//go:build limae2e

// This test boots a REAL Lima VM and so is gated behind the `limae2e` build
// tag (and the LIMA_E2E env var), mirroring lima_e2e_test.go — it never runs
// in the normal `go test ./...`. It validates the host secrets manager's own
// business logic end to end against a real VM: values that ONLY the real
// Lima/Ansible/git stack can prove, which the in-process unit tests (task 1's
// store round-trips, vars_test.go's SecretVars/BuildExtraVars mapping,
// provision_test.go's fake-runner argv assertions, clonetoken_test.go's
// RecordCloneTokenSecret routing, secret_test.go's CLI routing) can only
// assert as argv/YAML strings, never as an actually-resolved git credential
// or an actually-sourced shell variable.
//
// Test philosophy — "write a few tests, mostly integration": this file is
// ONE cohesive gated test (TestE2ESecrets, with scenario subtests) covering
// the four load-bearing scenarios from the task spec. It does NOT re-test
// git's includeIf mechanism, direnv, or `gh` internals in the abstract, and
// it does NOT re-test per-CRUD store operations (internal/secrets' own unit
// tests already cover Load/Save/SetSecret/RemoveSecret round-trips) or the
// `sand secret` CLI's flag/category routing (cmd/sand/secret_test.go already
// covers that in-process). What it DOES test — because nothing else can — is
// the full pipeline behavior: a secret set on the host survives a VM
// destroy/recreate with no re-entry; two GitHub tokens coexist in one VM,
// selected purely by checkout directory; a token rotation takes effect in an
// ALREADY-OPEN guest shell with no new shell/login; and the `--clone-token`
// legacy path records + renders a credential exactly like `sand secret set
// --github` would.
//
// Run (needs limactl + KVM/nested virt; downloads the Debian 13 image and
// builds the base image once — the base build alone can take several
// minutes, and this test also drives a Recreate, so budget generously):
//
//	go test -tags limae2e -timeout 60m -run TestE2ESecrets ./internal/provision/
//
// (set LIMA_E2E=1 in the environment). Everything above runs with NO real
// GitHub repo or token: the only network dependency is what VM provisioning
// itself already requires (apt/Debian image fetch), never a `git clone`.
//
// One additional scenario — actually cloning a real PRIVATE repo with a real
// token via `--clone-url`/`--clone-token` — is optional and skips cleanly
// unless BOTH of these are set:
//
//	SAND_E2E_PRIVATE_REPO_URL   e.g. https://github.com/some-org/private-repo.git
//	SAND_E2E_PRIVATE_REPO_TOKEN a token with read access to that repo
//
// Without them, TestE2ESecrets/LegacyCreateParity_RealPrivateClone reports
// SKIP and every other subtest still runs and asserts fully.
package provision

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
)

// secretsE2EName and secretsE2EBase are dedicated instance names, distinct
// from lima_e2e_test.go's claude-e2e-* and from a developer's normal `sand
// create` defaults (claude/claude-base), so this test never collides with
// either.
const (
	secretsE2EName = "claude-secrets-e2e"
	secretsE2EBase = "claude-secrets-e2e-base"
)

// githubSlugRe mirrors roles/secrets/tasks/main.yml's scope->filename slug:
// `item.scope | regex_replace('[^A-Za-z0-9]+', '-')`. Reproduced here (not
// imported — it lives in a Jinja template, not Go) purely so assertions can
// compute the expected on-disk credential filename for a given scope.
var githubSlugRe = regexp.MustCompile(`[^A-Za-z0-9]+`)

func githubSlug(scope string) string {
	if scope == "" {
		return "default"
	}
	return githubSlugRe.ReplaceAllString(scope, "-")
}

// credentialFillCmd is the guest shell command that resolves a directory's
// EFFECTIVE git credential via the real `git credential fill` machinery
// (not a config-only inspection), extracting just the resolved password
// (the token). It needs no network access — the `store` helper is a local
// file read — so it is safe to run from every scenario, including the
// default (no real repo/token) run.
func credentialFillCmd(dir string) string {
	return `printf 'protocol=https\nhost=github.com\n\n' | git -C ` + dir + ` credential fill 2>/dev/null | sed -n 's/^password=//p'`
}

// credentialFillPassword runs credentialFillCmd through a one-shot guest
// shell (guestOut, defined in lima_e2e_test.go) and returns the resolved
// password/token.
func credentialFillPassword(t *testing.T, cli *lima.Client, name, dir string) string {
	t.Helper()
	return guestOut(t, cli, name, "sh", "-c", credentialFillCmd(dir))
}

// persistentShell drives ONE long-lived `limactl shell <name> -- bash -s`
// guest session over stdin/stdout pipes. Every other helper in this package
// (guestOut, cli.Shell) opens a BRAND NEW limactl session per call — useful
// for one-shot assertions, but useless for proving the "live rotation"
// scenario's one load-bearing property: an ALREADY-OPEN guest shell picks up
// a rotated GitHub token on its very next git invocation, with no new shell
// or login. persistentShell keeps a single guest bash process alive across
// multiple commands so that property can actually be exercised.
type persistentShell struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

// newPersistentShell starts the session and registers cleanup (closing
// stdin lets the guest bash exit; Wait reaps the local `limactl shell`
// process) on t.
func newPersistentShell(t *testing.T, name string) *persistentShell {
	t.Helper()
	cmd := exec.Command("limactl", "shell", name, "--", "bash", "-s")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("persistent shell stdin pipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("persistent shell stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start persistent shell: %v", err)
	}
	ps := &persistentShell{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdoutPipe)}
	t.Cleanup(func() {
		_ = ps.stdin.Close()
		_ = ps.cmd.Wait()
		if stderr.Len() > 0 {
			t.Logf("persistent shell stderr: %s", stderr.String())
		}
	})
	return ps
}

// run sends command as a line of shell source to the ALREADY-OPEN session
// (no new process, no new login) and returns its trimmed output. It
// synchronizes on a unique marker line printed right after command so it
// reads exactly that command's output — never bleeding into the next call,
// never hanging forever on a well-behaved command.
func (ps *persistentShell) run(t *testing.T, command string) string {
	t.Helper()
	marker := fmt.Sprintf("__SAND_E2E_DONE_%d__", time.Now().UnixNano())
	if _, err := fmt.Fprintf(ps.stdin, "%s\necho %s\n", command, marker); err != nil {
		t.Fatalf("write to persistent shell: %v", err)
	}
	var out strings.Builder
	for {
		line, err := ps.stdout.ReadString('\n')
		if line != "" {
			if strings.TrimRight(line, "\r\n") == marker {
				return strings.TrimSpace(out.String())
			}
			out.WriteString(line)
		}
		if err != nil {
			t.Fatalf("persistent shell closed before marker %s: %v (output so far: %q)", marker, err, out.String())
		}
	}
}

// TestE2ESecrets is the single gated e2e covering all four task-spec
// scenarios against one shared VM (plus one extra, fully optional VM for the
// real-private-repo half of scenario 4). See the file doc comment for the
// run command, gating, and test-philosophy rationale.
func TestE2ESecrets(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima secrets e2e test")
	}

	// Isolate the host secrets store from the real user's data dir, mirroring
	// every other secrets-touching test in this repo (clonetoken_test.go,
	// cmd/sand/secret_test.go's withXDGDataHome).
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cli := lima.New(lima.NewExecRunner())
	_ = cli.Delete(secretsE2EName, true)
	_ = cli.Delete(secretsE2EBase, true)
	t.Cleanup(func() {
		_ = cli.Delete(secretsE2EName, true)
		_ = cli.Delete(secretsE2EBase, true)
	})

	dir, err := LocatePlaybook()
	if err != nil {
		t.Fatalf("LocatePlaybook: %v", err)
	}
	prov := &Provisioner{Lima: cli, PlaybookDir: dir}

	cfg := vm.DefaultCreateConfig()
	cfg.Name = secretsE2EName
	cfg.BaseName = secretsE2EBase
	cfg.User = vm.HostUser()
	cfg.GitName = "Sand E2E"
	cfg.GitEmail = "sand-e2e@example.com"
	cfg.CPUs = 2
	cfg.Memory = "2GiB"
	cfg.Disk = vm.BaseDiskFloor // no growpart work needed; this test isn't validating disk sizing

	const (
		globalVarName  = "SAND_E2E_GLOBAL"
		globalVarValue = "global-secret-value"

		orgAScope    = "github.com/e2eorga"
		orgATokenOld = "ghp_orgA_old_token"
		orgATokenNew = "ghp_orgA_new_token_after_rotation"

		orgBScope = "github.com/e2eorgb"
		orgBToken = "ghp_orgB_token"

		cloneOrgScope = "github.com/e2eclonetoken"
		cloneToken    = "ghp_clonetoken_fake"
	)

	// --- Seed the host store BEFORE the VM ever boots. This is the crux of
	// AC1 ("no re-entry of values"): every secret the VM will ever need is
	// already on the host, exactly as if set via `sand secret set`.
	store, err := secrets.Load(cfg.Name)
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	store.SetSecret(secrets.CategoryGlobal, "", globalVarName, globalVarValue)
	store.SetSecret(secrets.CategoryGitHub, orgAScope, "", orgATokenOld)
	store.SetSecret(secrets.CategoryGitHub, orgBScope, "", orgBToken)
	if err := store.Save(cfg.Name); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	// --- Legacy --clone-token reshape (AC4, unconditional half): mirrors
	// exactly what doHeadlessCreate (cmd/sand/create.go) does BEFORE calling
	// CreateVM — record {scope, token} derived from a clone URL as a
	// CategoryGitHub secret. cfg itself is left with CloneURL empty so the
	// project role's clone step is a no-op below (this half proves the
	// record+render pipeline without any network dependency; the optional
	// gated subtest at the end proves an actual clone with a real repo).
	cloneCfg := cfg
	cloneCfg.CloneURL = "https://github.com/e2eclonetoken/repo.git"
	cloneCfg.CloneToken = cloneToken
	if err := RecordCloneTokenSecret(cloneCfg); err != nil {
		t.Fatalf("RecordCloneTokenSecret: %v", err)
	}

	ctx := context.Background()

	var createOut bytes.Buffer
	if err := prov.CreateVM(ctx, cfg, &createOut); err != nil {
		t.Fatalf("CreateVM: %v\n%s", err, createOut.String())
	}

	home, err := guestHome(ctx, cli, cfg.Name, cfg.User)
	if err != nil {
		t.Fatalf("guestHome: %v", err)
	}

	// ---- AC4 (legacy create parity, unconditional half) ----------------
	t.Run("LegacyCreateParity_RecordsAndRenders", func(t *testing.T) {
		slug := githubSlug(cloneOrgScope)
		credFile := home + "/.config/sandbar/git-credentials/" + slug
		got := guestOut(t, cli, cfg.Name, "cat", credFile)
		want := "https://x-access-token:" + cloneToken + "@github.com"
		if got != want {
			t.Errorf("clone-token credential file %s = %q, want %q", credFile, got, want)
		}
	})

	// ---- AC1: recreation persistence ------------------------------------
	// Recreate (delete + re-clone-from-base + finalize) mirrors `sand create
	// --recreate`. No secret is re-entered here — BuildExtraVars loads the
	// host store fresh on every non-base phase, so the recreated VM's
	// finalize pass re-renders exactly what was already on the host.
	t.Run("RecreationPersistence", func(t *testing.T) {
		var recreateOut bytes.Buffer
		if err := prov.Recreate(ctx, cfg, &recreateOut); err != nil {
			t.Fatalf("Recreate: %v\n%s", err, recreateOut.String())
		}

		// A FRESH limactl shell (new session) running a LOGIN shell (`bash
		// -lc`), so the global var must come from ~/.config/sandbar/secrets.env
		// via .profile -> .bashrc, not from any interactive state.
		gotVar := guestOut(t, cli, cfg.Name, "bash", "-lc", `printf '%s' "$`+globalVarName+`"`)
		if gotVar != globalVarValue {
			t.Errorf("global var after recreate = %q, want %q", gotVar, globalVarValue)
		}

		slug := githubSlug(orgAScope)
		credFile := home + "/.config/sandbar/git-credentials/" + slug
		gotCred := guestOut(t, cli, cfg.Name, "cat", credFile)
		wantCred := "https://x-access-token:" + orgATokenOld + "@github.com"
		if gotCred != wantCred {
			t.Errorf("orgA credential file after recreate = %q, want %q", gotCred, wantCred)
		}
	})

	// ---- AC2: multi-token in one VM, selected by directory --------------
	t.Run("MultiTokenScopedByDirectory", func(t *testing.T) {
		for _, scope := range []string{orgAScope, orgBScope} {
			checkout := home + "/" + scope + "/repo"
			guestOut(t, cli, cfg.Name, "sh", "-c", "mkdir -p "+checkout+" && git -C "+checkout+" init -q")
		}

		cases := []struct{ scope, want string }{
			{orgAScope, orgATokenOld},
			{orgBScope, orgBToken},
		}
		for _, c := range cases {
			checkout := home + "/" + c.scope + "/repo"
			slug := githubSlug(c.scope)
			wantHelper := "store --file=" + home + "/.config/sandbar/git-credentials/" + slug

			gotHelper := guestOut(t, cli, cfg.Name, "git", "-C", checkout, "config", "--get", "credential.helper")
			if gotHelper != wantHelper {
				t.Errorf("%s: credential.helper = %q, want %q", c.scope, gotHelper, wantHelper)
			}

			gotPass := credentialFillPassword(t, cli, cfg.Name, checkout)
			if gotPass != c.want {
				t.Errorf("%s: git credential fill password = %q, want %q", c.scope, gotPass, c.want)
			}
		}
	})

	// ---- AC3: live rotation in one already-open shell, no new shell -----
	t.Run("LiveRotationSameShell_NoNewShell", func(t *testing.T) {
		checkout := home + "/" + orgAScope + "/repo"
		ps := newPersistentShell(t, cfg.Name)

		oldPass := ps.run(t, credentialFillCmd(checkout))
		if oldPass != orgATokenOld {
			t.Fatalf("pre-rotation password (same open shell) = %q, want %q", oldPass, orgATokenOld)
		}

		// Rotate host-side (mirrors `sand secret set --github` + `sand secret
		// sync`) via a SEPARATE control-plane connection. The already-open
		// guest shell above is never touched, closed, or reopened.
		rotated, err := secrets.Load(cfg.Name)
		if err != nil {
			t.Fatalf("secrets.Load (rotate): %v", err)
		}
		rotated.SetSecret(secrets.CategoryGitHub, orgAScope, "", orgATokenNew)
		if err := rotated.Save(cfg.Name); err != nil {
			t.Fatalf("store.Save (rotate): %v", err)
		}
		var syncOut bytes.Buffer
		if err := prov.RenderSecrets(ctx, cfg.Name, cfg, &syncOut); err != nil {
			t.Fatalf("RenderSecrets (rotate): %v\n%s", err, syncOut.String())
		}

		newPass := ps.run(t, credentialFillCmd(checkout))
		if newPass != orgATokenNew {
			t.Errorf("post-rotation password (same open shell, no new shell) = %q, want %q", newPass, orgATokenNew)
		}
		if newPass == oldPass {
			t.Errorf("post-rotation password did not change: still %q", oldPass)
		}
	})

	// ---- AC4 (legacy create parity, optional real-clone half) -----------
	t.Run("LegacyCreateParity_RealPrivateClone", func(t *testing.T) {
		repoURL := os.Getenv("SAND_E2E_PRIVATE_REPO_URL")
		token := os.Getenv("SAND_E2E_PRIVATE_REPO_TOKEN")
		if repoURL == "" || token == "" {
			t.Skip("set SAND_E2E_PRIVATE_REPO_URL and SAND_E2E_PRIVATE_REPO_TOKEN to exercise an actual `sand create --clone-url --clone-token` private clone")
		}

		realCfg := cfg
		realCfg.Name = secretsE2EName + "-privclone"
		realCfg.CloneURL = repoURL
		realCfg.CloneToken = token

		_ = cli.Delete(realCfg.Name, true)
		t.Cleanup(func() { _ = cli.Delete(realCfg.Name, true) })

		if err := RecordCloneTokenSecret(realCfg); err != nil {
			t.Fatalf("RecordCloneTokenSecret (real repo): %v", err)
		}

		var privOut bytes.Buffer
		if err := prov.CreateVM(ctx, realCfg, &privOut); err != nil {
			t.Fatalf("CreateVM (real private clone): %v\n%s", err, privOut.String())
		}

		orgRel, ok := CheckoutRelDir(realCfg.CloneURL)
		if !ok {
			t.Fatalf("CheckoutRelDir(%q): could not derive a checkout dir", realCfg.CloneURL)
		}
		realHome, err := guestHome(ctx, cli, realCfg.Name, realCfg.User)
		if err != nil {
			t.Fatalf("guestHome (real private clone): %v", err)
		}
		got := guestOut(t, cli, realCfg.Name, "sh", "-c", "test -d "+realHome+"/"+orgRel+"/.git && echo OK")
		if got != "OK" {
			t.Errorf("private repo clone into %s: expected a .git dir, got %q", orgRel, got)
		}
	})
}
