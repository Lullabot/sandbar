# Releases

How `sand` releases are cut and published.

## The pipeline

Releases run through `.github/workflows/release-please.yml` in two chained
jobs, `release-please` then `goreleaser`:

1. **release-please** watches every push to `main` and maintains a
   standing release PR that collects the changelog from
   [Conventional Commit](https://www.conventionalcommits.org) messages.
   Its configuration (`release-please-config.json`) sets `release-type:
   go`, `bump-minor-pre-major: true`, and — the part that matters for the
   next step — `draft: true` and `force-tag-creation: true`. Merging the
   release PR creates the `vX.Y.Z` tag and a **draft** GitHub Release
   rather than a published one.
2. **GoReleaser** (`.goreleaser.yaml`) then adopts that draft
   (`use_existing_draft: true`), cross-compiles `sand` for
   darwin+linux × amd64+arm64 with `CGO_ENABLED=0`, uploads the archives
   into the draft, publishes it, and pushes an updated formula to the
   `lullabot/homebrew-sandbar` tap.

Both steps live in one workflow, chained with `needs`, for a specific
reason: this repository has GitHub's immutable-releases setting turned on,
which freezes a release the instant it is *published* — no further assets
can ever be attached. release-please therefore leaves its release as a
draft, and GoReleaser is the one that publishes it, only after every
archive has been uploaded. A separate tag-triggered workflow would race the
draft's creation and risk a GoReleaser run that finds no draft to adopt,
which creates and publishes a release of its own and burns that version
number permanently.

## The Homebrew tap

GoReleaser's `brews:` publisher (not `homebrew_casks:`) pushes to
`lullabot/homebrew-sandbar`. A cask is deliberately not used: casks are
macOS-only, and `sand` needs to `brew install` on both macOS and Linux, so
a formula is the only cross-platform option.

## Docs releases

A release tag also triggers the docs publishing workflow, which builds and
publishes that version of this site with `mike` and moves the `latest`
alias to point at it. See the docs workflow itself for the exact trigger
and versioning behaviour.

## The one manual step

GitHub Pages must be pointed at the `gh-pages` branch once, by a repository
admin: **Settings → Pages → Build and deployment → Deploy from a branch →
`gh-pages` / `(root)`**. The docs workflow creates the `gh-pages` branch on
its first run, but the site returns a 404 until this setting is switched —
automation cannot flip it, since it requires repository admin access. This
is the one thing every future maintainer needs to know to unblock a fresh
fork or a repository transfer.
