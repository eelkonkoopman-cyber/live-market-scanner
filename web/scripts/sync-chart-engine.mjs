#!/usr/bin/env node
// Pulls the Blackthorn chart engine's TypeScript SOURCE from AfterBell (its
// source of truth since 2026-09-12 — see AfterBell's own PROVENANCE.md) into
// web/src/vendor/blackthorn-charts/, so the scanner always builds the engine
// from source instead of carrying a stale hand-built .mjs. Re-run this
// (`npm run sync:charts` from web/) after changing the engine in AfterBell.
//
// Usage: node scripts/sync-chart-engine.mjs [sourceDir]
// Source resolution: CLI arg > AFTERBELL_CHARTS_DIR env var > the default
// path below (AfterBell's checkout on this machine).

import { execFileSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const DEFAULT_SOURCE = 'D:/Blackthorn Live/src/lib/vendor/blackthorn-charts';

// Destination is fixed relative to THIS script's own location, never the cwd
// — so `sync:charts` lands in the same place whether run from web/ (the
// normal case, via the npm script) or invoked directly from somewhere else.
const scriptDir = dirname(fileURLToPath(import.meta.url));
const destDir = resolve(scriptDir, '../src/vendor/blackthorn-charts');

const sourceDir = resolve(process.argv[2] || process.env.AFTERBELL_CHARTS_DIR || DEFAULT_SOURCE);

function fail(message) {
  console.error(`sync-chart-engine: ${message}`);
  process.exit(1);
}

if (!existsSync(sourceDir) || !statSync(sourceDir).isDirectory()) {
  fail(`source directory not found: ${sourceDir}`);
}
if (!existsSync(join(sourceDir, 'index.ts')) || !existsSync(join(sourceDir, 'chart.ts'))) {
  fail(`source directory is missing index.ts and/or chart.ts — is this really the engine's directory? ${sourceDir}`);
}

// List every FILE under `dir`, as paths relative to `dir`.
function walkFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const abs = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...walkFiles(abs).map((f) => join(entry.name, f)));
    else if (entry.isFile()) out.push(entry.name);
  }
  return out;
}

// List every DIRECTORY under `dir` (absolute paths), children before parents,
// so deleting empties bottom-up never trips over a still-populated parent.
function walkDirsDeepestFirst(dir) {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const abs = join(dir, entry.name);
    out.push(...walkDirsDeepestFirst(abs), abs);
  }
  return out;
}

// ── mirror: wipe the destination's *.ts files (and any dirs they leave
// empty), then copy every *.ts file from source, preserving relative paths.
// Anything else already in the destination (only PROVENANCE.md, in practice)
// is left alone here — it's rewritten unconditionally below. ───────────────
if (existsSync(destDir)) {
  for (const rel of walkFiles(destDir)) {
    if (rel.endsWith('.ts')) rmSync(join(destDir, rel));
  }
  for (const dir of walkDirsDeepestFirst(destDir)) {
    if (readdirSync(dir).length === 0) rmSync(dir, { recursive: true });
  }
}
mkdirSync(destDir, { recursive: true });

const tsFiles = walkFiles(sourceDir).filter((rel) => rel.endsWith('.ts'));
for (const rel of tsFiles) {
  const destFile = join(destDir, rel);
  mkdirSync(dirname(destFile), { recursive: true });
  copyFileSync(join(sourceDir, rel), destFile);
}

// ── provenance: best-effort. A source dir that isn't a git repo (or no git
// on PATH) must not fail the sync — it just means less detail in
// PROVENANCE.md. Read-only git commands only, and only ever against the
// SOURCE (AfterBell) — never write to that repo. ───────────────────────────
let commitLine = 'unknown (git unavailable or source is not a git repo)';
let uncommittedFiles = [];
try {
  const root = execFileSync('git', ['-C', sourceDir, 'rev-parse', '--show-toplevel'], {
    encoding: 'utf8',
  }).trim();
  commitLine = execFileSync('git', ['-C', root, 'log', '-1', '--format=%h %cs %s'], { encoding: 'utf8' }).trim();
  // git always reports paths with forward slashes regardless of OS — match
  // that when stripping the source prefix below.
  const sourceRel = relative(root, sourceDir).split(sep).join('/');
  const status = execFileSync('git', ['-C', root, 'status', '--porcelain', '--', sourceRel], { encoding: 'utf8' });
  uncommittedFiles = status
    .split('\n')
    .map((line) => line.slice(3).trim())
    .filter(Boolean)
    .map((p) => (p.startsWith(sourceRel + '/') ? p.slice(sourceRel.length + 1) : p));
} catch {
  // git missing or source isn't a repo — leave the defaults above.
}

const hasUncommitted = uncommittedFiles.length > 0;
const syncedAt = new Date().toISOString();

const provenance = `# Blackthorn chart engine — vendored copy

These \`.ts\` files are copied verbatim from AfterBell's chart engine, which
has been the engine's source of truth since 2026-09-12 (see AfterBell's own
PROVENANCE.md next to the source below). \`D:${sep}TradingView\` is NOT the
source of truth for this engine any more — do not sync from it.

- Source: ${sourceDir}
- AfterBell commit: ${commitLine}
- Uncommitted engine changes included: ${hasUncommitted ? `yes — ${uncommittedFiles.join(', ')}` : 'no'}
- Synced: ${syncedAt}
- Files copied: ${tsFiles.length}

## Rules

- Do not hand-edit anything under this directory in the scanner — every file
  here is overwritten by the next sync.
- To change the engine, edit it in AfterBell (the source path above), then
  from \`web/\` run \`npm run sync:charts\` to pull the change into the scanner.
`;
writeFileSync(join(destDir, 'PROVENANCE.md'), provenance);

console.log(
  `sync-chart-engine: copied ${tsFiles.length} files @ ${commitLine} (uncommitted engine changes: ${
    hasUncommitted ? 'yes' : 'no'
  })`
);
