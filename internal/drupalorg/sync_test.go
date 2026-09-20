package drupalorg

import (
	"strings"
	"testing"
)

func TestBuildSyncCommandsValidateTheBranch(t *testing.T) {
	// The branch reaches the guest as script TEXT — RunArgv leaves no argv
	// slot for it — so anything that could end a quote, start a substitution,
	// or add a line must be refused rather than escaped and carried on with.
	bad := []string{
		"",
		"dubbot",              // no issue number
		"dubbot-",             // no issue number
		"-3619578",            // no module
		"Dubbot-3619578",      // uppercase is not a module name
		"dub-bot-3619578",     // "-" is not in a module name
		"dubbot-3619578; rm",  // command separator
		"dubbot-3619578 x",    // whitespace
		"dubbot-$(whoami)",    // substitution
		"dubbot-3619578\nx",   // newline
		"dubbot-3619578'",     // quote
		"`dubbot-3619578`",    // backticks
		"../../etc/passwd",    // traversal
		"refs/heads/dubbot-1", // a full ref, not a fork branch
	}
	for _, branch := range bad {
		if _, err := BuildFetchCommand(branch); err == nil {
			t.Errorf("BuildFetchCommand(%q): want an error, got nil", branch)
		}
		if _, err := BuildResetCommand(branch); err == nil {
			t.Errorf("BuildResetCommand(%q): want an error, got nil", branch)
		}
	}

	good := []string{"dubbot-3619578", "webform-1", "views_bulk_operations-3181657", "foo2-123"}
	for _, branch := range good {
		fetch, err := BuildFetchCommand(branch)
		if err != nil {
			t.Fatalf("BuildFetchCommand(%q): %v", branch, err)
		}
		if !strings.Contains(fetch, branch) {
			t.Errorf("BuildFetchCommand(%q) did not substitute the branch", branch)
		}
		if strings.Contains(fetch, "__BRANCH__") {
			t.Errorf("BuildFetchCommand(%q) left the token unsubstituted", branch)
		}
		reset, err := BuildResetCommand(branch)
		if err != nil {
			t.Fatalf("BuildResetCommand(%q): %v", branch, err)
		}
		if strings.Contains(reset, "__BRANCH__") {
			t.Errorf("BuildResetCommand(%q) left the token unsubstituted", branch)
		}
	}
}

// The fetch step must not be able to change anything in the guest: it runs
// after every publish with no confirmation of its own, so it has to be a
// read. The reset step is the one that writes, and it must re-check its own
// preconditions rather than trusting a host-side decision made minutes
// earlier against a VM that runs untrusted code.
func TestSyncScriptShapes(t *testing.T) {
	fetch, err := BuildFetchCommand("dubbot-3619578")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"reset", "checkout", "merge", "rebase", "push", "commit", "clean"} {
		if strings.Contains(fetch, "git "+forbidden) {
			t.Errorf("the fetch script runs `git %s`; it must only read", forbidden)
		}
	}

	reset, err := BuildResetCommand("dubbot-3619578")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"git status --porcelain",   // re-checks the tree is clean
		`git diff --quiet "$head"`, // re-checks the content still matches
		"git reset --hard",         // and only then acts
	} {
		if !strings.Contains(reset, want) {
			t.Errorf("the reset script is missing %q", want)
		}
	}
	// Order matters as much as presence: a guard after the reset guards
	// nothing.
	if strings.Index(reset, "git reset --hard") < strings.Index(reset, "git status --porcelain") {
		t.Error("the reset script resets before it checks the tree is clean")
	}
	// Both scripts must measure the working tree the SAME way, and it must be
	// the tracked-only way. A fetch that reported untracked files as dirt
	// would never offer the reset; a reset that refused on them would reject
	// the offer the fetch had just made. They are two halves of one decision.
	for name, script := range map[string]string{"fetch": fetch, "reset": reset} {
		if !strings.Contains(script, "git status --porcelain --untracked-files=no") {
			t.Errorf("the %s script counts untracked files as uncommitted work", name)
		}
	}
	// The fetch still has to REPORT the untracked count — it is in the
	// summary the user reads, and ParseSyncStatus requires the field.
	if !strings.Contains(fetch, "git ls-files --others --exclude-standard") {
		t.Error("the fetch script does not count untracked files at all")
	}
	if !strings.Contains(fetch, "untracked=%s") {
		t.Error("the fetch script does not print the untracked field")
	}

	// Neither script may assume the remote is called "origin" — a checkout
	// published from a differently-named remote must be fetched from the
	// same place the publish read it from.
	for name, script := range map[string]string{"fetch": fetch, "reset": reset} {
		if strings.Contains(script, `"origin"`) {
			t.Errorf("the %s script hard-codes the origin remote", name)
		}
	}
}

func TestParseSyncStatus(t *testing.T) {
	out := []byte("dirty=0\nuntracked=2\nlocal=aaa111\nfork=bbb222\nahead=3\nbehind=3\nsame=1\n")
	got, err := ParseSyncStatus(out)
	if err != nil {
		t.Fatalf("ParseSyncStatus: %v", err)
	}
	want := SyncStatus{Dirty: 0, Untracked: 2, Local: "aaa111", Fork: "bbb222", Ahead: 3, Behind: 3, SameContent: true}
	if got != want {
		t.Fatalf("ParseSyncStatus = %+v, want %+v", got, want)
	}
	if !got.Diverged() || !got.CanAdopt() {
		t.Errorf("a clean replay must read as diverged and adoptable: %+v", got)
	}
	// The untracked files above must be visible in the offer rather than
	// silently dropped: they are exactly what a `git reset --hard` prompt
	// makes a developer nervous about.
	if !strings.Contains(got.Summary(), "2 untracked file(s) are left untouched") {
		t.Errorf("Summary() does not account for the untracked files: %q", got.Summary())
	}
}

// A login shell's banner, or anything else the guest prints, is noise around
// the fields — not a parse failure and not a value.
func TestParseSyncStatusIgnoresNoise(t *testing.T) {
	out := []byte("Welcome to Ubuntu!\ndirty=1\nrandom line\nuntracked=0\nlocal=aaa\nfork=bbb\nahead=2\nbehind=5\nsame=0\nbye\n")
	got, err := ParseSyncStatus(out)
	if err != nil {
		t.Fatalf("ParseSyncStatus: %v", err)
	}
	if got.Dirty != 1 || got.Ahead != 2 || got.Behind != 5 || got.SameContent {
		t.Errorf("ParseSyncStatus = %+v, want the fields read past the noise", got)
	}
}

// A partial reading is the dangerous case: a missing "dirty" would default
// to 0 and a missing "same" to false, and CanAdopt would then be deciding
// whether to destroy a working tree from values nothing reported.
func TestParseSyncStatusRejectsPartialOutput(t *testing.T) {
	cases := map[string]string{
		"no dirty": "untracked=0\nlocal=a\nfork=b\nahead=1\nbehind=1\nsame=1\n",
		"no same":  "dirty=0\nuntracked=0\nlocal=a\nfork=b\nahead=1\nbehind=1\n",
		"no local": "dirty=0\nuntracked=0\nfork=b\nahead=1\nbehind=1\nsame=1\n",
		// A missing "untracked" must fail like any other absent field rather
		// than defaulting to 0. It gates nothing, but a reading that invents
		// values is not a reading.
		"no untracked": "dirty=0\nlocal=a\nfork=b\nahead=1\nbehind=1\nsame=1\n",
		"empty":        "",
		"non-numer":    "dirty=lots\nuntracked=0\nlocal=a\nfork=b\nahead=1\nbehind=1\nsame=1\n",
	}
	for name, out := range cases {
		if _, err := ParseSyncStatus([]byte(out)); err == nil {
			t.Errorf("ParseSyncStatus(%s): want an error, got nil", name)
		}
	}
}

func TestSyncStatusCanAdopt(t *testing.T) {
	cases := []struct {
		name   string
		status SyncStatus
		want   bool
	}{
		{
			name:   "clean replay: same content, divergent history, clean tree",
			status: SyncStatus{Local: "a", Fork: "b", SameContent: true},
			want:   true,
		},
		{
			// Publication carries committed commits only, so the
			// uncommitted remainder was deliberately left behind. Adopting
			// would destroy it.
			name:   "uncommitted work would be lost",
			status: SyncStatus{Local: "a", Fork: "b", SameContent: true, Dirty: 1},
			want:   false,
		},
		{
			// THE REGRESSION THIS GUARDS. These checkouts live in a VM whose
			// job is running agents over them, so untracked scratch files are
			// the normal state. Counting them as dirt withheld the offer
			// essentially always, and the reset provably cannot touch them:
			// SameContent means the fork's tree equals HEAD's, and an
			// untracked file is absent from both.
			name:   "untracked files alone do not block adopting",
			status: SyncStatus{Local: "a", Fork: "b", SameContent: true, Untracked: 7},
			want:   true,
		},
		{
			// Tracked changes still do, and the two must not be conflated:
			// a reset --hard discards these outright.
			name:   "tracked changes still block, untracked or not",
			status: SyncStatus{Local: "a", Fork: "b", SameContent: true, Dirty: 1, Untracked: 7},
			want:   false,
		},
		{
			// Not a SHA-divergence artifact: real local changes are absent
			// from the fork, and a reset would discard them.
			name:   "content genuinely differs",
			status: SyncStatus{Local: "a", Fork: "b", SameContent: false},
			want:   false,
		},
		{
			name:   "already in sync: nothing to adopt",
			status: SyncStatus{Local: "a", Fork: "a", SameContent: true},
			want:   false,
		},
		{
			name:   "no fork commit at all",
			status: SyncStatus{Local: "a", SameContent: true},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.CanAdopt(); got != tc.want {
				t.Errorf("CanAdopt() = %v, want %v for %+v", got, tc.want, tc.status)
			}
			if tc.status.Summary() == "" {
				t.Error("Summary() is empty; every state must describe itself")
			}
		})
	}
}
