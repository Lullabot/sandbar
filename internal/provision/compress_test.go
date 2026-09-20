package provision

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTarDecompressFlag(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"zstd", append(zstdMagic, 0x00, 0x01), "--zstd"},
		{"gzip", append(gzipMagic, 0x08, 0x00), "-z"},
		{"plain tar", []byte("ustar-ish nonsense"), ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.tar")
			if err := os.WriteFile(path, tc.head, 0o600); err != nil {
				t.Fatalf("seed archive: %v", err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer f.Close()
			if got := tarDecompressFlag(f); got != tc.want {
				t.Errorf("tarDecompressFlag(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestTarDecompressFlagLeavesTheOffsetAlone: StageIn sniffs the archive and then
// hands the SAME open file to the guest as the extract's stdin. A sniff that
// consumed the magic bytes would feed the guest a headless stream and fail the
// restore after the VM it is restoring into has already been rebuilt.
func TestTarDecompressFlagLeavesTheOffsetAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.tar")
	body := append(append([]byte{}, zstdMagic...), []byte("payload")...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	if got := tarDecompressFlag(f); got != "--zstd" {
		t.Fatalf("tarDecompressFlag = %q, want --zstd", got)
	}
	rest, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	streamed := make([]byte, len(rest))
	n, _ := f.Read(streamed)
	if !bytes.Equal(streamed[:n], body) {
		t.Errorf("the archive stream started at %q, want the whole file from byte 0", streamed[:n])
	}
}

// TestStagingArchiveRoundTripsThroughRealTar runs the argv StageOut and StageIn
// actually build against the real tar and zstd binaries.
//
// A fake runner proves what argv was CONSTRUCTED; it cannot prove that tar
// accepts it. "-I" with arguments in one word, and a zstd archive extracted from
// a PIPE (which is where GNU tar stops auto-detecting and demands the flag), are
// exactly the kind of thing that is right in a test double and wrong on a guest
// — and the guest is on the far side of a destroyed VM.
func TestStagingArchiveRoundTripsThroughRealTar(t *testing.T) {
	tarBin, err := exec.LookPath("tar")
	if err != nil {
		t.Skip("no tar on this host")
	}

	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "file.txt"), []byte("preserved\n"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	for _, codec := range []struct {
		name  string
		flags []string
	}{
		{"zstd", []string{"-I", zstdCompressProgram}},
		{"gzip", []string{"-z"}},
	} {
		t.Run(codec.name, func(t *testing.T) {
			if codec.name == "zstd" {
				if _, err := exec.LookPath("zstd"); err != nil {
					t.Skip("no zstd on this host")
				}
			}

			// Stage out: the same shape StageOut builds, minus the sudo a guest needs.
			argv := append([]string{"-C", src, "--ignore-failed-read"}, codec.flags...)
			argv = append(argv, "-cf", "-", "sub")
			var archive, createErr bytes.Buffer
			create := exec.CommandContext(context.Background(), tarBin, argv...)
			create.Stdout = &archive
			create.Stderr = &createErr
			if err := create.Run(); err != nil {
				t.Fatalf("tar create %v: %v (%s)", argv, err, createErr.String())
			}

			path := filepath.Join(t.TempDir(), "extras.tar")
			if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
				t.Fatalf("write archive: %v", err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open archive: %v", err)
			}
			defer f.Close()

			// Stage in: the flag comes from the archive itself, and the archive
			// arrives on stdin, as it does over a guest shell.
			dest := t.TempDir()
			extract := []string{"-C", dest}
			flag := tarDecompressFlag(f)
			if flag == "" {
				t.Fatalf("a %s archive sniffed as no known format", codec.name)
			}
			extract = append(extract, flag, "-xf", "-")
			cmd := exec.CommandContext(context.Background(), tarBin, extract...)
			cmd.Stdin = f
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("tar extract %v: %v (%s)", extract, err, out)
			}

			got, err := os.ReadFile(filepath.Join(dest, "sub", "file.txt"))
			if err != nil {
				t.Fatalf("restored file: %v", err)
			}
			if string(got) != "preserved\n" {
				t.Errorf("restored %q, want %q", got, "preserved\n")
			}
		})
	}
}
