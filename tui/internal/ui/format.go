package ui

import "strconv"

// humanizeBytes turns a raw byte count — how Lima reports memory/disk in
// `list --format json`, e.g. "8589934592" — into a human size like "8 GiB". A
// value that is empty or not a plain integer is returned unchanged, so an
// already-formatted size string (the create form uses "8GiB") or a missing value
// passes through untouched. Binary units (1024) match Lima's GiB sizing.
func humanizeBytes(s string) string {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return s
	}
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < len(units)-1 {
		div *= unit
		exp++
	}
	str := strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64)
	if len(str) > 2 && str[len(str)-2:] == ".0" {
		str = str[:len(str)-2] // trim a trailing ".0" for whole sizes
	}
	return str + " " + units[exp]
}
