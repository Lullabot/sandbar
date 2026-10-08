package provision

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

func TestFinalizeRevisionCapturedOnlyAfterGuestSuccess(t *testing.T) {
	for _, fail := range []bool{true, false} {
		t.Run(map[bool]string{true: "failed", false: "success"}[fail], func(t *testing.T) {
			f := &fakeRunner{}
			if fail {
				f.failOn = func(args []string) bool { return len(args) > 0 && args[0] == "shell" }
				f.failErr = errors.New("ansible failed")
			}
			p := &Provisioner{Lima: lima.New(f), PlaybookDir: t.TempDir()}
			var captured string
			err := p.runProvision(context.Background(), "web", "finalize", "web", vm.CreateConfig{Name: "web"}, false, io.Discard, PreservationVars{OnSetupRevision: func(v string) { captured = v }})
			if fail && (err == nil || captured != "") {
				t.Fatalf("failed finalize claimed setup: captured=%q err=%v", captured, err)
			}
			if !fail && (err != nil || captured == "") {
				t.Fatalf("successful finalize lost setup revision: captured=%q err=%v", captured, err)
			}
		})
	}
}
