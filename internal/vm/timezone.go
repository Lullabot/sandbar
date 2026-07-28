package vm

import (
	"fmt"
	"os"
	"path"
	"strings"
)

// FallbackTimezone is the zone used when the host's timezone cannot be
// determined. It is also roles/base's default, and it is what a guest got
// before sand configured the timezone at all — the Debian/Ubuntu cloud images
// ship UTC — so falling back here is a no-op against the old behaviour rather
// than a new guess.
const FallbackTimezone = "Etc/UTC"

// zoneinfoRoots are the tzdata trees this host might have: the Linux location
// first, then macOS's. Used only to CANONICALIZE a detected name (see
// canonicalZone) — never to decide whether a zone is acceptable, which only the
// guest can answer.
var zoneinfoRoots = []string{"/usr/share/zoneinfo/", "/var/db/timezone/zoneinfo/"}

// HostTimezone reports the IANA zone name of the machine sand itself is running
// on ("America/Toronto"), which is what gets provisioned into the guest so a
// VM's clock, log timestamps, `ls -l`, and cron all agree with the developer
// watching them. Before this, every guest kept the cloud image's UTC.
//
// The second return value reports whether the zone was actually DETECTED. When
// it is false the name is FallbackTimezone, chosen because nothing on this host
// would say — which is a materially different situation from a host that really
// is on UTC, and callers surface it as such rather than silently promising a
// match they did not achieve.
//
// It deliberately does NOT go through time.LoadLocation: Go's time.Local
// exposes no IANA name (its String() is "Local" unless TZ named it), and the
// guest resolves the name against its OWN tzdata anyway — so all sand needs to
// carry across is the name.
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
// zone-name-shaped falls through to the next; if all three do, the result is
// (FallbackTimezone, false), which preserves today's UTC behaviour exactly.
func HostTimezone() (string, bool) {
	for _, source := range []func() string{tzFromEnv, tzFromEtcTimezone, tzFromLocaltimeLink} {
		if name := canonicalZone(source()); ValidZoneName(name) {
			return name, true
		}
	}
	return FallbackTimezone, false
}

// tzFromEnv reads $TZ, in each of the three forms POSIX and Go's own time
// package accept:
//
//	TZ=America/Toronto                       plain zone name
//	TZ=:America/Toronto                      leading colon — "what follows names a zone"
//	TZ=:/usr/share/zoneinfo/America/Toronto  a PATH to the zone file
//
// An empty-but-SET TZ is not "unset": POSIX (and Go) read it as UTC, and that
// is the zone the user's own terminal is printing timestamps in, so it must not
// fall through to the system zone the user has explicitly overridden.
func tzFromEnv() string {
	raw, ok := os.LookupEnv("TZ")
	if !ok {
		return "" // genuinely unset: fall through to the system sources
	}
	value := strings.TrimPrefix(strings.TrimSpace(raw), ":")
	if value == "" {
		return FallbackTimezone
	}
	// The path form carries a zoneinfo directory; the same rule that reads
	// /etc/localtime's target reads this.
	if name := zoneFromLinkTarget(value); name != "" {
		return name
	}
	return value
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

// zoneFromLinkTarget extracts a zone name from a path into a tzdata tree:
//
//	/usr/share/zoneinfo/America/Toronto         -> America/Toronto
//	/var/db/timezone/zoneinfo/America/Vancouver -> America/Vancouver
//	../usr/share/zoneinfo/Etc/UTC               -> Etc/UTC
//
// Splitting on the zoneinfo directory rather than trimming a fixed prefix is
// what makes both OS layouts — and a relative link — resolve with one rule.
// Debian also ships a "zoneinfo.default" tree, so that spelling is accepted
// too. Returns "" for a path with no zoneinfo directory in it, so the caller
// falls through rather than inventing a zone name out of an unrelated path
// component.
func zoneFromLinkTarget(target string) string {
	for _, dir := range []string{"/zoneinfo/", "/zoneinfo.default/"} {
		if _, name, found := strings.Cut(target, dir); found {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

// canonicalZone rewrites a detected name into the one the GUEST is most likely
// to have, and is the difference between "sand create suddenly fails on a host
// that worked yesterday" and it just working.
//
// Two host-side spellings are shape-valid, in daily use, and absent from a
// stock guest's tzdata:
//
//   - Legacy aliases — "US/Eastern", "Canada/Eastern", "Japan", "EST5EDT".
//     Modern Debian moved these to the separate tzdata-legacy package, which
//     the base image does not install. On a host that HAS them they are
//     symlinks into the canonical tree, so following one link yields the name
//     the guest does have. That is exact, and needs no alias table to drift.
//   - The right/ and posix/ parallel trees (leap-second variants). Their leaf
//     name IS the canonical zone, so the prefix is simply dropped.
//
// This is the one place that reads the host's own tzdata, and only ever to
// IMPROVE a name: anything unreadable leaves the input untouched, so a macOS
// host provisioning a Debian guest — where the two trees legitimately differ —
// is never made worse off. A name that survives this and still isn't in the
// guest is handled there, gracefully; see roles/base/tasks/main.yml.
func canonicalZone(name string) string {
	if name == "" {
		return ""
	}
	for _, tree := range []string{"right/", "posix/"} {
		name = strings.TrimPrefix(name, tree)
	}
	if !ValidZoneName(name) {
		return name // let the caller reject it; never build a path out of it
	}
	for _, root := range zoneinfoRoots {
		full := root + name
		target, err := os.Readlink(full)
		if err != nil {
			continue // not a link (already canonical), or no such tree on this host
		}
		if !path.IsAbs(target) {
			target = path.Join(path.Dir(full), target)
		}
		if canonical := zoneFromLinkTarget(target); ValidZoneName(canonical) {
			return canonical
		}
	}
	return name
}

// ValidZoneName reports whether name is shaped like an IANA zone
// ("America/Argentina/Buenos_Aires", "UTC", "Etc/GMT+5").
//
// This is a gate, not a lookup — only the guest can say whether a zone really
// exists — and it is the boundary that keeps a hostile value out of the
// playbook. The name is interpolated into a guest-side symlink target under
// /usr/share/zoneinfo, so a value carrying "..", a leading slash, or a
// shell/YAML metacharacter must never reach it: "../../../etc/hostname" would
// otherwise resolve to a real, readable file, pass the guest's existence check,
// and leave /etc/localtime pointing at something that is not zone data — at
// which point glibc silently falls back to UTC, which is exactly the bug this
// feature exists to fix. Sources include $TZ and the --timezone flag, so this
// runs on EVERY value, detected or supplied (see CreateConfig.Validate).
//
// The charset below is exactly what tzdata uses, which leaves no room for any
// of that.
func ValidZoneName(name string) bool {
	// Long enough for "America/Argentina/ComodRivadavia" with room to spare,
	// short enough that a runaway environment variable is not a symlink target.
	if name == "" || len(name) > 64 {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" {
			return false // leading, trailing, or doubled slash
		}
		for _, r := range segment {
			switch {
			case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			case r == '_' || r == '-' || r == '+':
			default:
				return false // '.' (so ".." too), '/', spaces, metacharacters, non-ASCII
			}
		}
	}
	return true
}

// validateTimezone is Validate's timezone arm. An empty value is allowed and
// means "roles/base's own default stands" (BuildExtraVars omits the var
// entirely), which is what a caller predating the field produces.
func validateTimezone(name string) error {
	if name == "" || ValidZoneName(name) {
		return nil
	}
	return fmt.Errorf("timezone %q is not a valid IANA zone name: expected something like America/Toronto or Etc/UTC (letters, digits, '_', '-', '+', separated by '/')", name)
}
