// Unit tests for the local (in-process) Cache + Queue that back `palbase serve`.
//
// The deployed runtime backs Cache/Queue with the pod-local internal-api, which
// has no external route serve could proxy to — so serve uses local in-process
// implementations instead (see dev-server.js makeLocalCache/makeLocalQueue).
// These tests lock the CacheClient surface (get/set/del/incr/getOrSet, TTL
// expiry honored) and the worker-backed Queue (push runs the registered worker
// and returns a jobId; an unknown worker is a no-op that still resolves).
//
// Run: node --test internal/backend/devjs/dev-server.test.js

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

// Fixture project for the bundle-flow test below. PROJECT_ROOT is bound when
// dev-server.js is require()d, so PALBASE_DEV_ROOT must point at the fixture
// BEFORE the require. The other suites in this file never touch PROJECT_ROOT.
const FIXTURE_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'palbase-devsrv-test-'));
process.env.PALBASE_DEV_ROOT = FIXTURE_ROOT;

// RESOURCE_INLINE_CANARY marks the resource IMPLEMENTATION: it must appear in
// the shared resources bundle and must NOT appear in the controller bundle —
// if it does, the controller inlined its own never-booted Resource twin (the
// exact serve bug the deploy bundler's ExternalResourceImports prevents).
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

// Remote-env load-order fixture. `palbase serve` auto-fetches the branch's
// remote env vars (Studio env.pull) into a JSON file and passes its path as
// PALBASE_REMOTE_ENV_FILE; dev-server.js must load it AFTER .env.local with
// only-if-unset semantics, so precedence stays shell env > .env.local > remote.
// Prepared BEFORE the require() below because both loaders run at module load —
// this drives the REAL production load path, not a reimplementation.
//   X: .env.local + remote, absent in shell → .env.local wins → 'local'
//   Y: remote only                          → remote fills the gap → 'remote'
//   Z: shell + .env.local + remote          → shell wins → 'shell'
fs.writeFileSync(path.join(FIXTURE_ROOT, '.env.local'), [
  'REMOTE_ENV_TEST_X=local',
  'REMOTE_ENV_TEST_Z=local',
  '',
].join('\n'));
const REMOTE_ENV_FILE = path.join(FIXTURE_ROOT, 'remote-env.json');
fs.writeFileSync(REMOTE_ENV_FILE, JSON.stringify({
  REMOTE_ENV_TEST_X: 'remote',
  REMOTE_ENV_TEST_Y: 'remote',
  REMOTE_ENV_TEST_Z: 'remote',
}));
process.env.PALBASE_REMOTE_ENV_FILE = REMOTE_ENV_FILE;
delete process.env.REMOTE_ENV_TEST_X;
delete process.env.REMOTE_ENV_TEST_Y;
process.env.REMOTE_ENV_TEST_Z = 'shell';

// require()ing dev-server.js is side-effect-light because main() is guarded by
// `require.main === module`; the only top-level effect is one throwaway temp dir.
const {
  makeLocalCache, makeLocalQueue, workerRegistry, parseDotenv,
  runInRequestContext, currentRequestUserId, currentRequestUserToken,
  registerControllers, bundleResources, BUNDLED_CONTROLLERS_DIR, BUNDLED_RESOURCES_DIR,
} = require('./dev-server.js');

test.after(() => {
  // The fixture + the dev-server's bundle temp tree (main()'s exit cleanup is
  // not installed when required as a module).
  fs.rmSync(FIXTURE_ROOT, { recursive: true, force: true });
  fs.rmSync(path.dirname(BUNDLED_CONTROLLERS_DIR), { recursive: true, force: true });
});

// esbuildAvailable probes `npx esbuild` the same way bundleSrcDir invokes it.
// Absent/offline npx (no cached esbuild) → the bundle-flow test skips, matching
// the Go check-mode tests' skip-when-offline behavior.
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
// parity) so a controller shares the ONE booted resource singleton in
// BUNDLE_ROOT/resources/ instead of inlining a never-booted copy. This drives
// the REAL production flow (bundleResources → registerControllers, main()'s
// order) — not a reimplementation — so dropping CONTROLLER_RESOURCE_EXTERNALS
// at the registerControllers call site, emptying the constant, or removing the
// externals emission in bundleSrcDir all turn this RED (mutation-verified).
test('controllers bundle keeps ../resources/* external (shared booted singleton)', (t) => {
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
    'resource code must NOT be inlined into the controller bundle (a second never-booted instance)');
});

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Per-request identity isolation. THE platform bug this file's ALS switch fixed:
// two concurrent requests (Node serves HTTP concurrently) sharing a module-global
// `let currentRequestUserToken` cross RLS/auth context — a handler suspended at an
// await has its identity clobbered by the next dispatching request, then reads the
// OTHER user's token (cross-tenant rows), or the finally-cleared null (anon →
// 403/404), or a wrong-user context with no matching rows (silent empty 200). The
// getters must read an AsyncLocalStorage box scoped to each handler's async tree.
test('concurrent requests do not cross user identity across an await (ALS isolation)', async () => {
  const seen = {};
  await Promise.all([
    // Request A: signed in as A, then AWAITS (yields the event loop to B).
    runInRequestContext({ userId: 'A', userToken: 'tok_A' }, async () => {
      await sleep(20); // B dispatches during this suspension
      seen.A = { id: currentRequestUserId(), token: currentRequestUserToken() };
    }),
    // Request B: signed in as B, resolves while A is still suspended.
    runInRequestContext({ userId: 'B', userToken: 'tok_B' }, async () => {
      await sleep(5);
      seen.B = { id: currentRequestUserId(), token: currentRequestUserToken() };
    }),
  ]);
  // A must STILL see A after resuming — not B, not null.
  assert.deepStrictEqual(seen.A, { id: 'A', token: 'tok_A' },
    'request A must keep its own identity across the await (no cross-context leak)');
  assert.deepStrictEqual(seen.B, { id: 'B', token: 'tok_B' });
});

test('identity box does not leak outside a request (anonymous default)', async () => {
  await runInRequestContext({ userId: 'X', userToken: 'tok_X' }, async () => {
    assert.strictEqual(currentRequestUserId(), 'X');
  });
  // Outside any request context → null (anon → project defaults / anon edge).
  assert.strictEqual(currentRequestUserId(), null);
  assert.strictEqual(currentRequestUserToken(), null);
});

test('nested contexts restore the outer identity on exit', async () => {
  await runInRequestContext({ userId: 'outer', userToken: 't_out' }, async () => {
    await runInRequestContext({ userId: 'inner', userToken: 't_in' }, async () => {
      assert.strictEqual(currentRequestUserId(), 'inner');
    });
    assert.strictEqual(currentRequestUserId(), 'outer', 'inner scope must not clobber outer');
  });
});

test('parseDotenv: parses KEY=VALUE, skips comments/blanks, strips quotes', () => {
  const env = parseDotenv(
    [
      '# a comment',
      '',
      'OPENAI_API_KEY=sk-abc123',
      'QUOTED="with spaces"',
      "SINGLE='single quoted'",
      '  PADDED = trimmed ',
      'EQUALS_IN_VALUE=a=b=c',
      '=novalue', // empty key — skipped
      'NO_EQUALS_LINE', // no `=` — skipped
    ].join('\n'),
  );
  assert.strictEqual(env.OPENAI_API_KEY, 'sk-abc123');
  assert.strictEqual(env.QUOTED, 'with spaces'); // surrounding quotes stripped
  assert.strictEqual(env.SINGLE, 'single quoted');
  assert.strictEqual(env.PADDED, 'trimmed'); // key + value trimmed
  assert.strictEqual(env.EQUALS_IN_VALUE, 'a=b=c'); // only the first `=` splits
  assert.ok(!('' in env), 'empty key is dropped');
  assert.ok(!('NO_EQUALS_LINE' in env), 'a line without = is dropped');
  // A comment/blank produces no keys.
  assert.deepStrictEqual(parseDotenv('# only a comment\n\n'), {});
});

// Cross-boundary lock for serve's auto-fetched remote env (fixture written
// before the require() at the top of this file). Mutations that turn this RED:
// loading PALBASE_REMOTE_ENV_FILE before .env.local (or assigning
// unconditionally) → X becomes 'remote'; dropping the remote loader → Y
// undefined; losing the already-set-wins guard → Z loses 'shell'.
test('remote env file loads AFTER .env.local: shell > .env.local > remote', () => {
  assert.strictEqual(process.env.REMOTE_ENV_TEST_X, 'local',
    'a .env.local key must beat the remote-fetched value');
  assert.strictEqual(process.env.REMOTE_ENV_TEST_Y, 'remote',
    'a key only present remotely must be filled from PALBASE_REMOTE_ENV_FILE');
  assert.strictEqual(process.env.REMOTE_ENV_TEST_Z, 'shell',
    'a real shell env var must beat both .env.local and remote');
});

test('Cache: set then get round-trips JSON values', async () => {
  const c = makeLocalCache();
  await c.set('s', 'hello');
  await c.set('o', { a: 1, b: [2, 3] });
  await c.set('n', 42);
  assert.strictEqual(await c.get('s'), 'hello');
  assert.deepStrictEqual(await c.get('o'), { a: 1, b: [2, 3] });
  assert.strictEqual(await c.get('n'), 42);
});

test('Cache: get on a missing key returns null', async () => {
  const c = makeLocalCache();
  assert.strictEqual(await c.get('nope'), null);
});

test('Cache: del removes a key', async () => {
  const c = makeLocalCache();
  await c.set('k', 'v');
  await c.del('k');
  assert.strictEqual(await c.get('k'), null);
});

test('Cache: TTL expiry is honored (expired key reads as a miss)', async () => {
  const c = makeLocalCache();
  // 1-second TTL set with a value that should vanish after the deadline.
  await c.set('ttl', 'soon', 1);
  assert.strictEqual(await c.get('ttl'), 'soon'); // still live immediately
  await new Promise((r) => setTimeout(r, 1100));
  assert.strictEqual(await c.get('ttl'), null); // expired → miss
});

test('Cache: a zero/absent TTL never expires', async () => {
  const c = makeLocalCache();
  await c.set('forever', 'x'); // no ttl
  await c.set('zero', 'y', 0); // ttl 0 → no expiry
  await new Promise((r) => setTimeout(r, 50));
  assert.strictEqual(await c.get('forever'), 'x');
  assert.strictEqual(await c.get('zero'), 'y');
});

test('Cache: sweep evicts a never-read-again expired key (no unbounded growth)', async () => {
  const c = makeLocalCache();
  // A short-TTL key that we NEVER read again after writing: lazy-on-read expiry
  // would never fire for it, so without the sweeper the Map would grow forever.
  await c.set('leak', 'gone-soon', 1);
  // A no-TTL key the sweeper must NOT touch.
  await c.set('keep', 'stays');
  assert.strictEqual(c.size(), 2, 'both keys present before expiry');

  // Wait past the TTL deadline without ever reading 'leak'.
  await new Promise((r) => setTimeout(r, 1100));

  // The key is still physically in the Map (no read has lazily evicted it).
  assert.strictEqual(c.size(), 2, 'expired-but-unread key still occupies the Map');

  // Run one sweep (what the unref'd singleton interval calls): it must drop the
  // expired key and report it, while leaving the live no-TTL key alone.
  const dropped = c.sweep();
  assert.strictEqual(dropped, 1, 'sweep drops exactly the one expired entry');
  assert.strictEqual(c.size(), 1, 'Map shrank — expired key physically removed');
  assert.strictEqual(await c.get('keep'), 'stays', 'live no-TTL key survives the sweep');
  assert.strictEqual(await c.get('leak'), null, 'swept key reads as a miss');
});

test('Cache: sweep keeps lazy-on-read eviction working between sweeps', async () => {
  const c = makeLocalCache();
  await c.set('lazy', 'v', 1);
  await new Promise((r) => setTimeout(r, 1100));
  // No sweep yet — a READ must still expire it (lazy eviction kept alongside the
  // sweeper, since a key can expire in the gap between two sweeps).
  assert.strictEqual(await c.get('lazy'), null, 'read expires the key without a sweep');
  assert.strictEqual(c.size(), 0, 'lazy read also removed it from the Map');
});

test('Cache: incr increments atomically and starts from 0', async () => {
  const c = makeLocalCache();
  assert.strictEqual(await c.incr('count'), 1);
  assert.strictEqual(await c.incr('count'), 2);
  assert.strictEqual(await c.incr('count'), 3);
  assert.strictEqual(await c.get('count'), 3);
});

test('Cache: incr coerces a non-number current value (parse-or-zero)', async () => {
  const c = makeLocalCache();
  await c.set('mixed', 'not-a-number');
  assert.strictEqual(await c.incr('mixed'), 1);
});

test('Cache: getOrSet fills on miss, returns cached on hit, runs fn once', async () => {
  const c = makeLocalCache();
  let calls = 0;
  const fn = async () => {
    calls += 1;
    return { built: true };
  };
  const first = await c.getOrSet('go', 60, fn);
  const second = await c.getOrSet('go', 60, fn);
  assert.deepStrictEqual(first, { built: true });
  assert.deepStrictEqual(second, { built: true });
  assert.strictEqual(calls, 1, 'fn must run only on the miss');
});

test('Queue: push runs the registered worker and returns a jobId', async () => {
  workerRegistry.clear();
  let received = null;
  let meta = null;
  workerRegistry.set('send-email', {
    name: 'send-email',
    handler: async (payload, m) => {
      received = payload;
      meta = m;
    },
  });

  const q = makeLocalQueue();
  const { jobId } = await q.push('send-email', { to: 'a@b.c' });
  assert.match(jobId, /^job_dev_/, 'push must return a jobId');

  // The handler runs asynchronously (setImmediate) — wait a tick for it.
  await new Promise((r) => setImmediate(r));
  assert.deepStrictEqual(received, { to: 'a@b.c' }, 'worker must receive the payload');
  assert.ok(meta && typeof meta.env === 'object', 'worker meta must carry env');
  assert.strictEqual(meta.user, null);
});

test('Queue: push to an unknown worker still resolves with a jobId (no throw)', async () => {
  workerRegistry.clear();
  const q = makeLocalQueue();
  const { jobId } = await q.push('does-not-exist', { x: 1 });
  assert.match(jobId, /^job_dev_/, 'push must still return a jobId for a missing worker');
});

test('Queue: jobIds are unique across pushes', async () => {
  workerRegistry.clear();
  workerRegistry.set('w', { name: 'w', handler: async () => {} });
  const q = makeLocalQueue();
  const a = await q.push('w', {});
  const b = await q.push('w', {});
  assert.notStrictEqual(a.jobId, b.jobId);
});
