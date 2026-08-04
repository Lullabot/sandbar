// server/review.test.mjs
// Round-trips a minimal ReviewState through the same writeReview() path the
// POST /api/review handler calls, and reads the resulting review.xml back
// off disk — proving the write actually reaches the filesystem, not just
// that serializeReview() returns a string that looks right.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

import { writeReview, resolveOutputPath } from './review.mjs';

function fakeConfig(overrides = {}) {
  return { outputFile: './review.xml', ...overrides };
}

test('writeReview writes review.xml with the comment body, path and line', async t => {
  const repoRoot = mkdtempSync(path.join(tmpdir(), 'self-review-review-'));
  t.after(() => rmSync(repoRoot, { recursive: true, force: true }));

  const state = {
    timestamp: '2026-08-04T12:00:00.000Z',
    source: { type: 'git', gitDiffArgs: '', repository: repoRoot },
    files: [
      {
        path: 'src/foo.txt',
        changeType: 'modified',
        viewed: true,
        comments: [
          {
            id: 'c1',
            filePath: 'src/foo.txt',
            lineRange: { side: 'new', start: 7, end: 7 },
            body: 'This looks wrong.',
            category: 'bug',
            suggestion: null,
          },
        ],
      },
    ],
  };

  const outputPath = await writeReview({ state, repoRoot, config: fakeConfig() });

  assert.equal(outputPath, path.resolve(repoRoot, 'review.xml'));
  assert.ok(existsSync(outputPath), 'expected review.xml to exist on disk');

  const xml = readFileSync(outputPath, 'utf-8');
  assert.match(xml, /<body>This looks wrong\.<\/body>/);
  assert.match(xml, /path="src\/foo\.txt"/);
  assert.match(xml, /new-line-start="7"/);
  assert.match(xml, /new-line-end="7"/);
});

test('resolveOutputPath resolves outputFile against the repo root', () => {
  const resolved = resolveOutputPath('/repo/root', fakeConfig({ outputFile: 'out/review.xml' }));
  assert.equal(resolved, path.resolve('/repo/root', 'out/review.xml'));
});
