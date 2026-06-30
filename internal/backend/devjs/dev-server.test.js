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
const { makeLocalCache, makeLocalQueue, workerRegistry, parseDotenv } = require('./dev-server.js');

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
