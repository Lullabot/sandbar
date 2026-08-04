// server/payload.mjs
// Assembles the DiffLoadPayload the browser client's ReviewAdapter.loadDiff
// expects, mirroring the sequence the upstream Electron app runs in
// src/main/git-diff-loader.ts + src/main/main.ts's git-mode branch: resolve
// the repo root, run `git diff`, parse it, fold in untracked files as
// synthetic "added" diffs, drop anything the ignore config matches, then
// attach size stats so a caller can flag a large review. Kept dependency-free
// beyond @self-review/core so it is unit-testable against a real scratch
// repo without an HTTP server in the loop.

import {
  getRepoRootAsync,
  runGitDiffAsync,
  parseDiff,
  getUntrackedFilesAsync,
  generateUntrackedDiffs,
  createIgnoreFilter,
  computePayloadStats,
  countTotalLines,
} from '@self-review/core';

/**
 * @param {object} options
 * @param {string} options.repoPath - Path to (or inside) the checkout being reviewed.
 * @param {string|undefined} options.diffArgs - Raw `git diff` argument string,
 *   e.g. "main...HEAD". Undefined/empty reviews the working tree, matching
 *   @self-review/core's own default.
 * @param {import('@self-review/core').AppConfig} options.config
 * @returns {Promise<{files: import('@self-review/core').DiffFile[], source: {type: 'git', gitDiffArgs: string, repository: string}, stats: import('@self-review/core').PayloadStats}>}
 */
export async function buildDiffPayload({ repoPath, diffArgs, config }) {
  const repository = await getRepoRootAsync(repoPath);
  const gitDiffArgs = diffArgs ? diffArgs.split(/\s+/).filter(arg => arg.length > 0) : [];

  const rawDiff = await runGitDiffAsync(gitDiffArgs, repoPath);
  let files = parseDiff(rawDiff);

  const untrackedPaths = await getUntrackedFilesAsync(repoPath);
  if (untrackedPaths.length > 0) {
    const untrackedDiffStr = generateUntrackedDiffs(untrackedPaths, repository);
    if (untrackedDiffStr.length > 0) {
      const untrackedFiles = parseDiff(untrackedDiffStr);
      for (const file of untrackedFiles) {
        file.isUntracked = true;
      }
      files = [...files, ...untrackedFiles];
    }
  }

  const shouldKeep = createIgnoreFilter(config.ignore);
  const filteredFiles = files.filter(f => shouldKeep(f.newPath || f.oldPath));

  const stats = computePayloadStats(filteredFiles.length, countTotalLines(filteredFiles), config);

  return {
    files: filteredFiles,
    source: { type: 'git', gitDiffArgs: gitDiffArgs.join(' '), repository },
    stats,
  };
}
