// server/expand.mjs
// Re-runs the review's own `git diff` for ONE file at a wider context width, so
// the browser client can answer the panel's "show all hidden lines" affordance.
//
// The panel renders an expand bar between every pair of hunks whenever the diff
// source is git — which it always is here — and calls adapter.expandContext to
// fill it. With no such method the bar flipped to its loading state and stayed
// there forever, silently: a control the UI offered on every tracked file and
// could never satisfy.
//
// Expansion is just `git diff -U<n>` over the same range and the same file, so
// the hunks come from the same parser the initial payload uses and cannot drift
// from it. Kept beside payload.mjs (rather than inside it) because it answers a
// different request and is unit-testable on its own.

import { runGitDiffAsync, parseDiff, countTotalLines } from '@self-review/core';

/**
 * Upper bound on the context width a client may ask for. `git diff -U` takes
 * any non-negative integer, and "show everything" is spelled as a very large
 * number rather than a flag, so this caps the value into something git will
 * accept without letting an arbitrary request through unchecked.
 * @type {number}
 */
export const MAX_CONTEXT_LINES = 1000000;

/**
 * @param {object} options
 * @param {string} options.repoPath - Path to (or inside) the checkout.
 * @param {string|undefined} options.diffArgs - The session's `git diff` range,
 *   identical to the one buildDiffPayload used, so an expansion is the same
 *   comparison seen more widely rather than a different one.
 * @param {string} options.filePath - Repo-relative path of the file to expand.
 * @param {number} options.contextLines - Requested context width.
 * @returns {Promise<{hunks: import('@self-review/core').DiffHunk[], totalLines: number}>}
 */
export async function expandFileContext({ repoPath, diffArgs, filePath, contextLines }) {
  const requested = Number(contextLines);
  if (!Number.isFinite(requested) || requested < 0) {
    throw new Error(`invalid contextLines: ${contextLines}`);
  }
  const width = Math.min(Math.floor(requested), MAX_CONTEXT_LINES);

  const range = diffArgs ? diffArgs.split(/\s+/).filter(a => a.length > 0) : [];
  // `--` separates the range from the pathspec, so a file whose name could read
  // as a revision cannot be reinterpreted as one.
  const args = [`-U${width}`, ...range, '--', filePath];

  const raw = await runGitDiffAsync(args, repoPath);
  const files = parseDiff(raw);
  const match = files.find(f => (f.newPath || f.oldPath) === filePath) || files[0];
  if (!match) {
    // A file with no diff at this width is not an error — it is an empty
    // expansion, and the panel treats it as nothing further to show.
    return { hunks: [], totalLines: 0 };
  }
  return { hunks: match.hunks || [], totalLines: countTotalLines([match]) };
}
