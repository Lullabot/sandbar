// server/review.mjs
// Resolves where a finished review is written and writes it, mirroring the
// upstream Electron app's own submit handler (src/main/main.ts): serialize
// the posted ReviewState to XML, then write it with a trailing newline.
// Separated from server/index.mjs so the round trip is unit-testable without
// an HTTP request in the loop.

import { serializeReview } from '@self-review/core';
import { rename, writeFile } from 'node:fs/promises';
import path from 'node:path';

/**
 * Suffix of the single-slot backup writeReview keeps of the review it is about
 * to replace. One slot, not a timestamped series, so a repeatedly-reviewed
 * checkout accumulates exactly one extra file rather than an unbounded pile.
 * @type {string}
 */
export const BACKUP_SUFFIX = '.bak';

/**
 * @self-review/core's own default output file name, used when a config does not
 * name one. Mirrors internal/landreview's `outputFile` const on the Go side.
 * @type {string}
 */
export const DEFAULT_OUTPUT_FILE = 'review.xml';

/**
 * @param {string} repoRoot - Absolute checkout root (from getRepoRootAsync).
 * @param {import('@self-review/core').AppConfig} config
 * @returns {string} Absolute path review.xml is written to.
 */
export function resolveOutputPath(repoRoot, config) {
  // Falls back to @self-review/core's own default rather than throwing on a
  // config without an outputFile. loadConfig() always fills it in, but this is
  // also called to work out which path to keep OUT of the diff (payload.mjs),
  // and a partial config there must degrade to "the default name" instead of
  // taking the whole diff request down with a TypeError.
  return path.resolve(repoRoot, config.outputFile || DEFAULT_OUTPUT_FILE);
}

/**
 * @param {object} options
 * @param {import('@self-review/core').ReviewState} options.state
 * @param {string} options.repoRoot
 * @param {import('@self-review/core').AppConfig} options.config
 * @returns {Promise<string>} The absolute path the review was written to.
 */
export async function writeReview({ state, repoRoot, config }) {
  const outputPath = resolveOutputPath(repoRoot, config);
  const xml = await serializeReview(state, outputPath);

  // Preserve any review already sitting there before replacing it. There is no
  // resume flow yet (the adapter implements no loadResumedReview), so a second
  // `sand land --review` of the same checkout opens an EMPTY panel — and
  // submitting it used to overwrite the earlier review outright. Those comments
  // exist nowhere else: the file never leaves the VM, so an unconditional
  // overwrite was silent, unrecoverable data loss for anyone who reviewed a
  // checkout twice. Renaming costs one file and makes the loss recoverable.
  //
  // A missing source is the normal first-review case, not an error; anything
  // else (a permissions problem, a directory in the way) is worth surfacing,
  // but never at the cost of losing the review being submitted right now — so
  // it degrades to a warning and the write proceeds.
  try {
    await rename(outputPath, outputPath + BACKUP_SUFFIX);
  } catch (err) {
    if (err.code !== 'ENOENT') {
      console.error(`self-review server: could not back up ${outputPath}: ${err.message}`);
    }
  }

  await writeFile(outputPath, xml + '\n', 'utf-8');
  return outputPath;
}
