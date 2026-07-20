// Package sandbar is the module root. Its sole purpose is to embed the
// Ansible playbook fileset (site.yml, ansible.cfg, inventory, roles/,
// group_vars/, scripts/, shipped-profiles/) so a Homebrew-installed sand
// binary can provision without a repository checkout on disk. It must not
// import any internal/... package to avoid an import cycle with
// internal/provision, which imports this package for PlaybookFS.
package sandbar

import "embed"

// PlaybookFS is the embedded playbook fileset. The `all:` prefix on
// directories includes dot- and underscore-prefixed files that go:embed
// would otherwise silently drop.
//
// shipped-profiles/ holds the shipped provisioning-profile manifests
// (shipped-profiles/<tool>/profile.yml) and the Ansible role content backing
// them (shipped-profiles/roles/, on ansible.cfg's roles_path) — the
// restructured claude/ddev/go/java optional tools (strikethroo plan 18,
// task 02). It ships alongside the playbook for the same reason scripts/
// does: reusable per-clone by the finalize-stage repo-profile reconciliation,
// without becoming a second, divergent copy of role content.
//
// This list defines what "the playbook" is, so the in-guest rsync in
// internal/provision mirrors it as a filter (a repo-mode mount is the whole
// checkout, and the guest must get the same tree either way). Change one, change
// the other; TestGuestSyncCopiesOnlyThePlaybook fails if they drift.
//
//go:embed site.yml ansible.cfg inventory all:roles all:group_vars all:scripts all:shipped-profiles
var PlaybookFS embed.FS
