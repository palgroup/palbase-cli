'use strict';

/**
 * Palbase Backend Runtime — Per-Endpoint Throw Inference (typed backend errors)
 *
 * For each controller route, collects every error the route can throw —
 * directly or anywhere down its call graph — and appends one `recordThrows(...)`
 * IIFE per route, so the route registry (RouteMeta.throws) and the spec
 * (`x-palbase-errors`) carry the set. Every generated client (iOS, Android,
 * web) turns that set into the endpoint's typed error cases: an error this
 * analysis misses is an error the app can only ever receive as `.other`.
 *
 * It runs on the project's TypeScript PROGRAM and TYPE CHECKER, because
 * following a call is name resolution and name resolution is the compiler's
 * job — imports through barrels and `export *`, aliases, default and namespace
 * imports, `.js` specifiers, tsconfig `paths`, inheritance, locals. The
 * parser-only analysis this replaces re-implemented a slice of that by hand and
 * silently lost everything outside the slice. Measured 2026-09-26 on a real
 * project (93 endpoints, 184 declared errors): 21 errors reached no endpoint at
 * all and one was missing from 89 endpoints — and `palbase push` lost a further
 * 167 because its driver never handed the analysis the module graph. The graph
 * is therefore read HERE, from the project root, and no caller can omit it.
 *
 * What is followed from a route:
 *   - every call whose target is a project function, method, getter or
 *     constructor. `this.m()` dispatches on the RUNTIME class — an override
 *     wins — and `super.m()` on the parent;
 *   - the receiver's runtime class is found from how the value was made:
 *       `new C()` → C · a local or a field → its initializer · a call → what the
 *       callee returns · an injected dependency → the container's own rule (a
 *       provider is itself; a class no module owns — abstract OR concrete — is
 *       the ONE provider that extends it) · anything else → every project class
 *       the declared type admits. That last one over-approximates on purpose:
 *       an extra typed case costs a client nothing, a missing one costs it the
 *       error;
 *   - a project function or method passed as a value (a callback): whoever
 *     receives it may call it;
 *   - the error handed to a data-layer expectation — `.expectOne(err)`,
 *     `.expectNone(err)`, `.expectAtLeast(n, err)`, `.expectAtMost(n, err)`:
 *     the SDK throws exactly that object when the expectation fails
 *     (db/tx-plan.ts);
 *   - an SDK API that throws on the author's behalf (SDK_API_ERRORS).
 *
 * What is a typed error:
 *   - an SDK error class (the code table below, or the code it is handed);
 *   - a raw `HttpError` / `PalError` — named after its code, since the class
 *     name says nothing;
 *   - a `defineError("<code>", <status>, schema?)` class;
 *   - a project class extending any of the above.
 * A code or status is read from its literal, or from its TYPE where it is read:
 * `code: "a" | "b"` is two typed errors.
 * The thrown value is followed through locals, conditionals, fields and
 * factories (a call whose callee RETURNS the error). A plain `Error` is not a
 * typed error: the runtime answers it as an internal error.
 *
 * LOUD, NEVER SILENT. A defineError whose code or status is not known at build
 * time, a code thrown with a status other than the one it carries, two codes
 * that one endpoint would give the same case name, and any failure of the
 * analysis itself stop the build and name the site. A throw
 * whose code is decided at RUNTIME — a proxy passing on an upstream refusal —
 * cannot be a typed case for anyone; it is left out of the contract and the
 * build prints a warning naming the site (warnings()). Nothing is dropped
 * quietly.
 *
 * SHARED VERBATIM by the dev stager (stage.js), `palbase build` and `palbase
 * push` — the CLI embeds copies, and a parity check diffs them.
 */

const fs = require('fs');
const path = require('path');

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
  // from its CommonJS entry — no compiler API at all. Twin of return_types.js.
  if (typeof mod.createProgram !== 'function' || !mod.ScriptTarget) {
    throw new Error(TS_PARSER_HELP(
      'the resolved `typescript` (v' + (mod.version || 'unknown') + ') has no compiler API — ' +
        'TypeScript 7 ships the Go-native compiler, whose CommonJS build exposes version metadata only',
    ));
  }
  // The type checker's public assignability query is what types a value whose
  // origin cannot be followed; it is public API from TypeScript 5.4 on.
  if (typeof mod.getDecorators !== 'function' ||
      typeof mod.createProgram([], {}).getTypeChecker().isTypeAssignableTo !== 'function') {
    throw new Error(TS_PARSER_HELP('the resolved `typescript` (v' + (mod.version || 'unknown') + ') is older than 5.4'));
  }
  ts = mod;
  return ts;
}

// One actionable message for every compiler-load failure (twin of the
// return_types.js copy). The CLI normally puts its OWN pinned TypeScript 5 ahead
// of the project on NODE_PATH; reaching this means that provisioning was skipped
// (offline first run) and the project's typescript is unusable.
function TS_PARSER_HELP(reason) {
  return (
    'palbase needs the TypeScript 5 compiler API to analyze controller throw sites, but ' +
    reason +
    '.\n  fix: npm install --save-dev typescript@5   (or re-run once online: the CLI installs its own pinned compiler)'
  );
}

class ThrowAnalysisError extends Error {}

const SDK_SPECIFIER = '@palbase/backend';

// The SDK's error classes (mirrors backend/src/errors.ts): default code, HTTP
// status, and which constructor argument overrides the code (null: none).
const SDK_ERRORS = {
  BadRequest: { code: 'bad_request', status: 400, codeArg: null },
  Unauthorized: { code: 'unauthorized', status: 401, codeArg: 1 },
  Forbidden: { code: 'forbidden', status: 403, codeArg: 1 },
  NotFound: { code: 'not_found', status: 404, codeArg: 1 },
  Conflict: { code: 'conflict', status: 409, codeArg: 1 },
  UniqueViolation: { code: 'unique_violation', status: 409, codeArg: 2 },
  SerializationFailure: { code: 'serialization_failure', status: 409, codeArg: null },
  DeadlockDetected: { code: 'deadlock_detected', status: 409, codeArg: null },
  TooManyRequests: { code: 'too_many_requests', status: 429, codeArg: null },
};

// Raw error classes — ctor (status, code, message, data?).
const SDK_RAW_ERRORS = new Set(['HttpError', 'PalError']);

// Every method decorator that registers a route (decorators/methods.ts,
// upload.ts, sse.ts).
const ROUTE_DECORATORS = new Set(['Get', 'Post', 'Put', 'Patch', 'Delete', 'Query', 'Upload', 'Sse']);

// Errors the SDK throws ON THE AUTHOR'S BEHALF from inside an API a route calls:
// the route's contract, though no `throw` of the author's names them. Matched
// on the SDK declaration the compiler resolves the call to (the member name,
// and the interface that declares it where the name alone is too common).
// Mirrors the SDK's sources; __tests__/sdk-raised-errors.test.ts fails the
// moment an SDK API starts throwing something this table does not list.
const SDK_API_ERRORS = [
  // A cursor or page window the client sent that does not decode (engine/cursor.ts).
  { member: 'page', container: null, error: { name: 'BadRequest', code: 'bad_request', status: 400 } },
  { member: '$history', container: null, error: { name: 'BadRequest', code: 'bad_request', status: 400 } },
  // A repository update of a row the tenant does not have (db/repository.ts).
  { member: 'update', container: /^RepositoryOf(\$\d+)?$/, error: { name: 'NotFound', code: 'not_found', status: 404 } },
];

// Data-layer expectations, and which argument is the error they throw.
const EXPECTATIONS = new Map([['expectOne', 0], ['expectNone', 0], ['expectAtLeast', 1], ['expectAtMost', 1]]);

// What the stagers never copy: dependencies, build output, and the build output
// of an iOS app that lives beside the backend.
const SKIP_DIRS = new Set(['node_modules', 'dist', 'build', '.git', 'DerivedData', '.build', 'Pods', 'xcuserdata']);

// ---------------------------------------------------------------------------
// The project
// ---------------------------------------------------------------------------

/**
 * createThrowAnalysis builds ONE program over the project — every controller and
 * every module file, and everything they import — and answers per controller
 * file. Build it once per stage run and hand it every controller.
 *
 * @param {string} projectRoot the project's root (where its tsconfig and its
 *                             modules live)
 * @returns {{ analyze(controllerPath: string): Array<{className: string,
 *            ops: Record<string, Array<{name: string, code: string, status: number}>>}>,
 *            warnings(): string[] }}
 */
function createThrowAnalysis(projectRoot) {
  const tsapi = loadTS();
  const root = path.resolve(projectRoot);
  const files = projectFiles(root);
  const program = tsapi.createProgram([...files.controllers, ...files.modules], compilerOptions(tsapi, root));
  const p = {
    root,
    program,
    checker: program.getTypeChecker(),
    ids: new WeakMap(),
    nextId: 1,
    providerSet: null,
    concrete: null,
    summaries: new Map(),
    warnings: new Map(),
    statusOf: null,
    round: 0,
    cyclic: false,
    grew: false,
  };
  return {
    analyze(controllerPath) {
      // A recursive call reads its own summary before it is complete, so a
      // round that met recursion runs again, seeded with what it found, until
      // no summary grows. Without recursion one round is exact.
      let result;
      for (;;) {
        p.round += 1;
        p.cyclic = false;
        p.grew = false;
        result = analyzeFile(p, controllerPath);
        if (!p.cyclic || !p.grew) break;
      }
      for (const entry of p.summaries.values()) entry.final = true;
      return result;
    },
    // Throws the build could not type, one line each, for the driver to print.
    warnings() {
      return [...p.warnings.values()];
    },
  };
}

// projectFiles lists the controller and module files under the root, skipping
// what the stagers skip (dependencies, build output, symlinks).
function projectFiles(root) {
  const out = { controllers: [], modules: [] };
  const visit = (dir) => {
    let entries;
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      if (SKIP_DIRS.has(entry.name) || entry.name.startsWith('.palbase') || entry.isSymbolicLink()) continue;
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) visit(full);
      else if (/\.controller\.(c?ts|tsx)$/i.test(entry.name)) out.controllers.push(full);
      else if (/\.module\.(c?ts|tsx)$/i.test(entry.name)) out.modules.push(full);
    }
  };
  visit(root);
  return out;
}

// compilerOptions reads the project's own tsconfig so imports resolve exactly as
// the project's editor and bundler resolve them. No tsconfig: the scaffold's
// resolution (Bundler), which is also what Bun does.
function compilerOptions(tsapi, root) {
  const configFile = path.join(root, 'tsconfig.json');
  let options = {
    target: tsapi.ScriptTarget.ES2022,
    module: tsapi.ModuleKind.ESNext,
    moduleResolution: tsapi.ModuleResolutionKind.Bundler,
    experimentalDecorators: true,
    allowImportingTsExtensions: true,
  };
  if (fs.existsSync(configFile)) {
    const read = tsapi.readConfigFile(configFile, tsapi.sys.readFile);
    if (read.error) {
      throw new ThrowAnalysisError(`${configFile}: ${tsapi.flattenDiagnosticMessageText(read.error.messageText, '\n')}`);
    }
    options = tsapi.parseJsonConfigFileContent(read.config, tsapi.sys, root).options;
  }
  return { ...options, noEmit: true, skipLibCheck: true };
}

function isProjectFile(p, fileName) {
  const f = path.resolve(fileName);
  return (
    !f.includes(`${path.sep}node_modules${path.sep}`) &&
    !/\.d\.[cm]?ts$/.test(f) &&
    (f === p.root || f.startsWith(p.root + path.sep))
  );
}

function isProject(p, node) {
  return Boolean(node) && isProjectFile(p, node.getSourceFile().fileName);
}

function idOf(p, node) {
  let id = p.ids.get(node);
  if (!id) {
    id = p.nextId++;
    p.ids.set(node, id);
  }
  return id;
}

function siteOf(p, node) {
  const sf = node.getSourceFile();
  const { line } = sf.getLineAndCharacterOfPosition(node.getStart(sf));
  return `${path.relative(p.root, sf.fileName)}:${line + 1}`;
}

function refuse(p, node, message) {
  throw new ThrowAnalysisError(`${siteOf(p, node)} — ${message}`);
}

// ---------------------------------------------------------------------------
// Symbols
// ---------------------------------------------------------------------------

// unwrap sees through what does not change a value: parentheses, `as`, `!`,
// `satisfies`, `<T>x`, and `await` (an awaited call is still that call's value).
function unwrap(e) {
  const tsapi = loadTS();
  while (e) {
    if (
      tsapi.isParenthesizedExpression(e) ||
      tsapi.isAsExpression(e) ||
      tsapi.isNonNullExpression(e) ||
      tsapi.isAwaitExpression(e) ||
      tsapi.isTypeAssertionExpression(e) ||
      (tsapi.isSatisfiesExpression && tsapi.isSatisfiesExpression(e))
    ) {
      e = e.expression;
      continue;
    }
    return e;
  }
  return e;
}

function resolveAlias(p, sym) {
  const tsapi = loadTS();
  if (sym && sym.flags & tsapi.SymbolFlags.Alias) {
    const target = p.checker.getAliasedSymbol(sym);
    if (target && target.declarations) return target;
  }
  return sym;
}

// declOf is the declaration a name refers to, through every import and alias.
function declOf(p, node) {
  const sym = resolveAlias(p, p.checker.getSymbolAtLocation(node));
  if (!sym) return null;
  return sym.valueDeclaration || (sym.declarations && sym.declarations[0]) || null;
}

function moduleSpecifierOf(decl) {
  const tsapi = loadTS();
  let n = decl;
  while (n && !tsapi.isImportDeclaration(n) && !tsapi.isExportDeclaration(n)) n = n.parent;
  return n && n.moduleSpecifier && tsapi.isStringLiteral(n.moduleSpecifier) ? n.moduleSpecifier.text : null;
}

function isSdkFile(fileName) {
  return path.resolve(fileName).split(path.sep).join('/').includes('/node_modules/@palbase/backend/');
}

// sdkExport answers the `@palbase/backend` export an expression names — `Get`,
// `NotFound`, `defineError`, `pb.NotFound` through a namespace import — or null.
// Decided from the import chain, so it holds whether or not the SDK's own type
// declarations are installed where the analysis runs.
function sdkExport(p, expr) {
  const tsapi = loadTS();
  expr = unwrap(expr);
  if (!expr) return null;
  if (tsapi.isPropertyAccessExpression(expr)) {
    const ns = p.checker.getSymbolAtLocation(expr.expression);
    const viaNamespace = ns && (ns.declarations || []).some(
      (d) => tsapi.isNamespaceImport(d) && moduleSpecifierOf(d) === SDK_SPECIFIER,
    );
    return viaNamespace ? expr.name.text : null;
  }
  if (!tsapi.isIdentifier(expr)) return null;
  let sym = p.checker.getSymbolAtLocation(expr);
  const seen = new Set();
  while (sym && !seen.has(sym)) {
    seen.add(sym);
    for (const d of sym.declarations || []) {
      if ((tsapi.isImportSpecifier(d) || tsapi.isExportSpecifier(d)) && moduleSpecifierOf(d) === SDK_SPECIFIER) {
        return (d.propertyName || d.name).text;
      }
      if (isSdkFile(d.getSourceFile().fileName)) return sym.name;
    }
    if (!(sym.flags & tsapi.SymbolFlags.Alias)) return null;
    sym = p.checker.getImmediateAliasedSymbol(sym);
  }
  return null;
}

function decoratorName(p, deco) {
  const tsapi = loadTS();
  const e = deco.expression;
  return sdkExport(p, tsapi.isCallExpression(e) ? e.expression : e);
}

function memberNameOf(name) {
  const tsapi = loadTS();
  if (!name) return null;
  if (tsapi.isIdentifier(name) || tsapi.isPrivateIdentifier(name) || tsapi.isStringLiteral(name) || tsapi.isNumericLiteral(name)) {
    return name.text;
  }
  return null;
}

function isStaticMember(m) {
  const tsapi = loadTS();
  return (tsapi.getCombinedModifierFlags(m) & tsapi.ModifierFlags.Static) !== 0;
}

function isAbstractClass(cls) {
  const tsapi = loadTS();
  return (tsapi.getCombinedModifierFlags(cls) & tsapi.ModifierFlags.Abstract) !== 0;
}

function isClassLike(n) {
  const tsapi = loadTS();
  return tsapi.isClassDeclaration(n) || tsapi.isClassExpression(n);
}

// classOfDecl: the class a declaration IS — a class declaration anywhere (top
// level, inside a function or a method), or a class expression held by a
// const, a field (`static Inner = class {…}`) or an object property.
function classOfDecl(d) {
  const tsapi = loadTS();
  if (!d) return null;
  if (isClassLike(d)) return d;
  if ((tsapi.isVariableDeclaration(d) || tsapi.isPropertyDeclaration(d) || tsapi.isPropertyAssignment(d)) && d.initializer) {
    const init = unwrap(d.initializer);
    if (tsapi.isClassExpression(init)) return init;
  }
  return null;
}

// classNameOf: a class's name — its own, or, for a class expression, the name of
// what holds it.
function classNameOf(cls) {
  const tsapi = loadTS();
  if (cls.name) return cls.name.text;
  const holder = cls.parent;
  if (holder && (tsapi.isVariableDeclaration(holder) || tsapi.isPropertyDeclaration(holder) ||
      tsapi.isPropertyAssignment(holder)) && holder.name && memberNameOf(holder.name)) {
    return memberNameOf(holder.name);
  }
  return undefined;
}

// classOfValue: the PROJECT class an expression names as a value (`C` in
// `new C()`, `C.make()`, `new Outer.Inner()`), or null.
function classOfValue(p, expr) {
  const tsapi = loadTS();
  const e = unwrap(expr);
  if (!e) return null;
  const c = classOfDecl(declOf(p, tsapi.isPropertyAccessExpression(e) ? e.name : e));
  return c && isProject(p, c) ? c : null;
}

function superClassOf(p, cls) {
  const tsapi = loadTS();
  const clause = (cls.heritageClauses || []).find((h) => h.token === tsapi.SyntaxKind.ExtendsKeyword);
  const e = clause && clause.types[0] && clause.types[0].expression;
  return e ? classOfValue(p, e) : null;
}

function extendsClass(p, cls, base) {
  for (let c = superClassOf(p, cls); c; c = superClassOf(p, c)) if (c === base) return true;
  return false;
}

// lookupMember finds the implementation `name` resolves to on an instance (or,
// with isStatic, the class) of `cls`: its own member first, then up the chain.
// A method, a getter, or a field holding a function all count.
function lookupMember(p, cls, name, isStatic) {
  const tsapi = loadTS();
  for (let c = cls; c; c = superClassOf(p, c)) {
    for (const m of c.members) {
      if (memberNameOf(m.name) !== name || isStaticMember(m) !== isStatic) continue;
      if ((tsapi.isMethodDeclaration(m) || tsapi.isGetAccessorDeclaration(m)) && m.body) return { fn: m, owner: c };
      if (tsapi.isPropertyDeclaration(m) && m.initializer) {
        const init = unwrap(m.initializer);
        if (tsapi.isArrowFunction(init) || tsapi.isFunctionExpression(init)) return { fn: init, owner: c };
      }
    }
  }
  return null;
}

// providers: every class a module's `@Module({ providers: [...] })` lists — the
// graph the container builds.
function providers(p) {
  const tsapi = loadTS();
  if (p.providerSet) return p.providerSet;
  const set = new Set();
  for (const sf of p.program.getSourceFiles()) {
    if (!isProjectFile(p, sf.fileName)) continue;
    for (const stmt of sf.statements) {
      if (!tsapi.isClassDeclaration(stmt)) continue;
      for (const deco of tsapi.getDecorators(stmt) || []) {
        const call = deco.expression;
        if (!tsapi.isCallExpression(call) || sdkExport(p, call.expression) !== 'Module') continue;
        const def = unwrap(call.arguments[0]);
        if (!def || !tsapi.isObjectLiteralExpression(def)) continue;
        for (const prop of def.properties) {
          if (!tsapi.isPropertyAssignment(prop) || memberNameOf(prop.name) !== 'providers') continue;
          const list = unwrap(prop.initializer);
          if (!tsapi.isArrayLiteralExpression(list)) continue;
          for (const el of list.elements) {
            const c = classOfValue(p, el);
            if (c) set.add(c);
          }
        }
      }
    }
  }
  p.providerSet = set;
  return set;
}

// concreteClasses: every non-abstract class the program's project files declare
// — at any depth, and class expressions too.
function concreteClasses(p) {
  const tsapi = loadTS();
  if (p.concrete) return p.concrete;
  const out = [];
  const visitNode = (n) => {
    if (isClassLike(n) && !isAbstractClass(n)) out.push(n);
    tsapi.forEachChild(n, visitNode);
  };
  for (const sf of p.program.getSourceFiles()) {
    if (isProjectFile(p, sf.fileName)) visitNode(sf);
  }
  p.concrete = out;
  return out;
}

// instanceTypeOf: the type an instance of the class has.
function instanceTypeOf(p, cls) {
  const sym = cls.name ? p.checker.getSymbolAtLocation(cls.name) : cls.symbol;
  return sym ? p.checker.getDeclaredTypeOfSymbol(sym) : null;
}

function projectClassOfType(p, type) {
  const sym = type && (type.getSymbol() || type.aliasSymbol);
  const d = sym && (sym.declarations || []).find(isClassLike);
  return d && isProject(p, d) ? d : null;
}

function declaredTypeOf(p, decl) {
  return decl.type ? p.checker.getTypeFromTypeNode(decl.type) : p.checker.getTypeAtLocation(decl.name || decl);
}

// classesOfType: every project class a value of `type` can be, when nothing but
// the type is known. A class type admits the class and its subclasses; a project
// interface or object type admits every concrete class assignable to it.
function classesOfType(p, type, out) {
  const tsapi = loadTS();
  if (!type) return;
  if (type.isUnion()) {
    for (const t of type.types) classesOfType(p, t, out);
    return;
  }
  if (type.flags & (tsapi.TypeFlags.Any | tsapi.TypeFlags.Unknown | tsapi.TypeFlags.Primitive)) return;
  const sym = type.getSymbol();
  if (!sym) return;
  const cls = (sym.declarations || []).find(isClassLike);
  if (cls) {
    if (!isProject(p, cls)) return; // a library class — Map, Date, …
    if (!isAbstractClass(cls)) out.add(cls);
    for (const c of concreteClasses(p)) if (extendsClass(p, c, cls)) out.add(c);
    return;
  }
  const declaredInProject = (sym.declarations || []).some((d) => isProject(p, d));
  if (!declaredInProject) return;
  for (const c of concreteClasses(p)) {
    const inst = instanceTypeOf(p, c);
    if (inst && p.checker.isTypeAssignableTo(inst, type)) out.add(c);
  }
}

// injectedClasses applies the container's rule to a constructor-injected type:
// a provider is what it names; a class no module owns — abstract or concrete —
// is the ONE provider that extends it (container.ts, implementorsOf). When the
// module graph does not settle it, every class the type admits.
function injectedClasses(p, type, out) {
  const cls = projectClassOfType(p, type);
  if (cls) {
    const owned = providers(p);
    if (owned.has(cls)) {
      out.add(cls);
      return;
    }
    const impls = [...owned].filter((c) => c !== cls && extendsClass(p, c, cls));
    if (impls.length === 1) {
      out.add(impls[0]);
      return;
    }
  }
  classesOfType(p, type, out);
}

function isInside(node, container) {
  for (let n = node; n; n = n.parent) if (n === container) return true;
  return false;
}

// A local's initializer runs in the function that declares it; anything else
// (a module-level singleton) runs with no receiver.
function contextOf(decl, ctx) {
  return ctx.fn && isInside(decl, ctx.fn) ? ctx : { receiver: null, owner: null, fn: null };
}

// ---------------------------------------------------------------------------
// Values
// ---------------------------------------------------------------------------

// valueClasses answers which project classes the value of `expr` can be an
// instance of.
function valueClasses(p, expr, ctx, seen = new Set()) {
  const tsapi = loadTS();
  const out = new Set();
  expr = unwrap(expr);
  if (!expr) return out;
  const key = `${idOf(p, expr)}@${ctx.receiver ? idOf(p, ctx.receiver) : 0}`;
  if (seen.has(key)) return out;
  seen.add(key);
  const merge = (set) => {
    for (const c of set) out.add(c);
  };

  if (tsapi.isNewExpression(expr)) {
    const c = classOfValue(p, expr.expression);
    if (c) out.add(c);
    return out;
  }
  if (expr.kind === tsapi.SyntaxKind.ThisKeyword) {
    if (ctx.receiver) out.add(ctx.receiver);
    return out;
  }
  if (tsapi.isConditionalExpression(expr)) {
    merge(valueClasses(p, expr.whenTrue, ctx, seen));
    merge(valueClasses(p, expr.whenFalse, ctx, seen));
    return out;
  }
  if (tsapi.isBinaryExpression(expr)) {
    const op = expr.operatorToken.kind;
    const K = tsapi.SyntaxKind;
    if (op === K.QuestionQuestionToken || op === K.BarBarToken || op === K.AmpersandAmpersandToken) {
      merge(valueClasses(p, expr.left, ctx, seen));
      merge(valueClasses(p, expr.right, ctx, seen));
      return out;
    }
    if (op === K.CommaToken || op === K.EqualsToken) {
      merge(valueClasses(p, expr.right, ctx, seen));
      return out;
    }
  }
  if (tsapi.isCallExpression(expr)) {
    for (const t of callTargets(p, expr, ctx)) {
      for (const r of returnedExpressions(t.fn)) merge(valueClasses(p, r, t.ctx, seen));
    }
    if (out.size > 0) return out;
  } else if (tsapi.isIdentifier(expr) || tsapi.isPropertyAccessExpression(expr)) {
    const d = declOf(p, tsapi.isPropertyAccessExpression(expr) ? expr.name : expr);
    if (d && tsapi.isVariableDeclaration(d)) {
      if (d.initializer) merge(valueClasses(p, d.initializer, contextOf(d, ctx), seen));
      const isConst = (tsapi.getCombinedNodeFlags(d) & tsapi.NodeFlags.Const) !== 0;
      if (!isConst || !d.initializer) classesOfType(p, declaredTypeOf(p, d), out);
      return out;
    }
    if (d && tsapi.isParameter(d)) {
      const t = declaredTypeOf(p, d);
      if (d.parent && tsapi.isConstructorDeclaration(d.parent)) injectedClasses(p, t, out);
      else classesOfType(p, t, out);
      return out;
    }
    if (d && tsapi.isPropertyDeclaration(d)) {
      // A field with an initializer is that value; one without is assigned in the
      // constructor from what the container injects.
      if (d.initializer) merge(valueClasses(p, d.initializer, { receiver: d.parent, owner: d.parent, fn: null }, seen));
      else injectedClasses(p, declaredTypeOf(p, d), out);
      return out;
    }
    if (d && tsapi.isGetAccessorDeclaration(d) && d.body) {
      for (const r of returnedExpressions(d)) merge(valueClasses(p, r, { receiver: d.parent, owner: d.parent, fn: d }, seen));
      if (out.size > 0) return out;
    }
    if (d && tsapi.isPropertyAssignment(d)) {
      merge(valueClasses(p, d.initializer, { receiver: null, owner: null, fn: null }, seen));
      if (out.size > 0) return out;
    }
  }
  classesOfType(p, p.checker.getTypeAtLocation(expr), out);
  return out;
}

// returnedExpressions: what a function hands back — a concise arrow body, or its
// own `return` statements (not those of functions nested inside it).
function returnedExpressions(fn) {
  const tsapi = loadTS();
  if (tsapi.isArrowFunction(fn) && !tsapi.isBlock(fn.body)) return [fn.body];
  if (!fn.body) return [];
  const out = [];
  const visit = (n) => {
    if (tsapi.isReturnStatement(n)) {
      if (n.expression) out.push(n.expression);
      return;
    }
    if (tsapi.isFunctionLike(n) || isClassLike(n)) return;
    tsapi.forEachChild(n, visit);
  };
  tsapi.forEachChild(fn.body, visit);
  return out;
}

// ---------------------------------------------------------------------------
// Calls
// ---------------------------------------------------------------------------

// sdkApiErrors: the errors an SDK API throws on the author's behalf, when the
// call resolves to one (SDK_API_ERRORS).
function sdkApiErrors(p, callee) {
  const tsapi = loadTS();
  const name = memberAccessName(callee);
  if (name === null) return [];
  const sym = p.checker.getSymbolAtLocation(tsapi.isPropertyAccessExpression(callee) ? callee.name : callee.argumentExpression);
  const out = new Map();
  for (const d of (sym && sym.declarations) || []) {
    if (!isSdkFile(d.getSourceFile().fileName)) continue;
    const container = d.parent && d.parent.name && tsapi.isIdentifier(d.parent.name) ? d.parent.name.text : '';
    for (const api of SDK_API_ERRORS) {
      if (api.member === name && (!api.container || api.container.test(container))) out.set(api.error.code, api.error);
    }
  }
  return [...out.values()];
}

function memberAccessName(callee) {
  const tsapi = loadTS();
  if (tsapi.isPropertyAccessExpression(callee)) return callee.name.text;
  if (tsapi.isElementAccessExpression(callee) && tsapi.isStringLiteralLike(callee.argumentExpression)) {
    return callee.argumentExpression.text;
  }
  return null;
}

// memberIsProjectCode: does the checker's static view of `obj.name` land in
// project code (a class member or an interface member the project declares)?
// Library and SDK members (`arr.map`, `str.trim`, `Database.$atomic`) are not
// followed — their bodies are not the project's.
function memberIsProjectCode(p, callee) {
  const tsapi = loadTS();
  const nameNode = tsapi.isPropertyAccessExpression(callee) ? callee.name : callee.argumentExpression;
  const sym = p.checker.getSymbolAtLocation(nameNode);
  if (!sym) return true; // an untyped receiver: follow what the value is
  return (sym.declarations || []).some((d) => isProject(p, d));
}

// constructionTargets: what `new cls(...)` runs — each class's own field
// initializers and constructor, up to the first explicit constructor (whose
// `super(...)` call walks the rest).
function constructionTargets(p, cls, receiver) {
  const tsapi = loadTS();
  const out = [];
  for (let c = cls; c; c = superClassOf(p, c)) {
    for (const m of c.members) {
      if (!tsapi.isPropertyDeclaration(m) || !m.initializer || isStaticMember(m)) continue;
      // A field that HOLDS a function runs when called, not when constructed.
      const init = unwrap(m.initializer);
      if (tsapi.isArrowFunction(init) || tsapi.isFunctionExpression(init)) continue;
      out.push({ fn: m.initializer, ctx: { receiver, owner: c, fn: null } });
    }
    const ctor = c.members.find((m) => tsapi.isConstructorDeclaration(m) && m.body);
    if (ctor) {
      out.push({ fn: ctor, ctx: { receiver, owner: c, fn: ctor } });
      break;
    }
  }
  return out;
}

// callTargets: every project body a call can run, each with the receiver it
// runs on.
function callTargets(p, call, ctx) {
  const tsapi = loadTS();
  const callee = unwrap(call.expression);
  if (callee.kind === tsapi.SyntaxKind.SuperKeyword) {
    const base = ctx.owner && superClassOf(p, ctx.owner);
    return base ? constructionTargets(p, base, ctx.receiver) : [];
  }
  const name = memberAccessName(callee);
  if (name !== null) {
    const obj = unwrap(callee.expression);
    if (obj.kind === tsapi.SyntaxKind.SuperKeyword) {
      const base = ctx.owner && superClassOf(p, ctx.owner);
      const m = base && lookupMember(p, base, name, false);
      return m ? [{ fn: m.fn, ctx: { receiver: ctx.receiver, owner: m.owner, fn: m.fn } }] : [];
    }
    const staticClass = tsapi.isIdentifier(obj) || tsapi.isPropertyAccessExpression(obj) ? classOfValue(p, obj) : null;
    if (staticClass) {
      const m = lookupMember(p, staticClass, name, true);
      return m ? [{ fn: m.fn, ctx: { receiver: null, owner: m.owner, fn: m.fn } }] : [];
    }
    if (!memberIsProjectCode(p, callee)) return [];
    const targets = [];
    for (const c of valueClasses(p, obj, ctx)) {
      const m = lookupMember(p, c, name, false);
      if (m) targets.push({ fn: m.fn, ctx: { receiver: c, owner: m.owner, fn: m.fn } });
    }
    if (targets.length > 0) return targets;
  }
  // A plain call, or a member no class answered (an object literal's method):
  // whatever the checker resolved the call to, when that is project code.
  const sig = p.checker.getResolvedSignature(call);
  const decl = sig && sig.declaration;
  if (decl && decl.body && isProject(p, decl)) {
    const owner = decl.parent && isClassLike(decl.parent) ? decl.parent : null;
    const receiver = owner && !isStaticMember(decl) ? owner : null;
    return [{ fn: decl, ctx: { receiver, owner, fn: decl } }];
  }
  return [];
}

// functionReferenced: the project function or method an argument REFERS TO
// without calling it — a callback the receiver may call.
function referencedTargets(p, arg, ctx) {
  const tsapi = loadTS();
  const e = unwrap(arg);
  if (!e) return [];
  if (tsapi.isPropertyAccessExpression(e)) {
    const d = declOf(p, e.name);
    const isMethod = d && isProject(p, d) && (tsapi.isMethodDeclaration(d) || tsapi.isPropertyDeclaration(d));
    if (!isMethod) return [];
    const out = [];
    for (const c of valueClasses(p, e.expression, ctx)) {
      const m = lookupMember(p, c, e.name.text, false);
      if (m) out.push({ fn: m.fn, ctx: { receiver: c, owner: m.owner, fn: m.fn } });
    }
    return out;
  }
  if (tsapi.isIdentifier(e)) {
    const d = declOf(p, e);
    if (!d || !isProject(p, d)) return [];
    if (tsapi.isFunctionDeclaration(d) && d.body) return [{ fn: d, ctx: { receiver: null, owner: null, fn: d } }];
    if (tsapi.isVariableDeclaration(d) && d.initializer) {
      const init = unwrap(d.initializer);
      if (tsapi.isArrowFunction(init) || tsapi.isFunctionExpression(init)) {
        return [{ fn: init, ctx: { receiver: null, owner: null, fn: init } }];
      }
    }
  }
  return [];
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// literalsOf: every value a code or status argument can have, when the compiler
// can list them — a literal, or an expression whose type (narrowed where it is
// read) is a literal or a union of literals. null when the value is decided at
// runtime.
function literalsOf(p, node, kind) {
  const tsapi = loadTS();
  const n = unwrap(node);
  if (!n) return null;
  if (kind === 'string' && (tsapi.isStringLiteral(n) || tsapi.isNoSubstitutionTemplateLiteral(n))) return [n.text];
  if (kind === 'number' && tsapi.isNumericLiteral(n)) return [Number(n.text)];
  const type = p.checker.getTypeAtLocation(n);
  const out = [];
  for (const t of type.isUnion() ? type.types : [type]) {
    if (kind === 'string' && t.isStringLiteral()) out.push(t.value);
    else if (kind === 'number' && t.isNumberLiteral()) out.push(t.value);
    else return null;
  }
  return out.length > 0 ? [...new Set(out)] : null;
}

// warn records a throw the build cannot type: its code is decided at runtime
// (a proxy passing on an upstream refusal is the honest case). It is not
// declared — no client can have a case for a code nobody knows — and the build
// says so, naming the site, every time it runs.
function warn(p, site, message) {
  const at = siteOf(p, site);
  if (!p.warnings.has(at)) p.warnings.set(at, `${at} — ${message}`);
}

function isUndefinedArg(node) {
  const tsapi = loadTS();
  const n = unwrap(node);
  return !n || (tsapi.isIdentifier(n) && n.text === 'undefined') || n.kind === tsapi.SyntaxKind.VoidExpression;
}

// nameFromCode: the case name a client gives an error that has no class of its
// own — "payload_too_large" → PayloadTooLarge, "invite.emailMismatch" →
// InviteEmailMismatch.
function nameFromCode(code) {
  const name = code
    .split(/[^A-Za-z0-9]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join('');
  return /^[A-Za-z]/.test(name) ? name : `E${name}`;
}

function sdkError(p, sdk, args, site, ownName) {
  if (SDK_RAW_ERRORS.has(sdk)) {
    const statuses = literalsOf(p, args[0], 'number');
    const codes = literalsOf(p, args[1], 'string');
    if (!codes || !statuses || statuses.length !== 1) {
      warn(p, site,
        `new ${sdk}(status, code, …) with a code or status decided at runtime cannot be typed; clients receive ` +
          `it as their untyped case. To type it, give the code and status literal types (or declare it with defineError)`);
      return [];
    }
    return codes.map((code) => ({ name: ownName && codes.length === 1 ? ownName : nameFromCode(code), code, status: statuses[0] }));
  }
  const entry = Object.prototype.hasOwnProperty.call(SDK_ERRORS, sdk) ? SDK_ERRORS[sdk] : null;
  if (!entry) return [];
  let codes = [entry.code];
  if (entry.codeArg !== null && args.length > entry.codeArg && !isUndefinedArg(args[entry.codeArg])) {
    codes = literalsOf(p, args[entry.codeArg], 'string');
    if (!codes) {
      warn(p, site,
        `new ${sdk}(…) with a code decided at runtime cannot be typed; clients receive it as their untyped case. ` +
          `To type it, give the code a literal type (or declare it with defineError)`);
      return [];
    }
  }
  return codes.map((code) => ({
    name: ownName && codes.length === 1 ? ownName : code === entry.code ? sdk : nameFromCode(code),
    code,
    status: entry.status,
  }));
}

function defineErrorCall(p, decl) {
  const tsapi = loadTS();
  const init = decl.initializer && unwrap(decl.initializer);
  return init && tsapi.isCallExpression(init) && sdkExport(p, init.expression) === 'defineError' ? init : null;
}

function definedError(p, decl, call) {
  const codes = literalsOf(p, call.arguments[0], 'string');
  const statuses = literalsOf(p, call.arguments[1], 'number');
  const code = codes && codes.length === 1 ? codes[0] : null;
  const status = statuses && statuses.length === 1 ? statuses[0] : null;
  if (code === null || status === null) {
    refuse(p, call,
      `defineError(...) for "${decl.name.getText()}" declares a typed error, so its code and status must be known ` +
        `when the project is built (a literal, or a constant of literal type)`);
  }
  return { name: decl.name.getText(), code, status };
}

function superCallOf(ctor) {
  const tsapi = loadTS();
  let found = null;
  const visit = (n) => {
    if (found || tsapi.isFunctionLike(n)) return;
    if (tsapi.isCallExpression(n) && n.expression.kind === tsapi.SyntaxKind.SuperKeyword) {
      found = n;
      return;
    }
    tsapi.forEachChild(n, visit);
  };
  tsapi.forEachChild(ctor.body, visit);
  return found;
}

// errorOfClass: the typed error `new cls(args)` builds — the base's, with the
// code the class's own constructor hands up; null when cls is not a typed error.
function errorOfClass(p, cls, args, site) {
  const tsapi = loadTS();
  const clause = (cls.heritageClauses || []).find((h) => h.token === tsapi.SyntaxKind.ExtendsKeyword);
  const base = clause && clause.types[0] && clause.types[0].expression;
  if (!base) return [];
  const ctor = cls.members.find((m) => tsapi.isConstructorDeclaration(m) && m.body);
  let upArgs = args;
  let upSite = site;
  if (ctor) {
    const sup = superCallOf(ctor);
    if (!sup) return [];
    upArgs = sup.arguments;
    upSite = sup;
  }
  const own = classNameOf(cls);
  const sdk = sdkExport(p, base);
  if (sdk) return sdkError(p, sdk, upArgs, upSite, own);
  const d = declOf(p, base);
  if (d && tsapi.isVariableDeclaration(d)) {
    const call = defineErrorCall(p, d);
    return call ? [{ ...definedError(p, d, call), name: own || d.name.getText() }] : [];
  }
  const baseClass = classOfValue(p, base);
  if (baseClass) {
    const es = errorOfClass(p, baseClass, upArgs, upSite);
    return es.length === 1 && own ? [{ ...es[0], name: own }] : es;
  }
  return [];
}

// errorOfNew: the typed errors a `new X(...)` can build — none for a class that
// is not a typed error, several for a code the compiler knows to be one of a
// few literals.
function errorOfNew(p, ne) {
  const tsapi = loadTS();
  const args = ne.arguments || [];
  const sdk = sdkExport(p, ne.expression);
  if (sdk) return sdkError(p, sdk, args, ne);
  const target = unwrap(ne.expression);
  const d = declOf(p, tsapi.isPropertyAccessExpression(target) ? target.name : target);
  if (!d) return [];
  if (tsapi.isVariableDeclaration(d)) {
    const call = defineErrorCall(p, d);
    if (call) return [definedError(p, d, call)];
  }
  const cls = classOfDecl(d);
  return cls && isProject(p, cls) ? errorOfClass(p, cls, args, ne) : [];
}

// errorsOf: the typed errors a thrown (or expectation-handed) value can be,
// each sent where an error raised at `origin` goes (see destination).
function errorsOf(p, expr, ctx, env, origin, originAsync, seen = new Set()) {
  const tsapi = loadTS();
  expr = unwrap(expr);
  if (!expr) return;
  const key = `${idOf(p, expr)}@${ctx.receiver ? idOf(p, ctx.receiver) : 0}`;
  if (seen.has(key)) return;
  seen.add(key);
  if (tsapi.isNewExpression(expr)) {
    const site = siteOf(p, expr);
    raise(p, env, origin, originAsync, errorOfNew(p, expr).map((e) => ({ ...e, site })));
    return;
  }
  if (tsapi.isConditionalExpression(expr)) {
    errorsOf(p, expr.whenTrue, ctx, env, origin, originAsync, seen);
    errorsOf(p, expr.whenFalse, ctx, env, origin, originAsync, seen);
    return;
  }
  if (tsapi.isBinaryExpression(expr)) {
    const K = tsapi.SyntaxKind;
    const k = expr.operatorToken.kind;
    if (k === K.QuestionQuestionToken || k === K.BarBarToken) errorsOf(p, expr.left, ctx, env, origin, originAsync, seen);
    errorsOf(p, expr.right, ctx, env, origin, originAsync, seen);
    return;
  }
  if (tsapi.isCallExpression(expr)) {
    // The factory idiom: the callee RETURNS the error. (Its own throws are
    // collected by the walk, which visits this call like any other.)
    for (const t of callTargets(p, expr, ctx)) {
      for (const r of returnedExpressions(t.fn)) errorsOf(p, r, t.ctx, env, origin, originAsync, seen);
    }
    return;
  }
  if (tsapi.isIdentifier(expr) || tsapi.isPropertyAccessExpression(expr)) {
    const d = declOf(p, tsapi.isPropertyAccessExpression(expr) ? expr.name : expr);
    if (d && (tsapi.isVariableDeclaration(d) || tsapi.isPropertyDeclaration(d)) && d.initializer) {
      const at = tsapi.isVariableDeclaration(d) ? contextOf(d, ctx) : ctx;
      errorsOf(p, d.initializer, at, env, origin, originAsync, seen);
    }
  }
}

// ---------------------------------------------------------------------------
// Where an error goes
// ---------------------------------------------------------------------------
//
// An error raised inside a `try`, inside `Promise.allSettled(...)`, or on a
// promise with a `.catch(...)` may never leave the function — counting it
// would give the endpoint a case it can never receive. Each of those opens a
// SCOPE while its inside is walked, and `destination` decides, innermost scope
// first, where an error raised at `origin` ends up:
//
//   try        a synchronous error is caught. An ASYNCHRONOUS one — a call into
//              an async function, a data-layer expectation, anything inside an
//              async callback — is caught only if it is awaited inside the
//              `try`: `try { return this.save() } catch {}` does not catch
//              save()'s rejection, `try { return await this.save() }` does.
//              What the `try` catches reaches the function again only if its
//              `catch` rethrows (or has none).
//   settle     `Promise.allSettled(...)` turns rejections into results: an
//              asynchronous error inside is gone; a synchronous one (thrown
//              while the argument is built) still propagates.
//   catch      `p.catch(h)` / `p.then(ok, h)` swallows p's rejections unless h
//              rethrows them.
//
// Nothing caught is lost by accident: a scope removes only what it provably
// handles, and everything else continues outward to the function's summary.

function isAsyncFunction(p, fn) {
  const tsapi = loadTS();
  if (!tsapi.isFunctionLike(fn)) return false;
  if ((tsapi.getCombinedModifierFlags(fn) & tsapi.ModifierFlags.Async) !== 0) return true;
  const sig = p.checker.getSignatureFromDeclaration(fn);
  if (!sig) return false;
  const ret = p.checker.getReturnTypeOfSignature(sig);
  return p.checker.getPromisedTypeOfPromise(ret) !== undefined;
}

// outermostAsyncBetween: the outermost async function-like strictly between
// `node` and `container`, or null.
function outermostAsyncBetween(p, node, container) {
  const tsapi = loadTS();
  let found = null;
  for (let n = node.parent; n && n !== container; n = n.parent) {
    if (tsapi.isFunctionLike(n) && (tsapi.getCombinedModifierFlags(n) & tsapi.ModifierFlags.Async) !== 0) found = n;
  }
  return found;
}

function awaitedWithin(from, container) {
  const tsapi = loadTS();
  for (let n = from.parent; n && n !== container; n = n.parent) {
    if (tsapi.isAwaitExpression(n)) return true;
    if (tsapi.isForOfStatement(n) && n.awaitModifier) return true;
  }
  return false;
}

function destination(p, env, origin, originAsync) {
  for (let i = env.scopes.length - 1; i >= 0; i--) {
    const s = env.scopes[i];
    const boundary = outermostAsyncBetween(p, origin, s.node);
    const isAsync = originAsync || boundary !== null;
    if (s.kind === 'try') {
      if (!isAsync || awaitedWithin(boundary || origin, s.node)) return s.sink;
    } else if (s.kind === 'settle') {
      if (isAsync) return null;
    } else if (s.kind === 'catch') {
      if (isAsync && !s.rethrows) return null;
    }
  }
  return env.root;
}

// knownStatuses: the one status each code carries, as far as the build can see
// — the SDK's own codes, every defineError the program declares, and each throw
// site as it is found.
function knownStatuses(p) {
  const tsapi = loadTS();
  if (p.statusOf) return p.statusOf;
  const statusOf = new Map();
  for (const [name, e] of Object.entries(SDK_ERRORS)) statusOf.set(e.code, { status: e.status, site: `@palbase/backend ${name}` });
  const visitNode = (n) => {
    if (tsapi.isVariableDeclaration(n) && tsapi.isIdentifier(n.name)) {
      const call = defineErrorCall(p, n);
      const codes = call && literalsOf(p, call.arguments[0], 'string');
      const statuses = call && literalsOf(p, call.arguments[1], 'number');
      if (codes && statuses && codes.length === 1 && statuses.length === 1 && !statusOf.has(codes[0])) {
        statusOf.set(codes[0], { status: statuses[0], site: siteOf(p, n) });
      }
    }
    tsapi.forEachChild(n, visitNode);
  };
  for (const sf of p.program.getSourceFiles()) if (isProjectFile(p, sf.fileName)) visitNode(sf);
  p.statusOf = statusOf;
  return statusOf;
}

// claimStatus refuses a code thrown with a status other than the one it already
// carries. A client matches an error by its code, and the runtime refuses to
// describe such a contract — so the build refuses it first, naming both sites.
function claimStatus(p, e) {
  const statusOf = knownStatuses(p);
  const prev = statusOf.get(e.code);
  if (!prev) {
    statusOf.set(e.code, { status: e.status, site: e.site });
    return;
  }
  if (prev.status !== e.status) {
    throw new ThrowAnalysisError(
      `"${e.code}" is thrown with status ${e.status} (${e.site}) but the code carries status ${prev.status} ` +
        `(${prev.site}) — a client matches an error by its code, so one code carries one status. Throw the ` +
        `code's own class, or give the error its own code`,
    );
  }
}

function raise(p, env, origin, originAsync, errors) {
  for (const e of errors) claimStatus(p, e);
  const sink = destination(p, env, origin, originAsync);
  if (!sink) return;
  for (const e of errors) if (!sink.has(e.code)) sink.set(e.code, e);
}

// rethrowsParam: does a handler throw (or hand on) the error it was given?
// Handing it to a call counts — `throw mapped(e)` may give `e` back.
function rethrowsParam(body, name) {
  const tsapi = loadTS();
  if (!name) return false;
  let found = false;
  const mentions = (n) => {
    let hit = false;
    const look = (m) => {
      if (hit) return;
      if (tsapi.isIdentifier(m) && m.text === name) hit = true;
      else tsapi.forEachChild(m, look);
    };
    look(n);
    return hit;
  };
  const visitNode = (n) => {
    if (found || (tsapi.isFunctionLike(n) && n !== body)) return;
    if (tsapi.isThrowStatement(n) && n.expression && mentions(n.expression)) found = true;
    else tsapi.forEachChild(n, visitNode);
  };
  visitNode(body);
  return found;
}

function handlerRethrows(p, handler) {
  const tsapi = loadTS();
  const h = unwrap(handler);
  if (h && (tsapi.isArrowFunction(h) || tsapi.isFunctionExpression(h))) {
    const param = h.parameters[0];
    if (!param) return false;
    if (!tsapi.isIdentifier(param.name)) return true; // destructured: assume it is passed on
    return tsapi.isBlock(h.body) ? rethrowsParam(h.body, param.name.text) : false;
  }
  return true; // a handler by reference: assume it rethrows
}

function isPromiseStatic(p, callee, name) {
  const tsapi = loadTS();
  if (!tsapi.isPropertyAccessExpression(callee) || callee.name.text !== name) return false;
  const obj = unwrap(callee.expression);
  if (!tsapi.isIdentifier(obj) || obj.text !== 'Promise') return false;
  const d = declOf(p, obj);
  return !d || !isProject(p, d);
}

// ---------------------------------------------------------------------------
// The walk
// ---------------------------------------------------------------------------

// summaryOf: the errors that can escape a call to `fn` on `ctx.receiver`.
// Memoized across routes; recursion is settled by re-running the analysis until
// no summary grows (createThrowAnalysis → analyze).
function summaryOf(p, fn, ctx) {
  const tsapi = loadTS();
  const key = `${idOf(p, fn)}@${ctx.receiver ? idOf(p, ctx.receiver) : 0}`;
  let entry = p.summaries.get(key);
  if (entry && (entry.final || entry.round === p.round)) {
    if (entry.busy) p.cyclic = true;
    return entry.errors;
  }
  entry = { errors: entry ? new Map(entry.errors) : new Map(), busy: true, round: p.round, final: false };
  p.summaries.set(key, entry);
  const before = entry.errors.size;
  const env = { root: entry.errors, scopes: [] };
  if (tsapi.isFunctionLike(fn)) {
    for (const param of fn.parameters || []) if (param.initializer) visit(p, param.initializer, ctx, env);
    if (fn.body) visit(p, fn.body, ctx, env);
  } else {
    visit(p, fn, ctx, env); // a field initializer
  }
  entry.busy = false;
  if (entry.errors.size !== before) p.grew = true;
  return entry.errors;
}

function withScope(env, scope) {
  return { root: env.root, scopes: [...env.scopes, scope] };
}

function visit(p, node, ctx, env) {
  const tsapi = loadTS();
  if (tsapi.isTryStatement(node)) {
    const trySink = new Map();
    visit(p, node.tryBlock, ctx, withScope(env, { kind: 'try', node: node.tryBlock, sink: trySink }));
    const clause = node.catchClause;
    const rethrows = !clause ||
      (clause.variableDeclaration && tsapi.isIdentifier(clause.variableDeclaration.name)
        ? rethrowsParam(clause.block, clause.variableDeclaration.name.text)
        : Boolean(clause.variableDeclaration)); // a destructured binding: assume it is passed on
    if (rethrows) raise(p, env, node, false, [...trySink.values()]);
    if (clause) visit(p, clause.block, ctx, env);
    if (node.finallyBlock) visit(p, node.finallyBlock, ctx, env);
    return;
  }
  if (tsapi.isThrowStatement(node)) {
    if (node.expression) errorsOf(p, node.expression, ctx, env, node, false);
  } else if (tsapi.isCallExpression(node)) {
    const callee = unwrap(node.expression);
    if (isPromiseStatic(p, callee, 'allSettled')) {
      visit(p, node.expression, ctx, env);
      const settled = withScope(env, { kind: 'settle', node });
      for (const a of node.arguments) visit(p, a, ctx, settled);
      return;
    }
    const handlerAt = tsapi.isPropertyAccessExpression(callee)
      ? (callee.name.text === 'catch' ? 0 : callee.name.text === 'then' && node.arguments.length > 1 ? 1 : -1)
      : -1;
    if (handlerAt >= 0) {
      const rethrows = handlerRethrows(p, node.arguments[handlerAt]);
      visit(p, callee.expression, ctx, withScope(env, { kind: 'catch', node: callee.expression, rethrows }));
      for (const a of node.arguments) visit(p, a, ctx, env);
      return;
    }
    const verb = tsapi.isPropertyAccessExpression(callee) ? EXPECTATIONS.get(callee.name.text) : undefined;
    if (verb !== undefined && node.arguments[verb]) errorsOf(p, node.arguments[verb], ctx, env, node, true);
    const onBehalf = sdkApiErrors(p, callee);
    if (onBehalf.length > 0) {
      const site = siteOf(p, node);
      raise(p, env, node, true, onBehalf.map((e) => ({ ...e, site })));
    }
    for (const t of callTargets(p, node, ctx)) {
      raise(p, env, node, isAsyncFunction(p, t.fn), [...summaryOf(p, t.fn, t.ctx).values()]);
    }
    for (const a of node.arguments) {
      for (const t of referencedTargets(p, a, ctx)) raise(p, env, a, isAsyncFunction(p, t.fn), [...summaryOf(p, t.fn, t.ctx).values()]);
    }
  } else if (tsapi.isNewExpression(node)) {
    const c = classOfValue(p, node.expression);
    if (c) {
      for (const t of constructionTargets(p, c, c)) raise(p, env, node, false, [...summaryOf(p, t.fn, t.ctx).values()]);
    }
    for (const a of node.arguments || []) {
      for (const t of referencedTargets(p, a, ctx)) raise(p, env, a, isAsyncFunction(p, t.fn), [...summaryOf(p, t.fn, t.ctx).values()]);
    }
  } else if (tsapi.isPropertyAccessExpression(node) && !(node.parent && tsapi.isCallExpression(node.parent) && node.parent.expression === node)) {
    // Reading a getter runs it.
    const d = declOf(p, node.name);
    if (d && tsapi.isGetAccessorDeclaration(d) && d.body && isProject(p, d)) {
      for (const c of valueClasses(p, node.expression, ctx)) {
        const m = lookupMember(p, c, node.name.text, false);
        if (m) raise(p, env, node, false, [...summaryOf(p, m.fn, { receiver: c, owner: m.owner, fn: m.fn }).values()]);
      }
    }
  }
  if (isClassLike(node)) return; // a class body runs when it is constructed
  // A nested `function` has its own `this`; an arrow shares the enclosing one.
  const inner = tsapi.isFunctionExpression(node) || tsapi.isFunctionDeclaration(node)
    ? { receiver: null, owner: null, fn: node }
    : ctx;
  tsapi.forEachChild(node, (child) => visit(p, child, inner, env));
}

function lowerCamel(name) {
  return name.charAt(0).toLowerCase() + name.slice(1);
}

function analyzeRoute(p, cls, method) {
  const errors = summaryOf(p, method, { receiver: cls, owner: cls, fn: method });
  // Every client names an error case after its name (the spec keys it by the
  // lower-camel form), so two codes sharing one name in one endpoint would
  // leave a client with only one of them.
  const byKey = new Map();
  for (const e of errors.values()) {
    const k = lowerCamel(e.name);
    const prev = byKey.get(k);
    if (prev) {
      throw new ThrowAnalysisError(
        `${cls.name.text}.${memberNameOf(method.name)} can throw two errors named "${e.name}" — "${prev.code}" ` +
          `(${prev.site}) and "${e.code}" (${e.site}). Every client names an error case after it; give one of ` +
          `them its own defineError`,
      );
    }
    byKey.set(k, e);
  }
  return [...errors.values()].map(({ name, code, status }) => ({ name, code, status }));
}

function analyzeFile(p, controllerPath) {
  const tsapi = loadTS();
  const file = path.resolve(controllerPath);
  const sf = p.program.getSourceFile(file);
  if (!sf) {
    throw new ThrowAnalysisError(
      `${path.relative(p.root, file)} is not a controller file under the analyzed project root (${p.root})`,
    );
  }
  const out = [];
  for (const stmt of sf.statements) {
    if (!tsapi.isClassDeclaration(stmt)) continue;
    if (!(tsapi.getDecorators(stmt) || []).some((d) => decoratorName(p, d) === 'Controller')) continue;
    // The bindings reach the class by name; an anonymous one cannot be reached.
    if (!stmt.name) refuse(p, stmt, 'a @Controller class needs a name — its routes\' errors are bound to it by name');
    const ops = {};
    for (const m of stmt.members) {
      if (!tsapi.isMethodDeclaration(m) || !m.body) continue;
      if (!(tsapi.getDecorators(m) || []).some((d) => ROUTE_DECORATORS.has(decoratorName(p, d)))) continue;
      ops[memberNameOf(m.name)] = analyzeRoute(p, stmt, m);
    }
    out.push({ className: stmt.name.text, ops });
  }
  return out;
}

// ---------------------------------------------------------------------------
// Injection
// ---------------------------------------------------------------------------

// buildThrowInjection renders one recordThrows IIFE per analyzed route —
// INCLUDING empty sets, which say "analyzed, nothing thrown" rather than "never
// analyzed". The IIFE shape is a cross-repo contract (the SDK's recordThrows).
function buildThrowInjection(className, ops) {
  const fnNames = Object.keys(ops);
  if (fnNames.length === 0) return '';
  const lines = ['', '// --- AUTO-GENERATED: route throw bindings (inferred throw sites) ---'];
  for (const fn of fnNames) {
    const descriptors = ops[fn]
      .map((d) => `{name:${JSON.stringify(d.name)},code:${JSON.stringify(d.code)},status:${d.status}}`)
      .join(',');
    lines.push(`;(function(){ require("@palbase/backend").recordThrows(${className}.prototype, ${JSON.stringify(fn)}, [${descriptors}]); })();`);
  }
  lines.push('');
  return lines.join('\n');
}

/**
 * injectThrowBindings appends every controller class's recordThrows IIFEs to
 * the controller's (possibly already transformed) source text.
 *
 * @param {string} sourceText     what the stager is about to write
 * @param {string} controllerPath the controller's REAL path in the project
 * @param {{analyze: Function}} analysis  createThrowAnalysis(projectRoot)
 */
function injectThrowBindings(sourceText, controllerPath, analysis) {
  const snippet = analysis
    .analyze(controllerPath)
    .map(({ className, ops }) => buildThrowInjection(className, ops))
    .join('');
  return snippet ? `${sourceText}\n${snippet}\n` : sourceText;
}

module.exports = {
  SDK_API_ERRORS,
  ThrowAnalysisError,
  createThrowAnalysis,
  injectThrowBindings,
};
