package ui

// connecting.go renders the startup interstitial shown while the FIRST list from
// a REMOTE provider is in flight — the SSH handshake to the Lima host, which,
// unlike local Lima, is not instant. Without it the board renders immediately
// with host stats sampled from THIS machine (the header falls back to a local
// probe until the first remote sample lands), which reads as "connected, showing
// the remote" when it is neither. The interstitial holds that back until the
// first successful list and, because a remote connect can hang or be
// misconfigured, keeps ctrl+c / q live so the user can always get out — see the
// connecting field and its handling in model.go (View, the vmsLoadedMsg handler,
// and the KeyPressMsg guard).

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// connectingView centres "Connecting to <target>…" (plus the last connect error,
// if any, and the quit hint) in the terminal. m.width/m.height are seeded to a
// sane default in New and updated on every WindowSizeMsg, so this centres
// correctly even before the first real resize.
func (m model) connectingView() string {
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Connecting to %s…", m.scope.RemoteTarget)),
	}
	if m.connectErr != nil {
		lines = append(lines, "", errStyle.Render(m.connectErr.Error()))
	}
	lines = append(lines, "", statusStyle.Render("ctrl+c to quit"))

	body := lipgloss.JoinVertical(lipgloss.Center, lines...)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}
