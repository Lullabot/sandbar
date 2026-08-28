package drupalorg

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeTokenFile writes contents to the conventional token path under an
// XDG_CONFIG_HOME rooted in t.TempDir(), setting the mode explicitly, and
// returns the path. It never touches the developer's real ~/.config.
func writeTokenFile(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	dir := filepath.Join(xdg, "sandbar")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "drupalorg.token")
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	// WriteFile's mode is subject to umask; force the exact mode under test.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return path
}

func TestTokenPath(t *testing.T) {
	t.Run("honours XDG_CONFIG_HOME when set", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
		got, err := TokenPath()
		if err != nil {
			t.Fatalf("TokenPath: %v", err)
		}
		want := filepath.Join("/xdg/config", "sandbar", "drupalorg.token")
		if got != want {
			t.Errorf("TokenPath() = %q, want %q", got, want)
		}
	})

	t.Run("falls back to ~/.config when unset", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home directory available: %v", err)
		}
		got, err := TokenPath()
		if err != nil {
			t.Fatalf("TokenPath: %v", err)
		}
		want := filepath.Join(home, ".config", "sandbar", "drupalorg.token")
		if got != want {
			t.Errorf("TokenPath() = %q, want %q", got, want)
		}
	})
}

func TestLoadTokenModeMatrix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not carry the same meaning on windows")
	}

	const secret = "super-secret-pat-value"

	cases := []struct {
		mode    os.FileMode
		refused bool
	}{
		{mode: 0o600, refused: false},
		{mode: 0o640, refused: true},
		{mode: 0o604, refused: true},
		{mode: 0o666, refused: true},
	}

	for _, tc := range cases {
		t.Run(tc.mode.String(), func(t *testing.T) {
			writeTokenFile(t, secret, tc.mode)

			got, err := LoadToken()
			if tc.refused {
				if err == nil {
					t.Fatalf("LoadToken() with mode %04o: got nil error, want refusal", tc.mode)
				}
				if strings.Contains(err.Error(), secret) {
					t.Errorf("LoadToken() error contains the token value: %q", err.Error())
				}
				if !strings.Contains(err.Error(), "chmod 600") {
					t.Errorf("LoadToken() error = %q, want it to mention chmod 600", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadToken() with mode %04o: unexpected error: %v", tc.mode, err)
			}
			if got != secret {
				t.Errorf("LoadToken() = %q, want %q", got, secret)
			}
		})
	}
}

func TestLoadTokenAbsentFile(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	_, err := LoadToken()
	if err == nil {
		t.Fatal("LoadToken() with no token file: got nil error")
	}
	if !errors.Is(err, ErrNoToken) {
		t.Errorf("LoadToken() error = %v, want errors.Is(err, ErrNoToken)", err)
	}
}

func TestLoadTokenEmptyOrWhitespace(t *testing.T) {
	for _, contents := range []string{"", "   \n\t  "} {
		t.Run("contents="+contents, func(t *testing.T) {
			writeTokenFile(t, contents, 0o600)

			_, err := LoadToken()
			if err == nil {
				t.Fatal("LoadToken() with empty/whitespace file: got nil error")
			}
			if errors.Is(err, ErrNoToken) {
				t.Error("LoadToken() with an existing but empty file should not report ErrNoToken")
			}
		})
	}
}

func TestLoadTokenTrimsWhitespace(t *testing.T) {
	const secret = "abc123"
	writeTokenFile(t, "  "+secret+"\n\n", 0o600)

	got, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got != secret {
		t.Errorf("LoadToken() = %q, want %q", got, secret)
	}
}

// TestLoadTokenNeverLeaksTokenInErrors exercises the error-returning paths
// not already leak-checked by TestLoadTokenModeMatrix (which covers the
// over-permissive-mode case with its own secret) and asserts the token text
// never appears in the returned error.
func TestLoadTokenNeverLeaksTokenInErrors(t *testing.T) {
	const secret = "leak-canary-token-value"

	t.Run("absent file", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", xdg)
		_, err := LoadToken()
		if err == nil {
			t.Fatal("expected error for absent file")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks token value: %q", err.Error())
		}
	})

	t.Run("empty file", func(t *testing.T) {
		writeTokenFile(t, "", 0o600)
		_, err := LoadToken()
		if err == nil {
			t.Fatal("expected error for empty file")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks token value: %q", err.Error())
		}
	})
}
