package ui

import "strings"

// cloneFailureReason reads only fixed markers emitted by the project role.
// Ansible hides the raw Git result when a token is supplied; copying arbitrary
// output into Messages could expose that token. The role's markers are safe to
// repeat, and the fallback remains the provisioner's normal error.
func cloneFailureReason(output string) string {
	for _, reason := range []struct {
		marker string
		text   string
	}{
		{"SAND_CLONE_ERROR_AUTH:", "Git authentication failed. Check the clone token and its repository access."},
		{"SAND_CLONE_ERROR_REPO:", "Repository not found or inaccessible. Check the clone URL and token access."},
		{"SAND_CLONE_ERROR_NETWORK:", "Cannot reach the repository host. Check the clone URL and network access."},
		{"SAND_CLONE_ERROR_OTHER:", "Project clone failed. Check the clone URL and token access."},
	} {
		if strings.Contains(output, reason.marker) {
			return reason.text
		}
	}
	return ""
}
