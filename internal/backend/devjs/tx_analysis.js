'use strict';

/**
 * Palbase Backend Runtime — TxPlan Ref-Truthiness Build Gate
 *
 * `Database.transaction((tx) => { … })` builds a PLAN: the callback runs
 * SYNCHRONOUSLY, once, before any op is sent — op results are not real values,
 * they are `Ref<T>` placeholders (a Proxy the server resolves after the whole
 * plan is sent as one request). A field read off a plan row — `st.id`,
 * `inv.accepted_at` — is one of these Refs.
 *
 * The Ref Proxy traps `.then`/`valueOf`/`Symbol.toPrimitive`, so `await ref`,
 * `String(ref)`, and `` `${ref}` `` all throw a descriptive `TxRefError` at
 * RUNTIME. But JS object TRUTHINESS CANNOT BE TRAPPED — `if (inv.accepted_at)`
 * is always true (a Ref is an object), silently. `tsc` cannot catch this either
 * (a Ref is typed as a Ref, and `if` accepts any type). The result is a
 * transaction that COMMITS THE WRONG DATA, atomically, with no error anywhere —
 * exactly what a transaction exists to prevent.
 *
 * This is the third of three defense layers (see the TxPlan cutover plan):
 * unguarded field reads are a TS *type* error (P3), most misuse throws a
 * runtime TxRefError via the Proxy (P3), and truthiness — the one shape the
 * first two cannot reach — is caught HERE, at build time, by inspecting the
 * SOURCE AST for the syntactic shapes that put a Ref into a boolean/string/
 * arithmetic context. Parser-only (`ts.createSourceFile`), no TypeChecker —
 * same cost profile as return_types.js/throw_analysis.js. It is a heuristic,
 * not a prover; see the module-level "known gaps" list in the CLI's P4 report
 * for what it does NOT catch.
 *
 * POLICY — the one place this pass differs from its siblings: return_types.js
 * enforces its rule as a hard error, but throw_analysis.js is mostly
 * best-effort (an unresolvable throw site just degrades the client's error
 * typing to `.other`, no build failure). A Ref-truthiness miss is silent DATA
 * CORRUPTION, not a DX degradation — there is no "best-effort" mode here.
 * EVERY violation this module finds is a hard build failure. The caller
 * (build-check.js / the deploy stager) must never downgrade its result to a
 * warning or merge it into a softer failure list that could theoretically be
 * bypassed.
 *
 * This module is SHARED VERBATIM by the deploy stager and `palbase build`
 * (the same parity rule as return_types.js/throw_analysis.js — see
 * stager-copy-parity.yml). It needs only `typescript` on NODE_PATH.
 */

let ts = null;
function loadTS() {
  if (ts) return ts;
  let mod;
  try {
    mod = require('typescript');
  } catch (e) {
    throw new Error(TS_PARSER_HELP('the `typescript` package could not be loaded (' + e.message + ')'));
  }
  // TypeScript 7 (Go-native compiler) exports only { version, versionMajorMinor }
  // from its CommonJS entry — no createSourceFile, no ScriptTarget. Say so instead
  // of dying on "Cannot read properties of undefined". Twin of return_types.js /
  // throw_analysis.js.
  if (typeof mod.createSourceFile !== 'function' || !mod.ScriptTarget) {
    throw new Error(TS_PARSER_HELP(
      'the resolved `typescript` (v' + (mod.version || 'unknown') + ') has no compiler API — ' +
        'TypeScript 7 ships the Go-native compiler, whose CommonJS build exposes version metadata only',
    ));
  }
  ts = mod;
  return ts;
}

// One actionable message for both parser-load failures (verbatim twin of the
// return_types.js / throw_analysis.js copies).
function TS_PARSER_HELP(reason) {
  return (
    'palbase needs the TypeScript 5 compiler API to analyze Database.transaction() callbacks, but ' +
    reason +
    '.\n  fix: npm install --save-dev typescript@5   (or re-run once online: the CLI installs its own pinned parser)'
  );
}

class TxAnalysisError extends Error {}

// The plan API's row-guard methods — the ONLY calls that turn a plan op into a
// bound "row" (TxRow) whose FIELD ACCESS yields a Ref<T>. `expectNone` /
// `expectAtLeast` / `expectAtMost` don't produce a single-row binding in any
// real usage we've seen (tx-plan-cases.ts never reads a field off their
// result), but a variable bound from one is treated the same way defensively —
// property access on ANY of these calls' results is a Ref/Ref-like expression.
const ROW_PRODUCING_METHODS = new Set(['expectOne', 'expectNone', 'expectAtLeast', 'expectAtMost']);

// Arithmetic operators (SyntaxKind of a BinaryExpression's operatorToken).
// `+` is included even though it also means string concat — a Ref on either
// side of `+` hits the exact same Proxy trap as a template literal (both
// stringify), so it belongs in the same bucket the plan calls "ref üzerinde
// aritmetik".
function arithmeticOperators(tsapi) {
  return new Set([
    tsapi.SyntaxKind.PlusToken,
    tsapi.SyntaxKind.MinusToken,
    tsapi.SyntaxKind.AsteriskToken,
    tsapi.SyntaxKind.SlashToken,
    tsapi.SyntaxKind.PercentToken,
    tsapi.SyntaxKind.AsteriskAsteriskToken,
  ]);
}

// isRowProducingCall reports whether `node` is `<expr>.expectOne(...)` (or one
// of its ROW_PRODUCING_METHODS siblings) — an inline chain that yields a row
// without ever binding a variable, e.g. `tx.tables.x.select(f).expectOne(e).id`.
function isRowProducingCall(node, tsapi) {
  return (
    !!node &&
    tsapi.isCallExpression(node) &&
    tsapi.isPropertyAccessExpression(node.expression) &&
    ROW_PRODUCING_METHODS.has(node.expression.name.text)
  );
}

// isRefExpr reports whether `node` denotes a Ref<T>-shaped value that is
// ALWAYS an object, and therefore always truthy: a field read off a row
// (`st.id`, or the inline chain form), a bare identifier previously bound
// (directly or transitively) from one such field read — OR the ROW itself,
// bound or inline. A row (`const pot = tx.tables.x.findMany(f).expectOne(e)`)
// is exactly as truthy as any field on it: `if (!pot)` is the SAME bug as
// `if (!pot.x)`, one level up, and is the literal shape of a real bug this
// gate exists to catch (smartex controllers/savings.controller.ts:290:
// `if (!pot) throw new NotFound(...)`, ported as-is onto the plan API).
function isRefExpr(node, tsapi, rowNames, refNames) {
  if (!node) return false;
  if (isRowProducingCall(node, tsapi)) return true; // inline chain, e.g. `if (!tx.tables.x.select(f).expectOne(e))`
  if (tsapi.isIdentifier(node)) return rowNames.has(node.text) || refNames.has(node.text);
  if (tsapi.isPropertyAccessExpression(node) || tsapi.isElementAccessExpression(node)) {
    const obj = node.expression;
    if (tsapi.isIdentifier(obj) && rowNames.has(obj.text)) return true;
    if (isRowProducingCall(obj, tsapi)) return true;
  }
  return false;
}

// escapesCallback reports whether an assignment target's ROOT identifier
// (walking through `.prop`/`[...]` accessors) was NOT declared inside the
// plan callback — i.e. the assignment reaches a variable from an outer scope.
// Destructuring targets and other non-identifier roots are not analyzed here
// (a documented gap, not a silent pass: see findTxPlanViolations' header).
function escapesCallback(node, tsapi, localNames) {
  let root = node;
  while (tsapi.isPropertyAccessExpression(root) || tsapi.isElementAccessExpression(root)) {
    root = root.expression;
  }
  if (!tsapi.isIdentifier(root)) return false;
  return !localNames.has(root.text);
}

function posOf(sf, tsapi, node) {
  const { line, character } = tsapi.getLineAndCharacterOfPosition(sf, node.getStart(sf));
  return { line: line + 1, column: character + 1 };
}

// scanCallback walks one `Database.transaction((tx) => { … })` callback body
// and appends every Ref-misuse violation found to `out`.
function scanCallback(sf, tsapi, fn, fileLabel, out) {
  const ARITH = arithmeticOperators(tsapi);
  const rowNames = new Set();
  const refNames = new Set();
  const localNames = new Set();

  for (const p of fn.parameters) {
    if (tsapi.isIdentifier(p.name)) localNames.add(p.name.text);
  }
  if (fn.body) {
    (function collectLocals(node) {
      if (tsapi.isVariableDeclaration(node) && tsapi.isIdentifier(node.name)) {
        localNames.add(node.name.text);
      }
      tsapi.forEachChild(node, collectLocals);
    })(fn.body);

    // Row/Ref bindings, in one full pre-pass — a lint-style over-approximation:
    // a name bound ANYWHERE in the callback is treated as bound everywhere in
    // it, regardless of statement order. The only code this could mis-flag is
    // code that uses a `const` before its own declaration, which is already
    // broken (TDZ) at runtime.
    (function collectBindings(node) {
      if (tsapi.isVariableDeclaration(node) && tsapi.isIdentifier(node.name) && node.initializer) {
        if (isRowProducingCall(node.initializer, tsapi)) {
          rowNames.add(node.name.text);
        } else if (isRefExpr(node.initializer, tsapi, rowNames, refNames)) {
          refNames.add(node.name.text);
        }
      }
      tsapi.forEachChild(node, collectBindings);
    })(fn.body);

    const ref = (node) => isRefExpr(node, tsapi, rowNames, refNames);
    const report = (node, pattern, message) => {
      out.push(Object.assign({ file: fileLabel, pattern, message }, posOf(sf, tsapi, node)));
    };

    (function visit(node) {
      if (tsapi.isIfStatement(node) && ref(node.expression)) {
        report(node.expression, 'if-ref',
          'if (<ref>) always takes the true branch — a Ref is an object, so it is always truthy; ' +
          'only the server resolves the real value, after this callback has already returned. ' +
          'Express the check as a plan guard instead (a filtered updateWhere/deleteWhere + ' +
          'expectOne/expectNone/expectAtLeast/expectAtMost), not a branch on the field.');
      } else if (
        tsapi.isPrefixUnaryExpression(node) &&
        node.operator === tsapi.SyntaxKind.ExclamationToken &&
        ref(node.operand)
      ) {
        report(node.operand, 'not-ref',
          '!<ref> is always false — a Ref is an object, so negating it never reflects the real value. ' +
          'Express the check as a plan guard instead.');
      } else if (tsapi.isConditionalExpression(node) && ref(node.condition)) {
        report(node.condition, 'ternary-ref',
          '<ref> ? … : … always takes the truthy branch — same trap as `if (<ref>)`.');
      } else if (
        tsapi.isBinaryExpression(node) &&
        node.operatorToken.kind === tsapi.SyntaxKind.QuestionQuestionToken &&
        ref(node.left)
      ) {
        report(node.left, 'nullish-ref',
          '<ref> ?? … never falls through — a Ref is never null/undefined itself; only the ' +
          'eventual resolved value might be.');
      } else if (tsapi.isTemplateExpression(node)) {
        for (const span of node.templateSpans) {
          if (ref(span.expression)) {
            report(span.expression, 'template-ref',
              'A Ref interpolated into a template literal stringifies the PLACEHOLDER, not the ' +
              'eventual value (this also throws TxRefError at runtime — the Proxy traps ' +
              'Symbol.toPrimitive/toString). Read the value after the transaction resolves.');
          }
        }
      } else if (
        tsapi.isBinaryExpression(node) &&
        ARITH.has(node.operatorToken.kind) &&
        (ref(node.left) || ref(node.right))
      ) {
        report(node, 'arithmetic-ref',
          'Arithmetic on a Ref operates on the PLACEHOLDER, not the eventual value (this also ' +
          'throws TxRefError at runtime). Compute this after the transaction resolves, or express ' +
          'it as an inc()/dec() op on the write itself.');
      } else if (
        tsapi.isBinaryExpression(node) &&
        node.operatorToken.kind === tsapi.SyntaxKind.EqualsToken &&
        ref(node.right) &&
        escapesCallback(node.left, tsapi, localNames)
      ) {
        report(node, 'ref-escape',
          'A Ref assigned to a variable declared OUTSIDE this transaction callback escapes it — ' +
          'the callback runs synchronously, once, before the plan is even sent, so nothing outside ' +
          'it can hold a resolved value yet. Return it from the callback instead and read it off ' +
          'the awaited transaction() result.');
      }
      tsapi.forEachChild(node, visit);
    })(fn.body);
  }
}

// isDatabaseTransactionCall reports whether `node` is `Database.transaction(...)`
// — the plan entry point. Simple text match, the same convention
// return_types.js/throw_analysis.js use for `@Controller`/`@Get` — no import
// resolution, so a renamed import of `Database` would not be recognized (a
// documented gap).
function isDatabaseTransactionCall(node, tsapi) {
  if (!tsapi.isCallExpression(node)) return false;
  const callee = node.expression;
  return (
    tsapi.isPropertyAccessExpression(callee) &&
    callee.name.text === 'transaction' &&
    tsapi.isIdentifier(callee.expression) &&
    callee.expression.text === 'Database'
  );
}

/**
 * Parse one source file and return every TxPlan Ref-misuse violation found —
 * empty when the file is clean (including files with no Database.transaction
 * call at all, which are simply not analyzed further).
 *
 * @param {string} sourceText
 * @param {string} fileLabel  path/name for error messages
 * @returns {Array<{file:string,pattern:string,message:string,line:number,column:number}>}
 *   line/column are 1-based.
 */
function findTxPlanViolations(sourceText, fileLabel) {
  const tsapi = loadTS();
  const sf = tsapi.createSourceFile(
    fileLabel || 'source.ts',
    sourceText,
    tsapi.ScriptTarget.ES2022,
    /* setParentNodes */ true,
  );
  const violations = [];

  (function visitTop(node) {
    if (isDatabaseTransactionCall(node, tsapi)) {
      const fn = node.arguments[0];
      if (fn && (tsapi.isArrowFunction(fn) || tsapi.isFunctionExpression(fn))) {
        const isAsync = (fn.modifiers || []).some((m) => m.kind === tsapi.SyntaxKind.AsyncKeyword);
        if (isAsync) {
          violations.push(Object.assign(
            { file: fileLabel, pattern: 'async-plan-callback',
              message: 'Database.transaction\'s callback must be synchronous — it BUILDS a plan, it ' +
                'does not execute one op at a time. Move any awaited reads to BEFORE the ' +
                'transaction() call; op results inside the callback are Refs, not real values, ' +
                'so there is nothing to await in here.' },
            posOf(sf, tsapi, fn),
          ));
        }
        // Backstop for a literal `await` reaching the callback body even
        // without `async` on the callback itself (e.g. smuggled in through a
        // parser edge case) — belt and suspenders, not the primary detector.
        if (fn.body) {
          (function findAwait(n) {
            if (tsapi.isAwaitExpression(n)) {
              violations.push(Object.assign(
                { file: fileLabel, pattern: 'await-in-plan-callback',
                  message: 'await inside a Database.transaction callback — the callback is ' +
                    'synchronous and builds a plan; there is no promise to await in here.' },
                posOf(sf, tsapi, n),
              ));
            }
            tsapi.forEachChild(n, findAwait);
          })(fn.body);
        }

        scanCallback(sf, tsapi, fn, fileLabel, violations);
      }
    }
    tsapi.forEachChild(node, visitTop);
  })(sf);

  return violations;
}

/**
 * Convenience wrapper: throws TxAnalysisError (message = every violation,
 * one per line, `file:line:col — pattern: message`) when findTxPlanViolations
 * finds anything. Callers that want to collect violations across many files
 * before failing should call findTxPlanViolations directly instead.
 */
function assertNoTxPlanViolations(sourceText, fileLabel) {
  const violations = findTxPlanViolations(sourceText, fileLabel);
  if (violations.length === 0) return;
  throw new TxAnalysisError(violations.map(formatViolation).join('\n'));
}

function formatViolation(v) {
  return `${v.file}:${v.line}:${v.column} — ${v.pattern}: ${v.message}`;
}

module.exports = {
  TxAnalysisError,
  findTxPlanViolations,
  assertNoTxPlanViolations,
  formatViolation,
};
