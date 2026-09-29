package baseimage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type archRunner struct {
	output string
	err    error
}

func (r archRunner) HostOutput(context.Context, ...string) ([]byte, error) {
	return []byte(r.output), r.err
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
		got, err := RemoteArch(context.Background(), archRunner{output: input})
		if err != nil || got != want {
			t.Fatalf("RemoteArch(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := RemoteArch(context.Background(), archRunner{output: "mips\n"}); err == nil || !strings.Contains(err.Error(), "mips") {
		t.Fatalf("unsupported remote arch error = %v", err)
	}
	probeErr := errors.New("host unavailable")
	if _, err := RemoteArch(context.Background(), archRunner{err: probeErr}); !errors.Is(err, probeErr) {
		t.Fatalf("remote probe error = %v, want wrapped %v", err, probeErr)
	}

	local, err := LocalArch()
	if err != nil {
		t.Fatalf("LocalArch: %v", err)
	}
	if _, err := PinnedManifest.ForArch(local); err != nil {
		t.Fatalf("local architecture %q has no pinned image: %v", local, err)
	}
}

func TestImageEntryValidation(t *testing.T) {
	valid := ImageEntry{
		URL:      "https://example.invalid/image.qcow2",
		Filename: "image.qcow2",
		SHA256:   strings.Repeat("a", 64),
		Size:     1,
	}
	if err := valid.valid(); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}

	tests := []struct {
		name  string
		entry ImageEntry
	}{
		{name: "path filename", entry: ImageEntry{Filename: "dir/image.qcow2", SHA256: valid.SHA256, URL: valid.URL, Size: valid.Size}},
		{name: "bad digest", entry: ImageEntry{Filename: valid.Filename, SHA256: "xyz", URL: valid.URL, Size: valid.Size}},
		{name: "missing URL", entry: ImageEntry{Filename: valid.Filename, SHA256: valid.SHA256, Size: valid.Size}},
		{name: "empty image", entry: ImageEntry{Filename: valid.Filename, SHA256: valid.SHA256, URL: valid.URL}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.entry.valid(); err == nil {
				t.Fatalf("valid() accepted %+v", tt.entry)
			}
		})
	}
}
