package releasecheck

import (
	"strconv"
	"strings"
)

const releasesURL = "https://github.com/Lullabot/sandbar/releases"

type stableVersion [3]uint64

func parseStable(s string) (stableVersion, bool) {
	var version stableVersion
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version, false
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return version, false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return version, false
			}
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return version, false
		}
		version[i] = value
	}
	return version, true
}

// UpdateAvailable reports whether two stable release identities are comparable
// and the latest is numerically newer than the installed release.
func UpdateAvailable(installed, latest string) bool {
	current, ok := parseStable(installed)
	if !ok {
		return false
	}
	newest, ok := parseStable(latest)
	if !ok {
		return false
	}
	for i := range current {
		if newest[i] != current[i] {
			return newest[i] > current[i]
		}
	}
	return false
}

// ReleaseNotesURL links a released build to its tag. Source builds and
// malformed identities go to the releases index instead.
func ReleaseNotesURL(installed string) string {
	if _, ok := parseStable(installed); !ok {
		return releasesURL
	}
	return releasesURL + "/tag/v" + strings.TrimPrefix(installed, "v")
}

func tagNotesURL(tag string) string {
	if _, ok := parseStable(tag); !ok {
		return ""
	}
	return releasesURL + "/tag/" + tag
}
