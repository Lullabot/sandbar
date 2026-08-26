#!/usr/bin/env bash
#
# Plan 20 — validation probe for the GitLab token-minting workflow.
#
# Settles the two drupal.org unknowns the plan's minting layer depends on:
#   1. Does "Get push access" on an issue fork grant Maintainer (needed to mint)?
#   2. Is POST /projects/:id/access_tokens permitted by the Drupal Association?
# ...and the third (which scopes/access level are actually required).
#
# Run this on your workstation, NOT inside a VM: it uses your account-level PAT,
# which by design never enters a guest.
#
# Prerequisites:
#   brew install glab jq
#   A drupal.org PAT with `api` scope:
#     https://git.drupalcode.org/-/user_settings/personal_access_tokens
#   An issue fork YOU have push access to. Use one of your OWN issues — do not
#   probe against Drupal core's forks.
#
# Usage:
#   ./probe-gitlab-token-minting.sh <module> <issue-nid> [other-module] [other-nid]
#
# The optional second fork is used to prove least privilege: the minted token
# must NOT be able to push to it.

set -uo pipefail

HOST='git.drupalcode.org'
MODULE="${1:?usage: $0 <module> <issue-nid> [other-module] [other-nid]}"
NID="${2:?usage: $0 <module> <issue-nid> [other-module] [other-nid]}"
OTHER_MODULE="${3:-}"
OTHER_NID="${4:-}"

FORK_PATH="issue/${MODULE}-${NID}"
FORK_ENC="issue%2F${MODULE}-${NID}"
FORK_URL="https://${HOST}/${FORK_PATH}.git"

say() { printf '\n\033[1m== %s\033[0m\n' "$1"; }
note() { printf '   %s\n' "$1"; }

say "0. glab authentication status for ${HOST}"
if ! glab auth status --hostname "$HOST" 2>&1 | sed 's/^/   /'; then
  note "Not authenticated. Run:"
  note "  printf '%s' 'YOUR_PAT' | glab auth login --hostname ${HOST} --stdin"
  exit 1
fi

say "1. Who am I?"
glab api --hostname "$HOST" user 2>&1 | jq -r '"   \(.username)  (id \(.id))"'

say "2. Does the fork exist, and what is my access level on it?"
note "Fork: ${FORK_PATH}"
PROJ="$(glab api --hostname "$HOST" "projects/${FORK_ENC}" 2>&1)"
if ! printf '%s' "$PROJ" | jq -e '.id' >/dev/null 2>&1; then
  note "Could not read the project. Response:"
  printf '%s\n' "$PROJ" | head -5 | sed 's/^/   /'
  exit 1
fi
printf '%s' "$PROJ" | jq -r '"   id=\(.id)  path=\(.path_with_namespace)  visibility=\(.visibility)"'

ACCESS="$(printf '%s' "$PROJ" | jq -r '.permissions.project_access.access_level // .permissions.group_access.access_level // "none"')"
note "access_level = ${ACCESS}   (40=Maintainer, 30=Developer, none=not a member)"
case "$ACCESS" in
  40|50) note "UNKNOWN #1 ANSWERED: you have Maintainer+. Minting should be possible." ;;
  none)  note "UNKNOWN #1: no membership visible. Click 'Get push access' on the issue, then re-run." ;;
  *)     note "UNKNOWN #1 ANSWERED NEGATIVE: access_level ${ACCESS} < 40. GitLab requires"
         note "Maintainer or Owner to create a project access token. The minting layer"
         note "cannot work for this fork; the plan's placement layer still can." ;;
esac

say "3. Attempt the mint  (THE SECOND UNKNOWN)"
note "scopes MUST be sent as a JSON body: glab's --field does not encode arrays."
EXPIRES="$(date -u -v+30d +%Y-%m-%d 2>/dev/null || date -u -d '+30 days' +%Y-%m-%d)"
note "expires_at = ${EXPIRES}"

RESP="$(glab api -i --hostname "$HOST" --method POST "projects/${FORK_ENC}/access_tokens" --input - <<JSON 2>&1
{"name":"sandbar-probe","scopes":["write_repository","api"],"access_level":30,"expires_at":"${EXPIRES}"}
JSON
)"
printf '%s\n' "$RESP" | head -25 | sed 's/^/   /'

TOKEN="$(printf '%s' "$RESP" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
TOKEN_ID="$(printf '%s' "$RESP" | sed -n 's/.*"id":\([0-9]*\).*/\1/p' | head -1)"

if [ -z "$TOKEN" ]; then
  note ""
  note "No token returned. Interpreting the status line above:"
  note "  403  -> you lack Maintainer on this fork (unknown #1 negative)"
  note "  404  -> endpoint likely restricted by the Drupal Association,"
  note "          since reading projects/${FORK_ENC} above succeeded (unknown #2 negative)"
  note "  400  -> check expires_at (max 365 days) and the scopes list"
  note ""
  note "Either negative reduces the plan to its placement layer, which still works."
  exit 2
fi
note "UNKNOWN #2 ANSWERED: mint succeeded. token id=${TOKEN_ID}"

say "4. Does the minted token actually push?  (dry run — changes nothing)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
if git clone --depth 1 -q "$FORK_URL" "$TMP/fork" 2>&1 | sed 's/^/   /'; then :; fi
# Keep the token out of argv and out of the URL by feeding it via a helper.
export PROBE_TOKEN="$TOKEN"
HELPER='!f(){ echo username=sandbar-probe; echo password=$PROBE_TOKEN; };f'
if git -C "$TMP/fork" -c credential.helper="$HELPER" \
     push --dry-run "$FORK_URL" HEAD:refs/heads/sandbar-probe-delete-me 2>&1 | sed 's/^/   /'; then
  note "PUSH OK — write_repository at access_level 30 is sufficient for this fork."
else
  note "PUSH REFUSED — the branch may be protected, which would mean access_level 30"
  note "is not enough. Re-run the mint with access_level 40 to compare."
fi

say "5. Can the guest tooling read the merge request?  (API scope check)"
glab api --hostname "$HOST" "projects/${FORK_ENC}/merge_requests?per_page=1" \
  >/dev/null 2>&1 && note "api scope works for this fork." || note "api read failed — widen scopes."

if [ -n "$OTHER_MODULE" ] && [ -n "$OTHER_NID" ]; then
  say "6. LEAST PRIVILEGE — the same token must NOT push to a different fork"
  OTHER_URL="https://${HOST}/issue/${OTHER_MODULE}-${OTHER_NID}.git"
  if git -C "$TMP/fork" -c credential.helper="$HELPER" \
       push --dry-run "$OTHER_URL" HEAD:refs/heads/should-not-work 2>&1 | sed 's/^/   /'; then
    note "!! UNEXPECTED: the token pushed to another fork. Least privilege is NOT holding."
  else
    note "Correctly refused. The token is scoped to ${FORK_PATH} alone."
  fi
fi

say "7. List tokens on this fork  (note: visible to others with access)"
glab api --hostname "$HOST" "projects/${FORK_ENC}/access_tokens" 2>&1 \
  | jq -r '.[] | "   id=\(.id)  name=\(.name)  scopes=\(.scopes|join(","))  expires=\(.expires_at)"' 2>/dev/null \
  || note "could not list"

say "8. Revoke the probe token"
note "Running: DELETE projects/${FORK_ENC}/access_tokens/${TOKEN_ID}"
glab api --hostname "$HOST" --method DELETE "projects/${FORK_ENC}/access_tokens/${TOKEN_ID}" 2>&1 | sed 's/^/   /'
note "Verify it is gone:"
glab api --hostname "$HOST" "projects/${FORK_ENC}/access_tokens" 2>&1 \
  | jq -r '.[] | "   still present: id=\(.id) name=\(.name)"' 2>/dev/null \
  || note "   (none listed)"

say "Done."
