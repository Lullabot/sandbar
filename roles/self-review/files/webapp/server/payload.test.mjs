// server/payload.test.mjs
// Exercises buildDiffPayload against a real, throwaway git repository — the
// only way to prove the untracked-file and ignore-filter wiring actually
// works, per AGENTS.md's "test the far side" rule. No mocking of git itself.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, realpathSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

import { buildDiffPayload } from './payload.mjs';

/** Minimal AppConfig shape sufficient for createIgnoreFilter/computePayloadStats. */
function fakeConfig(overrides = {}) {
  return {
    ignore: [],
    maxFiles: 500,
    maxTotalLines: 100000,
    ...overrides,
  };
}

function git(cwd, ...args) {
  execFileSync('git', args, { cwd, stdio: 'pipe' });
}

/**
 * Builds a scratch git repo with:
 *  - a committed baseline file, then modified (a tracked change)
 *  - one untracked file that is not ignored
 * Returns the repo's real (symlink-resolved) path, matching what
 * `git rev-parse --show-toplevel` reports on macOS/Linux temp dirs.
 */
function makeScratchRepo() {
  const dir = realpathSync(mkdtempSync(path.join(tmpdir(), 'self-review-payload-')));
  git(dir, 'init', '--initial-branch=main');
  git(dir, 'config', 'user.email', 'test@example.com');
  git(dir, 'config', 'user.name', 'Test');

  const trackedPath = path.join(dir, 'tracked.txt');
  writeFileSync(trackedPath, 'line one\nline two\n');
  git(dir, 'add', 'tracked.txt');
  git(dir, 'commit', '-m', 'initial commit');

  writeFileSync(trackedPath, 'line one\nline two changed\n');

  const untrackedPath = path.join(dir, 'new-file.txt');
  writeFileSync(untrackedPath, 'brand new content\n');

  return dir;
}

test('buildDiffPayload names the modified and untracked files, each with hunks', async t => {
  const repoPath = makeScratchRepo();
  t.after(() => rmSync(repoPath, { recursive: true, force: true }));

  const payload = await buildDiffPayload({
    repoPath,
    diffArgs: undefined,
    config: fakeConfig(),
  });

  assert.equal(payload.source.type, 'git');
  assert.equal(payload.source.repository, repoPath);

  const byPath = new Map(payload.files.map(f => [f.newPath || f.oldPath, f]));

  const tracked = byPath.get('tracked.txt');
  assert.ok(tracked, 'expected tracked.txt in the payload');
  assert.ok(tracked.hunks.length > 0, 'expected tracked.txt to carry at least one hunk');
  assert.equal(tracked.isUntracked, undefined);

  const untracked = byPath.get('new-file.txt');
  assert.ok(untracked, 'expected new-file.txt (untracked) in the payload');
  assert.ok(untracked.hunks.length > 0, 'expected new-file.txt to carry at least one hunk');
  assert.equal(untracked.isUntracked, true);

  assert.equal(payload.stats.fileCount, 2);
});

test('buildDiffPayload filters files matched by the ignore config', async t => {
  const repoPath = makeScratchRepo();
  t.after(() => rmSync(repoPath, { recursive: true, force: true }));

  const payload = await buildDiffPayload({
    repoPath,
    diffArgs: undefined,
    config: fakeConfig({ ignore: ['new-file.txt'] }),
  });

  const paths = payload.files.map(f => f.newPath || f.oldPath);
  assert.ok(paths.includes('tracked.txt'));
  assert.ok(!paths.includes('new-file.txt'));
});

// A finished review lands INSIDE the checkout, so without an explicit
// exclusion the next review of that checkout lists it — and the backup
// writeReview keeps beside it — as untracked "added" files, showing the
// reviewer their own previous comments as code to review. @self-review/core's
// default ignore list does not cover them (upstream's Electron app writes the
// file somewhere it does not re-scan), and nothing adds them to .gitignore.
test('buildDiffPayload never lists the review output file or its backup', async t => {
  const dir = makeScratchRepo();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  writeFileSync(path.join(dir, 'review.xml'), '<review/>\n');
  writeFileSync(path.join(dir, 'review.xml.bak'), '<review/>\n');

  const payload = await buildDiffPayload({
    repoPath: dir,
    diffArgs: undefined,
    config: fakeConfig({ outputFile: 'review.xml' }),
  });

  const named = payload.files.map(f => f.newPath || f.oldPath);
  assert.ok(!named.includes('review.xml'), `review.xml must not be reviewable: ${named.join(', ')}`);
  assert.ok(
    !named.includes('review.xml.bak'),
    `the review backup must not be reviewable: ${named.join(', ')}`
  );
  // The real work must still be there — an over-broad exclusion would pass the
  // two assertions above by returning nothing at all.
  assert.ok(named.includes('tracked.txt'), `the modified file is missing: ${named.join(', ')}`);
  assert.ok(named.includes('new-file.txt'), `the untracked file is missing: ${named.join(', ')}`);
});

// The same must hold when a project's .self-review.yaml puts the review
// somewhere other than the default name.
test('buildDiffPayload honours a configured outputFile when excluding the review', async t => {
  const dir = makeScratchRepo();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  writeFileSync(path.join(dir, 'my-review.xml'), '<review/>\n');

  const payload = await buildDiffPayload({
    repoPath: dir,
    diffArgs: undefined,
    config: fakeConfig({ outputFile: 'my-review.xml' }),
  });

  const named = payload.files.map(f => f.newPath || f.oldPath);
  assert.ok(!named.includes('my-review.xml'), `configured outputFile must not be reviewable: ${named.join(', ')}`);
});
