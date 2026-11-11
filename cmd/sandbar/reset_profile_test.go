package main

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provision"
)

// The case behind `--from-backup` answering "no such VM": with more than one
// profile enabled and the VM already gone, nothing on any profile says where it
// lived. The backup does.
func TestResumeProfileComesFromTheBackup(t *testing.T) {
	store := newTestStore(t)
	mini, err := store.Add(profiles.Profile{Name: "mini", Type: profiles.TypeRemoteSSH, Host: "mini.example.com", User: "dev", Port: 22, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	off, err := store.Add(profiles.Profile{Name: "off", Type: profiles.TypeRemoteSSH, Host: "off.example.com", User: "dev", Port: 22, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	backup := func(id, name string) string {
		m := &provision.ResetManifest{Schema: 1, Config: recordedVM(), Plan: provision.PreservePlan{WholeHome: true}, ProfileID: id, ProfileName: name}
		return backupDir(t, m, "home.tar")
	}

	cases := []struct {
		name    string
		dir     string
		profile string
		wantID  string
		wantErr string
	}{
		{"recorded profile", backup(mini.ID, mini.Name), "", mini.ID, ""},
		{"--profile wins", backup(mini.ID, mini.Name), "local", "", ""},
		{"no profile recorded", backup("", ""), "", "", ""},
		{"no manifest", backupDir(t, nil, "home.tar"), "", "", ""},
		{"profile deleted", backup("gone-id", "old-mac"), "", "", `"old-mac", which no longer exists`},
		{"profile disabled", backup(off.ID, off.Name), "", "", "disabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &resetOptions{fromBackup: tc.dir, profile: tc.profile}
			got, err := resumeProfile(store, o)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got.ID != tc.wantID {
				t.Errorf("resumeProfile = %q, %v; want %q", got.ID, err, tc.wantID)
			}
		})
	}
}
