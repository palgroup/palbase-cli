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

// require()ing dev-server.js is side-effect-light because main() is guarded by
// `require.main === module`; the only top-level effect is one throwaway temp dir.
const { makeLocalCache, makeLocalQueue, workerRegistry } = require('./dev-server.js');

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
