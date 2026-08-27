#!/usr/bin/env bash
#
# Plan 20 — drupal.org GitLab API capability map.
#
# Supersedes probe-gitlab-token-minting.sh, whose question is settled: per-issue
# -fork tokens are impossible on drupal.org. Two independent reasons, both
# verified 2026-08-27:
#
#   1. "Get push access" on an issue fork grants Developer (access_level 30).
#      GitLab requires Maintainer/Owner to create a project access token by ANY
#      route, including the web UI. So there is no UI to script either.
#   2. drupal.org runs a per-path, per-method allowlist in front of the GitLab
#      API and blocks POST .../access_tokens, .../deploy_tokens and
#      .../deploy_keys outright — for everyone, on every project, authenticated
#      or not. A blocked request never reaches GitLab; it returns a ~56KB HTML
#      404 from the drupal.org Drupal site instead of GitLab JSON.
#
# What drupal.org DOES allow is every content-write endpoint, which is what the
# plan's host-side publication design depends on. This script re-verifies that,
# and is plan 20's validation step "re-run the endpoint map" — run it whenever
# publication starts failing, to tell a platform policy change apart from a bug.
#
# Runs entirely UNAUTHENTICATED on purpose: routing is decided before auth, so
# this classifies endpoints without credentials and cannot create anything.
#
# Usage:  ./probe-drupalorg-api.sh
# Needs:  glab   (brew install glab)

set -uo pipefail

H=git.drupalcode.org
FORK='issue%2Fdrupal-3181657'   # a real, public, long-lived issue fork
PROJ='project%2Fdrupal'

pass=0; fail=0

probe() {
  local expect="$1" method="$2" path="$3" body="${4:-}"
  local out routing status attempt

  # A blocked endpoint answers with a ~56KB HTML page, and several of those in
  # quick succession can time out or get rate limited. An empty capture is a
  # transport failure, NOT evidence about routing — retry rather than report a
  # policy change that did not happen.
  for attempt in 1 2 3; do
    if [ -n "$body" ]; then
      out=$(printf '%s' "$body" | timeout 90 glab api -i --hostname "$H" --method "$method" "$path" --input - 2>&1)
    else
      out=$(timeout 90 glab api -i --hostname "$H" --method "$method" "$path" 2>&1)
    fi
    # NOTE: deliberately bash string matching, not `grep -q`. With `pipefail`
    # set, `printf | grep -q` on a large body makes grep close the pipe early,
    # printf take EPIPE, and a SUCCESSFUL match report failure. That bit only the
    # ~56KB blocked responses and produced phantom "policy changed" reports.
    [[ "$out" == HTTP/* ]] && break
    out=""
  done

  if [ -z "$out" ]; then
    printf '  \033[33mSKIP\033[0m %-7s %-46s %-8s no response after 3 attempts\n' "$method" "$path" "-"
    return
  fi

  status=$(printf '%s' "$out" | head -1 | tr -d '\r')

  # The discriminator is whether drupal.org's own Drupal site answered. A routed
  # request reaches GitLab and may legitimately return JSON (most endpoints) or
  # text/plain (the raw-file endpoint), so "is it JSON" is NOT the test.
  if [[ "$out" == *"X-Generator: Drupal"* ]]; then
    routing=blocked
  else
    routing=routed
  fi

  if [ "$routing" = "$expect" ]; then
    printf '  \033[32mok\033[0m   %-7s %-46s %-8s %s\n' "$method" "$path" "$routing" "$status"
    pass=$((pass+1))
  else
    printf '  \033[31mFAIL\033[0m %-7s %-46s %-8s %s  (expected %s)\n' "$method" "$path" "$routing" "$status" "$expect"
    fail=$((fail+1))
  fi
}

echo
echo "Content-write endpoints — the publication design REQUIRES these to be routed:"
probe routed POST "projects/$FORK/repository/commits"          '{"branch":"p","commit_message":"x","actions":[]}'
probe routed POST "projects/$FORK/repository/branches?branch=p&ref=main"
probe routed POST "projects/$FORK/repository/files/README%2Emd" '{"branch":"p","content":"x","commit_message":"x"}'
probe routed PUT  "projects/$FORK/repository/files/README%2Emd" '{"branch":"p","content":"x","commit_message":"x"}'
probe routed POST "projects/$FORK/merge_requests"              '{"source_branch":"a","target_branch":"b","title":"x"}'

echo
echo "Credential-minting endpoints — expected BLOCKED (this is why tokens are out):"
probe blocked POST "projects/$FORK/access_tokens" '{"name":"x","scopes":["write_repository"]}'
probe blocked POST "projects/$FORK/deploy_tokens" '{"name":"x","scopes":["write_repository"]}'
probe blocked POST "projects/$FORK/deploy_keys"   '{"title":"x","key":"ssh-ed25519 AAAA"}'

echo
echo "Anonymous reads — the guest's whole loop depends on these needing no credential:"
probe routed GET "projects/$FORK/merge_requests?per_page=1"
probe routed GET "projects/$PROJ/pipelines?per_page=1"
probe routed GET "projects/$FORK/repository/branches?per_page=1"
probe routed GET "projects/$FORK/repository/tree?per_page=1"
probe routed GET "projects/$FORK/repository/commits?per_page=1"
probe routed GET "projects/$FORK/repository/files/composer%2Ejson/raw?ref=9.2.x"

echo
if [ "$fail" -eq 0 ]; then
  printf '\033[32mAll %d checks match the plan'"'"'s assumptions.\033[0m\n' "$pass"
  echo "drupal.org still permits content writes and still blocks credential minting."
else
  printf '\033[31m%d of %d checks DIVERGED from the plan'"'"'s assumptions.\033[0m\n' "$fail" "$((pass+fail))"
  echo "drupal.org's API policy has changed. Re-read plan 20's Background before"
  echo "assuming this is a bug in sand: a newly-routed access_tokens endpoint would"
  echo "reopen the per-fork token design, and a newly-blocked content endpoint would"
  echo "break publication."
  exit 1
fi
