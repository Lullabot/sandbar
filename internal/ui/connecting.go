package ui

// connecting.go renders the board's "the fleet hasn't connected yet" hint. It
// replaces the old full-screen "Connecting to <host>…" interstitial: a fleet no
// longer blocks the whole board on one profile's handshake — each member
// connects, lists and renders on its own, so a slow remote never hides a healthy
// local's tiles. The only case left worth a special hint is the one the old
// vmsLoaded guard also protects against: EVERY enabled member is still
// connecting or errored AND there are no tiles at all, so the board is empty
// because nothing has landed — not because the user has no VMs. Showing the
// bare "press enter to add a VM" ghost there would misrepresent an in-flight
// fleet as an empty one. Once ANY member connects (boardReady), the ghost/roster
// takes over; the per-profile status bar surfaces per-profile connection state.

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// fleetConnectingBanner is the hint shown in the grid area while the whole fleet
// is still connecting/errored with nothing to show. It names how many profiles
// are being reached, and — for the common single-profile case — the reason
// there is nothing yet, without the create invitation that would be a lie about
// why the board is empty.
func (m model) fleetConnectingBanner() string {
	n := m.enabledMemberCount()
	noun := "profiles"
	if n == 1 {
		noun = "profile"
	}
	body := lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Connecting to %d %s…", n, noun))

	// Surface the first member's error, if any, so a misconfigured or unreachable
	// profile is visible rather than a board that just sits blank forever.
	var errLine string
	for i := range m.members {
		if m.members[i].state == connErrored && m.members[i].lastErr != nil {
			errLine = m.members[i].lastErr.Error()
			break
		}
	}
	lines := []string{body}
	if errLine != "" {
		lines = append(lines, "", errStyle.Render(errLine))
	}
	return lipgloss.JoinVertical(lipgloss.Center, lines...)
}
