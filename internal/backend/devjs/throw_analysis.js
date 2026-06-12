'use strict';

/**
 * Palbase Backend Runtime — Per-Endpoint Throw Inference (typed backend errors)
 *
 * Sibling of return_types.js: a parser-only (TS parser AST, no TypeChecker)
 * pass that, for each controller route method, collects the error classes the
 * method can throw — directly or through the resolvable slice of its call
 * graph — and appends a `recordThrows(...)` IIFE per op so the live route
 * registry (RouteMeta.throws), the meta extractor, and the worker's
 * conformance guard all see the inferred set. The wire shape downstream is the
 * existing `x-palbase-errors` v1 extension.
 *
 * Resolution rules (anything else is SKIPPED SILENTLY — misses degrade to the
 * client's `.other` case and light up the runtime conformance guard):
 *
 *   throw new X(...)        X via import map / local decl:
 *                           - SDK named classes (NotFound/Conflict/…): fixed
 *                             code table; a LITERAL second-arg code override is
 *                             honored, a non-literal override drops the site.
 *                           - HttpError/PalError: literal code arg → that code
 *                             under the name "PalError"; non-literal → drop.
 *                           - defineError classes: the defining file's
 *                             defineError("<lit>", <lit>, …) literals. Non-
 *                             literal args there are the ONE HARD ERROR this
 *                             analysis raises (deploy fails naming the file).
 *   svc.m(...)              `svc` imported/local → defining module →
 *                           `const svc = new SvcClass(…)` → class decl →
 *                           method body, recurse.
 *   Cls.m(...)              imported/local class decl → STATIC method body.
 *   fn(...)                 imported/local function decl (or const fn = …).
 *   this.m(...)             same-class method body.
 *
 * Transitive walk is depth-bounded (8), cycle-safe via a (file, symbol)
 * visited set, and PROJECT FILES ONLY: relative imports resolve against the
 * REAL project tree (`.ts` / `/index.ts` candidates); bare specifiers other
 * than @palbase/backend never resolve. Per-op descriptors dedupe by code.
 *
 * This module is SHARED VERBATIM by the deploy stager and `palbase serve`
 * (the same parity rule as return_types.js). It needs only `typescript` on
 * NODE_PATH and never touches the filesystem itself — the caller injects
 * { readFile, fileExists }.
 */

const path = require('path');

let ts = null;
function loadTS() {
  if (ts) return ts;
  ts = require('typescript');
  return ts;
}

class ThrowAnalysisError extends Error {}

const SDK_SPECIFIER = '@palbase/backend';

// Fixed default-code table for the SDK's named error classes (mirrors
// backend/src/errors.ts — ctor (message?, code?, data?)). Pre-seeded as
// built-ins in the SDK error registry, so extract/openapi resolve their
// status/hasData by code.
const SDK_NAMED_ERRORS = {
  BadRequest: 'bad_request',
  Unauthorized: 'unauthorized',
  Forbidden: 'forbidden',
  NotFound: 'not_found',
  Conflict: 'conflict',
  TooManyRequests: 'too_many_requests',
};

// The named built-ins whose ctor is DATA-FIRST (`new X(data, message?)`) rather
// than the data-less `(message?, code?)` shape. Their 2nd arg is a message, NOT
// a code override — so the canonical code from the table always wins and the
// site must NOT be dropped when the 2nd arg is a non-string (it is the data
// object or a variable). Must stay in lockstep with the SDK: these are the
// errors error-registry.ts pre-seeds with a dataSchema.
const SDK_DATA_FIRST_ERRORS = new Set(['BadRequest', 'TooManyRequests']);

// Raw SDK error classes — ctor (status, code, message, data?): the code is the
// SECOND argument. Both surface under the name "PalError" (HttpError is the
// base; the distinction carries no client meaning).
const SDK_RAW_ERRORS = new Set(['HttpError', 'PalError']);

// Maximum call-graph depth: the route method body is depth 0; each resolved
// call enters its target body at depth+1. Bodies deeper than MAX_DEPTH are not
// entered (their throws degrade to `.other` + the runtime guard).
const MAX_DEPTH = 8;

// ---------------------------------------------------------------------------
// File model
// ---------------------------------------------------------------------------

// parseFile lowers one source file into the binding maps the resolver needs.
// Cached per analysis run (ctx.files) so shared services parse once.
function parseFile(ctx, filePath, sourceText) {
  const tsapi = loadTS();
  const sf = tsapi.createSourceFile(
    filePath,
    sourceText,
    tsapi.ScriptTarget.ES2022,
    /* setParentNodes */ true,
  );

  const imports = new Map(); // localName → { specifier, original }
  const classes = new Map(); // className → ClassDeclaration
  const functions = new Map(); // fnName → node with a .body (FunctionDeclaration / arrow / fn-expr)
  const vars = new Map(); // varName → initializer node (may be undefined)

  for (const stmt of sf.statements) {
    if (tsapi.isImportDeclaration(stmt) && stmt.importClause) {
      const spec = stmt.moduleSpecifier.text;
      const named = stmt.importClause.namedBindings;
      if (named && tsapi.isNamedImports(named)) {
        for (const el of named.elements) {
          const local = el.name.text;
          const original = el.propertyName ? el.propertyName.text : local;
          imports.set(local, { specifier: spec, original });
        }
      }
    } else if (tsapi.isClassDeclaration(stmt) && stmt.name) {
      classes.set(stmt.name.text, stmt);
    } else if (tsapi.isFunctionDeclaration(stmt) && stmt.name && stmt.body) {
      functions.set(stmt.name.text, stmt);
    } else if (tsapi.isVariableStatement(stmt)) {
      for (const d of stmt.declarationList.declarations) {
        if (tsapi.isIdentifier(d.name)) {
          vars.set(d.name.text, d.initializer);
          // `const fn = () => {}` / `const fn = function () {}` is callable.
          if (
            d.initializer &&
            (tsapi.isArrowFunction(d.initializer) || tsapi.isFunctionExpression(d.initializer)) &&
            d.initializer.body
          ) {
            functions.set(d.name.text, d.initializer);
          }
        }
      }
    }
  }

  return { path: filePath, sf, imports, classes, functions, vars };
}

// loadFile reads + parses a project file through the deps seam (null when the
// file does not exist / cannot be read).
function loadFile(ctx, filePath) {
  if (ctx.files.has(filePath)) return ctx.files.get(filePath);
  let info = null;
  const text = ctx.deps.readFile(filePath);
  if (typeof text === 'string') {
    info = parseFile(ctx, filePath, text);
  }
  ctx.files.set(filePath, info);
  return info;
}

// resolveModuleFile resolves a RELATIVE import specifier against the importing
// file's REAL directory: `<spec>.ts` then `<spec>/index.ts`. Bare specifiers
// (node_modules) never resolve — project files only.
function resolveModuleFile(ctx, fromPath, specifier) {
  if (!specifier.startsWith('./') && !specifier.startsWith('../')) return null;
  const base = path.resolve(path.dirname(fromPath), specifier);
  // Clamp to the project root — an `import "../../../etc/x"` chain must not
  // walk the analyzer outside the project tree (spec: project files only).
  const rel = path.relative(ctx.projectRoot, base);
  if (rel === '..' || rel.startsWith('..' + path.sep) || path.isAbsolute(rel)) return null;
  const candidates = base.endsWith('.ts')
    ? [base]
    : [base + '.ts', path.join(base, 'index.ts')];
  for (const c of candidates) {
    if (ctx.deps.fileExists(c)) return c;
  }
  return null;
}

// ---------------------------------------------------------------------------
// Binding resolution (across files, bounded hops)
// ---------------------------------------------------------------------------

// resolveBinding resolves `name` in `fileInfo` to a concrete declaration:
//   { kind: 'sdk', original }                       — named import from @palbase/backend
//   { kind: 'class', file, node }                   — class declaration
//   { kind: 'function', file, node }                — function decl / const fn
//   { kind: 'var', file, node /*initializer*/ }     — other variable initializer
// Follows re-imports through project files (bounded, cycle-safe). null = not
// resolvable (dynamic / node_modules / missing file).
function resolveBinding(ctx, fileInfo, name, hops) {
  if (!fileInfo || hops > MAX_DEPTH) return null;

  const imp = fileInfo.imports.get(name);
  if (imp) {
    if (imp.specifier === SDK_SPECIFIER) {
      return { kind: 'sdk', original: imp.original };
    }
    const target = resolveModuleFile(ctx, fileInfo.path, imp.specifier);
    if (!target) return null;
    const targetInfo = loadFile(ctx, target);
    return resolveBinding(ctx, targetInfo, imp.original, hops + 1);
  }
  if (fileInfo.classes.has(name)) {
    return { kind: 'class', file: fileInfo, node: fileInfo.classes.get(name) };
  }
  if (fileInfo.functions.has(name)) {
    return { kind: 'function', file: fileInfo, node: fileInfo.functions.get(name) };
  }
  if (fileInfo.vars.has(name)) {
    return { kind: 'var', file: fileInfo, node: fileInfo.vars.get(name) };
  }
  return null;
}

// ---------------------------------------------------------------------------
// Throw-site lowering
// ---------------------------------------------------------------------------

// literalString returns the literal value of a string-literal-ish node, else
// null (a template WITH substitutions, an identifier, a call, … are not
// literals).
function literalString(tsapi, node) {
  if (!node) return null;
  if (tsapi.isStringLiteral(node)) return node.text;
  if (tsapi.isNoSubstitutionTemplateLiteral(node)) return node.text;
  return null;
}

// addDescriptor appends {name, code} to out, deduping by code (first name
// wins — names are cosmetic, the code is the join key everywhere downstream).
function addDescriptor(out, name, code) {
  if (out.some((d) => d.code === code)) return;
  out.push({ name, code });
}

// resolveThrownNew lowers ONE `throw new X(args)` site into a descriptor (or
// silently skips it). The ONLY hard error: X is a defineError class whose
// defining call has non-literal code/status args.
function resolveThrownNew(ctx, fileInfo, newExpr, out) {
  const tsapi = loadTS();
  if (!tsapi.isIdentifier(newExpr.expression)) return; // computed/namespaced ctor → skip
  const className = newExpr.expression.text;
  const args = newExpr.arguments || [];

  const binding = resolveBinding(ctx, fileInfo, className, 0);
  if (!binding) return;

  if (binding.kind === 'sdk') {
    const original = binding.original;
    if (SDK_NAMED_ERRORS[original]) {
      // Two ctor shapes coexist among the named built-ins:
      //   data-first (data, message?): `new TooManyRequests({retryAfter}, "msg")`
      //     — the 2nd arg is a MESSAGE, never a code. The canonical table code
      //     always wins; the site must NOT be dropped when the 1st/2nd arg is a
      //     non-string (the old "non-literal 2nd arg → return" silently lost
      //     these typed-data built-in throws — caught live on the share/bulk
      //     endpoints).
      //   data-less (message?, code?): `new NotFound("msg", "custom_code")` — a
      //     string-literal 2nd arg overrides the canonical code; a non-literal
      //     override is unresolvable and still degrades the site to .other.
      if (SDK_DATA_FIRST_ERRORS.has(original)) {
        addDescriptor(out, original, SDK_NAMED_ERRORS[original]);
        return;
      }
      if (args.length >= 2) {
        const override = literalString(tsapi, args[1]);
        if (override === null) return; // non-literal override → site degrades to .other
        addDescriptor(out, original, override);
        return;
      }
      addDescriptor(out, original, SDK_NAMED_ERRORS[original]);
      return;
    }
    if (SDK_RAW_ERRORS.has(original)) {
      // ctor (status, code, message, data?) — code is the SECOND argument.
      const code = literalString(tsapi, args[1]);
      if (code === null) return;
      addDescriptor(out, 'PalError', code);
      return;
    }
    return; // some other SDK export → not an error class we know
  }

  if (binding.kind === 'var') {
    // `export const TodoLocked = defineError("todo_locked", 409, schema?)`.
    const init = binding.node;
    if (
      init &&
      tsapi.isCallExpression(init) &&
      tsapi.isIdentifier(init.expression) &&
      init.expression.text === 'defineError'
    ) {
      const dargs = init.arguments || [];
      const code = literalString(tsapi, dargs[0]);
      const statusOk = dargs[1] && tsapi.isNumericLiteral(dargs[1]);
      if (code === null || !statusOk) {
        throw new ThrowAnalysisError(
          `${binding.file.path} — defineError(...) for "${className}" must be called with ` +
            `literal arguments (a string-literal code and a numeric-literal status); the ` +
            `static analyzer reads these literals to type the endpoint's errors`,
        );
      }
      addDescriptor(out, className, code);
    }
    // Any other variable-class (hand-rolled subclass, factory, …) → skip.
  }
  // A plain local class declaration → no code to read → skip.
}

// ---------------------------------------------------------------------------
// Call-graph walk
// ---------------------------------------------------------------------------

// classMethod finds a (static or instance) method body on a class declaration.
// classProperty finds a non-static property declaration by name on a class.
function classProperty(tsapi, classNode, name) {
  for (const m of classNode.members) {
    if (!tsapi.isPropertyDeclaration(m)) continue;
    if (!m.name || !tsapi.isIdentifier(m.name) || m.name.text !== name) continue;
    const isStatic = (m.modifiers || []).some((x) => x.kind === tsapi.SyntaxKind.StaticKeyword);
    if (isStatic) continue;
    return m;
  }
  return null;
}

// resolveInstanceMethodFromInit resolves `<init>.<methodName>` where <init> is
// a service-instance initializer expression: `new SvcClass(...)` directly, or
// an identifier that (through imports / one var hop) reaches the singleton
// (`export const todoService = new TodoService()`). Returns a walk target or
// null.
function resolveInstanceMethodFromInit(ctx, fileInfo, init, methodName, hop = 0) {
  const tsapi = loadTS();
  if (!init || hop > MAX_DEPTH) return null;
  if (tsapi.isNewExpression(init) && tsapi.isIdentifier(init.expression)) {
    const clsBinding = resolveBinding(ctx, fileInfo, init.expression.text, 0);
    if (!clsBinding || clsBinding.kind !== 'class') return null;
    const m = classMethod(tsapi, clsBinding.node, methodName, /* static */ false);
    if (!m) return null;
    const clsName = clsBinding.node.name ? clsBinding.node.name.text : '<anon>';
    return {
      file: clsBinding.file,
      body: m.body,
      classNode: clsBinding.node,
      key: `${clsBinding.file.path}#${clsName}.${methodName}`,
    };
  }
  if (tsapi.isIdentifier(init)) {
    const binding = resolveBinding(ctx, fileInfo, init.text, 0);
    if (!binding) return null;
    if (binding.kind === 'var') {
      return resolveInstanceMethodFromInit(ctx, binding.file, binding.node, methodName, hop + 1);
    }
    if (binding.kind === 'class') {
      // `private todos = TodoService` (class object itself) — instance call
      // through a class REFERENCE is not the singleton idiom; skip.
      return null;
    }
  }
  return null;
}

function classMethod(tsapi, classNode, methodName, wantStatic) {
  for (const m of classNode.members) {
    if (!tsapi.isMethodDeclaration(m) || !m.body) continue;
    if (m.name.getText(classNode.getSourceFile()) !== methodName) continue;
    const isStatic = (m.modifiers || []).some((mod) => mod.kind === tsapi.SyntaxKind.StaticKeyword);
    if (isStatic !== wantStatic) continue;
    return m;
  }
  return null;
}

// resolveCallTarget lowers ONE call expression into the body to recurse into:
//   { file, body, classNode|null, key } — or null (skip silently).
function resolveCallTarget(ctx, fileInfo, classNode, callExpr) {
  const tsapi = loadTS();
  const callee = callExpr.expression;

  // fn(...) — plain identifier call.
  if (tsapi.isIdentifier(callee)) {
    const binding = resolveBinding(ctx, fileInfo, callee.text, 0);
    if (binding && binding.kind === 'function' && binding.node.body) {
      return {
        file: binding.file,
        body: binding.node.body,
        classNode: null,
        key: `${binding.file.path}#fn:${callee.text}`,
      };
    }
    return null;
  }

  // obj.m(...) — property access on `this` or a plain identifier.
  if (tsapi.isPropertyAccessExpression(callee) && tsapi.isIdentifier(callee.name)) {
    const methodName = callee.name.text;

    // this.m(...) — same-class method.
    if (callee.expression.kind === tsapi.SyntaxKind.ThisKeyword) {
      if (!classNode) return null;
      const m = classMethod(tsapi, classNode, methodName, /* static */ false);
      if (!m) return null;
      const clsName = classNode.name ? classNode.name.text : '<anon>';
      return {
        file: fileInfo,
        body: m.body,
        classNode,
        key: `${fileInfo.path}#${clsName}.${methodName}`,
      };
    }

    // this.<prop>.m(...) — class property holding a service instance
    // (`private todos = todoService;` / `private svc = new SvcClass();`) —
    // the documented thin-controller idiom. Resolve through the PROPERTY
    // DECLARATION's initializer.
    if (
      tsapi.isPropertyAccessExpression(callee.expression) &&
      callee.expression.expression.kind === tsapi.SyntaxKind.ThisKeyword &&
      tsapi.isIdentifier(callee.expression.name)
    ) {
      if (!classNode) return null;
      const propName = callee.expression.name.text;
      const prop = classProperty(tsapi, classNode, propName);
      if (!prop || !prop.initializer) return null;
      return resolveInstanceMethodFromInit(ctx, fileInfo, prop.initializer, methodName);
    }

    if (!tsapi.isIdentifier(callee.expression)) return null; // a.b.c(...) → skip
    const objName = callee.expression.text;
    const binding = resolveBinding(ctx, fileInfo, objName, 0);
    if (!binding) return null;

    // Cls.staticMethod(...).
    if (binding.kind === 'class') {
      const m = classMethod(tsapi, binding.node, methodName, /* static */ true);
      if (!m) return null;
      const clsName = binding.node.name ? binding.node.name.text : '<anon>';
      return {
        file: binding.file,
        body: m.body,
        classNode: binding.node,
        key: `${binding.file.path}#${clsName}.static.${methodName}`,
      };
    }

    // svc.method(...) where svc = new SvcClass(...) (the singleton idiom).
    if (binding.kind === 'var') {
      return resolveInstanceMethodFromInit(ctx, binding.file, binding.node, methodName);
    }
  }

  return null; // dynamic dispatch / computed / IIFE / namespaced → skip
}

// collectFromBody walks one body subtree: lowers every `throw new X(...)` and
// recurses into every resolvable call (bounded by MAX_DEPTH + visited).
function collectFromBody(ctx, fileInfo, classNode, body, out, depth, visited) {
  const tsapi = loadTS();

  const visit = (node) => {
    if (tsapi.isThrowStatement(node) && node.expression && tsapi.isNewExpression(node.expression)) {
      resolveThrownNew(ctx, fileInfo, node.expression, out);
    } else if (tsapi.isCallExpression(node)) {
      const target = resolveCallTarget(ctx, fileInfo, classNode, node);
      if (target && depth + 1 <= MAX_DEPTH && !visited.has(target.key)) {
        visited.add(target.key);
        collectFromBody(ctx, target.file, target.classNode, target.body, out, depth + 1, visited);
      }
    }
    tsapi.forEachChild(node, visit);
  };
  tsapi.forEachChild(body, visit);
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * analyzeThrows parses one controller source and infers, per route method, the
 * throwable error descriptors `[{name, code}]` (deduped by code).
 *
 * @param {string} sourceText      the controller's .ts source
 * @param {string} controllerPath  the controller's REAL path (relative imports
 *                                 resolve against its directory)
 * @param {{readFile: (p:string)=>string|null, fileExists:(p:string)=>boolean,
 *          projectRoot?: string}} deps
 * @returns {{className: string|null, ops: Record<string, Array<{name:string,code:string}>>}
 *          | {error: string}}
 *   `{error}` is returned ONLY for the defineError non-literal-args violation
 *   (the one hard error); every other unresolvable shape is skipped silently.
 */
function analyzeThrows(sourceText, controllerPath, deps) {
  const tsapi = loadTS();
  // PROJECT FILES ONLY: resolution is clamped under the project root — by
  // default the parent of the controllers/ dir; callers that know the real
  // root (the stager / dev-server) pass deps.projectRoot explicitly.
  const projectRoot =
    (deps && deps.projectRoot) || path.dirname(path.dirname(path.resolve(controllerPath)));
  const ctx = { deps, files: new Map(), projectRoot };
  const fileInfo = parseFile(ctx, controllerPath, sourceText);
  ctx.files.set(controllerPath, fileInfo);

  let className = null;
  const ops = {};

  try {
    for (const stmt of fileInfo.sf.statements) {
      if (!tsapi.isClassDeclaration(stmt)) continue;
      const decos = tsapi.getDecorators ? tsapi.getDecorators(stmt) || [] : [];
      const isController = decos.some((d) => {
        const ex = d.expression;
        const callee = tsapi.isCallExpression(ex) ? ex.expression : ex;
        return callee && callee.getText(fileInfo.sf) === 'Controller';
      });
      if (!isController) continue;
      className = stmt.name ? stmt.name.text : null;

      for (const m of stmt.members) {
        if (!tsapi.isMethodDeclaration(m) || !m.body) continue;
        const mdecos = tsapi.getDecorators ? tsapi.getDecorators(m) || [] : [];
        const isRoute = mdecos.some((d) => {
          const ex = d.expression;
          const callee = tsapi.isCallExpression(ex) ? ex.expression : ex;
          const n = callee && callee.getText(fileInfo.sf);
          return n === 'Get' || n === 'Post' || n === 'Put' || n === 'Patch' || n === 'Delete';
        });
        if (!isRoute) continue;

        const fnName = m.name.getText(fileInfo.sf);
        const out = [];
        const visited = new Set([`${controllerPath}#${className || '<anon>'}.${fnName}`]);
        collectFromBody(ctx, fileInfo, stmt, m.body, out, 0, visited);
        ops[fnName] = out;
      }
    }
  } catch (e) {
    if (e instanceof ThrowAnalysisError) return { error: e.message };
    throw e;
  }

  return { className, ops };
}

// buildThrowInjection renders the pinned recordThrows IIFEs — one per ANALYZED
// route method, INCLUDING empty sets. An explicit empty `recordThrows(.., [])`
// is what arms the runtime conformance guard for routes whose throws were all
// statically invisible (the routes most likely to be missing typing), and it
// distinguishes "analyzed, nothing visible" from "pre-analysis bundle". The
// IIFE shape is a cross-repo contract — the SDK (recordThrows), the worker
// guard, and `palbase serve` all key off it.
function buildThrowInjection(className, ops) {
  const fnNames = Object.keys(ops);
  if (!className || fnNames.length === 0) return '';
  const lines = [];
  lines.push('');
  lines.push('// --- AUTO-GENERATED: route throw bindings (inferred throw sites) ---');
  for (const fn of fnNames) {
    const descriptors = ops[fn]
      .map((d) => `{name:${JSON.stringify(d.name)},code:${JSON.stringify(d.code)}}`)
      .join(',');
    lines.push(';(function(){ try { var __pb = require("@palbase/backend");');
    lines.push(`  __pb.recordThrows(${className}.prototype, ${JSON.stringify(fn)}, [${descriptors}]);`);
    lines.push('} catch (e) {} })();');
  }
  lines.push('');
  return lines.join('\n');
}

/**
 * injectThrowBindings appends the recordThrows IIFEs to a controller's source.
 * Best-effort by design: any unexpected analysis failure returns the source
 * UNCHANGED (the routes just carry no `throws` — clients degrade to `.other`).
 * The ONE exception: the defineError non-literal-args violation rethrows as
 * ThrowAnalysisError so the stager fails the deploy loudly, naming the file.
 */
function injectThrowBindings(sourceText, controllerPath, deps) {
  let analyzed;
  try {
    analyzed = analyzeThrows(sourceText, controllerPath, deps);
  } catch {
    return sourceText; // best-effort — never fail the stage on an analyzer bug
  }
  if (analyzed.error) {
    throw new ThrowAnalysisError(analyzed.error);
  }
  const snippet = buildThrowInjection(analyzed.className, analyzed.ops);
  if (!snippet) return sourceText;
  return sourceText + '\n' + snippet + '\n';
}

module.exports = {
  ThrowAnalysisError,
  analyzeThrows,
  injectThrowBindings,
};
