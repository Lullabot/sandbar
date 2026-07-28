package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// $TZ is the only source of HostTimezone a test can drive without writing to
// /etc, so it carries the end-to-end assertions; the other sources are
// exercised through their own helpers below. Every test here sets TZ explicitly
// (t.Setenv, so it is restored) — including to "" — because the developer's own
// TZ would otherwise decide the result.
func TestHostTimezone_FromEnv(t *testing.T) {
	cases := []struct {
		name string
		tz   string
		want string
	}{
		{"plain name", "America/Toronto", "America/Toronto"},
		{"three-part name", "America/Argentina/Salta", "America/Argentina/Salta"},
		{"posix leading colon is stripped", ":Europe/Berlin", "Europe/Berlin"},
		{"surrounding whitespace is trimmed", "  Asia/Tokyo\n", "Asia/Tokyo"},
		{"plus and underscore are legal", "Etc/GMT+5", "Etc/GMT+5"},
		// The POSIX path form, which Go's own time.Local honours. Both the
		// colon-prefixed and bare spellings must resolve to the zone name
		// rather than falling through to the system zone.
		{"path form with colon", ":/usr/share/zoneinfo/Asia/Tokyo", "Asia/Tokyo"},
		{"path form without colon", "/usr/share/zoneinfo/Australia/Perth", "Australia/Perth"},
		{"macOS path form", "/var/db/timezone/zoneinfo/Europe/Lisbon", "Europe/Lisbon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TZ", tc.tz)
			got, detected := HostTimezone()
			if got != tc.want {
				t.Errorf("HostTimezone() = %q, want %q", got, tc.want)
			}
			if !detected {
				t.Errorf("HostTimezone() reported not-detected for %q", tc.tz)
			}
		})
	}
}

// POSIX and Go both read a SET-but-empty TZ as UTC. Treating it as "unset" and
// falling through to /etc/timezone would provision the guest in the host's
// system zone while every timestamp in the user's own terminal is UTC — the
// precise mismatch this feature exists to remove.
func TestHostTimezone_EmptyTZMeansUTC(t *testing.T) {
	t.Setenv("TZ", "")
	got, detected := HostTimezone()
	if got != FallbackTimezone {
		t.Errorf("HostTimezone() = %q for TZ=\"\", want %q (POSIX: empty TZ is UTC)", got, FallbackTimezone)
	}
	if !detected {
		t.Error("HostTimezone() reported not-detected for TZ=\"\"; an empty TZ is an explicit choice of UTC, not an absent answer")
	}
}

// A malformed $TZ must not abort detection — it falls through to the
// file-backed sources, and on a host with neither, to FallbackTimezone. The
// assertion is deliberately "not the bad value" rather than "== FallbackTimezone":
// this runs on a Linux box that HAS /etc/timezone, so source 2 legitimately answers.
func TestHostTimezone_RejectsMalformedEnv(t *testing.T) {
	for _, bad := range []string{
		"/etc/localtime",       // absolute path outside any zoneinfo tree
		"../../etc/shadow",     // traversal
		"America//Toronto",     // empty segment
		"<-03>3",               // bare POSIX TZ spec, not a zone name
		"America/Toronto; rm",  // shell metacharacters
		"America/Toronto\ntwo", // embedded newline
	} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("TZ", bad)
			got, _ := HostTimezone()
			if got == bad {
				t.Errorf("HostTimezone() = %q, want the malformed value rejected", got)
			}
			if !ValidZoneName(got) {
				t.Errorf("HostTimezone() = %q, which is not a valid zone name", got)
			}
		})
	}
}

// The contract every caller depends on: whatever comes out is safe to
// interpolate into the playbook's /usr/share/zoneinfo symlink target.
func TestHostTimezone_AlwaysReturnsAValidName(t *testing.T) {
	t.Setenv("TZ", "not a zone")
	got, _ := HostTimezone()
	if !ValidZoneName(got) {
		t.Errorf("HostTimezone() = %q, which is not a valid zone name", got)
	}
}

// canonicalZone is what stops a host-side spelling the guest lacks from hard
// failing a create. The right//posix/ trees are pure string work, so they are
// asserted directly; the symlink-following arm needs a real tzdata tree and is
// covered by the alias test below when the host has one.
func TestCanonicalZone_StripsLeapSecondTrees(t *testing.T) {
	for input, want := range map[string]string{
		"right/America/Toronto": "America/Toronto",
		"posix/Asia/Tokyo":      "Asia/Tokyo",
		"America/Toronto":       "America/Toronto", // already canonical, untouched
	} {
		if got := canonicalZone(input); got != want {
			t.Errorf("canonicalZone(%q) = %q, want %q", input, got, want)
		}
	}
}

// A legacy alias is a SYMLINK inside the host's tzdata tree, so following it
// yields the canonical name the guest actually has. Skipped rather than failed
// where the host has no such alias — the point is the resolution rule, and CI
// runners vary in whether tzdata-legacy is installed.
func TestCanonicalZone_ResolvesLegacyAliasesViaHostTzdata(t *testing.T) {
	var alias, want string
	for a, w := range map[string]string{
		"US/Eastern":     "America/New_York",
		"Canada/Eastern": "America/Toronto",
		"Japan":          "Asia/Tokyo",
		"UTC":            "Etc/UTC", // present even without tzdata-legacy
	} {
		if fi, err := os.Lstat("/usr/share/zoneinfo/" + a); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			alias, want = a, w
			break
		}
	}
	if alias == "" {
		t.Skip("no symlinked zone alias on this host to resolve")
	}
	if got := canonicalZone(alias); got != want {
		t.Errorf("canonicalZone(%q) = %q, want %q", alias, got, want)
	}
}

// canonicalZone builds a filesystem path out of its input, so it must never be
// handed one that escapes the zoneinfo tree.
func TestCanonicalZone_RefusesToPathBuildFromAMalformedName(t *testing.T) {
	for _, bad := range []string{"../../etc/hostname", "/etc/hostname", ""} {
		if got := canonicalZone(bad); got != bad {
			t.Errorf("canonicalZone(%q) = %q, want it returned untouched for the caller to reject", bad, got)
		}
	}
}

func TestValidZoneName(t *testing.T) {
	valid := []string{
		"UTC",
		"Etc/UTC",
		"Etc/GMT+5",
		"America/Toronto",
		"America/Argentina/Buenos_Aires",
		"America/Port-au-Prince",
	}
	for _, name := range valid {
		if !ValidZoneName(name) {
			t.Errorf("ValidZoneName(%q) = false, want true", name)
		}
	}

	invalid := []string{
		"",                      // empty
		"/America/Toronto",      // leading slash
		"America/Toronto/",      // trailing slash
		"America//Toronto",      // doubled slash
		"..",                    // traversal on its own
		"America/../../etc/pwd", // traversal buried mid-name
		".",                     // a dot is not a segment charset member
		"America/Toronto\n",     // trailing newline
		"America Toronto",       // space
		"America/Toronto;id",    // shell metacharacter
		"America/Toronto$(id)",  // command substitution
		"Amérique/Toronto",      // non-ASCII
		"'America/Toronto'",     // quotes
	}
	for _, name := range invalid {
		if ValidZoneName(name) {
			t.Errorf("ValidZoneName(%q) = true, want false", name)
		}
	}
}

// A zone name is a symlink target in the guest; an unbounded environment
// variable is not. The cap is its own case because it is the one rule with no
// natural example above.
func TestValidZoneName_RejectsOverlongNames(t *testing.T) {
	long := strings.Repeat("a", 65)
	if ValidZoneName(long) {
		t.Error("ValidZoneName(65 chars) = true, want false")
	}
	if !ValidZoneName(long[:64]) {
		t.Error("ValidZoneName(64 chars) = false, want true — the cap is inclusive")
	}
}

// zoneFromLinkTarget has to cope with three real layouts, so parse them
// directly rather than only through whatever this build host happens to be.
func TestParseLocaltimeLinkLayouts(t *testing.T) {
	cases := map[string]string{
		"/usr/share/zoneinfo/America/Toronto":         "America/Toronto",
		"/var/db/timezone/zoneinfo/America/Vancouver": "America/Vancouver",
		"../usr/share/zoneinfo/Etc/UTC":               "Etc/UTC",
		"/usr/share/zoneinfo.default/Asia/Kolkata":    "Asia/Kolkata",
	}
	for target, want := range cases {
		if got := zoneFromLinkTarget(target); got != want {
			t.Errorf("zoneFromLinkTarget(%q) = %q, want %q", target, got, want)
		}
	}

	// A target with no zoneinfo directory in it yields nothing, so HostTimezone
	// falls through rather than inventing a name from a path component.
	for _, target := range []string{"/etc/localtime", "", "/usr/share/posix/UTC"} {
		if got := zoneFromLinkTarget(target); got != "" {
			t.Errorf("zoneFromLinkTarget(%q) = %q, want empty", target, got)
		}
	}
}

// The Go fallback and the playbook's own default have to be the SAME zone, or a
// direct `ansible-playbook` run and a `sand create` on an undetectable host
// would put the guest in two different places. Read the YAML rather than
// restate the constant: comparing a Go literal to a Go constant would stay
// green while roles/base/defaults/main.yml drifted, which is precisely the
// mismatch this is here to catch.
func TestFallbackTimezoneMatchesPlaybookDefault(t *testing.T) {
	path := filepath.Join("..", "..", "roles", "base", "defaults", "main.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var got string
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "base_timezone:"); ok {
			got = strings.TrimSpace(rest)
			break
		}
	}
	if got == "" {
		t.Fatalf("no base_timezone key found in %s; the Go fallback %q now has nothing to agree with", path, FallbackTimezone)
	}
	if got != FallbackTimezone {
		t.Errorf("%s sets base_timezone: %s, but vm.FallbackTimezone is %q — a guest would land in a different zone depending on which path provisioned it", path, got, FallbackTimezone)
	}
	if !ValidZoneName(FallbackTimezone) {
		t.Errorf("FallbackTimezone = %q, which is not a valid zone name", FallbackTimezone)
	}
}
