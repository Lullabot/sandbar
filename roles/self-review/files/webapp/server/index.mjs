#!/usr/bin/env node
// server/index.mjs
// The Node half of the sandbar review web app: an http.createServer (no
// framework — see the plan's minimal-dependency principle) implementing the
// adapter contract @self-review/react's ReviewPanel expects, against a real
// checkout. Binding to 127.0.0.1 is a security property, not a default:
// self-review's whole premise is that unfinished code never leaves the
// machine, so this must never listen on 0.0.0.0. Reachability from the
// workstation is the caller's job (Lima's own forwarding, or an `ssh -L`).
//
// POST /api/review closing the server and exiting 0 is deliberate: it is the
// completion signal the CLI orchestration (task 5) blocks on, so there is no
// separate "are you done" protocol to invent.

import http from 'node:http';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { getRepoRootAsync, loadConfig, checkWritability } from '@self-review/core';

import { buildDiffPayload } from './payload.mjs';
import { resolveOutputPath, writeReview } from './review.mjs';

const WEBAPP_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

const STATIC_CONTENT_TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.json': 'application/json; charset=utf-8',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
};

function parseArgs(argv) {
  const args = {};
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === '--repo' || arg === '--port' || arg === '--diff-args') {
      args[arg.slice(2)] = argv[++i];
    }
  }
  if (!args.repo) {
    throw new Error('--repo <path> is required');
  }
  if (!args.port) {
    throw new Error('--port <n> is required');
  }
  const port = Number(args.port);
  if (!Number.isInteger(port) || port <= 0) {
    throw new Error('--port must be a positive integer');
  }
  return { repo: args.repo, port, diffArgs: args['diff-args'] };
}

function sendJson(res, status, body) {
  const json = JSON.stringify(body);
  res.writeHead(status, {
    'content-type': 'application/json; charset=utf-8',
    'content-length': Buffer.byteLength(json),
  });
  res.end(json);
}

function sendError(res, status, err) {
  const message = err instanceof Error ? err.message : String(err);
  console.error(`[self-review server] ${message}`);
  if (!res.headersSent) {
    sendJson(res, status, { error: message });
  } else {
    res.end();
  }
}

function readRequestBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    req.on('data', chunk => chunks.push(chunk));
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf-8')));
    req.on('error', reject);
  });
}

/**
 * Resolves a request path against `dist/`, refusing anything that would
 * escape it (a `../` request cannot read outside the built bundle).
 */
function resolveStaticPath(distDir, urlPath) {
  const decoded = decodeURIComponent(urlPath);
  const relative = decoded === '/' ? 'index.html' : decoded.replace(/^\/+/, '');
  const resolved = path.resolve(distDir, relative);
  const distWithSep = distDir.endsWith(path.sep) ? distDir : distDir + path.sep;
  if (resolved !== distDir && !resolved.startsWith(distWithSep)) {
    return null;
  }
  return resolved;
}

async function serveStatic(res, distDir, urlPath) {
  const filePath = resolveStaticPath(distDir, urlPath);
  if (!filePath) {
    return sendError(res, 403, new Error('Refusing to serve a path outside dist/'));
  }
  try {
    const data = await readFile(filePath);
    const ext = path.extname(filePath).toLowerCase();
    const contentType = STATIC_CONTENT_TYPES[ext] ?? 'application/octet-stream';
    res.writeHead(200, { 'content-type': contentType, 'content-length': data.length });
    res.end(data);
  } catch (err) {
    if (err && err.code === 'ENOENT') {
      return sendError(
        res,
        500,
        new Error(
          `${path.relative(distDir, filePath) || 'index.html'} not found under dist/ — build the web client first (see roles/self-review/files/webapp's vite build)`
        )
      );
    }
    throw err;
  }
}

async function handleRequest(req, res, ctx) {
  const { repoRoot, diffArgs, config, distDir } = ctx;
  const url = new URL(req.url, 'http://127.0.0.1');

  if (req.method === 'GET' && url.pathname === '/api/diff') {
    const payload = await buildDiffPayload({ repoPath: repoRoot, diffArgs, config });
    return sendJson(res, 200, payload);
  }

  if (req.method === 'GET' && url.pathname === '/api/config') {
    const outputPath = resolveOutputPath(repoRoot, config);
    const outputPathInfo = {
      resolvedOutputPath: outputPath,
      outputPathWritable: checkWritability(outputPath),
    };
    return sendJson(res, 200, { config, outputPathInfo });
  }

  if (req.method === 'POST' && url.pathname === '/api/review') {
    const body = await readRequestBody(req);
    const state = JSON.parse(body);
    const outputPath = await writeReview({ state, repoRoot, config });
    const json = JSON.stringify({ path: outputPath });
    res.writeHead(200, {
      'content-type': 'application/json; charset=utf-8',
      'content-length': Buffer.byteLength(json),
    });
    // Exit is the completion signal task 5's CLI orchestration waits on —
    // do not keep the process running after a submit.
    res.end(json, () => {
      ctx.server.close(() => process.exit(0));
    });
    return;
  }

  if (req.method === 'GET' && !url.pathname.startsWith('/api/')) {
    return serveStatic(res, distDir, url.pathname);
  }

  return sendJson(res, 404, { error: `no route for ${req.method} ${url.pathname}` });
}

async function main() {
  const { repo, port, diffArgs } = parseArgs(process.argv.slice(2));
  const repoPath = path.resolve(repo);

  let repoRoot;
  try {
    repoRoot = await getRepoRootAsync(repoPath);
  } catch (err) {
    console.error(`self-review server: ${repoPath} is not a git repository (${err.message})`);
    process.exit(1);
    return;
  }

  // loadConfig() reads its project-level override from process.cwd(); running
  // from inside the checkout is what makes a `.self-review.yaml` there apply,
  // same as when self-review's own CLI is run from a repo.
  process.chdir(repoRoot);
  const config = loadConfig();
  const distDir = path.join(WEBAPP_ROOT, 'dist');

  const ctx = { repoRoot, diffArgs, config, distDir };
  const server = http.createServer((req, res) => {
    handleRequest(req, res, ctx).catch(err => sendError(res, 500, err));
  });
  ctx.server = server;

  server.listen(port, '127.0.0.1', () => {
    console.log(`self-review server listening on 127.0.0.1:${port} (repo: ${repoRoot})`);
  });
}

main();
