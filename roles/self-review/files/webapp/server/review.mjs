// server/review.mjs
// Resolves where a finished review is written and writes it, mirroring the
// upstream Electron app's own submit handler (src/main/main.ts): serialize
// the posted ReviewState to XML, then write it with a trailing newline.
// Separated from server/index.mjs so the round trip is unit-testable without
// an HTTP request in the loop.

import { serializeReview } from '@self-review/core';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';

/**
 * @param {string} repoRoot - Absolute checkout root (from getRepoRootAsync).
 * @param {import('@self-review/core').AppConfig} config
 * @returns {string} Absolute path review.xml is written to.
 */
export function resolveOutputPath(repoRoot, config) {
  return path.resolve(repoRoot, config.outputFile);
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
  await writeFile(outputPath, xml + '\n', 'utf-8');
  return outputPath;
}
