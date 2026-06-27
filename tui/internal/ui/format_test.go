package ui

import "testing"

func TestHumanizeBytes(t *testing.T) {
	cases := map[string]string{
		"8589934592":   "8 GiB",   // Lima's typical memory report
		"107374182400": "100 GiB", // 100 GiB disk
		"1073741824":   "1 GiB",
		"536870912":    "512 MiB",
		"1610612736":   "1.5 GiB", // non-whole keeps one decimal
		"2048":         "2 KiB",
		"":             "",     // missing → unchanged
		"8GiB":         "8GiB", // already a size string → unchanged
		"0":            "0",    // non-positive → unchanged
		"not-a-number": "not-a-number",
	}
	for in, want := range cases {
		if got := humanizeBytes(in); got != want {
			t.Errorf("humanizeBytes(%q) = %q, want %q", in, got, want)
		}
	}
}
