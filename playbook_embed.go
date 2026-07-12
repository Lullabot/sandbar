// Package sandbar is the module root. Its sole purpose is to embed the
// Ansible playbook fileset (site.yml, ansible.cfg, inventory, roles/,
// group_vars/) so a Homebrew-installed sand binary can provision without a
// repository checkout on disk. It must not import any internal/... package
// to avoid an import cycle with internal/provision, which imports this
// package for PlaybookFS.
package sandbar

import "embed"

// PlaybookFS is the embedded playbook fileset. The `all:` prefix on
// directories includes dot- and underscore-prefixed files that go:embed
// would otherwise silently drop.
//
// This list defines what "the playbook" is, so the in-guest rsync in
// internal/provision mirrors it as a filter (a repo-mode mount is the whole
// checkout, and the guest must get the same tree either way). Change one, change
// the other; TestGuestSyncCopiesOnlyThePlaybook fails if they drift.
//
//go:embed site.yml ansible.cfg inventory all:roles all:group_vars
var PlaybookFS embed.FS
