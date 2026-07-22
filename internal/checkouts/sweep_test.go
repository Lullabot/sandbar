package checkouts

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// sweepRecord builds one raw sweep record (key=value lines + the record
// delimiter) from a field map, exactly as BuildSweepCommand's guest script
// emits it. Missing keys are simply omitted, mirroring a guest field that
// came back empty (e.g. a detached HEAD's branch).
func sweepRecord(fields map[string]string) string {
	var b strings.Builder
	for _, k := range []string{"path", "kind", "gitdirptr", "branch", "remote", "url", "tracking", "ahead", "behind", "dirty"} {
		if v, ok := fields[k]; ok {
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(v)
			b.WriteByte('\n')
		}
	}
	b.WriteString(sweepRecordDelim)
	b.WriteByte('\n')
	return b.String()
}

func TestParseSweep_PushStateClassification(t *testing.T) {
	cases := []struct {
		name       string
		fields     map[string]string
		wantState  PushState
		wantAhead  int
		wantBehind int
	}{
		{
			name: "pushed: tracking ref exists, HEAD matches it",
			fields: map[string]string{
				"path": "/home/u/proj", "kind": "repo", "branch": "main",
				"remote": "origin", "url": "git@github.com:org/repo.git",
				"tracking": "1", "ahead": "0", "behind": "0", "dirty": "0",
			},
			wantState: PushStatePushed, wantAhead: 0, wantBehind: 0,
		},
		{
			name: "pushed via -u-less push: tracking ref present, no upstream config was ever consulted",
			fields: map[string]string{
				// This simulates `git push origin HEAD` (no -u): the guest
				// resolves the tracking ref directly and finds it, with HEAD
				// at 0 ahead of it. ParseSweep must classify this "pushed"
				// purely from tracking+ahead — it has no upstream-config
				// field to consult even if it wanted to.
				"path": "/home/u/proj", "kind": "repo", "branch": "feature",
				"remote": "origin", "url": "https://github.com/org/repo.git",
				"tracking": "1", "ahead": "0", "behind": "0", "dirty": "0",
			},
			wantState: PushStatePushed, wantAhead: 0, wantBehind: 0,
		},
		{
			name: "unpushed: tracking ref exists but HEAD is ahead of it",
			fields: map[string]string{
				"path": "/home/u/proj", "kind": "repo", "branch": "main",
				"remote": "origin", "url": "git@github.com:org/repo.git",
				"tracking": "1", "ahead": "3", "behind": "0", "dirty": "0",
			},
			wantState: PushStateUnpushed, wantAhead: 3, wantBehind: 0,
		},
		{
			name: "never: no remote-tracking ref at all",
			fields: map[string]string{
				"path": "/home/u/proj", "kind": "repo", "branch": "scratch",
				"remote": "", "url": "",
				"tracking": "0", "ahead": "0", "behind": "0", "dirty": "0",
			},
			wantState: PushStateNever, wantAhead: 0, wantBehind: 0,
		},
		{
			name: "never: stray ahead/behind values are zeroed when there is no tracking ref",
			fields: map[string]string{
				// A malformed/defensive case: tracking=0 must win even if
				// ahead/behind somehow carry stale nonzero text.
				"path": "/home/u/proj", "kind": "repo", "branch": "scratch",
				"tracking": "0", "ahead": "9", "behind": "9", "dirty": "0",
			},
			wantState: PushStateNever, wantAhead: 0, wantBehind: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := sweepRecord(tc.fields)
			got := ParseSweep(raw)
			if len(got.Checkouts) != 1 {
				t.Fatalf("got %d checkouts, want 1 (raw=%q)", len(got.Checkouts), raw)
			}
			c := got.Checkouts[0]
			if c.PushState != tc.wantState {
				t.Errorf("PushState = %q, want %q", c.PushState, tc.wantState)
			}
			if c.Ahead != tc.wantAhead {
				t.Errorf("Ahead = %d, want %d", c.Ahead, tc.wantAhead)
			}
			if c.Behind != tc.wantBehind {
				t.Errorf("Behind = %d, want %d", c.Behind, tc.wantBehind)
			}
		})
	}
}

func TestParseSweep_RemoteURLParsing(t *testing.T) {
	cases := []struct {
		name        string
		url         string
		wantForge   string
		wantOrgRepo string
	}{
		{
			name:        "SSH scp-like GitHub URL",
			url:         "git@github.com:org/repo.git",
			wantForge:   "github.com",
			wantOrgRepo: "org/repo",
		},
		{
			name:        "HTTPS GitHub URL with .git suffix",
			url:         "https://github.com/org/repo.git",
			wantForge:   "github.com",
			wantOrgRepo: "org/repo",
		},
		{
			name:        "HTTPS GitHub URL without .git suffix",
			url:         "https://github.com/org/repo",
			wantForge:   "github.com",
			wantOrgRepo: "org/repo",
		},
		{
			name:        "SSH scp-like GitLab URL",
			url:         "git@gitlab.com:group/repo.git",
			wantForge:   "gitlab.com",
			wantOrgRepo: "group/repo",
		},
		{
			name:        "HTTPS GitLab URL with nested subgroups",
			url:         "https://gitlab.com/group/sub/repo.git",
			wantForge:   "gitlab.com",
			wantOrgRepo: "group/sub/repo",
		},
		{
			name:        "ssh:// form with userinfo",
			url:         "ssh://git@gitlab.example.com/group/sub/repo.git",
			wantForge:   "gitlab.example.com",
			wantOrgRepo: "group/sub/repo",
		},
		{
			name:        "plain http:// form",
			url:         "http://github.com/org/repo.git",
			wantForge:   "github.com",
			wantOrgRepo: "org/repo",
		},
		{
			name:        "no remote configured at all",
			url:         "",
			wantForge:   "",
			wantOrgRepo: "",
		},
		{
			name:        "scp-like syntax missing the ':' separator entirely",
			url:         "git@github.com",
			wantForge:   "",
			wantOrgRepo: "",
		},
		{
			name:        "HTTPS URL with a bare host and no path",
			url:         "https://github.com",
			wantForge:   "github.com",
			wantOrgRepo: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := sweepRecord(map[string]string{
				"path": "/home/u/proj", "kind": "repo", "branch": "main",
				"remote": "origin", "url": tc.url,
				"tracking": "0", "ahead": "0", "behind": "0", "dirty": "0",
			})
			got := ParseSweep(raw)
			if len(got.Checkouts) != 1 {
				t.Fatalf("got %d checkouts, want 1", len(got.Checkouts))
			}
			c := got.Checkouts[0]
			if c.Forge != tc.wantForge {
				t.Errorf("Forge = %q, want %q", c.Forge, tc.wantForge)
			}
			if c.OrgRepo != tc.wantOrgRepo {
				t.Errorf("OrgRepo = %q, want %q", c.OrgRepo, tc.wantOrgRepo)
			}
		})
	}
}

func TestParseSweep_WorktreeRow(t *testing.T) {
	raw := sweepRecord(map[string]string{
		"path": "/home/u/proj/.worktrees/feature-x", "kind": "worktree",
		"gitdirptr": "/home/u/proj/.git/worktrees/feature-x",
		"branch":    "feature-x",
		"remote":    "origin", "url": "git@github.com:org/repo.git",
		"tracking": "0", "ahead": "0", "behind": "0", "dirty": "0",
	})
	got := ParseSweep(raw)
	if len(got.Checkouts) != 1 {
		t.Fatalf("got %d checkouts, want 1", len(got.Checkouts))
	}
	c := got.Checkouts[0]
	if c.Kind != KindWorktree {
		t.Errorf("Kind = %q, want %q", c.Kind, KindWorktree)
	}
	if want := "/home/u/proj"; c.Parent != want {
		t.Errorf("Parent = %q, want %q", c.Parent, want)
	}
}

func TestParseSweep_RepoRowHasNoParent(t *testing.T) {
	// Even if a repo record somehow carried a gitdirptr value, a KindRepo row
	// must never surface a Parent — Parent is a worktree-only concept.
	raw := sweepRecord(map[string]string{
		"path": "/home/u/proj", "kind": "repo",
		"gitdirptr": "/home/u/proj/.git/worktrees/feature-x",
		"branch":    "main",
		"tracking":  "0", "ahead": "0", "behind": "0", "dirty": "0",
	})
	got := ParseSweep(raw)
	if len(got.Checkouts) != 1 {
		t.Fatalf("got %d checkouts, want 1", len(got.Checkouts))
	}
	if c := got.Checkouts[0]; c.Parent != "" {
		t.Errorf("Parent = %q, want empty for a repo row", c.Parent)
	}
}

func TestParseSweep_NonOriginRemote(t *testing.T) {
	// The remote need not be named "origin" — the guest reads the branch's
	// configured remote (or the first `git remote`), never assumes the name,
	// and ParseSweep must classify correctly regardless of what it's called.
	raw := sweepRecord(map[string]string{
		"path": "/home/u/proj", "kind": "repo", "branch": "main",
		"remote": "upstream", "url": "git@github.com:org/repo.git",
		"tracking": "1", "ahead": "0", "behind": "2", "dirty": "0",
	})
	got := ParseSweep(raw)
	if len(got.Checkouts) != 1 {
		t.Fatalf("got %d checkouts, want 1", len(got.Checkouts))
	}
	c := got.Checkouts[0]
	if c.PushState != PushStatePushed {
		t.Errorf("PushState = %q, want %q", c.PushState, PushStatePushed)
	}
	if c.Forge != "github.com" || c.OrgRepo != "org/repo" {
		t.Errorf("Forge/OrgRepo = %q/%q, want github.com/org/repo", c.Forge, c.OrgRepo)
	}
	if c.Behind != 2 {
		t.Errorf("Behind = %d, want 2", c.Behind)
	}
}

func TestParseSweep_DirtyCount(t *testing.T) {
	raw := sweepRecord(map[string]string{
		"path": "/home/u/proj", "kind": "repo", "branch": "main",
		"remote": "origin", "url": "git@github.com:org/repo.git",
		"tracking": "1", "ahead": "0", "behind": "0", "dirty": "4",
	})
	got := ParseSweep(raw)
	if len(got.Checkouts) != 1 {
		t.Fatalf("got %d checkouts, want 1", len(got.Checkouts))
	}
	if c := got.Checkouts[0]; c.Dirty != 4 {
		t.Errorf("Dirty = %d, want 4", c.Dirty)
	}
}

func TestParseSweep_TruncationFlag(t *testing.T) {
	raw := sweepTruncatedMarker + "\n" +
		sweepRecord(map[string]string{
			"path": "/home/u/proj", "kind": "repo", "branch": "main",
			"tracking": "0", "ahead": "0", "behind": "0", "dirty": "0",
		})
	got := ParseSweep(raw)
	if !got.Truncated {
		t.Error("Truncated = false, want true")
	}
	if len(got.Checkouts) != 1 {
		t.Fatalf("got %d checkouts, want 1", len(got.Checkouts))
	}
}

func TestParseSweep_MalformedIntegerFieldsDoNotCrash(t *testing.T) {
	// A per-repo `timeout` that fired mid-read, or a corrupted guest write,
	// could hand back garbage instead of a digit. atoiOr's fallback must keep
	// ParseSweep from panicking on strconv.Atoi and must default to 0, never
	// invent a number.
	raw := sweepRecord(map[string]string{
		"path": "/home/u/proj", "kind": "repo", "branch": "main",
		"tracking": "1", "ahead": "not-a-number", "behind": "also-bad", "dirty": "??",
	})
	got := ParseSweep(raw)
	if len(got.Checkouts) != 1 {
		t.Fatalf("got %d checkouts, want 1", len(got.Checkouts))
	}
	c := got.Checkouts[0]
	if c.Ahead != 0 || c.Behind != 0 || c.Dirty != 0 {
		t.Errorf("Ahead/Behind/Dirty = %d/%d/%d, want 0/0/0 on malformed input", c.Ahead, c.Behind, c.Dirty)
	}
	// tracking=1 with a non-numeric (defaulted to 0) ahead still reads as
	// "pushed" per the classification rule — not a crash, not a fabricated state.
	if c.PushState != PushStatePushed {
		t.Errorf("PushState = %q, want %q", c.PushState, PushStatePushed)
	}
}

func TestParseSweep_NoTruncationByDefault(t *testing.T) {
	raw := sweepRecord(map[string]string{
		"path": "/home/u/proj", "kind": "repo", "branch": "main",
		"tracking": "0", "ahead": "0", "behind": "0", "dirty": "0",
	})
	got := ParseSweep(raw)
	if got.Truncated {
		t.Error("Truncated = true, want false")
	}
}

func TestParseSweep_MultipleRecordsAndNoise(t *testing.T) {
	// Login-shell noise (a motd, a profile banner) must not corrupt or
	// duplicate records, mirroring the heartbeat parser's own tolerance for
	// an unrecognized line.
	raw := "Welcome to Ubuntu 22.04\n" +
		sweepRecord(map[string]string{
			"path": "/home/u/one", "kind": "repo", "branch": "main",
			"tracking": "0", "ahead": "0", "behind": "0", "dirty": "0",
		}) +
		"some=noise=with=extra=equals\n" +
		sweepRecord(map[string]string{
			"path": "/home/u/two", "kind": "repo", "branch": "main",
			"tracking": "1", "ahead": "0", "behind": "0", "dirty": "1",
		})

	got := ParseSweep(raw)
	if len(got.Checkouts) != 2 {
		t.Fatalf("got %d checkouts, want 2", len(got.Checkouts))
	}
	if got.Checkouts[0].Path != "/home/u/one" {
		t.Errorf("Checkouts[0].Path = %q, want /home/u/one", got.Checkouts[0].Path)
	}
	if got.Checkouts[1].Path != "/home/u/two" {
		t.Errorf("Checkouts[1].Path = %q, want /home/u/two", got.Checkouts[1].Path)
	}
}

func TestParseSweep_EmptyRawYieldsEmptyResult(t *testing.T) {
	got := ParseSweep("")
	if len(got.Checkouts) != 0 {
		t.Errorf("got %d checkouts, want 0", len(got.Checkouts))
	}
	if got.Truncated {
		t.Error("Truncated = true, want false")
	}
	if got.SweptAt.IsZero() {
		t.Error("SweptAt is zero, want it stamped even for an empty sweep")
	}
}

func TestParseSweep_StampsLastSeenAndSweptAt(t *testing.T) {
	before := time.Now()
	raw := sweepRecord(map[string]string{
		"path": "/home/u/proj", "kind": "repo", "branch": "main",
		"tracking": "0", "ahead": "0", "behind": "0", "dirty": "0",
	})
	got := ParseSweep(raw)
	after := time.Now()

	if len(got.Checkouts) != 1 {
		t.Fatalf("got %d checkouts, want 1", len(got.Checkouts))
	}
	ls := got.Checkouts[0].LastSeen
	if ls.Before(before) || ls.After(after) {
		t.Errorf("LastSeen = %v, want between %v and %v", ls, before, after)
	}
	if got.SweptAt.Before(before) || got.SweptAt.After(after) {
		t.Errorf("SweptAt = %v, want between %v and %v", got.SweptAt, before, after)
	}
}

func TestParseRemoteURL(t *testing.T) {
	cases := []struct {
		name, raw, forge, orgRepo string
	}{
		{"scp-like ssh", "git@github.com:org/repo.git", "github.com", "org/repo"},
		{"https plain", "https://gitlab.com/group/sub/repo.git", "gitlab.com", "group/sub/repo"},
		{"ssh:// with user", "ssh://git@example.com/org/repo.git", "example.com", "org/repo"},
		// A remote carrying embedded credentials must NOT leak the secret into the
		// forge host (which is persisted to disk) — the userinfo is stripped.
		{"https with token", "https://oauth2:GLPAT-xxxx@gitlab.com/org/repo.git", "gitlab.com", "org/repo"},
		{"http with user:pass", "http://user:pw@ghe.internal/o/r.git", "ghe.internal", "o/r"},
		{"empty", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			forge, orgRepo := parseRemoteURL(c.raw)
			if forge != c.forge || orgRepo != c.orgRepo {
				t.Errorf("parseRemoteURL(%q) = (%q, %q), want (%q, %q)", c.raw, forge, orgRepo, c.forge, c.orgRepo)
			}
			if strings.Contains(forge, "@") {
				t.Errorf("forge %q still contains userinfo — a credential would be persisted to disk", forge)
			}
		})
	}
}

func TestParentFromGitdirPointer(t *testing.T) {
	cases := []struct {
		name string
		ptr  string
		want string
	}{
		{
			name: "ordinary worktree pointer",
			ptr:  "/home/u/proj/.git/worktrees/feature-x",
			want: "/home/u/proj",
		},
		{
			name: "trailing newline/whitespace tolerated",
			ptr:  "/home/u/proj/.git/worktrees/feature-x\n",
			want: "/home/u/proj",
		},
		{
			name: "trailing slash tolerated",
			ptr:  "/home/u/proj/.git/worktrees/feature-x/",
			want: "/home/u/proj",
		},
		{
			name: "empty pointer yields empty parent",
			ptr:  "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parentFromGitdirPointer(tc.ptr); got != tc.want {
				t.Errorf("parentFromGitdirPointer(%q) = %q, want %q", tc.ptr, got, tc.want)
			}
		})
	}
}

// TestBuildSweepCommand_Golden asserts the guest command carries every
// load-bearing property the acceptance criteria call for, without trying to
// execute real shell — the exec plumbing belongs to tasks 3/9, not this pure
// string builder.
func TestBuildSweepCommand_Golden(t *testing.T) {
	cmd := BuildSweepCommand()

	mustContain := []string{
		`find "$HOME"`, // bounded from the guest's own home
		"-maxdepth " + strconv.Itoa(sweepMaxDepth), // depth cap
		"-name .git",                    // matches both dirs (repos) and files (worktree pointers) via -print
		"node_modules",                  // noise pruning
		".cache",                        // noise pruning
		strconv.Itoa(sweepMaxCheckouts), // total count cap (~50)
		"head -n " + strconv.Itoa(sweepMaxCheckouts),
		"timeout " + strconv.Itoa(sweepPerRepoTimeout), // per-checkout timeout wrap
		"--no-optional-locks",                          // read-only git reads
		sweepRecordDelim,                               // per-record delimiter, distinct from the heartbeat's
		sweepTruncatedMarker,                           // truncation marker line
	}
	for _, want := range mustContain {
		if !strings.Contains(cmd, want) {
			t.Errorf("BuildSweepCommand() missing %q\n--- command ---\n%s", want, cmd)
		}
	}

	// Distinct from the stats heartbeat's own delimiter (internal/ui/heartbeat.go).
	const heartbeatDelim = "---sand-heartbeat---"
	if sweepRecordDelim == heartbeatDelim {
		t.Errorf("sweepRecordDelim must differ from the heartbeat's delimiter")
	}

	// No network call, ever: no ls-remote, fetch, pull, clone, or push.
	mustNotContain := []string{"ls-remote", "fetch", "pull", "clone", "push"}
	for _, bad := range mustNotContain {
		if strings.Contains(cmd, bad) {
			t.Errorf("BuildSweepCommand() must not contain network call %q\n--- command ---\n%s", bad, cmd)
		}
	}
}

func TestBuildSweepCommand_Deterministic(t *testing.T) {
	if a, b := BuildSweepCommand(), BuildSweepCommand(); a != b {
		t.Error("BuildSweepCommand() is not deterministic across calls")
	}
}

// TestSweepRecordsDefaultBranch pins that the sweep carries the remote's
// default branch through to the parsed Checkout — the field NothingToLand
// needs to tell a pristine clone apart from a fully-pushed feature branch.
func TestSweepRecordsDefaultBranch(t *testing.T) {
	raw := "path=/home/u/repo\nkind=repo\nbranch=main\nremote=origin\n" +
		"url=https://github.com/acme/repo.git\ntracking=1\nahead=0\nbehind=0\ndirty=0\n" +
		"defbranch=main\n" + sweepRecordDelim + "\n"
	got := ParseSweep(raw)
	if len(got.Checkouts) != 1 {
		t.Fatalf("Checkouts = %d, want 1", len(got.Checkouts))
	}
	if got.Checkouts[0].DefaultBranch != "main" {
		t.Fatalf("DefaultBranch = %q, want %q", got.Checkouts[0].DefaultBranch, "main")
	}
}

// TestBuildSweepCommandReadsDefaultBranch pins that the guest script actually
// asks for the remote's HEAD — the read NothingToLand depends on — and that it
// stays a purely LOCAL ref read (no ls-remote, no network), per the package's
// "no network, ever" rule.
func TestBuildSweepCommandReadsDefaultBranch(t *testing.T) {
	cmd := BuildSweepCommand()
	if !strings.Contains(cmd, `symbolic-ref --short "refs/remotes/$remote/HEAD"`) {
		t.Fatalf("sweep command does not read the remote's default branch:\n%s", cmd)
	}
	if !strings.Contains(cmd, "defbranch=%s") {
		t.Fatalf("sweep command does not emit a defbranch field:\n%s", cmd)
	}
	if strings.Contains(cmd, "ls-remote") {
		t.Fatalf("sweep command must not contact the forge:\n%s", cmd)
	}
}

// TestNothingToLand covers the discriminator that keeps a pristine clone from
// reading as work worth landing — the bug where a freshly provisioned VM whose
// only content was a clone showed the amber "actionable" badge.
func TestNothingToLand(t *testing.T) {
	cases := []struct {
		name string
		c    Checkout
		want bool
	}{
		{"pristine clone on main", Checkout{Branch: "main", DefaultBranch: "main", PushState: PushStatePushed}, true},
		{"default branch is not main", Checkout{Branch: "trunk", DefaultBranch: "trunk", PushState: PushStatePushed}, true},
		{"fully pushed feature branch", Checkout{Branch: "feature", DefaultBranch: "main", PushState: PushStatePushed}, false},
		{"feature branch named like a trunk elsewhere", Checkout{Branch: "master", DefaultBranch: "main", PushState: PushStatePushed}, false},
		{"unpushed work on main", Checkout{Branch: "main", DefaultBranch: "main", PushState: PushStateUnpushed, Ahead: 2}, false},
		{"never pushed", Checkout{Branch: "main", DefaultBranch: "main", PushState: PushStateNever}, false},
		{"detached HEAD", Checkout{Branch: "", DefaultBranch: "main", PushState: PushStatePushed}, false},
		// No origin/HEAD to read: fall back to the conventional trunk names.
		{"no default branch known, on main", Checkout{Branch: "main", PushState: PushStatePushed}, true},
		{"no default branch known, on master", Checkout{Branch: "master", PushState: PushStatePushed}, true},
		{"no default branch known, on a feature", Checkout{Branch: "feature", PushState: PushStatePushed}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.NothingToLand(); got != tc.want {
				t.Errorf("NothingToLand() = %v, want %v", got, tc.want)
			}
		})
	}
}
