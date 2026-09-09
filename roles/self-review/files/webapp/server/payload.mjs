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
import path from 'node:path';

import { BACKUP_SUFFIX, resolveOutputPath } from './review.mjs';

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

  // The review's own output file is never part of the diff. @self-review/core's
  // default ignore list does not mention it (it is written by the Electron app
  // into a directory the app does not then re-scan), and nothing here adds it
  // to the checkout's .gitignore — so a second review of the same checkout
  // listed the reviewer's PREVIOUS review as an untracked "added" file and
  // presented their own comments back to them as code to review. The single-slot
  // backup writeReview keeps goes with it, for the same reason.
  const outputPath = resolveOutputPath(repository, config);
  const outputRel = path.relative(repository, outputPath);
  const outputRelBackup = outputRel + BACKUP_SUFFIX;

  const shouldKeep = createIgnoreFilter(config.ignore);
  const filteredFiles = files.filter(f => {
    const p = f.newPath || f.oldPath;
    if (p === outputRel || p === outputRelBackup) {
      return false;
    }
    return shouldKeep(p);
  });

  const stats = computePayloadStats(filteredFiles.length, countTotalLines(filteredFiles), config);

  return {
    files: filteredFiles,
    source: { type: 'git', gitDiffArgs: gitDiffArgs.join(' '), repository },
    stats,
  };
}

/**
 * The set of repo-relative paths a client is allowed to ask about, taken from
 * a payload buildDiffPayload has already produced.
 *
 * This exists because /api/expand takes a file path straight off the query
 * string and hands it to git. Without a check, that is an arbitrary,
 * unauthenticated argument reaching a subprocess: the value is attacker-chosen
 * whenever the reviewer has any other page open, since a cross-origin GET to
 * 127.0.0.1 needs no preflight and the attacker never has to read the reply to
 * have caused the call. @self-review/core passes git arguments as argv rather
 * than through a shell (upstream's own fix for exactly this class of bug), so
 * the metacharacter half is closed there — but "the dependency happens to be
 * safe today" is not the property this server should rely on, and it says
 * nothing about which FILES may be read. Answering only for paths the reviewer
 * was already shown is a property this server can hold on its own.
 *
 * Both sides of a rename are included: the panel identifies a file by newPath
 * for everything except a deletion, where oldPath is all there is.
 *
 * @param {{files: import('@self-review/core').DiffFile[]}} payload
 * @returns {Set<string>}
 */
export function reviewablePaths(payload) {
  const paths = new Set();
  for (const file of payload.files) {
    if (file.newPath) paths.add(file.newPath);
    if (file.oldPath) paths.add(file.oldPath);
  }
  return paths;
}
