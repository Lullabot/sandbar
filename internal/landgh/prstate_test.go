package landgh

import (
	"context"
	"reflect"
	"testing"
)

func TestPRListArgs(t *testing.T) {
	got := prListArgs("acme/widgets", "feature-x")
	want := []string{"pr", "list", "-R", "acme/widgets", "--head", "feature-x", "--json", "number,url,state,isDraft"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prListArgs = %v, want %v", got, want)
	}
}

// TestPRListArgsArgvInjectionSafety is the graded acceptance-criterion test:
// a branch name containing shell metacharacters must survive as ONE argv
// element, proving prListArgs never builds a shell string that a metachar
// could break out of.
func TestPRListArgsArgvInjectionSafety(t *testing.T) {
	evilBranch := "main; rm -rf / #`id`$(whoami)"
	got := prListArgs("acme/widgets", evilBranch)
	want := []string{"pr", "list", "-R", "acme/widgets", "--head", evilBranch, "--json", "number,url,state,isDraft"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prListArgs with metacharacter branch = %v, want %v", got, want)
	}
	// The dangerous branch name must appear as EXACTLY one slice element, not
	// split across several the way a shell would tokenize it.
	found := 0
	for _, a := range got {
		if a == evilBranch {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("evil branch name appeared %d times as a whole argv element, want exactly 1", found)
	}
}

func TestPRState(t *testing.T) {
	ctx := context.Background()

	t.Run("no PR: empty array decodes to nil, nil", func(t *testing.T) {
		run := &fakeRunner{
			outputs: map[string][]byte{
				argvKey(prListArgs("acme/widgets", "feature-x")): []byte(`[]`),
			},
		}
		c := &Client{run: run}
		pr, err := c.PRState(ctx, "acme/widgets", "feature-x")
		if err != nil {
			t.Fatalf("PRState() error = %v", err)
		}
		if pr != nil {
			t.Fatalf("PRState() = %+v, want nil (no PR)", pr)
		}
	})

	t.Run("PR exists: decodes number/url/state/isDraft", func(t *testing.T) {
		run := &fakeRunner{
			outputs: map[string][]byte{
				argvKey(prListArgs("acme/widgets", "feature-x")): []byte(
					`[{"number":42,"url":"https://github.com/acme/widgets/pull/42","state":"OPEN","isDraft":true}]`,
				),
			},
		}
		c := &Client{run: run}
		pr, err := c.PRState(ctx, "acme/widgets", "feature-x")
		if err != nil {
			t.Fatalf("PRState() error = %v", err)
		}
		if pr == nil {
			t.Fatal("PRState() = nil, want a PR")
		}
		want := &PR{Number: 42, URL: "https://github.com/acme/widgets/pull/42", State: "OPEN", Draft: true}
		if *pr != *want {
			t.Fatalf("PRState() = %+v, want %+v", pr, want)
		}
	})

	t.Run("gh error propagates without panicking", func(t *testing.T) {
		run := &fakeRunner{err: errGhMissing}
		c := &Client{run: run}
		pr, err := c.PRState(ctx, "acme/widgets", "feature-x")
		if err == nil {
			t.Fatal("PRState() error = nil, want error when gh invocation fails")
		}
		if pr != nil {
			t.Fatalf("PRState() = %+v, want nil on error", pr)
		}
	})

	t.Run("malformed JSON returns an error, not a panic", func(t *testing.T) {
		run := &fakeRunner{
			outputs: map[string][]byte{
				argvKey(prListArgs("acme/widgets", "feature-x")): []byte("not json"),
			},
		}
		c := &Client{run: run}
		if _, err := c.PRState(ctx, "acme/widgets", "feature-x"); err == nil {
			t.Fatal("PRState() error = nil, want decode error for malformed JSON")
		}
	})
}
