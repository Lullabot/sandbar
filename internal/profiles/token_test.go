package profiles

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLoadTokenRejectsGroupOrOtherReadable is the core security property of
// LoadToken: a token file readable by anyone but the owner must be refused
// outright, not merely warned about. Permission bits are not meaningfully
// enforceable on Windows, so the assertion is guarded like the existing
// runtime.GOOS pattern in internal/ui/diskusage_test.go.
func TestLoadTokenRejectsGroupOrOtherReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	_, err := LoadToken(path)
	if err == nil {
		t.Fatal("LoadToken() on a 0644 file: want error, got nil")
	}
	if !strings.Contains(err.Error(), "0644") && !strings.Contains(err.Error(), "permission") && !strings.Contains(err.Error(), "group or other") {
		t.Errorf("LoadToken() error = %q, want it to mention the permission problem", err.Error())
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Error("LoadToken() error must never include the file contents")
	}
}

// TestLoadTokenAcceptsOwnerOnlyMode confirms the counterpart: a 0600 file is
// read successfully and the token is trimmed of surrounding whitespace.
func TestLoadTokenAcceptsOwnerOnlyMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("  s3cr3t-token  \n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := LoadToken(path)
	if err != nil {
		t.Fatalf("LoadToken() error = %v", err)
	}
	if got != "s3cr3t-token" {
		t.Errorf("LoadToken() = %q, want %q", got, "s3cr3t-token")
	}
}

// TestLoadTokenMissingFileErrors confirms a missing path surfaces a clear
// error rather than a bare os error, and never confuses the "not found" case
// with the permission case.
func TestLoadTokenMissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	_, err := LoadToken(path)
	if err == nil {
		t.Fatal("LoadToken() on a missing file: want error, got nil")
	}
}

// TestLoadTokenEmptyFileErrors confirms an empty (or whitespace-only) token
// file is rejected rather than silently returning "".
func TestLoadTokenEmptyFileErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadToken(path)
	if err == nil {
		t.Fatal("LoadToken() on an empty/whitespace-only file: want error, got nil")
	}
}

// TestLoadTokenExpandsHome confirms a leading "~/" is expanded against
// os.UserHomeDir(), mirroring how identity_path is handled elsewhere.
func TestLoadTokenExpandsHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(home, ".config", "pve-token")
	if err := os.WriteFile(path, []byte("home-token"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := LoadToken("~/.config/pve-token")
	if err != nil {
		t.Fatalf("LoadToken() error = %v", err)
	}
	if got != "home-token" {
		t.Errorf("LoadToken() = %q, want %q", got, "home-token")
	}
}
