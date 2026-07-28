package vm

import (
	"os"
	"strings"
)

// FallbackTimezone is the zone used when the host's timezone cannot be
// determined. It is also roles/base's default, and it is what a guest got
// before sand configured the timezone at all — the Debian/Ubuntu cloud images
// ship UTC — so falling back here is a no-op against the old behaviour rather
// than a new guess.
const FallbackTimezone = "Etc/UTC"

// HostTimezone reports the IANA zone name of the machine sand itself is running
// on ("America/Toronto"), which is what gets provisioned into the guest so a
// VM's clock, log timestamps, `ls -l`, and cron all agree with the developer
// watching them. Before this, every guest kept the cloud image's UTC.
//
// It deliberately does NOT go through time.LoadLocation: Go's time.Local
// exposes no IANA name (its String() is "Local" unless TZ named it), and the
// guest resolves the name against its OWN tzdata anyway — so all sand needs to
// carry across is the name. Nothing here consults /usr/share/zoneinfo on the
// host either: a macOS host provisioning a Debian guest is the normal case, and
// checking the wrong side's tzdata would reject a zone the guest has. The guest
// checks that it exists, where the answer is authoritative (roles/base's
// "Confirm the requested timezone exists in the guest").
//
// Sources, in order of authority:
//
//  1. $TZ — an explicit per-process override, and the one thing Go's own
//     time.Local honours above the system setting. If the user ran sand under
//     a TZ, that is the timezone they are reading their VM's clock in.
//  2. /etc/timezone — Debian/Ubuntu hosts record the plain name here.
//  3. The /etc/localtime symlink target — systemd's and macOS's source of
//     truth, and the only one of the three that exists on a Mac.
//
// A source that is missing, unreadable, or holds something that is not
// zone-name-shaped (a bare POSIX TZ spec like "<-03>3", say) falls through to
// the next; if all three do, the result is FallbackTimezone, which preserves
// today's UTC behaviour exactly.
func HostTimezone() string {
	for _, source := range []func() string{tzFromEnv, tzFromEtcTimezone, tzFromLocaltimeLink} {
		if name := source(); validZoneName(name) {
			return name
		}
	}
	return FallbackTimezone
}

// tzFromEnv reads $TZ. A leading colon is valid POSIX ("TZ=:America/Toronto")
// and means "the following is a file/zone name", so strip it rather than let it
// fail the name check.
func tzFromEnv() string {
	return strings.TrimPrefix(strings.TrimSpace(os.Getenv("TZ")), ":")
}

// tzFromEtcTimezone reads Debian/Ubuntu's plain-name file. Only the first line
// counts; the file is a single name but has been seen with trailing comments in
// the wild.
func tzFromEtcTimezone() string {
	data, err := os.ReadFile("/etc/timezone")
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(first)
}

// tzFromLocaltimeLink derives the name from where /etc/localtime points:
//
//	Linux   /etc/localtime -> /usr/share/zoneinfo/America/Toronto
//	macOS   /etc/localtime -> /var/db/timezone/zoneinfo/America/Toronto
//
// Splitting on the zoneinfo directory rather than trimming a fixed prefix is
// what makes both layouts — and a relative link like
// "../usr/share/zoneinfo/UTC" — resolve with one rule. Debian also ships a
// "zoneinfo.default" tree, so that spelling is accepted too.
//
// Note this reads the LINK, not the file: os.Readlink fails on a host where
// /etc/localtime is a regular copied file rather than a symlink, which is why
// this is the last source rather than the first.
func tzFromLocaltimeLink() string {
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	return zoneFromLinkTarget(target)
}

// zoneFromLinkTarget is the parsing half of tzFromLocaltimeLink, split out so
// the layouts above can be tested without depending on what /etc looks like on
// the machine running the tests. It returns "" for a target with no zoneinfo
// directory in it, so the caller falls through rather than inventing a zone
// name out of an unrelated path component.
func zoneFromLinkTarget(target string) string {
	for _, dir := range []string{"/zoneinfo/", "/zoneinfo.default/"} {
		if _, name, found := strings.Cut(target, dir); found {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

// validZoneName reports whether name is shaped like an IANA zone
// ("America/Argentina/Buenos_Aires", "UTC", "Etc/GMT+5").
//
// This is a gate, not a lookup: the name is interpolated into a guest-side
// symlink target under /usr/share/zoneinfo, so a value carrying "..", a leading
// slash, or a shell/YAML metacharacter must never reach the playbook — from
// $TZ, which is attacker-controllable in the way any environment variable is.
// The charset below is exactly what tzdata uses, which leaves no room for any
// of that.
func validZoneName(name string) bool {
	// Long enough for "America/Argentina/ComodRivadavia" with room to spare,
	// short enough that a runaway environment variable is not a symlink target.
	if name == "" || len(name) > 64 {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" {
			return false // leading, trailing, or doubled slash — and rejects ".." implicitly below
		}
		for _, r := range segment {
			switch {
			case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			case r == '_' || r == '-' || r == '+':
			default:
				return false // '.', '/', spaces, shell metacharacters, anything non-ASCII
			}
		}
	}
	return true
}
