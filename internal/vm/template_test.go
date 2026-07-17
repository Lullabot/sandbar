package vm

import "testing"

func TestTemplateInstanceName(t *testing.T) {
	tests := []struct {
		name string
		user string
		want string
	}{
		{"simple lowercase", "golden", "sandbar-tmpl-golden"},
		{"uppercase folded", "MyTemplate", "sandbar-tmpl-mytemplate"},
		{"spaces collapse to a single hyphen", "my template name", "sandbar-tmpl-my-template-name"},
		{"punctuation collapses", "my_template!!", "sandbar-tmpl-my-template"},
		{"leading/trailing junk trimmed", "  --My Template--  ", "sandbar-tmpl-my-template"},
		{"digits and hyphens pass through", "web-v2", "sandbar-tmpl-web-v2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TemplateInstanceName(tt.user); got != tt.want {
				t.Errorf("TemplateInstanceName(%q) = %q, want %q", tt.user, got, tt.want)
			}
		})
	}
}

func TestValidateTemplateName(t *testing.T) {
	tests := []struct {
		name    string
		user    string
		wantErr bool
	}{
		{"valid simple", "golden", false},
		{"valid with hyphen and digits", "web-v2", false},
		{"valid with spaces (slugs to something valid)", "My Golden Image", false},
		{"empty", "", true},
		{"only punctuation slugs to empty", "!!!", true},
		{"whitespace only", "   ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTemplateName(tt.user)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateTemplateName(%q) = nil, want error", tt.user)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateTemplateName(%q) = %v, want nil", tt.user, err)
			}
		})
	}
}

// TestValidateTemplateName_BaseNameGuardExists locks in the defensive guard
// required by this task: ValidateTemplateName must refuse any name whose
// TemplateInstanceName would collide with the shared base image's own
// instance name. Under the current templateInstancePrefix ("sandbar-tmpl-")
// and default base name ("sandbar-base") this can never actually trigger —
// every template instance name is prefixed, so it can never literally equal
// an unprefixed base name — so this test only pins the invariant the guard
// protects, not a reachable rejection path; see ValidateTemplateName's doc
// comment.
func TestValidateTemplateName_BaseNameGuardExists(t *testing.T) {
	base := DefaultCreateConfig().BaseName
	if TemplateInstanceName(base) == base {
		t.Fatalf("TemplateInstanceName(%q) unexpectedly collided with the base name; ValidateTemplateName's guard would now be reachable and must be exercised directly", base)
	}
}
