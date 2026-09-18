'use strict';
// strings_scan.js — the keys of palbase/strings.json, read from the deploy tree.
//
// Usage: node strings_scan.js <root>
// Prints ONE line: {"keys":[…],"dynamic":[{"file","line"}]}. Exit 0 only when
// every file was read; anything else is the CLI's "the table was not updated"
// (FR-030) — a partial key set must never reach the merge, where a missing key
// is a DELETED key (FR-019).
//
// A call counts only when `t` is the one @palbase/backend exports, and the
// compiler's own symbol resolution decides that: a local function named `t`, a
// parameter that shadows the import, or a `t` from another package is not a
// string of this table (D-12). Resolution needs no other module (noResolve,
// noLib) — an identifier's declaration is the import that bound it.

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

// Is `id` bound by an import of @palbase/backend — `t` itself, or (wantNamespace)
// a namespace import of the package? Answered from the declaration the checker
// resolves the identifier to, so shadowing is seen for what it is.
function importedFromSDK(checker, id, wantNamespace) {
  const sym = checker.getSymbolAtLocation(id);
  const decl = sym && sym.declarations && sym.declarations[0];
  if (!decl) return false;
  if (wantNamespace) {
    if (!ts.isNamespaceImport(decl)) return false;
    const imp = decl.parent.parent;
    return !imp.importClause.isTypeOnly && ts.isStringLiteral(imp.moduleSpecifier) && imp.moduleSpecifier.text === SDK;
  }
  if (!ts.isImportSpecifier(decl) || decl.isTypeOnly) return false;
  const imported = (decl.propertyName || decl.name).text;
  const imp = decl.parent.parent.parent;
  return imported === 't' && !imp.importClause.isTypeOnly &&
    ts.isStringLiteral(imp.moduleSpecifier) && imp.moduleSpecifier.text === SDK;
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
    const isT = (callee) => {
      if (ts.isIdentifier(callee)) return importedFromSDK(checker, callee, false);
      if (ts.isPropertyAccessExpression(callee) && ts.isIdentifier(callee.expression) &&
          ts.isIdentifier(callee.name) && callee.name.text === 't') {
        return importedFromSDK(checker, callee.expression, true);
      }
      return false;
    };
    const visit = (node) => {
      if (ts.isCallExpression(node) && isT(node.expression)) {
        const arg = node.arguments[0];
        if (arg && (ts.isStringLiteral(arg) || ts.isNoSubstitutionTemplateLiteral(arg))) {
          keys.add(arg.text);
        } else {
          const { line } = sf.getLineAndCharacterOfPosition(node.getStart(sf));
          dynamic.push({ file: path.relative(root, file).split(path.sep).join('/'), line: line + 1 });
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
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
