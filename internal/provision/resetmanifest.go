// resetmanifest.go makes a staging directory self-describing, so a reset that
// failed after the guest was deleted can be finished later.
//
// The archives alone are not enough to resume from: which of them go back
// before finalize and which after, whether the playbook's clone step must be
// skipped, and which checkouts the extras archive holds are all decisions
// StagePreserve made from the live guest. Once the guest is gone that knowledge
// is gone with it, so the same pass records it beside the archives. The VM's
// configuration rides along for the same reason: the managed-VM index prunes a
// VM that no longer exists the next time anything lists instances, and a resume
// that could only rebuild from defaults would not be "give me this VM back".
package provision

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lullabot/sandbar/internal/vm"
)

// resetManifestName is the file inside a staging directory that holds the
// manifest. Deliberately unlike the archive names so a glob for archives never
// picks it up.
const resetManifestName = "reset.json"

// resetManifestSchema versions the manifest. A newer schema than this binary
// understands is refused rather than half-read: restoring from a plan whose
// fields were misread could overwrite a checkout the plan meant to protect.
const resetManifestSchema = 1

// ResetManifest is what a resume needs beyond the archives themselves.
type ResetManifest struct {
	Schema int `json:"schema"`
	// Config is the configuration the VM was being rebuilt with. CloneToken is
	// never written (see NewResetManifest): the staging directory sits on disk
	// for as long as the user takes to notice the failure.
	Config vm.CreateConfig `json:"config"`
	// Plan is what was staged and how it goes back.
	Plan PreservePlan `json:"plan"`
	// TemplateSource is non-empty when the reset rebuilds from a golden template
	// rather than the base image; see ResetOptions.TemplateSource.
	TemplateSource string `json:"template_source,omitempty"`
}

// NewResetManifest builds the manifest for a reset about to delete its guest.
// The clone token is stripped: it is a credential, not configuration, and the
// same rule keeps it out of the managed index and the provenance marker.
func NewResetManifest(cfg vm.CreateConfig, plan PreservePlan, opts ResetOptions) ResetManifest {
	cfg.CloneToken = ""
	return ResetManifest{
		Schema:         resetManifestSchema,
		Config:         cfg,
		Plan:           plan,
		TemplateSource: opts.TemplateSource,
	}
}

// WriteManifest records m in the staging directory. A nil guard means nothing
// was staged, so there is nothing to describe. The write is 0600 like the
// archives: the config carries the user's git identity and hostname.
func (g *StageGuard) WriteManifest(m ResetManifest) error {
	if g.Dir() == "" {
		return nil
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode reset manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(g.dir, resetManifestName), data, 0o600); err != nil {
		return fmt.Errorf("write reset manifest: %w", err)
	}
	g.name = m.Config.Name
	return nil
}

// ResumeStage opens a staging directory left behind by a failed reset and
// returns the guard that owns it plus the plan to restore from.
//
// The guard is returned already marked as having destroyed the guest: a resume
// starts after the delete, so the archives are the only copy and every failure
// keeps them. Only a full success removes the directory.
//
// A directory from before manifests existed has no reset.json. Its plan is
// inferred from the archives for the two cases that can be reconstructed
// faithfully — a whole-home archive or an agent-state archive — and legacy
// reports true so the caller knows the configuration is not recorded. A project
// or extras archive without a manifest is refused, because the paths inside it
// were chosen by a probe that cannot be repeated.
func ResumeStage(dir string) (g *StageGuard, m ResetManifest, legacy bool, err error) {
	st, statErr := os.Stat(dir)
	if statErr != nil {
		return nil, m, false, fmt.Errorf("backup directory: %w", statErr)
	}
	if !st.IsDir() {
		return nil, m, false, fmt.Errorf("backup directory: %s is not a directory", dir)
	}
	g = &StageGuard{dir: dir, destroyed: true}

	data, readErr := os.ReadFile(filepath.Join(dir, resetManifestName))
	switch {
	case readErr == nil:
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, m, false, fmt.Errorf("read %s: %w", resetManifestName, err)
		}
		if m.Schema > resetManifestSchema {
			return nil, m, false, fmt.Errorf("%s in %s was written by a newer sand (schema %d, this one reads %d); upgrade sand to resume from it", resetManifestName, dir, m.Schema, resetManifestSchema)
		}
		if err := checkArchives(dir, m.Plan); err != nil {
			return nil, m, false, err
		}
		g.name = m.Config.Name
		return g, m, false, nil
	case !errors.Is(readErr, os.ErrNotExist):
		return nil, m, false, fmt.Errorf("read %s: %w", resetManifestName, readErr)
	}

	has := func(archive string) bool {
		fi, err := os.Stat(filepath.Join(dir, archive))
		return err == nil && fi.Mode().IsRegular()
	}
	if has(projectArchive) || has(extrasArchive) {
		return nil, m, false, fmt.Errorf("%s has no %s, and its project/checkout archives cannot be restored without one (the paths inside them were chosen by the reset that made them); extract them by hand with tar", dir, resetManifestName)
	}
	switch {
	case has(homeArchive):
		m.Plan.WholeHome = true
	case has(agentsArchive):
		m.Plan.Agents = true
	default:
		return nil, m, false, fmt.Errorf("%s holds no home.tar or agents.tar to restore", dir)
	}
	return g, m, true, nil
}

// ResumeReset opens dir for a resume and returns its guard and plan. The
// configuration the manifest recorded is the CALLER's to apply (the CLI reads it
// with ResumeStage before building cfg); this only needs the name for messages. A legacy
// directory keeps the caller's cfg, and because nothing recorded whether the
// project tree is inside the archive, the finalize clone is skipped for a
// whole-home restore rather than risk cloning over it.
func ResumeReset(dir, name string, out io.Writer) (*StageGuard, PreservePlan, error) {
	g, m, legacy, err := ResumeStage(dir)
	if err != nil {
		return nil, PreservePlan{}, err
	}
	step(out, "Restoring %q from the backup in %s", name, dir)
	if legacy {
		note(out, "Note: %s predates reset manifests, so only the archive itself is restored; the VM is rebuilt from the settings passed now.", dir)
		if m.Plan.WholeHome {
			m.Plan.Project.RestoresCheckout = true
		}
	}
	return g, m.Plan, nil
}

// checkArchives verifies that every archive plan says was staged is present, so
// a resume fails before it builds a VM rather than after restoring half a plan.
func checkArchives(dir string, plan PreservePlan) error {
	var need []string
	switch {
	case plan.WholeHome:
		need = append(need, homeArchive)
	case plan.Agents:
		need = append(need, agentsArchive)
	}
	if plan.Project.Staged {
		need = append(need, projectArchive)
	}
	if len(plan.Extras) > 0 {
		need = append(need, extrasArchive)
	}
	for _, a := range need {
		if fi, err := os.Stat(filepath.Join(dir, a)); err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is missing %s, which its manifest says was staged", dir, a)
		}
	}
	return nil
}
