package landgh

import (
	"os"
	"path/filepath"
	"strings"
)

// opplugin.go teaches the gh Runner about the 1Password shell plugin, which a
// good share of this tool's audience uses to hold their GitHub token.
//
// # Why this needs any code at all
//
// The plugin works by having the user's shell rc define `gh` as an ALIAS (or
// function) expanding to `op plugin run -- gh ...`. sand execs gh directly,
// argv-only, never through a shell (see the Runner contract in landgh.go), so
// that alias is invisible to it: the bare `gh` binary it finds on PATH holds
// no credential, `gh auth status` exits non-zero, and Landing correctly — but
// unhelpfully — reported "not authenticated" to someone whose gh works fine at
// their own prompt.
//
// # Why supporting it does NOT weaken the no-shell rule
//
// `op plugin run -- gh <args...>` is an ORDINARY argv invocation; the alias is
// only sugar over it, and 1Password documents running it directly for exactly
// this "subshells don't inherit aliases" case. So the fix is to prepend two
// argv elements, NOT to introduce a shell. Every argument still reaches gh as
// its own element, and a branch name containing `;` or `$(...)` stays as inert
// as it was before — which matters because those strings come from a sweep of
// the guest, the lowest-trust source in the system.
//
// # Why detection is file-only
//
// `op plugin run` can require interactive authorization (biometric, or a
// desktop-app approval), and probing for the plugin by RUNNING something is
// what would make that prompt fire at an arbitrary moment — underneath a
// full-screen TUI that owns the terminal. Detection here therefore reads
// config files and nothing else: no subprocess, no prompt, no chance of
// corrupting the display. The actual gh calls that follow are already safe by
// construction (Runner.Output gives the child no stdin and captures its
// stdout, so it can never draw on sand's terminal, and every call is
// context-bounded).

// opPluginConfig is where `op plugin init` writes the shell alias/function it
// tells the user to source. Its presence, mentioning gh, is the signal that a
// gh plugin was configured for this user.
const opPluginConfig = ".config/op/plugins.sh"

// ghCommand is the argv prefix a gh invocation should use: just {"gh"}
// normally, or {"op", "plugin", "run", "--", "gh"} when the 1Password gh
// plugin is what holds this user's token.
//
// env is the process environment (os.Environ-shaped "K=V" entries), home the
// user's home directory, and lookPath the PATH resolver — all injected so the
// decision is a pure function that tests drive without touching a real
// filesystem or a real op binary.
func ghCommand(env []string, home string, lookPath func(string) (string, error), readFile func(string) ([]byte, error)) []string {
	plain := []string{"gh"}

	// An explicit token in sand's own environment wins outright: gh reads
	// GH_TOKEN/GITHUB_TOKEN directly, so plain gh already works and routing
	// through op would only add a dependency (and a possible prompt) for
	// nothing. This is also the documented escape hatch for anyone who does
	// not want sand touching op at all.
	if envHas(env, "GH_TOKEN") || envHas(env, "GITHUB_TOKEN") {
		return plain
	}
	if home == "" {
		return plain
	}
	if _, err := lookPath("op"); err != nil {
		return plain
	}
	b, err := readFile(filepath.Join(home, opPluginConfig))
	if err != nil {
		return plain
	}
	if !mentionsGhPlugin(string(b)) {
		return plain
	}
	// The `--` is not optional here even though 1Password's examples sometimes
	// omit it: without it, a gh flag like `--json` would be parsed by op.
	return []string{"op", "plugin", "run", "--", "gh"}
}

// mentionsGhPlugin reports whether an `op plugin init`-generated config
// defines gh. The file holds one alias/function per configured plugin, so this
// looks for gh specifically rather than treating any op config as a gh one —
// a user with only a `doctl` or `aws` plugin must still get plain gh.
func mentionsGhPlugin(s string) bool {
	// Two independent signals, because op has emitted two shapes and they put
	// the pieces on different lines:
	//
	//	alias gh="op plugin run -- gh"          # one line
	//	gh() {                                  # ...or a function, where the
	//	  op plugin run -- gh "$@"              #    declaration and the body
	//	}                                       #    are separate lines
	//
	// So look for a gh DECLARATION and for the op invocation anywhere in the
	// file, rather than insisting both appear on one line.
	var declaresGh, invokesOp bool
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "op plugin run") {
			invokesOp = true
		}
		if strings.HasPrefix(line, "gh()") || strings.HasPrefix(line, "gh ()") ||
			strings.HasPrefix(line, "alias gh=") || strings.HasPrefix(line, "alias gh ") {
			declaresGh = true
		}
	}
	return declaresGh && invokesOp
}

// envHas reports whether key is set to a non-empty value in an os.Environ
// -shaped slice.
func envHas(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimSpace(e[len(prefix):]) != ""
		}
	}
	return false
}

// resolveGhCommand is ghCommand wired to the real process environment. It is
// evaluated ONCE, when a Client is built, rather than per call: the answer
// cannot change mid-session without the user editing their shell config, and
// re-reading a file on every gh invocation would be pure waste.
func resolveGhCommand() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return ghCommand(os.Environ(), home, execLookPath, os.ReadFile)
}
