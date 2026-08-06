// server/expand.test.mjs
// Exercises expandFileContext against a real git repository, because the whole
// question it answers — "does a wider -U actually return more of the file" — is
// a property of git's output, not of anything worth mocking.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, realpathSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

import { expandFileContext } from './expand.mjs';

function git(cwd, ...args) {
  execFileSync('git', args, { cwd, stdio: 'pipe' });
}

/** A repo with a long file and a one-line change in the middle of it, so a
 *  narrow diff shows a few lines and a wide one shows the whole file. */
function makeScratchRepo() {
  const dir = realpathSync(mkdtempSync(path.join(tmpdir(), 'self-review-expand-')));
  git(dir, 'init', '--initial-branch=main');
  git(dir, 'config', 'user.email', 'test@example.com');
  git(dir, 'config', 'user.name', 'Test');

  const lines = Array.from({ length: 60 }, (_, i) => `line ${i + 1}`);
  writeFileSync(path.join(dir, 'long.txt'), lines.join('\n') + '\n');
  git(dir, 'add', 'long.txt');
  git(dir, 'commit', '-m', 'initial commit');

  lines[29] = 'line 30 CHANGED';
  writeFileSync(path.join(dir, 'long.txt'), lines.join('\n') + '\n');
  return dir;
}

function lineCount(hunks) {
  return hunks.reduce((n, h) => n + (h.lines ? h.lines.length : 0), 0);
}

test('expandFileContext returns more of the file as the context widens', async t => {
  const dir = makeScratchRepo();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  const narrow = await expandFileContext({
    repoPath: dir,
    diffArgs: undefined,
    filePath: 'long.txt',
    contextLines: 3,
  });
  const wide = await expandFileContext({
    repoPath: dir,
    diffArgs: undefined,
    filePath: 'long.txt',
    contextLines: 100000,
  });

  assert.ok(lineCount(narrow.hunks) > 0, 'the narrow expansion returned no lines at all');
  assert.ok(
    lineCount(wide.hunks) > lineCount(narrow.hunks),
    `a wider context must reveal more lines: narrow=${lineCount(narrow.hunks)} wide=${lineCount(wide.hunks)}`
  );
  // The whole file must be on screen, which is what the panel's "show all
  // hidden lines" affordance promises. Asserted by content rather than by an
  // exact line count: how the parser accounts for the changed pair is its
  // business, but the first and last lines of the file either made it into the
  // expansion or they did not.
  const wideText = wide.hunks.flatMap(h => h.lines || []).map(l => l.content ?? '').join('\n');
  assert.match(wideText, /line 1\b/, 'the widest expansion is missing the start of the file');
  assert.match(wideText, /line 60\b/, 'the widest expansion is missing the end of the file');
  const narrowText = narrow.hunks.flatMap(h => h.lines || []).map(l => l.content ?? '').join('\n');
  assert.doesNotMatch(narrowText, /line 1\b/, 'the narrow expansion should NOT already show the whole file, or this proves nothing');
});

test('expandFileContext rejects a nonsensical context width', async t => {
  const dir = makeScratchRepo();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  await assert.rejects(
    () => expandFileContext({ repoPath: dir, diffArgs: undefined, filePath: 'long.txt', contextLines: -1 }),
    /invalid contextLines/
  );
});

test('expandFileContext returns an empty expansion for a file with no diff', async t => {
  const dir = makeScratchRepo();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  const got = await expandFileContext({
    repoPath: dir,
    diffArgs: undefined,
    filePath: 'does-not-exist.txt',
    contextLines: 10,
  });
  assert.deepEqual(got, { hunks: [], totalLines: 0 });
});
