package vm

import "testing"

// $TZ is the only source of HostTimezone that a test can drive without writing
// to /etc, so it carries the end-to-end assertions; the file-backed sources are
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
		{"single segment", "UTC", "UTC"},
		{"posix leading colon is stripped", ":Europe/Berlin", "Europe/Berlin"},
		{"surrounding whitespace is trimmed", "  Asia/Tokyo\n", "Asia/Tokyo"},
		{"plus and underscore are legal", "Etc/GMT+5", "Etc/GMT+5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TZ", tc.tz)
			if got := HostTimezone(); got != tc.want {
				t.Errorf("HostTimezone() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A malformed $TZ must not abort detection — it falls through to the file-backed
// sources, and on a host with neither, to FallbackTimezone. The assertion is
// deliberately "not the bad value" rather than "== FallbackTimezone": this test
// runs on a Linux box that HAS /etc/timezone, so source 2 legitimately answers.
func TestHostTimezone_RejectsMalformedEnv(t *testing.T) {
	for _, bad := range []string{
		"",                     // unset
		"/etc/localtime",       // absolute path
		"../../etc/shadow",     // traversal
		"America//Toronto",     // empty segment
		"<-03>3",               // bare POSIX TZ spec, not a zone name
		"America/Toronto; rm",  // shell metacharacters
		"America/Toronto\ntwo", // embedded newline
	} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("TZ", bad)
			got := HostTimezone()
			if got == bad && bad != "" {
				t.Errorf("HostTimezone() = %q, want the malformed value rejected", got)
			}
			if !validZoneName(got) {
				t.Errorf("HostTimezone() = %q, which is not a valid zone name", got)
			}
		})
	}
}

// The contract every caller depends on: whatever comes out is safe to
// interpolate into the playbook's /usr/share/zoneinfo symlink target.
func TestHostTimezone_AlwaysReturnsAValidName(t *testing.T) {
	t.Setenv("TZ", "")
	if got := HostTimezone(); !validZoneName(got) {
		t.Errorf("HostTimezone() = %q, which is not a valid zone name", got)
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
		if !validZoneName(name) {
			t.Errorf("validZoneName(%q) = false, want true", name)
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
		if validZoneName(name) {
			t.Errorf("validZoneName(%q) = true, want false", name)
		}
	}
}

// A zone name is a symlink target in the guest; an unbounded environment
// variable is not. The cap is its own case because it is the one rule with no
// natural example above.
func TestValidZoneName_RejectsOverlongNames(t *testing.T) {
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	if validZoneName(string(long)) {
		t.Error("validZoneName(65 chars) = true, want false")
	}
	if !validZoneName(string(long[:64])) {
		t.Error("validZoneName(64 chars) = false, want true — the cap is inclusive")
	}
}

// tzFromLocaltimeLink has to cope with three real layouts, so parse them
// directly rather than only through whatever this build host happens to be.
func TestParseLocaltimeLinkLayouts(t *testing.T) {
	// The parsing rule under test is "everything after the zoneinfo dir", which
	// is what makes Linux, macOS, and relative links resolve identically.
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

// The exported constant is what roles/base/defaults/main.yml's base_timezone
// must agree with; a mismatch would make the Go fallback and the playbook
// fallback two different timezones.
func TestFallbackTimezoneIsUTC(t *testing.T) {
	if FallbackTimezone != "Etc/UTC" {
		t.Errorf("FallbackTimezone = %q, want Etc/UTC to match roles/base/defaults/main.yml", FallbackTimezone)
	}
	if !validZoneName(FallbackTimezone) {
		t.Errorf("FallbackTimezone = %q, which is not a valid zone name", FallbackTimezone)
	}
}
