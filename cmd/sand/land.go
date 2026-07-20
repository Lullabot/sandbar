package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/landgh"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// ghActions is the narrow landgh surface runLand needs: gh-availability
// detection, the authoritative PR-state lookup, one-shot draft-PR creation,
// and the browser opener. *landgh.Client satisfies it directly (its methods
// match exactly); narrowing to an interface here — rather than depending on
// the concrete *landgh.Client — is what lets landPR/landWeb/listCheckouts be
// tested with a fake that records calls and returns canned PR/error values,
// with no real gh binary or browser spawned (mirroring landgh's own
// Runner/Opener injection one level up).
//
// landgh.CompareURL and landgh.PRURL are deliberately NOT part of this
// interface: they are pure, deterministic string-building functions (no I/O),
// so callers below call them directly even in tests.
type ghActions interface {
	Availability(ctx context.Context) landgh.Availability
	PRState(ctx context.Context, orgRepo, branch string) (*landgh.PR, error)
	CreateDraftPR(ctx context.Context, orgRepo, branch string) (*landgh.PR, error)
	OpenInBrowser(ctx context.Context, target string) error
}

// vmRunningChecker is the narrow provider surface runLand needs to confirm
// the target VM exists and is running before doing anything else — a sweep
// (checkouts.BuildSweepCommand) needs a live guest to run against, matching
// the pane's own running-VM gating. Narrower than shell.go's vmGetter (no
// AttachArgv): land never attaches an interactive session.
type vmRunningChecker interface {
	Get(name string) (vm.VM, error)
}

// requireRunningVM looks up name and returns a clear, actionable error for an
// unknown instance or one that is not Running, mirroring shellAttachArgv's
// same two refusals (shell.go) so the two CLI entrypoints read identically
// to a user, rather than drifting into two different phrasings for the same
// fact.
func requireRunningVM(g vmRunningChecker, name string) (vm.VM, error) {
	found, err := g.Get(name)
	if err != nil {
		if errors.Is(err, lima.ErrNoSuchInstance) {
			return vm.VM{}, fmt.Errorf("sand land: no VM named %q (run 'sand' to list instances)", name)
		}
		return vm.VM{}, fmt.Errorf("sand land: %w", err)
	}
	if found.Status != limaRunning {
		return vm.VM{}, fmt.Errorf("sand land: VM %q is not running (status: %s); start it first", name, found.Status)
	}
	return found, nil
}

// isTerminal reports whether f is connected to a real terminal rather than a
// pipe, file, or /dev/null redirect. It uses the stdlib os.ModeCharDevice bit
// on f's Stat() — the standard zero-dependency isatty check — rather than
// adding a terminal library: neither an existing isatty helper nor
// golang.org/x/term is present anywhere in this repo or go.mod (grepped
// before writing this), and charmbracelet/x/term is only an INDIRECT
// dependency of Bubble Tea, not something this package already imports
// directly. A package-level var (like fleet.go's newDefault/newRemoteLima
// seam) so tests can force both the TTY and pipe branch deterministically
// without a real terminal.
var isTerminal = func(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// confirmOpenPrompt asks the user, on the real terminal, whether to open a
// compare URL in the browser right now — the interactive half of the --pr
// gh-absent TTY branch. It is the ONLY piece of that path that touches a real
// terminal, so landPR takes it as an injected func() bool rather than reading
// stdin itself, keeping the branching logic testable with a canned
// true/false (see land_test.go).
func confirmOpenPrompt() bool {
	fmt.Print("Open in browser now? [y/N] ")
	var reply string
	_, _ = fmt.Scanln(&reply)
	reply = strings.ToLower(strings.TrimSpace(reply))
	return reply == "y" || reply == "yes"
}

// runLand implements the `sand land NAME [PATH] [--pr|--web]` subcommand,
// mirroring `create`/`shell`'s single-profile dispatch (cmd/sand/main.go's
// switch calls this the same way it calls runCreate/runShell).
//
// With no PATH/flags it lists NAME's checkouts and their branch/push/PR
// state. --pr PATH opens a one-shot draft PR for that checkout's pushed
// branch via host gh, falling back to the gh-free compare URL when gh is
// unavailable. --web PATH opens the checkout's branch (or, thanks to
// GitHub's own redirect for an existing PR, its PR) in a browser — gh-free by
// construction.
//
// Detection is entirely internal/checkouts' shared code
// (BuildSweepCommand/ParseSweep): runLand runs it ONCE via a single
// `limactl shell` (ShellOut), never the long-lived loop the TUI's own sweep
// uses — a headless CLI invocation has no reason to keep a guest connection
// open past its one answer. Every gh action is internal/landgh's Client,
// unmodified.
func runLand(args []string) error {
	fs := flag.NewFlagSet("land", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "Connection profile NAME lives on (only needed when NAME exists under more than one enabled profile)")
	prFlag := fs.Bool("pr", false, "Open a one-shot draft PR for PATH's pushed branch (host gh; falls back to the compare URL without gh)")
	webFlag := fs.Bool("web", false, "Open PATH's branch (or its PR) in a browser — gh-free")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sand land NAME [PATH] [--pr | --web] [--profile <name>]

List NAME's git checkouts and their branch/push/PR state, or act on one:

  sand land NAME                list checkouts + branch/push/PR state
  sand land NAME PATH --pr      open a one-shot draft PR for PATH's pushed branch
  sand land NAME PATH --web     open PATH's branch (or PR) in a browser

--pr uses the workstation's own 'gh' (never the guest's token). Without gh
it prints the compare URL and, on a terminal, offers to open it; piped or
scripted, it exits non-zero with the URL on stderr so automation can react.
--web never needs gh: it opens a constructed GitHub URL, which redirects to
an existing PR for the branch on its own.

The named VM must already exist and be running (see 'sand' to list
instances, or 'sand create' to make one). If NAME is managed under more than
one connection profile, --profile picks which one to act on.
`)
	}
	// --profile/--pr/--web may appear before or after the positional
	// arguments (e.g. "sand land NAME PATH --pr"); reorder so all flags
	// precede them, which is what flag.FlagSet.Parse requires (it stops
	// parsing flags at the first non-flag token) — mirrors shell.go's
	// reorderShellFlags.
	if err := fs.Parse(reorderLandFlags(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // usage was already printed; -h/--help is not a failure
		}
		return err // flag package already printed usage
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		fs.Usage()
		return errors.New("sand land: need a VM NAME, and optionally a checkout PATH")
	}
	if *prFlag && *webFlag {
		return errors.New("sand land: --pr and --web cannot be used together")
	}
	name := fs.Arg(0)
	var path string
	if fs.NArg() == 2 {
		path = fs.Arg(1)
	}
	if (*prFlag || *webFlag) && path == "" {
		return errors.New("sand land: --pr/--web require a checkout PATH (run 'sand land NAME' to list them)")
	}
	if path != "" && !*prFlag && !*webFlag {
		return errors.New("sand land: PATH was given but neither --pr nor --web was set")
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
		return fmt.Errorf("sand land: %w", err)
	}
	if err := p.Preflight(); err != nil {
		return err
	}
	if _, err := requireRunningVM(p, name); err != nil {
		return err
	}

	// A ctrl-C during the sweep or a gh call should cancel it cleanly rather
	// than leaving an orphaned limactl/gh child, mirroring create.go's own
	// signal.NotifyContext use around its own long-running work.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	out, err := p.ShellOut(ctx, name, "sh", "-c", checkouts.BuildSweepCommand())
	if err != nil {
		return fmt.Errorf("sand land: sweep %q: %w", name, err)
	}
	vc := checkouts.ParseSweep(string(out))

	gh := landgh.New()

	switch {
	case *prFlag:
		co, err := findCheckout(vc, path)
		if err != nil {
			return err
		}
		return landPR(ctx, os.Stdout, gh, isTerminal(os.Stdout), confirmOpenPrompt, co)
	case *webFlag:
		co, err := findCheckout(vc, path)
		if err != nil {
			return err
		}
		return landWeb(ctx, gh, co)
	default:
		return listCheckouts(ctx, os.Stdout, gh, vc)
	}
}

// reorderLandFlags moves every recognised flag token (and, for --profile,
// its value) ahead of the positional arguments, so "sand land NAME PATH
// --pr" parses the same as "sand land --pr NAME PATH" under flag.FlagSet,
// which otherwise stops parsing flags at the first non-flag token. Mirrors
// shell.go's reorderShellFlags, extended with the boolean --pr/--web tokens.
// Anything else is left positional so an unrecognised flag still reaches
// fs.Parse and produces its normal error.
func reorderLandFlags(args []string) []string {
	var flagArgs, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help" || a == "-help":
			flagArgs = append(flagArgs, a)
		case a == "--pr" || a == "-pr" || a == "--web" || a == "-web":
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

// findCheckout returns the checkout at path within vc, or a clear error
// naming the miss and pointing the user at the listing form of the command.
func findCheckout(vc checkouts.VMCheckouts, path string) (checkouts.Checkout, error) {
	for _, co := range vc.Checkouts {
		if co.Path == path {
			return co, nil
		}
	}
	return checkouts.Checkout{}, fmt.Errorf("sand land: no checkout at %q (run 'sand land NAME' to list them)", path)
}

// prLabel renders a *landgh.PR (or its absence) as the listing's PR column:
// "no PR" when pr is nil (no open PR exists for the branch), else "#N STATE"
// with a "(draft)" suffix when the PR is still a draft.
func prLabel(pr *landgh.PR) string {
	if pr == nil {
		return "no PR"
	}
	label := fmt.Sprintf("#%d %s", pr.Number, pr.State)
	if pr.Draft {
		label += " (draft)"
	}
	return label
}

// pushLabel renders a checkout's push state for the listing's PUSH column,
// including the ahead count for an unpushed branch so "3 commits sitting
// only in the VM" reads as urgency, not just a bare enum value.
func pushLabel(co checkouts.Checkout) string {
	switch co.PushState {
	case checkouts.PushStateUnpushed:
		return fmt.Sprintf("unpushed (+%d)", co.Ahead)
	case checkouts.PushStatePushed:
		if co.Dirty > 0 {
			return "pushed (dirty)"
		}
		return "pushed"
	default:
		return "never pushed"
	}
}

// listCheckouts is the no-PATH/no-flag action: it prints a table of vc's
// checkouts (path, kind, branch, push state, PR state) to w, calling
// gh.PRState per pushed-with-a-known-remote checkout to annotate PR
// existence — the same lazy, authoritative check the TUI's Landing pane
// performs at open. A gh.PRState error for one row is reported inline
// ("? (gh error)") rather than aborting the whole listing, so one bad
// lookup (a rate limit, an unreachable network) does not hide every other
// checkout's state.
func listCheckouts(ctx context.Context, w io.Writer, gh ghActions, vc checkouts.VMCheckouts) error {
	if len(vc.Checkouts) == 0 {
		fmt.Fprintln(w, "no git checkouts found")
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tKIND\tBRANCH\tPUSH\tPR")
	for _, co := range vc.Checkouts {
		pr := "-"
		switch {
		case co.PushState != checkouts.PushStatePushed:
			// Nothing to check: no PR can exist for a branch that is not
			// (currently) reflected on the forge.
		case co.OrgRepo == "":
			pr = "no remote"
		default:
			state, err := gh.PRState(ctx, co.OrgRepo, co.Branch)
			if err != nil {
				pr = "? (gh error)"
			} else {
				pr = prLabel(state)
			}
		}

		branch := co.Branch
		if co.Kind == checkouts.KindWorktree {
			branch = fmt.Sprintf("%s (worktree of %s)", branch, co.Parent)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", co.Path, co.Kind, branch, pushLabel(co), pr)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if vc.Truncated {
		fmt.Fprintln(w, "(sweep truncated: the guest has more checkouts than this pass covered)")
	}
	return nil
}

// landPR implements the --pr action's full decision logic for one checkout.
// It is the pure branch runLand delegates to, taking gh (fakeable), tty (the
// isTerminal(os.Stdout) result), and confirmOpen (fakeable) as parameters
// rather than reaching for os.Stdout/global state itself, so the TTY-vs-pipe
// exit contract can be exercised without a real gh binary, terminal, or
// browser:
//
//   - co not pushed, or no recognized remote: a plain error (nothing to act
//     on).
//   - gh available: CreateDraftPR — a one-shot draft PR — and report its URL.
//   - gh unavailable, tty: print the compare URL and, if confirmOpen()
//     returns true, open it via gh.OpenInBrowser.
//   - gh unavailable, NOT a tty (a script/pipe): return an error whose
//     message IS the compare URL and nothing else. This is deliberate: it
//     lets runLand's caller — main.go's generic "print err to stderr, exit
//     1" dispatch (identical to create/shell, no special-casing needed) —
//     already satisfy the acceptance criterion ("exits non-zero with the URL
//     on stderr") with no extra plumbing.
func landPR(ctx context.Context, stdout io.Writer, gh ghActions, tty bool, confirmOpen func() bool, co checkouts.Checkout) error {
	if co.PushState != checkouts.PushStatePushed {
		return fmt.Errorf("sand land: checkout %q has no pushed branch to open a PR for (state: %s)", co.Path, co.PushState)
	}
	if co.OrgRepo == "" {
		return fmt.Errorf("sand land: checkout %q has no recognized remote to open a PR against", co.Path)
	}

	if gh.Availability(ctx).OK() {
		pr, err := gh.CreateDraftPR(ctx, co.OrgRepo, co.Branch)
		if err != nil {
			return fmt.Errorf("sand land: %w", err)
		}
		fmt.Fprintf(stdout, "draft PR opened: %s\n", pr.URL)
		return nil
	}

	url, err := landgh.CompareURL(co.OrgRepo, co.Branch)
	if err != nil {
		return fmt.Errorf("sand land: %w", err)
	}

	if !tty {
		// Script/pipe with no gh: the returned error's message IS the URL —
		// see the doc comment above for why that satisfies the stderr/exit
		// contract with no special-casing in main.go.
		return errors.New(url)
	}

	fmt.Fprintf(stdout, "gh is not available on this workstation; open the compare URL to create the PR:\n  %s\n", url)
	if confirmOpen() {
		if err := gh.OpenInBrowser(ctx, url); err != nil {
			return fmt.Errorf("sand land: opening browser: %w", err)
		}
	}
	return nil
}

// landWeb implements the --web action: gh-free by construction, so it keeps
// working on a workstation with no gh at all. It never calls
// gh.Available/PRState — it only constructs the branch's compare URL
// (landgh.CompareURL) and hands it to the OS opener (gh.OpenInBrowser, which
// itself makes no gh call). This still satisfies "opens the branch's PR (or
// the branch)": GitHub's own /pull/new/<branch> route redirects to an
// existing open PR for that branch when one exists, so no local PR lookup is
// needed to get the right result.
func landWeb(ctx context.Context, gh ghActions, co checkouts.Checkout) error {
	if co.PushState != checkouts.PushStatePushed {
		return fmt.Errorf("sand land: checkout %q has no pushed branch to open (state: %s)", co.Path, co.PushState)
	}
	if co.OrgRepo == "" {
		return fmt.Errorf("sand land: checkout %q has no recognized remote to open", co.Path)
	}

	url, err := landgh.CompareURL(co.OrgRepo, co.Branch)
	if err != nil {
		return fmt.Errorf("sand land: %w", err)
	}
	if err := gh.OpenInBrowser(ctx, url); err != nil {
		return fmt.Errorf("sand land: opening browser: %w", err)
	}
	return nil
}
