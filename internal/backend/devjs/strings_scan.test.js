// Unit tests for strings_scan.js — the source of every key in
// palbase/strings.json (FR-015), of the build's warnings for a t() use it
// cannot read (FR-016), and of its refusal to hand back a partial key set
// (FR-030).
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

// A file that names the package and does not parse: the t() calls past line 2
// are missing from the tree the parser recovers (FR-030).
const CONFLICTED = [
  'import { t } from "@palbase/backend";',
  '<<<<<<< HEAD',
  'export const a = t("bizim");',
  '=======',
  'export const a = t("onların");',
  '>>>>>>> feature',
].join('\n');

test("named, aliased and namespace imports of @palbase/backend's t are collected", (t) => {
  const root = project(t, {
    'controllers/named.ts': [
      'import { t } from "@palbase/backend";',
      'export const a = t("A");',
      'export const n = t("A", { iç: t("İç") });',
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
  assert.deepStrictEqual(scan(root), { keys: ['A', 'B', 'C', 'İç'], dynamic: [] });
});

test('a parenthesised, asserted or element-access callee is still a call to t', (t) => {
  const root = project(t, {
    'controllers/callee.ts': [
      'import { t } from "@palbase/backend";',
      'import * as B from "@palbase/backend";',
      'export const a = (t)("parantezli");',
      'export const b = (t as any)("iddialı");',
      'export const c = t!("kesin");',
      'export const d = B["t"]("eleman");',
    ].join('\n'),
  });
  assert.deepStrictEqual(scan(root), { keys: ['eleman', 'iddialı', 'kesin', 'parantezli'], dynamic: [] });
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
      'export { t };',
    ].join('\n'),
    'controllers/shadow.ts': [
      'import { t } from "@palbase/backend";',
      'export function f(t: (s: string) => string) { return t("gölge"); }',
    ].join('\n'),
    'controllers/renamed.ts': [
      'import { NotFound as t } from "@palbase/backend";',
      'export const d = t("başka üye, t adıyla");',
      'export { t };',
    ].join('\n'),
    'controllers/typespecifier.ts': [
      'import { type t } from "@palbase/backend";',
      'export const e = t("belirteç düzeyinde tip");',
      'export { t };',
    ].join('\n'),
    'controllers/typenamespace.ts': [
      'import type * as B from "@palbase/backend";',
      'export const f = B.t("tip ad alanı");',
      'export { B };',
    ].join('\n'),
    'controllers/othermember.ts': [
      'import * as B from "@palbase/backend";',
      '@B.Controller("/dekoratör")',
      'export class C {',
      '  g() { throw B.NotFound("başka üye"); }',
      '}',
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

test('a literal under parentheses or a type assertion is still a key (FR-016)', (t) => {
  const root = project(t, {
    'controllers/wrapped.ts': [
      'import { t } from "@palbase/backend";',
      'export const a = t(("parantez"));',
      'export const b = t("as const" as const);',
      'export const c = t("satisfies" satisfies string);',
      'export const d = t("kesin"!);',
      'export const e = t(<string>"açılı iddia");',
    ].join('\n'),
  });
  assert.deepStrictEqual(scan(root), {
    keys: ['as const', 'açılı iddia', 'kesin', 'parantez', 'satisfies'],
    dynamic: [],
  });
});

test('a use of t other than a direct call is reported with file and line (FR-016)', (t) => {
  const root = project(t, {
    'controllers/uses.ts': [
      'import { t } from "@palbase/backend";', //                1
      'import * as B from "@palbase/backend";', //               2
      'export const tagged = t`etiketli`;', //                   3
      'const tt = t;', //                                        4
      'export const mapped = ["m"].map(t);', //                  5
      'export const called = t.call(null, "call ile");', //      6
      'export const nsRef = B.t;', //                            7
      'export const nsElem = B["t"];', //                        8
      'export const inObject = { t };', //                       9
      'const { t: t2 } = B;', //                                10
      'export const whole = B;', //                             11
      'export { t, tt, t2 };', //                               12
      'type T = typeof t;', //                                  13 — a type, nothing at run time
      'export const kind = typeof t;', //                       14 — a string, t does not escape
      'const { NotFound } = B;', //                             15 — t is not taken
      'export const nf = B.NotFound;', //                       16
      'export const nsKind = typeof B.t;', //                   17 — a string, as on line 14
      'export const inst = t<string>;', //                      18
      'export const byKey = (k: string) => B[k];', //           19 — might be t
    ].join('\n'),
  });
  const at = (line) => ({ file: 'controllers/uses.ts', line });
  assert.deepStrictEqual(scan(root), {
    keys: [],
    dynamic: [at(3), at(4), at(5), at(6), at(7), at(8), at(9), at(10), at(11), at(12), at(18), at(19)],
  });
});

test('a re-export of t is reported with file and line (FR-016)', (t) => {
  const root = project(t, {
    'lib/a.ts': 'export { t } from "@palbase/backend";',
    'lib/b.ts': 'export { NotFound, t as tr } from "@palbase/backend";',
    'lib/c.ts': 'export * from "@palbase/backend";',
    'lib/d.ts': 'export * as P from "@palbase/backend";',
    'lib/e.ts': [
      'export { NotFound } from "@palbase/backend";',
      'export type { t } from "@palbase/backend";',
    ].join('\n'),
    // The call through the barrel names no @palbase/backend, so it is not a key —
    // the warning at the re-export is what says so.
    'controllers/x.ts': [
      'import { t } from "../lib/a";',
      'export const x = t("barrel üzerinden");',
    ].join('\n'),
  });
  assert.deepStrictEqual(scan(root), {
    keys: [],
    dynamic: [
      { file: 'lib/a.ts', line: 1 },
      { file: 'lib/b.ts', line: 1 },
      { file: 'lib/c.ts', line: 1 },
      { file: 'lib/d.ts', line: 1 },
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

test('mts and cts are read', (t) => {
  const root = project(t, {
    'controllers/a.mts': [
      'import { t } from "@palbase/backend";',
      'export const a = t("mts");',
    ].join('\n'),
    'controllers/b.cts': [
      'import { t } from "@palbase/backend";',
      'export const b = t("cts");',
    ].join('\n'),
  });
  assert.deepStrictEqual(scan(root), { keys: ['cts', 'mts'], dynamic: [] });
});

test('a file that names @palbase/backend and does not parse stops the scan (FR-030)', (t) => {
  const template = project(t, {
    'controllers/fine.ts': [
      'import { t } from "@palbase/backend";',
      'export const ok = t("sağlam");',
    ].join('\n'),
    'controllers/template.ts': [
      'import { t } from "@palbase/backend";',
      'export const a = t("önce");',
      'const bad = `kapanmamış template;',
      'export const b = t("sonra");',
    ].join('\n'),
  });
  assert.throws(() => scan(template), {
    message: 'controllers/template.ts:4 does not parse (Unterminated template literal.) — its t() strings cannot be read',
  });

  const conflict = project(t, { 'controllers/conflict.ts': CONFLICTED });
  assert.throws(() => scan(conflict), {
    message: 'controllers/conflict.ts:2 does not parse (Merge conflict marker encountered.) — its t() strings cannot be read',
  });
});

test('a file that does not name @palbase/backend is not stopped by its syntax error', (t) => {
  const root = project(t, {
    'controllers/broken.ts': 'export const x = `kapanmamış;\n',
    'controllers/a.ts': [
      'import { t } from "@palbase/backend";',
      'export const a = t("A");',
    ].join('\n'),
  });
  assert.deepStrictEqual(scan(root), { keys: ['A'], dynamic: [] });
});

test("an error inside one file's walk names that file", (t) => {
  // 30 000 nested `+` overflow a recursive walk; the scan still fails (FR-030),
  // and the build's one-line warning has to say where.
  const root = project(t, {
    'controllers/deep.ts': [
      'import { t } from "@palbase/backend";',
      'export const a = t("A");',
      'export const s = "a"' + ' + "a"'.repeat(29999) + ';',
    ].join('\n'),
  });
  assert.throws(() => scan(root), { message: /^controllers\/deep\.ts: / });
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

test('the CLI exits 1 with nothing on stdout when a file does not parse (FR-030)', (t) => {
  const root = project(t, { 'controllers/conflict.ts': CONFLICTED });
  const res = spawnSync(process.execPath, [SCANNER, root], { env: process.env, encoding: 'utf8' });
  assert.strictEqual(res.status, 1, 'stdout: ' + res.stdout);
  assert.strictEqual(res.stdout, '');
  assert.match(res.stderr, /^controllers\/conflict\.ts:2 does not parse/);
});

test('the CLI exits 3 when typescript cannot be loaded', (t) => {
  // A copy outside any node_modules ancestry, with NODE_PATH emptied: nothing
  // can resolve `typescript`.
  const dir = project(t, { 'strings_scan.js': fs.readFileSync(SCANNER, 'utf8') });
  const res = spawnSync(process.execPath, [path.join(dir, 'strings_scan.js'), dir], {
    env: { ...process.env, NODE_PATH: '' },
    encoding: 'utf8',
  });
  assert.strictEqual(res.status, 3, 'stderr: ' + res.stderr);
  assert.strictEqual(res.stdout, '');
  assert.match(res.stderr, /typescript/);
});
