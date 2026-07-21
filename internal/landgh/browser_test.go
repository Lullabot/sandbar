package landgh

import (
	"context"
	"errors"
	"testing"
)

func TestCompareURL(t *testing.T) {
	cases := []struct {
		name    string
		orgRepo string
		branch  string
		want    string
		wantErr bool
	}{
		{"simple", "acme/widgets", "feature-x", "https://github.com/acme/widgets/pull/new/feature-x", false},
		{"branch with slash", "acme/widgets", "andrew/feature-x", "https://github.com/acme/widgets/pull/new/andrew/feature-x", false},
		{"invalid orgRepo", "not-a-repo", "main", "", true},
		{"empty branch", "acme/widgets", "", "", true},
		{
			"shell metacharacters become URL text, not commands",
			"acme/widgets", "main; rm -rf /",
			"https://github.com/acme/widgets/pull/new/main; rm -rf /",
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CompareURL(tc.orgRepo, tc.branch)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CompareURL(%q, %q) error = %v, wantErr %v", tc.orgRepo, tc.branch, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("CompareURL(%q, %q) = %q, want %q", tc.orgRepo, tc.branch, got, tc.want)
			}
		})
	}
}

func TestPRURL(t *testing.T) {
	cases := []struct {
		name    string
		orgRepo string
		number  int
		want    string
		wantErr bool
	}{
		{"simple", "acme/widgets", 42, "https://github.com/acme/widgets/pull/42", false},
		{"invalid orgRepo", "bad", 1, "", true},
		{"non-positive number", "acme/widgets", 0, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PRURL(tc.orgRepo, tc.number)
			if (err != nil) != tc.wantErr {
				t.Fatalf("PRURL(%q, %d) error = %v, wantErr %v", tc.orgRepo, tc.number, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("PRURL(%q, %d) = %q, want %q", tc.orgRepo, tc.number, got, tc.want)
			}
		})
	}
}

func TestOpenInBrowser(t *testing.T) {
	ctx := context.Background()

	t.Run("delegates to the injected opener", func(t *testing.T) {
		fo := &fakeOpener{}
		c := &Client{open: fo.open}
		if err := c.OpenInBrowser(ctx, "https://github.com/acme/widgets/pull/42"); err != nil {
			t.Fatalf("OpenInBrowser() error = %v", err)
		}
		if len(fo.calls) != 1 || fo.calls[0] != "https://github.com/acme/widgets/pull/42" {
			t.Fatalf("opener calls = %v, want one call with the URL", fo.calls)
		}
	})

	t.Run("rejects an empty URL without invoking the opener", func(t *testing.T) {
		fo := &fakeOpener{}
		c := &Client{open: fo.open}
		if err := c.OpenInBrowser(ctx, ""); err == nil {
			t.Fatal("OpenInBrowser(\"\") error = nil, want error")
		}
		if len(fo.calls) != 0 {
			t.Fatalf("opener should not be called for an invalid URL, got %v", fo.calls)
		}
	})

	t.Run("propagates the opener's error", func(t *testing.T) {
		fo := &fakeOpener{err: errors.New("xdg-open: command not found")}
		c := &Client{open: fo.open}
		if err := c.OpenInBrowser(ctx, "https://github.com/acme/widgets/pull/42"); err == nil {
			t.Fatal("OpenInBrowser() error = nil, want propagated opener error")
		}
	})
}
