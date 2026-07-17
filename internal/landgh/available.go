package landgh

import "context"

// Available reports whether host gh is present on PATH and authenticated
// (`gh auth status` succeeds). It never touches a VM or the guest — this is a
// purely workstation-local check. Callers use it to decide whether to offer
// the one-key draft-create action or fall back to the gh-free browser URL
// helpers (CompareURL/PRURL/OpenInBrowser).
func (c *Client) Available(ctx context.Context) bool {
	if _, err := c.lookPath("gh"); err != nil {
		return false
	}
	if _, err := c.run.Output(ctx, "auth", "status"); err != nil {
		return false
	}
	return true
}
