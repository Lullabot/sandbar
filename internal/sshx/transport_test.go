package sshx

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

// Only ssh's own 255 is a transport failure, however deeply it is wrapped; a
// guest command's status (ansible's 2, say) is not.
func TestIsTransportFailure(t *testing.T) {
	exit := func(code int) error {
		return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"ssh 255", exit(255), true},
		{"wrapped 255", fmt.Errorf("finalize: %w", fmt.Errorf("limactl shell: %w", exit(255))), true},
		{"ansible failed task", exit(2), false},
		{"not an exit", errors.New("exit status 255"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := IsTransportFailure(tc.err); got != tc.want {
			t.Errorf("%s: IsTransportFailure = %v, want %v", tc.name, got, tc.want)
		}
	}
}
