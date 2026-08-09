// Unit tests for the Node side of `palbase build` (build-check.js).
//
// Two locks live here, both on the REAL production flow rather than a
// reimplementation of it:
//   1. the controllers bundle keeps `../resources/*` EXTERNAL — deploy bundler
//      (ExternalResourceImports) parity;
//   2. the shared TypeScript parser guard turns a TS7 / missing-typescript
//      environment into an actionable error instead of a raw TypeError.
//
// Run: node --test internal/backend/devjs/build-check.test.js

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

// Fixture project for the bundle-flow test below. PROJECT_ROOT is bound when
// build-check.js is require()d, so PALBASE_DEV_ROOT must point at the fixture
// BEFORE the require.
const FIXTURE_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'palbase-buildcheck-test-'));
process.env.PALBASE_DEV_ROOT = FIXTURE_ROOT;

// RESOURCE_INLINE_CANARY marks the resource IMPLEMENTATION: it must appear in
// the shared resources bundle and must NOT appear in the controller bundle —
// if it does, the controller inlined its own never-booted Resource twin (the
// exact bug the deploy bundler's ExternalResourceImports prevents).
// Plain-JS fixture ON PURPOSE: a `.controller.ts` takes the staging
// return-bindings path (return_types.js), which needs the `typescript` package
// — absent in the CI unit env. A `.controller.js` is copied VERBATIM by
// stageControllersWithReturnBindings, so the bundle flow under test (the
// externals) runs identically with zero extra deps.
fs.mkdirSync(path.join(FIXTURE_ROOT, 'resources'), { recursive: true });
fs.mkdirSync(path.join(FIXTURE_ROOT, 'controllers'), { recursive: true });
fs.writeFileSync(path.join(FIXTURE_ROOT, 'resources', 'env.js'), [
  'import { Resource } from "@palbase/backend";',
  '',
  'export class EnvDiag extends Resource {',
  '  value = "";',
  '  async init(env) {',
  '    this.value = "RESOURCE_INLINE_CANARY";',
  '  }',
  '}',
  '',
  'export const envDiag = new EnvDiag();',
  '',
].join('\n'));
fs.writeFileSync(path.join(FIXTURE_ROOT, 'controllers', 'diag.controller.js'), [
  'import { envDiag } from "../resources/env";',
  '',
  'export default class DiagController {',
  '  async read() {',
  '    return { value: envDiag.value };',
  '  }',
  '}',
  '',
].join('\n'));

// A NON-controller file dropped into controllers/. Its top-level `new URL()` on a
// relative path throws the moment the file is require()d — the exact shape of the
// reported failure ("DEPLOY WOULD FAIL: controllers/tenancy.test.js — Invalid URL")
// for a file the DEPLOY never loads, because discoverControllerEntries keys on
// `.controller.`.
fs.writeFileSync(path.join(FIXTURE_ROOT, 'controllers', 'tenancy.test.js'), [
  'const parsed = new URL("/relative");',
  'export default parsed;',
  '',
].join('\n'));

// require()ing build-check.js is side-effect-light because main() is guarded by
// `require.main === module`; the only top-level effect is one throwaway temp dir.
const {
  registerControllers, bundleResources, BUNDLED_CONTROLLERS_DIR, BUNDLED_RESOURCES_DIR,
} = require('./build-check.js');

test.after(() => {
  // The fixture + the bundle temp tree (the process-exit cleanup still fires,
  // but the fixture root is ours to remove).
  fs.rmSync(FIXTURE_ROOT, { recursive: true, force: true });
  fs.rmSync(path.dirname(BUNDLED_CONTROLLERS_DIR), { recursive: true, force: true });
});

// esbuildAvailable probes `npx esbuild` the same way bundleSrcDir invokes it.
// Absent/offline npx (no cached esbuild) → the bundle-flow test skips, matching
// the Go build tests' skip-when-offline behavior.
function esbuildAvailable() {
  try {
    execFileSync('npx', ['--yes', 'esbuild', '--version'], { stdio: 'ignore', cwd: FIXTURE_ROOT });
    return true;
  } catch {
    return false;
  }
}

// Cross-boundary lock: the CONTROLLERS bundle must keep the project's OWN
// `../resources/*` imports EXTERNAL (deploy bundler ExternalResourceImports
// parity) so a controller shares the ONE resource module in
// BUNDLE_ROOT/resources/ instead of inlining a copy. This drives the REAL
// production flow (bundleResources → registerControllers, main()'s order) — so
// dropping CONTROLLER_RESOURCE_EXTERNALS at the registerControllers call site,
// emptying the constant, or removing the externals emission in bundleSrcDir all
// turn this RED (mutation-verified).
test('controllers bundle keeps ../resources/* external (shared resource module)', (t) => {
  if (!esbuildAvailable()) return t.skip('npx esbuild unavailable (offline?)');

  // Resources bundle FIRST — main()'s order (and the deploy extractor's).
  assert.strictEqual(bundleResources(), true, 'fixture resources/ must bundle');
  const resourceBundle = path.join(BUNDLED_RESOURCES_DIR, 'env.js');
  assert.ok(fs.existsSync(resourceBundle),
    'BUNDLE_ROOT/resources/env.js must exist (the controller require()s it at runtime)');
  assert.match(fs.readFileSync(resourceBundle, 'utf8'), /RESOURCE_INLINE_CANARY/,
    'the resource implementation must live in the shared resources bundle');

  // The REAL controllers flow: stage + bundle with the production externals.
  // (The controller is later skipped at require — no @palbase/backend in the
  // fixture — but the bundled .js this test inspects is already emitted.)
  registerControllers();
  const controllerBundle = path.join(BUNDLED_CONTROLLERS_DIR, 'diag.controller.js');
  assert.ok(fs.existsSync(controllerBundle), 'bundled controller must exist');
  const bundled = fs.readFileSync(controllerBundle, 'utf8');
  assert.match(bundled, /require\("\.\.\/resources\/env"\)/,
    'controller bundle must require("../resources/env") — external, resolved to the shared instance');
  assert.doesNotMatch(bundled, /RESOURCE_INLINE_CANARY/,
    'resource code must NOT be inlined into the controller bundle (a second copy)');
});

// ── TypeScript parser guard ────────────────────────────────────────────────
//
// return_types.js + throw_analysis.js drive the TS compiler API. A user tree on
// TypeScript 7 (whose CJS build has no compiler API) or with no typescript at
// all must produce an ACTIONABLE error, never a raw TypeError — the same rule
// on the deploy stager, which shares these two files byte-for-byte.
// Both suites fake the `typescript` resolution, so they run with no npm deps.

const CtrlSrc = [
  'import { Controller, Get } from "@palbase/backend";',
  '@Controller("/t")',
  'export default class T {',
  '  @Get("/") list(): void {}',
  '}',
].join('\n');

function withFakeTypescript(fake, fn) {
  const Module = require('node:module');
  const origLoad = Module._load;
  Module._load = function (request, ...rest) {
    if (request === 'typescript') return fake();
    return origLoad.call(this, request, ...rest);
  };
  try {
    fn();
  } finally {
    Module._load = origLoad;
  }
}

test('parser guard: a TypeScript 7 surface produces an actionable error, not a TypeError', () => {
  const returnTypes = require('./return_types.js');
  withFakeTypescript(() => ({ version: '7.0.2', versionMajorMinor: '7.0' }), () => {
    assert.throws(
      () => returnTypes.readReturnTypes(CtrlSrc, 'todos.controller.ts'),
      (err) => {
        assert.doesNotMatch(err.message, /Cannot read properties of undefined/,
          'the raw TypeError must never reach the user');
        assert.match(err.message, /TypeScript 5 compiler API/);
        assert.match(err.message, /v7\.0\.2/, 'name the version that was resolved');
        assert.match(err.message, /npm install --save-dev typescript@5/, 'tell them what to do');
        return true;
      },
    );
  });
});

test('parser guard: a missing typescript produces the same actionable error', () => {
  const returnTypes = require('./return_types.js');
  const throwAnalysis = require('./throw_analysis.js');
  withFakeTypescript(
    () => {
      const e = new Error("Cannot find module 'typescript'");
      e.code = 'MODULE_NOT_FOUND';
      throw e;
    },
    () => {
      assert.throws(
        () => returnTypes.readReturnTypes(CtrlSrc, 'todos.controller.ts'),
        (err) => {
          assert.match(err.message, /TypeScript 5 compiler API/);
          assert.match(err.message, /npm install --save-dev typescript@5/);
          return true;
        },
      );
      // The throw analyzer is best-effort at the inject level (a parser problem
      // must not fail the stage on its own — return_types already did, loudly),
      // but its own loadTS carries the same actionable message.
      assert.throws(
        () => throwAnalysis.analyzeThrows(CtrlSrc, '/tmp/todos.controller.ts', {
          readFile: () => null,
          fileExists: () => false,
          projectRoot: '/tmp',
        }),
        /TypeScript 5 compiler API/,
      );
    },
  );
});

// ── TxPlan Ref-truthiness gate wiring ────────────────────────────────────────
//
// tx_analysis.js's OWN pattern-detection is covered exhaustively by
// tx_analysis.test.js (8 positive + 6 negative fixtures, mutation-verified).
// These two tests lock the WIRING into build-check.js instead — a real
// `node build-check.js` subprocess against a fixture PROJECT_ROOT, because the
// gate runs (and, on a violation, process.exit(1)s) BEFORE any esbuild/
// @palbase/backend dependency is touched, so this is cheap and needs no
// node_modules of its own.

const { spawnSync } = require('node:child_process');

function runBuildCheck(fixtureRoot) {
  return spawnSync(
    process.execPath,
    [path.join(__dirname, 'build-check.js')],
    { env: Object.assign({}, process.env, { PALBASE_DEV_ROOT: fixtureRoot }), encoding: 'utf8' },
  );
}

test('TxPlan gate: a Ref-truthiness violation in controllers/ fails the build before any bundling', () => {
  const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'palbase-txgate-bad-'));
  fs.mkdirSync(path.join(fixtureRoot, 'controllers'), { recursive: true });
  fs.writeFileSync(path.join(fixtureRoot, 'controllers', 'invites.controller.ts'), [
    'import { Database, Conflict, NotFound } from "@palbase/backend";',
    '',
    'export function acceptInvite(token) {',
    '  return Database.transaction((tx) => {',
    '    const locked = tx.tables.invites.updateWhere({ token }, {}).expectOne(new NotFound("nf"));',
    '    if (locked.accepted_at) throw new Conflict("used");',
    '  });',
    '}',
  ].join('\n'));

  const result = runBuildCheck(fixtureRoot);
  assert.strictEqual(result.status, 1,
    `expected exit 1, got ${result.status}\nstdout: ${result.stdout}\nstderr: ${result.stderr}`);
  assert.match(result.stdout, /TxPlan Ref-truthiness violation/);
  assert.match(result.stdout, /invites\.controller\.ts:6:/, 'must name the offending file and line');
});

test('TxPlan gate: a clean transaction() plan does not trip it', () => {
  const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'palbase-txgate-clean-'));
  fs.mkdirSync(path.join(fixtureRoot, 'controllers'), { recursive: true });
  fs.writeFileSync(path.join(fixtureRoot, 'controllers', 'invites.controller.ts'), [
    'import { Database, Conflict, now } from "@palbase/backend";',
    '',
    'export function acceptInvite(token) {',
    '  return Database.transaction((tx) => {',
    '    tx.tables.invites',
    '      .updateWhere({ token, accepted_at: null }, { accepted_at: now() })',
    '      .expectOne(new Conflict("used"));',
    '  });',
    '}',
  ].join('\n'));

  const result = runBuildCheck(fixtureRoot);
  assert.doesNotMatch(result.stdout, /TxPlan Ref-truthiness violation/,
    `the gate must not fire on a clean plan\nstdout: ${result.stdout}`);
  // The build itself still fails past this point in this tiny fixture (no
  // package.json, no @palbase/backend) — irrelevant here, this test locks
  // ONLY the TxPlan gate's pass-through behavior.
});

test('TxPlan gate: services/*.ts is scanned too, not just controllers/', () => {
  const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'palbase-txgate-services-'));
  fs.mkdirSync(path.join(fixtureRoot, 'services'), { recursive: true });
  fs.writeFileSync(path.join(fixtureRoot, 'services', 'invite.service.ts'), [
    'import { Database, Conflict, NotFound } from "@palbase/backend";',
    '',
    'export function acceptInvite(token) {',
    '  return Database.transaction((tx) => {',
    '    const locked = tx.tables.invites.updateWhere({ token }, {}).expectOne(new NotFound("nf"));',
    '    if (locked.accepted_at) throw new Conflict("used");',
    '  });',
    '}',
  ].join('\n'));

  const result = runBuildCheck(fixtureRoot);
  assert.strictEqual(result.status, 1, `expected exit 1, got ${result.status}\nstdout: ${result.stdout}`);
  assert.match(result.stdout, /invite\.service\.ts:6:/, 'must name the offending file and line inside services/');
});

// ── controllers/ discovery must match the deploy's ─────────────────────────

// The reported defect: a test file dropped into controllers/ failed the build
// with "Invalid URL" — a file the DEPLOY never loads. discoverControllerEntries
// (modules/backend/internal/deploy/bundler.go) keys on `.controller.`, so the
// build was STRICTER than the deploy and its "DEPLOY WOULD FAIL" claim was
// false. Mutation: widen CONTROLLER_ENTRY_RE / BUNDLED_CONTROLLER_RE back to
// /\.(c?js|mjs|ts)$/ and this goes RED on both assertions.
test('a non-controller file in controllers/ is neither bundled nor loaded', (t) => {
  if (!esbuildAvailable()) return t.skip('npx esbuild unavailable (offline?)');

  bundleResources();
  const reg = registerControllers();

  assert.ok(!fs.existsSync(path.join(BUNDLED_CONTROLLERS_DIR, 'tenancy.test.js')),
    'only *.controller.* files are entry points, exactly as on deploy');
  const offending = (reg.skipped || []).filter((s) => /tenancy\.test/.test(s.file));
  assert.deepEqual(offending, [],
    'a file the deploy never loads must not produce a build failure');
  assert.ok(fs.existsSync(path.join(BUNDLED_CONTROLLERS_DIR, 'diag.controller.js')),
    'the real controller is still bundled');
});

// Narrowing the scan must NOT cost the silent-failure signal it was guarding:
// a controllers/ full of source that registers nothing deploys as a SUCCESS
// serving zero endpoints, which is the whole reason this runner exists.
test('controllers/ with source but no *.controller.ts fails and names the convention', (t) => {
  if (!esbuildAvailable()) return t.skip('npx esbuild unavailable (offline?)');

  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'palbase-buildcheck-noentry-'));
  fs.mkdirSync(path.join(root, 'controllers'), { recursive: true });
  // Misnamed on purpose: a @Controller class in a file the deploy will never
  // treat as an entry point.
  fs.writeFileSync(path.join(root, 'controllers', 'todos.js'),
    'export default class TodosController {}\n');

  let out = '';
  let code = 0;
  try {
    out = execFileSync('node', [path.join(__dirname, 'build-check.js')], {
      env: Object.assign({}, process.env, { PALBASE_DEV_ROOT: root }),
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    });
  } catch (err) {
    code = err.status;
    out = String(err.stdout || '') + String(err.stderr || '');
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }

  assert.strictEqual(code, 1, `build must fail; output:\n${out}`);
  assert.match(out, /only \*\.controller\.ts files register routes/);
});
