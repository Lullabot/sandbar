package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
)

// withXDGDataHome points XDG_DATA_HOME at a fresh temp dir for the duration
// of the test, mirroring internal/secrets' own test helper, so these tests
// never touch the real user data dir.
func withXDGDataHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

// TestReorderFlags_PositionalNameBeforeFlags is the load-bearing bug this
// covers: Go's flag package stops parsing at the first non-flag token, but
// the CLI's documented/required invocation shape is
// `sand secret set NAME --vm x` (positional NAME BEFORE the flags). Without
// reordering, fs.Parse would see NAME as the first non-flag token and dump
// everything after it (including --vm/--dir/--github) into fs.Args() as
// unparsed positional args instead of recognized flags.
func TestReorderFlags_PositionalNameBeforeFlags(t *testing.T) {
	valueFlags := map[string]bool{"vm": true, "dir": true}

	tests := []struct {
		name           string
		args           []string
		wantPositional []string
		wantFlagArgs   []string
	}{
		{
			name:           "name before flags",
			args:           []string{"MYVAR", "--vm", "demo"},
			wantPositional: []string{"MYVAR"},
			wantFlagArgs:   []string{"--vm", "demo"},
		},
		{
			name:           "name before flags with bool flag and dir value",
			args:           []string{"TOK", "--vm", "demo", "--dir", "github.com/acme", "--github"},
			wantPositional: []string{"TOK"},
			wantFlagArgs:   []string{"--vm", "demo", "--dir", "github.com/acme", "--github"},
		},
		{
			name:           "flags before name",
			args:           []string{"--vm", "demo", "MYVAR"},
			wantPositional: []string{"MYVAR"},
			wantFlagArgs:   []string{"--vm", "demo"},
		},
		{
			name:           "equals form self-contained",
			args:           []string{"VAR", "--dir=some/dir"},
			wantPositional: []string{"VAR"},
			wantFlagArgs:   []string{"--dir=some/dir"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPositional, gotFlagArgs := reorderFlags(tt.args, valueFlags)
			if !equalStrings(gotPositional, tt.wantPositional) {
				t.Fatalf("positional = %v, want %v", gotPositional, tt.wantPositional)
			}
			if !equalStrings(gotFlagArgs, tt.wantFlagArgs) {
				t.Fatalf("flagArgs = %v, want %v", gotFlagArgs, tt.wantFlagArgs)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSecretCategory is the load-bearing routing table from the task spec:
// --dir/--github flag combinations must map to the correct secrets.Category,
// independent of any I/O.
func TestSecretCategory(t *testing.T) {
	tests := []struct {
		name   string
		dir    string
		github bool
		want   secrets.Category
	}{
		{"no dir, no github -> global", "", false, secrets.CategoryGlobal},
		{"dir, no github -> dir_env", "some/dir", false, secrets.CategoryDirEnv},
		{"github, no dir -> github (default token)", "", true, secrets.CategoryGitHub},
		{"dir + github -> github (scoped token)", "github.com/acme", true, secrets.CategoryGitHub},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := secretCategory(tt.dir, tt.github)
			if got != tt.want {
				t.Fatalf("secretCategory(%q, %v) = %q, want %q", tt.dir, tt.github, got, tt.want)
			}
		})
	}
}

// TestReadSecretValue_NonTTYReadsStdinWithoutPrompt is the critical "value
// comes from stdin, never argv" behavior: piped (non-TTY) stdin must be read
// silently (no prompt written), with the trailing newline stripped.
func TestReadSecretValue_NonTTYReadsStdinWithoutPrompt(t *testing.T) {
	in := strings.NewReader("topsecret\n")
	var prompt bytes.Buffer

	got, err := readSecretValue(in, &prompt, false, "MY_VAR")
	if err != nil {
		t.Fatalf("readSecretValue: %v", err)
	}
	if got != "topsecret" {
		t.Fatalf("readSecretValue value = %q, want %q", got, "topsecret")
	}
	if prompt.Len() != 0 {
		t.Fatalf("readSecretValue wrote a prompt on non-TTY stdin: %q", prompt.String())
	}
}

// TestReadSecretValue_TTYWritesPromptToStderr checks the interactive path:
// when stdin is a TTY, a prompt naming the secret is written to the prompt
// writer (stderr) before reading the line.
func TestReadSecretValue_TTYWritesPromptToStderr(t *testing.T) {
	in := strings.NewReader("hunter2\n")
	var prompt bytes.Buffer

	got, err := readSecretValue(in, &prompt, true, "MY_VAR")
	if err != nil {
		t.Fatalf("readSecretValue: %v", err)
	}
	if got != "hunter2" {
		t.Fatalf("readSecretValue value = %q, want %q", got, "hunter2")
	}
	if !strings.Contains(prompt.String(), "MY_VAR") {
		t.Fatalf("readSecretValue prompt = %q, want it to mention MY_VAR", prompt.String())
	}
}

// TestReadSecretValue_NoTrailingNewlineStillReads covers the EOF-without-
// newline edge case (e.g. `printf 'v' | sand secret set ...`, no trailing
// \n): the value should still be captured rather than erroring.
func TestReadSecretValue_NoTrailingNewlineStillReads(t *testing.T) {
	in := strings.NewReader("novnewline")
	var prompt bytes.Buffer

	got, err := readSecretValue(in, &prompt, false, "MY_VAR")
	if err != nil {
		t.Fatalf("readSecretValue: %v", err)
	}
	if got != "novnewline" {
		t.Fatalf("readSecretValue value = %q, want %q", got, "novnewline")
	}
}

// TestReadSecretValue_EmptyStdinErrors ensures a closed/empty stdin (no
// value at all) is a clear error rather than silently storing "".
func TestReadSecretValue_EmptyStdinErrors(t *testing.T) {
	in := strings.NewReader("")
	var prompt bytes.Buffer

	_, err := readSecretValue(in, &prompt, false, "MY_VAR")
	if err == nil {
		t.Fatal("readSecretValue on empty stdin: expected error, got nil")
	}
}

// TestDoSecretSet_RoutesCategoriesAndPersists exercises the real
// internal/secrets Load/SetSecret/Save round trip for each routing case,
// confirming the value that ends up on disk is exactly the value passed in
// (never mutated) and lands in the right category/scope/name slot.
func TestDoSecretSet_RoutesCategoriesAndPersists(t *testing.T) {
	withXDGDataHome(t)
	const vmName = "test-vm"

	if err := doSecretSet(vmName, "", false, "GLOBAL_VAR", "gval"); err != nil {
		t.Fatalf("doSecretSet(global): %v", err)
	}
	if err := doSecretSet(vmName, "github.com/acme", true, "unused", "ghp_token"); err != nil {
		t.Fatalf("doSecretSet(github scoped): %v", err)
	}
	if err := doSecretSet(vmName, "", true, "unused", "default_token"); err != nil {
		t.Fatalf("doSecretSet(github default): %v", err)
	}
	if err := doSecretSet(vmName, "some/dir", false, "DIR_VAR", "dval"); err != nil {
		t.Fatalf("doSecretSet(dir_env): %v", err)
	}

	store, err := secrets.Load(vmName)
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}

	if len(store.Global) != 1 || store.Global[0].Name != "GLOBAL_VAR" || store.Global[0].Value != "gval" {
		t.Fatalf("Global = %+v, want [{GLOBAL_VAR gval}]", store.Global)
	}
	if len(store.GitHub) != 2 {
		t.Fatalf("GitHub = %+v, want 2 entries", store.GitHub)
	}
	foundScoped, foundDefault := false, false
	for _, g := range store.GitHub {
		if g.Scope == "github.com/acme" && g.Token == "ghp_token" {
			foundScoped = true
		}
		if g.Scope == "" && g.Token == "default_token" {
			foundDefault = true
		}
	}
	if !foundScoped || !foundDefault {
		t.Fatalf("GitHub entries missing expected scope/token: %+v", store.GitHub)
	}
	if len(store.DirEnv) != 1 || store.DirEnv[0].Scope != "some/dir" || store.DirEnv[0].Name != "DIR_VAR" || store.DirEnv[0].Value != "dval" {
		t.Fatalf("DirEnv = %+v, want [{some/dir DIR_VAR dval}]", store.DirEnv)
	}
}

// TestPrintSecretList_MasksByDefaultRevealsWithFlag is the masked-list
// acceptance criterion: default output must never contain the cleartext
// value, and --reveal must.
func TestPrintSecretList_MasksByDefaultRevealsWithFlag(t *testing.T) {
	store := &secrets.Store{}
	store.SetSecret(secrets.CategoryGlobal, "", "MY_VAR", "topsecret")

	var masked bytes.Buffer
	printSecretList(&masked, store, false)
	if strings.Contains(masked.String(), "topsecret") {
		t.Fatalf("printSecretList(reveal=false) leaked cleartext value: %q", masked.String())
	}
	if !strings.Contains(masked.String(), "MY_VAR") {
		t.Fatalf("printSecretList(reveal=false) missing secret name: %q", masked.String())
	}

	var revealed bytes.Buffer
	printSecretList(&revealed, store, true)
	if !strings.Contains(revealed.String(), "topsecret") {
		t.Fatalf("printSecretList(reveal=true) = %q, want it to contain cleartext value", revealed.String())
	}
}

// TestPrintSecretList_EmptyStoreSaysNoSecrets covers the empty case so list
// output isn't blank/confusing.
func TestPrintSecretList_EmptyStoreSaysNoSecrets(t *testing.T) {
	store := &secrets.Store{}
	var out bytes.Buffer
	printSecretList(&out, store, false)
	if out.Len() == 0 {
		t.Fatal("printSecretList on empty store produced no output")
	}
}

// TestEffectSummaryLines_GitHubOnly is the honesty-critical AC for `sand
// secret sync`: with only a GitHub secret stored, the summary must claim the
// GitHub/git effect is immediate and must NOT print the env-var line (there
// is no env-var secret to have changed).
func TestEffectSummaryLines_GitHubOnly(t *testing.T) {
	store := &secrets.Store{}
	store.SetSecret(secrets.CategoryGitHub, "", "", "ghp_x")

	lines := effectSummaryLines(store)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "effective immediately") {
		t.Fatalf("expected an immediate-effect line, got: %v", lines)
	}
	if strings.Contains(joined, "new shell") {
		t.Fatalf("must not print the env-var line when no env-var secret is stored: %v", lines)
	}
}

// TestEffectSummaryLines_EnvVarOnly covers the inverse: only a global or
// dir-scoped env var stored must print the "new shell" line and must NOT
// claim an immediate GitHub/git effect. It also asserts the critical honesty
// property from the task spec: the text must not claim a running process
// (e.g. a running claude) picks up the new value, and must not instruct the
// user (or sand itself) to force a restart — describing the fact that an
// already-running process keeps its old values "until restarted" is fine
// (and is the spec's own suggested wording); telling the user to actually go
// do it is not.
func TestEffectSummaryLines_EnvVarOnly(t *testing.T) {
	store := &secrets.Store{}
	store.SetSecret(secrets.CategoryGlobal, "", "MY_VAR", "v")

	lines := effectSummaryLines(store)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "new shell") {
		t.Fatalf("expected the new-shell line for an env-var secret, got: %v", lines)
	}
	if strings.Contains(joined, "effective immediately") {
		t.Fatalf("must not claim an immediate git/GitHub effect when no GitHub secret is stored: %v", lines)
	}
	if strings.Contains(joined, "pick up") || strings.Contains(joined, "picks up") {
		t.Fatalf("must not claim a running process picks up the new value: %v", lines)
	}
	lower := strings.ToLower(joined)
	if strings.Contains(lower, "please restart") || strings.Contains(lower, "must restart") || strings.Contains(lower, "restart the vm") || strings.Contains(lower, "restart your shell") {
		t.Fatalf("must not instruct a forced restart: %v", lines)
	}
}

// TestEffectSummaryLines_Both covers a store with both kinds of secrets:
// both lines must print.
func TestEffectSummaryLines_Both(t *testing.T) {
	store := &secrets.Store{}
	store.SetSecret(secrets.CategoryGitHub, "", "", "ghp_x")
	store.SetSecret(secrets.CategoryDirEnv, "some/dir", "MY_VAR", "v")

	lines := effectSummaryLines(store)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "effective immediately") {
		t.Fatalf("expected the GitHub line, got: %v", lines)
	}
	if !strings.Contains(joined, "new shell") {
		t.Fatalf("expected the env-var line, got: %v", lines)
	}
}

// TestEffectSummaryLines_Empty covers an empty store: sync still succeeds
// (the secrets role is a safe no-op), so the summary must say something
// rather than print nothing.
func TestEffectSummaryLines_Empty(t *testing.T) {
	lines := effectSummaryLines(&secrets.Store{})
	if len(lines) == 0 {
		t.Fatal("effectSummaryLines(empty store) produced no output")
	}
}

// TestPrintSetReminder_NotApplied: when the VM isn't running, `set` only wrote
// the host store, so the reminder must point the user at how it gets applied
// (create/start/sync) and must NOT claim it was applied.
func TestPrintSetReminder_NotApplied(t *testing.T) {
	var b strings.Builder
	printSetReminder(&b, secrets.CategoryGlobal, "dev", false)
	out := b.String()
	if !strings.Contains(out, "sand secret sync --vm dev") {
		t.Fatalf("not-applied reminder should mention sync, got: %q", out)
	}
	if strings.Contains(out, "Saved and applied") {
		t.Fatalf("must not claim it was applied when the VM wasn't running: %q", out)
	}
}

// TestPrintSetReminder_AppliedEnvVar: an applied env-var secret must tell the
// user to reconnect active sessions (new shell) — the load-bearing honesty
// point, since rendering does not change a running process's environment.
func TestPrintSetReminder_AppliedEnvVar(t *testing.T) {
	var b strings.Builder
	printSetReminder(&b, secrets.CategoryGlobal, "dev", true)
	out := b.String()
	if !strings.Contains(out, "reconnect") || !strings.Contains(out, "NEW shells") {
		t.Fatalf("applied env-var reminder must tell the user to reconnect for a new shell, got: %q", out)
	}
}

// TestPrintSetReminder_AppliedGitHub: an applied GitHub token is live on the
// next git/gh call, so the reminder must say no reconnect is needed and must
// NOT tell the user to reconnect sessions.
func TestPrintSetReminder_AppliedGitHub(t *testing.T) {
	var b strings.Builder
	printSetReminder(&b, secrets.CategoryGitHub, "dev", true)
	out := b.String()
	if !strings.Contains(out, "git/gh call") {
		t.Fatalf("applied github reminder should mention the next git/gh call, got: %q", out)
	}
	if strings.Contains(out, "reconnect any active sessions") {
		t.Fatalf("github token is live; must not tell the user to reconnect sessions: %q", out)
	}
}

// TestPrintRmReminder_NotApplied: a removal on a non-running VM only touched
// the host store, so the reminder must point at how it gets purged from the VM
// (create/start/sync) and must not claim it was already removed from the VM.
func TestPrintRmReminder_NotApplied(t *testing.T) {
	var b strings.Builder
	printRmReminder(&b, secrets.CategoryGlobal, "dev", false)
	out := b.String()
	if !strings.Contains(out, "sand secret sync --vm dev") {
		t.Fatalf("not-applied rm reminder should mention sync, got: %q", out)
	}
	if !strings.Contains(out, "host store") {
		t.Fatalf("not-applied rm reminder should say it was removed from the host store, got: %q", out)
	}
}

// TestPrintRmReminder_AppliedEnvVar: a removed env var still lingers in
// already-open shells, so the reminder must say to open a new shell.
func TestPrintRmReminder_AppliedEnvVar(t *testing.T) {
	var b strings.Builder
	printRmReminder(&b, secrets.CategoryGlobal, "dev", true)
	out := b.String()
	if !strings.Contains(out, "new shell") {
		t.Fatalf("applied env-var rm reminder must mention opening a new shell, got: %q", out)
	}
}

// TestPrintRmReminder_AppliedGitHub: a removed GitHub token stops being used on
// the next git/gh call.
func TestPrintRmReminder_AppliedGitHub(t *testing.T) {
	var b strings.Builder
	printRmReminder(&b, secrets.CategoryGitHub, "dev", true)
	out := b.String()
	if !strings.Contains(out, "next call") {
		t.Fatalf("applied github rm reminder should say git stops using it on the next call, got: %q", out)
	}
}

// TestCheckVMRunning_Running: a "Running" status with no error passes.
func TestCheckVMRunning_Running(t *testing.T) {
	if err := checkVMRunning("dev", "Running", nil); err != nil {
		t.Fatalf("checkVMRunning(Running): unexpected error: %v", err)
	}
}

// TestCheckVMRunning_StoppedSurfacesStatus: a non-running status must error,
// and the error must surface the observed status so the user knows why sync
// refused (task 5: "error clearly if the VM isn't running (surface limactl
// status)").
func TestCheckVMRunning_StoppedSurfacesStatus(t *testing.T) {
	err := checkVMRunning("dev", "Stopped", nil)
	if err == nil {
		t.Fatal("checkVMRunning(Stopped): expected an error")
	}
	if !strings.Contains(err.Error(), "Stopped") {
		t.Fatalf("error must surface the observed status: %v", err)
	}
	if !strings.Contains(err.Error(), "dev") {
		t.Fatalf("error must name the VM: %v", err)
	}
}

// TestCheckVMRunning_AbsentSurfacesNotFound: an empty status (VM does not
// exist) must error without a blank/confusing status string.
func TestCheckVMRunning_AbsentSurfacesNotFound(t *testing.T) {
	err := checkVMRunning("dev", "", nil)
	if err == nil {
		t.Fatal("checkVMRunning(absent): expected an error")
	}
	if strings.Contains(err.Error(), "status: )") || strings.Contains(err.Error(), "status: \n") {
		t.Fatalf("error should not print a blank status: %v", err)
	}
}

// TestCheckVMRunning_StatusErrorSurfaced: an error from limactl itself
// (e.g. limactl not usable) must be surfaced, not swallowed.
func TestCheckVMRunning_StatusErrorSurfaced(t *testing.T) {
	statusErr := errors.New("limactl: boom")
	err := checkVMRunning("dev", "", statusErr)
	if err == nil {
		t.Fatal("checkVMRunning(status error): expected an error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("underlying limactl error must be surfaced: %v", err)
	}
}

// TestSyncConfig_FallsBackWhenUnmanaged: an unmanaged/unregistered VM name
// still resolves to a usable CreateConfig (host user, sand's defaults) —
// RenderSecrets requires cfg.User for the secrets role's getent lookup, and
// sync must not fail just because the VM predates/bypasses the registry.
func TestSyncConfig_FallsBackWhenUnmanaged(t *testing.T) {
	withXDGDataHome(t)

	cfg := syncConfig("never-registered")
	if cfg.Name != "never-registered" {
		t.Fatalf("cfg.Name = %q, want %q", cfg.Name, "never-registered")
	}
	if cfg.User == "" {
		t.Fatal("syncConfig must never return an empty User (required by the secrets role)")
	}
}

// TestSyncConfig_UsesRegisteredConfig: a managed VM's recorded CreateConfig
// (notably its User) must be reused, not sand's generic default, so sync
// renders against the identity the VM actually has.
func TestSyncConfig_UsesRegisteredConfig(t *testing.T) {
	withXDGDataHome(t)

	reg, err := registry.Load()
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	if err := reg.Add(vm.CreateConfig{Name: "dev", BaseName: "claude-base", User: "someoneelse", CPUs: 2}); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}

	cfg := syncConfig("dev")
	if cfg.Name != "dev" {
		t.Fatalf("cfg.Name = %q, want %q", cfg.Name, "dev")
	}
	if cfg.User != "someoneelse" {
		t.Fatalf("cfg.User = %q, want the registered user %q", cfg.User, "someoneelse")
	}
}
