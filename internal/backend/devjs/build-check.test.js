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
// The TypeScript parser has to be resolvable IN THIS PROCESS too.
//
// Two tests call `scanTxPlanViolations` directly rather than through a
// subprocess, so an env var does not reach them: they were failing on
// "typescript could not be loaded" instead of on their subject, and had been for
// as long as this file has been run outside the CLI (which installs its own
// pinned parser). A test that cannot reach its subject measures nothing.
{
  const repoModules = path.resolve(
    __dirname, '..', '..', '..', '..', 'palbase-ts', 'backend', 'node_modules',
  );
  if (fs.existsSync(path.join(repoModules, 'typescript'))) {
    process.env.NODE_PATH = process.env.NODE_PATH
      ? repoModules + path.delimiter + process.env.NODE_PATH
      : repoModules;
    require('node:module').Module._initPaths();
  }
}

const FIXTURE_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'palbase-buildcheck-test-'));
process.env.PALBASE_DEV_ROOT = FIXTURE_ROOT;

// RESOURCE_INLINE_CANARY marks the resource IMPLEMENTATION: it must appear in
// the shared resources bundle and must NOT appear in the controller bundle — if
// it does, the controller inlined a PRIVATE COPY of the module instead of
// sharing the one (the exact bug the deploy bundler's ExternalResourceImports
// prevents), and two controllers importing it would each hold their own.
// Plain-JS fixture ON PURPOSE: a `.controller.ts` takes the staging
// return-bindings path (return_types.js), which needs the `typescript` package
// — absent in the CI unit env. A `.controller.js` is copied VERBATIM by
// stageControllersWithReturnBindings, so the bundle flow under test (the
// externals) runs identically with zero extra deps.
fs.mkdirSync(path.join(FIXTURE_ROOT, 'resources'), { recursive: true });
fs.mkdirSync(path.join(FIXTURE_ROOT, 'controllers'), { recursive: true });
fs.writeFileSync(path.join(FIXTURE_ROOT, 'resources', 'env.js'), [
  'export const envDiag = {',
  '  value: "RESOURCE_INLINE_CANARY",',
  '};',
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

// THE MODULE. The bundler collects `*.module.ts` and nothing else, so a
// controller reaches the bundle by being listed here — not by sitting in
// controllers/.
fs.writeFileSync(path.join(FIXTURE_ROOT, 'app.module.js'), [
  'import DiagController from "./controllers/diag.controller";',
  '',
  'export const declared = [DiagController];',
  '',
].join('\n'));

// Where the TypeScript parser comes from when this file runs under a bare
// `node --test`.
//
// build-check needs it (tx_analysis, return_types) and the CLI normally installs
// its own pinned copy — which is not there in a test run. Two tests were failing
// on that rather than on their subject, and a test that cannot reach its subject
// is not measuring anything. The monorepo's own install is used instead, and
// when it is absent the tests SKIP with the reason rather than reporting a
// failure of the code.
function parserPath() {
  const repo = path.resolve(__dirname, '..', '..', '..', '..', 'palbase-ts', 'backend', 'node_modules');
  return fs.existsSync(path.join(repo, 'typescript')) ? repo : '';
}

function parserAvailable() {
  return parserPath() !== '';
}

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
  surfaceClassesIn,
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
  const controllerBundle = path.join(BUNDLED_CONTROLLERS_DIR, 'app.module.js');
  assert.ok(fs.existsSync(controllerBundle), 'the bundled MODULE must exist — it is the entry now');
  const bundled = fs.readFileSync(controllerBundle, 'utf8');
  assert.match(bundled, /require\("\.\.\/resources\/env"\)/,
    'the module bundle must require("../resources/env") — external, resolved to the shared instance');
  assert.doesNotMatch(bundled, /RESOURCE_INLINE_CANARY/,
    'resource code must NOT be inlined into the module bundle (a second copy)');
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
    'only *.module.* files are entry points, exactly as on deploy');
  const offending = (reg.skipped || []).filter((s) => /tenancy\.test/.test(s.file));
  assert.deepEqual(offending, [],
    'a file the deploy never loads must not produce a build failure');
  assert.ok(fs.existsSync(path.join(BUNDLED_CONTROLLERS_DIR, 'app.module.js')),
    'the module — the entry — is still bundled');
});

// Narrowing the scan must NOT cost the silent-failure signal it was guarding:
// a controllers/ full of source that registers nothing deploys as a SUCCESS
// serving zero endpoints, which is the whole reason this runner exists.
test('a project with source but NO module fails and says what a module is for', (t) => {
  if (!esbuildAvailable()) return t.skip('npx esbuild unavailable (offline?)');

  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'palbase-buildcheck-noentry-'));
  fs.mkdirSync(path.join(root, 'controllers'), { recursive: true });
  // A controller written and never listed. It reaches no bundle, registers
  // nothing, and would deploy as a success serving zero endpoints — the silence
  // the module glob made possible and this refusal closes.
  fs.writeFileSync(path.join(root, 'controllers', 'todos.controller.ts'),
    'export default class TodosController {}\n');

  let out = '';
  let code = 0;
  try {
    out = execFileSync('node', [path.join(__dirname, 'build-check.js')], {
      env: Object.assign({}, process.env, { PALBASE_DEV_ROOT: root, NODE_PATH: parserPath() }),
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
  assert.match(out, /no \*\.module\.ts/);
});

// ── the reported path must be one the author can OPEN ──────────────────────
//
// `palbase build` prints a line per route:
//
//   GET    /todos  →  controllers/todos.controller.js [list]
//
// and that `.js` does not exist. The author wrote `controllers/todos.controller.ts`;
// the `.js` is the esbuild output in a temp dir that is deleted on exit. So the
// one path in the output a person would click, or paste into an editor, or grep
// for, is the one path that resolves to nothing — and the same string is reused
// for skipped controllers and for extractor errors, which is exactly where
// somebody needs to open the file.
//
// bundledToSrcRel took the extension from the BUNDLED path (path.relative over
// BUNDLED_CONTROLLERS_DIR), so it could only ever say `.js`.
//
// Both suites below drive the REAL staging function — the only place that sees
// the source tree and the staged tree at once — so unhooking the manifest from
// it turns them red.

const { stageControllersWithReturnBindings, bundledToSrcRel } = require('./build-check.js');

// typescriptAvailable probes the parser return_types.js needs, the same way
// esbuildAvailable() probes esbuild. `go test` provisions it on NODE_PATH
// (devjs_node_test.go → ensureParserTS), so this runs in the Go gate; a bare
// `node --test` on a machine without typescript skips instead of failing.
function typescriptAvailable() {
  try {
    const ts = require('typescript');
    return typeof ts.createSourceFile === 'function';
  } catch {
    return false;
  }
}

function stagedFixture(t, files) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'palbase-srcrel-'));
  const src = path.join(root, 'controllers');
  fs.mkdirSync(src, { recursive: true });
  for (const [name, body] of Object.entries(files)) {
    fs.writeFileSync(path.join(src, name), body);
  }
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  stageControllersWithReturnBindings(src, path.join(root, '.palbase-build-controllers'));
}

test('the reported path carries the SOURCE extension, not the bundled one', (t) => {
  // `helpers.ts` is copied VERBATIM by the stager (only *.controller.ts takes
  // the return-bindings path), so this case needs neither esbuild nor a
  // TypeScript parser and always runs.
  stagedFixture(t, {
    'helpers.ts': 'export const ok = true;\n',
    'diag.controller.js': 'export default class D {}\n',
  });

  assert.strictEqual(
    bundledToSrcRel(path.join(BUNDLED_CONTROLLERS_DIR, 'helpers.js')),
    path.join('controllers', 'helpers.ts'),
    'a .ts source must be reported as .ts — the bundled .js is a temp file that is deleted on exit',
  );

  // NEGATIVE CONTROL: a source that really IS .js keeps .js. Without this,
  // "always say .ts" would pass the assertion above and lie about every
  // plain-JS controller.
  assert.strictEqual(
    bundledToSrcRel(path.join(BUNDLED_CONTROLLERS_DIR, 'diag.controller.js')),
    path.join('controllers', 'diag.controller.js'),
    'a genuine .js source must stay .js',
  );

  // FAIL-SAFE: a bundled file the manifest never saw keeps the bundled
  // extension rather than being guessed at.
  assert.strictEqual(
    bundledToSrcRel(path.join(BUNDLED_CONTROLLERS_DIR, 'ghost.controller.js')),
    path.join('controllers', 'ghost.controller.js'),
    'an unmapped bundled path must fall back to the bundled extension',
  );
});

test('a real *.controller.ts route reports its .ts source', (t) => {
  if (!typescriptAvailable()) return t.skip('no TypeScript 5 compiler API (bare `node --test`?)');

  stagedFixture(t, {
    'todos.controller.ts': [
      'import { Controller, Get } from "@palbase/backend";',
      '@Controller("/todos")',
      'export default class TodosController {',
      '  @Get("/") list(): void {}',
      '}',
    ].join('\n'),
  });

  assert.strictEqual(
    bundledToSrcRel(path.join(BUNDLED_CONTROLLERS_DIR, 'todos.controller.js')),
    path.join('controllers', 'todos.controller.ts'),
  );
});


// THE SURFACE CLASSES THE BUILD NEVER LOADED (FR-011's missing half).
//
// `declaredSurfaces()` reads the filesystem and COUNTS files; it never loads a
// class, so the SDK's zero-argument rule had no build-time half for jobs, hooks
// and webhooks. A `@Job` with constructor parameters built clean and took the
// deploy down at boot, when the runtime resolved it.
//
// This proves the half that was missing: the build can now SEE those classes.
// That the rule then refuses one is the SDK's own contract, proven live against
// a real project (the run's UAT), because the rule and its message belong to the
// SDK and this file only asks it.
test('jobs/ classes are discovered, not just counted', (t) => {
  if (!esbuildAvailable()) {
    t.skip('esbuild unavailable (offline npx)');
    return;
  }
  fs.mkdirSync(path.join(FIXTURE_ROOT, 'jobs'), { recursive: true });
  fs.writeFileSync(
    path.join(FIXTURE_ROOT, 'jobs', 'topup.job.js'),
    [
      'class Topup { constructor(repo) { this.repo = repo; } async run() {} }',
      // What `@Job` stamps. The fixture carries it because the rule reads it —
      // an undecorated class in jobs/ is not a job, and the earlier version of
      // this fixture (no stamp) is why every exported FUNCTION looked like one.
      "Object.defineProperty(Topup, '__palbase', { value: 'job' });",
      'module.exports = { default: Topup };',
    ].join('\n'),
  );

  const found = surfaceClassesIn('jobs');
  assert.ok(found.length > 0, 'the build must LOAD jobs/, not only count the files');
  const topup = found.find((f) => f.cls && f.cls.name === 'Topup');
  assert.ok(topup, `Topup not among ${JSON.stringify(found.map((f) => f.cls && f.cls.name))}`);
  // The arity the SDK's rule reads. One parameter is what took the deploy down.
  assert.strictEqual(topup.cls.length, 1);
});

// A JOB FILE IS ALLOWED TO EXPORT ORDINARY CODE.
//
// The discovery took every exported FUNCTION for a surface class, and the SDK's
// rule only reads arity — so `export const makeClient = (url) => …` beside a job
// was reported as "job class makeClient declares a constructor with 1
// parameter(s)" and `palbase build` exited 1. A correct project could not build.
// The controller path never had this: it elides on `__palbase !== 'controller'`.
test('an ordinary helper beside a job is not mistaken for a job class', (t) => {
  if (!esbuildAvailable()) {
    t.skip('esbuild unavailable (offline npx)');
    return;
  }
  fs.mkdirSync(path.join(FIXTURE_ROOT, 'jobs'), { recursive: true });
  fs.writeFileSync(
    path.join(FIXTURE_ROOT, 'jobs', 'sweep.job.js'),
    [
      'class Sweep { async run() {} }',
      "Object.defineProperty(Sweep, '__palbase', { value: 'job' });",
      'function centsToLira(cents) { return cents / 100; }',
      'const makeClient = (url) => ({ url });',
      'class SweepFailed extends Error { constructor(msg) { super(msg); } }',
      'module.exports = { default: Sweep, centsToLira, makeClient, SweepFailed };',
    ].join('\n'),
  );

  const names = surfaceClassesIn('jobs')
    .filter((f) => f.cls)
    .map((f) => f.cls.name);
  assert.ok(names.includes('Sweep'), `the job itself must still be found: ${JSON.stringify(names)}`);
  for (const stranger of ['centsToLira', 'makeClient', 'SweepFailed']) {
    assert.ok(!names.includes(stranger), `${stranger} is not a job class: ${JSON.stringify(names)}`);
  }
});

test('an absent surface directory is not a fault', () => {
  assert.deepStrictEqual(surfaceClassesIn('no-such-surface-dir'), []);
});
