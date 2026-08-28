package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/lullabot/sandbar/internal/drupalorg"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// collector is the narrow surface runPublish needs to gather a change set
// off a targeted guest checkout: the module it belongs to (derived from the
// checkout's own origin remote — see drupalorg.ModuleFromRemoteURL) and the
// commits not yet on the fork, parsed into task 1's payload type. Narrow and
// consumer-declared, mirroring land.go's ghActions/vmRunningChecker, so
// doPublish's decision logic (decline, non-TTY refusal, reporting) is
// testable with a fake that returns canned values — no VM, no guest, no
// network. providerCollector (below) is the only production implementation.
type collector interface {
	Collect(ctx context.Context, v vm.VM, path string) (module string, cs drupalorg.ChangeSet, err error)
}

// destPublisher is the narrow internal/drupalorg surface runPublish needs on
// the drupal.org side: resolving where a change set lands (an anonymous
// read plus task 4's issue/-namespace guard) and then publishing it there
// (the one call that ever attaches the account PAT). Narrow and
// consumer-declared for the same reason collector is — see its doc comment
// — so a test can exercise the decline path, the non-TTY refusal, and the
// partial-failure report without a real git.drupalcode.org call or a real
// PAT. liveDestPublisher (below), wrapping a *drupalorg.Client and a
// *drupalorg.Publisher, is the only production implementation.
//
// This file holds no publication logic of its own: every method here is a
// thin call into internal/drupalorg (task 3's Client, task 4's Destination
// guard, task 5's Publisher) — resolution, the guard, the payload, the
// client, and result formatting all live there, exactly as the plan
// requires.
type destPublisher interface {
	// ResolveDestination derives ForkPath(module, issue), fetches it
	// anonymously, and runs task 4's guard (drupalorg.NewDestination).
	// allowOutsideIssueNS is threaded straight through to it — see
	// NewDestination's doc comment for exactly what it permits.
	ResolveDestination(ctx context.Context, module string, issue int, allowOutsideIssueNS bool) (drupalorg.Destination, error)
	// Publish replays cs onto dest — see drupalorg.Publisher.Publish.
	Publish(ctx context.Context, dest drupalorg.Destination, cs drupalorg.ChangeSet) (drupalorg.Result, error)
}

// runPublish implements the `sand publish NAME PATH ISSUE` subcommand: the
// headless entry point that resolves a destination, collects the change set
// from a guest checkout, shows the confirmation, requires an explicit human
// yes, publishes, and prints the result.
//
// Mirrors runLand's shape closely: flag parsing and reordering, the store/
// registry/provider resolution dance, requireRunningVM for the same clear
// refusal on an unknown or non-running VM (land.go's phrasing, reused
// verbatim rather than invented a second time for the same fact), and a
// signal.NotifyContext so a ctrl-C cancels a sweep or an API call cleanly
// instead of leaving an orphaned child.
func runPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "Connection profile NAME lives on (only needed when NAME exists under more than one enabled profile)")
	yesFlag := fs.Bool("yes", false, "Confirm publication non-interactively — the non-interactive form of the human confirmation this command otherwise asks for on a terminal. Pass it only after you have reviewed the printed confirmation yourself; it is never read from an environment variable.")
	allowOutsideNSFlag := fs.Bool("allow-outside-issue-namespace", false, "Allow the commit destination to fall outside the issue/<module>-<issue> fork namespace — the only way past the destination guard, and the only way to publish straight to a canonical drupal.org project")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sand publish NAME PATH ISSUE [--yes] [--allow-outside-issue-namespace] [--profile <name>]

Publish PATH's local commits (inside the VM named NAME) to the drupal.org
issue fork for issue ISSUE, using the workstation's own drupal.org token —
never a credential inside the VM. Prints the destination and every commit
and file that will change, then asks for confirmation before writing
anything; declining publishes nothing.

The named VM must already exist and be running (see 'sand' to list
instances, or 'sand create' to make one). If NAME is managed under more than
one connection profile, --profile picks which one to act on.
`)
	}
	if err := fs.Parse(reorderPublishFlags(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // usage was already printed; -h/--help is not a failure
		}
		return err // flag package already printed usage
	}
	if fs.NArg() != 3 {
		fs.Usage()
		return errors.New("sand publish: need a VM NAME, a checkout PATH, and an ISSUE number")
	}
	name := fs.Arg(0)
	path := fs.Arg(1)
	issue, convErr := strconv.Atoi(fs.Arg(2))
	if convErr != nil || issue <= 0 {
		return fmt.Errorf("sand publish: invalid ISSUE %q: must be a positive integer", fs.Arg(2))
	}

	// Publication cannot complete without explicit human confirmation, and
	// that gate is meaningless if a PAT never existed to publish with. So
	// the very first thing this command does — before touching the VM, the
	// guest checkout, or drupal.org at all — is check for one, and report
	// "publication is unavailable" up front rather than letting an absent
	// PAT surface as a confusing failure in the middle of a collection run
	// or, worse, mid-replay after some commits already landed.
	if _, err := drupalorg.LoadToken(); err != nil {
		if errors.Is(err, drupalorg.ErrNoToken) {
			return fmt.Errorf("sand publish: publication is unavailable: %w", err)
		}
		return fmt.Errorf("sand publish: %w", err)
	}

	store := loadStore()
	reg, loadErr := registry.Load()
	if reg == nil {
		reg = registry.NewEmpty()
	}
	if loadErr != nil {
		fmt.Fprintln(os.Stderr, "warning:", loadErr)
	}

	p, err := resolveShellProvider(store, reg, name, *profileFlag)
	if err != nil {
		return fmt.Errorf("sand publish: %w", err)
	}
	if err := p.Preflight(); err != nil {
		return err
	}
	v, err := requireRunningVM(p, name)
	if err != nil {
		return rewordRequireRunningVMError(err)
	}

	// A ctrl-C during collection or a drupal.org call should cancel it
	// cleanly rather than leaving an orphaned guest process or an
	// in-flight HTTP request, mirroring land.go/create.go's own
	// signal.NotifyContext use around their long-running work.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dp, err := newLiveDestPublisher()
	if err != nil {
		return fmt.Errorf("sand publish: %w", err)
	}

	return doPublish(ctx, os.Stdout, os.Stdin, isTerminal(os.Stdin), providerCollector{p: p}, dp, v, path, issue, *yesFlag, *allowOutsideNSFlag)
}

// rewordRequireRunningVMError re-prefixes a requireRunningVM error (land.go)
// for this command. requireRunningVM's DECISIONS (unknown vs. not-running)
// and their wording are reused verbatim rather than invented a second time
// for the same fact — see runPublish's doc comment — but its error text is
// hardcoded with land.go's own "sand land:" prefix (it is land.go's
// helper, after all), which would otherwise misreport this command's own
// name. Only that prefix is swapped, via CutPrefix rather than a bare
// TrimPrefix, so a future edit to land.go's wording that drops the prefix
// is not silently double-prefixed here — it fails loudly (falls back to
// wrapping the whole original message) instead of guessing.
func rewordRequireRunningVMError(err error) error {
	rest, ok := strings.CutPrefix(err.Error(), "sand land: ")
	if !ok {
		return fmt.Errorf("sand publish: %w", err)
	}
	return fmt.Errorf("sand publish: %s", rest)
}

// reorderPublishFlags moves every recognised flag token (and, for --profile,
// its value) ahead of the positional arguments, so "sand publish NAME PATH
// ISSUE --yes" parses the same as "sand publish --yes NAME PATH ISSUE" under
// flag.FlagSet, which otherwise stops parsing flags at the first non-flag
// token. Mirrors land.go's reorderLandFlags, extended with this command's
// own boolean flags. Anything else is left positional so an unrecognised
// flag still reaches fs.Parse and produces its normal error.
func reorderPublishFlags(args []string) []string {
	var flagArgs, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help" || a == "-help":
			flagArgs = append(flagArgs, a)
		case a == "--yes" || a == "-yes":
			flagArgs = append(flagArgs, a)
		case a == "--allow-outside-issue-namespace" || a == "-allow-outside-issue-namespace":
			flagArgs = append(flagArgs, a)
		case a == "--profile" || a == "-profile":
			flagArgs = append(flagArgs, a)
			if i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
		case strings.HasPrefix(a, "--profile=") || strings.HasPrefix(a, "-profile="):
			flagArgs = append(flagArgs, a)
		default:
			positional = append(positional, a)
		}
	}
	return append(flagArgs, positional...)
}

// doPublish is runPublish's testable core: given already-resolved
// collaborators (coll, dp) and a running VM, it collects the change set,
// resolves the destination, prints the confirmation, gates on an explicit
// human act, publishes, and reports the result — in that order, and no
// step is skipped or reordered by a flag. Everything above this in
// runPublish (flag parsing, VM resolution, drupal.org client construction)
// needs a real VM or a real PAT and so cannot be unit-tested; every
// interesting decision is pulled down into this function instead, driven
// entirely by fakes in publish_test.go — mirroring land.go's
// landPR/landWeb/listCheckouts split.
func doPublish(ctx context.Context, stdout io.Writer, stdin io.Reader, tty bool, coll collector, dp destPublisher, v vm.VM, path string, issue int, yes, allowOutsideNS bool) error {
	module, cs, err := coll.Collect(ctx, v, path)
	if err != nil {
		return err
	}
	if len(cs.Commits) == 0 {
		fmt.Fprintln(stdout, "sand publish: nothing to publish — no local commits ahead of this checkout's upstream branch")
		return nil
	}

	dest, err := dp.ResolveDestination(ctx, module, issue, allowOutsideNS)
	if err != nil {
		return err
	}

	// The confirmation is the one control standing between an agent's
	// output and a public, permanent write — see drupalorg.RenderConfirmation
	// and the package doc comment on internal/drupalorg/confirm.go. It is
	// printed here, never re-rendered or summarised: this file holds no
	// publication logic, and that includes not deciding for itself what is
	// safe to omit from what the human is asked to approve.
	fmt.Fprint(stdout, drupalorg.RenderConfirmation(cs, dest))

	confirmed, err := confirmPublish(stdout, stdin, tty, yes)
	if err != nil {
		return err
	}
	if !confirmed {
		// A decline publishes nothing and is not an error: see the plan's
		// "Destination selection and confirmation" section and this
		// command's own acceptance criteria.
		fmt.Fprintln(stdout, "sand publish: declined — nothing was published")
		return nil
	}

	res, pubErr := dp.Publish(ctx, dest, cs)
	// Reported whether or not Publish returned an error: a partial run
	// (some commits landed, one failed, the rest never attempted) is a
	// first-class outcome per Result's own doc comment in publish.go, not
	// an error path that forfeits the report.
	reportResult(stdout, res)
	if pubErr != nil {
		return fmt.Errorf("sand publish: %w", pubErr)
	}
	return nil
}

// confirmPublish decides whether to proceed, and is the ONLY piece of
// doPublish that reads a real terminal — taking stdin/tty/yes as parameters
// (rather than reaching for os.Stdin and isTerminal itself) is what makes
// the decline path and the non-TTY refusal testable with a canned
// io.Reader, mirroring land.go's confirmOpenPrompt/isTerminal split.
//
// yes is the ONLY sanctioned non-interactive route: someone typing --yes is
// a deliberate human act, unlike a bare pipe. It is never satisfied by an
// environment variable — the flag is the whole contract. Without it, a
// non-TTY stdin refuses outright rather than publishing silently: a pipe is
// not a human, and this command must never default to publishing.
func confirmPublish(stdout io.Writer, stdin io.Reader, tty, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	if !tty {
		return false, errors.New("sand publish: refusing to publish: stdin is not a terminal, so there is no human to confirm this; re-run with --yes after reviewing the confirmation above, or run this from an interactive terminal")
	}
	fmt.Fprint(stdout, "Publish the above to drupal.org? [y/N] ")
	reader := bufio.NewReader(stdin)
	line, _ := reader.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}

// reportResult prints res.Commits in change-set order — one line per
// commit, naming its status and, when it has one, its SHA on the fork — and
// then the merge request and any warnings. It formats fields Publish
// already computed; nothing here re-derives a commit's status or recomputes
// which one failed (that is res.FirstFailure(), read directly), matching
// the acceptance criterion that the report is "printed from task 5's Result
// rather than re-derived".
//
// Publish (internal/drupalorg/publish.go) has no incremental progress
// callback — it is one blocking call that returns a complete Result — so
// there is no way to print a commit's line as it actually lands without
// adding one to that package, which is out of this file's scope (no
// publication logic here). This is therefore the closest a CLI-only change
// can get to "progress streamed per commit": one line per commit, printed
// in order, immediately after the call returns, rather than a single
// collapsed summary.
func reportResult(w io.Writer, res drupalorg.Result) {
	if len(res.Commits) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "COMMIT\tSTATUS\tSHA")
	for _, c := range res.Commits {
		sha := c.SHA
		if sha == "" {
			sha = "-"
		}
		fmt.Fprintf(tw, "%d/%d %s\t%s\t%s\n", c.Index+1, len(res.Commits), c.Subject, c.Status, sha)
	}
	_ = tw.Flush()

	if first := res.FirstFailure(); first != nil {
		fmt.Fprintf(w, "first failure: commit %d/%d (%s): %v\n", first.Index+1, len(res.Commits), first.Subject, first.Err)
	}
	if res.MergeRequest != nil {
		verb := "already open"
		if res.MergeRequestOpened {
			verb = "opened"
		}
		fmt.Fprintf(w, "merge request %s: %s\n", verb, res.MergeRequest.WebURL)
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
}

// liveDestPublisher is destPublisher's only production implementation: it
// wraps a credential-free *drupalorg.Client for the anonymous fork lookup
// task 4's guard needs, and a *drupalorg.Publisher for the one call that
// ever attaches the account PAT. Those stay two different fields (rather
// than one type) because that split is NewPublisher's own contract — see
// its doc comment in publish.go — and this type does not second-guess it.
type liveDestPublisher struct {
	client *drupalorg.Client
	pub    *drupalorg.Publisher
}

// newLiveDestPublisher builds the real destPublisher: a Client pointed at
// git.drupalcode.org's default API root, and a Publisher over it. No PAT is
// read here — NewPublisher loads it lazily, at the moment of the first
// authenticated call (see its doc comment) — and no network call is made
// either; Config{} only decides which base URL to parse.
func newLiveDestPublisher() (*liveDestPublisher, error) {
	client, err := drupalorg.New(drupalorg.Config{})
	if err != nil {
		return nil, err
	}
	return &liveDestPublisher{client: client, pub: drupalorg.NewPublisher(client)}, nil
}

// ResolveDestination derives the canonical issue-fork path for module and
// issue, fetches it anonymously, and hands both to drupalorg.NewDestination
// — task 4's guard — which is the only place a Destination is ever built.
// allowOutsideIssueNS passes straight through to it.
func (l *liveDestPublisher) ResolveDestination(ctx context.Context, module string, issue int, allowOutsideIssueNS bool) (drupalorg.Destination, error) {
	forkPath, err := drupalorg.ForkPath(module, issue)
	if err != nil {
		return drupalorg.Destination{}, fmt.Errorf("sand publish: %w", err)
	}
	fork, err := l.client.Project(ctx, forkPath)
	if err != nil {
		if drupalorg.IsNotFound(err) {
			return drupalorg.Destination{}, fmt.Errorf("sand publish: no issue fork at %q — create one from the issue page on drupal.org first: %w", forkPath, err)
		}
		return drupalorg.Destination{}, fmt.Errorf("sand publish: resolving fork %q: %w", forkPath, err)
	}
	dest, err := drupalorg.NewDestination(module, issue, forkPath, fork, allowOutsideIssueNS)
	if err != nil {
		return drupalorg.Destination{}, fmt.Errorf("sand publish: %w", err)
	}
	return dest, nil
}

// Publish delegates to the wrapped *drupalorg.Publisher — see its doc
// comment in publish.go. No logic of its own.
func (l *liveDestPublisher) Publish(ctx context.Context, dest drupalorg.Destination, cs drupalorg.ChangeSet) (drupalorg.Result, error) {
	return l.pub.Publish(ctx, dest, cs)
}

// The two guest reads this command performs — the checkout's remote URL and
// upstream ref, then its change set — come from internal/drupalorg's
// BuildRemoteInfoCommand/ParseRemoteInfo and BuildCollectCommand/ParseCollect,
// executed by provider.RunCaptured. They lived as private copies here and in
// internal/ui/landing.go until the two had already drifted in their error
// text; the plan requires resolution logic to live once, so the two surfaces
// cannot derive a destination differently.

type providerCollector struct {
	p provider.Provider
}

// Collect resolves path's module (from its origin remote) and upstream ref,
// then runs and parses drupalorg.BuildCollectCommand/ParseCollect against
// that ref — the guest-side collector and host-side parser task 6 already
// built. No logic beyond wiring those two calls to path's actual origin and
// upstream lives here.
func (c providerCollector) Collect(ctx context.Context, v vm.VM, path string) (string, drupalorg.ChangeSet, error) {
	out, err := provider.RunCaptured(ctx, c.p, v, path, drupalorg.BuildRemoteInfoCommand())
	if err != nil {
		return "", drupalorg.ChangeSet{}, fmt.Errorf("sand publish: resolving %q's remote and upstream branch: %w", path, err)
	}
	remoteURL, upstream, err := drupalorg.ParseRemoteInfo(out)
	if err != nil {
		return "", drupalorg.ChangeSet{}, err
	}

	module, err := drupalorg.ModuleFromRemoteURL(remoteURL)
	if err != nil {
		return "", drupalorg.ChangeSet{}, fmt.Errorf("sand publish: checkout %q: %w", path, err)
	}

	script, err := drupalorg.BuildCollectCommand(upstream)
	if err != nil {
		return "", drupalorg.ChangeSet{}, fmt.Errorf("sand publish: %w", err)
	}
	collected, err := provider.RunCaptured(ctx, c.p, v, path, script)
	if err != nil {
		return "", drupalorg.ChangeSet{}, fmt.Errorf("sand publish: collecting changes from %q: %w", path, err)
	}
	cs, err := drupalorg.ParseCollect(string(collected))
	if err != nil {
		return "", drupalorg.ChangeSet{}, fmt.Errorf("sand publish: %w", err)
	}
	return module, cs, nil
}

// run executes expr against path inside v via Provider.RunArgv, capturing
// stdout. Unlike internal/ui/landing.go's use of RunArgv (tea.ExecProcess,
// the caller's real TTY attached, for a `git commit` that opens an editor),
// neither guest script this collector runs needs one — both only read git
// objects and print — so a plain exec.CommandContext with stdout captured
// is enough, and it is what makes the output parseable at all.
