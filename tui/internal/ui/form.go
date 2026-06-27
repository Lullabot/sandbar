package ui

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
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
	"Clone URL",
	"Clone token",
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
		def.Name,                    // fName
		def.Name,                    // fHostname  (defaults to the instance name)
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
	m.view = viewForm
	return m.inputs[0].Focus()
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

// field returns a trimmed form value, so a field holding only whitespace counts
// as blank for defaulting.
func (m model) field(i int) string { return strings.TrimSpace(m.inputs[i].Value()) }

// buildConfig assembles a CreateConfig from the form fields. Like new-vm.sh, a
// blank field falls back to its default rather than producing an empty-named VM,
// an empty primary user, or an empty memory/disk that Lima would reject. Only
// the git identity has no default and is required (enforced by Validate).
func (m model) buildConfig() (vm.CreateConfig, error) {
	cfg := vm.DefaultCreateConfig()
	cfg.Name = orDefault(m.field(fName), cfg.Name)
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

// submitForm validates the form; on failure it keeps the form and surfaces the
// error, on success it switches to the streaming progress view and fires create.
func (m model) submitForm() (tea.Model, tea.Cmd) {
	cfg, err := m.buildConfig()
	if err != nil {
		m.formErr = err
		return m, nil
	}
	if err := cfg.Validate(); err != nil {
		m.formErr = err
		return m, nil
	}
	m.formErr = nil
	cmd := m.beginProvision("Creating "+cfg.Name, m.prov.CreateVM, cfg)
	return m, cmd
}

// updateForm handles keys while the create form is active.
func (m model) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Note: 'q' is a text character here, so only ctrl+c (handled globally) quits.
	// Only esc (not the shared Back binding) leaves the form: Back also matches
	// backspace, which here must edit the focused field, not navigate away.
	switch {
	case msg.Type == tea.KeyEsc:
		m.view = viewList
		return m, nil
	case key.Matches(msg, m.keys.Submit):
		return m.submitForm()
	case key.Matches(msg, m.keys.Tab):
		return m, m.focusNext()
	case key.Matches(msg, m.keys.ShiftTab):
		return m, m.focusPrev()
	}

	cmds := make([]tea.Cmd, len(m.inputs))
	for i := range m.inputs {
		m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
	}
	return m, tea.Batch(cmds...)
}

// formView renders the labelled inputs, validation error, and help.
func (m model) formView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("New VM"))
	b.WriteString("\n\n")

	for i := range m.inputs {
		ls := labelStyle
		if i == m.focusIdx {
			ls = focusedLabelStyle
		}
		b.WriteString(ls.Render(fieldLabels[i]+":") + " " + m.inputs[i].View() + "\n")
	}

	if m.formErr != nil {
		b.WriteString("\n" + errStyle.Render("Error: "+m.formErr.Error()))
	}

	b.WriteString("\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}
