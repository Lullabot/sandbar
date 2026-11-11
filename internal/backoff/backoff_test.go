package backoff

import (
	"testing"
	"time"
)

// Doubling doubles per consecutive failure and stops at the ceiling, and a
// single failure is still retried at the base delay — the ordinary `limactl
// stop` must not be punished for the pathological case this exists for.
func TestDoublingDoublesToACeiling(t *testing.T) {
	base, max := 5*time.Second, 30*time.Second
	for _, tc := range []struct {
		n    int
		want time.Duration
	}{
		{0, 5 * time.Second}, // never happened; treated as the first
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{4, 30 * time.Second}, // clamped
		{9, 30 * time.Second},
	} {
		if got := Doubling(base, tc.n, max); got != tc.want {
			t.Errorf("Doubling(5s, %d, 30s) = %s, want %s", tc.n, got, tc.want)
		}
	}
	if got := Doubling(0, 3, max); got != 0 {
		t.Errorf("Doubling with no base = %s, want 0 (a poller with no retry delay)", got)
	}
}
