'use strict';

/**
 * Unit test for buildDbEdgeClient (module-clients.js) — the `palbase serve`
 * Database client that proxies the @palbase/backend DBClient surface to the
 * br-pod's authenticated edge at POST <gateway>/v1/backend-db/<op>.
 *
 * Runnable with a plain `node dev_db_edge_test.js` (no test framework) — uses
 * a fakeFetch that captures requests so we can assert the exact wire contract:
 * URL, apikey header, Bearer header, request bodies, the findMany eq-map
 * lowering, transaction begin/commit/rollback token threading, and the KEYLESS
 * service-role (asService) path: the project OWNER's session token rides as the
 * Bearer + a non-authoritative `X-Palbase-Request-Role: service_role` intent
 * hint. NO service-role apikey ever touches the laptop — the edge verifies
 * ownership server-side and grants service_role.
 */

const assert = require('assert');
const { buildDbEdgeClient } = require('./module-clients.js');

// ─── fakeFetch ───────────────────────────────────────────────────────────
// Records every request and replies with a scripted response. `script` maps a
// `${op}` (the last path segment) to a function (req) => { status, json } OR a
// plain { status, json }. Default: 200 with {} so a missing script entry is
// still a 2xx (callers that don't care about the body still pass).
function makeFakeFetch(script) {
  const calls = [];
  async function fakeFetch(url, options) {
    const opts = options || {};
    let body;
    try { body = opts.body ? JSON.parse(opts.body) : undefined; } catch { body = opts.body; }
    const headers = opts.headers || {};
    const op = url.split('/').pop();
    calls.push({ url, op, method: opts.method, headers, body });

    let spec = script && script[op];
    if (typeof spec === 'function') spec = spec({ url, op, headers, body });
    const status = spec && spec.status !== undefined ? spec.status : 200;
    const jsonBody = spec && 'json' in spec ? spec.json : {};
    return {
      status,
      ok: status >= 200 && status < 300,
      async json() { return jsonBody; },
      async text() { return JSON.stringify(jsonBody); },
    };
  }
  fakeFetch.calls = calls;
  return fakeFetch;
}

const BASE = 'https://abc123m.dev.palbase.studio';
const ANON_KEY = 'pb_abc123m_canon0000000000000000';
const OWNER_TOKEN = 'owner-session-token';
const INTENT_HEADER = 'X-Palbase-Request-Role';

let passed = 0;
function ok(name) { passed += 1; console.log(`  ok - ${name}`); }

async function run() {
  // ── 1. insert hits /v1/backend-db/insert with the right body + apikey + Bearer
  {
    const fetchImpl = makeFakeFetch({ insert: { json: { row: { id: 'r1', title: 't' } } } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    const row = await db.insert('todos', { title: 't' });
    const call = fetchImpl.calls[0];
    assert.strictEqual(call.url, `${BASE}/v1/backend-db/insert`, 'insert URL');
    assert.strictEqual(call.method, 'POST', 'insert method');
    assert.strictEqual(call.headers.apikey, ANON_KEY, 'insert apikey = publishable');
    assert.strictEqual(call.headers.Authorization, 'Bearer usertok', 'insert Bearer = user token');
    assert.ok(!(INTENT_HEADER in call.headers), 'non-service op carries no intent hint');
    assert.strictEqual(call.headers['Content-Type'], 'application/json', 'insert content-type');
    assert.deepStrictEqual(call.body, { table: 'todos', data: { title: 't' } }, 'insert body');
    assert.deepStrictEqual(row, { id: 'r1', title: 't' }, 'insert returns r.row (unwrapped)');
    ok('insert: URL + apikey + Bearer + body + unwrap r.row (no intent hint)');
  }

  // ── 2. update → {table,data,where:{id}}, returns r.row
  {
    const fetchImpl = makeFakeFetch({ update: { json: { row: { id: 'r1', done: true } } } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    const row = await db.update('todos', 'r1', { done: true });
    const call = fetchImpl.calls[0];
    assert.strictEqual(call.url, `${BASE}/v1/backend-db/update`, 'update URL');
    assert.deepStrictEqual(call.body, { table: 'todos', data: { done: true }, where: { id: 'r1' } }, 'update body maps id→where.id');
    assert.deepStrictEqual(row, { id: 'r1', done: true }, 'update returns r.row');
    ok('update: id→where:{id} body + unwrap r.row');
  }

  // ── 3. delete → {table,where:{id}}, returns undefined
  {
    const fetchImpl = makeFakeFetch({ delete: { json: { deleted: 1 } } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    const result = await db.delete('todos', 'r1');
    const call = fetchImpl.calls[0];
    assert.strictEqual(call.url, `${BASE}/v1/backend-db/delete`, 'delete URL');
    assert.deepStrictEqual(call.body, { table: 'todos', where: { id: 'r1' } }, 'delete body maps id→where.id');
    assert.strictEqual(result, undefined, 'delete returns undefined');
    ok('delete: id→where:{id} body + returns undefined');
  }

  // ── 4. findById → {table,id}, returns r.row || null
  {
    const fetchImpl = makeFakeFetch({ findById: { json: { row: null } } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    const row = await db.findById('todos', 'missing');
    const call = fetchImpl.calls[0];
    assert.strictEqual(call.url, `${BASE}/v1/backend-db/findById`, 'findById URL');
    assert.deepStrictEqual(call.body, { table: 'todos', id: 'missing' }, 'findById body');
    assert.strictEqual(row, null, 'findById returns null when r.row is null');
    ok('findById: {table,id} body + null fallback');
  }

  // ── 5. findMany with eq-map query → opts.where; returns r.rows || []
  {
    const fetchImpl = makeFakeFetch({ findMany: { json: { rows: [{ id: 'a' }, { id: 'b' }] } } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    const rows = await db.findMany('todos', { user_id: 'u1', done: false });
    const call = fetchImpl.calls[0];
    assert.strictEqual(call.url, `${BASE}/v1/backend-db/findMany`, 'findMany URL');
    assert.deepStrictEqual(call.body, {
      table: 'todos',
      opts: { where: [
        { field: 'user_id', operator: 'eq', value: 'u1' },
        { field: 'done', operator: 'eq', value: false },
      ] },
    }, 'findMany eq-map → opts.where');
    assert.deepStrictEqual(rows, [{ id: 'a' }, { id: 'b' }], 'findMany returns r.rows');
    ok('findMany: eq-map query → opts.where [{field,operator:eq,value}]');
  }

  // ── 6. findMany WITHOUT query omits opts entirely
  {
    const fetchImpl = makeFakeFetch({ findMany: { json: { rows: [] } } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    const rows = await db.findMany('todos');
    const call = fetchImpl.calls[0];
    assert.deepStrictEqual(call.body, { table: 'todos' }, 'findMany without query omits opts');
    assert.deepStrictEqual(rows, [], 'findMany returns [] when r.rows empty');
    ok('findMany: no query omits opts; r.rows||[] empty fallback');
  }

  // ── 7. query → {sql,params}; returns r.rows || []
  {
    const fetchImpl = makeFakeFetch({ query: { json: { rows: [{ n: 1 }] } } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    const rows = await db.query('SELECT 1 WHERE x=$1', [42]);
    const call = fetchImpl.calls[0];
    assert.strictEqual(call.url, `${BASE}/v1/backend-db/query`, 'query URL');
    assert.deepStrictEqual(call.body, { sql: 'SELECT 1 WHERE x=$1', params: [42] }, 'query body');
    assert.deepStrictEqual(rows, [{ n: 1 }], 'query returns r.rows');
    ok('query: {sql,params} body + r.rows');
  }

  // ── 8. no user token → NO Authorization header, NO intent hint (anonymous path)
  {
    const fetchImpl = makeFakeFetch({ findMany: { json: { rows: [] } } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => null, getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    await db.findMany('todos');
    const call = fetchImpl.calls[0];
    assert.strictEqual(call.headers.apikey, ANON_KEY, 'anon apikey = publishable');
    assert.ok(!('Authorization' in call.headers), 'no Bearer when user token absent');
    assert.ok(!(INTENT_HEADER in call.headers), 'no intent hint on the non-service path');
    ok('no user token: apikey only, no Bearer, no intent hint (anonymous path)');
  }

  // ── 9. transaction: begin (read tx_token) + commit; ops threaded with token
  {
    const fetchImpl = makeFakeFetch({
      begin: { json: { tx_token: 'TX1' } },
      insert: { json: { row: { id: 'r1' } } },
      commit: { json: {} },
    });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    const result = await db.transaction(async (tx) => {
      const row = await tx.insert('todos', { title: 'x' });
      return row.id;
    });
    assert.strictEqual(result, 'r1', 'transaction returns callback value');
    const ops = fetchImpl.calls.map((c) => c.op);
    assert.deepStrictEqual(ops, ['begin', 'insert', 'commit'], 'begin → insert → commit order');
    const beginCall = fetchImpl.calls[0];
    assert.strictEqual(beginCall.url, `${BASE}/v1/backend-db/begin`, 'begin URL');
    assert.deepStrictEqual(beginCall.body, {}, 'begin body is {}');
    assert.ok(!('X-Transaction-Token' in beginCall.headers), 'begin carries no tx token');
    const insertCall = fetchImpl.calls[1];
    assert.strictEqual(insertCall.headers['X-Transaction-Token'], 'TX1', 'insert threads tx_token');
    const commitCall = fetchImpl.calls[2];
    assert.strictEqual(commitCall.headers['X-Transaction-Token'], 'TX1', 'commit threads tx_token');
    ok('transaction: begin reads tx_token, ops + commit thread it');
  }

  // ── 10. transaction rollback on throw
  {
    const fetchImpl = makeFakeFetch({
      begin: { json: { tx_token: 'TX2' } },
      insert: { json: { row: { id: 'r1' } } },
      rollback: { json: {} },
    });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    let threw = null;
    try {
      await db.transaction(async (tx) => {
        await tx.insert('todos', { title: 'x' });
        throw new Error('boom');
      });
    } catch (err) { threw = err; }
    assert.ok(threw && threw.message === 'boom', 'transaction re-throws the callback error');
    const ops = fetchImpl.calls.map((c) => c.op);
    assert.deepStrictEqual(ops, ['begin', 'insert', 'rollback'], 'begin → insert → rollback on throw');
    const rollbackCall = fetchImpl.calls[2];
    assert.strictEqual(rollbackCall.headers['X-Transaction-Token'], 'TX2', 'rollback threads tx_token');
    ok('transaction: rollback on throw + re-throws');
  }

  // ── 11. begin failure (no tx_token) → throw
  {
    const fetchImpl = makeFakeFetch({ begin: { json: {} } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    let threw = null;
    try {
      await db.transaction(async () => 1);
    } catch (err) { threw = err; }
    assert.ok(threw && /transaction/i.test(threw.message), 'begin with no tx_token throws');
    ok('transaction: begin without tx_token throws');
  }

  // ── 12. asService (KEYLESS): owner-session Bearer + intent hint, publishable apikey
  {
    const fetchImpl = makeFakeFetch({ findMany: { json: { rows: [{ id: 's' }] } } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    const svc = db.asService();
    const rows = await svc.findMany('todos');
    const call = fetchImpl.calls[0];
    assert.strictEqual(call.headers.apikey, ANON_KEY, 'asService apikey = publishable (NOT a service-role key)');
    assert.strictEqual(call.headers.Authorization, `Bearer ${OWNER_TOKEN}`, 'asService Bearer = OWNER session token');
    assert.strictEqual(call.headers[INTENT_HEADER], 'service_role', 'asService carries the service_role intent hint');
    assert.deepStrictEqual(rows, [{ id: 's' }], 'asService findMany returns rows');
    // asService sibling does NOT re-expose asService (no double-bypass).
    assert.strictEqual(typeof svc.asService, 'undefined', 'asService sibling does not re-expose asService');
    assert.strictEqual(typeof svc.transaction, 'function', 'asService sibling exposes transaction');
    ok('asService: keyless — publishable apikey + owner Bearer + intent hint, no re-exposed asService');
  }

  // ── 13. asService transaction (KEYLESS): owner Bearer + intent hint on every op
  {
    const fetchImpl = makeFakeFetch({
      begin: { json: { tx_token: 'TXS' } },
      insert: { json: { row: { id: 's1' } } },
      commit: { json: {} },
    });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    await db.asService().transaction(async (tx) => { await tx.insert('todos', { x: 1 }); });
    for (const call of fetchImpl.calls) {
      assert.strictEqual(call.headers.apikey, ANON_KEY, `${call.op} uses publishable apikey (no service-role key)`);
      assert.strictEqual(call.headers.Authorization, `Bearer ${OWNER_TOKEN}`, `${call.op} sends owner Bearer in service tx`);
      assert.strictEqual(call.headers[INTENT_HEADER], 'service_role', `${call.op} carries service_role intent hint`);
    }
    assert.strictEqual(fetchImpl.calls[1].headers['X-Transaction-Token'], 'TXS', 'service tx threads token');
    ok('asService transaction: keyless owner Bearer + intent hint + token threaded');
  }

  // ── 14. asService WITHOUT an owner token (serve not logged in) → owner hint
  {
    const fetchImpl = makeFakeFetch({});
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => undefined, fetchImpl,
    });
    let threw = null;
    try { db.asService(); } catch (err) { threw = err; }
    assert.ok(threw, 'asService without an owner token throws');
    assert.ok(/palbase login/i.test(threw.message), 'message hints `palbase login`');
    assert.ok(/OWNER/i.test(threw.message), 'message names the project OWNER');
    assert.ok(/git push/i.test(threw.message), 'message offers git push as the alternative');
    assert.strictEqual(fetchImpl.calls.length, 0, 'asService throws before any request (no pre-check fetch)');
    ok('asService without owner token: actionable owner/login hint thrown');
  }

  // ── 15. 404 → clear "table not found / git push" error (code not_found)
  {
    const fetchImpl = makeFakeFetch({ insert: { status: 404, json: { error: 'not_found' } } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    let threw = null;
    try { await db.insert('ghost', {}); } catch (err) { threw = err; }
    assert.ok(threw, '404 throws');
    assert.strictEqual(threw.code, 'not_found', '404 error code = not_found');
    assert.ok(/git push/i.test(threw.message), '404 message hints git push to deploy the migration');
    ok('404: not_found code + git-push hint');
  }

  // ── 16. other non-2xx → "db edge <op> → <status>: <body>"
  {
    const fetchImpl = makeFakeFetch({ insert: { status: 500, json: { error: 'boom' } } });
    const db = buildDbEdgeClient({
      baseUrl: BASE, apiKey: ANON_KEY,
      getUserToken: () => 'usertok', getOwnerToken: () => OWNER_TOKEN, fetchImpl,
    });
    let threw = null;
    try { await db.insert('todos', {}); } catch (err) { threw = err; }
    assert.ok(threw, '500 throws');
    assert.ok(/db edge insert/.test(threw.message), 'message names the op');
    assert.ok(/500/.test(threw.message), 'message includes the status');
    ok('non-2xx: "db edge <op> → <status>" error');
  }

  console.log(`\n${passed} assertion group(s) passed.`);
}

run().catch((err) => {
  console.error('\nFAIL:', err && err.stack ? err.stack : err);
  process.exit(1);
});
