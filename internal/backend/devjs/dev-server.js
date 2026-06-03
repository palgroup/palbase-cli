#!/usr/bin/env node
/**
 * Palbase backend dev server (v2 — controllers/).
 *
 * Local equivalent of the prod backend-runtime app-server
 * (modules/backend/internal/runtime/worker.js), tuned for hot reload +
 * interactive output. The shape of every user-facing surface (controllers,
 * handlers, the per-request `req`, the imported singleton services, resources)
 * matches prod so what runs under `palbase serve` runs identically in
 * services-shared/br-<ref> after a `git push` deploy.
 *
 * Routing surface: controllers/ ONLY. A controllers/*.ts file default-exports a
 * ControllerDef (`__palbase === 'controller'`); each route in `routes[mapKey]`
 * carries a HandlerDef at `.handler`. The full route path is `basePath +
 * route.path` and the verb is `route.method` — identical to the runtime's
 * dispatch + extract_meta.js + generator.go. There is NO v1 `endpoints/`
 * filename→method path (clean break).
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
const PROJECT_ROOT = process.env.PALBASE_DEV_ROOT || process.cwd();
const PROJECT_REF = process.env.PALBASE_PROJECT_REF || 'local';
const PUBLIC_HOST = process.env.PALBASE_PUBLIC_HOST || '';
const CONTROLLERS_DIR = path.join(PROJECT_ROOT, 'controllers');
const RESOURCES_DIR = path.join(PROJECT_ROOT, 'resources');

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
// controllers/ is the ONLY routing surface (v2 — clean break from v1
// `endpoints/`). Each controllers/*.ts file default-exports a ControllerDef:
//   { __palbase: 'controller', basePath, routes: { [mapKey]: RouteDef } }
//   RouteDef = { __palbase: 'route', method, path, handler: HandlerDef }
// The full route path is `basePath + route.path` and the verb is route.method —
// identical to extract_meta.js / generator.go / worker.js's dispatch.
//
// Each route-table entry: { method, urlPattern, regex, paramNames,
// controllerPath, routeKey }. `controllerPath` is the file to require();
// `routeKey` (the map key) selects the route within the controller (mirroring
// worker.js's `context.route_key`). urlPattern keeps `:param` colons for
// printing + matching; the regex matches incoming requests with capture groups.
const routes = new Map();

// loadControllerDef require()s a controller file (cache-busted so saves take
// effect without a restart) and returns its default-export ControllerDef.
// Throws a clear error when the default export is not a controller — there is
// no v1 fallback, matching extract_meta.js + worker.js.
function loadControllerDef(controllerPath) {
  delete require.cache[require.resolve(controllerPath)];
  const mod = require(controllerPath);
  const controller = mod.default ?? mod;
  if (!controller || controller.__palbase !== 'controller') {
    const shown = controllerPath.startsWith(BUNDLED_CONTROLLERS_DIR)
      ? bundledToSrcRel(controllerPath)
      : path.relative(PROJECT_ROOT, controllerPath);
    throw new Error(
      `${shown} must default-export a ControllerDef ` +
      `(use defineController / a controllers/* file); got ` +
      (controller && controller.__palbase ? `__palbase=${JSON.stringify(controller.__palbase)}` : 'a non-controller export'),
    );
  }
  return controller;
}

// urlToRegex compiles a `/places/:id`-style pattern into { regex, paramNames }.
// The full path produced by `basePath + route.path` uses `:param` for path
// params (the SDK's own convention); we keep that here for matching.
function urlToRegex(urlPattern) {
  const segments = urlPattern.split('/').filter((s) => s !== '');
  const paramNames = [];
  const body = segments.map((s) => {
    if (s.startsWith(':')) {
      paramNames.push(s.slice(1));
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
    return;
  }

  // esbuild-bundle controllers/*.ts (+ their transitively-imported handlers/
  // and services/) into BUNDLED_CONTROLLERS_DIR. We discover/require the BUNDLED
  // CJS, so extensionless relative imports resolve exactly as they do on deploy.
  rmBundledTree(BUNDLED_CONTROLLERS_DIR);
  try {
    bundleSrcDir(CONTROLLERS_DIR, BUNDLED_CONTROLLERS_DIR);
  } catch (err) {
    // A bundle error (syntax error, unresolved import) must be LOUD — otherwise
    // the dir scan below finds nothing and silently registers 0 routes.
    log(`esbuild failed for controllers/ — ${esbuildErr(err)}`);
    log('registered 0 route(s) (fix the build error above and save to retry)');
    return;
  }

  if (!fs.existsSync(BUNDLED_CONTROLLERS_DIR)) {
    log('registered 0 route(s) (no controllers/*.ts found)');
    return;
  }

  for (const file of walk(BUNDLED_CONTROLLERS_DIR)) {
    if (!CONTROLLER_FILE_RE.test(path.basename(file))) continue;
    let controller;
    try {
      controller = loadControllerDef(file);
    } catch (err) {
      log(`skipping ${bundledToSrcRel(file)} — ${err.message}`);
      continue;
    }
    const routeMap = (controller.routes && typeof controller.routes === 'object') ? controller.routes : {};
    for (const [routeKey, routeDef] of Object.entries(routeMap)) {
      if (!routeDef || typeof routeDef !== 'object') continue;
      const method = typeof routeDef.method === 'string' ? routeDef.method.toUpperCase() : '';
      if (!method) continue;
      const urlPattern = joinPath(controller.basePath, routeDef.path);
      const { regex, paramNames } = urlToRegex(urlPattern);
      routes.set(method + ' ' + urlPattern, {
        method,
        urlPattern,
        regex,
        paramNames,
        controllerPath: file,
        routeKey,
      });
    }
  }
  log(`registered ${routes.size} route(s):`);
  for (const route of routes.values()) {
    log(`  ${route.method.padEnd(6)} ${route.urlPattern}  →  ${bundledToSrcRel(route.controllerPath)} [${route.routeKey}]`);
  }
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

// resolveHandlerDef re-loads the route's controller (cache-busted) and returns
// the live HandlerDef for the route's mapKey — mirroring worker.js resolving
// `controller.routes[route_key].handler`. Throws when the controller no longer
// has the route (e.g. the file changed since registration).
function resolveHandlerDef(route) {
  const controller = loadControllerDef(route.controllerPath);
  const routeDef = controller.routes && controller.routes[route.routeKey];
  if (!routeDef || typeof routeDef !== 'object') {
    throw new Error(`route "${route.routeKey}" not found in ${bundledToSrcRel(route.controllerPath)}`);
  }
  const handlerDef = routeDef.handler;
  if (!handlerDef || typeof handlerDef !== 'object' || typeof handlerDef.handler !== 'function') {
    throw new Error(`route "${route.routeKey}" has no handler function`);
  }
  return handlerDef;
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
const { buildModuleClients } = require('./module-clients.js');

// Request-scoped user id for auto-binding flags resolution to the current
// request's user — mirrors worker.js's `currentRequestUserId`. The dev-server
// builds the module clients ONCE (singleton, below) rather than per-request, so
// instead of a per-call closure we keep a module-scoped variable that the http
// handler sets before each invocation and clears after. The getter passed into
// buildModuleClients reads it lazily at flags-call time, so `flags.get('key')`
// resolves user-override → project-default for the signed-in user (project
// defaults when anonymous). Concurrency note: the dev-server handles requests
// effectively serially for this purpose (a handler awaits its flags call within
// the same turn before the next request mutates the variable); this is a local
// one-developer dev tool, not the multi-tenant pod. Prod's per-subprocess model
// gives the same guarantee structurally.
let currentRequestUserId = null;

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
    // Lazy getter — read at flags-call time so it sees whatever the http
    // handler set for the in-flight request. Matches worker.js.
    getCurrentUserId: () => currentRequestUserId,
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
// mode. In `palbase serve` we don't have access to the per-tenant pgx pool
// (that's tied to the backend-runtime pod's internal-API on 127.0.0.1, not
// externally reachable). Rather than half-fake it and have handlers silently
// behave differently in dev vs prod, every Database op throws a clear
// "use Documents in serve, or deploy to test" hint on first call. This
// keeps dev = prod for everything we CAN honestly mirror (Documents, Storage,
// Notifications, Flags, …) and names the one surface that needs a deploy.
function dbClient() {
  const hint =
    'Database is not available under `palbase serve` (no local Postgres ' +
    'tunnel to the tenant pool). For local dev, use Documents.collection() ' +
    'which proxies through the deployed module. For Database tests, ' +
    '`git push` to deploy and exercise the deployed endpoint.';
  return new Proxy({}, {
    get(_t, method) {
      if (typeof method !== 'string') return undefined;
      return () => { throw new Error(hint); };
    },
  });
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

// installRuntime injects the dev service clients into the shared
// @palbase/backend instance, mirroring worker.js's runtime seam. Both
// dev-server.js (temp dir) and the user's controllers/handlers resolve
// @palbase/backend from the same project node_modules (NODE_PATH), so they
// share ONE module instance and a handler's `import { Database }` sees what we
// set here.
//
// EXCLUDED on purpose: Realtime, Functions, CMS, Links, Analytics, Auth.
function installRuntime() {
  const clients = moduleClients();
  require('@palbase/backend').__setRuntime({
    Database: dbClient(),
    Documents: clients.docs,
    Storage: clients.storage,
    Cache: notConfiguredModule('Cache'),
    Queue: notConfiguredModule('Queue'),
    Log: makeLog(),
    Notifications: clients.notifications,
    Flags: clients.flags,
  });
}

// makeRequest builds the request-scoped object passed to the handler. Services
// are NOT on it — they are the imported singletons (installed by
// installRuntime). `req` here is the Node http request.
function makeRequest(req, params, body, user) {
  return {
    input: body ?? {},
    params,
    query: {},
    headers: req.headers || {},
    user: user ?? null,
    client: clientInfoFromHeaders(req.headers || {}),
    file: null,
    method: req.method,
    requestId: `req_dev_${Date.now().toString(36)}`,
    traceId: '0'.repeat(32),
    spanId: '0'.repeat(16),
    errors: {},
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
// (modules/backend/internal/openapi/generator.go) from the same
// defineEndpoint() configs. `palbase mobile codegen` / `palbase types
// --env local` fetch /openapi.json to drive local codegen, so the LOCAL spec
// must match the REMOTE one the deployed pod serves — otherwise codegen output
// silently differs between local serve and the deployed pod.
//
// We rebuild the document fresh on every GET (not on the fs.watch reload):
// it's the simpler correct option — controllers already reload per request via
// resolveHandlerDef()'s cache-bust, so building here picks up edits with zero
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

// operationId mirrors generator.go's operationID: toLower(method) + per
// non-empty path segment, `By`+Capitalize(param) for `{param}` else
// Capitalize(segment). e.g. POST /todos/create → postTodosCreate;
// GET /rooms/{id} → getRoomsById.
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

// headerParameters lowers a headers JSON Schema (object, string-typed props)
// into a name-sorted array of `in: header` parameters — matching
// generator.go's headerParameters (struct order: name, in, required,
// description?, schema).
function headerParameters(schema) {
  if (!schema || typeof schema !== 'object') return [];
  const props = schema.properties;
  if (!props || typeof props !== 'object') return [];
  const required = new Set(Array.isArray(schema.required) ? schema.required : []);
  const out = [];
  for (const name of Object.keys(props).sort()) {
    const prop = props[name] && typeof props[name] === 'object' ? props[name] : {};
    const param = { name, in: 'header', required: required.has(name) };
    if (typeof prop.description === 'string' && prop.description !== '') {
      param.description = prop.description;
    }
    param.schema = sortDeep(props[name]);
    out.push(param);
  }
  return out;
}

// buildHandlerMeta reads the spec-relevant fields off a route's HandlerDef.
// method + path come from the route (controller basePath + route.path) — NOT
// from the handler, which carries no routing — matching the dev-server's own
// dispatch. auth uses the shared isAuthRequired() so spec == enforcement.
function buildHandlerMeta(route, handlerDef) {
  return {
    method: route.method,
    openPath: openApiPath(route.urlPattern),
    authRequired: isAuthRequired(handlerDef.auth),
    authRole: (handlerDef.auth && typeof handlerDef.auth === 'object' && typeof handlerDef.auth.role === 'string')
      ? handlerDef.auth.role : '',
    rateLimit: (handlerDef.rateLimit && typeof handlerDef.rateLimit === 'object'
      && typeof handlerDef.rateLimit.max === 'number'
      && typeof handlerDef.rateLimit.window === 'number')
      ? { max: handlerDef.rateLimit.max, window: handlerDef.rateLimit.window } : null,
    inputSchema: handlerDef.input ? zodToJSON(handlerDef.input) : null,
    outputSchema: handlerDef.output ? zodToJSON(handlerDef.output) : null,
    headersSchema: handlerDef.headers ? zodToJSON(handlerDef.headers) : null,
    errors: extractErrors(handlerDef.errors),
  };
}

// extractErrors lowers a defineEndpoint({ errors }) table into the shape the
// generator consumes: { name: { status, code, description, data } }.
// description defaults to '' (the generator always emits the key);
// data is the zod-to-json-schema body or null.
function extractErrors(errors) {
  if (!errors || typeof errors !== 'object') return null;
  const out = {};
  for (const [name, def] of Object.entries(errors)) {
    if (!def || typeof def !== 'object') continue;
    out[name] = {
      status: def.status,
      code: def.code,
      description: typeof def.description === 'string' ? def.description : '',
      data: def.data ? zodToJSON(def.data) : null,
    };
  }
  return Object.keys(out).length > 0 ? out : null;
}

// buildOperation assembles one OpenAPI operation object with keys inserted in
// Go's exact emission order. When x- extensions are present Go re-marshals the
// whole operation as a flat map (all keys sorted alpha), so we sort the
// top-level keys in that case — see sortOperationKeys.
function buildOperation(meta) {
  const op = {};
  // struct field order: summary, operationId, tags, security, parameters,
  // requestBody, responses (summary/tags omitted — dev-server has no
  // description/tags on the route; they'd be empty).
  op.operationId = operationId(meta.method, meta.openPath);

  if (meta.authRequired) {
    // bearerAuth then apiKey — generator.go emits them in that array order.
    op.security = [{ bearerAuth: [] }, { apiKey: [] }];
  } else {
    // explicit opt-out → empty requirement (auth optional).
    op.security = [{}];
  }

  if (meta.headersSchema) {
    const params = headerParameters(meta.headersSchema);
    if (params.length > 0) op.parameters = params;
  }

  if (meta.inputSchema) {
    op.requestBody = {
      required: true,
      content: { 'application/json': { schema: meta.inputSchema } },
    };
  }

  // responses — status keys inserted in sorted order ("200" < "400" < "401" <
  // declared). 200 always; 400 always; 401 only when auth required.
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

  // Declared typed errors → per-status response envelope + x-palbase-errors.
  const extensions = {};
  if (meta.rateLimit) {
    extensions['x-rate-limit'] = { max: meta.rateLimit.max, window: meta.rateLimit.window };
  }
  if (meta.errors) {
    const ext = {};
    const byStatus = new Map();
    for (const [name, e] of Object.entries(meta.errors)) {
      if (!byStatus.has(e.status)) byStatus.set(e.status, []);
      byStatus.get(e.status).push(name);
    }
    for (const status of byStatus.keys()) {
      const names = byStatus.get(status).slice().sort();
      const variants = [];
      for (const name of names) {
        const e = meta.errors[name];
        const envelopeProps = {
          error: { const: e.code, type: 'string' },
          message: { type: 'string' },
        };
        const required = ['error'];
        if (e.data) {
          envelopeProps.data = e.data;
          required.push('data');
        }
        // properties object: insert keys sorted (error < message[ < data]) —
        // Go marshals envelopeProps as a map. error/message always; data when
        // present. After sort: data, error, message.
        const sortedProps = {};
        for (const k of Object.keys(envelopeProps).sort()) sortedProps[k] = envelopeProps[k];
        // variant struct is a map in Go → keys sorted: properties, required, type.
        variants.push({ properties: sortedProps, required, type: 'object' });
        ext[name] = {
          code: e.code,
          description: e.description,
          hasData: e.data != null,
          status: e.status,
        };
      }
      const schema = variants.length === 1 ? variants[0] : { oneOf: variants };
      responseEntries.push([String(status), {
        description: 'Declared error response',
        content: { 'application/json': { schema } },
      }]);
    }
    // x-palbase-errors is a Go map[string]any (generator.go:324,378) re-marshaled
    // through Operation.MarshalJSON's flat map (generator.go:60-83), so its
    // error-NAME keys come out GLOBALLY lexicographically sorted — NOT in the
    // status-grouped insertion order ext was built in above. Re-emit the names
    // globally sorted so the bytes match Go for endpoints whose error names
    // don't happen to sort the same as their status grouping (e.g. zeta@409,
    // alpha@422, mid@409 → alpha, mid, zeta).
    const sortedExt = {};
    for (const name of Object.keys(ext).sort()) sortedExt[name] = ext[name];
    extensions['x-palbase-errors'] = sortedExt;
  }

  // Insert response status keys in sorted order ("200" < "400" < "401" < …).
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
    let handlerDef;
    try {
      handlerDef = resolveHandlerDef(route);
    } catch (err) {
      log(`openapi: skipping ${route.method} ${route.urlPattern} — ${err.message}`);
      continue;
    }
    const meta = buildHandlerMeta(route, handlerDef);
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
      description: 'Auto-generated from defineEndpoint() configs.',
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
    res.statusCode = 404;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'not_found', path: parsed.pathname, method: req.method }));
    return;
  }
  const params = {};
  route.paramNames.forEach((name, i) => { params[name] = match[i + 1]; });

  let body = null;
  if (req.method !== 'GET' && req.method !== 'HEAD') {
    body = await readJsonBody(req);
  } else if (Object.keys(parsed.query).length > 0) {
    body = parsed.query;
  }

  let handlerDef;
  try {
    handlerDef = resolveHandlerDef(route);
  } catch (err) {
    res.statusCode = 500;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'load_error', message: err.message }));
    log(`[${req.method}] ${parsed.pathname}  500  ${Date.now() - start}ms  — ${err.message}`);
    return;
  }

  // Auth gate — same contract as prod: optional => no token = pass with
  // req.user=null, present token = palauth verify. Required => must have
  // a token AND palauth must accept it. `authRequired` comes from the shared
  // isAuthRequired() so the enforcement here and the served /openapi.json
  // `security` block never drift (omitted auth → required, secure-by-default).
  // Note: pass handlerDef.auth RAW — `handlerDef.auth || {}` would silently turn
  // an explicit `auth: false` into `{}`, re-securing a route the developer
  // opted out of.
  const authCfg = (handlerDef.auth && typeof handlerDef.auth === 'object') ? handlerDef.auth : {};
  const authRequired = isAuthRequired(handlerDef.auth);
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
  if (authCfg.role && user && user.role !== authCfg.role) {
    res.statusCode = 403;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'forbidden', message: `role ${authCfg.role} required, got ${user.role}` }));
    log(`[${req.method}] ${parsed.pathname}  403  ${Date.now() - start}ms  — role mismatch`);
    return;
  }

  // Rate limit gate.
  if (handlerDef.rateLimit) {
    const ip = (req.headers['x-forwarded-for'] || req.socket.remoteAddress || 'local').toString().split(',')[0].trim();
    const result = rateLimitCheck(`${route.method} ${route.urlPattern}`, ip, handlerDef.rateLimit);
    res.setHeader('x-ratelimit-limit', String(handlerDef.rateLimit.max));
    res.setHeader('x-ratelimit-remaining', String(Math.max(0, result.remaining)));
    if (!result.allowed) {
      res.statusCode = 429;
      res.setHeader('retry-after', String(result.retryAfter));
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({
        error: 'rate_limited',
        message: `max ${handlerDef.rateLimit.max} req / ${handlerDef.rateLimit.window}s`,
        retry_after: result.retryAfter,
      }));
      log(`[${req.method}] ${parsed.pathname}  429  ${Date.now() - start}ms  — rate limited (retry in ${result.retryAfter}s)`);
      return;
    }
  }

  installRuntime();
  const pbReq = makeRequest(req, params, body, user);

  // Input validation — mirror worker.js: a Zod-compatible `input` schema parses
  // pbReq.input; failure returns a 400 validation_error envelope (the parsed,
  // stripped data replaces the raw input). This keeps `palbase serve` honest
  // about the same 400s the deployed pod returns.
  if (handlerDef.input && typeof handlerDef.input.safeParse === 'function') {
    const parsedInput = handlerDef.input.safeParse(pbReq.input);
    if (!parsedInput.success) {
      const details = (parsedInput.error?.issues || []).map((issue) => ({
        field: (issue.path || []).join('.') || 'input',
        message: issue.message || 'Validation failed',
      }));
      res.statusCode = 400;
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({
        error: 'validation_error',
        error_description: 'Input validation failed',
        details,
      }));
      log(`[${req.method}] ${parsed.pathname}  400  ${Date.now() - start}ms  — input validation`);
      return;
    }
    pbReq.input = parsedInput.data;
  }

  // Declared typed errors — re-project each `errors` entry as a thrower on
  // pbReq.errors so the handler can `throw req.errors.todoLocked({...})`. Mirrors
  // worker.js's HttpError minting (status/code/description + optional data).
  if (handlerDef.errors && typeof handlerDef.errors === 'object') {
    pbReq.errors = buildErrorThrowers(handlerDef.errors);
  }

  let result;
  try {
    // Make this request's user visible to the flags client's auto-bind getter
    // (mirrors worker.js): `Flags.get('key')` resolves user-override → default
    // without the handler passing { userId } manually. Anonymous → null →
    // project defaults. Reset in finally so a later request never inherits it.
    currentRequestUserId = (pbReq.user && pbReq.user.id) || null;
    result = await handlerDef.handler(pbReq);
  } catch (err) {
    // HttpError (a declared-error throw or a hand-built one) is a HANDLER
    // OUTCOME, not infra failure: surface its status + envelope verbatim,
    // mirroring worker.js's catch path.
    if (err && typeof err === 'object' && err.name === 'HttpError' && typeof err.status === 'number') {
      const envelope = {
        error: err.error || 'http_error',
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
    res.statusCode = 500;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'handler_error', message: err.message, stack: err.stack }));
    log(`[${req.method}] ${parsed.pathname}  500  ${Date.now() - start}ms  — ${err.message}`);
    return;
  } finally {
    currentRequestUserId = null;
  }

  // Output validation — mirror worker.js: a Zod `output` schema validates the
  // result; a failure is a 500 internal_error (detail logged, never leaked).
  if (handlerDef.output && typeof handlerDef.output.safeParse === 'function') {
    const out = handlerDef.output.safeParse(result);
    if (!out.success) {
      log(`output validation failed: ${JSON.stringify(out.error && out.error.issues)}`);
      res.statusCode = 500;
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ error: 'internal_error', error_description: 'output validation failed' }));
      log(`[${req.method}] ${parsed.pathname}  500  ${Date.now() - start}ms  — output validation`);
      return;
    }
  }

  res.statusCode = 200;
  res.setHeader('content-type', 'application/json');
  res.end(JSON.stringify(result ?? null));
  log(`[${req.method}] ${parsed.pathname}  200  ${Date.now() - start}ms`);
});

// buildErrorThrowers re-projects a HandlerDef.errors map into thrower functions
// the handler calls (`throw req.errors.todoLocked({...})`). Each returns an
// Error tagged name='HttpError' carrying status/code/description (+ optional
// data validated against the declared zod schema) — the exact wire-shape the
// dispatch catch path serialises, identical to worker.js.
function buildErrorThrowers(declaredErrors) {
  const throwers = {};
  for (const [name, def] of Object.entries(declaredErrors)) {
    if (!def || typeof def !== 'object') continue;
    const { status, code, description, data: dataSchema } = def;
    throwers[name] = (payload) => {
      let validated = payload;
      if (dataSchema && typeof dataSchema.parse === 'function') {
        validated = dataSchema.parse(payload);
      }
      const err = new Error(description || `${code} (${status})`);
      err.name = 'HttpError';
      err.status = status;
      err.error = code;
      err.errorDescription = description || `${code} (${status})`;
      if (validated !== undefined) err.data = validated;
      return err;
    };
  }
  return throwers;
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
  registerControllers();
  watchControllers();
  await bootResources();

  server.listen(PORT, () => {
    log(`listening on http://127.0.0.1:${PORT}`);
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
    server.close(() => process.exit(0));
    setTimeout(() => process.exit(0), 3000).unref();
  };
  process.on('SIGINT', onSignal);
  process.on('SIGTERM', onSignal);
}

main().catch((err) => {
  log(`FATAL: dev-server boot failed: ${err && err.stack ? err.stack : String(err)}`);
  process.exit(1);
});
