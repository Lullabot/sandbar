package baseimage

import (
	"context"
	"strings"
	"testing"
)

type archRunner struct{ output string }

func (r archRunner) HostOutput(context.Context, ...string) ([]byte, error) {
	return []byte(r.output), nil
}

func TestManifestLookupAndHostArchitectures(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		entry, err := PinnedManifest.ForArch(arch)
		if err != nil || entry.URL == "" || entry.Filename == "" || len(entry.SHA256) != 64 {
			t.Fatalf("ForArch(%q) = %+v, %v", arch, entry, err)
		}
	}
	if _, err := PinnedManifest.ForArch("riscv64"); err == nil || !strings.Contains(err.Error(), "riscv64") {
		t.Fatalf("unsupported arch error = %v", err)
	}
	for input, want := range map[string]string{"x86_64\n": "amd64", "aarch64\n": "arm64"} {
		got, err := RemoteArch(context.Background(), archRunner{input})
		if err != nil || got != want {
			t.Fatalf("RemoteArch(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := RemoteArch(context.Background(), archRunner{"mips\n"}); err == nil || !strings.Contains(err.Error(), "mips") {
		t.Fatalf("unsupported remote arch error = %v", err)
	}
}
