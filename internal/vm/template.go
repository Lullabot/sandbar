package vm

import (
	"fmt"
	"regexp"
	"strings"
)

// templateInstancePrefix marks a Lima instance as a golden template rather
// than a user VM. Every template a user saves is stored under an instance
// name starting with this prefix, so a template's instance can never collide
// with (or be mistaken for) an ordinary managed VM's name, and a template can
// always be recognized by name alone without consulting the registry.
const templateInstancePrefix = "sandbar-tmpl-"

// templateNameRE is the character discipline a slugged template name must
// satisfy: lowercase alphanumeric, hyphen-separated, starting with a letter
// or digit (never a bare hyphen). It mirrors the discipline Lima itself
// expects of an instance name.
var templateNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// slugNonAlnumRE matches any run of characters outside [a-z0-9], collapsed to
// a single hyphen by slug so arbitrary user input becomes something
// templateNameRE accepts.
var slugNonAlnumRE = regexp.MustCompile(`[^a-z0-9]+`)

// slug lowercases userName and collapses every run of non-alphanumeric
// characters into a single hyphen, trimming any leading/trailing hyphen left
// behind. TemplateInstanceName and ValidateTemplateName both slug through
// this one function so they can never disagree about what a name normalizes
// to.
func slug(userName string) string {
	s := strings.ToLower(strings.TrimSpace(userName))
	s = slugNonAlnumRE.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// TemplateInstanceName returns the reserved Lima instance name a golden
// template named userName is stored under. The prefix keeps a template's
// instance permanently distinguishable from a managed VM's — no ordinary
// create or clone path can ever produce a name starting with it.
func TemplateInstanceName(userName string) string {
	return templateInstancePrefix + slug(userName)
}

// ValidateTemplateName rejects a template name that is empty (or slugs to
// nothing), does not match the reserved character discipline, or whose
// instance name (see TemplateInstanceName) would collide with the shared
// base image's own instance name (vm.DefaultCreateConfig().BaseName). That
// last check is a defensive invariant rather than a reachable rejection
// under today's constants — templateInstancePrefix is always prepended, so a
// template's instance name can never literally equal an unprefixed base
// name — but it is kept so the guard still holds if either constant ever
// changes.
func ValidateTemplateName(userName string) error {
	s := slug(userName)
	if s == "" {
		return fmt.Errorf("template name %q is empty after normalization; use letters, digits, and hyphens", userName)
	}
	if !templateNameRE.MatchString(s) {
		return fmt.Errorf("template name %q is invalid: must match %s after normalization", userName, templateNameRE.String())
	}
	if TemplateInstanceName(userName) == DefaultCreateConfig().BaseName {
		return fmt.Errorf("template name %q collides with the base image instance name %q", userName, DefaultCreateConfig().BaseName)
	}
	return nil
}
