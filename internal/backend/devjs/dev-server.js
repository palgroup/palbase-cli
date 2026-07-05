#!/usr/bin/env node
/**
 * Palbase backend dev server (class controllers).
 *
 * Local equivalent of the prod backend-runtime app-server
 * (modules/backend/internal/runtime/worker.js), tuned for hot reload +
 * interactive output. The shape of every user-facing surface (controllers,
 * the per-request injection, the imported singleton services, resources)
 * matches prod so what runs under `palbase serve` runs identically in
 * services-shared/br-<ref> after a `git push` deploy. This file is worker.js's
 * LOCAL TWIN — its dispatch mirrors worker.js EXACTLY (keep them in lockstep).
 *
 * Routing surface: controllers/ ONLY. A controllers/*.ts file default-exports a
 * @Controller CLASS (`__palbase === 'controller'` is stamped on the class). The
 * route registry lives in an ARRAY on `Symbol.for("palbase.backend.routes")`,
 * each entry a RouteMeta `{ method, subpath, fnName, options:{auth?,rateLimit?},
 * params: ParamMeta[] }`; `@Returns(zod)` schemas live in a separate buffer on
 * `Symbol.for("palbase.backend.returnBuffer")` keyed by fnName; the controller
 * meta (`{ __palbase, basePath, defaultAuth? }`) lives on
 * `Symbol.for("palbase.backend.controllerMeta")`. The full route path is
 * `basePath + subpath`; the verb is `route.method`. Each route is dispatched by
 * calling the class method `instance[fnName](...args)` with positional param
 * injection — identical to the runtime's dispatch + extract_meta.js +
 * generator.go. There is NO old functional model (`controller.routes[mapKey]
 * .handler` / `req.errors`) — clean cutover; errors are the global throw classes
 * (Conflict/NotFound/…) caught by SHAPE (`{ status:number, error:string }`).
 *
 * Invocation (set by the Go CLI):
 *   PALBASE_DEV_PORT=4003 PALBASE_PROJECT_REF=foo PALBASE_DEV_ROOT=/abs/path node dev-server.js
 */
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const http = require('http');
const url = require('url');
const { execFileSync } = require('child_process');

// 4003 is the single canonical local port — the codegen consumers (`palbase
// mobile codegen`, `palbase types|pull --env local`) probe localhost:4003, and
// the Go `serve --port` default matches it, so a plain `palbase serve` lands
// where codegen looks.
const PORT = Number(process.env.PALBASE_DEV_PORT) || 4003;

// lanIP returns the primary non-internal IPv4 address, so we can print a
// device-reachable URL (a physical device on the same Wi-Fi can't use
// localhost). Returns null when only loopback exists. (`os` is required at top.)
function lanIP() {
  for (const addrs of Object.values(os.networkInterfaces())) {
    for (const a of addrs || []) {
      if (a.family === 'IPv4' && !a.internal) return a.address;
    }
  }
  return null;
}

const PROJECT_ROOT = process.env.PALBASE_DEV_ROOT || process.cwd();

// parseDotenv parses dotenv text into a {KEY: value} map. Pure (no I/O, no
// process.env) so it is unit-testable. Skips blank lines and `#` comments,
// requires a non-empty key before `=`, and strips one pair of surrounding
// single/double quotes from the value.
function parseDotenv(text) {
  const out = {};
  for (const raw of String(text).split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const eq = line.indexOf('=');
    if (eq <= 0) continue;
    const key = line.slice(0, eq).trim();
    if (!key) continue;
    let val = line.slice(eq + 1).trim();
    if (
      (val.startsWith('"') && val.endsWith('"')) ||
      (val.startsWith("'") && val.endsWith("'"))
    ) {
      val = val.slice(1, -1);
    }
    out[key] = val;
  }
  return out;
}

// Auto-load <PROJECT_ROOT>/.env.local (then .env) into process.env for local dev
// — the same file `palbase secret pull` writes. Without this, a Resource that
// reads process.env.OPENAI_API_KEY (etc.) got an empty value locally even after
// the secret was pulled (the docs implied serve loads it; it did not). An
// ALREADY-SET process.env var WINS (explicit `FOO=bar palbase serve` overrides
// the file; an earlier file's key wins over a later one).
(function loadDotEnvLocal() {
  for (const name of ['.env.local', '.env']) {
    let text;
    try {
      text = fs.readFileSync(path.join(PROJECT_ROOT, name), 'utf8');
    } catch {
      continue; // file absent — fine
    }
    for (const [key, val] of Object.entries(parseDotenv(text))) {
      if (!Object.prototype.hasOwnProperty.call(process.env, key)) process.env[key] = val;
    }
  }
})();

const PROJECT_REF = process.env.PALBASE_PROJECT_REF || 'local';
const PUBLIC_HOST = process.env.PALBASE_PUBLIC_HOST || '';
const CONTROLLERS_DIR = path.join(PROJECT_ROOT, 'controllers');
const RESOURCES_DIR = path.join(PROJECT_ROOT, 'resources');
const WORKERS_DIR = path.join(PROJECT_ROOT, 'workers');

// ── esbuild bundling — IDENTICAL to the deploy path ────────────────────────
//
// We do NOT require() the raw controllers/*.ts. Node's loader can't resolve the
// v2 canonical EXTENSIONLESS relative imports (`import x from "../handlers/foo"`,
// `"../../services/bar.service"`) — those need a bundler. The deployed pod
// esbuild-bundles each controllers/* (and resources/*) file as its own entry
// point with the import graph (handlers/, services/, …) inlined and
// @palbase/backend kept external (modules/backend internal/deploy/bundler.go +
// bundler_config.go). We mirror that EXACTLY here so what `palbase serve` loads
// is what the pod loads.
//
// Each src dir is bundled into BUNDLE_ROOT/<name>/ preserving the source tree
// (esbuild --outbase), so controllers/todos.controller.ts →
// BUNDLE_ROOT/controllers/todos.controller.js. The dev-server then discovers +
// require()s the BUNDLED .js (CJS), never the raw .ts.
const BUNDLE_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'palbase-serve-bundle-'));
const BUNDLED_CONTROLLERS_DIR = path.join(BUNDLE_ROOT, 'controllers');
const BUNDLED_RESOURCES_DIR = path.join(BUNDLE_ROOT, 'resources');
const BUNDLED_WORKERS_DIR = path.join(BUNDLE_ROOT, 'workers');
// Staging tree for controllers with return-schema bindings injected, BEFORE
// esbuild. Keeps the user's source untouched. MUST be a sibling of
// PROJECT_ROOT/controllers so a staged controller's `../models` / `../services`
// relative imports resolve to the real project dirs (the same depth as the
// original). Cleaned up between rebuilds + on exit.
const STAGED_CONTROLLERS_DIR = path.join(PROJECT_ROOT, '.palbase-serve-controllers');

// A {"type":"commonjs"} marker in BUNDLE_ROOT pins our --format=cjs `.js`
// bundles to CommonJS regardless of the project's package.json "type" — the
// same marker the deploy bundler drops in .palbase/ so require() of a bundle
// never throws ERR_REQUIRE_ESM (bundler.go's CommonJS-marker note).
fs.writeFileSync(path.join(BUNDLE_ROOT, 'package.json'), '{"type":"commonjs"}\n');

// The esbuild externals + resolve-extensions match the deploy bundler
// (bundler_config.go DefaultBundleConfig + buildArgs): @palbase/backend stays
// external so the bundle's `import { Database }` resolves to the project's ONE
// installed instance (the same one the dev-server installs via __setRuntime),
// and .ts is added to the resolve set so a `.js`-spelled import of a `.ts`
// source (the TS-idiomatic ESM form) still resolves.
const ESBUILD_EXTERNAL = '@palbase/backend';
const ESBUILD_RESOLVE_EXTENSIONS = '.ts,.tsx,.js,.jsx,.json';

// Return-type → response-schema binder (shared VERBATIM with the deploy
// extractor: modules/backend/internal/runtime/return_types.js). Reads each
// controller's method RETURN TYPE from source and injects the matching zod
// binding (the @Returns replacement). Required — a missing/disallowed return
// type is a HARD error here, exactly as on deploy.
const returnTypes = require('./return_types.js');
// Shared throw-site analyzer — VERBATIM-identical to the deploy copy
// (modules/backend/internal/runtime/throw_analysis.js), the same parity rule
// as return_types.js: serve and deploy must infer identical error sets.
const throwAnalysis = require('./throw_analysis.js');

// The named SDK classes' canonical wire codes — ad-hoc `throw new NotFound()`
// is always allowed and never trips the conformance guard (worker.js twin).
const GUARD_BUILTIN_CODES = new Set([
  'bad_request', 'unauthorized', 'forbidden', 'not_found', 'conflict', 'too_many_requests',
]);

// stageControllersWithReturnBindings copies controllers/*.ts into a staging dir,
// appending each file's return-type schema injection, and returns the staging
// dir for esbuild to bundle. The user's source is never modified. A return-type
// violation throws (caught by registerControllers and surfaced loudly), matching
// the deploy behavior so serve == deploy.
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
      // violation — serve == deploy).
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
// Returns true when at least one entry file was bundled; false when srcDir is
// absent or empty (a clean no-op). Throws on an esbuild error so a syntax error
// surfaces loudly rather than silently registering 0 routes.
function bundleSrcDir(srcDir, outDir) {
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
    // TodosController2), and without --keep-names the local spec would emit
    // todosController2.create while the deployed pod emits todos.create. Not
    // a tunable — a correctness requirement, always on.
    '--keep-names',
    `--outdir=${outDir}`,
    `--outbase=${srcDir}`,
    `--resolve-extensions=${ESBUILD_RESOLVE_EXTENSIONS}`,
    `--external:${ESBUILD_EXTERNAL}`,
    ...entries,
  ];
  // Run from PROJECT_ROOT so node_modules resolution (for any non-external dep
  // a handler/service imports) + relative imports anchor to the project. esbuild
  // is resolved via `npx --yes` — the same way the CLI's env-gen bundling shells
  // out (bundleSchemaTS), so no separate install is required.
  execFileSync('npx', args, {
    cwd: PROJECT_ROOT,
    env: Object.assign({}, process.env, { NODE_PATH: path.join(PROJECT_ROOT, 'node_modules') }),
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  return true;
}

// rmBundledTree removes a previously-emitted bundle dir so a rename/delete in
// the source tree doesn't leave a stale bundled `.js` behind (which would keep
// serving a deleted controller). Best-effort.
function rmBundledTree(outDir) {
  try { fs.rmSync(outDir, { recursive: true, force: true }); } catch { /* ignore */ }
}

// Capture pod-bound credentials into module-scope closures, then DELETE
// them from process.env BEFORE require()ing user code. User endpoints
// see ctx.env (a curated subset) — they cannot read process.env.PALBASE_*.
// Same security invariant as the prod runtime's worker.js, but enforced
// here for dev-server because this is a long-lived process re-loading
// user modules on file changes.
const TENANT_APIKEY = process.env.PALBASE_TENANT_APIKEY || '';
delete process.env.PALBASE_TENANT_APIKEY;

// Public host the SDK calls. Same shape as prod: <ref>.<host>.
// When PROJECT_REF==='local' (no `palbase login`) we run without
// the module clients live — ctx.docs/ctx.storage/… throw on use.
const PALBASE_URL = (PROJECT_REF !== 'local' && PUBLIC_HOST)
  ? `https://${PROJECT_REF}.${PUBLIC_HOST.replace(/\/+$/, '')}`
  : '';

const CONTROLLER_FILE_RE = /\.(c?js|mjs|ts)$/i;

// ── route table ────────────────────────────────────────────────────────
//
// controllers/ is the ONLY routing surface. Each controllers/*.ts file
// default-exports a @Controller CLASS. The route registry + controller meta +
// @Returns buffer live on Symbols stamped on the class (see header). We read the
// registry the SAME way worker.js does (readControllerRoutes), so dev = prod.
//
// Each route-table entry: { method, urlPattern, regex, paramNames,
// controllerPath, controllerName, routeKey, effectiveAuth, params,
// returnSchema }. `controllerPath` is the bundled file to require();
// `controllerName` is the dotted-operationId namespace derived from the live
// class name (deriveControllerName — "" falls back to the flat id); `routeKey`
// is the route's fnName (mirroring worker.js's `context.route_key`); `params`
// is the ordered ParamMeta injection plan; `returnSchema` is the live @Returns
// zod (or null); `effectiveAuth` is the resolved route.options.auth ??
// controller.defaultAuth ?? true. urlPattern keeps the class-controller
// `{param}` braces for printing; urlToRegex turns each `{param}` segment into
// a capture group for matching.
const routes = new Map();

// Class-controller registry symbols — MUST match the SDK
// (@palbase/backend src/decorators/registry.ts). worker.js reads the SAME
// symbols (RETURN_BUFFER_SYMBOL + ROUTES_SYMBOL) — keep them in lockstep.
const ROUTES_SYMBOL = Symbol.for('palbase.backend.routes');
const RETURN_BUFFER_SYMBOL = Symbol.for('palbase.backend.returnBuffer');
const CONTROLLER_META_SYMBOL = Symbol.for('palbase.backend.controllerMeta');

// loadControllerClass require()s a controller file (cache-busted so saves take
// effect without a restart) and returns its default-export @Controller CLASS.
// Throws a clear error when the default export is not a controller class — there
// is no functional-model fallback, matching extract_meta.js + worker.js.
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
// worker.js: prefer the SDK's own getRoutes() (an Array<RouteMeta> with the
// @Returns buffer merged) when the vendored SDK exports it, else read the raw
// ROUTES_SYMBOL array and merge the returnBuffer ourselves. The registry holds
// the LIVE zod schemas on each ParamMeta + in the return buffer, so the
// dev-server validates against them directly (the sidecar JSON is only for
// Go/OpenAPI). Returns a fresh array; never mutates the registry's own array.
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
// defaultAuth? }`. Mirrors worker.js's contract: prefer the SDK's
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

// effectiveAuth resolves a route's auth via the controller→route cascade:
// route.options.auth ?? controller.defaultAuth ?? true (secure-by-default).
// Returns the raw AuthSpec (boolean | { required?, role? } | undefined-as-true);
// isAuthRequired() lowers it to the enforcement decision.
function effectiveAuth(routeOptions, controllerMeta) {
  const routeAuth = routeOptions && typeof routeOptions === 'object' ? routeOptions.auth : undefined;
  if (routeAuth !== undefined) return routeAuth;
  if (controllerMeta && controllerMeta.defaultAuth !== undefined) return controllerMeta.defaultAuth;
  return true;
}

// urlToRegex compiles a `/todos/{id}`-style pattern into { regex, paramNames }.
// Class controllers (@Get("/{id}")) emit path params in BRACE form `{param}` —
// that's the one true form the route registry uses (worker.js + the OpenAPI
// generator agree). A path segment that is wholly `{name}` is a capture; anything
// else is a literal (regex-escaped). (Legacy `:param` is gone — clean cutover.)
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

function registerControllers() {
  routes.clear();
  if (!fs.existsSync(CONTROLLERS_DIR)) {
    log(`controllers/ not found at ${CONTROLLERS_DIR}`);
    return { sawControllerFiles: false, staleSDKSignature: false, routeCount: 0 };
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
    bundleSrcDir(staged, BUNDLED_CONTROLLERS_DIR);
  } catch (err) {
    // A bundle error (syntax error, unresolved import) OR a return-type
    // violation must be LOUD — otherwise the dir scan below finds nothing and
    // silently registers 0 routes.
    const msg = err instanceof returnTypes.ReturnTypeError ? err.message : esbuildErr(err);
    log(`controllers/ build failed — ${msg}`);
    log('registered 0 route(s) (fix the error above and save to retry)');
    return { sawControllerFiles: false, staleSDKSignature: false, routeCount: 0 };
  }

  if (!fs.existsSync(BUNDLED_CONTROLLERS_DIR)) {
    log('registered 0 route(s) (no controllers/*.ts found)');
    return { sawControllerFiles: false, staleSDKSignature: false, routeCount: 0 };
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
      // throws "... is not a function" at module-eval time. Flag it so the boot
      // guard can give an actionable message instead of a silent 0-route start.
      if (/is not a function/.test(err.message)) staleSDKSignature = true;
      log(`skipping ${bundledToSrcRel(file)} — ${err.message}`);
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
        effectiveAuth: effectiveAuth(route.options, meta),
        rateLimit: (route.options && typeof route.options === 'object') ? route.options.rateLimit : undefined,
        params: Array.isArray(route.params) ? route.params : [],
        returnSchema: route.returnSchema,
      });
    }
  }
  log(`registered ${routes.size} route(s):`);
  for (const route of routes.values()) {
    log(`  ${route.method.padEnd(6)} ${route.urlPattern}  →  ${bundledToSrcRel(route.controllerPath)} [${route.routeKey}]`);
  }
  return { sawControllerFiles, staleSDKSignature, routeCount: routes.size };
}

// bundledToSrcRel maps a bundled controller path back to the project-relative
// SOURCE path for friendly logging (BUNDLE_ROOT/controllers/x.controller.js →
// controllers/x.controller.ts-ish). The bundled `.js` mirrors the source tree
// (esbuild --outbase), so we just swap the bundle root for "controllers" and
// keep the relative tail (extension shown as the emitted .js).
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
// iterate without a callback (used by registerControllers).
function walk(dir) {
  const out = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...walk(full));
    else out.push(full);
  }
  return out;
}

// controllerInstances caches ONE instance per controller bundle path. Mirrors
// worker.js: class controllers are stateless-by-convention (services are
// singletons), so a single instance is reused; `this` binds to it so
// `this.someService` works in methods. The hot-reload (registerControllers →
// rmBundledTree + re-bundle) drops the require cache for the controller file;
// resolveRoute below detects a stale instance (its constructor differs from the
// freshly-required class) and rebuilds it, so a save takes effect without a
// restart. Keyed by the bundled controllerPath.
const controllerInstances = new Map();

// resolveRoute re-loads the route's controller class (cache-busted), finds the
// live RouteMeta by fnName, and returns the cached-or-fresh class instance plus
// the live route meta — mirroring worker.js's dispatch resolution. Throws when
// the controller no longer has the route or the method is gone (e.g. the file
// changed since registration). The returned route is the LIVE registry entry
// (live zod schemas on its params + returnSchema), so validation runs off the
// in-memory schemas exactly as worker.js does.
function resolveRoute(route) {
  const Ctrl = loadControllerClass(route.controllerPath);
  const routeList = readControllerRoutes(Ctrl);
  const live = routeList.find((r) => r && r.fnName === route.routeKey);
  if (!live || typeof live !== 'object') {
    throw new Error(`route "${route.routeKey}" not found in ${bundledToSrcRel(route.controllerPath)}`);
  }
  if (typeof live.fnName !== 'string' || typeof Ctrl.prototype[live.fnName] !== 'function') {
    throw new Error(`route "${route.routeKey}" has no method`);
  }
  // Instantiate ONCE per bundle and cache; rebuild if the class identity changed
  // (a hot-reload re-required the file → a new class object).
  let instance = controllerInstances.get(route.controllerPath);
  if (!instance || instance.constructor !== Ctrl) {
    instance = new Ctrl();
    controllerInstances.set(route.controllerPath, instance);
  }
  return { Ctrl, instance, route: live };
}

// findRouteParam returns the FIRST ParamMeta of the given kind on a route's
// param list, or undefined — mirrors worker.js. The registry stores the LIVE
// zod schema on the param meta (`.schema`), validated directly.
function findRouteParam(params, kind) {
  if (!Array.isArray(params)) return undefined;
  return params.find((p) => p && p.kind === kind);
}

// ── module clients — bit-perfect twin of the prod runtime's services ────
//
// The module clients (the Documents/Storage/… singletons installed via
// __setRuntime, below) are inline fetch wrappers hitting the same public hosts
// the deployed pod hits. Local dev = LIVE data: writes go to production
// palauth/paldocs/db. The CLI prints a banner before starting the server so
// this isn't a silent surprise.
//
// module-clients.js is a verbatim mirror of the backend-runtime's
// internal/runtime/module-clients.js. Keep them in lockstep — edit both
// when changing any client surface. There is no @palbase/server dep
// anymore; customers only add @palbase/backend.
const { buildModuleClients, buildDbEdgeClient } = require('./module-clients.js');

// Per-request identity carried in an AsyncLocalStorage box — mirrors the
// deployed executor.js (internal/runtime/executor.js: `requestALS.getStore()`).
// The dev-server builds the module clients ONCE (singleton, below), so the
// getters passed into buildModuleClients / buildDbEdgeClient read the CURRENT
// request's box lazily at call time: `Flags.get('key')` resolves the signed-in
// user's override → project default, and the Database edge forwards THIS
// request's Bearer on the authenticated RLS path.
//
// Why ALS and not a module-scoped `let`: Node serves HTTP requests concurrently.
// A `let currentRequestUserToken` set before `await handler()` is CLOBBERED the
// moment a second request dispatches while the first is suspended at any await —
// the resumed handler then reads the OTHER user's token (cross-tenant RLS) or a
// null the finally-clear left behind (anon → 403/404), and a wrong-user context
// with no matching rows returns a silent empty 200. ALS scopes the box to the
// async call tree, so it survives awaits and never leaks across requests. This
// makes serve match prod's per-request isolation exactly (dev = prod).
const { AsyncLocalStorage } = require('node:async_hooks');
const requestALS = new AsyncLocalStorage();

/** Run `fn` with the request's identity box in scope for all its async descendants. */
function runInRequestContext(box, fn) {
  return requestALS.run(box, fn);
}
/** The in-flight request's user id, or null (anonymous / outside a request). */
function currentRequestUserId() {
  const box = requestALS.getStore();
  return box ? box.userId : null;
}
/** The in-flight request's raw Bearer, or null. NEVER logged. */
function currentRequestUserToken() {
  const box = requestALS.getStore();
  return box ? box.userToken : null;
}

let palbaseClientSingleton = null;
function getPalbaseClients() {
  if (!PALBASE_URL || !TENANT_APIKEY) return null;
  if (palbaseClientSingleton) return palbaseClientSingleton;
  // Local dev receives only the publishable key. Privileged Palbase
  // module operations must run in the managed backend runtime; local
  // ctx.* calls use normal publishable-key permissions and fail clearly
  // when a service requires internal authority.
  palbaseClientSingleton = buildModuleClients({
    url: PALBASE_URL,
    apikey: TENANT_APIKEY,
  }, {
    // Lazy getter — reads the in-flight request's ALS box at flags-call time.
    // Matches worker.js/executor.js (per-request isolation across awaits).
    getCurrentUserId: currentRequestUserId,
  });
  return palbaseClientSingleton;
}

// Module clients (docs, storage, realtime, …) hang directly off ctx —
// ctx.docs, NOT ctx.palbase.docs — mirroring the deployed runtime
// (modules/backend/internal/runtime/worker.js, the flat-ctx refactor
// 0331a6d6). Keeping dev = prod here is the whole point: a handler that
// works under `palbase serve` must work once deployed.
const MODULE_NAMES = ['auth', 'storage', 'docs', 'realtime', 'functions',
  'flags', 'notifications', 'analytics', 'links', 'cms'];

// Customer-facing short names that alias to a canonical SDK surface.
// Kept identical to worker.js's MODULE_CLIENT_ALIASES so dev = prod (e.g.
// ctx.notify === ctx.notifications). Edit both in lock-step.
const MODULE_ALIASES = {
  notify: 'notifications',
};

function moduleClients() {
  const clients = getPalbaseClients();
  if (!clients) {
    // No tenant credentials: each module slot throws on first use so
    // partial behaviour fails loudly (same shape as worker.js's stub).
    const out = {};
    for (const n of MODULE_NAMES) out[n] = notConfiguredModule(n);
    for (const [alias, canonical] of Object.entries(MODULE_ALIASES)) {
      out[alias] = out[canonical] || notConfiguredModule(canonical);
    }
    return out;
  }
  const out = {};
  for (const n of MODULE_NAMES) out[n] = clients[n];
  // Identity-equal alias (out.notify === out.notifications) so handlers
  // can use either name and worker.js / dev-server stay consistent.
  for (const [alias, canonical] of Object.entries(MODULE_ALIASES)) {
    out[alias] = out[canonical];
  }
  return out;
}

// The Database singleton = the project's own Postgres surface in deployed
// mode. In `palbase serve` we don't have a local pgx pool (that's pod-local on
// 127.0.0.1), so we proxy the SAME @palbase/backend DBClient surface
// (insert/update/delete/findById/findMany/query + transaction + asService) to
// the br-pod's AUTHENTICATED edge at POST <gateway>/v1/backend-db/<op>. The edge
// derives RLS identity from the publishable apikey + a Bearer: the current
// request's user token on the authenticated path, OR — for asService() — the
// project OWNER's session token plus a non-authoritative `service_role` intent
// hint, which the edge verifies server-side before granting service_role. The
// service-role key NEVER touches the laptop. This keeps dev = prod — local
// Database hits the deployed tables with full RLS, the same as a deployed
// readOwnerToken returns the project OWNER's current session token for keyless
// asService(). serve writes it to PALBASE_OWNER_TOKEN_FILE and REFRESHES that file
// periodically (a session token expires ~30m), so we read it FRESH on every
// asService() call rather than caching a stale boot-time value. Returns undefined
// when not logged in (file absent/unreadable) → asService() throws the owner hint.
function readOwnerToken() {
  const f = process.env.PALBASE_OWNER_TOKEN_FILE;
  if (!f) return undefined;
  try {
    const t = fs.readFileSync(f, 'utf8').trim();
    return t || undefined;
  } catch {
    return undefined;
  }
}

// handler. Requires login (PALBASE_URL + TENANT_APIKEY); without it the Database
// slot throws notConfiguredModule('Database') on use.
function databaseClient() {
  if (!PALBASE_URL || !TENANT_APIKEY) return notConfiguredModule('Database');
  return buildDbEdgeClient({
    baseUrl: PALBASE_URL,
    apiKey: TENANT_APIKEY,
    // Lazy getter — reads the in-flight request's ALS box at DB-call time so it
    // forwards THIS request's verified user token, never a concurrent request's.
    getUserToken: currentRequestUserToken,
    // The OWNER's palauth session token (a normal user session, NOT a service-
    // role credential), set by `serve` only when logged in as the project owner.
    // asService() forwards it + a `service_role` intent hint; the edge grants
    // service_role only after verifying ownership. Absent → asService() throws
    // the actionable owner/login hint. NEVER logged.
    getOwnerToken: readOwnerToken,
  });
}

// isTenantModuleRoute reports whether an unknown (non-controller) path is a route
// the LIVE tenant gateway serves — auth, the SDK module APIs (/v1/...), storage,
// pg-meta, OIDC discovery. `palbase serve` runs your controllers locally but is a
// transparent window onto the deployed tenant for everything else (auth, flags,
// docs, storage, realtime, …), so these are forwarded rather than 404'd.
function isTenantModuleRoute(pathname) {
  // NEVER forward the authenticated Database edge. The SDK reaches it from INSIDE
  // controllers (server-to-server), never over serve's public HTTP face. If a
  // client hit localhost:4003/v1/backend-db directly we must NOT proxy it to the
  // live gateway — that would expose the privileged Database edge anonymously on
  // 0.0.0.0:4003. Let it fall through to a 404 instead.
  if (pathname.startsWith('/v1/backend-db')) return false;
  return (
    pathname.startsWith('/auth/') || pathname === '/auth' ||
    pathname.startsWith('/v1/') ||
    pathname.startsWith('/storage/') ||
    pathname.startsWith('/pg-meta') ||
    pathname.startsWith('/.well-known/') ||
    pathname.startsWith('/oauth')
  );
}

// proxyToTenant forwards an unknown module request to the live tenant gateway
// (PALBASE_URL) with the apikey injected and the caller's Authorization + content
// type preserved, then streams the upstream status/body back. Keeps `palbase
// serve` honest: auth/login/flags/etc. behave EXACTLY as deployed, so an app
// pointed at local serve can fully log in and use every module — only the
// controllers run locally.
async function proxyToTenant(req, res, parsed, start) {
  const target = PALBASE_URL.replace(/\/+$/, '') + req.url;
  const headers = { apikey: TENANT_APIKEY };
  if (req.headers['authorization']) headers['authorization'] = req.headers['authorization'];
  if (req.headers['content-type']) headers['content-type'] = req.headers['content-type'];
  // Collect the body (module calls are small JSON) for non-GET/HEAD.
  let body;
  if (req.method !== 'GET' && req.method !== 'HEAD') {
    const chunks = [];
    for await (const c of req) chunks.push(c);
    body = Buffer.concat(chunks);
  }
  try {
    const upstream = await fetch(target, { method: req.method, headers, body });
    res.statusCode = upstream.status;
    const ct = upstream.headers.get('content-type');
    if (ct) res.setHeader('content-type', ct);
    const buf = Buffer.from(await upstream.arrayBuffer());
    res.end(buf);
    log(`[${req.method}] ${parsed.pathname}  ${upstream.status}  ${Date.now() - start}ms  → tenant`);
  } catch (err) {
    res.statusCode = 502;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'tenant_proxy_failed', message: err.message }));
    log(`[${req.method}] ${parsed.pathname}  502  ${Date.now() - start}ms  — tenant proxy: ${err.message}`);
  }
}

// dev log surface (the Log singleton in serve mode).
function makeLog() {
  return {
    info: (...a) => console.log('[handler]', ...a),
    warn: (...a) => console.warn('[handler]', ...a),
    error: (...a) => console.error('[handler]', ...a),
    debug: (...a) => console.log('[handler:debug]', ...a),
  };
}

// clientInfoFromHeaders mirrors worker.js — derive req.client from the
// X-Palbase-*-Version headers (Faz 1: nullable raw fields only).
function clientInfoFromHeaders(headers) {
  const lower = {};
  for (const k of Object.keys(headers || {})) {
    lower[k.toLowerCase()] = headers[k];
  }
  const pick = (name) => {
    const v = lower[name];
    return v === undefined || v === null || v === '' ? null : String(v);
  };
  return {
    sdkVersion: pick('x-palbase-sdk-version'),
    appVersion: pick('x-palbase-client-version'),
    platform: pick('x-palbase-platform'),
    osVersion: pick('x-palbase-os-version'),
  };
}

// ── Cache (local, in-process) ──────────────────────────────────────────────
//
// The deployed runtime backs Cache with the pod-local internal-api (/internal-api
// /cache/* → the runtime's Redis), reachable ONLY from inside the br-pod — there
// is no external tenant route for it (no /v1/cache in Kong), so `palbase serve`
// can't proxy to it the way it proxies Documents. Instead we give serve a LOCAL,
// in-process cache so a controller calling Cache.get/set/incr/getOrSet actually
// WORKS for dev (rather than throwing a false "no credentials" error). It is
// dev-only and non-durable: the store is a single-process Map that vanishes when
// serve stops, and there's no cross-replica coordination (serve is one process).
// The method surface matches @palbase/backend's CacheClient EXACTLY
// (get/set/del/incr/getOrSet) so a handler that works here works once deployed.
//
// TTL is honored (unlike the unit-test mock): an expired key reads back as a
// miss, mirroring the prod cache's lazy expiry. The cache is a module-level
// singleton (built once, NOT per request) so values survive across the
// per-request installRuntime() calls — the same lifetime pattern as the
// Database/module-client singletons above.
function makeLocalCache() {
  // key → { value, expiresAt } where expiresAt is an epoch-ms deadline or null
  // (no TTL). Values are stored structurally (already JSON-serializable), so any
  // JSON value round-trips, matching the JSON-typed prod cache.
  const store = new Map();

  const live = (key) => {
    const entry = store.get(key);
    if (entry === undefined) return undefined;
    if (entry.expiresAt !== null && Date.now() >= entry.expiresAt) {
      store.delete(key); // lazy expiry
      return undefined;
    }
    return entry;
  };

  const put = (key, value, ttl) => {
    const expiresAt = typeof ttl === 'number' && ttl > 0 ? Date.now() + ttl * 1000 : null;
    store.set(key, { value, expiresAt });
  };

  // sweep drops every entry whose TTL deadline is already in the past. Lazy
  // expiry (in `live`) only evicts a key when it's READ — a key that's written
  // with a TTL and never read again would otherwise sit in the Map forever,
  // leaking memory for serve's whole lifetime. The singleton drives this on a
  // low-frequency unref'd interval (see localCache below); it's also exported on
  // the cache so it can be tested directly. Returns the number of entries
  // dropped. Synchronous (no I/O) so the interval callback can't overlap itself.
  const sweep = () => {
    const now = Date.now();
    let dropped = 0;
    for (const [key, entry] of store) {
      if (entry.expiresAt !== null && now >= entry.expiresAt) {
        store.delete(key);
        dropped += 1;
      }
    }
    return dropped;
  };

  const cache = {
    async get(key) {
      const entry = live(key);
      return entry === undefined ? null : entry.value;
    },
    async set(key, value, ttl) {
      put(key, value, ttl);
    },
    async del(key) {
      store.delete(key);
    },
    async incr(key) {
      // Single-process serve → no concurrent writers, so a read-modify-write is
      // atomic enough. A non-number current value coerces like the prod cache's
      // INCR (parse-or-zero), so an incr after a string set still advances.
      const entry = live(key);
      const raw = entry === undefined ? 0 : entry.value;
      const current = typeof raw === 'number' ? raw : (parseInt(String(raw), 10) || 0);
      const next = current + 1;
      // incr preserves any existing TTL deadline (prod INCR doesn't reset expiry).
      const expiresAt = entry === undefined ? null : entry.expiresAt;
      store.set(key, { value: next, expiresAt });
      return next;
    },
    // getOrSet is single-process here, so no distributed lock is needed — it is
    // just get-miss → fn → set, the same simplification the SDK test mock uses.
    async getOrSet(key, ttl, fn) {
      const hit = await cache.get(key);
      if (hit !== null) return hit;
      const value = await fn();
      await cache.set(key, value, ttl);
      return value;
    },
    // sweep evicts all expired entries up-front (background eviction; see above).
    // Exposed so the singleton's interval — and the unit test — can drive it.
    sweep,
    // size reports the current Map size (used by the test to prove the sweeper
    // actually shrinks the store, not just that reads miss). Not part of the
    // prod CacheClient surface; dev-only introspection.
    size() {
      return store.size;
    },
  };
  return cache;
}

// SWEEP_INTERVAL_MS is how often the cache singleton drops never-read-again
// expired entries. Low frequency (45s) — this is a memory-leak guard, not a
// precision timer; lazy-on-read expiry already keeps reads correct between
// sweeps. A module-level constant so the interval is created ONCE.
const SWEEP_INTERVAL_MS = 45 * 1000;

let localCacheSingleton = null;
let cacheSweeper = null;
function localCache() {
  if (!localCacheSingleton) {
    localCacheSingleton = makeLocalCache();
    // Create the sweep interval ONCE, alongside the singleton (the cache is
    // module-level, so the timer must be too — never per request). unref() so
    // this timer never keeps the Node process alive: serve must still exit
    // cleanly on Ctrl+C / when stdin closes, exactly as before the sweeper.
    cacheSweeper = setInterval(() => { localCacheSingleton.sweep(); }, SWEEP_INTERVAL_MS);
    cacheSweeper.unref();
  }
  return localCacheSingleton;
}

// ── Queue (local, in-process, worker-backed) ───────────────────────────────
//
// Like Cache, the deployed Queue is pod-local internal-api only (/internal-api/
// queue/push → the runtime's queue, drained by the Go processor running the
// project's workers/). There is no external route to proxy to. So serve runs the
// project's workers IN-PROCESS: workers/ is discovered + bundled at boot (the
// same esbuild path as controllers/), and Queue.push(worker, payload) schedules
// the matching defineWorker handler to run asynchronously, returning { jobId }.
//
// This is dev-only/non-durable: there is no persistence, retry/backoff, or DLQ —
// a thrown worker handler is logged (mirroring the prod processor's "job failed"
// log) and dropped. But the happy path WORKS: a controller that enqueues a job
// sees its worker actually run, so `palbase serve` exercises the full flow.

// workerRegistry: worker name → ResolvedWorkerConfig (from defineWorker). Rebuilt
// at boot (and not hot-reloaded — workers/ changes need a serve restart, like
// resources/). A module-level singleton so push() resolves against the live set.
const workerRegistry = new Map();

// registerWorkers discovers + bundles workers/, then registers each file's
// default-exported defineWorker config by name. Best-effort: a bundle error or a
// file without a valid worker export is logged and skipped so the rest of serve
// still runs (a project may have controllers but no workers, or a half-written
// worker). Returns the count registered.
function registerWorkers() {
  workerRegistry.clear();
  if (!fs.existsSync(WORKERS_DIR)) return 0;

  rmBundledTree(BUNDLED_WORKERS_DIR);
  try {
    if (!bundleSrcDir(WORKERS_DIR, BUNDLED_WORKERS_DIR)) return 0;
  } catch (err) {
    log(`⚠ esbuild failed for workers/ — ${esbuildErr(err)} — Queue.push will have no workers to run`);
    return 0;
  }

  let registered = 0;
  for (const file of walk(BUNDLED_WORKERS_DIR)) {
    if (!CONTROLLER_FILE_RE.test(path.basename(file))) continue;
    let mod;
    try {
      mod = require(file);
    } catch (err) {
      log(`workers: skipping ${path.join('workers', path.relative(BUNDLED_WORKERS_DIR, file))} — ${err.message}`);
      continue;
    }
    const cfg = mod && (mod.default ?? mod);
    // defineWorker returns a ResolvedWorkerConfig: { name, handler, retry, ... }.
    if (!cfg || typeof cfg !== 'object' || typeof cfg.name !== 'string' || typeof cfg.handler !== 'function') {
      log(`workers: skipping ${path.join('workers', path.relative(BUNDLED_WORKERS_DIR, file))} — not a defineWorker default export`);
      continue;
    }
    workerRegistry.set(cfg.name, cfg);
    registered += 1;
  }
  if (registered > 0) log(`registered ${registered} worker(s): ${[...workerRegistry.keys()].join(', ')}`);
  return registered;
}

// workerMeta builds the WorkerMeta passed to a worker handler — mirrors the SDK's
// WorkerMeta shape ({ env, user, requestId, projectId, environmentId }). Local
// dev has no per-branch secret injection, so env is the developer's own env (a
// copy, matching resourceEnvMap). The enqueuing user isn't threaded through the
// local queue, so user is null (background jobs are usually system-initiated).
function workerMeta() {
  return {
    env: Object.assign({}, process.env),
    user: null,
    requestId: `req_job_${Date.now().toString(36)}`,
    projectId: PROJECT_REF,
    environmentId: process.env.PALBASE_BRANCH || 'main',
  };
}

// makeLocalQueue returns the Queue singleton surface (push). push schedules the
// worker to run on the next tick (fire-and-forget, like enqueue → async drain in
// prod) and resolves immediately with a jobId so the caller never blocks on the
// job. A missing worker is logged (mirroring the prod processor's "no handler
// registered") but still returns a jobId — push's contract is "accepted", not
// "ran".
function makeLocalQueue() {
  let seq = 0;
  return {
    async push(worker, payload) {
      const jobId = `job_dev_${Date.now().toString(36)}_${(seq++).toString(36)}`;
      const cfg = workerRegistry.get(worker);
      if (!cfg) {
        log(`Queue.push: no worker registered for "${worker}" — job ${jobId} dropped ` +
          `(add workers/${worker}.ts with defineWorker({ name: "${worker}", ... }))`);
        return { jobId };
      }
      // Run async so push() returns immediately (the prod queue is async too).
      // installRuntime() has already wired the singletons the handler imports, so
      // the worker reaches Database/Cache/Log exactly as a controller does.
      setImmediate(async () => {
        try {
          await cfg.handler(payload, workerMeta());
          log(`Queue: job ${jobId} (${worker}) completed`);
        } catch (err) {
          // No retry/DLQ in dev — log like the prod processor's "job failed" and drop.
          log(`Queue: job ${jobId} (${worker}) failed — ${err && err.message ? err.message : String(err)}`);
        }
      });
      return { jobId };
    },
  };
}

let localQueueSingleton = null;
function localQueue() {
  if (!localQueueSingleton) localQueueSingleton = makeLocalQueue();
  return localQueueSingleton;
}

// installRuntime injects the dev service clients into the shared
// @palbase/backend instance, mirroring worker.js's runtime seam. Both
// dev-server.js (temp dir) and the user's controllers/handlers resolve
// @palbase/backend from the same project node_modules (NODE_PATH), so they
// share ONE module instance and a handler's `import { Database }` sees what we
// set here.
//
// Cache and Queue are LOCAL in-process implementations (the deployed pod backs
// them with pod-local internal-api/Redis, which has no external route serve could
// proxy to). They're dev-only/non-durable but functional, so a controller using
// Cache.*/Queue.* WORKS under serve instead of throwing a false "no credentials"
// error. Both are module-level singletons (built once) so cached values and the
// worker registry survive across the per-request installRuntime() calls.
//
// EXCLUDED on purpose: Realtime, Functions, CMS, Links, Analytics, Auth.
function installRuntime() {
  const clients = moduleClients();
  require('@palbase/backend').__setRuntime({
    Database: databaseClient(),
    Documents: clients.docs,
    Storage: clients.storage,
    Cache: localCache(),
    Queue: localQueue(),
    Log: makeLog(),
    Notifications: clients.notifications,
    Flags: clients.flags,
  });
}

// makeRequest builds the request-scoped object the dispatcher pulls injected
// params from (@Body→input, @Query→query, @Param→params, @Headers→headers,
// @User/@OptionalUser→user, @Client→client, @RequestId→requestId,
// @TraceId→traceId, @Req→the whole object). Services are NOT on it — they are
// the imported singletons (installed by installRuntime). Mirrors worker.js's
// pbReq. `req` here is the Node http request; `query` is the parsed URL query;
// `body` is the parsed JSON body. There is NO `errors` map — errors are the
// global throw classes.
function makeRequest(req, params, query, body, user) {
  // The handler-visible user MUST match prod's pbReq.user ({id, email?, role,
  // metadata}) — the raw Bearer is threaded to the Database edge separately (via
  // currentRequestUserToken) and is NEVER exposed to handler code. Strip `token`
  // here so `@User()`/`@Req()` never hand a secret to user endpoints.
  let publicUser = null;
  if (user) {
    const { token: _omitToken, ...rest } = user;
    publicUser = rest;
  }
  return {
    input: body ?? {},
    params,
    query: query ?? {},
    headers: req.headers || {},
    user: publicUser,
    client: clientInfoFromHeaders(req.headers || {}),
    file: null,
    method: req.method,
    requestId: `req_dev_${Date.now().toString(36)}`,
    traceId: '0'.repeat(32),
    spanId: '0'.repeat(16),
  };
}

function notConfiguredModule(name) {
  return new Proxy({}, {
    get() {
      throw new Error(
        `${name} unavailable: dev-server has no tenant credentials. ` +
        `Run \`palbase login\` then \`palbase serve\` from inside a project directory.`,
      );
    },
  });
}

// ── auth-required policy ──────────────────────────────────────────────────

// isAuthRequired is the SINGLE source of truth for "does this endpoint need a
// verified user?" — read by BOTH the request-time auth gate (below) AND the
// /openapi.json builder, so the served `security` block always matches what the
// dev-server actually enforces. Secure-by-default, mirroring the prod runtime
// (project memory: endpoint-auth-default — `auth` OMITTED is treated as
// REQUIRED, an explicit `auth: false` / `{ required: false }` is the only way
// to opt out). The `role` shorthand (`auth: { role: 'admin' }`) implies
// required too.
//
//   undefined            → required (secure-by-default)
//   true                 → required
//   false                → optional
//   { required: false }  → optional
//   { required: true }   → required
//   { role: 'x' }        → required (role without explicit required:false)
//   { required: false, role: 'x' } → optional
function isAuthRequired(auth) {
  if (auth === undefined || auth === null) return true; // omitted → secure-by-default
  if (typeof auth === 'boolean') return auth;
  if (typeof auth === 'object') {
    if (auth.required === false) return false;
    return auth.required === true || auth.role !== undefined || auth.required === undefined;
  }
  return true;
}

// ── /openapi.json — byte-identical to the prod backend-runtime spec ─────
//
// The deployed pod generates the spec in Go
// (modules/backend/internal/openapi/generator.go) from the same class-controller
// registry. `palbase mobile codegen` / `palbase types --env local` fetch
// /openapi.json to drive local codegen, so the LOCAL spec must match the REMOTE
// one the deployed pod serves — otherwise codegen output silently differs
// between local serve and the deployed pod. Request input is SPLIT by source:
// @Body → requestBody, @Query → query parameters, @Param → path parameters,
// @Headers → header parameters; @Returns → 200 response. Errors are the global
// throw classes (no per-route table), so only the generic 400/401 responses are
// emitted — exactly like generator.go.
//
// We rebuild the document fresh on every GET (not on the fs.watch reload):
// it's the simpler correct option — controllers already reload per request via
// resolveRoute()'s cache-bust, so building here picks up edits with zero
// extra bookkeeping, and a spec fetch is far rarer + cheaper than a hot path.
//
// Byte-shape rules (matched to Go's json.MarshalIndent, which sorts map keys
// lexicographically but emits struct fields in declaration order):
//   - 2-space indent.
//   - Struct-shaped objects (spec, info, operation-without-extensions,
//     requestBody, response, parameter, securityScheme) keep Go's field order,
//     reproduced here by INSERTING keys in that order (JSON.stringify preserves
//     insertion order for string keys).
//   - Map-shaped objects (paths, path-item methods, responses, securitySchemes,
//     security requirements, x-rate-limit, x-palbase-errors, and the opaque
//     zod-to-json-schema bodies) sort keys — reproduced by inserting in sorted
//     order (and sortDeep() for the opaque schema sub-trees).
//   - An operation WITH x- extensions re-marshals as a flat map in Go, so ALL
//     its top-level keys sort alphabetically.

// zod-to-json-schema is resolved lazily from the project node_modules via the
// same NODE_PATH the dev-server already uses for @palbase/backend. Older
// projects may not have it; we cache the (possibly null) result so we only warn
// once and never crash the route.
let zodToJsonSchemaFn; // undefined = not tried, null = absent, fn = loaded
function getZodToJsonSchema() {
  if (zodToJsonSchemaFn !== undefined) return zodToJsonSchemaFn;
  try {
    // eslint-disable-next-line global-require
    const mod = require('zod-to-json-schema');
    zodToJsonSchemaFn = mod.zodToJsonSchema || mod.default || null;
  } catch {
    zodToJsonSchemaFn = null;
  }
  if (!zodToJsonSchemaFn) {
    log('hint: zod-to-json-schema not found in node_modules — /openapi.json ' +
      'will omit request/response/header schemas. `npm i zod-to-json-schema` ' +
      'for full local codegen parity.');
  }
  return zodToJsonSchemaFn;
}

// zodToJSON converts a Zod schema to the same Draft-7 JSON Schema body the prod
// extractor emits (target 'jsonSchema7', outer $schema stripped). Returns null
// when the dep is absent or the value isn't a Zod schema — callers then omit
// the body, matching the prod "no schema declared" path. sortDeep() makes the
// opaque body's object keys match Go's sorted map marshaling.
function zodToJSON(z) {
  const conv = getZodToJsonSchema();
  if (!conv) return null;
  if (!z || typeof z._def !== 'object') return null;
  try {
    const schema = conv(z, { target: 'jsonSchema7' });
    if (schema && typeof schema === 'object') {
      const { $schema, ...rest } = schema;
      return sortDeep(rest);
    }
    return null;
  } catch {
    return null;
  }
}

// sortDeep recursively sorts object keys (arrays preserved positionally) so an
// opaque sub-tree byte-matches Go's lexicographic map key ordering.
function sortDeep(value) {
  if (Array.isArray(value)) return value.map(sortDeep);
  if (value && typeof value === 'object') {
    const out = {};
    for (const key of Object.keys(value).sort()) out[key] = sortDeep(value[key]);
    return out;
  }
  return value;
}

// capitalize upcases the FIRST byte only — identical to the Go generator's
// capitalize (which slices s[:1]); a no-op for empty strings.
function capitalize(s) {
  if (!s) return s;
  return s[0].toUpperCase() + s.slice(1);
}

// openApiPath converts a dev-server urlPattern (`:id` colon params, from `[id]`
// dirs) to the OpenAPI/Chi `{id}` form the prod path keys use.
function openApiPath(urlPattern) {
  return urlPattern.replace(/:([^/]+)/g, '{$1}');
}

// deriveControllerName lowers a controller CLASS name into the dotted
// operationId namespace: strip ONE trailing "Controller" suffix
// (case-sensitive), then lower-case the first letter.
// MembersController → "members", UserProfileController → "userProfile",
// Members → "members". Returns "" when the derivation is empty (a class named
// exactly "Controller", or an anonymous class) — buildOperation then falls
// back to the flat operationId. MUST stay byte-for-byte identical to
// deriveControllerName in the prod extractor (modules/backend
// internal/runtime/extract_meta.js) and @palbase/backend's
// openapi/discover.ts — the three spec twins.
function deriveControllerName(Ctrl) {
  let name = typeof Ctrl === 'function' && typeof Ctrl.name === 'string' ? Ctrl.name : '';
  if (name.endsWith('Controller')) name = name.slice(0, -'Controller'.length);
  if (name === '') return '';
  return name.charAt(0).toLowerCase() + name.slice(1);
}

// operationId mirrors generator.go's flatOperationID — the FALLBACK id used
// when no controllerName can be derived: toLower(method) + per non-empty path
// segment, `By`+Capitalize(param) for `{param}` else Capitalize(segment).
// e.g. POST /todos/create → postTodosCreate; GET /rooms/{id} → getRoomsById.
// Controller routes normally get the dotted `<controllerName>.<fnName>` id
// instead (see buildOperation), matching generator.go's operationID.
function operationId(method, openPath) {
  let out = method.toLowerCase();
  for (const seg of openPath.split('/')) {
    if (!seg) continue;
    if (seg.startsWith('{') && seg.endsWith('}')) {
      out += 'By' + capitalize(seg.slice(1, -1));
    } else {
      out += capitalize(seg);
    }
  }
  return out;
}

// schemaParameters lowers an object JSON Schema into a name-sorted array of
// OpenAPI parameters with the given `in` location ("path"/"query"/"header") —
// matching generator.go's schemaParameters (struct order: name, in, required,
// description?, schema). Required-ness comes from the schema's `required` array,
// EXCEPT for path params which OpenAPI mandates be required:true regardless.
function schemaParameters(schema, where) {
  if (!schema || typeof schema !== 'object') return [];
  const props = schema.properties;
  if (!props || typeof props !== 'object') return [];
  const required = new Set(Array.isArray(schema.required) ? schema.required : []);
  const out = [];
  for (const name of Object.keys(props).sort()) {
    const prop = props[name] && typeof props[name] === 'object' ? props[name] : {};
    const param = { name, in: where, required: where === 'path' || required.has(name) };
    if (typeof prop.description === 'string' && prop.description !== '') {
      param.description = prop.description;
    }
    param.schema = sortDeep(props[name]);
    out.push(param);
  }
  return out;
}

// paramsSchemaFromRoute synthesizes the @Param JSON Schema — one
// {type:"string"} property per @Param("name"), all required — matching
// extract_meta.js / generator.go. Returns null when the route has no @Param.
function paramsSchemaFromRoute(route) {
  const names = [];
  for (const p of (route.params || [])) {
    if (p && p.kind === 'param' && typeof p.name === 'string' && p.name !== '') names.push(p.name);
  }
  if (names.length === 0) return null;
  const properties = {};
  for (const name of names.sort()) properties[name] = { type: 'string' };
  return { type: 'object', properties, required: names.slice() };
}

// buildRouteMeta reads the spec-relevant fields off a route's live registry
// entry. Input is SPLIT by source (body/query/params/headers) — each schema read
// from the route's ParamMeta list (the live zod on a @Body/@Query/@Headers
// param, or the synthesized @Param object). Output is @Returns. method + path
// come from the route (controller basePath + subpath) — matching the
// dev-server's own dispatch. auth uses the resolved effectiveAuth + the shared
// isAuthRequired() so spec == enforcement. controllerName + fnName feed the
// dotted operationId (`<controllerName>.<fnName>`, flat fallback when empty).
function buildRouteMeta(route) {
  const auth = route.effectiveAuth;
  const bodyParam = findRouteParam(route.params, 'body');
  const queryParam = findRouteParam(route.params, 'query');
  const headersParam = findRouteParam(route.params, 'headers');
  return {
    method: route.method,
    openPath: openApiPath(route.urlPattern),
    controllerName: typeof route.controllerName === 'string' ? route.controllerName : '',
    fnName: typeof route.fnName === 'string' ? route.fnName : '',
    authRequired: isAuthRequired(auth),
    authRole: (auth && typeof auth === 'object' && typeof auth.role === 'string') ? auth.role : '',
    rateLimit: (route.rateLimit && typeof route.rateLimit === 'object'
      && typeof route.rateLimit.max === 'number'
      && typeof route.rateLimit.window === 'number')
      ? { max: route.rateLimit.max, window: route.rateLimit.window } : null,
    bodySchema: bodyParam && bodyParam.schema ? zodToJSON(bodyParam.schema) : null,
    querySchema: queryParam && queryParam.schema ? zodToJSON(queryParam.schema) : null,
    paramsSchema: paramsSchemaFromRoute(route),
    headersSchema: headersParam && headersParam.schema ? zodToJSON(headersParam.schema) : null,
    outputSchema: route.returnSchema ? zodToJSON(route.returnSchema) : null,
    throws: resolveRouteThrows(route),
  };
}

// resolveRouteThrows joins the route's inferred throw descriptors (stamped by
// the staged recordThrows IIFEs) with the SDK error registry — by CODE, the
// single source of truth for status/data schema. Unknown codes are skipped
// silently and a stale SDK (no getErrorRegistry) degrades to no errors,
// exactly like extract_meta.js on the deploy side.
function resolveRouteThrows(route) {
  if (!Array.isArray(route.throws) || route.throws.length === 0) return [];
  let sdk;
  try {
    sdk = runtimeModule();
  } catch {
    return [];
  }
  if (!sdk || typeof sdk.getErrorRegistry !== 'function') return [];
  const registry = sdk.getErrorRegistry();
  const out = [];
  for (const t of route.throws) {
    if (!t || typeof t.code !== 'string' || typeof t.name !== 'string') continue;
    const reg = registry.get(t.code);
    if (!reg) continue;
    const entry = { name: t.name, code: t.code, status: reg.status, hasData: Boolean(reg.dataSchema) };
    if (reg.dataSchema) {
      const js = zodToJSON(reg.dataSchema);
      if (js) entry.dataJsonSchema = js;
    }
    out.push(entry);
  }
  return out;
}

// buildOperation assembles one OpenAPI operation object with keys inserted in
// Go's exact emission order (generator.go). When the x-rate-limit extension is
// present Go re-marshals the whole operation as a flat map (all keys sorted
// alpha), so we sort the top-level keys in that case — see sortOperationKeys.
// Errors are global throw classes: only the generic 400/401 responses are
// emitted (no per-route table, no x-palbase-errors).
function buildOperation(meta) {
  const op = {};
  // struct field order: summary, operationId, tags, security, parameters,
  // requestBody, responses (summary/tags omitted — dev-server has no
  // description/tags on the route; they'd be empty).
  //
  // operationId is DOTTED `<controllerName>.<fnName>` (members.getUser) when
  // both are present — generator.go's operationID — with the flat
  // verb-prefixed derivation as the fallback (empty controllerName).
  op.operationId = (meta.controllerName && meta.fnName)
    ? meta.controllerName + '.' + meta.fnName
    : operationId(meta.method, meta.openPath);

  if (meta.authRequired) {
    // bearerAuth then apiKey — generator.go emits them in that array order.
    op.security = [{ bearerAuth: [] }, { apiKey: [] }];
  } else {
    // explicit opt-out → empty requirement (auth optional).
    op.security = [{}];
  }

  // Parameters: path (@Param) + query (@Query) + header (@Headers), appended in
  // that group order, each name-sorted — matching generator.go's addEndpoint.
  let params = [];
  if (meta.paramsSchema) params = params.concat(schemaParameters(meta.paramsSchema, 'path'));
  if (meta.querySchema) params = params.concat(schemaParameters(meta.querySchema, 'query'));
  if (meta.headersSchema) params = params.concat(schemaParameters(meta.headersSchema, 'header'));
  if (params.length > 0) op.parameters = params;

  if (meta.bodySchema) {
    op.requestBody = {
      required: true,
      content: { 'application/json': { schema: meta.bodySchema } },
    };
  }

  // responses — status keys inserted in sorted order ("200" < "400" < "401").
  // 200 always; 400 always; 401 only when auth required.
  const responses = {};
  const responseEntries = [];
  if (meta.outputSchema) {
    responseEntries.push(['200', {
      description: 'Successful response',
      content: { 'application/json': { schema: meta.outputSchema } },
    }]);
  } else {
    responseEntries.push(['200', { description: 'Successful response' }]);
  }
  responseEntries.push(['400', { description: 'Bad request' }]);
  if (meta.authRequired) {
    responseEntries.push(['401', { description: 'Unauthorized' }]);
  }
  // Typed-error responses are pushed AFTER the static placeholders so a typed
  // envelope on the same status OVERWRITES the generic one (the Map below is
  // last-wins) — generator.go's exact rule.
  for (const [status, response] of errorResponseEntries(meta.throws || [])) {
    responseEntries.push([status, response]);
  }

  const extensions = {};
  // x-palbase-errors (wire format v1 — swiftgen's declaredErrors parses
  // exactly this; byte-shape twin of generator.go errorsExtension).
  if (meta.throws && meta.throws.length > 0) {
    extensions['x-palbase-errors'] = errorsExtension(meta.throws);
  }
  // x-rate-limit extension (generator.go's other operation extension).
  if (meta.rateLimit) {
    extensions['x-rate-limit'] = { max: meta.rateLimit.max, window: meta.rateLimit.window };
  }

  // Insert response status keys in sorted order ("200" < "400" < "401").
  const byCode = new Map(responseEntries);
  for (const code of [...byCode.keys()].sort(compareStatus)) {
    responses[code] = byCode.get(code);
  }
  op.responses = responses;

  if (Object.keys(extensions).length > 0) {
    for (const [k, v] of Object.entries(extensions)) op[k] = v;
    return sortOperationKeys(op);
  }
  return op;
}

// compareStatus sorts status-code strings the way Go sorts its responses map
// keys: lexicographically as strings ("200" < "400" < "401" < "404" < "409").
function compareStatus(a, b) {
  return a < b ? -1 : a > b ? 1 : 0;
}

// errorsExtension — byte-shape twin of generator.go errorsExtension. Key =
// lowerCamel(class name); when several errors share a class name ALL of them
// disambiguate as "<name>_<code>" (order-independent). Keys emitted sorted
// (Go marshals map keys sorted).
function errorsExtension(throws) {
  const nameCount = new Map();
  for (const t of throws) {
    const k = lowerCamelName(t.name);
    nameCount.set(k, (nameCount.get(k) || 0) + 1);
  }
  const entries = [];
  for (const t of throws) {
    let key = lowerCamelName(t.name);
    if (nameCount.get(key) > 1) key = key + '_' + t.code;
    entries.push([key, {
      // Go marshals map[string]any with sorted keys: code, description,
      // hasData, status.
      code: t.code,
      description: '',
      hasData: t.hasData,
      status: t.status,
    }]);
  }
  entries.sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
  const out = {};
  for (const [k, v] of entries) out[k] = v;
  return out;
}

// errorResponseEntries — byte-shape twin of generator.go errorResponses:
// group by status, sort each group by code, single → bare envelope schema,
// multiple → oneOf (variants in code order), description "Error: a | b".
function errorResponseEntries(throws) {
  const byStatus = new Map();
  for (const t of throws) {
    const key = String(t.status);
    if (!byStatus.has(key)) byStatus.set(key, []);
    byStatus.get(key).push(t);
  }
  const out = [];
  for (const [status, group] of byStatus) {
    group.sort((a, b) => (a.code < b.code ? -1 : a.code > b.code ? 1 : 0));
    const schema = group.length === 1
      ? errorEnvelopeSchema(group[0])
      : { oneOf: group.map(errorEnvelopeSchema) };
    out.push([status, {
      // Alphabetical key order (content, description) — Go struct marshal.
      content: { 'application/json': { schema } },
      description: 'Error: ' + group.map((t) => t.code).join(' | '),
    }]);
  }
  return out;
}

// errorEnvelopeSchema — twin of generator.go errorEnvelopeSchema. Properties
// inserted in Go's sorted-map order: data?, error, error_description,
// request_id, status; top level: properties, required, type.
function errorEnvelopeSchema(t) {
  const props = {};
  if (t.hasData && t.dataJsonSchema) props.data = t.dataJsonSchema;
  props.error = { const: t.code };
  props.error_description = { type: 'string' };
  props.request_id = { type: 'string' };
  props.status = { type: 'integer' };
  return {
    properties: props,
    required: ['error', 'error_description', 'status'],
    type: 'object',
  };
}

// lowerCamelName — twin of generator.go lowerCamel ("TodoLocked" →
// "todoLocked", "NotFound" → "notFound").
function lowerCamelName(name) {
  if (!name) return name;
  return name.charAt(0).toLowerCase() + name.slice(1);
}

// sortOperationKeys re-emits an operation's TOP-LEVEL keys alphabetically,
// matching Go's behaviour when x- extensions force the whole operation to
// marshal as a flat map. Values keep their own (already-correct) ordering.
function sortOperationKeys(op) {
  const out = {};
  for (const key of Object.keys(op).sort()) out[key] = op[key];
  return out;
}

// buildOpenApiSpec walks the route table and produces the full document with
// keys ordered to byte-match the prod Go generator. A module that throws on
// require is skipped (logged) — one bad endpoint must not break the whole spec,
// mirroring the dispatcher's load-error tolerance.
function buildOpenApiSpec() {
  // paths: { <path>: { <method>: <operation> } }. Build into a plain map first,
  // then re-emit with sorted path keys and sorted method keys.
  const rawPaths = new Map();
  for (const route of routes.values()) {
    let resolved;
    try {
      resolved = resolveRoute(route);
    } catch (err) {
      log(`openapi: skipping ${route.method} ${route.urlPattern} — ${err.message}`);
      continue;
    }
    // Carry the registration's effectiveAuth/rateLimit/urlPattern (and the
    // controllerName derived from the live class at registration) onto the
    // live route meta so buildRouteMeta reads the resolved cascade + the path
    // the dev-server actually matches.
    const liveRoute = Object.assign({}, resolved.route, {
      method: route.method,
      urlPattern: route.urlPattern,
      effectiveAuth: route.effectiveAuth,
      rateLimit: route.rateLimit,
      controllerName: route.controllerName,
    });
    const meta = buildRouteMeta(liveRoute);
    const operation = buildOperation(meta);
    if (!rawPaths.has(meta.openPath)) rawPaths.set(meta.openPath, {});
    rawPaths.get(meta.openPath)[route.method.toLowerCase()] = operation;
  }

  const paths = {};
  for (const pathKey of [...rawPaths.keys()].sort()) {
    const item = rawPaths.get(pathKey);
    const sortedItem = {};
    for (const method of Object.keys(item).sort()) sortedItem[method] = item[method];
    paths[pathKey] = sortedItem;
  }

  // Top-level + info + components in Go struct field order; the dynamic map
  // bodies (paths, securitySchemes) are inserted in sorted order.
  return {
    openapi: '3.1.0',
    info: {
      title: 'Palbase Backend',
      version: '1.0.0',
      description: 'Auto-generated from @Controller class decorators.',
    },
    // servers OMITTED — prod's ServerURL is empty for the deployed spec.
    paths,
    components: {
      securitySchemes: {
        // sorted map keys: apiKey < bearerAuth.
        apiKey: { type: 'apiKey', name: 'apikey', in: 'header' },
        bearerAuth: { type: 'http', scheme: 'bearer', bearerFormat: 'JWT' },
      },
    },
  };
}

// ── auth: real palauth /auth/user verify ──────────────────────────────
//
// Bit-perfect with prod: pod and dev-server BOTH ask palauth to verify
// Bearer tokens (signature + DB lookup) — no local crypto, no
// decode-only fallback. Token cache (30s) keyed by raw token string so
// hot endpoints aren't dominated by RTT.

function decodeJwtPayload(token) {
  try {
    const parts = token.split('.');
    if (parts.length !== 3) return null;
    const padded = parts[1].replace(/-/g, '+').replace(/_/g, '/').padEnd(parts[1].length + ((4 - parts[1].length % 4) % 4), '=');
    return JSON.parse(Buffer.from(padded, 'base64').toString('utf8'));
  } catch {
    return null;
  }
}

const tokenCache = new Map();
const TOKEN_TTL_MS = 30_000;

async function verifyTokenViaPalauth(token) {
  if (!PALBASE_URL || !TENANT_APIKEY) return { ok: false, reason: 'dev_not_configured' };

  const cached = tokenCache.get(token);
  if (cached && cached.expires > Date.now()) return cached.value;

  let resp;
  try {
    resp = await fetch(`${PALBASE_URL}/auth/user`, {
      headers: {
        Authorization: `Bearer ${token}`,
        apikey: TENANT_APIKEY,
      },
    });
  } catch (err) {
    return { ok: false, reason: 'palauth_unreachable', detail: err.message };
  }
  if (resp.status === 401) {
    // Surface what palauth actually said + show a token fingerprint so
    // the dev can spot stale-keychain-vs-fresh-token mismatches without
    // dumping the whole JWT to stdout.
    let body = '';
    try { body = await resp.text(); } catch {}
    const claims = decodeJwtPayload(token) || {};
    const fp = `sub=${claims.sub || '?'} exp=${claims.exp || '?'} iat=${claims.iat || '?'}`;
    log(`palauth /auth/user → 401 (${fp}): ${body.slice(0, 240)}`);
    const value = { ok: false, reason: 'invalid_token' };
    tokenCache.set(token, { value, expires: Date.now() + TOKEN_TTL_MS });
    return value;
  }
  if (!resp.ok) {
    let body = '';
    try { body = await resp.text(); } catch {}
    log(`palauth /auth/user → ${resp.status}: ${body.slice(0, 240)}`);
    return { ok: false, reason: 'palauth_error', status: resp.status };
  }
  let profile;
  try {
    profile = await resp.json();
  } catch (err) {
    return { ok: false, reason: 'palauth_bad_response', detail: err.message };
  }
  // palauth verified the JWT. Pull role/metadata from the now-trusted
  // payload — same shortcut the Go pipeline uses.
  const claims = decodeJwtPayload(token) || {};
  const value = {
    ok: true,
    user: {
      id: profile.id,
      email: profile.email || '',
      role: typeof claims.role === 'string' ? claims.role : 'authenticated',
      metadata: claims.metadata && typeof claims.metadata === 'object' ? claims.metadata : {},
      // RETAIN the raw Bearer so the Database edge client can forward it on the
      // authenticated RLS path (the edge derives identity from this). NEVER
      // logged — only the JWT fingerprint (sub/exp/iat) is ever printed.
      token,
    },
  };
  tokenCache.set(token, { value, expires: Date.now() + TOKEN_TTL_MS });
  return value;
}

// Token-bucket-ish rate limiter keyed by (route + remote IP). Lives in
// memory for the dev server's lifetime — perfect for one-off sanity
// checks of `rateLimit: { max, window }` configs.
const rateBuckets = new Map();

function rateLimitCheck(routeKey, ip, cfg) {
  const now = Date.now();
  const windowMs = cfg.window * 1000;
  const key = `${routeKey}|${ip}`;
  const bucket = rateBuckets.get(key) || [];
  // Drop entries older than the window.
  const fresh = bucket.filter((t) => now - t < windowMs);
  if (fresh.length >= cfg.max) {
    rateBuckets.set(key, fresh);
    const retryAfterMs = windowMs - (now - fresh[0]);
    return { allowed: false, retryAfter: Math.ceil(retryAfterMs / 1000), remaining: 0 };
  }
  fresh.push(now);
  rateBuckets.set(key, fresh);
  return { allowed: true, retryAfter: 0, remaining: cfg.max - fresh.length };
}

// ── http ────────────────────────────────────────────────────────────────

const server = http.createServer(async (req, res) => {
  const start = Date.now();
  const parsed = url.parse(req.url, true);

  // CORS for local dev: a Next.js app on localhost:3000 calling this dev server
  // on :4003 is cross-origin, so without these headers the browser blocks every
  // request (and the OPTIONS preflight 405'd). REFLECT the request Origin rather
  // than send `*` — that keeps credentialed requests (cookies/Authorization)
  // working, which `*` forbids. This is the LOCAL dev server only (deployed
  // tenants sit behind Kong, which owns prod CORS); it is never the production
  // path, so reflecting origin here is not the wildcard-CORS anti-pattern.
  const origin = req.headers.origin;
  if (origin) {
    res.setHeader('Access-Control-Allow-Origin', origin);
    res.setHeader('Vary', 'Origin');
    res.setHeader('Access-Control-Allow-Credentials', 'true');
  }
  if (req.method === 'OPTIONS') {
    res.setHeader('Access-Control-Allow-Methods', 'GET,POST,PATCH,PUT,DELETE,QUERY,OPTIONS');
    res.setHeader(
      'Access-Control-Allow-Headers',
      req.headers['access-control-request-headers'] || 'authorization,apikey,content-type',
    );
    res.setHeader('Access-Control-Max-Age', '86400');
    res.statusCode = 204;
    res.end();
    return;
  }

  // /openapi.json — served before route matching so it never falls through to
  // the 404 path. Built fresh per request (see buildOpenApiSpec comment) and
  // serialized with 2-space indent. Never crashes the route: any failure logs
  // and returns 500 with a json envelope, same as the other error paths.
  if (req.method === 'GET' && parsed.pathname === '/openapi.json') {
    try {
      const spec = buildOpenApiSpec();
      const bodyJson = JSON.stringify(spec, null, 2);
      res.statusCode = 200;
      res.setHeader('content-type', 'application/json');
      res.end(bodyJson);
      log(`[GET] /openapi.json  200  ${Date.now() - start}ms  (${routes.size} route(s))`);
    } catch (err) {
      res.statusCode = 500;
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ error: 'openapi_error', message: err.message }));
      log(`[GET] /openapi.json  500  ${Date.now() - start}ms  — ${err.message}`);
    }
    return;
  }

  let route = null;
  let match = null;
  for (const r of routes.values()) {
    if (r.method !== req.method) continue;
    const m = r.regex.exec(parsed.pathname);
    if (m) { route = r; match = m; break; }
  }
  if (!route) {
    // Not one of THIS project's controllers. It may still be a tenant-module
    // route the live gateway serves (auth, flags, docs, storage, …). `palbase
    // serve` runs YOUR controllers locally but is otherwise a transparent
    // window onto the deployed tenant — same as it proxies Database/ctx.* to the
    // live branch. So forward unknown module routes to the tenant gateway
    // (PALBASE_URL) with the apikey + the caller's Authorization preserved.
    // Everything else is a genuine 404.
    if (PALBASE_URL && TENANT_APIKEY && isTenantModuleRoute(parsed.pathname)) {
      await proxyToTenant(req, res, parsed, start);
      return;
    }
    res.statusCode = 404;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'not_found', path: parsed.pathname, method: req.method }));
    return;
  }
  const params = {};
  route.paramNames.forEach((name, i) => { params[name] = match[i + 1]; });

  // Split request sources: body (POST/PUT/PATCH/DELETE/QUERY JSON) → pbReq.input,
  // URL query → pbReq.query. NOT folded together: the class controller injects
  // @Body and @QueryParams separately (worker.js keeps context.input vs
  // context.query distinct), so a GET with `?name=x` must land in query, never input.
  let body = null;
  if (req.method !== 'GET' && req.method !== 'HEAD') {
    body = await readJsonBody(req);
  }
  const query = parsed.query || {};

  // Resolve the live route + cached class instance (cache-busted require so a
  // save takes effect without a restart) — mirrors worker.js's dispatch
  // resolution by fnName off the class registry.
  let resolved;
  try {
    resolved = resolveRoute(route);
  } catch (err) {
    res.statusCode = 500;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'load_error', message: err.message }));
    log(`[${req.method}] ${parsed.pathname}  500  ${Date.now() - start}ms  — ${err.message}`);
    return;
  }
  const { instance, route: liveRoute } = resolved;
  const routeParams = Array.isArray(liveRoute.params) ? liveRoute.params : [];

  // Auth gate — same contract as prod: optional => no token = pass with
  // user=null, present token = palauth verify. Required => must have a token AND
  // palauth must accept it. effectiveAuth is the resolved route→controller
  // cascade (route.options.auth ?? controller.defaultAuth ?? true); isAuthRequired
  // lowers it to the enforcement decision so the gate here and the served
  // /openapi.json `security` block never drift (omitted → required,
  // secure-by-default).
  const authSpec = route.effectiveAuth;
  const authRole = (authSpec && typeof authSpec === 'object' && typeof authSpec.role === 'string') ? authSpec.role : '';
  const authRequired = isAuthRequired(authSpec);
  const authHeader = req.headers['authorization'] || '';
  let user = null;
  if (authHeader.toLowerCase().startsWith('bearer ')) {
    const token = authHeader.slice(7).trim();
    const verifyResult = await verifyTokenViaPalauth(token);
    if (verifyResult.ok) {
      user = verifyResult.user;
    } else if (authRequired) {
      const status = verifyResult.reason === 'invalid_token' ? 401 : 502;
      res.statusCode = status;
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({
        error: verifyResult.reason,
        message: status === 401 ? 'Token is invalid or expired' : 'Could not reach palauth to verify the token',
      }));
      log(`[${req.method}] ${parsed.pathname}  ${status}  ${Date.now() - start}ms  — ${verifyResult.reason}`);
      return;
    }
  }
  if (authRequired && !user) {
    res.statusCode = 401;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'unauthorized', message: 'Authorization header required' }));
    log(`[${req.method}] ${parsed.pathname}  401  ${Date.now() - start}ms  — auth required`);
    return;
  }
  if (authRole && user && user.role !== authRole) {
    res.statusCode = 403;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'forbidden', message: `role ${authRole} required, got ${user.role}` }));
    log(`[${req.method}] ${parsed.pathname}  403  ${Date.now() - start}ms  — role mismatch`);
    return;
  }

  // Rate limit gate (route.options.rateLimit).
  const rateLimit = route.rateLimit;
  if (rateLimit && typeof rateLimit === 'object' && typeof rateLimit.max === 'number' && typeof rateLimit.window === 'number') {
    const ip = (req.headers['x-forwarded-for'] || req.socket.remoteAddress || 'local').toString().split(',')[0].trim();
    const result = rateLimitCheck(`${route.method} ${route.urlPattern}`, ip, rateLimit);
    res.setHeader('x-ratelimit-limit', String(rateLimit.max));
    res.setHeader('x-ratelimit-remaining', String(Math.max(0, result.remaining)));
    if (!result.allowed) {
      res.statusCode = 429;
      res.setHeader('retry-after', String(result.retryAfter));
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({
        error: 'rate_limited',
        message: `max ${rateLimit.max} req / ${rateLimit.window}s`,
        retry_after: result.retryAfter,
      }));
      log(`[${req.method}] ${parsed.pathname}  429  ${Date.now() - start}ms  — rate limited (retry in ${result.retryAfter}s)`);
      return;
    }
  }

  installRuntime();
  const pbReq = makeRequest(req, params, query, body, user);

  // --- Validate present source schemas off the LIVE registry ---------------
  // Each @Body/@QueryParams/@Headers ParamMeta carries its live zod schema; @Param
  // synthesizes required string path params. A failure → 400 with zod issues.
  // Parsed/stripped values replace the raw source. Mirrors worker.js exactly.

  // Body (from @Body(zod)).
  const bodyParam = findRouteParam(routeParams, 'body');
  if (bodyParam && bodyParam.schema && typeof bodyParam.schema.safeParse === 'function') {
    const parsedBody = bodyParam.schema.safeParse(pbReq.input);
    if (!parsedBody.success) {
      send400(res, 'Input validation failed', parsedBody.error, 'input', req, parsed, start);
      return;
    }
    pbReq.input = parsedBody.data;
  }

  // Query (from @QueryParams(zod)).
  const queryParam = findRouteParam(routeParams, 'query');
  if (queryParam && queryParam.schema && typeof queryParam.schema.safeParse === 'function') {
    const parsedQuery = queryParam.schema.safeParse(pbReq.query);
    if (!parsedQuery.success) {
      send400(res, 'Query validation failed', parsedQuery.error, 'query', req, parsed, start);
      return;
    }
    pbReq.query = parsedQuery.data;
  }

  // Headers (from @Headers(zod)). HTTP header names are case-insensitive, so
  // match against a lowercase view, project the declared keys, parse, then merge
  // the typed values back. Mirrors worker.js.
  let parsedHeaders = pbReq.headers;
  const headersParam = findRouteParam(routeParams, 'headers');
  if (headersParam && headersParam.schema && typeof headersParam.schema.safeParse === 'function') {
    const headersSchema = headersParam.schema;
    const lower = {};
    for (const k of Object.keys(pbReq.headers || {})) lower[k.toLowerCase()] = pbReq.headers[k];
    const shape = headersSchema._def && headersSchema._def.shape;
    const shapeObj = typeof shape === 'function' ? shape() : shape;
    const declared = {};
    for (const key of Object.keys(shapeObj || {})) {
      const v = lower[key.toLowerCase()];
      if (v !== undefined) declared[key] = v;
    }
    const parsedH = headersSchema.safeParse(declared);
    if (!parsedH.success) {
      send400(res, 'Header validation failed', parsedH.error, 'header', req, parsed, start);
      return;
    }
    parsedHeaders = Object.assign({}, pbReq.headers, parsedH.data);
    pbReq.headers = parsedHeaders;
  }

  // Path params (synthesized from @Param("name")). Each is a string on the wire;
  // absence is a 400 (a route declares @Param only for a path segment that must
  // exist). Mirrors worker.js.
  const pathParams = pbReq.params || {};
  for (const p of routeParams) {
    if (p && p.kind === 'param' && typeof p.name === 'string') {
      if (pathParams[p.name] === undefined) {
        send400Detail(res, 'Path parameter validation failed', [{ field: p.name, message: 'required path parameter is missing' }], req, parsed, start);
        return;
      }
    }
  }

  // --- Build the args array ordered by ParamMeta.index ----------------------
  // Each param decorator injects its piece directly. @User/@OptionalUser both
  // resolve to pbReq.user (null on a public/anon request). Mirrors worker.js.
  const args = [];
  for (const p of routeParams) {
    if (!p || typeof p.index !== 'number') continue;
    let value;
    switch (p.kind) {
      case 'body':         value = pbReq.input; break;
      case 'query':        value = pbReq.query; break;
      case 'param':        value = pathParams[p.name]; break;
      case 'headers':      value = parsedHeaders; break;
      case 'user':         value = pbReq.user; break;
      case 'optionalUser': value = pbReq.user; break;
      case 'client':       value = pbReq.client; break;
      case 'requestId':    value = pbReq.requestId; break;
      case 'traceId':      value = pbReq.traceId; break;
      case 'req':          value = pbReq; break;
      default:             value = undefined; break;
    }
    args[p.index] = value;
  }

  // This request's identity box, scoped to the handler's async call tree via
  // ALS so it survives awaits and NEVER leaks into a concurrent request:
  //   - userId  → the flags client's auto-bind getter (`Flags.get('key')`
  //     resolves user-override → default without a manual { userId }).
  //   - userToken → the Database edge forwards this Bearer on the authenticated
  //     RLS path. Sourced from the verify-result `user` (which RETAINS the
  //     token) — NOT pbReq.user, which is stripped of the token before handlers
  //     see it. NEVER logged.
  // Anonymous → both null → project defaults / anon edge.
  const requestBox = {
    userId: (pbReq.user && pbReq.user.id) || null,
    userToken: (user && user.token) || null,
  };
  let result;
  try {
    // Execute the controller method bound to the cached instance so `this`
    // resolves the controller's fields/services — inside the ALS scope so every
    // ctx.db / ctx.flags call the handler makes (across any await) reads THIS
    // request's identity, not a concurrently-dispatched one.
    result = await runInRequestContext(requestBox, () => instance[liveRoute.fnName](...args));
  } catch (err) {
    // The SDK's global throw classes (Conflict/NotFound/BadRequest/… and the
    // base PalError/HttpError) all extend HttpError and set `.status` (number)
    // + `.error` (string code). They are a HANDLER OUTCOME, not infra failure:
    // surface the status + envelope verbatim. Match by SHAPE, not name —
    // mirroring worker.js's catch path exactly.
    if (err instanceof Error && typeof err.status === 'number' && typeof err.error === 'string') {
      // Typed-errors conformance guard (mirrors worker.js byte-for-byte): an
      // analyzed route (throws meta present, EMPTY INCLUDED) throwing a code
      // that is neither inferred nor a built-in means the throw site was
      // statically invisible — say so loudly at first occurrence. The
      // response envelope is untouched.
      if (Array.isArray(liveRoute.throws)
        && !liveRoute.throws.some((t) => t && t.code === err.error)
        && !GUARD_BUILTIN_CODES.has(err.error)) {
        // The live class NAME (worker.js twin prints TodosController, not the
        // lowered namespace).
        const ctrl = (instance && instance.constructor && instance.constructor.name)
          || (route.controllerName && String(route.controllerName)) || 'Controller';
        console.error(
          `[palbase] ${ctrl}.${liveRoute.fnName} threw "${err.error}" — statically invisible throw site; ` +
          'make the throw resolvable (direct throw / singleton service method) so typed clients see it',
        );
      }
      const envelope = {
        error: err.error,
        error_description: err.errorDescription || err.message || 'request failed',
        status: err.status,
      };
      if (err.data !== undefined) envelope.data = err.data;
      res.statusCode = err.status;
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify(envelope));
      log(`[${req.method}] ${parsed.pathname}  ${err.status}  ${Date.now() - start}ms  — ${envelope.error}`);
      return;
    }
    // Don't leak the stack (absolute file paths, internal line numbers) in the
    // HTTP response — it's noise for the client and an info-leak. Print it to the
    // serve terminal for the developer, and return a clean envelope matching the
    // platform error shape.
    res.statusCode = 500;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'handler_error', error_description: err.message, status: 500 }));
    log(`[${req.method}] ${parsed.pathname}  500  ${Date.now() - start}ms  — ${err.message}`);
    if (err.stack) console.error(err.stack);
    return;
  }
  // No finally-clear needed: the identity box lives in AsyncLocalStorage, scoped
  // to this handler's async call tree — it evaporates when the tree settles and
  // never bleeds into a concurrent request (the whole point of the ALS switch).

  // Output validation — mirror worker.js: a Zod @Returns schema validates the
  // result; a failure is a 500 internal_error (detail logged, never leaked).
  if (liveRoute.returnSchema && typeof liveRoute.returnSchema.safeParse === 'function') {
    const out = liveRoute.returnSchema.safeParse(result);
    if (!out.success) {
      log(`output validation failed: ${JSON.stringify(out.error && out.error.issues)}`);
      res.statusCode = 500;
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ error: 'internal_error', error_description: 'output validation failed' }));
      log(`[${req.method}] ${parsed.pathname}  500  ${Date.now() - start}ms  — output validation`);
      return;
    }
  }

  // void / undefined / null → 204 (no response body), mirroring worker.js.
  if (result === undefined || result === null) {
    res.statusCode = 204;
    res.end();
    log(`[${req.method}] ${parsed.pathname}  204  ${Date.now() - start}ms`);
    return;
  }

  res.statusCode = 200;
  res.setHeader('content-type', 'application/json');
  res.end(JSON.stringify(result));
  log(`[${req.method}] ${parsed.pathname}  200  ${Date.now() - start}ms`);
});

// send400 writes a validation_error envelope from a zod error — the same wire
// shape worker.js produces (error/error_description/details[]).
function send400(res, description, zodError, fallbackField, req, parsed, start) {
  const details = ((zodError && zodError.issues) || []).map((issue) => ({
    field: (issue.path || []).join('.') || fallbackField,
    message: issue.message || 'Validation failed',
  }));
  send400Detail(res, description, details, req, parsed, start);
}

// send400Detail writes a validation_error envelope from a pre-built details
// array (used for synthesized path-param errors).
function send400Detail(res, description, details, req, parsed, start) {
  res.statusCode = 400;
  res.setHeader('content-type', 'application/json');
  res.end(JSON.stringify({ error: 'validation_error', error_description: description, details }));
  log(`[${req.method}] ${parsed.pathname}  400  ${Date.now() - start}ms  — ${description}`);
}

function readJsonBody(req) {
  return new Promise((resolve) => {
    let data = '';
    req.setEncoding('utf8');
    req.on('data', (chunk) => { data += chunk; });
    req.on('end', () => {
      if (!data) return resolve(null);
      try { resolve(JSON.parse(data)); }
      catch { resolve({ raw: data }); }
    });
  });
}

// ── watch ───────────────────────────────────────────────────────────────

function watchControllers() {
  if (!fs.existsSync(CONTROLLERS_DIR)) return;
  fs.watch(CONTROLLERS_DIR, { recursive: true }, (event, filename) => {
    if (!filename) return;
    log(`reload: ${event} ${filename}`);
    registerControllers();
  });
}

function log(msg) {
  const ts = new Date().toISOString().slice(11, 19);
  process.stdout.write(`[palbase serve ${ts}] ${msg}\n`);
}

// ── resources (v2) ─────────────────────────────────────────────────────────
//
// Mirrors worker.js's bootResources/shutdownResources: require() each
// resources/* file, register every `instanceof Resource` export via the SDK's
// __registerResource, then run __runResourceBoot ONCE with a curated env subset
// BEFORE serving. On shutdown (SIGINT/SIGTERM) run __shutdownResources.
//
// Best-effort for local dev: the SDK must expose the Resource API (older
// @palbase/backend without it → skip silently), and a declared secret that is
// absent from the local env is WARNED about rather than aborting boot — local
// dev often runs without the full secret set, and crashing would block the
// developer from exercising the controllers that don't need the resource.
// (The deployed pod is strict; it fails boot on a missing secret.)

// resourceEnvMap is the env handed to Resource boot. Local dev has no per-branch
// secret injection like the pod, so we expose the developer's own process.env
// (a copy, so a Resource cannot mutate the live env). PALBASE_* pod credentials
// were already deleted/closed-over above, so a Resource cannot read them.
function resourceEnvMap() {
  return Object.assign({}, process.env);
}

// missingDeclaredSecrets returns the declared `static secrets` names that are
// absent from envMap, so we can warn before boot (instead of letting
// __runResourceBoot throw and abort local dev).
function missingDeclaredSecrets(resource, envMap) {
  const ctor = resource && resource.constructor;
  const declared = ctor && Array.isArray(ctor.secrets) ? ctor.secrets : [];
  return declared.filter((name) => envMap[name] === undefined);
}

let resourcesBooted = false;

async function bootResources() {
  if (!fs.existsSync(RESOURCES_DIR)) return 0;
  const runtime = require('@palbase/backend');
  if (typeof runtime.__registerResource !== 'function' ||
      typeof runtime.__runResourceBoot !== 'function' ||
      typeof runtime.Resource !== 'function') {
    log('hint: installed @palbase/backend has no Resource API — resources/ skipped. ' +
      'Upgrade @palbase/backend for local resource support.');
    return 0;
  }

  // esbuild-bundle resources/*.ts the same way controllers/ are bundled (CJS,
  // @palbase/backend external, extensionless-import resolution) so a resource
  // that imports `../services/foo` loads under serve exactly as on deploy.
  rmBundledTree(BUNDLED_RESOURCES_DIR);
  try {
    if (!bundleSrcDir(RESOURCES_DIR, BUNDLED_RESOURCES_DIR)) return 0;
  } catch (err) {
    log(`⚠ esbuild failed for resources/ — ${esbuildErr(err)} — serving without resources`);
    return 0;
  }

  const envMap = resourceEnvMap();
  let registered = 0;
  for (const file of walk(BUNDLED_RESOURCES_DIR)) {
    if (!CONTROLLER_FILE_RE.test(path.basename(file))) continue;
    let mod;
    try {
      mod = require(file);
    } catch (err) {
      log(`resources: skipping ${path.join('resources', path.relative(BUNDLED_RESOURCES_DIR, file))} — ${err.message}`);
      continue;
    }
    const candidates = [];
    if (mod && mod.default !== undefined) candidates.push(mod.default);
    for (const k of Object.keys(mod || {})) {
      if (k === 'default') continue;
      candidates.push(mod[k]);
    }
    for (const v of candidates) {
      if (v instanceof runtime.Resource) {
        const missing = missingDeclaredSecrets(v, envMap);
        if (missing.length > 0) {
          log(`⚠ resource ${v.constructor.name} declares secret(s) ${missing.join(', ')} ` +
            `not set in your environment — its init() may fail. Set them (e.g. export ${missing[0]}=…) before serving.`);
        }
        runtime.__registerResource(v);
        registered += 1;
      }
    }
  }

  if (registered > 0) {
    try {
      await runtime.__runResourceBoot(envMap);
      resourcesBooted = true;
      log(`booted ${registered} resource(s)`);
    } catch (err) {
      // A missing declared secret (or a failing init) throws here. In local dev
      // we warn and continue serving rather than aborting — the controllers that
      // don't touch this resource still work; one that does will surface the
      // error on first use.
      log(`⚠ resource boot failed: ${err.message} — serving without booted resources`);
    }
  }
  return registered;
}

async function shutdownResources() {
  if (!resourcesBooted) return;
  try {
    const runtime = require('@palbase/backend');
    if (typeof runtime.__shutdownResources === 'function') {
      await runtime.__shutdownResources();
    }
  } catch (err) {
    log(`resource shutdown failed: ${err.message}`);
  }
}

// ── boot ────────────────────────────────────────────────────────────────

async function main() {
  const reg = registerControllers();
  if (reg.sawControllerFiles && reg.routeCount === 0) {
    if (reg.staleSDKSignature) {
      log('FATAL: @palbase/backend is stale or missing — your controllers use');
      log('@Controller/@Get decorators (added in @palbase/backend 4.0.0) but the');
      log('installed package does not export them. Run:');
      log('    npm install @palbase/backend@latest');
      log('then re-run `palbase serve`.');
    } else {
      log('FATAL: controllers/ has files but 0 routes registered (see skip reasons above).');
    }
    process.exit(1);
  }
  watchControllers();
  await bootResources();
  // Discover workers/ so Queue.push(worker, payload) can run them in-process.
  // After installRuntime() wires Queue → localQueue() per request, a controller
  // enqueuing a job sees its worker actually execute (dev-only, non-durable).
  registerWorkers();

  server.listen(PORT, '0.0.0.0', () => {
    const ip = lanIP();
    log(`listening on http://localhost:${PORT}` + (ip ? ` and http://${ip}:${PORT} (device)` : ''));
    log(`project ref: ${PROJECT_REF}`);
    log(`watching: ${CONTROLLERS_DIR}`);
    if (PALBASE_URL) {
      log('────────────────────────────────────────────────────────────');
      log(`⚠ connected to LIVE data for project ${PROJECT_REF}`);
      log(`  Documents/Storage/… writes hit ${PALBASE_URL}`);
      log(`  key: publishable — protected module writes require managed runtime authority`);
      log(`  Auth tokens verified by ${PALBASE_URL}/auth/user`);
      log('────────────────────────────────────────────────────────────');
    } else {
      log('Documents/Storage/… disabled (no project credentials). Run `palbase login` then re-run.');
    }
    log(`press Ctrl+C to quit`);
  });

  // Graceful shutdown: drain booted resources (close pools, flush buffers) on
  // SIGINT/SIGTERM before exiting, mirroring worker.js's SIGTERM hook, then
  // remove the temp esbuild bundle tree so it doesn't linger in tmp.
  const onSignal = async () => {
    await shutdownResources();
    rmBundledTree(BUNDLE_ROOT);
    rmBundledTree(STAGED_CONTROLLERS_DIR); // staging lives under PROJECT_ROOT — never leave it behind
    server.close(() => process.exit(0));
    setTimeout(() => process.exit(0), 3000).unref();
  };
  process.on('SIGINT', onSignal);
  process.on('SIGTERM', onSignal);

  // Belt-and-suspenders cleanup: remove the temp esbuild bundle tree on ANY
  // normal process exit, not only the SIGINT/SIGTERM path above. The signal
  // handler covers Ctrl+C, but a plain `process.exit()` (e.g. the FATAL boot
  // guard), an uncaught-then-exit, or the parent CLI closing our stdio can end
  // the process WITHOUT a signal — and then BUNDLE_ROOT would leak in tmp until
  // a reboot. 'exit' fires on every clean exit and MUST be synchronous (the loop
  // is already stopping), so we use the sync fs.rmSync directly rather than the
  // async drain. A SIGKILL/hard crash still can't run this — that leak is swept
  // on the NEXT serve start by the Go side (backend.go sweepStaleServeTempDirs).
  process.on('exit', () => {
    try { fs.rmSync(BUNDLE_ROOT, { recursive: true, force: true }); } catch { /* best-effort */ }
    try { fs.rmSync(STAGED_CONTROLLERS_DIR, { recursive: true, force: true }); } catch { /* best-effort */ }
  });
}

// Auto-boot only when run directly (`node dev-server.js`). Guarded so the file
// can be require()d by tests (e.g. the local Cache/Queue unit test) without
// starting a server or running the boot side effects.
if (require.main === module) {
  main().catch((err) => {
    log(`FATAL: dev-server boot failed: ${err && err.stack ? err.stack : String(err)}`);
    process.exit(1);
  });
}

// Exported for unit tests (dev-server.test.js): the local Cache/Queue factories
// and the worker registry. Not used by the running dev-server, which calls them
// directly. Keeping the export minimal avoids leaking the whole internal surface.
module.exports = {
  makeLocalCache, makeLocalQueue, workerRegistry, parseDotenv,
  // Per-request identity isolation (exported for the concurrency test).
  runInRequestContext, currentRequestUserId, currentRequestUserToken,
};
