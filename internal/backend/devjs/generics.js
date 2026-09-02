'use strict';

/**
 * Refuses constructor parameters the container cannot resolve HONESTLY, at the
 * only place they are still visible: the source.
 *
 * Two shapes qualify, and both were MEASURED rather than assumed:
 *
 *   Repo<A>   `emitDecoratorMetadata` erases the type arguments — `Repo<A>` and
 *             `Repo<B>` both arrive as `Repo`. The container would hand the same
 *             instance to both and nothing at runtime could tell that was wrong.
 *
 *   A | null  The two transpilers this project depends on DISAGREE. Bun narrows
 *             it to `A`, SWC emits `Object`. The container accepts the first and
 *             refuses the second — so the SDK's own tests would certify a
 *             behaviour the tenant does not get. Cut at the root instead: reject
 *             the shape, and the disagreement can never be reached.
 *
 * WHY A TREE WALK AND NOT THE STAGING LOOP. The stager is handed
 * `<project>/controllers` (stack_bundle.go) and stages that directory alone.
 * Bolting this check to that loop would leave every service unexamined — and a
 * service is where an injectable class normally lives. So the check reads every
 * source file.
 *
 * WHICH CLASSES IT SPEAKS ABOUT — and this was narrowed after a real project
 * was refused for a class nothing injects. `GooglePlacesError extends Error`,
 * constructed by hand with `new GooglePlacesError("HTTP", …)`, was rejected for
 * a union parameter it is entitled to have. The rule is not "every class": it is
 * every class the container can CONSTRUCT, and that set is decided by a
 * mechanism, not by a naming convention. `emitDecoratorMetadata` writes
 * `design:paramtypes` onto a class only when the class carries a class
 * decorator. Without it there are no emitted parameter types, so the container
 * cannot inject the class and the Bun/SWC disagreement is unreachable. A
 * decorated class is therefore exactly the set at risk — and reading the
 * decorator's PRESENCE rather than its NAME keeps the check honest when
 * `import { Injectable as Inj }` renames it.
 *
 * A class listed in a module's `providers` without a decorator is not a hole:
 * the container refuses it on its own, naming the missing metadata.
 */

const fs = require('node:fs');
const path = require('node:path');

let tsapi = null;

function ts() {
  if (tsapi) return tsapi;
  let mod;
  try {
    mod = require('typescript');
  } catch (e) {
    throw new Error(
      'generics: the `typescript` package could not be loaded (' +
        e.message +
        '). It is the parser this check reads your constructors with.',
    );
  }
  // Same guard return_types.js carries, for the same reason: TypeScript 7 is the
  // Go-native compiler and its CommonJS entry exports version metadata only, so
  // `ts.ScriptTarget.Latest` would throw "Cannot read properties of undefined"
  // and tell the reader nothing.
  if (typeof mod.createSourceFile !== 'function' || !mod.ScriptTarget) {
    throw new Error(
      'generics: the resolved `typescript` (v' +
        (mod.version || 'unknown') +
        ') has no compiler API — TypeScript 7 ships the Go-native compiler, whose ' +
        'CommonJS build exposes version metadata only.',
    );
  }
  tsapi = mod;
  return tsapi;
}

function messageFor(relPath, owner, index, typeText, kind) {
  const why =
    kind === 'generic'
      ? 'Generic dependencies cannot be injected: decorator metadata erases the type ' +
        'arguments, so Repo<A> and Repo<B> resolve to the SAME instance and nothing at ' +
        'runtime can tell that apart.'
      : 'Union and intersection dependencies cannot be injected: transpilers DISAGREE on ' +
        'them. Measured — Bun emits the class for `A | null`, SWC emits Object. The same ' +
        'code would behave one way in tests and another in production.';

  const fixes =
    kind === 'generic'
      ? '  - depend on a concrete non-generic class\n' +
        '  - or name the specialisation: class UserRepo extends Repo<User> {}'
      : '  - depend on exactly one class\n' +
        '  - if the dependency is genuinely optional, give it a null-object implementation';

  return (
    relPath +
    ': ' +
    owner +
    ' constructor, parameter ' +
    index +
    ' is typed `' +
    typeText +
    '`.\n' +
    why +
    '\n\nPotential solutions:\n' +
    fixes
  );
}

/** Checks ONE file's source. Throws on the first violation, naming it. */
function assertNoGenericDeps(source, relPath) {
  const t = ts();
  const sf = t.createSourceFile(relPath, source, t.ScriptTarget.Latest, true);

  /**
   * Does this class carry a class decorator? That is the emitter's own
   * condition for writing `design:paramtypes`, so it is the condition for the
   * container being able to construct the class at all.
   */
  const isDecorated = (cls) => {
    if (!cls) return false;
    const decorators =
      typeof t.canHaveDecorators === 'function' && t.canHaveDecorators(cls)
        ? t.getDecorators(cls)
        : cls.decorators;
    return Array.isArray(decorators) && decorators.length > 0;
  };

  const visit = (node) => {
    if (t.isConstructorDeclaration(node) && isDecorated(node.parent)) {
      const owner =
        node.parent && node.parent.name && node.parent.name.text
          ? node.parent.name.text
          : '<anonymous>';
      node.parameters.forEach((p, i) => {
        const ty = p.type;
        if (!ty) return;
        const isGeneric =
          t.isTypeReferenceNode(ty) && ty.typeArguments && ty.typeArguments.length > 0;
        const isUnion = t.isUnionTypeNode(ty) || t.isIntersectionTypeNode(ty);
        if (isGeneric || isUnion) {
          throw new Error(
            messageFor(relPath, owner, i, ty.getText(sf), isGeneric ? 'generic' : 'union'),
          );
        }
      });
    }
    t.forEachChild(node, visit);
  };

  t.forEachChild(sf, visit);
}

/**
 * Directories that are never a tenant's source.
 *
 * `.palbase-staged-controllers` and `.palbase` hold machine-written copies of
 * code that was already checked; walking them would report the same violation
 * twice, from a path the author does not recognise.
 */
const SKIP = new Set([
  'node_modules',
  'dist',
  'build',
  '.git',
  '.palbase',
  '.palbase-staged-controllers',
  '.palbase-build-controllers',
]);

function sources(dir, root, out) {
  let entries;
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const entry of entries) {
    if (entry.name.startsWith('.') && entry.name !== '.') {
      if (SKIP.has(entry.name)) continue;
    }
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (SKIP.has(entry.name)) continue;
      sources(full, root, out);
    } else if (/\.(c?ts|tsx)$/i.test(entry.name) && !/\.d\.ts$/i.test(entry.name)) {
      out.push(full);
    }
  }
  return out;
}

/**
 * Checks every source file under `projectRoot`.
 *
 * Returns HOW MANY files were examined, deliberately. A walk whose filter is
 * wrong finds nothing and reports success — the count is what separates
 * "checked and clean" from "never looked", and the caller prints it.
 */
function assertNoGenericDepsInTree(projectRoot) {
  const files = sources(projectRoot, projectRoot, []);
  for (const file of files) {
    assertNoGenericDeps(fs.readFileSync(file, 'utf8'), path.relative(projectRoot, file));
  }
  return files.length;
}

module.exports = { assertNoGenericDeps, assertNoGenericDepsInTree };
