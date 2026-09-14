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
// roles/ is enumerated role-by-role instead of a single blanket `all:roles` —
// this used to be one line, and changing it back looks like a harmless
// simplification. It is not: go:embed embeds whatever is on disk at build
// time, and does NOT consult .gitignore. roles/self-review/files/webapp/
// carries a real (gitignored) node_modules once `npm ci` has run there
// (measured at 358MB) and a dist/ once `npm run build` has (5.7MB) — both
// routine local development state for anyone working on that web app, not
// something exotic. A blanket `all:roles` swept both into the compiled
// binary: `go build ./cmd/sand` went from 16.7MB to 288.7MB with those
// directories present, and because internal/provision rsyncs this embedded
// tree into every guest it provisions, that bloat would have shipped into
// every VM too. Enumerating roles/self-review down to its individual files
// (rather than embedding roles/self-review/files/webapp wholesale) closes
// that off by construction: node_modules and dist are never named, so they
// can never be swept in no matter what a developer's checkout happens to
// have on disk. The cost is that a future role's directory must be added
// here explicitly — go:embed has no exclusion syntax, so there is no way to
// keep the one-line form and still exclude a subtree only some developers'
// disks will ever have.
//
//go:embed site.yml ansible.cfg inventory all:group_vars
//go:embed all:roles/base all:roles/claude-code all:roles/codex
//go:embed all:roles/dev-tools all:roles/project all:roles/samba all:roles/user
//go:embed all:roles/self-review/defaults all:roles/self-review/tasks
//go:embed roles/self-review/files/webapp/package.json
//go:embed roles/self-review/files/webapp/package-lock.json
//go:embed roles/self-review/files/webapp/index.html
//go:embed roles/self-review/files/webapp/vite.config.ts
//go:embed roles/self-review/files/webapp/tsconfig.json
//go:embed all:roles/self-review/files/webapp/server
//go:embed all:roles/self-review/files/webapp/src
var PlaybookFS embed.FS
