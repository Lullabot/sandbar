package ui

import (
	"bufio"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/browse"
	"github.com/deviantintegral/claude-code-ansible/tui/internal/lima"
	"github.com/deviantintegral/claude-code-ansible/tui/internal/provision"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// startTransfer opens the file browser for an Upload (host→guest) or Download
// (guest→host). Both directions require a running VM and guard with a clear
// message otherwise, mirroring the list's shell-action guard. The browser is
// seeded with the appropriate DirLister and start directory per direction.
func (m model) startTransfer(upload bool) (tea.Model, tea.Cmd) {
	if m.detail.Status != "Running" {
		m.status = m.detail.Name + " must be running to transfer files (press s to start it)"
		return m, nil
	}
	m.status = ""
	m.transferVM = m.detail.Name
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
		lister = browse.NewGuestLister(m.cli, m.transferVM)
		startDir = m.guestDefaultDir()
		title = "Download — pick a guest file or directory"
	}
	m.browser = browse.NewBrowser(lister, title)
	if m.width > 0 && m.height > 0 {
		m.browser.SetSize(max(20, m.width-6), max(5, m.height-8))
	}
	m.view = viewBrowse
	return m, m.browser.Open(startDir)
}

// updateBrowse routes keys while the source browser is active. Esc backs out to
// the detail view (unless the user is mid-filter, where the browser cancels the
// filter). When the browser reports a selection, the flow advances to the
// destination prompt pre-filled with a per-direction default directory.
func (m model) updateBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Back) && m.browser.NotFiltering() {
		m.view = viewDetail
		return m, nil
	}
	var cmd tea.Cmd
	m.browser, cmd = m.browser.Update(msg)
	if p, isDir, ok := m.browser.Selected(); ok {
		m.transferSrc = p
		m.transferRecursive = isDir
		// The destination is always a DIRECTORY; the source is placed inside it.
		def := m.hostWorkDir()
		if m.transferUpload {
			def = m.guestDefaultDir() // an upload lands in the guest
		}
		m.dest = browse.NewDestInput("Destination dir: ", def)
		m.view = viewDest
		return m, textinput.Blink
	}
	return m, cmd
}

// updateDest routes keys on the destination prompt. Esc goes back to the browser;
// Submit (ctrl+s) confirms and launches the copy. Backspace must edit the field,
// so only esc (not the esc/backspace Back binding) backs out.
func (m model) updateDest(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyEsc:
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
// copy through the reused streaming plumbing (viewProgress). The destination is
// always a directory; a directory source sets recursive=true.
func (m model) launchCopy() (tea.Model, tea.Cmd) {
	destDir := m.dest.Value()
	var src, dst string
	if m.transferUpload {
		src, dst = m.transferSrc, lima.GuestPath(m.transferVM, destDir)
	} else {
		src, dst = lima.GuestPath(m.transferVM, m.transferSrc), destDir
	}

	verb := "Downloading "
	if m.transferUpload {
		verb = "Uploading "
	}
	title := verb + path.Base(m.transferSrc)

	recursive := m.transferRecursive
	run := func(ctx context.Context, out io.Writer) error {
		return m.cli.Copy(ctx, out, recursive, src, dst)
	}
	// beginStream clears provCfg, so provisionDoneMsg will NOT record the transfer
	// in the managed registry — a copy is not a managed VM.
	return m, m.beginStream(title, run)
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
	b.WriteString("\n\n" + m.help.ShortHelpView(m.viewHelp()))
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
// The guest home is /home/<guest-user>, where the guest user is the account Lima
// actually created — read from the instance's ssh.config, the exact user
// `limactl copy`/`shell` authenticate as. Lima may name that account differently
// from the host user (e.g. "andrew.guest" vs the host's "andrew"), so the host
// `id -un` is only a last-resort fallback, never the source of truth.
func (m model) guestDefaultDir() string {
	cfg, ok := m.reg.Config(m.transferVM)
	user := guestUser(m.detail.Dir)
	if user == "" {
		// ssh.config unreadable: fall back to the recorded config, then the host
		// user (the old best-effort guess).
		switch {
		case ok && cfg.User != "":
			user = cfg.User
		default:
			user = hostUser()
		}
	}
	home := "/home/" + user
	if ok && cfg.CloneURL != "" {
		if rel, relOK := provision.CheckoutRelDir(cfg.CloneURL); relOK {
			return home + "/" + rel
		}
	}
	return home
}

// guestUser returns the guest login user for the Lima instance whose data dir is
// instanceDir, parsed from Lima's generated ssh.config ("User <name>"). That is
// the account limactl authenticates as for shell/copy, which Lima may name
// differently from the host user. Returns "" when it can't be determined, so the
// caller can fall back.
func guestUser(instanceDir string) string {
	if instanceDir == "" {
		return ""
	}
	f, err := os.Open(filepath.Join(instanceDir, "ssh.config"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// ssh.config indents directives, e.g. "  User debian".
		if fields := strings.Fields(sc.Text()); len(fields) == 2 && fields[0] == "User" {
			return fields[1]
		}
	}
	return ""
}
