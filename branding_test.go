package sandbar

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// legacyNameAllowed lists the string literals that must keep saying `sand`
// as a word, keyed by file: the compatibility symlink check and the warning
// it prints, and on-disk paths that existing installs already hold.
var legacyNameAllowed = map[string][]string{
	"cmd/sandbar/main.go": {
		"sand",
		"Warning: sand has been renamed to sandbar. The sand compatibility symlink will be removed in a future release; use sandbar instead.",
	},
	// ~/.cache/sand/apt-cache: the seeded apt archive cache, and CI's cache key.
	"internal/provision/aptcache.go": {"sand"},
}

// TestNoLegacyCommandNameInStrings guards the sand → sandbar rename. Host-side
// output — errors, help, progress and log lines — must name the command the
// user actually runs. The rename was done string by string, and a review
// still found a dozen messages saying `sand`; this check makes the next one
// fail here instead.
//
// Only a standalone word counts. Guest-side identifiers deliberately keep the
// old spelling (`~/.sand/clip`, `_sand/`, `sand-xclip`, `/var/log/sand-*`,
// `SAND_*`) because renaming them would break existing VMs, and every one of
// them is joined to a neighbouring `.`, `/`, `_`, `~` or `-`.
func TestNoLegacyCommandNameInStrings(t *testing.T) {
	skipDirs := map[string]bool{".git": true, ".ai": true, ".claude": true, "dist": true, "site": true, "node_modules": true, "testdata": true}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		allowed := legacyNameAllowed[filepath.ToSlash(path)]
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !hasLegacyWord(s) {
				return true
			}
			for _, a := range allowed {
				if s == a {
					return true
				}
			}
			t.Errorf("%s: string literal names the old command `sand`: %q", fset.Position(lit.Pos()), s)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// hasLegacyWord reports whether s contains "sand" (any case) as a word of its
// own, rather than as part of an identifier or path such as `sandbar`,
// `.sand/` or `sand-xclip`.
func hasLegacyWord(s string) bool {
	lower := strings.ToLower(s)
	for i := 0; ; {
		j := strings.Index(lower[i:], "sand")
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len("sand")
		before := start == 0 || !strings.ContainsRune(identChars+"./~", rune(lower[start-1]))
		after := end == len(lower) || !strings.ContainsRune(identChars, rune(lower[end])) ||
			proseCompound(lower[end:])
		if before && after {
			return true
		}
		i = end
	}
}

const identChars = "abcdefghijklmnopqrstuvwxyz0123456789_-"

// proseCompound reports whether rest (the text after "sand") is a hyphenated
// English compound such as "-managed" — the product name in prose, unlike an
// identifier such as "-xclip" or "-heartbeat".
func proseCompound(rest string) bool {
	for _, w := range []string{"-managed", "-created", "-owned", "-side", "-built"} {
		if strings.HasPrefix(rest, w) && (len(rest) == len(w) || !strings.ContainsRune(identChars, rune(rest[len(w)]))) {
			return true
		}
	}
	return false
}

func TestHasLegacyWord(t *testing.T) {
	for s, want := range map[string]bool{
		"upgrade sand":               true,
		"sand: cannot attach":        true,
		"Sand creates VMs":           true,
		"a sand-managed VM":          true,
		"sand-side state":            true,
		"sand-xclip":                 false,
		"---sand-heartbeat---":       false,
		"sandbar":                    false,
		"~/.sand/clip/latest.png":    false,
		"/var/log/sand-finalize.log": false,
		"_sand/images":               false,
		"SAND_KEEP_FAILED":           false,
		"thousands":                  false,
	} {
		if got := hasLegacyWord(s); got != want {
			t.Errorf("hasLegacyWord(%q) = %v, want %v", s, got, want)
		}
	}
}
