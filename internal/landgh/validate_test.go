package landgh

import "testing"

func TestValidateOrgRepo(t *testing.T) {
	cases := []struct {
		name    string
		orgRepo string
		wantErr bool
	}{
		{"valid simple", "acme/widgets", false},
		{"valid with dots and dashes", "my-org.io/some_repo.go", false},
		{"missing slash", "acmewidgets", true},
		{"empty", "", true},
		{"two slashes", "acme/widgets/extra", true},
		{"empty owner", "/widgets", true},
		{"empty repo", "acme/", true},
		{"shell metacharacters", "acme/widgets; rm -rf /", true},
		{"space", "acme/wid gets", true},
		{"backtick", "acme/`id`", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOrgRepo(tc.orgRepo)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateOrgRepo(%q) error = %v, wantErr %v", tc.orgRepo, err, tc.wantErr)
			}
		})
	}
}
