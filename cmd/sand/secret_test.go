package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/secrets"
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
