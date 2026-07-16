package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// startTransfer opens the file browser for an Upload (host→guest) or Download
// (guest→host). Both directions require a running VM; that guard lives in the
// Upload/Download commands' enabledFor (commandreg.go) — same as Shell — so a
// stopped VM never dispatches here at all rather than dispatching and
// explaining itself. The browser is seeded with the appropriate DirLister and
// start directory per direction.
//
// The VM is the one the COMMAND REGISTRY handed the action, passed in explicitly.
// It used to be read off m.detail — the VM screen's own record — which was
// harmless while the VM screen was the only place a verb could fire from, and is
// a wrong-VM bug the moment the board fires the same verb on the tile under its
// focus ring: the transfer would target whichever VM the user last zoomed into.
// There is exactly one source for "which VM is this verb acting on", and it is
// this argument.
func (m model) startTransfer(v boardVM, upload bool) (tea.Model, tea.Cmd) {
	m.transferVM = v.Name
	m.transferScope = v.scope // the transfer runs against THIS VM's own member
	m.transferUpload = upload

	var lister browse.DirLister
	var startDir, title string
	if upload {
		// Upload: browse the HOST for a source, starting at the working directory.
		lister = browse.NewLocalLister()
		startDir = m.hostWorkDir()
		title = "Upload — pick a host file or directory"
	} else {
		// Download: browse the GUEST for a source, starting at the project checkout.
		lister = browse.NewGuestLister(m.provFor(m.transferScope), m.transferVM)
		startDir = m.guestDefaultDir(v.VM)
		title = "Download — pick a guest file or directory"
	}
	m.browser = browse.NewBrowser(lister, title)
	m.browser.SetSize(m.layout.ContentWidth, m.layout.GridHeight)
	m.view = viewBrowse
	return m, m.browser.Open(startDir)
}

// updateBrowse routes keys while the source browser is active. Esc backs out to
// the board (unless the user is mid-filter, where the browser cancels the
// filter). When the browser reports a selection, the flow advances to the
// destination prompt pre-filled with a per-direction default directory.
func (m model) updateBrowse(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Back) && m.browser.NotFiltering() {
		m.view = viewBoard
		return m, nil
	}
	var cmd tea.Cmd
	m.browser, cmd = m.browser.Update(msg)
	if p, isDir, ok := m.browser.Selected(); ok {
		m.transferSrc = p
		m.transferRecursive = isDir
		// The destination is always a DIRECTORY; the source is placed inside it.
		// The dest field autocompletes directories on the destination side — the
		// host for a download, the guest for an upload.
		def := m.hostWorkDir()
		var destLister browse.DirLister = browse.NewLocalLister()
		if m.transferUpload {
			// The instance record comes from the TRANSFER's VM+scope, looked up in
			// its owning member — not from m.detail, which is the VM screen's record
			// and need not be this one now that the board fires transfers too.
			target, _ := m.lookupVM(m.transferScope, m.transferVM)
			def = m.guestDefaultDir(target) // an upload lands in the guest
			destLister = browse.NewGuestLister(m.provFor(m.transferScope), m.transferVM)
		}
		var initCmd tea.Cmd
		m.dest, initCmd = browse.NewDestInput("Destination dir: ", def, destLister)
		m.view = viewDest
		return m, tea.Batch(textinput.Blink, initCmd)
	}
	return m, cmd
}

// updateDest routes keys on the destination prompt. Esc goes back to the browser;
// Submit (ctrl+s) confirms and launches the copy. Backspace must edit the field,
// so only esc (not the esc/backspace Back binding) backs out.
func (m model) updateDest(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Code == tea.KeyEsc:
		// Drop the pending source pick so returning to the browser lets the user
		// navigate/re-select, instead of the still-set selection bouncing them
		// straight back here on the next keystroke.
		m.browser.ClearSelection()
		m.view = viewBrowse
		return m, nil
	case key.Matches(msg, m.keys.Submit):
		return m.launchCopy()
	}
	var cmd tea.Cmd
	m.dest, cmd = m.dest.Update(msg)
	return m, cmd
}

// launchCopy builds the source/destination endpoints per direction and runs the
// copy through the reused streaming plumbing (viewProgress).
//
// The destination is the directory the user picked, VERBATIM — for a file and for
// a directory alike. lima.Copy's contract is that the source is placed inside the
// destination, which is what scp does natively and why that backend is pinned.
//
// There used to be a compensation layer here (transferDest) that appended the
// source's basename for directory copies, because Lima's RSYNC backend splats a
// directory's contents into the destination instead of nesting it. With the
// backend pinned, that compensation is not merely redundant — it is a bug: scp
// copying `mydir` into `dest/mydir` nests correctly only while `dest/mydir` does
// not yet exist, so the SECOND upload of the same directory landed in
// dest/mydir/mydir. Verified against real limactl 2.1.3.
func (m model) launchCopy() (tea.Model, tea.Cmd) {
	prov := m.provFor(m.transferScope)
	destDir := m.dest.Value()
	var src, dst string
	if m.transferUpload {
		src, dst = m.transferSrc, prov.GuestPath(m.transferVM, destDir)
	} else {
		src, dst = prov.GuestPath(m.transferVM, m.transferSrc), destDir
	}

	verb := "Downloading "
	if m.transferUpload {
		verb = "Uploading "
	}
	title := verb + path.Base(m.transferSrc)

	recursive := m.transferRecursive
	run := func(ctx context.Context, out io.Writer) error {
		// Announce the copy so the job log always has content. `limactl copy`'s scp
		// backend prints a live progress meter only to a TTY, and this runs over a
		// captured pipe (and, for a remote provider, a second SSH hop) with no TTY —
		// so there is no per-file progress to stream, and without this line the log
		// would be blank but for the spinner. The endpoints tell the user exactly
		// what is moving and where.
		fmt.Fprintf(out, "Copying %s\n     to %s\n\n", src, dst)
		return prov.Copy(ctx, out, recursive, src, dst)
	}
	// The copy gets the VM's TRANSFER slot, never its provision slot. That is what
	// keeps provisionDoneMsg from recording it in the managed registry (a copy is
	// not a managed VM), what keeps the tile from reading as Building while it
	// runs — and what stops it from evicting a retained failed build, whose red
	// tile and Ansible log are the only record the user has of why that VM is
	// broken. Both runs can be in flight at once; each has its own log.
	key := transferKey(m.transferScope, m.transferVM)
	cmd, started := m.beginStream(key, title, run)
	// A transfer opens its log, unlike a build. A build has a tile to render its
	// progress onto and a board worth staying on; a transfer has no tile bar of its
	// own, and its output is the only sign it is moving. `esc` still returns to the
	// board with the copy running.
	if started {
		m.focusJob(key)
	}
	return m, cmd
}

// destHelp returns the bindings shown in the destination-prompt's help bar.
func (m model) destHelp() []key.Binding {
	return []key.Binding{m.keys.Submit, m.keys.Back}
}

// destView renders the destination-directory prompt.
func (m model) destView() string {
	var b strings.Builder
	side := "guest"
	if !m.transferUpload {
		side = "host"
	}
	b.WriteString(titleStyle.Render("Destination directory (" + side + ")"))
	b.WriteString("\n\n")
	b.WriteString(m.dest.View())
	b.WriteString("\n\n")
	b.WriteString(statusStyle.Render("The selected item is placed INSIDE this directory."))
	b.WriteString("\n" + statusStyle.Render("Type to autocomplete · ↑/↓ choose · enter fills · ctrl+s copy · esc back"))
	b.WriteString("\n\n" + m.footerView(m.destHelp()))
	return appStyle.Render(b.String())
}

// hostWorkDir is the host browser's / download destination's default: the current
// working directory, falling back to the home directory, then "/".
func (m model) hostWorkDir() string {
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "/"
}

// guestDefaultDir is the guest browser's / upload destination's default: the VM's
// project checkout (<home>/<host>/<org>/<repo>, derived from the recorded
// CloneURL) when known, otherwise the guest home.
//
// The guest home is read through the provider (for local Lima, its instance's
// cloud-config.yaml — Lima places it at /home/<user>.guest, NOT /home/<user>,
// so it cannot be reconstructed from the username). If that can't be read,
// fall back to the old /home/<user> guess (the provider's guest user, then
// recorded config, then host user). v is the VM record the caller is acting
// on (see startTransfer).
func (m model) guestDefaultDir(v vm.VM) string {
	prov := m.provFor(m.transferScope)
	cfg, ok := m.reg.ConfigInScope(m.transferVM, m.transferScope)
	home := prov.GuestHome(v)
	if home == "" {
		user := prov.GuestUser(v)
		if user == "" && ok {
			user = cfg.User
		}
		if user == "" {
			user = hostUser()
		}
		home = "/home/" + user
	}
	if ok && cfg.CloneURL != "" {
		if rel, relOK := provision.CheckoutRelDir(cfg.CloneURL); relOK {
			return home + "/" + rel
		}
	}
	return home
}
