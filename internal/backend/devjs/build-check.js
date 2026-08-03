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

// stageControllersWithReturnBindings copies controllers/*.ts into a staging dir,
// appending each file's return-type schema injection, and returns the staging
// dir for esbuild to bundle. The user's source is never modified. A return-type
// violation throws (caught by registerControllers and surfaced loudly), matching
// the deploy behavior.
function stageControllersWithReturnBindings(srcDir, stageDir) {
  rmBundledTree(stageDir);
  fs.mkdirSync(stageDir, { recursive: true });
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
function bundleSrcDir(srcDir, outDir, externals = []) {
  if (!fs.existsSync(srcDir)) return false;
  const entries = walk(srcDir).filter((f) => /\.(c?js|mjs|tsx?|jsx)$/i.test(path.basename(f)));
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

const CONTROLLER_FILE_RE = /\.(c?js|mjs|ts)$/i;

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
const RETURN_BUFFER_SYMBOL = Symbol.for('palbase.backend.returnBuffer');
const CONTROLLER_META_SYMBOL = Symbol.for('palbase.backend.controllerMeta');

// loadControllerClass require()s a bundled controller file and returns its
// default-export @Controller CLASS. Throws a clear error when the default export
// is not a controller class — there is no functional-model fallback, matching
// extract_meta.js + the runtime.
function loadControllerClass(controllerPath) {
  delete require.cache[require.resolve(controllerPath)];
  const mod = require(controllerPath);
  const Ctrl = mod.default ?? mod;
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
    bundleSrcDir(staged, BUNDLED_CONTROLLERS_DIR, CONTROLLER_RESOURCE_EXTERNALS);
  } catch (err) {
    // A bundle error (syntax error, unresolved import) OR a return-type
    // violation must be LOUD — otherwise the dir scan below finds nothing and
    // silently registers 0 routes.
    const msg = err instanceof returnTypes.ReturnTypeError ? err.message : esbuildErr(err);
    log(`controllers/ build failed — ${msg}`);
    return { sawControllerFiles: false, staleSDKSignature: false, routeCount: 0, skipped, buildError: msg };
  }

  if (!fs.existsSync(BUNDLED_CONTROLLERS_DIR)) {
    log('0 route(s) (no controllers/*.ts found)');
    return { sawControllerFiles: false, staleSDKSignature: false, routeCount: 0, skipped };
  }

  let sawControllerFiles = false;
  let staleSDKSignature = false;
  for (const file of walk(BUNDLED_CONTROLLERS_DIR)) {
    if (!CONTROLLER_FILE_RE.test(path.basename(file))) continue;
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
  return { sawControllerFiles, staleSDKSignature, routeCount: routes.size, skipped };
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
    if (!CONTROLLER_FILE_RE.test(path.basename(file))) continue;
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
// SOURCE path for friendly logging (BUNDLE_ROOT/controllers/x.controller.js →
// controllers/x.controller.js). The bundled `.js` mirrors the source tree
// (esbuild --outbase), so we just swap the bundle root for "controllers".
function bundledToSrcRel(bundledPath) {
  const rel = path.relative(BUNDLED_CONTROLLERS_DIR, bundledPath);
  return path.join('controllers', rel);
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

function main() {
  // TxPlan Ref-truthiness gate runs FIRST and exits immediately on any
  // finding — never merged into the `failures` list below. That list can, in
  // principle, grow a tolerated/soft entry some day; this one must never be
  // able to. A Ref-truthiness miss is a transaction that commits the WRONG
  // data with no error anywhere, which is worse than any other build failure
  // this file reports — see tx_analysis.js's header.
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

  if (failures.length === 0) {
    log(`build OK — ${reg.routeCount} route(s) across the controllers would deploy cleanly`);
    process.exit(0);
  }
  for (const f of failures) log(`✗ DEPLOY WOULD FAIL: ${f.file} — ${f.error}`);
  log(`build FAILED (${failures.length} error${failures.length === 1 ? '' : 's'}) — a deploy of this tree would be rejected`);
  process.exit(1);
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
};
