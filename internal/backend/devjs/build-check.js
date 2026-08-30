#!/usr/bin/env node
/**
 * `palbase build` — one-shot pre-deploy validation of the local tree.
 *
 * Stages controllers/ (return-type + throw bindings injected), esbuild-bundles
 * them EXACTLY the way the deploy does, then runs the deploy's own
 * extract_meta.js over every bundled controller. Exits 0 when a deploy of this
 * tree would activate cleanly, 1 when it would be rejected — or would activate
 * with ZERO endpoints, which the server reports as a successful deploy. That
 * silent class is the reason this runner exists: `deploy successful` is written
 * even when controller metadata extraction collapses, and the endpoints simply
 * vanish. Here it is a loud, local, exit-1 failure before the push.
 *
 * This is NOT an emulator. The scripts it drives — return_types.js,
 * throw_analysis.js, tx_analysis.js, extract_meta.js — are byte-identical
 * copies of the deploy runtime's own (modules/backend/internal/runtime/*).
 * Neither submodule's CI can see the other, so the parent repo's
 * .github/workflows/stager-copy-parity.yml diffs them and fails CI the
 * moment a copy drifts (there is no Go test for this in this repo). What
 * passes here is what the deploy accepts, by construction rather than by
 * resemblance.
 *
 * Invocation (set by the Go CLI):
 *   PALBASE_DEV_ROOT=/abs/path PALBASE_RUNTIME_MODULES=/abs/node_modules node build-check.js
 */
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const { execFileSync } = require('child_process');

const PROJECT_ROOT = process.env.PALBASE_DEV_ROOT || process.cwd();

// Where @palbase/backend actually lives, which is NOT always inside PROJECT_ROOT.
// `palbase build` stages the tree the DEPLOY would receive (node_modules stripped,
// exactly like the push tarball) so esbuild can't resolve a bare third-party import
// the deploy would fail on. The metadata extractor still has to require()
// @palbase/backend, and on the pod that comes from the runtime's own global install
// — not the user's tree. This env var is that "global install" for the local run.
const RUNTIME_MODULES = process.env.PALBASE_RUNTIME_MODULES || path.join(PROJECT_ROOT, 'node_modules');
const CONTROLLERS_DIR = path.join(PROJECT_ROOT, 'controllers');
const RESOURCES_DIR = path.join(PROJECT_ROOT, 'resources');
// services/ is scanned directly (not just as a controller's transitive
// import) for the SAME reason SOURCE — not bundled — .ts is what
// scanTxPlanViolations() needs below: a Database.transaction() plan is
// project business logic and, per the repo convention, as likely to live in
// services/*.service.ts as directly in a controller method.
const SERVICES_DIR = path.join(PROJECT_ROOT, 'services');

// ── esbuild bundling — IDENTICAL to the deploy path ────────────────────────
//
// We do NOT require() the raw controllers/*.ts. Node's loader can't resolve the
// v2 canonical EXTENSIONLESS relative imports (`import x from "../handlers/foo"`,
// `"../../services/bar.service"`) — those need a bundler. The deployed pod
// esbuild-bundles each controllers/* (and resources/*) file as its own entry
// point with the import graph (handlers/, services/, …) inlined and
// @palbase/backend kept external (modules/backend internal/deploy/bundler.go +
// bundler_config.go). We mirror that EXACTLY here, so a bundle that fails to
// build here is a deploy that fails to build there.
//
// Each src dir is bundled into BUNDLE_ROOT/<name>/ preserving the source tree
// (esbuild --outbase), so controllers/todos.controller.ts →
// BUNDLE_ROOT/controllers/todos.controller.js. We then discover + require() the
// BUNDLED .js (CJS), never the raw .ts.
const BUNDLE_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'palbase-build-bundle-'));
const BUNDLED_CONTROLLERS_DIR = path.join(BUNDLE_ROOT, 'controllers');
const BUNDLED_RESOURCES_DIR = path.join(BUNDLE_ROOT, 'resources');
// Staging tree for controllers with return-schema bindings injected, BEFORE
// esbuild. Keeps the user's source untouched. MUST be a sibling of
// PROJECT_ROOT/controllers so a staged controller's `../models` / `../services`
// relative imports resolve to the real project dirs (the same depth as the
// original). Cleaned up on exit.
const STAGED_CONTROLLERS_DIR = path.join(PROJECT_ROOT, '.palbase-build-controllers');

// A {"type":"commonjs"} marker in BUNDLE_ROOT pins our --format=cjs `.js`
// bundles to CommonJS regardless of the project's package.json "type" — the
// same marker the deploy bundler drops in .palbase/ so require() of a bundle
// never throws ERR_REQUIRE_ESM (bundler.go's CommonJS-marker note).
fs.writeFileSync(path.join(BUNDLE_ROOT, 'package.json'), '{"type":"commonjs"}\n');

// The esbuild externals + resolve-extensions match the deploy bundler
// (bundler_config.go DefaultBundleConfig + buildArgs): @palbase/backend stays
// external so the bundle's `import { Database }` resolves to the project's ONE
// installed instance, and .ts is added to the resolve set so a `.js`-spelled
// import of a `.ts` source (the TS-idiomatic ESM form) still resolves.
const ESBUILD_EXTERNAL = '@palbase/backend';
const ESBUILD_RESOLVE_EXTENSIONS = '.ts,.tsx,.js,.jsx,.json';

// Extra externals for the CONTROLLERS bundle ONLY — the project's OWN
// resources/* relative imports stay external, mirroring the deploy bundler's
// ExternalResourceImports (modules/backend internal/deploy/bundler.go +
// bundler_config.go). esbuild matches a `*`-external against the import
// specifier AS WRITTEN and permits exactly ONE `*`, so one glob per relative
// depth a controller can sit at (same fixed 3-deep set as the pod). resources/
// bundles do NOT get these (a resource is never externalised against its own
// tree — pod parity).
const CONTROLLER_RESOURCE_EXTERNALS = ['../resources/*', '../../resources/*', '../../../resources/*'];

// Return-type → response-schema binder (shared VERBATIM with the deploy
// extractor: modules/backend/internal/runtime/return_types.js). Reads each
// controller's method RETURN TYPE from source and injects the matching zod
// binding (the @Returns replacement). Required — a missing/disallowed return
// type is a HARD error here, exactly as on deploy.
const returnTypes = require('./return_types.js');
// Shared throw-site analyzer — VERBATIM-identical to the deploy copy
// (modules/backend/internal/runtime/throw_analysis.js), the same parity rule
// as return_types.js: build and deploy must infer identical error sets.
const throwAnalysis = require('./throw_analysis.js');
// TxPlan Ref-truthiness build gate — VERBATIM-identical to the deploy copy
// (modules/backend/internal/runtime/tx_analysis.js), same parity rule. Unlike
// its two siblings above (return-schema binding, throw inference — both DX/
// completeness passes), a miss here is silent DATA CORRUPTION: see the
// module's own header for why `if (ref)` can never be trapped and must be
// caught statically instead. scanTxPlanViolations() below runs it and is
// NEVER merged into the softer `failures` list main() builds — it exits
// straight away.
const txAnalysis = require('./tx_analysis.js');

// SRC_EXT_BY_STEM is the staging manifest: project-relative stem
// ("controllers/todos.controller") → the extension the AUTHOR wrote (".ts").
//
// esbuild emits `.js` beside every source it bundles, so past the stager the
// original extension is simply gone — and every path this runner reports is
// derived from the bundled tree. Recording the correspondence at the one moment
// both trees are in hand is cheaper and more honest than guessing later.
//
// `null` means AMBIGUOUS: two sources sharing a stem (todos.controller.ts AND
// todos.controller.js) map onto one bundled file, so there is no single source
// to name and the fail-safe applies. Naming the wrong one of two real files is
// worse than naming neither.
const SRC_EXT_BY_STEM = new Map();

// recordStagedSources fills the manifest from the source tree, BEFORE any
// injection runs — a stage that throws on a bad return type has still recorded
// what it walked, so the error it reports can name the file the author opens.
function recordStagedSources(srcDir) {
  for (const file of walk(srcDir)) {
    const stem = withoutExtension(path.join('controllers', path.relative(srcDir, file)));
    const ext = path.extname(file);
    if (SRC_EXT_BY_STEM.has(stem) && SRC_EXT_BY_STEM.get(stem) !== ext) {
      SRC_EXT_BY_STEM.set(stem, null);
      continue;
    }
    SRC_EXT_BY_STEM.set(stem, ext);
  }
}

// stageControllersWithReturnBindings copies controllers/*.ts into a staging dir,
// appending each file's return-type schema injection, and returns the staging
// dir for esbuild to bundle. The user's source is never modified. A return-type
// violation throws (caught by registerControllers and surfaced loudly), matching
// the deploy behavior.
//
// It also records the staging manifest bundledToSrcRel reads, because this is
// the only place that sees the source tree and the staged tree at once.
function stageControllersWithReturnBindings(srcDir, stageDir) {
  rmBundledTree(stageDir);
  fs.mkdirSync(stageDir, { recursive: true });
  recordStagedSources(srcDir);
  for (const file of walk(srcDir)) {
    const rel = path.relative(srcDir, file);
    const dest = path.join(stageDir, rel);
    fs.mkdirSync(path.dirname(dest), { recursive: true });
    // Only controllers carry route methods; inject into *.controller.ts only,
    // copy everything else (imported services/models) verbatim so relative
    // imports still resolve from the staging tree.
    if (/\.controller\.(c?ts|tsx)$/i.test(path.basename(file))) {
      const src = fs.readFileSync(file, 'utf8');
      let out = returnTypes.injectReturnBindings(src, rel);
      // Throw inference runs AFTER return bindings, against the controller's
      // REAL path so `../services` / `../models` imports resolve in the real
      // project tree (mirrors the deploy stager). Only the defineError
      // non-literal violation can throw (surfaced loudly like a return-type
      // violation).
      out = throwAnalysis.injectThrowBindings(out, file, {
        readFile: (p) => {
          try { return fs.readFileSync(p, 'utf8'); } catch { return null; }
        },
        fileExists: (p) => fs.existsSync(p),
        projectRoot: PROJECT_ROOT,
      });
      fs.writeFileSync(dest, out);
    } else {
      fs.copyFileSync(file, dest);
    }
  }
  return stageDir;
}

// bundleSrcDir esbuild-bundles every entry file under srcDir into outDir,
// preserving the relative tree (--outbase=srcDir). One esbuild invocation for
// the whole dir, exactly like the deploy bundler's per-entry-dir Bundle().
// `externals` appends extra --external globs on top of the always-external
// @palbase/backend (the controllers call passes CONTROLLER_RESOURCE_EXTERNALS;
// resources/ passes nothing). Returns true when at least one entry file was
// bundled; false when srcDir is absent or empty (a clean no-op). Throws on an
// esbuild error so a syntax error surfaces loudly rather than silently
// registering 0 routes.
// `entryFilter`, when given, narrows the entry set further (controllers pass the
// `.controller.` rule so a sibling file is only ever pulled in through a
// controller's import graph — exactly how the deploy bundler treats it).
function bundleSrcDir(srcDir, outDir, externals = [], entryFilter = null) {
  if (!fs.existsSync(srcDir)) return false;
  const entries = walk(srcDir)
    .filter((f) => /\.(c?js|mjs|tsx?|jsx)$/i.test(path.basename(f)))
    .filter((f) => (entryFilter ? entryFilter.test(path.basename(f)) : true));
  if (entries.length === 0) return false;

  fs.mkdirSync(outDir, { recursive: true });
  const args = [
    '--yes', 'esbuild',
    '--bundle',
    '--platform=node',
    '--format=cjs',
    '--target=es2022',
    // Preserve `class.name` through bundling — prod parity with the deploy
    // bundler (modules/backend internal/deploy/bundler.go, same flag). The
    // dotted operationId namespace derives from the live Ctrl.name; when a
    // controller class and an imported service class share a name, esbuild's
    // scope hoisting renames the controller's binding (TodosController →
    // TodosController2). Not a tunable — a correctness requirement, always on.
    '--keep-names',
    `--outdir=${outDir}`,
    `--outbase=${srcDir}`,
    `--resolve-extensions=${ESBUILD_RESOLVE_EXTENSIONS}`,
    `--external:${ESBUILD_EXTERNAL}`,
    ...externals.map((e) => `--external:${e}`),
    ...entries,
  ];
  // Run from PROJECT_ROOT so node_modules resolution (for any non-external dep
  // a handler/service imports) + relative imports anchor to the project. esbuild
  // is resolved via `npx --yes` — the same way the CLI's env-gen bundling shells
  // out (bundleSchemaTS), so no separate install is required.
  // NODE_PATH is PROJECT_ROOT/node_modules and must STAY that — do NOT "unify" it
  // with RUNTIME_MODULES. esbuild honours NODE_PATH, so pointing it at the real
  // node_modules lets a bare `import "zod"` resolve here while the deploy (which
  // bundles from a node_modules-free tarball) fails on it — the exact false-green
  // `palbase build` used to print. Under `palbase build` this path does not exist,
  // which is what makes the local bundle match the deploy.
  execFileSync('npx', args, {
    cwd: PROJECT_ROOT,
    env: Object.assign({}, process.env, { NODE_PATH: path.join(PROJECT_ROOT, 'node_modules') }),
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  return true;
}

// rmBundledTree removes a previously-emitted bundle dir so a rename/delete in
// the source tree doesn't leave a stale bundled `.js` behind. Best-effort.
function rmBundledTree(outDir) {
  try { fs.rmSync(outDir, { recursive: true, force: true }); } catch { /* ignore */ }
}

// The `.controller.` suffix is the ONLY routing convention. The deploy's own
// discoverControllerEntries (modules/backend/internal/deploy/bundler.go) keys on
// exactly this and nothing else under controllers/ is ever an entry point there
// — anything else is reached only if a controller imports it.
//
// Matching that here is not a convenience, it is the parity this whole file
// exists to provide. A broader scan made `palbase build` reject trees the deploy
// accepts: a `controllers/tenancy.test.js` was require()d + meta-extracted, its
// top-level code threw, and the build printed
// "✗ DEPLOY WOULD FAIL: controllers/tenancy.test.js — Invalid URL" about a file
// no deploy would ever load.
const CONTROLLER_ENTRY_RE = /\.controller\.(c?ts|tsx|c?js|mjs)$/i;
// Bundled output is always plain JS (esbuild), so the bundled-side twin of the
// rule above drops the TypeScript extensions.
const BUNDLED_CONTROLLER_RE = /\.controller\.(c?js|mjs)$/i;
// Every source extension the controllers/ tree may legitimately contain. Used
// only to answer "does this directory hold source at all" when no controller
// entry was found — the difference between an empty directory (fine) and a
// directory whose files register nothing (a deploy that serves zero endpoints).
const CONTROLLER_SOURCE_RE = /\.(c?js|mjs|tsx?|jsx)$/i;

// ── route table ────────────────────────────────────────────────────────
//
// controllers/ is the ONLY routing surface. Each controllers/*.ts file
// default-exports a @Controller CLASS. The route registry + controller meta +
// return buffer live on Symbols stamped on the class. We read the registry the
// SAME way the runtime does (readControllerRoutes), so a controller that
// registers zero routes here registers zero routes on deploy.
const routes = new Map();

// Class-controller registry symbols — MUST match the SDK
// (@palbase/backend src/decorators/registry.ts). The runtime reads the SAME
// symbols (RETURN_BUFFER_SYMBOL + ROUTES_SYMBOL) — keep them in lockstep.
const ROUTES_SYMBOL = Symbol.for('palbase.backend.routes');

// registeredControllers reads the SDK's own registry — the same function the
// runtime calls. Empty when the SDK is older than the registry or cannot be
// resolved, in which case the default-export path below still answers.
function registeredControllers() {
  try {
    const sdk = require('@palbase/backend');
    return typeof sdk.getRegisteredControllers === 'function'
      ? sdk.getRegisteredControllers()
      : [];
  } catch {
    return [];
  }
}
const RETURN_BUFFER_SYMBOL = Symbol.for('palbase.backend.returnBuffer');
const CONTROLLER_META_SYMBOL = Symbol.for('palbase.backend.controllerMeta');

// loadControllerClass require()s a bundled controller file and returns the
// @Controller CLASS it carries.
//
// THE REGISTRY FIRST, the default export second — the order the runtime uses.
// `@Controller` records the class as it decorates it, so a controller file needs
// no export at all, and the SDK stopped requiring one. This read did not follow:
// it insisted on a default export and rejected every file written the current
// way, including this repository's own fixture, with "controllers bundle must
// default-export a @Controller class" for a project `palbase push` deploys
// happily. A local gate that refuses what the deploy accepts is worse than no
// gate — it teaches people to stop running it.
function loadControllerClass(controllerPath) {
  delete require.cache[require.resolve(controllerPath)];

  // What the SDK had registered BEFORE this file ran, so the diff below is what
  // THIS file registered rather than everything loaded so far.
  const before = new Set(registeredControllers());
  const mod = require(controllerPath);
  const registered = registeredControllers().filter((c) => !before.has(c));
  const Ctrl = registered.length > 0 ? registered[registered.length - 1] : (mod.default ?? mod);
  if (typeof Ctrl !== 'function' || Ctrl.__palbase !== 'controller') {
    const shown = controllerPath.startsWith(BUNDLED_CONTROLLERS_DIR)
      ? bundledToSrcRel(controllerPath)
      : path.relative(PROJECT_ROOT, controllerPath);
    throw new Error(
      `${shown} must default-export a @Controller class ` +
      `(a controllers/* file decorated with @Controller); got ` +
      (Ctrl && Ctrl.__palbase ? `__palbase=${JSON.stringify(Ctrl.__palbase)}` : 'a non-controller export'),
    );
  }
  return Ctrl;
}

// runtimeModule resolves @palbase/backend best-effort (cached). The real project
// always has it installed (it's the import); but we never let a resolve failure
// crash the registry read — we fall straight to the raw-symbol path, which is the
// contract's documented fallback. Returns null when the package isn't resolvable.
let runtimeModuleCache; // undefined = not tried, null = absent, obj = loaded
function runtimeModule() {
  if (runtimeModuleCache !== undefined) return runtimeModuleCache;
  try {
    runtimeModuleCache = require('@palbase/backend');
  } catch {
    runtimeModuleCache = null;
  }
  return runtimeModuleCache;
}

// readControllerRoutes returns the route list for a @Controller class. Mirrors
// the runtime: prefer the SDK's own getRoutes() (an Array<RouteMeta> with the
// return buffer merged) when the installed SDK exports it, else read the raw
// ROUTES_SYMBOL array and merge the returnBuffer ourselves. Returns a fresh
// array; never mutates the registry's own array.
function readControllerRoutes(Ctrl) {
  const runtime = runtimeModule();
  if (runtime && typeof runtime.getRoutes === 'function') {
    try {
      const list = runtime.getRoutes(Ctrl);
      if (Array.isArray(list)) return list;
    } catch {
      // fall through to the raw-symbol path
    }
  }
  const raw = Ctrl[ROUTES_SYMBOL];
  const list = Array.isArray(raw) ? raw.slice() : [];
  const returnBuffer = Ctrl[RETURN_BUFFER_SYMBOL];
  if (returnBuffer) {
    return list.map((route) => {
      if (route && route.returnSchema === undefined && returnBuffer[route.fnName] !== undefined) {
        return Object.assign({}, route, { returnSchema: returnBuffer[route.fnName] });
      }
      return route;
    });
  }
  return list;
}

// readControllerMeta returns the @Controller meta `{ __palbase, basePath,
// defaultAuth? }`. Mirrors the runtime's contract: prefer the SDK's
// resolveController() when exported, else read the raw CONTROLLER_META_SYMBOL.
function readControllerMeta(Ctrl) {
  const runtime = runtimeModule();
  if (runtime && typeof runtime.resolveController === 'function') {
    try {
      const meta = runtime.resolveController(Ctrl);
      if (meta && typeof meta === 'object') return meta;
    } catch {
      // fall through to the raw-symbol path
    }
  }
  const meta = Ctrl[CONTROLLER_META_SYMBOL];
  return (meta && typeof meta === 'object') ? meta : { __palbase: 'controller', basePath: '' };
}

// urlToRegex compiles a `/todos/{id}`-style pattern into { regex, paramNames }.
// Class controllers (@Get("/{id}")) emit path params in BRACE form `{param}` —
// that's the one true form the route registry uses.
function urlToRegex(urlPattern) {
  const segments = urlPattern.split('/').filter((s) => s !== '');
  const paramNames = [];
  const body = segments.map((s) => {
    const m = /^\{(.+)\}$/.exec(s);
    if (m) {
      paramNames.push(m[1]);
      return '([^/]+)';
    }
    return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  }).join('/');
  const regexSrc = '^/' + body + '/?$';
  return { regex: new RegExp(regexSrc), paramNames };
}

// joinPath composes basePath + subPath exactly as the SDK does (a plain string
// concat), then normalises duplicate / leading slashes so "/places" + "/" →
// "/places", "/places" + "/import" → "/places/import", "" + "/x" → "/x".
function joinPath(basePath, subPath) {
  const raw = `${basePath || ''}${subPath || ''}`;
  if (raw === '') return '/';
  const collapsed = ('/' + raw).replace(/\/{2,}/g, '/');
  // Drop a trailing slash (but keep the root "/").
  return collapsed.length > 1 ? collapsed.replace(/\/$/, '') : collapsed;
}

// deriveControllerName lowercases the class name minus its "Controller" suffix:
// Members → "members". Returns "" when the derivation is empty (a class named
// exactly "Controller", or an anonymous class). MUST stay byte-for-byte
// identical to deriveControllerName in the prod extractor (modules/backend
// internal/runtime/extract_meta.js) and @palbase/backend's openapi/discover.ts
// — the three spec twins.
function deriveControllerName(Ctrl) {
  let name = typeof Ctrl === 'function' && typeof Ctrl.name === 'string' ? Ctrl.name : '';
  if (name.endsWith('Controller')) name = name.slice(0, -'Controller'.length);
  if (name === '') return '';
  return name.charAt(0).toLowerCase() + name.slice(1);
}

// registerControllers stages, bundles and loads every controller, filling the
// route table. Returns { sawControllerFiles, staleSDKSignature, routeCount,
// skipped[], buildError? } — a skipped controller or a buildError is a deploy
// that would be rejected (or activate empty), which main() turns into exit 1.
function registerControllers() {
  routes.clear();
  const skipped = []; // {file, error} — a controller that failed to load
  if (!fs.existsSync(CONTROLLERS_DIR)) {
    log(`controllers/ not found at ${CONTROLLERS_DIR}`);
    return { sawControllerFiles: false, staleSDKSignature: false, routeCount: 0, skipped };
  }

  // esbuild-bundle controllers/*.ts (+ their transitively-imported handlers/
  // and services/) into BUNDLED_CONTROLLERS_DIR. We discover/require the BUNDLED
  // CJS, so extensionless relative imports resolve exactly as they do on deploy.
  rmBundledTree(BUNDLED_CONTROLLERS_DIR);
  try {
    // Inject return-type → response-schema bindings into a staging copy, then
    // bundle THAT. A return-type violation (missing annotation, inline/union
    // type, unimported schema) throws here and is surfaced loudly below.
    const staged = stageControllersWithReturnBindings(CONTROLLERS_DIR, STAGED_CONTROLLERS_DIR);
    // Controllers keep their `../resources/*` imports EXTERNAL so they resolve
    // to the shared BUNDLE_ROOT/resources/ copy (bundled by bundleResources
    // BEFORE this runs — main() ordering). Deploy parity: bundler.go sets
    // ExternalResourceImports only for the controllers bundle.
    bundleSrcDir(staged, BUNDLED_CONTROLLERS_DIR, CONTROLLER_RESOURCE_EXTERNALS, CONTROLLER_ENTRY_RE);
  } catch (err) {
    // A bundle error (syntax error, unresolved import) OR a return-type
    // violation must be LOUD — otherwise the dir scan below finds nothing and
    // silently registers 0 routes.
    const msg = err instanceof returnTypes.ReturnTypeError ? err.message : esbuildErr(err);
    log(`controllers/ build failed — ${msg}`);
    return { sawControllerFiles: false, staleSDKSignature: false, routeCount: 0, skipped, buildError: msg };
  }

  if (!fs.existsSync(BUNDLED_CONTROLLERS_DIR)) {
    // Nothing bundled means no *.controller.ts entry existed. Two very different
    // situations hide behind that: an empty controllers/ (a project with no
    // endpoints yet — fine), and a controllers/ full of source that registers
    // NOTHING, which deploys as a success serving zero endpoints. Narrowing the
    // scan to `.controller.` must not cost us the second signal.
    const sourceFiles = walk(CONTROLLERS_DIR).filter((f) => CONTROLLER_SOURCE_RE.test(path.basename(f)));
    if (sourceFiles.length > 0) {
      const err = 'only *.controller.ts files register routes; controllers/ has source files but none of them';
      log(`controllers/ has no *.controller.ts — ${err}`);
      return { sawControllerFiles: false, staleSDKSignature: false, routeCount: 0, skipped, buildError: err };
    }
    log('0 route(s) (no controllers/*.controller.ts found)');
    return { sawControllerFiles: false, staleSDKSignature: false, routeCount: 0, skipped };
  }

  let sawControllerFiles = false;
  let staleSDKSignature = false;
  for (const file of walk(BUNDLED_CONTROLLERS_DIR)) {
    if (!BUNDLED_CONTROLLER_RE.test(path.basename(file))) continue;
    sawControllerFiles = true;
    let Ctrl;
    try {
      Ctrl = loadControllerClass(file);
    } catch (err) {
      // A decorator that resolved to `undefined` (stale/missing @palbase/backend)
      // throws "... is not a function" at module-eval time. Flag it so main()
      // can give an actionable message instead of a bare skip.
      if (/is not a function/.test(err.message)) staleSDKSignature = true;
      skipped.push({ file: bundledToSrcRel(file), error: err.message });
      continue;
    }
    // A controller the runtime cannot construct is a deploy that comes up and
    // then fails — or worse, comes up with `undefined` fields. The SDK owns the
    // rule and the message; asking it here is what moves the answer from BOOT
    // (where it takes the deploy down) to BUILD (where the author is looking).
    // Older SDKs do not export it, and an older SDK is not a reason to refuse.
    try {
      const sdk = require('@palbase/backend');
      if (typeof sdk.assertZeroArgConstructor === 'function') {
        sdk.assertZeroArgConstructor(Ctrl, 'controller');
      }
    } catch (err) {
      skipped.push({ file: bundledToSrcRel(file), error: err.message });
      continue;
    }
    const meta = readControllerMeta(Ctrl);
    const routeList = readControllerRoutes(Ctrl);
    for (const route of routeList) {
      if (!route || typeof route !== 'object') continue;
      const method = typeof route.method === 'string' ? route.method.toUpperCase() : '';
      const routeKey = typeof route.fnName === 'string' ? route.fnName : '';
      if (!method || !routeKey) continue;
      const urlPattern = joinPath(meta.basePath, route.subpath);
      const { regex, paramNames } = urlToRegex(urlPattern);
      routes.set(method + ' ' + urlPattern, {
        method,
        urlPattern,
        regex,
        paramNames,
        controllerPath: file,
        controllerName: deriveControllerName(Ctrl),
        routeKey,
      });
    }
  }
  for (const route of routes.values()) {
    log(`  ${route.method.padEnd(6)} ${route.urlPattern}  →  ${bundledToSrcRel(route.controllerPath)} [${route.routeKey}]`);
  }

  // EVERY SOURCE CONTROLLER MUST ANSWER FOR ITSELF.
  //
  // The count is a SUM, and a sum hides a subtraction: a file that stops
  // registering takes its endpoints off the air while the line above still ends
  // in "build OK". The existing 0-route guard only fires when the WHOLE tree
  // registers nothing, so the one shape that actually happens — one file out of
  // seventeen going quiet — passed straight through.
  //
  // Ölçüldü 26.08.2026 (palai-cloud): bir controller bir an 0 bayta düştü ve
  // `palbase build` "build OK — 66 route(s)" dedi. Ne dosyayı andı ne düşen
  // rotayı; o çıktıda okunması gereken tek satır toplam sayıydı ve onu da
  // ezberden bilmek gerekiyordu. Boşalan (ya da @Controller sınıfını kaybeden)
  // bir dosya bu ağaçta bir rotayı SESSİZCE yayından kaldırır.
  //
  // A file that failed to LOAD is already reported as `skipped`; naming it twice
  // would turn one fault into two findings.
  const answered = new Set();
  for (const route of routes.values()) answered.add(withoutExtension(bundledToSrcRel(route.controllerPath)));
  for (const s of skipped) answered.add(withoutExtension(s.file));
  const silent = [];
  for (const src of sourceControllerFiles()) {
    if (!answered.has(withoutExtension(src))) silent.push(src);
  }

  return { sawControllerFiles, staleSDKSignature, routeCount: routes.size, skipped, silent };
}

// sourceControllerFiles lists the *.controller.ts a person actually wrote,
// project-relative — the list the registration is measured AGAINST.
function sourceControllerFiles() {
  if (!fs.existsSync(CONTROLLERS_DIR)) return [];
  return walk(CONTROLLERS_DIR)
    .filter((f) => f.endsWith('.controller.ts'))
    .map((f) => path.join('controllers', path.relative(CONTROLLERS_DIR, f)));
}

// withoutExtension compares a SOURCE path with a BUNDLED one: the bundler emits
// `.js` beside the `.ts` it came from, and only the extension differs.
function withoutExtension(p) {
  return p.replace(/\.[^./\\]+$/, '');
}

// deployExtractErrors runs the deploy's extract_meta.js over every BUNDLED
// controller — the SAME script + stdin/stdout contract the pod's MetaExtractor
// uses (meta_extractor.go). It catches exactly the deploy-fatal extraction
// failures the route-register above does NOT (assertZodSchema for @Body/
// @QueryParams/@Headers given a non-zod arg, reserved/non-string @Headers,
// Express-style :param). Returns [{file, error}]; empty when every controller
// extracts clean. A spawn failure (node/extractor missing) is reported as its
// own entry so the run fails loud rather than passing blind.
function deployExtractErrors() {
  const out = [];
  if (!fs.existsSync(BUNDLED_CONTROLLERS_DIR)) return out;
  const extractor = path.join(__dirname, 'extract_meta.js');
  for (const file of walk(BUNDLED_CONTROLLERS_DIR)) {
    if (!BUNDLED_CONTROLLER_RE.test(path.basename(file))) continue;
    const srcRel = bundledToSrcRel(file);
    let stdout;
    try {
      stdout = execFileSync('node', [extractor], {
        input: JSON.stringify({ bundle_path: file }),
        // RUNTIME_MODULES, not PROJECT_ROOT/node_modules: the project root is the
        // DEPLOY-SHAPED staging tree, which has no node_modules (that is the
        // point — see RUNTIME_MODULES). The extractor still needs the real
        // @palbase/backend, the same way the pod's global install provides it.
        env: Object.assign({}, process.env, { NODE_PATH: RUNTIME_MODULES }),
        encoding: 'utf8',
        stdio: ['pipe', 'pipe', 'pipe'],
      });
    } catch (err) {
      out.push({ file: srcRel, error: `extractor failed to run: ${err.message}` });
      continue;
    }
    let res;
    try {
      res = JSON.parse(stdout);
    } catch {
      out.push({ file: srcRel, error: `extractor produced no JSON: ${String(stdout).slice(0, 200)}` });
      continue;
    }
    if (res && res.error) out.push({ file: srcRel, error: res.error });
  }
  return out;
}

// bundledToSrcRel maps a bundled controller path back to the project-relative
// SOURCE path (BUNDLE_ROOT/controllers/x.controller.js → controllers/x.controller.ts).
// The bundled tree mirrors the source tree (esbuild --outbase), so the bundle
// root is swapped for "controllers" and the extension is restored from the
// staging manifest.
//
// THE EXTENSION IS THE WHOLE POINT. This used to take it from the bundled path,
// which is always `.js` — so `palbase build` reported
// `controllers/todos.controller.js` for a file the author had written as
// `todos.controller.ts`. That string is what the route table prints, what a
// skipped controller is named by, and what an extractor error points at: the
// three places somebody needs to OPEN the file, all naming a path that resolves
// to nothing (the `.js` lives in a temp dir that is deleted on exit).
//
// Unmapped or ambiguous stems keep the bundled extension — a fail-safe, because
// a path that is merely unhelpful beats a path that is confidently wrong.
function bundledToSrcRel(bundledPath) {
  const rel = path.relative(BUNDLED_CONTROLLERS_DIR, bundledPath);
  const srcRel = path.join('controllers', rel);
  const srcExt = SRC_EXT_BY_STEM.get(withoutExtension(srcRel));
  if (!srcExt) return srcRel;
  return withoutExtension(srcRel) + srcExt;
}

// esbuildErr renders a child_process.execFileSync error: prefer the captured
// stderr (the esbuild diagnostic with file:line:col), fall back to the message.
function esbuildErr(err) {
  const stderr = err && err.stderr ? String(err.stderr).trim() : '';
  if (stderr) return stderr;
  return err && err.message ? err.message : String(err);
}

// walk yields every file under dir (recursive). Returns an array so callers can
// iterate without a callback.
function walk(dir) {
  const out = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...walk(full));
    else out.push(full);
  }
  return out;
}

// declaredSurfaces names what ELSE this tree would deploy: jobs, webhooks and
// hooks.
//
// A TOTAL HIDES A SUBTRACTION. This line used to say `build OK — 67 route(s)`
// and nothing more, and it said exactly that over a project carrying four @Job
// classes that the push then dropped on the floor. Four background jobs — one of
// them a balance auto-top-up — deployed green and never fired, and the report a
// person actually reads never mentioned them, so there was no number to notice
// was wrong (measured 2026-08-26, tenant `1jhp7jbrm`).
//

// SURFACE CONSTRUCTORS — the half of the zero-argument rule that was missing.
//
// The SDK refuses a hook/job/webhook class that declares constructor parameters
// (decorators/{hook,job,webhook}.ts call assertZeroArgConstructor), but it does
// so in the RESOLVER, which runs at boot. So a project with
// `@Job class Topup { constructor(private repo: Repo) {} }` built clean, exit 0,
// and then took the deploy down when the runtime resolved it — the exact place
// FR-010 exists to move the answer AWAY from.
//
// Controllers were already checked here (registerControllers below). These three
// directories were only COUNTED — declaredSurfaces reads the filesystem and
// never loads a class — so the rule had no build-time half for them.
//
// The rule and the message stay the SDK's; this asks it. An older SDK that does
// not export the check is not a reason to refuse a build.
const SURFACE_DIRS = [
  ['jobs', 'job class'],
  ['webhooks', 'webhook class'],
  ['hooks', 'hook class'],
];

/** Every class a surface file exports, decorated or default. Bundled first, for
 * the same reason controllers are: the sources are TypeScript and the extension
 * -less relative imports must resolve the way they do on deploy. */
function surfaceClassesIn(dirName) {
  const srcDir = path.join(PROJECT_ROOT, dirName);
  if (!fs.existsSync(srcDir)) return [];
  const outDir = path.join(BUNDLE_ROOT, dirName);
  rmBundledTree(outDir);
  if (!bundleSrcDir(srcDir, outDir, CONTROLLER_RESOURCE_EXTERNALS)) return [];

  const found = [];
  for (const file of walk(outDir)) {
    if (!/\.(c?js)$/i.test(path.basename(file))) continue;
    let mod;
    try {
      delete require.cache[require.resolve(file)];
      mod = require(file);
    } catch (err) {
      // A file that will not load is reported the way a controller's is; it is
      // not silently skipped, because "did not load" and "declared nothing"
      // deploy very differently.
      found.push({ file, error: err.message });
      continue;
    }
    const candidates = [mod && mod.default, ...(mod ? Object.keys(mod).map((k) => mod[k]) : [])];
    for (const value of candidates) {
      if (typeof value === 'function' && !found.some((f) => f.cls === value)) {
        found.push({ file, cls: value });
      }
    }
  }
  return found;
}

/** Refusals for every surface class that declares constructor parameters. */
function checkSurfaceConstructors() {
  const runtime = runtimeModule();
  if (!runtime || typeof runtime.assertZeroArgConstructor !== 'function') return [];
  const failures = [];
  for (const [dirName, kind] of SURFACE_DIRS) {
    for (const entry of surfaceClassesIn(dirName)) {
      const shown = path.join(dirName, path.relative(path.join(BUNDLE_ROOT, dirName), entry.file))
        .replace(/\.c?js$/i, '.ts');
      if (entry.error) {
        failures.push({ file: shown, error: entry.error });
        continue;
      }
      try {
        runtime.assertZeroArgConstructor(entry.cls, kind);
      } catch (err) {
        failures.push({ file: shown, error: err.message });
      }
    }
  }
  return failures;
}

// Counted from the DIRECTORY rather than from a bundle, deliberately: this is
// the declaration, and the point of printing it here is to give the author a
// figure they can compare against what the runtime reports at boot. `palbase
// push` is what refuses a bundle that lost one.
function declaredSurfaces() {
  const parts = [];
  for (const [dir, noun] of [['jobs', 'job'], ['webhooks', 'webhook'], ['hooks', 'hook']]) {
    let files = [];
    try {
      files = fs.readdirSync(path.join(PROJECT_ROOT, dir), { withFileTypes: true })
        .filter((e) => !e.isDirectory() && /\.m?ts$/i.test(e.name) && !/\.test\.m?ts$/i.test(e.name));
    } catch {
      continue; // No such directory is the normal case, not a fault.
    }
    if (files.length > 0) parts.push(`${files.length} ${noun}(s)`);
  }
  return parts.length > 0 ? `, plus ${parts.join(', ')}` : '';
}

function log(msg) {
  process.stdout.write(`${msg}\n`);
}

// bundleResources esbuild-bundles resources/*.ts the same way controllers/ are
// bundled. MUST run BEFORE controllers are bundled + require()d: the controllers
// bundle keeps `../resources/*` imports EXTERNAL (CONTROLLER_RESOURCE_EXTERNALS),
// so a bundled controller's require("../resources/x") resolves to
// BUNDLE_ROOT/resources/x.js. Same order as the deploy extractor (resources
// bundled before controllers). An esbuild error here is reported but not fatal
// on its own: a controller that actually imports the resource then fails LOUDLY
// at require, which registerControllers records as a skip.
function bundleResources() {
  if (!fs.existsSync(RESOURCES_DIR)) return false;
  rmBundledTree(BUNDLED_RESOURCES_DIR);
  try {
    return bundleSrcDir(RESOURCES_DIR, BUNDLED_RESOURCES_DIR);
  } catch (err) {
    log(`⚠ esbuild failed for resources/ — ${esbuildErr(err)} — continuing ` +
      '(controllers importing them will fail to load below)');
    return false;
  }
}

// SOURCE_FILE_RE matches the TypeScript files scanTxPlanViolations reads —
// SOURCE .ts/.tsx, never a bundled .js: tx_analysis.js is a parser (no
// TypeChecker), so it needs the file that still carries the syntax it looks
// for. Deliberately excludes .d.ts (types-only, never a call site).
const SOURCE_FILE_RE = /(?<!\.d)\.tsx?$/i;

// scanTxPlanViolations walks controllers/ + services/ (source, not bundled —
// see SOURCE_FILE_RE) for Database.transaction() Ref-truthiness misuse. Every
// finding here is a hard build failure — see tx_analysis.js's own header for
// why this pass, alone among the three shared with the deploy stager, has no
// best-effort / warn-only mode.
function scanTxPlanViolations() {
  const violations = [];
  for (const dir of [CONTROLLERS_DIR, SERVICES_DIR]) {
    if (!fs.existsSync(dir)) continue;
    for (const file of walk(dir)) {
      if (!SOURCE_FILE_RE.test(file)) continue;
      const src = fs.readFileSync(file, 'utf8');
      const rel = path.relative(PROJECT_ROOT, file);
      violations.push(...txAnalysis.findTxPlanViolations(src, rel));
    }
  }
  return violations;
}


// scanTypecheckIsRunning answers ONE question: would `tsc` actually check this
// project, or would it exit reporting only a config problem and verify nothing?
//
// THE SILENT-NOTHING CASE IS THE POINT. A tsconfig naming `types: ["node"]`
// without @types/node installed makes tsc print
//
//   error TS2688: Cannot find type definition file for 'node'
//
// and then check NO FILES. A deliberate `const n: number = "string"` in a
// controller is not reported; `--listFiles` still lists the file. Measured
// 2026-08-29 on a copied fixture, where it silently invalidated a run's own
// verification.
//
// It matters more than it used to. A project's spellable secret, flag and
// bucket names come from the generated `palbase-stack.d.ts` and are enforced by
// the COMPILER — nothing else checks them. A typecheck that verifies nothing
// takes that gate away without saying so, and the build looks green.
//
// Only the config-level failure is reported here. Ordinary type errors are the
// author's business and this build has never graded them; the claim being made
// is narrower and stronger — "your typecheck is not running at all".
function scanTypecheckIsRunning() {
  const tsconfig = path.join(PROJECT_ROOT, 'tsconfig.json');
  if (!fs.existsSync(tsconfig)) return null;

  let raw;
  try {
    raw = fs.readFileSync(tsconfig, 'utf8');
  } catch {
    return null;
  }
  // A `types` array is what turns a missing @types package into a program-wide
  // stop. Without one, a missing type package is just a missing import.
  if (!/"types"\s*:\s*\[/.test(raw)) return null;

  // `-p typescript` ON PURPOSE: bare `npx tsc` resolves to an unrelated package
  // of that name (it prints "This is not the tsc command you are looking for"
  // and exits 0), so a check written the obvious way silently passes. Binding
  // the PACKAGE rather than the binary name is what makes this run the compiler.
  let out = '';
  try {
    out = execFileSync('npx', ['--yes', '-p', 'typescript', 'tsc', '--noEmit'], {
      cwd: PROJECT_ROOT, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
    });
  } catch (err) {
    out = `${err.stdout || ''}${err.stderr || ''}`;
  }
  const missing = [...out.matchAll(/error TS2688: Cannot find type definition file for '([^']+)'/g)]
    .map((m) => m[1]);
  if (missing.length === 0) return null;
  return missing;
}

function main() {
  // TxPlan Ref-truthiness gate runs FIRST and exits immediately on any
  // finding — never merged into the `failures` list below. That list can, in
  // principle, grow a tolerated/soft entry some day; this one must never be
  // able to. A Ref-truthiness miss is a transaction that commits the WRONG
  // data with no error anywhere, which is worse than any other build failure
  // this file reports — see tx_analysis.js's header.
  // TYPECHECK-IS-RUNNING gate, before anything else reports success.
  //
  // A project whose tsc exits on a config error checks nothing, and the names a
  // controller may spell (`palbase-stack.d.ts`) are enforced by the compiler and
  // by nothing else. Green here would mean the gate is gone.
  const missingTypes = scanTypecheckIsRunning();
  if (missingTypes && missingTypes.length > 0) {
    log(`\u2717 DEPLOY WOULD FAIL: tsc cannot resolve ${missingTypes.map((t) => `"${t}"`).join(', ')} ` +
      'from `types` in tsconfig.json, so it stops before checking a single file.');
    log('  Nothing is being typechecked — not your controllers, and not the secret, flag and');
    log('  bucket names that only the compiler enforces. Install the missing package');
    log(`  (\`npm i -D @types/${missingTypes[0]}\`) or drop it from \`types\`.`);
    log('build FAILED (typecheck is not running)');
    process.exit(1);
  }

  const txViolations = scanTxPlanViolations();
  if (txViolations.length > 0) {
    for (const v of txViolations) log(`✗ DEPLOY WOULD FAIL: ${txAnalysis.formatViolation(v)}`);
    log(`build FAILED (${txViolations.length} TxPlan Ref-truthiness violation${txViolations.length === 1 ? '' : 's'}) — ` +
      'a transaction() plan reads a field that is a Ref, not a real value, in a boolean/string/arithmetic ' +
      'context. See the message above each site for the fix.');
    process.exit(1);
  }

  // Resources bundle FIRST: registerControllers require()s the bundled
  // controllers, and their external `../resources/*` requires must resolve to
  // BUNDLE_ROOT/resources/.
  bundleResources();
  const reg = registerControllers();

  const failures = [];
  if (reg.buildError) failures.push({ file: 'controllers/', error: reg.buildError });
  for (const s of reg.skipped || []) failures.push(s);
  for (const e of deployExtractErrors()) failures.push(e);
  for (const f of reg.silent || []) {
    failures.push({
      file: f,
      error: 'registered no routes — a deploy would take its endpoints off the air, and the ' +
        'total route count is the only place that would have shown it',
    });
  }

  // Files but no routes is the SILENT class: the deploy would be written as
  // successful and serve zero endpoints. Fail here instead.
  if (failures.length === 0 && reg.sawControllerFiles && reg.routeCount === 0) {
    if (reg.staleSDKSignature) {
      failures.push({
        file: 'controllers/',
        error: '@palbase/backend is stale or missing — the @Controller/@Get decorators ' +
          'resolved to undefined. Run `npm install @palbase/backend@latest`.',
      });
    } else {
      failures.push({ file: 'controllers/', error: 'controllers/ has files but 0 routes would register' });
    }
  }

  // ‼️ `process.exit` DEĞİL. stdout bir boru olduğunda yazım asenkrondur ve
  // `exit` kuyrukta kalanı düşürür — ölçüldü 26.08.2026: 128 KiB yazan bir
  // çocuktan tam 65 536 bayt okunuyor (node 26.7 ve bun 1.3.9). Bu dosya route
  // tablosunu satır satır basıyor, yani büyük bir projede rapor tam da
  // okunması gereken yerden kesilirdi. `exitCode` atayıp doğal çıkışı beklemek
  // iki çalışma zamanında da her baytı teslim ediyor (ölçüldü).
  // The surface classes the deploy would resolve at boot, checked HERE instead.
  for (const f of checkSurfaceConstructors()) failures.push(f);

  if (failures.length === 0) {
    log(`build OK — ${reg.routeCount} route(s) across the controllers would deploy cleanly${declaredSurfaces()}`);
    process.exitCode = 0;
    return;
  }
  for (const f of failures) log(`✗ DEPLOY WOULD FAIL: ${f.file} — ${f.error}`);
  log(`build FAILED (${failures.length} error${failures.length === 1 ? '' : 's'}) — a deploy of this tree would be rejected`);
  process.exitCode = 1;
}

// Remove the temp bundle tree + the in-project staging tree on ANY exit, not
// just the happy path: STAGED_CONTROLLERS_DIR lives under PROJECT_ROOT and must
// never be left behind in the user's repo. 'exit' handlers must be synchronous.
process.on('exit', () => {
  try { fs.rmSync(BUNDLE_ROOT, { recursive: true, force: true }); } catch { /* best-effort */ }
  try { fs.rmSync(STAGED_CONTROLLERS_DIR, { recursive: true, force: true }); } catch { /* best-effort */ }
});

// Node runs 'exit' handlers on a normal return or process.exit(), but NOT when a
// default-handled signal kills the process. Ctrl-C during a build therefore left
// .palbase-build-controllers/ sitting in the user's repo — the one path that put
// it there at all, since a build otherwise stages into a temp tree. Re-exit
// explicitly so the handler above runs. (128+signo is the shell convention.)
for (const [sig, code] of [['SIGINT', 130], ['SIGTERM', 143], ['SIGHUP', 129]]) {
  process.on(sig, () => process.exit(code));
}

// Auto-run only when invoked directly. Guarded so the file can be require()d by
// tests (the resource-externals cross-boundary test drives the REAL bundle path)
// without running the check.
if (require.main === module) {
  try {
    main();
  } catch (err) {
    log(`FATAL: build check failed to run: ${err && err.stack ? err.stack : String(err)}`);
    process.exit(1);
  }
}

// Exported for the Go-side tests: the REAL stage+bundle path must be exercised
// directly, never re-implemented in a test.
module.exports = {
  registerControllers, bundleResources, BUNDLED_CONTROLLERS_DIR, BUNDLED_RESOURCES_DIR,
  stageControllersWithReturnBindings, bundledToSrcRel,
  surfaceClassesIn, checkSurfaceConstructors,
};
