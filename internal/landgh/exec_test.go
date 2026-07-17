package landgh

import (
	"reflect"
	"testing"
)

func TestOpenerCommandByGOOS(t *testing.T) {
	cases := []struct {
		goos     string
		wantName string
		wantArgs []string
	}{
		{"darwin", "open", []string{"https://example.com/x"}},
		{"windows", "cmd", []string{"/c", "start", "", "https://example.com/x"}},
		{"linux", "xdg-open", []string{"https://example.com/x"}},
		{"freebsd", "xdg-open", []string{"https://example.com/x"}}, // unknown GOOS falls back to xdg-open
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			name, args := openerCommand(tc.goos, "https://example.com/x")
			if name != tc.wantName {
				t.Errorf("openerCommand(%q) name = %q, want %q", tc.goos, name, tc.wantName)
			}
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Errorf("openerCommand(%q) args = %v, want %v", tc.goos, args, tc.wantArgs)
			}
		})
	}
}
