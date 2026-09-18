// Unit tests for strings_scan.js — the source of every key in
// palbase/strings.json (FR-015) and of the build's "not a literal" warning
// (FR-016).
//
// Every negative fixture still mentions @palbase/backend: the scanner skips a
// file that never names the package, and a fixture that is skipped for that
// reason proves nothing about symbol resolution.
//
// Run: node --test internal/backend/devjs/strings_scan.test.js
// (with NODE_PATH at the pinned typescript — runDevJSSuite provides it)

'use strict';

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { scan } = require('./strings_scan.js');

const SCANNER = path.join(__dirname, 'strings_scan.js');

// project writes `files` (root-relative path → contents) under a fresh temp
// root and removes it when the test ends.
function project(t, files) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'strings-scan-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  for (const [rel, src] of Object.entries(files)) {
    const full = path.join(root, rel);
    fs.mkdirSync(path.dirname(full), { recursive: true });
    fs.writeFileSync(full, src);
  }
  return root;
}

test("named, aliased and namespace imports of @palbase/backend's t are collected", (t) => {
  const root = project(t, {
    'controllers/named.ts': [
      'import { t } from "@palbase/backend";',
      'export const a = t("A");',
    ].join('\n'),
    'controllers/aliased.ts': [
      "import { t as tr } from '@palbase/backend';",
      "export const b = tr('B');",
    ].join('\n'),
    'controllers/namespace.ts': [
      'import * as B from "@palbase/backend";',
      'export const c = B.t(`C`);',
    ].join('\n'),
  });
  assert.deepStrictEqual(scan(root), { keys: ['A', 'B', 'C'], dynamic: [] });
});

test("a t that is not @palbase/backend's is not collected", (t) => {
  const root = project(t, {
    'controllers/local.ts': [
      'import { Database } from "@palbase/backend";',
      'function t(s: string) { return s; }',
      'export const a = t("yerel");',
    ].join('\n'),
    'controllers/other.ts': [
      'import { t } from "i18next";',
      'import { NotFound } from "@palbase/backend";',
      'export const b = t("başka paket");',
    ].join('\n'),
    'controllers/typeonly.ts': [
      'import type { t } from "@palbase/backend";',
      'export const c = t("tip");',
    ].join('\n'),
    'controllers/shadow.ts': [
      'import { t } from "@palbase/backend";',
      'export function f(t: (s: string) => string) { return t("gölge"); }',
    ].join('\n'),
  });
  assert.deepStrictEqual(scan(root), { keys: [], dynamic: [] });
});

test('a non-literal first argument is reported with file and line (FR-016)', (t) => {
  const root = project(t, {
    'controllers/a.ts': [
      'import { t } from "@palbase/backend";',
      '',
      'const x = "dünya";',
      'export const a = t(`Merhaba ${x}`);',
      'export const b = t(x);',
    ].join('\n'),
  });
  assert.deepStrictEqual(scan(root), {
    keys: [],
    dynamic: [
      { file: 'controllers/a.ts', line: 4 },
      { file: 'controllers/a.ts', line: 5 },
    ],
  });
});

test('node_modules, palbase and declaration files are not source', (t) => {
  const sdkCall = [
    'import { t } from "@palbase/backend";',
    'export const a = t("kaynak değil");',
  ].join('\n');
  const root = project(t, {
    'node_modules/some-pkg/index.ts': sdkCall,
    'palbase/generated.ts': sdkCall,
    'controllers/types.d.ts': sdkCall,
  });
  assert.deepStrictEqual(scan(root), { keys: [], dynamic: [] });
});

test('tsx is read', (t) => {
  const root = project(t, {
    'controllers/c.tsx': [
      'import { t } from "@palbase/backend";',
      'export const C = () => <div>{t("JSX")}</div>;',
    ].join('\n'),
  });
  assert.deepStrictEqual(scan(root), { keys: ['JSX'], dynamic: [] });
});

test('the CLI prints one JSON line and exits 0', (t) => {
  const root = project(t, {
    'controllers/a.ts': [
      'import { t } from "@palbase/backend";',
      'export const a = t("A");',
    ].join('\n'),
  });
  const res = spawnSync(process.execPath, [SCANNER, root], { env: process.env, encoding: 'utf8' });
  assert.strictEqual(res.status, 0, 'stderr: ' + res.stderr);
  assert.strictEqual(res.stdout.split('\n').length, 2, 'not exactly one line: ' + JSON.stringify(res.stdout));
  assert.deepStrictEqual(JSON.parse(res.stdout.trim()), { keys: ['A'], dynamic: [] });
});
