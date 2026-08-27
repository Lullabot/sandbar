package lima

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// stagePlaybookHost builds an SSHHost whose remote side answers the one probe
// StagePlaybook makes — `pwd` followed by the staged copy's stamp — with abs and
// haveStamp, and succeeds at everything else (scp, rm, the stamp write).
func stagePlaybookHost(abs, haveStamp string) (*SSHHost, *recordingExec) {
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		if anyContains(argv, "pwd") {
			return sh(ctx, "printf '%s\\n%s' "+abs+" "+haveStamp)
		}
		return exec.CommandContext(ctx, "true")
	}}
	return hostWith(testCfg, rec), rec
}

func stagedAnything(calls [][]string) bool {
	for _, c := range calls {
		if len(c) > 0 && c[0] == "scp" {
			return true
		}
		if anyContains(c, "rm -rf") {
			return true
		}
	}
	return false
}

// TestSSHStagePlaybookSkipsAnUnchangedCopy is the guard that makes staging safe
// to run on every clone rather than only on a base build. Re-copying means
// `rm -rf` and scp over a directory a CONCURRENT build's guest may be part-way
// through rsyncing its playbook out of; when the stamp says the remote already
// holds this exact playbook, nothing may be touched.
func TestSSHStagePlaybookSkipsAnUnchangedCopy(t *testing.T) {
	const abs = "/home/dev/.lima/_sand"
	const stamp = "deadbeefcafe"
	h, rec := stagePlaybookHost(abs, stamp)

	dst, err := h.StagePlaybook(context.Background(), "/local/playbook", stamp)
	if err != nil {
		t.Fatalf("StagePlaybook: %v", err)
	}
	if want := abs + "/playbook"; dst != want {
		t.Fatalf("StagePlaybook returned %q, want %q", dst, want)
	}
	if stagedAnything(rec.calls) {
		t.Fatalf("a matching stamp still re-staged the playbook: %v", rec.calls)
	}
}

// TestSSHStagePlaybookRestagesOnAChangedStamp is the other half: a remote
// holding a DIFFERENT playbook must be refreshed, or a clone finalizes from
// someone else's playbook — the failure the stamp exists beside, not instead of.
func TestSSHStagePlaybookRestagesOnAChangedStamp(t *testing.T) {
	const abs = "/home/dev/.lima/_sand"
	h, rec := stagePlaybookHost(abs, "an-older-playbook")

	dst, err := h.StagePlaybook(context.Background(), "/local/playbook", "the-current-one")
	if err != nil {
		t.Fatalf("StagePlaybook: %v", err)
	}
	if !stagedAnything(rec.calls) {
		t.Fatalf("a changed stamp did not re-stage the playbook: %v", rec.calls)
	}
	scpCall := findCall(t, rec.calls, "scp")
	if !hasToken(scpCall, "-r") || !hasToken(scpCall, h.target()+":"+dst) {
		t.Fatalf("stage scp = %v, want scp -r to %s", scpCall, h.target()+":"+dst)
	}
	// The new stamp is recorded, or every later run pays for the copy again.
	var wroteStamp bool
	for _, c := range rec.calls {
		if anyContains(c, "playbook.stamp") && anyContains(c, "the-current-one") {
			wroteStamp = true
		}
	}
	if !wroteStamp {
		t.Fatalf("the new stamp was never written: %v", rec.calls)
	}
}

// TestSSHStagePlaybookAlwaysStagesWithoutAStamp pins the fallback: a caller that
// could not compute the playbook's hash passes "", which must force the copy
// rather than trust whatever is already there.
func TestSSHStagePlaybookAlwaysStagesWithoutAStamp(t *testing.T) {
	h, rec := stagePlaybookHost("/home/dev/.lima/_sand", "")

	if _, err := h.StagePlaybook(context.Background(), "/local/playbook", ""); err != nil {
		t.Fatalf("StagePlaybook: %v", err)
	}
	if !stagedAnything(rec.calls) {
		t.Fatalf("an empty stamp skipped the copy: %v", rec.calls)
	}
	for _, c := range rec.calls {
		if anyContains(c, "playbook.stamp") && anyContains(c, "printf") {
			t.Fatalf("an empty stamp was written to the remote: %v", strings.Join(c, " "))
		}
	}
}

// TestSSHStagePlaybookRefusesAnEmptyRemotePath guards the shell precedence in
// the probe. `mkdir && cd && pwd && cat stamp || true` exits 0 when the MKDIR
// fails — `||` binds no tighter than `&&`, so the fallback covers the whole
// chain — which handed this function a successful call with no output at all.
// The path then resolved to "/playbook", i.e. the remote filesystem ROOT, and
// the staged copy (and every clone's mount) went there. The `|| true` is scoped
// to the cat now; this pins the belt-and-braces guard on the parse.
func TestSSHStagePlaybookRefusesAnEmptyRemotePath(t *testing.T) {
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		return exec.CommandContext(ctx, "true") // exit 0, no output
	}}
	h := hostWith(testCfg, rec)

	dst, err := h.StagePlaybook(context.Background(), "/local/playbook", "stamp")
	if err == nil {
		t.Fatalf("a probe that printed no path was accepted, staging to %q", dst)
	}
	if stagedAnything(rec.calls) {
		t.Fatalf("the playbook was staged despite an unusable remote path: %v", rec.calls)
	}
}
