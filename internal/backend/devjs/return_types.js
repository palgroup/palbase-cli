'use strict';

/**
 * Palbase Backend Runtime — Return-Type Schema Binder (source-`.ts` name reader)
 *
 * The response schema of a route is the method's RETURN TYPE, not a separate
 * `@Returns(...)` decorator (which was removed — clean cutover). For each route
 * method this reads the WRITTEN return-type annotation from the controller's
 * SOURCE `.ts` (types are erased by esbuild, so they only exist in source),
 * resolves the same-named exported zod const, and emits an injection snippet that
 * binds it onto the controller's route registry at module-eval time — so BOTH the
 * OpenAPI/codegen extractor AND the deployed pod's runtime output-validation see
 * the live zod (no esbuild plugin needed; the bind is plain top-level code in the
 * bundle).
 *
 * Convention (enforced — a violation is a HARD build error, never silent):
 *   - a body-returning route MUST return a NAMED zod-inferred type:
 *       foo(): Promise<TodoSchema>           → schema = TodoSchema
 *       foo(): Promise<TodoSchema[]>         → schema = z.array(TodoSchema)
 *       foo(): TodoSchema / TodoSchema[]     → (Promise optional)
 *   - `void` / `Promise<void>` / no annotation → no 200 body (allowed)
 *   - anything else (inline object, union, intersection, a type with no
 *     same-named exported zod) → HARD error naming <Controller>.<method>.
 *
 * This module is SHARED VERBATIM by the deploy extractor and `palbase build`
 * (the load-bearing serve==deploy parity rule). It uses the `typescript` PARSER
 * only (ts.createSourceFile) — no type-checker, no Program — so it's cheap and
 * needs only `typescript` on NODE_PATH.
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
  // TypeScript 7 is the Go-native compiler: its CommonJS entry exports only
  // { version, versionMajorMinor } — no createSourceFile, no ScriptTarget. Reading
  // `ts.ScriptTarget.ES2022` off it threw "Cannot read properties of undefined",
  // which told the user nothing. Check the surface we need and SAY what's wrong.
  if (typeof mod.createSourceFile !== 'function' || !mod.ScriptTarget) {
    throw new Error(TS_PARSER_HELP(
      'the resolved `typescript` (v' + (mod.version || 'unknown') + ') has no compiler API — ' +
        'TypeScript 7 ships the Go-native compiler, whose CommonJS build exposes version metadata only',
    ));
  }
  ts = mod;
  return ts;
}

// One actionable message for both parser-load failures. The CLI/pod normally puts
// its OWN pinned TypeScript 5 ahead of the project on NODE_PATH, so reaching this
// means that provisioning was skipped (offline first run) and the fallback — the
// project's typescript — is unusable.
function TS_PARSER_HELP(reason) {
  return (
    'palbase needs the TypeScript 5 compiler API to read controller return types, but ' +
    reason +
    '.\n  fix: npm install --save-dev typescript@5   (or re-run once online: the CLI installs its own pinned parser)'
  );
}

class ReturnTypeError extends Error {}

// Symbols MUST match the SDK (@palbase/backend src/decorators/registry.ts).
const ROUTES_SYMBOL_KEY = 'palbase.backend.routes';
const RETURN_BUFFER_SYMBOL_KEY = 'palbase.backend.returnBuffer';

/**
 * Parse one controller source file and return, per method, the resolved
 * return-type binding the injector needs.
 *
 * @param {string} sourceText  the controller's .ts source
 * @param {string} fileLabel   path/name for error messages
 * @returns {{ className: string, methods: Array<{ fnName, typeName, isArray }>, imports: Record<string,string> }}
 *   - methods: only routes that HAVE a resolvable named return type (void/none omitted)
 *   - imports: local-binding-name → module-specifier (for the injector's import)
 *   Throws ReturnTypeError on an un-resolvable/disallowed return type.
 */
function readReturnTypes(sourceText, fileLabel) {
  const tsapi = loadTS();
  const sf = tsapi.createSourceFile(
    fileLabel || 'controller.ts',
    sourceText,
    tsapi.ScriptTarget.ES2022,
    /* setParentNodes */ true,
  );

  // Map local binding name → module specifier, for named imports only.
  // `import { TodoSchema } from "../models/todo"`         → TodoSchema:"../models/todo"
  // `import { TodoSchema as T } from "../models/todo"`    → T:"../models/todo" (+ original)
  const imports = {};        // localName → specifier
  const importOriginal = {}; // localName → originalExportedName (for aliases)
  for (const stmt of sf.statements) {
    if (!tsapi.isImportDeclaration(stmt) || !stmt.importClause) continue;
    const spec = stmt.moduleSpecifier.text;
    const named = stmt.importClause.namedBindings;
    if (named && tsapi.isNamedImports(named)) {
      for (const el of named.elements) {
        const local = el.name.text;
        const original = el.propertyName ? el.propertyName.text : local;
        imports[local] = spec;
        importOriginal[local] = original;
      }
    }
  }

  let className = null;
  const methods = [];

  function err(method, msg) {
    return new ReturnTypeError(
      `${fileLabel || 'controller'} — ${className || '?'}.${method}: ${msg}`,
    );
  }

  // Unwrap Promise<T>, detect arrays, and require a single named type reference.
  // Returns { typeName, isArray } or null for void/undefined/no-annotation.
  function resolveType(typeNode, method) {
    if (!typeNode) return null; // no annotation → allowed (void-ish)

    // Promise<X> → unwrap
    if (
      tsapi.isTypeReferenceNode(typeNode) &&
      typeNode.typeName.getText(sf) === 'Promise' &&
      typeNode.typeArguments &&
      typeNode.typeArguments.length === 1
    ) {
      return resolveType(typeNode.typeArguments[0], method);
    }

    // void / undefined → no body
    if (
      typeNode.kind === tsapi.SyntaxKind.VoidKeyword ||
      typeNode.kind === tsapi.SyntaxKind.UndefinedKeyword
    ) {
      return null;
    }

    // X[]  → array of element
    if (tsapi.isArrayTypeNode(typeNode)) {
      const inner = resolveNamed(typeNode.elementType, method);
      return { typeName: inner, isArray: true };
    }

    // Array<X> → array of element
    if (
      tsapi.isTypeReferenceNode(typeNode) &&
      typeNode.typeName.getText(sf) === 'Array' &&
      typeNode.typeArguments &&
      typeNode.typeArguments.length === 1
    ) {
      const inner = resolveNamed(typeNode.typeArguments[0], method);
      return { typeName: inner, isArray: true };
    }

    // A single named type reference (no type args) → the schema name
    return { typeName: resolveNamed(typeNode, method), isArray: false };
  }

  // A node that resolves to a single schema NAME. Accepts either form (so the
  // author never has to write a separate `export type X` line):
  //   - a bare reference to the schema:        `TodoSchema`
  //   - the zod-inferred type of the schema:   `z.infer<typeof TodoSchema>`
  //     (and the std `z.output<...>` / `z.input<...>` aliases)
  // Anything else — inline object, union, intersection, literal, other generic —
  // is a hard error (name the schema in a model file).
  function resolveNamed(node, method) {
    // bare `TodoSchema`
    if (tsapi.isTypeReferenceNode(node) && !node.typeArguments) {
      return node.typeName.getText(sf);
    }
    // `z.infer<typeof TodoSchema>` (or z.output / z.input) → unwrap to TodoSchema
    if (tsapi.isTypeReferenceNode(node) && node.typeArguments && node.typeArguments.length === 1) {
      const head = node.typeName.getText(sf); // e.g. "z.infer" / "infer"
      if (/^(z\.)?(infer|output|input)$/.test(head)) {
        const arg = node.typeArguments[0];
        // arg is `typeof TodoSchema` — a TypeQuery whose exprName is the schema.
        if (tsapi.isTypeQueryNode(arg)) {
          return arg.exprName.getText(sf);
        }
        // tolerate a plain reference inside (rare): z.infer<TodoSchema>
        if (tsapi.isTypeReferenceNode(arg) && !arg.typeArguments) {
          return arg.typeName.getText(sf);
        }
      }
    }
    if (node.kind === tsapi.SyntaxKind.VoidKeyword) {
      throw err(method, 'void cannot appear inside an array/Promise here');
    }
    throw err(
      method,
      'return type must be a NAMED zod schema — write `Promise<z.infer<typeof TodoSchema>>` ' +
        '(or `TodoSchema` / `TodoSchema[]`); inline objects, unions, literals, and other ' +
        'generics are not allowed — name the schema in a model file',
    );
  }

  for (const stmt of sf.statements) {
    if (!tsapi.isClassDeclaration(stmt)) continue;
    // Only consider a class decorated with @Controller (the routing surface).
    const decos = tsapi.getDecorators ? tsapi.getDecorators(stmt) || [] : [];
    const isController = decos.some((d) => {
      const ex = d.expression;
      const callee = tsapi.isCallExpression(ex) ? ex.expression : ex;
      return callee && callee.getText(sf) === 'Controller';
    });
    if (!isController) continue;
    className = stmt.name ? stmt.name.text : null;

    for (const m of stmt.members) {
      if (!tsapi.isMethodDeclaration(m)) continue;
      const mdecos = tsapi.getDecorators ? tsapi.getDecorators(m) || [] : [];
      const isRoute = mdecos.some((d) => {
        const ex = d.expression;
        const callee = tsapi.isCallExpression(ex) ? ex.expression : ex;
        const n = callee && callee.getText(sf);
        return n === 'Get' || n === 'Post' || n === 'Put' || n === 'Patch' || n === 'Delete' || n === 'Query'
          // @Upload is a route like any other: on the wire it is a POST, and its
          // RETURN TYPE is the completion's response contract. Leaving it out here
          // meant an @Upload method's return type was never read, so the operation
          // shipped with NO 200 schema and every generated client got an EMPTY
          // response struct — the upload worked and the document it returns
          // (url, variants, caption) was untyped and unreachable from the app.
          || n === 'Upload';
      });
      if (!isRoute) continue;

      const fnName = m.name.getText(sf);
      // A route method MUST declare its return type explicitly — either a named
      // zod schema or `void`/`Promise<void>`. A missing annotation is the SAME
      // silent gap @Returns had (codegen would emit an untyped struct), so it's a
      // HARD error: the author must say what the route returns.
      if (!m.type) {
        throw err(
          fnName,
          'route method has no return type — annotate it (e.g. `: Promise<TodoSchema>`, ' +
            '`: TodoSchema[]`, or `: void` for no body)',
        );
      }
      const resolved = resolveType(m.type, fnName);
      if (!resolved) continue; // explicit void/Promise<void> — no 200 body, allowed

      const { typeName, isArray } = resolved;
      // The schema name must be an imported (or locally-defined) value. If it's
      // not imported, we can't bind it from the bundle — hard error.
      if (!(typeName in imports) && !localValueExists(sf, typeName, tsapi)) {
        throw err(
          fnName,
          `return type \`${typeName}\` has no matching imported/exported zod schema in scope — ` +
            `export \`const ${typeName} = z.object(...)\` and import it`,
        );
      }
      methods.push({ fnName, typeName, isArray });
    }
  }

  return { className, methods, imports, importOriginal };
}

// A schema declared in the controller file itself (rare) — `const X = z...`.
function localValueExists(sf, name, tsapi) {
  for (const stmt of sf.statements) {
    if (tsapi.isVariableStatement(stmt)) {
      for (const d of stmt.declarationList.declarations) {
        if (d.name.getText(sf) === name) return true;
      }
    }
  }
  return false;
}

/**
 * Build the JS injection snippet appended to a controller's source (or its
 * generated sibling) BEFORE esbuild bundles it. It writes each method's
 * return-schema onto the controller class's route registry (the same
 * ROUTES/returnBuffer symbols the SDK uses), wrapping arrays in z.array.
 *
 * The snippet imports nothing new beyond `z` and the schema consts (which the
 * controller already imports, so they're already in the bundle — referencing
 * them here keeps esbuild from tree-shaking them).
 *
 * @returns {string} the snippet, or '' when there's nothing to bind.
 */
function buildInjection(parsed) {
  if (!parsed.className || parsed.methods.length === 0) return '';
  const cls = parsed.className;
  const lines = [];
  lines.push('');
  lines.push('// --- AUTO-GENERATED: route return-schema bindings (from return types) ---');
  lines.push('(() => {');
  lines.push(`  const __ROUTES = Symbol.for(${JSON.stringify(ROUTES_SYMBOL_KEY)});`);
  lines.push(`  const __RBUF = Symbol.for(${JSON.stringify(RETURN_BUFFER_SYMBOL_KEY)});`);
  lines.push(`  const __ctor = ${cls};`);
  lines.push('  const __bind = (fn, schema) => {');
  lines.push('    const routes = __ctor[__ROUTES];');
  lines.push('    const r = routes && routes.find((x) => x.fnName === fn);');
  lines.push('    if (r) { r.returnSchema = schema; return; }');
  lines.push('    if (!__ctor[__RBUF]) __ctor[__RBUF] = {};');
  lines.push('    __ctor[__RBUF][fn] = schema;');
  lines.push('  };');
  for (const m of parsed.methods) {
    const expr = m.isArray ? `z.array(${m.typeName})` : m.typeName;
    lines.push(`  __bind(${JSON.stringify(m.fnName)}, ${expr});`);
  }
  lines.push('})();');
  lines.push('');
  return lines.join('\n');
}

/**
 * Append the return-schema injection to a controller's source text. Ensures `z`
 * is in scope (controllers already import it from @palbase/backend; if not, we
 * add the import). Pure string transform — the caller writes the result to the
 * file esbuild will bundle.
 */
function injectReturnBindings(sourceText, fileLabel) {
  const parsed = readReturnTypes(sourceText, fileLabel);
  const snippet = buildInjection(parsed);
  if (!snippet) return sourceText;
  let out = sourceText;
  // Ensure `z` is importable for z.array(...) wrapping.
  if (!/\bimport\b[^\n]*\bz\b[^\n]*@palbase\/backend/.test(sourceText) &&
      !/\bimport\b[^\n]*\{[^}]*\bz\b[^}]*\}/.test(sourceText)) {
    out = `import { z } from "@palbase/backend";\n` + out;
  }
  return out + '\n' + snippet + '\n';
}

module.exports = {
  ReturnTypeError,
  readReturnTypes,
  buildInjection,
  injectReturnBindings,
};
