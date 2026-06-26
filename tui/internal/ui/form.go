package ui

import (
	"os/exec"
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

// newInputs builds the form's text inputs, seeded from DefaultCreateConfig and
// the host git identity. The clone token is masked.
func newInputs() []textinput.Model {
	def := vm.DefaultCreateConfig()
	seeds := []string{
		def.Name,               // fName
		"",                     // fHostname
		"",                     // fUser
		hostGit("user.name"),   // fGitName
		hostGit("user.email"),  // fGitEmail
		strconv.Itoa(def.CPUs), // fCPUs
		def.Memory,             // fMemory
		def.Disk,               // fDisk
		"",                     // fDockerProxyHost
		"",                     // fCloneURL
		"",                     // fCloneToken
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

// buildConfig assembles a CreateConfig from the form fields, parsing CPUs.
func (m model) buildConfig() (vm.CreateConfig, error) {
	cfg := vm.DefaultCreateConfig()
	cfg.Name = m.inputs[fName].Value()
	cfg.Hostname = m.inputs[fHostname].Value()
	cfg.User = m.inputs[fUser].Value()
	cfg.GitName = m.inputs[fGitName].Value()
	cfg.GitEmail = m.inputs[fGitEmail].Value()

	cpus, err := vm.ParseCPUs(m.inputs[fCPUs].Value())
	if err != nil {
		return cfg, err
	}
	cfg.CPUs = cpus

	cfg.Memory = m.inputs[fMemory].Value()
	cfg.Disk = m.inputs[fDisk].Value()
	cfg.DockerProxyHost = m.inputs[fDockerProxyHost].Value()
	cfg.CloneURL = m.inputs[fCloneURL].Value()
	cfg.CloneToken = m.inputs[fCloneToken].Value()
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
	switch {
	case key.Matches(msg, m.keys.Back):
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
