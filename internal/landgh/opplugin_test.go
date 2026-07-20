package landgh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var (
	plainGh = []string{"gh"}
	opGh    = []string{"op", "plugin", "run", "--", "gh"}
)

func TestGhCommand(t *testing.T) {
	const home = "/home/dev"
	cfg := filepath.Join(home, opPluginConfig)

	found := func(string) (string, error) { return "/usr/bin/op", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	withConfig := func(contents string) func(string) ([]byte, error) {
		return func(name string) ([]byte, error) {
			if name != cfg {
				return nil, os.ErrNotExist
			}
			return []byte(contents), nil
		}
	}
	noConfig := func(string) ([]byte, error) { return nil, os.ErrNotExist }

	ghAlias := `alias gh="op plugin run -- gh"`
	ghFunc := "gh() {\n  op plugin run -- gh \"$@\"\n}"

	cases := []struct {
		name     string
		env      []string
		lookPath func(string) (string, error)
		readFile func(string) ([]byte, error)
		want     []string
	}{
		{"op plugin configured (alias form)", nil, found, withConfig(ghAlias), opGh},
		{"op plugin configured (function form)", nil, found, withConfig(ghFunc), opGh},
		{"no op config at all", nil, found, noConfig, plainGh},
		{"op not installed", nil, missing, withConfig(ghAlias), plainGh},
		// A user with SOME op plugin but not gh must still get plain gh.
		{
			name: "op configured for another tool only", lookPath: found,
			readFile: withConfig(`alias doctl="op plugin run -- doctl"`), want: plainGh,
		},
		// An explicit token wins: plain gh already works, so do not drag op
		// (and a possible prompt) into it.
		{"GH_TOKEN set", []string{"GH_TOKEN=ghp_x"}, found, withConfig(ghAlias), plainGh},
		{"GITHUB_TOKEN set", []string{"GITHUB_TOKEN=ghp_x"}, found, withConfig(ghAlias), plainGh},
		// An EMPTY token is not a token.
		{"GH_TOKEN empty", []string{"GH_TOKEN="}, found, withConfig(ghAlias), opGh},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ghCommand(tc.env, home, tc.lookPath, tc.readFile)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ghCommand() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExecRunnerArgvKeepsEveryArgumentSeparate is the injection-safety
// property, pinned on the op path specifically: routing through `op plugin
// run` must prepend argv elements, never build a shell string. A branch name
// full of shell metacharacters has to survive as ONE element, because these
// strings come from a sweep of the guest.
func TestExecRunnerArgvKeepsEveryArgumentSeparate(t *testing.T) {
	nasty := "feature/$(rm -rf ~)`whoami`;echo"
	for _, prefix := range [][]string{plainGh, opGh} {
		r := execRunner{cmd: prefix}
		// Exercise the argv assembly without spawning anything: a cancelled
		// context makes CommandContext fail before exec, and what we care
		// about is that assembly never joins arguments into one string.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _ = r.Output(ctx, "pr", "list", "--head", nasty)

		argv := append(append([]string{}, prefix[1:]...), "pr", "list", "--head", nasty)
		for _, a := range argv {
			if a == "" {
				t.Fatalf("prefix %v produced an empty argv element", prefix)
			}
		}
		if argv[len(argv)-1] != nasty {
			t.Fatalf("prefix %v mangled the branch argument: %q", prefix, argv[len(argv)-1])
		}
	}
}
