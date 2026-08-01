// Unit tests for tx_analysis.js — the TxPlan Ref-truthiness build gate.
//
// 8 positive fixtures (one per forbidden pattern) assert BOTH that the
// violation is caught AND that its file:line:column is correct — a gate that
// reports the wrong line sends the author hunting through the wrong file.
// 6 negative fixtures (legitimate plans, five of them close variants of the
// REAL smartex transactions the TxPlan cutover plan is migrating — see
// sdk/palbase-ts backend/src/db/tx-plan-cases.ts) assert ZERO violations —
// a gate that rejects legitimate plans is worse than no gate at all.
//
// Run: node --test internal/backend/devjs/tx_analysis.test.js

const test = require('node:test');
const assert = require('node:assert');
const { findTxPlanViolations, assertNoTxPlanViolations, TxAnalysisError } = require('./tx_analysis.js');

// lineOf locates `needle`'s first occurrence in `src` and returns its 1-based
// {line, column} — the SAME coordinate system findTxPlanViolations reports.
// Fixtures are written so each needle is unique within its own source, so
// this never has to guess between two matches.
function lineOf(src, needle) {
  const lines = src.split('\n');
  for (let i = 0; i < lines.length; i++) {
    const col = lines[i].indexOf(needle);
    if (col !== -1) return { line: i + 1, column: col + 1 };
  }
  throw new Error(`fixture is missing its own position marker: ${JSON.stringify(needle)}`);
}

// ── positive fixtures — one forbidden pattern each ──────────────────────────
//
// Two are adapted from REAL smartex source (cited in each fixture's comment,
// verified against the file at fixture-authoring time): the exact idiom that
// file uses, ported field-wise onto a Ref the way a developer migrating that
// controller to the plan API would plausibly (and wrongly) write it. The
// other six are synthetic but shaped like the SDK's own tx-plan-cases.ts.

const POSITIVE = [
  {
    name: 'if (<ref>) — adapted from smartex controllers/invite.controller.ts:60 (`if (locked.accepted_at) throw new Conflict(...)`)',
    pattern: 'if-ref',
    marker: 'locked.accepted_at',
    src: [
      'import { Database, Conflict, NotFound } from "@palbase/backend";',
      '',
      'export function acceptInvite(token) {',
      '  return Database.transaction((tx) => {',
      '    const locked = tx.tables.invites',
      '      .updateWhere({ token }, {})',
      '      .expectOne(new NotFound("Davet bulunamadi"));',
      '    if (locked.accepted_at) throw new Conflict("Davet zaten kullanilmis");',
      '  });',
      '}',
    ].join('\n'),
  },
  {
    name: '!<ref> — adapted from smartex controllers/savings.controller.ts:290 (`if (!pot) throw new NotFound(...)`)',
    pattern: 'not-ref',
    marker: 'pot.household_id',
    src: [
      'import { Database, NotFound } from "@palbase/backend";',
      '',
      'export function addContribution(potId, hid) {',
      '  return Database.transaction((tx) => {',
      '    const pot = tx.tables.savings_pots',
      '      .updateWhere({ id: potId, household_id: hid }, {})',
      '      .expectOne(new NotFound("Kasa bulunamadi"));',
      '    if (!pot.household_id) throw new NotFound("Kasa bulunamadi");',
      '  });',
      '}',
    ].join('\n'),
  },
  {
    name: '<ref> ? : ',
    pattern: 'ternary-ref',
    marker: 'entry.approved',
    src: [
      'import { Database, NotFound } from "@palbase/backend";',
      '',
      'export function labelEntry(entryId) {',
      '  return Database.transaction((tx) => {',
      '    const entry = tx.tables.entries.updateWhere({ id: entryId }, {}).expectOne(new NotFound("not found"));',
      '    const label = entry.approved ? "approved" : "pending";',
      '    tx.tables.entries.updateWhere({ id: entryId }, { note: label });',
      '  });',
      '}',
    ].join('\n'),
  },
  {
    name: '<ref> ??',
    pattern: 'nullish-ref',
    marker: 'entry.note ??',
    src: [
      'import { Database, NotFound } from "@palbase/backend";',
      '',
      'export function noteOrDefault(entryId) {',
      '  return Database.transaction((tx) => {',
      '    const entry = tx.tables.entries.updateWhere({ id: entryId }, {}).expectOne(new NotFound("not found"));',
      '    const note = entry.note ?? "no note";',
      '    tx.tables.entries.updateWhere({ id: entryId }, { note });',
      '  });',
      '}',
    ].join('\n'),
  },
  {
    name: 'ref interpolated into a template literal',
    pattern: 'template-ref',
    marker: 'pot.balance_kurus}',
    src: [
      'import { Database, NotFound } from "@palbase/backend";',
      '',
      'export function auditLog(potId) {',
      '  return Database.transaction((tx) => {',
      '    const pot = tx.tables.savings_pots.updateWhere({ id: potId }, {}).expectOne(new NotFound("not found"));',
      '    tx.tables.audit_log.insert({ message: `pot balance is now ${pot.balance_kurus}` });',
      '  });',
      '}',
    ].join('\n'),
  },
  {
    name: 'arithmetic on a ref',
    pattern: 'arithmetic-ref',
    marker: 'pot.balance_kurus * 2',
    src: [
      'import { Database, NotFound } from "@palbase/backend";',
      '',
      'export function bumpBalance(potId) {',
      '  return Database.transaction((tx) => {',
      '    const pot = tx.tables.savings_pots.updateWhere({ id: potId }, {}).expectOne(new NotFound("not found"));',
      '    const doubled = pot.balance_kurus * 2;',
      '    tx.tables.savings_pots.updateWhere({ id: potId }, { balance_kurus: doubled });',
      '  });',
      '}',
    ].join('\n'),
  },
  {
    name: 'ref escapes the callback (assigned to an outer-scope variable)',
    pattern: 'ref-escape',
    marker: 'leakedHouseholdId = inv.household_id',
    src: [
      'import { Database, NotFound } from "@palbase/backend";',
      '',
      'export function acceptInvite(token) {',
      '  let leakedHouseholdId;',
      '  Database.transaction((tx) => {',
      '    const inv = tx.tables.invites.updateWhere({ token }, {}).expectOne(new NotFound("not found"));',
      '    leakedHouseholdId = inv.household_id;',
      '  });',
      '  return leakedHouseholdId;',
      '}',
    ].join('\n'),
  },
  {
    name: 'async plan callback (await in the plan callback)',
    pattern: 'async-plan-callback',
    marker: 'async (tx)',
    src: [
      'import { Database, NotFound } from "@palbase/backend";',
      '',
      'export function legacyStyleTx(potId) {',
      '  return Database.transaction(async (tx) => {',
      '    const pot = tx.tables.savings_pots.updateWhere({ id: potId }, {}).expectOne(new NotFound("not found"));',
      '    return pot;',
      '  });',
      '}',
    ].join('\n'),
  },
];

for (const tc of POSITIVE) {
  test(`tx_analysis catches: ${tc.name}`, () => {
    const violations = findTxPlanViolations(tc.src, 'fixture.ts');
    assert.strictEqual(violations.length, 1,
      `expected exactly 1 violation, got ${violations.length}: ${JSON.stringify(violations)}`);
    const v = violations[0];
    assert.strictEqual(v.pattern, tc.pattern);
    const want = lineOf(tc.src, tc.marker);
    assert.strictEqual(v.line, want.line, `wrong line — the gate must point at the actual offending code`);
    assert.strictEqual(v.column, want.column, `wrong column`);
    assert.ok(v.message.length > 20, 'message must actually explain the problem, not just name it');

    // assertNoTxPlanViolations (the throwing convenience wrapper) must throw
    // TxAnalysisError with the SAME file:line:col in its message.
    assert.throws(
      () => assertNoTxPlanViolations(tc.src, 'fixture.ts'),
      (err) => {
        assert.ok(err instanceof TxAnalysisError);
        assert.match(err.message, new RegExp(`fixture\\.ts:${want.line}:${want.column}`));
        return true;
      },
    );
  });
}

test('positive fixtures cover exactly the 8 patterns the plan requires, no duplicates', () => {
  const patterns = POSITIVE.map((tc) => tc.pattern);
  assert.strictEqual(new Set(patterns).size, 8, 'each positive fixture must exercise a DISTINCT pattern');
});

// ── negative fixtures — legitimate plans, must produce ZERO violations ─────
//
// 1-5 are close variants of the real smartex transactions in
// sdk/palbase-ts backend/src/db/tx-plan-cases.ts (Case 1 through Case 5 —
// uploadStatement / acceptInvite / addDividend / addContribution /
// wireCoverage). #6 is a file with no Database.transaction call at all, to
// prove ordinary async/await controller code is never touched.

const NEGATIVE = [
  {
    name: 'uploadStatement (tx-plan-cases.ts Case 1) — ref used as an insertMany field value, and returned',
    src: [
      'import { Database, PalError } from "@palbase/backend";',
      '',
      'export function uploadStatement(input) {',
      '  const { householdId, fileName, sha, parsed } = input;',
      '  return Database.transaction((tx) => {',
      '    const st = tx.tables.statements',
      '      .insert({ household_id: householdId, file_name: fileName, file_sha256: sha })',
      '      .expectOne(new PalError(500, "statement_insert_failed", "Ekstre kaydedilemedi"));',
      '',
      '    const rows = [];',
      '    for (const l of parsed.lines) {',
      '      if (l.signedAmountKurus === 0) continue;',
      '      rows.push({ statement_id: st.id, category: l.resolvedCategory });',
      '    }',
      '    tx.tables.statement_lines.insertMany(rows);',
      '',
      '    return st;',
      '  });',
      '}',
    ].join('\n'),
  },
  {
    name: 'acceptInvite (tx-plan-cases.ts Case 2) — the CORRECT rewrite of the buggy fixture above',
    src: [
      'import { Database, Conflict, NotFound, now } from "@palbase/backend";',
      '',
      'export function acceptInvite(input) {',
      '  const { token, userId } = input;',
      '  return Database.transaction((tx) => {',
      '    const inv = tx.tables.invites',
      '      .select({ token }, { limit: 1, lock: "update" })',
      '      .expectOne(new NotFound("Davet bulunamadi"));',
      '',
      '    tx.tables.invites',
      '      .updateWhere({ token, accepted_at: null }, { accepted_at: now() })',
      '      .expectOne(new Conflict("Davet zaten kullanilmis", "invite_used"));',
      '',
      '    tx.tables.memberships',
      '      .select({ user_id: userId, household_id: inv.household_id })',
      '      .expectNone(new Conflict("Zaten bu haneye uyesiniz", "already_member"));',
      '',
      '    tx.tables.memberships.deleteWhere({ user_id: userId });',
      '    tx.tables.memberships.insert({ user_id: userId, household_id: inv.household_id, role: inv.role });',
      '',
      '    return { householdId: inv.household_id };',
      '  });',
      '}',
    ].join('\n'),
  },
  {
    name: 'addDividend (tx-plan-cases.ts Case 3) — a NON-ref template literal must not false-positive',
    src: [
      'import { Database, PalError } from "@palbase/backend";',
      '',
      'export function addDividend(input) {',
      '  return Database.transaction((tx) => {',
      '    const entry = tx.tables.entries',
      '      .insert({',
      '        household_id: input.householdId,',
      '        title: `Temettu: ${input.holdingLabel}`,',
      '        amount_kurus: input.amountKurus,',
      '      })',
      '      .expectOne(new PalError(500, "dividend_entry_insert_failed", "Temettu kaydedilemedi"));',
      '',
      '    tx.tables.dividend_entries.insert({',
      '      holding_id: input.holdingId, amount_kurus: input.amountKurus, entry_id: entry.id,',
      '    });',
      '  });',
      '}',
    ].join('\n'),
  },
  {
    name: 'addContribution (tx-plan-cases.ts Case 4) — inc(), no field read at all',
    src: [
      'import { Database, NotFound, inc } from "@palbase/backend";',
      '',
      'export function addContribution(input) {',
      '  return Database.transaction((tx) => {',
      '    tx.tables.savings_contributions.insert({',
      '      household_id: input.householdId, pot_id: input.potId, month: input.month,',
      '      amount_kurus: String(input.amountKurus),',
      '    });',
      '',
      '    tx.tables.savings_pots',
      '      .updateWhere(',
      '        { id: input.potId, household_id: input.householdId },',
      '        { balance_kurus: inc(input.amountKurus) },',
      '      )',
      '      .expectOne(new NotFound("Kasa bulunamadi"));',
      '  });',
      '}',
    ].join('\n'),
  },
  {
    name: 'wireCoverage (tx-plan-cases.ts Case 5) — expectAtMost/expectAtLeast/dec/unfiltered select/deleteWhere',
    src: [
      'import { Database, NotFound, PalError, dec } from "@palbase/backend";',
      '',
      'export function wireCoverage(input) {',
      '  return Database.transaction((tx) => {',
      '    tx.tables.savings_pots',
      '      .select(undefined, { limit: 3 })',
      '      .expectAtMost(3, new PalError(500, "too_many_pots", "too many pots"));',
      '',
      '    const pot = tx.tables.savings_pots',
      '      .select({ household_id: input.householdId }, { limit: 1 })',
      '      .expectOne(new NotFound("Kasa bulunamadi"));',
      '',
      '    tx.tables.savings_contributions',
      '      .insertMany([{ household_id: input.householdId, pot_id: pot.id, month: input.month, amount_kurus: "1" }])',
      '      .expectAtLeast(1, new PalError(500, "contributions_not_written", "contributions not written"));',
      '',
      '    tx.tables.savings_pots',
      '      .updateWhere({ id: pot.id }, { balance_kurus: dec(3) })',
      '      .expectOne(new NotFound("Kasa bulunamadi"));',
      '',
      '    tx.tables.savings_contributions.deleteWhere({ pot_id: pot.id, month: "1970-01" });',
      '',
      '    return { potId: pot.id };',
      '  });',
      '}',
    ].join('\n'),
  },
  {
    name: 'no Database.transaction call at all — ordinary async controller code must be left alone',
    src: [
      'import { Database } from "@palbase/backend";',
      '',
      'export async function listPots(householdId) {',
      '  const pots = await Database.tables.savings_pots.findMany({ household_id: householdId });',
      '  if (!pots) return [];',
      '  return pots.map((p) => (p ? { id: p.id, name: p.name } : null));',
      '}',
    ].join('\n'),
  },
];

for (const tc of NEGATIVE) {
  test(`tx_analysis passes: ${tc.name}`, () => {
    const violations = findTxPlanViolations(tc.src, 'fixture.ts');
    assert.deepStrictEqual(violations, [],
      `a legitimate plan must produce ZERO violations — rejecting real code is worse than no gate`);
    assert.doesNotThrow(() => assertNoTxPlanViolations(tc.src, 'fixture.ts'));
  });
}

// ── TypeScript parser guard (same rule as return_types.js / throw_analysis.js) ─

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
  // Every fixture test above already ran a REAL findTxPlanViolations call,
  // which caches the resolved `typescript` module inside tx_analysis.js's own
  // module-level `ts` variable — loadTS() would short-circuit past the fake
  // below without this. Force a fresh module instance (mirrors the ordering
  // trick build-check.test.js uses — a .js fixture there, a cache reset here
  // — same underlying problem: prove the fake is actually reached).
  delete require.cache[require.resolve('./tx_analysis.js')];
  const fresh = require('./tx_analysis.js');
  try {
    withFakeTypescript(() => ({ version: '7.0.2', versionMajorMinor: '7.0' }), () => {
      assert.throws(
        () => fresh.findTxPlanViolations('export const x = 1;', 'x.ts'),
        (err) => {
          assert.doesNotMatch(err.message, /Cannot read properties of undefined/);
          assert.match(err.message, /TypeScript 5 compiler API/);
          assert.match(err.message, /v7\.0\.2/);
          return true;
        },
      );
    });
  } finally {
    delete require.cache[require.resolve('./tx_analysis.js')];
  }
});
