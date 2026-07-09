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
	"strings"
	"syscall"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
)

// maskedDisplay is what `sand secret list` prints in place of a cleartext
// value when --reveal is not given. It mirrors internal/secrets' own mask
// so masked output is consistent whichever path (Redacted() or a direct
// Store read) produced it.
const maskedDisplay = "****"

// runSecret is the `sand secret` command-group dispatcher: it routes to the
// set/list/rm leaves, mirroring the top-level dispatch in main.go.
func runSecret(args []string) error {
	if len(args) == 0 {
		secretUsage(os.Stderr)
		return errors.New("sand secret: missing subcommand (set|list|rm)")
	}

	switch args[0] {
	case "-h", "--help", "help":
		secretUsage(os.Stdout)
		return nil
	case "set":
		return runSecretSet(args[1:])
	case "list":
		return runSecretList(args[1:])
	case "rm":
		return runSecretRm(args[1:])
	case "sync":
		return runSecretSync(args[1:])
	default:
		secretUsage(os.Stderr)
		return fmt.Errorf("sand secret: unknown subcommand %q", args[0])
	}
}

// secretUsage documents the subcommand tree and, critically, the stdin
// value convention: the whole reason this command group reads from stdin
// instead of a flag/positional argument is so secret values never appear on
// argv (visible to any other user via `ps`, and liable to end up in shell
// history).
func secretUsage(w io.Writer) {
	fmt.Fprint(w, `Usage: sand secret <subcommand> [flags]

Manage per-VM host-side secrets (global environment variables, GitHub
tokens, and directory-scoped environment variables) that sand can inject
into a VM. This only mutates the host-side store; applying secrets to a
running VM is a separate 'sand secret sync' step.

IMPORTANT: secret values are never accepted as a CLI argument. 'set' always
reads the value from stdin, e.g.:

    printf 'the-value\n' | sand secret set MY_VAR --vm test

When stdin is a terminal (no pipe/redirect), you are prompted for the value
on stderr instead (the input is not hidden/no-echo).

Subcommands:
  set <NAME> --vm <name> [--dir <relpath>] [--github]
      Store a secret read from stdin (see above).
      Category routing:
        (no flags)          -> VM-global environment variable
        --dir <relpath>     -> directory-scoped environment variable
        --github            -> default GitHub token
        --dir <relpath> --github -> GitHub token scoped to <relpath>

  list --vm <name> [--reveal]
      List stored secrets. Values are masked by default; --reveal prints
      cleartext values.

  rm <NAME> --vm <name> [--dir <relpath>] [--github]
      Remove a secret. Uses the same category routing as 'set'.

  sync --vm <name>
      Re-render the host store's current secrets into an ALREADY-RUNNING VM
      (applies only the secrets role — fast, no other role runs, no VM or
      shell restart). Requires the VM to already be running.

Examples:
  printf 'ghp_xxx\n' | sand secret set TOKEN --vm dev --github
  printf 'ghp_yyy\n' | sand secret set TOKEN --vm dev --github --dir github.com/acme
  printf 'v\n'       | sand secret set VAR   --vm dev --dir some/dir
  sand secret list --vm dev
  sand secret list --vm dev --reveal
  sand secret rm VAR --vm dev --dir some/dir
  sand secret sync --vm dev
`)
}

// secretCategory maps the --dir/--github flags to the secrets.Category to
// store/remove under, following the task's routing table:
//   - --github (--dir given or not) -> CategoryGitHub, scope = dir (empty
//     scope is the VM-wide default token).
//   - --dir without --github        -> CategoryDirEnv, scope = dir.
//   - neither                       -> CategoryGlobal.
func secretCategory(dir string, github bool) secrets.Category {
	switch {
	case github:
		return secrets.CategoryGitHub
	case dir != "":
		return secrets.CategoryDirEnv
	default:
		return secrets.CategoryGlobal
	}
}

// reorderFlags splits args into (positional, flagArgs) so Go's flag package
// — which stops parsing at the first non-flag token and dumps everything
// after it into fs.Args(), unparsed — can still parse flags that come AFTER
// a positional argument. That ordering is this CLI's documented shape (e.g.
// `sand secret set NAME --vm test`, NAME first), so without this
// reordering step, flags placed after NAME would never be recognized.
// valueFlags names (without leading dashes) the flags that consume a
// following argument (e.g. "vm", "dir"); any other "-x"/"--x" token is
// treated as boolean (no following value consumed), unless it is
// self-contained via "=" (e.g. "--dir=some/dir").
func reorderFlags(args []string, valueFlags map[string]bool) (positional, flagArgs []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flagArgs = append(flagArgs, a)
			name := strings.TrimLeft(a, "-")
			if strings.Contains(name, "=") {
				continue // self-contained (--flag=value); no value to consume
			}
			if valueFlags[name] && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return positional, flagArgs
}

// requireVM validates that --vm was supplied. The codebase has no notion of
// a "current/selected" VM for headless commands (sand create requires an
// explicit --name the same way), so --vm is always required here.
func requireVM(subcommand, vmName string) error {
	if vmName == "" {
		return fmt.Errorf("sand secret %s: --vm is required", subcommand)
	}
	return nil
}

// isTerminal reports whether f is connected to a terminal (as opposed to a
// pipe, redirect, or /dev/null). Used to decide whether readSecretValue
// should print an interactive prompt. Implemented directly against
// os.ModeCharDevice rather than pulling in a TTY-detection dependency (e.g.
// golang.org/x/term) — a plain, echoed line read is an acceptable trade-off
// here since this only gates whether a prompt is printed, not how the line
// is read.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// readSecretValue reads a secret value for name from in, one line, with the
// trailing newline (if any) stripped. This is the ONLY way a secret value
// enters this command group: it is never accepted as a positional or flag
// argument, so it never appears on argv (e.g. in `ps` output) or shell
// history. When isTTY is true (interactive stdin), a prompt naming the
// secret is written to prompt (stderr) first; a no-echo/hidden read was
// judged not worth an extra dependency for a host-local secrets file, so
// the input is read (and possibly locally echoed by the terminal) plainly.
func readSecretValue(in io.Reader, prompt io.Writer, isTTY bool, name string) (string, error) {
	if isTTY {
		fmt.Fprintf(prompt, "Enter value for %s: ", name)
	}
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil {
		if !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read secret value: %w", err)
		}
		if line == "" {
			return "", fmt.Errorf("no value provided for %s (stdin was empty)", name)
		}
		// EOF with no trailing newline: the value we got is still valid.
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// doSecretSet performs the actual Load/SetSecret/Save round trip shared by
// runSecretSet, factored out so the category-routing + persistence logic is
// unit-testable without going through flag parsing or stdin.
func doSecretSet(vmName, dir string, github bool, name, value string) error {
	store, err := secrets.Load(vmName)
	if err != nil {
		return fmt.Errorf("load secrets for %q: %w", vmName, err)
	}
	cat := secretCategory(dir, github)
	store.SetSecret(cat, dir, name, value)
	if err := store.Save(vmName); err != nil {
		return fmt.Errorf("save secrets for %q: %w", vmName, err)
	}
	return nil
}

// runSecretSet implements `sand secret set <NAME> [--vm] [--dir] [--github]`.
func runSecretSet(args []string) error {
	fs := flag.NewFlagSet("secret set", flag.ContinueOnError)
	vmName := fs.String("vm", "", "target VM name (required)")
	dir := fs.String("dir", "", "home-relative directory scope (absent = VM-global)")
	github := fs.Bool("github", false, "store as a GitHub token (category github)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: sand secret set <NAME> --vm <name> [--dir <relpath>] [--github]

Reads the secret VALUE from stdin (never as a CLI argument) — see
'sand secret --help' for the full convention and category-routing rules.

Flags:
`)
		fs.PrintDefaults()
	}
	positional, flagArgs := reorderFlags(args, map[string]bool{"vm": true, "dir": true})
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if len(positional) != 1 {
		return fmt.Errorf("sand secret set: expected exactly one NAME argument, got %d", len(positional))
	}
	name := positional[0]

	if err := requireVM("set", *vmName); err != nil {
		return err
	}

	value, err := readSecretValue(os.Stdin, os.Stderr, isTerminal(os.Stdin), name)
	if err != nil {
		return err
	}

	if err := doSecretSet(*vmName, *dir, *github, name, value); err != nil {
		return err
	}

	// A `set` only writes the host store. Apply it to the VM immediately when
	// it is running so "set then use it" just works (the natural expectation);
	// when the VM is not running the value stays safely on the host and renders
	// on the next create/start/sync. Either way, print an honest reminder — an
	// env-var secret needs a NEW shell, so any active session must be
	// reconnected to see it.
	applied, err := applyToVMIfRunning(*vmName)
	if err != nil {
		return fmt.Errorf("secret saved to the host store, but applying it to %q failed: %w", *vmName, err)
	}
	printSetReminder(os.Stdout, secretCategory(*dir, *github), *vmName, applied)
	return nil
}

// printSetReminder tells the user what `set` did and what remains. The
// load-bearing part is the env-var case: rendering the secret into the VM does
// NOT change any already-running process's environment, so the user must open
// a new shell / reconnect active sessions (and restart tools like claude) to
// see it. A GitHub token needs no reconnect — the file-backed credential store
// is re-read on the next git/gh call.
func printSetReminder(w io.Writer, cat secrets.Category, vmName string, applied bool) {
	if !applied {
		fmt.Fprintf(w, "Saved to the host store for %q. It will be applied when the VM is created or next running (or run: sand secret sync --vm %s).\n", vmName, vmName)
		return
	}
	if cat == secrets.CategoryGitHub {
		fmt.Fprintf(w, "Saved and applied to %q — effective on the next git/gh call (no need to reconnect sessions).\n", vmName)
		return
	}
	fmt.Fprintf(w, "Saved and applied to %q. Environment-variable secrets take effect in NEW shells only — reconnect any active sessions (reopen 'limactl shell', restart tools like claude) to pick it up.\n", vmName)
}

// applyToVMIfRunning renders the host store into vmName when it is running,
// reporting whether it applied. Unlike `sync` — which errors on a non-running
// VM — `set` treats "not running" (or a VM that doesn't exist yet) as fine: the
// value is safely on the host and renders on the next create/start/sync. This
// is what makes `sand secret set` apply immediately to a live VM. It reuses the
// same secrets-only RenderSecrets path as `sync`.
func applyToVMIfRunning(vmName string) (bool, error) {
	cli := lima.New(lima.NewExecRunner())
	if err := cli.Preflight(); err != nil {
		return false, err
	}
	status, statusErr := cli.Status(vmName)
	if statusErr != nil || status != "Running" {
		// Unreadable status (e.g. the VM doesn't exist yet) or a stopped VM is
		// not an error for set — nothing to apply now.
		return false, nil
	}

	dir, err := provision.LocatePlaybook()
	if err != nil {
		return false, err
	}
	prov := &provision.Provisioner{Lima: cli, PlaybookDir: dir}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := prov.RenderSecrets(ctx, vmName, syncConfig(vmName), os.Stdout); err != nil {
		return false, err
	}
	return true, nil
}

// runSecretList implements `sand secret list [--vm] [--reveal]`.
func runSecretList(args []string) error {
	fs := flag.NewFlagSet("secret list", flag.ContinueOnError)
	vmName := fs.String("vm", "", "target VM name (required)")
	reveal := fs.Bool("reveal", false, "show cleartext values instead of masked")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: sand secret list --vm <name> [--reveal]

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if err := requireVM("list", *vmName); err != nil {
		return err
	}

	store, err := secrets.Load(*vmName)
	if err != nil {
		return fmt.Errorf("load secrets for %q: %w", *vmName, err)
	}

	printSecretList(os.Stdout, store, *reveal)
	return nil
}

// printSecretList writes one line per stored secret to w. By default it
// goes through internal/secrets' Redacted() helper — the only supported way
// to display a Store's contents without leaking cleartext — so masking
// behavior stays centralized in the secrets package. --reveal (reveal=true)
// prints the store's raw values instead.
func printSecretList(w io.Writer, store *secrets.Store, reveal bool) {
	if !reveal {
		entries := store.Redacted()
		if len(entries) == 0 {
			fmt.Fprintln(w, "no secrets stored")
			return
		}
		for _, e := range entries {
			fmt.Fprintln(w, formatSecretLine(e.Category, e.Scope, e.Name, e.Masked))
		}
		return
	}

	var lines int
	for _, g := range store.Global {
		fmt.Fprintln(w, formatSecretLine(secrets.CategoryGlobal, "", g.Name, g.Value))
		lines++
	}
	for _, g := range store.GitHub {
		fmt.Fprintln(w, formatSecretLine(secrets.CategoryGitHub, g.Scope, "", g.Token))
		lines++
	}
	for _, d := range store.DirEnv {
		fmt.Fprintln(w, formatSecretLine(secrets.CategoryDirEnv, d.Scope, d.Name, d.Value))
		lines++
	}
	if lines == 0 {
		fmt.Fprintln(w, "no secrets stored")
	}
}

// formatSecretLine renders one secrets list row. value is already either
// masked or cleartext by the time it reaches here — this function only
// handles layout.
func formatSecretLine(cat secrets.Category, scope, name, value string) string {
	switch cat {
	case secrets.CategoryGlobal:
		return fmt.Sprintf("[global]  %s = %s", name, value)
	case secrets.CategoryGitHub:
		scopeDisp := scope
		if scopeDisp == "" {
			scopeDisp = "(default)"
		}
		return fmt.Sprintf("[github]  %s = %s", scopeDisp, value)
	case secrets.CategoryDirEnv:
		return fmt.Sprintf("[dir_env] %s:%s = %s", scope, name, value)
	default:
		return fmt.Sprintf("[%s] %s:%s = %s", cat, scope, name, value)
	}
}

// runSecretRm implements `sand secret rm <NAME> [--vm] [--dir] [--github]`.
func runSecretRm(args []string) error {
	fs := flag.NewFlagSet("secret rm", flag.ContinueOnError)
	vmName := fs.String("vm", "", "target VM name (required)")
	dir := fs.String("dir", "", "home-relative directory scope (must match the value used at 'set' time)")
	github := fs.Bool("github", false, "remove a GitHub token (category github)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: sand secret rm <NAME> --vm <name> [--dir <relpath>] [--github]

Flags:
`)
		fs.PrintDefaults()
	}
	positional, flagArgs := reorderFlags(args, map[string]bool{"vm": true, "dir": true})
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if len(positional) != 1 {
		return fmt.Errorf("sand secret rm: expected exactly one NAME argument, got %d", len(positional))
	}
	name := positional[0]

	if err := requireVM("rm", *vmName); err != nil {
		return err
	}

	store, err := secrets.Load(*vmName)
	if err != nil {
		return fmt.Errorf("load secrets for %q: %w", *vmName, err)
	}

	cat := secretCategory(*dir, *github)
	if !store.RemoveSecret(cat, *dir, name) {
		return fmt.Errorf("sand secret rm: no such secret %q (category %s, dir %q)", name, cat, *dir)
	}

	if err := store.Save(*vmName); err != nil {
		return fmt.Errorf("save secrets for %q: %w", *vmName, err)
	}

	// Re-render the (now-smaller) store into the VM so the removed secret
	// actually disappears from it — the secrets role reconciles removals
	// (rewrites/clears secrets.env, prunes stale GitHub credential files).
	// Mirrors `set`: apply when running, otherwise it purges on the next
	// create/start/sync.
	applied, err := applyToVMIfRunning(*vmName)
	if err != nil {
		return fmt.Errorf("secret removed from the host store, but re-rendering %q failed: %w", *vmName, err)
	}
	printRmReminder(os.Stdout, cat, *vmName, applied)
	return nil
}

// printRmReminder explains what `rm` did. The load-bearing honesty point is
// the same as set's: re-rendering removes the secret from the VM's files, but
// an env var already exported into a running shell/process stays until that
// session is reconnected; a removed GitHub token stops being used on the next
// git/gh call.
func printRmReminder(w io.Writer, cat secrets.Category, vmName string, applied bool) {
	if !applied {
		fmt.Fprintf(w, "Removed from the host store for %q. It will be removed from the VM on the next create/start (or run: sand secret sync --vm %s).\n", vmName, vmName)
		return
	}
	if cat == secrets.CategoryGitHub {
		fmt.Fprintf(w, "Removed from %q and applied — git/gh stops using it on the next call.\n", vmName)
		return
	}
	fmt.Fprintf(w, "Removed from %q and applied. Already-open shells keep the variable until reconnected — open a new shell (and restart tools like claude) to drop it.\n", vmName)
}

// runSecretSync implements `sand secret sync --vm <name>`: it re-renders the
// host store's CURRENT secrets into an already-running VM by applying only
// the secrets role (via provision.Provisioner.RenderSecrets — task 4/5's
// shared render entry point), never a full finalize pass, and never starting,
// stopping, or restarting the VM or a shell. Real limactl/ansible execution
// is exercised end to end by task 7's real-VM e2e test; this function's own
// pure-logic pieces (checkVMRunning, syncConfig, effectSummaryLines) are unit
// tested directly.
func runSecretSync(args []string) error {
	fs := flag.NewFlagSet("secret sync", flag.ContinueOnError)
	vmName := fs.String("vm", "", "target VM name (required)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: sand secret sync --vm <name>

Re-render the host secrets store's current contents into an ALREADY-RUNNING
VM by applying only the secrets role (not a full finalize) — fast, and with
no side effects on any other role. The VM must already be running (create it
with 'sand create' or start it first); sync never starts, stops, or restarts
a VM, and never asks you to restart a shell.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if err := requireVM("sync", *vmName); err != nil {
		return err
	}

	cli := lima.New(lima.NewExecRunner())
	if err := cli.Preflight(); err != nil {
		return err
	}

	status, statusErr := cli.Status(*vmName)
	if err := checkVMRunning(*vmName, status, statusErr); err != nil {
		return err
	}

	store, err := secrets.Load(*vmName)
	if err != nil {
		return fmt.Errorf("load secrets for %q: %w", *vmName, err)
	}

	dir, err := provision.LocatePlaybook()
	if err != nil {
		return err
	}
	prov := &provision.Provisioner{Lima: cli, PlaybookDir: dir}

	// A cancellable context lets ctrl+c abort mid-flight, matching the
	// headless create/TUI provisioning paths.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := prov.RenderSecrets(ctx, *vmName, syncConfig(*vmName), os.Stdout); err != nil {
		return err
	}

	for _, line := range effectSummaryLines(store) {
		fmt.Fprintln(os.Stdout, line)
	}
	return nil
}

// checkVMRunning enforces sync's require-running precondition: RenderSecrets
// targets an already-running VM (it never starts one), so a non-running
// target is a clear, immediate error rather than a confusing failure deep
// inside the shell call. It surfaces the observed limactl status (or the
// underlying limactl error) so the user knows exactly why sync refused, per
// the task's "error clearly if the VM isn't running (surface limactl
// status)" requirement. Extracted as a pure function so the message is
// unit-testable without a real limactl.
func checkVMRunning(vmName, status string, statusErr error) error {
	if statusErr != nil {
		return fmt.Errorf("sand secret sync: could not determine status of %q: %w", vmName, statusErr)
	}
	if status != "Running" {
		disp := status
		if disp == "" {
			disp = "not found"
		}
		return fmt.Errorf("sand secret sync: %q is not running (status: %s) — start it first, then retry", vmName, disp)
	}
	return nil
}

// syncConfig resolves the vm.CreateConfig RenderSecrets needs: the VM's
// recorded managed-registry config when one exists, so cfg.User (required by
// the secrets role's getent lookup) matches the identity the VM actually has,
// falling back to sand's own defaults (host username) for a VM that predates
// or bypasses the registry. Registry load errors are treated the same way
// doHeadlessCreate treats them (best-effort; a corrupt/missing index falls
// back to the same defaults rather than blocking sync).
func syncConfig(vmName string) vm.CreateConfig {
	cfg := vm.DefaultCreateConfig()
	cfg.Name = vmName
	cfg.User = vm.HostUser()

	if reg, _ := registry.Load(); reg != nil {
		if stored, ok := reg.Config(vmName); ok {
			cfg = stored
		}
	}
	cfg.Name = vmName
	return cfg
}

// effectSummaryLines picks the honest effect-summary lines to print after a
// successful RenderSecrets, based on which secret categories are actually
// present in the host store (the categories RenderSecrets just rendered):
//   - a GitHub secret present -> the git/GitHub line (effective immediately,
//     since the file-backed credential store + includeIf takes effect on the
//     next git/gh invocation).
//   - a global or directory-scoped env-var secret present -> the env-var
//     line (requires a NEW shell; already-running processes, e.g. a running
//     claude, keep their old environment until restarted).
//
// It deliberately never claims a running process picks up a new env var and
// never suggests forcing a VM or shell restart — both are stated non-goals
// in the task spec. An empty store (nothing to render) still returns a
// non-empty summary so sync's output is never silent.
func effectSummaryLines(store *secrets.Store) []string {
	var lines []string
	if store != nil && len(store.GitHub) > 0 {
		lines = append(lines, "GitHub/git secrets updated — effective immediately (next git/gh call).")
	}
	if store != nil && (len(store.Global) > 0 || len(store.DirEnv) > 0) {
		lines = append(lines, "Global/directory environment variable secrets updated — open a new shell for them to take effect. Already-running processes (e.g. a running claude) keep the old values until restarted.")
	}
	if len(lines) == 0 {
		lines = append(lines, "No secrets are currently stored for this VM — nothing to render.")
	}
	return lines
}
