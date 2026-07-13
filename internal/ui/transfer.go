package ui

import (
	"bufio"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	yaml "gopkg.in/yaml.v3"
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
func (m model) startTransfer(v vm.VM, upload bool) (tea.Model, tea.Cmd) {
	m.transferVM = v.Name
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
		startDir = m.guestDefaultDir(v.Dir)
		title = "Download — pick a guest file or directory"
	}
	m.browser = browse.NewBrowser(lister, title)
	m.browser.SetSize(m.layout.ContentWidth, m.layout.GridHeight)
	m.view = viewBrowse
	return m, m.browser.Open(startDir)
}

// updateBrowse routes keys while the source browser is active. Esc backs out to
// the detail view (unless the user is mid-filter, where the browser cancels the
// filter). When the browser reports a selection, the flow advances to the
// destination prompt pre-filled with a per-direction default directory.
func (m model) updateBrowse(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		// The dest field autocompletes directories on the destination side — the
		// host for a download, the guest for an upload.
		def := m.hostWorkDir()
		var destLister browse.DirLister = browse.NewLocalLister()
		if m.transferUpload {
			// The instance dir comes from the TRANSFER's VM (m.transferVM), looked up
			// in the loaded fleet — not from m.detail, which is the VM screen's record
			// and need not be this one now that the board fires transfers too.
			target, _ := m.lookupVM(m.transferVM)
			def = m.guestDefaultDir(target.Dir) // an upload lands in the guest
			destLister = browse.NewGuestLister(m.cli, m.transferVM)
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

// transferDest computes the final copy destination. A directory source is placed
// INSIDE destDir as destDir/<name>: `limactl copy -r src dst` copies the source
// directory's *contents* into dst with the rsync backend (and nests it with scp),
// so without the appended name a directory transfer spills its contents into
// destDir instead of creating the directory. Files go straight into destDir. The
// join and the source basename use POSIX vs host path semantics per side (an
// upload's dest is a guest path and its source a host path; a download's are
// reversed).
func transferDest(destDir, srcPath string, recursive, upload bool) string {
	if !recursive {
		return destDir
	}
	if upload {
		return path.Join(destDir, filepath.Base(srcPath)) // guest dest, host source
	}
	return filepath.Join(destDir, path.Base(srcPath)) // host dest, guest source
}

// launchCopy builds the source/destination endpoints per direction and runs the
// copy through the reused streaming plumbing (viewProgress). A directory source
// is nested under the destination via transferDest.
func (m model) launchCopy() (tea.Model, tea.Cmd) {
	destDir := transferDest(m.dest.Value(), m.transferSrc, m.transferRecursive, m.transferUpload)
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
	// The copy gets the VM's TRANSFER slot, never its provision slot. That is what
	// keeps provisionDoneMsg from recording it in the managed registry (a copy is
	// not a managed VM), what keeps the tile from reading as Building while it
	// runs — and what stops it from evicting a retained failed build, whose red
	// tile and Ansible log are the only record the user has of why that VM is
	// broken. Both runs can be in flight at once; each has its own log.
	cmd, _ := m.beginStream(transferKey(m.transferVM), title, viewDetail, run)
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
	b.WriteString("\n\n" + m.help.ShortHelpView(m.destHelp()))
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
// The guest home is read from the instance's cloud-config.yaml, because Lima
// places it at /home/<user>.guest — NOT /home/<user> — so it cannot be
// reconstructed from the username. If that can't be read, fall back to the old
// /home/<user> guess (ssh.config user, then recorded config, then host user).
// dir is the VM's Lima instance directory, from the record the caller is acting
// on (never m.detail — see startTransfer).
func (m model) guestDefaultDir(dir string) string {
	cfg, ok := m.reg.Config(m.transferVM)
	home := guestHome(dir)
	if home == "" {
		user := guestUser(dir)
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

// guestHome returns the guest login user's home directory for the Lima instance
// whose data dir is instanceDir, read from Lima's generated cloud-config.yaml
// (the cloud-init `homedir`). Lima places the guest home at /home/<user>.guest —
// not /home/<user> — so the home cannot be reconstructed from the username. The
// entry matching the ssh.config login user is preferred, otherwise the first
// user carrying a homedir. Returns "" when it can't be determined so the caller
// can fall back.
func guestHome(instanceDir string) string {
	if instanceDir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(instanceDir, "cloud-config.yaml"))
	if err != nil {
		return ""
	}
	var doc struct {
		Users []yaml.Node `yaml:"users"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return ""
	}
	want := guestUser(instanceDir) // prefer the entry for the ssh login user
	first := ""
	for i := range doc.Users {
		// The users list can hold a bare "default" string alongside mappings; skip
		// anything that isn't a user mapping.
		if doc.Users[i].Kind != yaml.MappingNode {
			continue
		}
		var u struct {
			Name    string `yaml:"name"`
			Homedir string `yaml:"homedir"`
		}
		if err := doc.Users[i].Decode(&u); err != nil || u.Homedir == "" {
			continue
		}
		if want != "" && u.Name == want {
			return u.Homedir
		}
		if first == "" {
			first = u.Homedir
		}
	}
	return first
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
