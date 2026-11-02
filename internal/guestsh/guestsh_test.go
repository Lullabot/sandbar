package guestsh

import (
	"slices"
	"strings"
	"testing"
)

// TestAttachArgvShape pins the three-element `bash -c <expr>` contract every
// backend wraps. A single element would be shell-escaped into one word by
// limactl and fail in the guest as `command not found`.
func TestAttachArgvShape(t *testing.T) {
	for _, mode := range []AttachMode{AttachFullScreen, AttachControl} {
		argv := AttachArgvMode("truecolor", mode)
		if len(argv) != 3 || argv[0] != "bash" || argv[1] != "-c" {
			t.Fatalf("mode %d: argv = %q, want [bash -c <expr>]", mode, argv)
		}
		if argv[2] != guestAttachExpr(colortermFlag("truecolor"), mode.flag()) {
			t.Errorf("mode %d: expression is not guestAttachExpr's", mode)
		}
	}
	if !slices.Equal(AttachArgv("24bit"), AttachArgvMode("24bit", AttachFullScreen)) {
		t.Error("AttachArgv must be the full-screen case of AttachArgvMode")
	}
	if strings.Contains(AttachArgv("")[2], "COLORTERM") {
		t.Error("an empty COLORTERM must not be forwarded")
	}
}

// TestColortermSafe pins that ColortermSafe and colortermFlag agree, so a
// wrapper forwarding COLORTERM another way (lima.RunArgv) drops exactly the
// values the attach drops.
func TestColortermSafe(t *testing.T) {
	for _, v := range []string{"truecolor", "24bit", "1", "gnome-terminal", "rxvt_x", "", "a b", "x;y", "$(id)", "'"} {
		if got, want := ColortermSafe(v), colortermFlag(v) != ""; got != want {
			t.Errorf("ColortermSafe(%q) = %v, but colortermFlag forwards it: %v", v, got, want)
		}
	}
}
