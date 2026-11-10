package manage

import (
	"context"
	"fmt"
	"io"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
)

// OwnRebuild reports whether the instance called name is the half-built
// replacement that the reset which left backupDir behind created: its
// provenance marker carries the RebuildID recorded beside the backup (see
// provision.MarkRebuildCloned). That, and nothing weaker, is what makes the
// instance safe to delete before restoring into its name — an instance made
// by hand under the same name, or one whose marker is missing, is never
// treated as disposable.
func OwnRebuild(ctx context.Context, prov provider.Provenancer, backupDir, name string) (bool, error) {
	id, err := provision.RebuildID(backupDir)
	if err != nil || id == "" || prov == nil {
		return false, err
	}
	pv, ok, err := prov.ProvenanceOf(ctx, name)
	if err != nil {
		return false, fmt.Errorf("read %q's provenance marker: %w", name, err)
	}
	return ok && pv.RebuildID == id, nil
}

// RemoveOwnRebuild deletes a half-built replacement that OwnRebuild has
// already identified, saying so.
func RemoveOwnRebuild(b interface{ Delete(string, bool) error }, name string, out io.Writer) error {
	fmt.Fprintf(out, "\n==> Removing the half-built %q left by the failed reset…\n", name)
	if err := b.Delete(name, true); err != nil {
		return fmt.Errorf("delete the half-built %q: %w", name, err)
	}
	return nil
}
