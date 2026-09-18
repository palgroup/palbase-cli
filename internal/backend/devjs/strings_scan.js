'use strict';
// strings_scan.js — the keys of palbase/strings.json, read from the deploy tree.
//
// Usage: node strings_scan.js <root>
// Prints ONE line: {"keys":[…],"dynamic":[{"file","line"}]}. Exit 0 only when
// every file was read; anything else is the CLI's "the table was not updated"
// (FR-030) — a partial key set must never reach the merge, where a missing key
// is a DELETED key (FR-019). That includes a file that names the package and
// does not parse: the parser recovers from any syntax error with a PARTIAL
// tree, and the calls it dropped would be deleted keys.
//
// A call counts only when `t` is the one @palbase/backend exports, and the
// compiler's own symbol resolution decides that: a local function named `t`, a
// parameter that shadows the import, or a `t` from another package is not a
// string of this table (D-12). Resolution needs no other module (noResolve,
// noLib) — an identifier's declaration is the import that bound it.
//
// Every use of that `t` whose string cannot be read lands in `dynamic` with its
// place (FR-016): a non-literal first argument, and any use that is not a
// direct call — assigned, passed, tagged, `.call`ed, re-exported — because a
// call made through it is out of the scanner's sight.

const fs = require('node:fs');
const path = require('node:path');

const SDK = '@palbase/backend';
const SKIP_DIRS = new Set([
  'node_modules', '.git', '.palbase', 'palbase', 'dist', 'build',
  '.palbase-build-controllers', '.palbase-staged-controllers',
]);
const SOURCE_RE = /\.(ts|tsx|mts|cts)$/;
const DECLARATION_RE = /\.d\.[cm]?ts$/;

class ScanUnavailable extends Error {}

let ts = null;
function loadTS() {
  if (ts) return ts;
  let mod;
  try {
    mod = require('typescript');
  } catch (e) {
    throw new ScanUnavailable('the `typescript` package could not be loaded (' + e.message + ')');
  }
  // TypeScript 7's CommonJS entry carries version metadata only (see
  // throw_analysis.js loadTS) — no compiler to resolve a symbol with.
  if (typeof mod.createProgram !== 'function' || !mod.ScriptTarget) {
    throw new ScanUnavailable('the resolved `typescript` (v' + (mod.version || 'unknown') + ') has no compiler API');
  }
  ts = mod;
  return ts;
}

function sourceFiles(root) {
  const out = [];
  const walk = (dir) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, e.name);
      if (e.isDirectory()) {
        if (!SKIP_DIRS.has(e.name)) walk(full);
      } else if (e.isFile() && SOURCE_RE.test(e.name) && !DECLARATION_RE.test(e.name)) {
        out.push(full);
      }
    }
  };
  walk(root);
  return out.sort();
}

function isSDKSpecifier(spec) {
  return !!spec && ts.isStringLiteral(spec) && spec.text === SDK;
}

// Is `sym` bound by an import of @palbase/backend — `t` itself, or
// (wantNamespace) a namespace import of the package? Answered from the
// declaration the checker resolved to, so shadowing is seen for what it is.
function isSDKBinding(sym, wantNamespace) {
  const decl = sym && sym.declarations && sym.declarations[0];
  if (!decl) return false;
  if (wantNamespace) {
    if (!ts.isNamespaceImport(decl)) return false;
    const imp = decl.parent.parent;
    return !imp.importClause.isTypeOnly && isSDKSpecifier(imp.moduleSpecifier);
  }
  if (!ts.isImportSpecifier(decl) || decl.isTypeOnly) return false;
  const imported = (decl.propertyName || decl.name).text;
  const imp = decl.parent.parent.parent;
  return imported === 't' && !imp.importClause.isTypeOnly && isSDKSpecifier(imp.moduleSpecifier);
}

function importedFromSDK(checker, id, wantNamespace) {
  return isSDKBinding(checker.getSymbolAtLocation(id), wantNamespace);
}

// The expression under parentheses and type-only wrappers — `("x")`,
// `"x" as const`, `"x" satisfies string`, `t!`, `<string>"x"` — none of which
// changes the value at run time.
function unwrap(e) {
  while (ts.isParenthesizedExpression(e) || ts.isAsExpression(e) || ts.isSatisfiesExpression(e) ||
         ts.isNonNullExpression(e) || ts.isTypeAssertionExpression(e)) {
    e = e.expression;
  }
  return e;
}

function isLiteral(e) {
  return ts.isStringLiteral(e) || ts.isNoSubstitutionTemplateLiteral(e);
}

// The member a property or element access names — `B.t` and `B["t"]` → "t";
// null for a key the scanner cannot read (`B[k]`).
function memberName(access) {
  if (ts.isPropertyAccessExpression(access)) return access.name.text;
  const key = unwrap(access.argumentExpression);
  return isLiteral(key) ? key.text : null;
}

// A destructuring that takes `t` off the namespace, or takes it unnamed
// (`...rest`, a computed key).
function bindsT(pattern) {
  return pattern.elements.some((el) => {
    if (el.dotDotDotToken) return true;
    const key = el.propertyName || el.name;
    return ts.isComputedPropertyName(key) || key.text === 't';
  });
}

function scanFile(checker, sf, rel, keys, dynamic) {
  // The local names this file binds to the SDK's `t` and to its namespace. Only
  // an identifier spelled like one of them is handed to the checker.
  const tNames = new Set();
  const nsNames = new Set();
  for (const st of sf.statements) {
    if (!ts.isImportDeclaration(st) || !isSDKSpecifier(st.moduleSpecifier)) continue;
    const clause = st.importClause;
    if (!clause || clause.isTypeOnly || !clause.namedBindings) continue;
    if (ts.isNamespaceImport(clause.namedBindings)) {
      nsNames.add(clause.namedBindings.name.text);
      continue;
    }
    for (const el of clause.namedBindings.elements) {
      if (!el.isTypeOnly && (el.propertyName || el.name).text === 't') tNames.add(el.name.text);
    }
  }

  const warn = (node) => {
    const { line } = sf.getLineAndCharacterOfPosition(node.getStart(sf));
    dynamic.push({ file: rel, line: line + 1 });
  };
  const isSDKT = (e) => ts.isIdentifier(e) && tNames.has(e.text) && importedFromSDK(checker, e, false);
  const isSDKNamespace = (e) => ts.isIdentifier(e) && nsNames.has(e.text) && importedFromSDK(checker, e, true);
  // `B.t` / `B["t"]` on the SDK's namespace import.
  const isNamespaceT = (e) => (ts.isPropertyAccessExpression(e) || ts.isElementAccessExpression(e)) &&
    isSDKNamespace(e.expression) && memberName(e) === 't';

  // The SDK's `t` named anywhere but as a callee — the callee never reaches
  // here. `typeof t` reads a string; the function does not escape.
  const strayT = (id) => {
    if (!tNames.has(id.text) || ts.isTypeOfExpression(id.parent)) return false;
    if (ts.isShorthandPropertyAssignment(id.parent) && id.parent.name === id) {
      // `{ t }`: the name resolves to the property; its value is the import.
      return isSDKBinding(checker.getShorthandAssignmentValueSymbol(id.parent), false);
    }
    return importedFromSDK(checker, id, false);
  };
  // The SDK's namespace escaping whole — `const ns = B`, `f(B)`,
  // `export default B`, `const { t: t2 } = B`, `B[k]` — carries its `t` where
  // no call can be seen. Reading another member (`B.NotFound`,
  // `@B.Controller`) does not; `B.t` itself is judged at the access.
  const strayNamespace = (id) => {
    if (!nsNames.has(id.text) || ts.isTypeOfExpression(id.parent) || !importedFromSDK(checker, id, true)) return false;
    const p = id.parent;
    if ((ts.isPropertyAccessExpression(p) || ts.isElementAccessExpression(p)) && p.expression === id) {
      return memberName(p) === null;
    }
    if (ts.isVariableDeclaration(p) && p.initializer === id && ts.isObjectBindingPattern(p.name)) {
      return bindsT(p.name);
    }
    return true;
  };
  // `export { t } from "@palbase/backend"`, `export * [as X] from …`, or an
  // `export { t }` / `export { B }` of this file's own binding.
  const reexportsT = (decl) => {
    if (decl.isTypeOnly) return false;
    const clause = decl.exportClause;
    if (decl.moduleSpecifier) {
      if (!isSDKSpecifier(decl.moduleSpecifier)) return false;
      if (!clause || ts.isNamespaceExport(clause)) return true;
      return clause.elements.some((el) => !el.isTypeOnly && (el.propertyName || el.name).text === 't');
    }
    return !!clause && ts.isNamedExports(clause) && clause.elements.some((el) => {
      if (el.isTypeOnly) return false;
      const local = checker.getExportSpecifierLocalTargetSymbol(el);
      return isSDKBinding(local, false) || isSDKBinding(local, true);
    });
  };

  const visit = (node) => {
    // An import binds and a type describes; neither uses `t` at run time. An
    // instantiation expression (`t<string>`) is a type node that still holds a
    // value, so it is walked.
    if (ts.isImportDeclaration(node)) return;
    if (ts.isTypeNode(node) && !ts.isExpressionWithTypeArguments(node)) return;
    if (ts.isExportDeclaration(node)) {
      if (reexportsT(node)) warn(node);
      return;
    }
    if (ts.isCallExpression(node)) {
      const callee = unwrap(node.expression);
      if (isSDKT(callee) || isNamespaceT(callee)) {
        const arg = node.arguments[0] && unwrap(node.arguments[0]);
        if (arg && isLiteral(arg)) keys.add(arg.text);
        else warn(node);
        // The arguments may hold calls of their own; the callee is not a stray use.
        node.arguments.forEach(visit);
        return;
      }
    }
    if (isNamespaceT(node)) {
      if (!ts.isTypeOfExpression(node.parent)) warn(node);
      return;
    }
    if (ts.isIdentifier(node)) {
      if (strayT(node) || strayNamespace(node)) warn(node);
      return;
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

function scan(root) {
  loadTS();
  const files = sourceFiles(root);
  const program = ts.createProgram(files, {
    noResolve: true, noLib: true, types: [], allowJs: false, jsx: ts.JsxEmit.Preserve,
    target: ts.ScriptTarget.Latest, experimentalDecorators: true,
  });
  const checker = program.getTypeChecker();
  const keys = new Set();
  const dynamic = [];
  for (const file of files) {
    const sf = program.getSourceFile(file);
    if (!sf) throw new Error('the compiler did not read ' + file);
    if (!sf.text.includes(SDK)) continue;
    const rel = path.relative(root, file).split(path.sep).join('/');
    // After the filter: a broken file that never names the package has no key
    // to lose, and must not hold the table hostage.
    const broken = program.getSyntacticDiagnostics(sf);
    if (broken.length) {
      const d = broken[0];
      const where = d.start === undefined ? '' : ':' + (sf.getLineAndCharacterOfPosition(d.start).line + 1);
      throw new Error(rel + where + ' does not parse (' + ts.flattenDiagnosticMessageText(d.messageText, ' ') +
        ') — its t() strings cannot be read');
    }
    try {
      scanFile(checker, sf, rel, keys, dynamic);
    } catch (e) {
      // Whatever broke the walk — a 30 000-term expression overflows the
      // stack — the build's one-line warning has to name the file.
      throw new Error(rel + ': ' + (e && e.message ? e.message : String(e)));
    }
  }
  return { keys: [...keys].sort(), dynamic };
}

module.exports = { scan, ScanUnavailable };

if (require.main === module) {
  const root = process.argv[2];
  if (!root) {
    process.stderr.write('usage: node strings_scan.js <root>\n');
    process.exit(2);
  }
  let result;
  try {
    result = scan(path.resolve(root));
  } catch (e) {
    process.stderr.write((e && e.message ? e.message : String(e)) + '\n');
    process.exit(e instanceof ScanUnavailable ? 3 : 1);
  }
  process.stdout.write(JSON.stringify(result) + '\n', () => process.exit(0));
}
