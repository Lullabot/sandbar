package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/provision"
	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Form field indices. The slice order also drives tab/shift+tab focus movement.
const (
	fName = iota
	fHostname
	fUser
	fGitName
	fGitEmail
	fCPUs
	fMemory
	fDisk
	fDockerProxyHost
	fCloneURL
	fCloneToken
)

var fieldLabels = []string{
	"Name",
	"Hostname",
	"User",
	"Git name",
	"Git email",
	"CPUs",
	"Memory",
	"Disk",
	"Docker proxy host",
	"GitHub repo URL",
	"GitHub token",
}

// fieldInfo is the per-field help shown for the focused field. The GitHub token
// entry mirrors new-vm.sh's github_token_help: where to create a fine-grained
// token and the recommended (deliberately limited) permissions.
var fieldInfo = []string{
	"Required. Lima instance name — also the VM you'll `limactl shell` into. Must differ from the base image.",
	"VM hostname inside the guest. Blank → same as the instance name.",
	"Primary VM user. Blank → your host username (Lima creates a matching user).",
	"Required. git user.name written into the VM's git config.",
	"Required. git user.email written into the VM's git config.",
	"vCPUs for the VM. Blank → half your host's cores (minimum 2).",
	"RAM for the VM, e.g. 8GiB. Blank → 8GiB.",
	"Disk size for the VM, e.g. 100GiB. Blank → 100GiB.",
	"Optional. Docker registry pull-through proxy host. Blank to skip.",
	"Optional. HTTPS repo to clone into the VM now (GitHub-oriented). Blank to skip.",
	"Optional. Token for a private GitHub repo (blank = public / set up later).\n" +
		"Create a fine-grained token scoped to the repo at:\n" +
		"  https://github.com/settings/personal-access-tokens/new\n" +
		"Recommended permissions (PRs/Issues stay read-only so the agent can't\n" +
		"self-merge to main without human review):\n" +
		"  Actions: Read and write    Contents: Read and write\n" +
		"  Issues: Read    Pull requests: Read    Workflows: Read and write",
}

// hostGit reads a single value from the host git config, best-effort: any error
// (git missing, key unset) yields an empty seed.
func hostGit(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// hostUser mirrors new-vm.sh's default primary user: Lima creates a guest user
// matching the host username, so default to it (best-effort), falling back to
// $USER and then "claude".
func hostUser() string {
	if out, err := exec.Command("id", "-un").Output(); err == nil {
		if u := strings.TrimSpace(string(out)); u != "" {
			return u
		}
	}
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		return u
	}
	return "claude"
}

// defaultCPUs mirrors new-vm.sh's default_cpus(): half the host's logical CPUs,
// with a floor of 2.
func defaultCPUs() int {
	if n := runtime.NumCPU() / 2; n >= 2 {
		return n
	}
	return 2
}

// orDefault returns v when it has content, else def — the Go analogue of the
// script's `: "${VAR:=default}"`.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// newInputs builds the form's text inputs, seeded from DefaultCreateConfig and
// the host git identity. The clone token is masked.
func newInputs() []textinput.Model {
	def := vm.DefaultCreateConfig()
	seeds := []string{
		"",                          // fName      (required; no default — user must name it)
		"",                          // fHostname  (defaults to the instance name at submit)
		hostUser(),                  // fUser      (host username, like Lima)
		hostGit("user.name"),        // fGitName
		hostGit("user.email"),       // fGitEmail
		strconv.Itoa(defaultCPUs()), // fCPUs      (half the host cores, floor 2)
		def.Memory,                  // fMemory
		def.Disk,                    // fDisk
		"",                          // fDockerProxyHost
		"",                          // fCloneURL
		"",                          // fCloneToken
	}

	inputs := make([]textinput.Model, len(fieldLabels))
	for i := range inputs {
		ti := textinput.New()
		ti.CharLimit = 256
		ti.Width = 44
		ti.SetValue(seeds[i])
		if i == fCloneToken {
			ti.EchoMode = textinput.EchoPassword
		}
		inputs[i] = ti
	}
	return inputs
}

// openForm initialises the create form and focuses the first field, returning
// the cursor-blink command.
func (m *model) openForm() tea.Cmd {
	m.inputs = newInputs()
	m.focusIdx = 0
	m.formErr = nil
	m.resetMode = false // a create form is never in reset mode (even after a reset)
	m.view = viewForm
	return m.inputs[0].Focus()
}

// openResetForm initialises the create form in reset mode, pre-filled from the
// target VM's recorded config. The Name is locked to the VM being reset, so focus
// starts on the first editable field (Hostname); the clone token is never stored,
// so it is left blank to be re-supplied for a private repo.
func (m *model) openResetForm(name string, cfg vm.CreateConfig) tea.Cmd {
	m.inputs = newInputs()
	m.inputs[fName].SetValue(cfg.Name)
	m.inputs[fHostname].SetValue(cfg.Hostname)
	m.inputs[fUser].SetValue(cfg.User)
	m.inputs[fGitName].SetValue(cfg.GitName)
	m.inputs[fGitEmail].SetValue(cfg.GitEmail)
	m.inputs[fCPUs].SetValue(strconv.Itoa(cfg.CPUs))
	m.inputs[fMemory].SetValue(cfg.Memory)
	m.inputs[fDisk].SetValue(cfg.Disk)
	m.inputs[fDockerProxyHost].SetValue(cfg.DockerProxyHost)
	m.inputs[fCloneURL].SetValue(cfg.CloneURL)

	m.resetMode = true
	m.resetName = cfg.Name
	m.resetBaseName = cfg.BaseName
	m.preserveClaude = false
	m.preserveProject = false
	m.projectToggleEnabled = cfg.CloneURL != "" // no clone => nothing to preserve
	m.toggleFocus = -1
	m.formErr = nil
	m.view = viewForm
	m.focusIdx = fHostname
	return m.inputs[fHostname].Focus()
}

// focusNext / focusPrev move the cursor between fields, wrapping around.
func (m *model) focusNext() tea.Cmd {
	m.inputs[m.focusIdx].Blur()
	m.focusIdx = (m.focusIdx + 1) % len(m.inputs)
	return m.inputs[m.focusIdx].Focus()
}

func (m *model) focusPrev() tea.Cmd {
	m.inputs[m.focusIdx].Blur()
	m.focusIdx = (m.focusIdx - 1 + len(m.inputs)) % len(m.inputs)
	return m.inputs[m.focusIdx].Focus()
}

// resetFocusNext advances focus in reset mode: through the editable inputs
// (starting at fHostname), then into the two preserve toggles, wrapping back to
// fHostname. The locked Name field is never focused, and the project toggle is
// skipped when disabled (the VM cloned no repo).
func (m *model) resetFocusNext() tea.Cmd {
	switch {
	case m.toggleFocus == -1:
		if m.focusIdx < fCloneToken {
			m.inputs[m.focusIdx].Blur()
			m.focusIdx++
			return m.inputs[m.focusIdx].Focus()
		}
		// Past the last input → first toggle; blur all text inputs.
		m.inputs[m.focusIdx].Blur()
		m.toggleFocus = 0
		return nil
	case m.toggleFocus == 0 && m.projectToggleEnabled:
		m.toggleFocus = 1
		return nil
	default: // last toggle → wrap around to the first editable input
		m.toggleFocus = -1
		m.focusIdx = fHostname
		return m.inputs[fHostname].Focus()
	}
}

// resetFocusPrev reverses resetFocusNext.
func (m *model) resetFocusPrev() tea.Cmd {
	switch {
	case m.toggleFocus == 1:
		m.toggleFocus = 0
		return nil
	case m.toggleFocus == 0:
		// Back up from the first toggle to the last input.
		m.toggleFocus = -1
		m.focusIdx = fCloneToken
		return m.inputs[fCloneToken].Focus()
	default: // focus is in the inputs
		if m.focusIdx > fHostname {
			m.inputs[m.focusIdx].Blur()
			m.focusIdx--
			return m.inputs[m.focusIdx].Focus()
		}
		// At the first editable input → wrap up to the last toggle.
		m.inputs[m.focusIdx].Blur()
		if m.projectToggleEnabled {
			m.toggleFocus = 1
		} else {
			m.toggleFocus = 0
		}
		return nil
	}
}

// field returns a trimmed form value, so a field holding only whitespace counts
// as blank for defaulting.
func (m model) field(i int) string { return strings.TrimSpace(m.inputs[i].Value()) }

// buildConfig assembles a CreateConfig from the form fields. Like new-vm.sh, a
// blank field falls back to its default rather than producing an empty-named VM,
// an empty primary user, or an empty memory/disk that Lima would reject. Only
// the git identity has no default and is required (enforced by Validate).
func (m model) buildConfig() (vm.CreateConfig, error) {
	cfg := vm.DefaultCreateConfig()
	cfg.Name = m.field(fName)                              // required; Validate rejects empty
	cfg.Hostname = orDefault(m.field(fHostname), cfg.Name) // hostname defaults to the name
	cfg.User = orDefault(m.field(fUser), hostUser())
	cfg.GitName = m.field(fGitName)
	cfg.GitEmail = m.field(fGitEmail)

	if cpuStr := m.field(fCPUs); cpuStr == "" {
		cfg.CPUs = defaultCPUs()
	} else {
		cpus, err := vm.ParseCPUs(cpuStr)
		if err != nil {
			return cfg, err
		}
		cfg.CPUs = cpus
	}

	cfg.Memory = orDefault(m.field(fMemory), cfg.Memory)
	cfg.Disk = orDefault(m.field(fDisk), cfg.Disk)
	if lang := strings.TrimSpace(os.Getenv("LANG")); lang != "" {
		cfg.Locale = lang // matches the script's LOCALE="${LANG:-en_US.UTF-8}"
	}
	cfg.DockerProxyHost = m.field(fDockerProxyHost)
	cfg.CloneURL = m.field(fCloneURL)
	cfg.CloneToken = m.field(fCloneToken)
	return cfg, nil
}

// parseLimaSize parses a Lima-style size string ("20GiB", "512MiB", "2TiB") into
// bytes using binary (1024) units, matching Lima's sizing. A bare number is
// treated as bytes. It returns false when the value is empty or unparseable, so
// callers can decide whether an unrecognisable size is an error.
func parseLimaSize(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// Split the leading number (digits/decimal point) from the trailing unit.
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	num, err := strconv.ParseFloat(s[:i], 64)
	if err != nil || num < 0 {
		return 0, false
	}
	var mult float64
	switch strings.ToLower(strings.TrimSpace(s[i:])) {
	case "", "b":
		mult = 1
	case "k", "kib", "kb":
		mult = 1 << 10
	case "m", "mib", "mb":
		mult = 1 << 20
	case "g", "gib", "gb":
		mult = 1 << 30
	case "t", "tib", "tb":
		mult = 1 << 40
	case "p", "pib", "pb":
		mult = 1 << 50
	default:
		return 0, false
	}
	return int64(num * mult), true
}

// submitForm validates the form; on failure it keeps the form and surfaces the
// error, on success it switches to the streaming progress view and fires create
// (or, in reset mode, the reset).
func (m model) submitForm() (tea.Model, tea.Cmd) {
	cfg, err := m.buildConfig()
	if err != nil {
		m.formErr = err
		return m, nil
	}
	if m.resetMode {
		return m.submitReset(cfg)
	}
	if err := cfg.Validate(); err != nil {
		m.formErr = err
		return m, nil
	}
	m.formErr = nil
	cmd := m.beginProvision("Creating "+cfg.Name, m.prov.CreateVM, cfg)
	return m, cmd
}

// submitReset validates a reset-mode form and dispatches provision.Reset. The
// Name and base image come from the locked targets (not the editable fields), and
// the disk must be at least the base floor: the base image is built at
// BaseDiskFloor and a clone's qcow2 disk can grow but not shrink.
func (m model) submitReset(cfg vm.CreateConfig) (tea.Model, tea.Cmd) {
	cfg.Name = m.resetName
	cfg.BaseName = m.resetBaseName
	if err := cfg.Validate(); err != nil {
		m.formErr = err
		return m, nil
	}
	floor, _ := parseLimaSize(vm.BaseDiskFloor)
	if want, ok := parseLimaSize(cfg.Disk); ok && want < floor {
		m.formErr = fmt.Errorf("disk %s is below the base floor of %s; a reset can only grow the disk", cfg.Disk, vm.BaseDiskFloor)
		return m, nil
	}
	m.formErr = nil
	opts := provision.ResetOptions{PreserveClaude: m.preserveClaude, PreserveProject: m.preserveProject && m.projectToggleEnabled}
	run := func(ctx context.Context, c vm.CreateConfig, out io.Writer) error {
		return m.prov.Reset(ctx, c, opts, out)
	}
	return m, m.beginProvision("Resetting "+cfg.Name, run, cfg)
}

// updateForm handles keys while the create form is active.
func (m model) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Note: 'q' is a text character here, so only ctrl+c (handled globally) quits.
	// Only esc (not the shared Back binding) leaves the form: Back also matches
	// backspace, which here must edit the focused field, not navigate away.
	switch {
	case msg.Type == tea.KeyEsc:
		m.view = viewList
		m.resetMode = false // a later create form must not inherit reset state
		return m, nil
	case key.Matches(msg, m.keys.Submit): // ctrl+s — submit from any field
		return m.submitForm()
	}

	// Reset mode locks the Name field and adds two preserve toggles after the
	// inputs, so it navigates differently; create mode keeps its existing flow.
	if m.resetMode {
		return m.updateResetForm(msg)
	}

	switch {
	case key.Matches(msg, m.keys.ShiftTab), key.Matches(msg, m.keys.Up):
		return m, m.focusPrev()
	// Down/Tab/enter all advance to the next field; enter no longer creates.
	case key.Matches(msg, m.keys.Down), key.Matches(msg, m.keys.Tab):
		return m, m.focusNext()
	}

	cmds := make([]tea.Cmd, len(m.inputs))
	for i := range m.inputs {
		m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
	}
	return m, tea.Batch(cmds...)
}

// updateResetForm handles keys for the reset-mode form: navigation skips the
// locked Name and extends into the two preserve toggles, and space/enter on a
// focused toggle flips it instead of moving focus.
func (m model) updateResetForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// On a focused toggle, space/enter flips its bool rather than navigating.
	if m.toggleFocus >= 0 && (msg.Type == tea.KeySpace || msg.Type == tea.KeyEnter) {
		switch {
		case m.toggleFocus == 0:
			m.preserveClaude = !m.preserveClaude
		case m.projectToggleEnabled: // toggle 1 is inert without a cloned project
			m.preserveProject = !m.preserveProject
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.ShiftTab), key.Matches(msg, m.keys.Up):
		return m, m.resetFocusPrev()
	case key.Matches(msg, m.keys.Down), key.Matches(msg, m.keys.Tab):
		return m, m.resetFocusNext()
	}

	// Only forward edits while a text input is focused (toggles aren't inputs).
	if m.toggleFocus == -1 {
		cmds := make([]tea.Cmd, len(m.inputs))
		for i := range m.inputs {
			m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

// toggleRow renders one reset-mode preserve toggle: a checkbox glyph, the label
// highlighted when focused, and a grayed "(no project cloned)" note when the
// toggle is disabled (the VM cloned no repo, so there is nothing to preserve).
// It uses inline styles rather than labelStyle/focusedLabelStyle, whose fixed
// width would wrap these longer lines.
func toggleRow(label string, on, focused, enabled bool) string {
	box := "[ ]"
	if on {
		box = "[x]"
	}
	line := box + " " + label
	switch {
	case !enabled:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(line + "  (no project cloned)")
	case focused:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Render(line)
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(line)
	}
}

// formView renders the labelled inputs, validation error, and help. In reset mode
// the Name is shown as a locked line and two preserve toggles follow the inputs.
func (m model) formView() string {
	var b strings.Builder
	title := "New VM"
	if m.resetMode {
		title = "Reset VM"
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n\n")

	for i := range m.inputs {
		// In reset mode the Name is fixed to the target VM: render it as a static,
		// dimmed line rather than an editable input box.
		if m.resetMode && i == fName {
			b.WriteString(statusStyle.Render("Name: "+m.resetName+" (locked)") + "\n")
			continue
		}
		ls := labelStyle
		focused := i == m.focusIdx
		if m.resetMode {
			focused = m.toggleFocus == -1 && i == m.focusIdx
		}
		if focused {
			ls = focusedLabelStyle
		}
		b.WriteString(ls.Render(fieldLabels[i]+":") + " " + m.inputs[i].View() + "\n")
	}

	// The two preserve toggles and their compromise warning (reset mode only).
	if m.resetMode {
		b.WriteString("\n")
		b.WriteString(toggleRow("Preserve Claude Code settings", m.preserveClaude, m.toggleFocus == 0, true) + "\n")
		b.WriteString(toggleRow("Preserve project .env + checkout", m.preserveProject, m.toggleFocus == 1, m.projectToggleEnabled) + "\n")
		if m.preserveClaude || m.preserveProject {
			b.WriteString("\n" + errStyle.Render("Preserving copies your Claude login and the .env token out of the VM to your host. Do NOT preserve if you suspect this VM is compromised.") + "\n")
		}
		b.WriteString("\n" + fieldInfoStyle.Render("Disk can only grow from the base floor (min "+vm.BaseDiskFloor+").") + "\n")
	}

	// Help for the focused field (where to get a GitHub token, what defaults
	// apply, which fields are required). Skipped when a toggle is focused.
	showInfo := m.focusIdx >= 0 && m.focusIdx < len(fieldInfo)
	if m.resetMode && m.toggleFocus >= 0 {
		showInfo = false
	}
	if showInfo {
		b.WriteString("\n" + fieldInfoStyle.Render(fieldInfo[m.focusIdx]) + "\n")
	}

	if m.formErr != nil {
		b.WriteString("\n" + errStyle.Render("Error: "+m.formErr.Error()))
	}

	b.WriteString("\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}
