/**
 * Palbase Backend Runtime — Class-Controller Metadata Extractor
 *
 * Extracts route metadata from a bundled class-controller file. Called during
 * deploy to generate .meta.json sidecar files.
 *
 * A controllers/* bundle's default export is the controller CLASS produced by
 * `@Controller(...) class X { ... }`. The class carries:
 *   - a non-enumerable `__palbase === "controller"` marker (else deploy error);
 *   - controller meta under `Symbol.for("palbase.backend.controllerMeta")`
 *       → { __palbase:"controller", basePath, defaultAuth? };
 *   - the route registry under `Symbol.for("palbase.routes")`
 *       → Map<fnName, RouteMeta> where
 *         RouteMeta = { method, subpath, fnName, options:{auth?,rateLimit?},
 *                       params: ParamMeta[], returnSchema? }
 *         ParamMeta = { index, kind, schema?, name? }.
 *
 * The class is lowered into the sidecar contract (see
 * docs/superpowers/specs/2026-06-04-sidecar-contract.md):
 *
 *   { controller:true, basePath, controllerName?, routes:[ {
 *       mapKey, method, path, auth:{required,role}, rateLimit|null,
 *       bodySchema|null, querySchema|null, paramsSchema|null,
 *       headersSchema|null, outputSchema|null,
 *       params:[ {index, kind, name?} ]
 *   } ] }
 *
 * `controllerName` is the dotted-operationId namespace derived from the
 * controller CLASS name (trailing "Controller" suffix stripped, first letter
 * lower-cased — MembersController → "members"); the Go OpenAPI generator joins
 * it with each route's mapKey into `<controllerName>.<mapKey>`
 * (members.getUser). Omitted when the derivation is empty — the generator then
 * falls back to the flat verb-prefixed operationId from method + `path` (the
 * FULL route path, basePath + subpath). `mapKey` is the route fnName; the
 * worker dispatches on it. NOTE: bundles are built with esbuild `--keep-names`
 * (deploy/bundler.go) so the class name survives minification. Per-kind input
 * schemas are SPLIT
 * (bodySchema/querySchema/paramsSchema/headersSchema) rather than a single
 * inputSchema; paramsSchema is synthesized from @Param("<name>") param metas.
 *
 * We DO NOT import @palbase/backend (version coupling) — the symbols are read
 * directly off the class.
 *
 * There is NO per-route HandlerDef shape and NO v1 single-`defineEndpoint`
 * shape: a class controller is the only HTTP routing surface. A non-controller
 * default export is a deploy error.
 *
 * Usage: echo '{"bundle_path":"/path/to/controller.js"}' | node extract_meta.js
 * Output: JSON metadata object
 */

'use strict';

// Registry symbols — MUST match the SDK (@palbase/backend
// src/decorators/registry.ts + controller.ts). Read directly off the controller
// class to avoid importing (and version-coupling to) the SDK. The routes live in
// an ARRAY anchored on Symbol.for("palbase.backend.routes"); @Returns schemas
// may be buffered separately and are merged below (mirroring SDK getRoutes()).
const ROUTES_SYMBOL = Symbol.for('palbase.backend.routes');
const RETURN_BUFFER_SYMBOL = Symbol.for('palbase.backend.returnBuffer');
const CONTROLLER_META_SYMBOL = Symbol.for('palbase.backend.controllerMeta');

function writeResult(result) {
  process.stdout.write(JSON.stringify(result));
  process.exit(0);
}

function writeError(error) {
  writeResult({ error: String(error) });
}

// Sentinel for a header-rule violation. The caller turns it into a top-level
// deploy error (writeError) so the message is surfaced verbatim.
class ExtractError extends Error {}

async function main() {
  const chunks = [];
  for await (const chunk of process.stdin) {
    chunks.push(chunk);
  }
  const input = Buffer.concat(chunks).toString();

  let req;
  try {
    req = JSON.parse(input);
  } catch (e) {
    writeError('Invalid JSON input: ' + e.message);
    return;
  }

  const { bundle_path } = req;
  if (!bundle_path) {
    writeError('bundle_path is required');
    return;
  }

  try {
    const mod = require(bundle_path);
    const Ctrl = mod.default || mod;

    // zod-to-json-schema: bundled inside @palbase/backend (the same converter
    // the OpenAPI generator uses there). Load lazily so the extractor stays
    // usable when @palbase/backend ships without it.
    let zodToJsonSchema = null;
    try {
      ({ zodToJsonSchema } = require('zod-to-json-schema'));
    } catch (e) {
      // Fall back to null payloads — keeps deploy working when the dep is not
      // installed (older runtime images).
    }
    function zodToJSON(z) {
      if (!zodToJsonSchema) return null;
      if (!z || typeof z._def !== 'object') return null;
      try {
        const schema = zodToJsonSchema(z, { target: 'jsonSchema7' });
        if (schema && typeof schema === 'object') {
          const { $schema, ...rest } = schema;
          return rest;
        }
        return null;
      } catch {
        return null;
      }
    }

    // controllers/ bundles MUST default-export a @Controller CLASS. Anything
    // else (a stray defineEndpoint, a plain object) is a deploy error — there
    // is no fallback.
    if (!Ctrl || Ctrl.__palbase !== 'controller') {
      return writeError(
        'controllers bundle must default-export a @Controller class ' +
        '(use @Controller / a controllers/* file); got ' +
        (Ctrl && Ctrl.__palbase ? `__palbase=${JSON.stringify(Ctrl.__palbase)}` : 'a non-controller export')
      );
    }

    // SDK error registry (typed backend errors): defineError(...) calls in the
    // just-required bundle registered into the SDK's project-global registry
    // (@palbase/backend is EXTERNAL in the bundle, so registration landed on
    // the shared instance NODE_PATH resolves). Reading it through
    // getErrorRegistry() also pre-seeds the built-ins (NotFound/Conflict/…).
    //
    // INTEGRATION SEAM (Phase A): getErrorRegistry ships with the defineError
    // SDK release. Until the pod's vendored SDK carries it, the registry is
    // empty here and route.throws simply doesn't lower into `errors` — clients
    // degrade to `.other`, extraction never breaks.
    const errorRegistry = loadErrorRegistry();

    try {
      return writeResult(extractControllerMeta(Ctrl, zodToJSON, errorRegistry));
    } catch (e) {
      if (e instanceof ExtractError) return writeError(e.message);
      throw e;
    }
  } catch (err) {
    writeError('Failed to extract metadata: ' + err.message);
  }
}

// loadErrorRegistry resolves the SDK's project-global error registry
// (Map<code, {code, status, className, dataSchema?, builtin}>) via
// require("@palbase/backend").getErrorRegistry(). Returns an EMPTY Map when
// the SDK (or the export) is unavailable — see the seam note in main().
function loadErrorRegistry() {
  try {
    const sdk = require('@palbase/backend');
    if (sdk && typeof sdk.getErrorRegistry === 'function') {
      const reg = sdk.getErrorRegistry();
      if (reg && typeof reg.get === 'function') return reg;
    }
  } catch {
    // SDK not resolvable from this process — registry stays empty.
  }
  return new Map();
}

// lowerRouteErrors joins a route's inferred throw descriptors ({name, code} —
// stamped by the staged recordThrows IIFEs) against the error registry by
// code, lowering each into the sidecar shape
// { name, code, status, hasData, dataJsonSchema? }. Codes the registry doesn't
// know are SKIPPED silently (the runtime guard surfaces them at first
// occurrence); the data schema uses the SAME zodToJSON as response schemas.
function lowerRouteErrors(throwsList, registry, zodToJSON) {
  if (!Array.isArray(throwsList)) return [];
  const out = [];
  for (const t of throwsList) {
    if (!t || typeof t.name !== 'string' || typeof t.code !== 'string') continue;
    const entry = registry.get(t.code);
    if (!entry || typeof entry.status !== 'number') continue;
    const hasData = Boolean(entry.dataSchema);
    const lowered = { name: t.name, code: t.code, status: entry.status, hasData };
    if (hasData) {
      const dataJsonSchema = zodToJSON(entry.dataSchema);
      if (dataJsonSchema) lowered.dataJsonSchema = dataJsonSchema;
    }
    out.push(lowered);
  }
  return out;
}

// normalizeAuth resolves the effective auth value (the controller→route cascade
// is resolved by the CALLER which passes the already-cascaded value) into the
// wire shape `{ required: boolean, role: string }`. The Go/worker side never
// re-resolves the cascade.
//
//   true            → { required: true,  role: "" }
//   false           → { required: false, role: "" }
//   { required, role } → { required: required !== false, role: role || "" }
//   undefined/null  → { required: true,  role: "" }  (secure-by-default)
function normalizeAuth(auth) {
  if (auth === false) return { required: false, role: '' };
  if (auth === true || auth === undefined || auth === null) {
    return { required: true, role: '' };
  }
  if (typeof auth === 'object') {
    return { required: auth.required !== false, role: auth.role || '' };
  }
  // Any other truthy scalar → required, no role.
  return { required: true, role: '' };
}

// findParam returns the FIRST ParamMeta of the given kind, or undefined.
function findParam(params, kind) {
  if (!Array.isArray(params)) return undefined;
  return params.find((p) => p && p.kind === kind);
}

// assertZodSchema fails the deploy LOUDLY when an @Body/@Query/@Headers param
// was given something other than a real Zod schema (e.g. the NestJS-style
// `@Query("workout_id")` string). A genuine zod schema has both a `_def` object
// and a `safeParse` function; anything else means the decorator arg is wrong.
// Without this, a string schema is silently swallowed (zodToJSON→null,
// validation skipped, the whole request map injected into the handler) and the
// endpoint 400s on every call with no deploy-time signal — the W3-F5 trap.
// A `param` with no schema at all is fine for kinds that don't carry one; this
// only fires when the param EXISTS but its schema is not a zod schema.
function assertZodSchema(param, decorator, method, fullPath) {
  if (!param) return; // decorator not used on this route
  const s = param.schema;
  // A real zod schema carries a `_def` object — the SAME validity gate zodToJSON
  // (the lowerer) already keys on. A string/field-name (`@Query("workout_id")`)
  // has no `_def`, so this catches exactly the wrong-arg case without rejecting
  // valid schemas.
  const isZod = s && typeof s === 'object' && typeof s._def === 'object';
  if (!isZod) {
    const got = s === undefined ? 'nothing' : (typeof s === 'string' ? `the string ${JSON.stringify(s)}` : `a ${typeof s}`);
    throw new ExtractError(
      `route "${method} ${fullPath}": @${decorator}(...) must be given a zod schema, but got ${got}. ` +
      `@${decorator} takes a SCHEMA (e.g. @${decorator}(z.object({ … }))), NOT a field name — ` +
      `the value it injects is the whole parsed ${decorator.toLowerCase()} validated against that schema. ` +
      `(NestJS-style @${decorator}("fieldName") is not supported.)`
    );
  }
}

// validateHeadersSchema enforces the reserved-header + string-type rules on a
// lowered @Headers(zod) JSON Schema. Reserved headers the platform owns are a
// DEPLOY ERROR, not a silent drop; non-string-typed header props are too (HTTP
// header values are always strings on the wire). Returns the schema unchanged
// when valid; throws ExtractError otherwise. Non-object schemas (or null) pass
// through verbatim.
function validateHeadersSchema(hs) {
  if (!hs || hs.type !== 'object' || !hs.properties) return hs;
  const reserved = new Set([
    'authorization', 'apikey', 'content-type', 'content-length',
    'idempotency-key', 'cookie', 'host',
  ]);
  for (const name of Object.keys(hs.properties)) {
    const lower = name.toLowerCase();
    if (reserved.has(lower) || lower.startsWith('x-palbase-')) {
      throw new ExtractError(
        `headers: "${name}" is a reserved header the platform manages; ` +
        `remove it from the headers schema`
      );
    }
    const prop = hs.properties[name];
    const t = prop && prop.type;
    if (t !== 'string') {
      throw new ExtractError(
        `headers: "${name}" must be a string-typed header ` +
        `(got ${JSON.stringify(t)}); HTTP header values are strings on the wire`
      );
    }
  }
  return hs;
}

// synthParamsSchema builds a JSON Schema object from all `kind:"param"`
// ParamMetas: one `{type:"string"}` property per @Param("<name>"), all required.
// Returns null when there are no path params.
function synthParamsSchema(params) {
  if (!Array.isArray(params)) return null;
  const names = params
    .filter((p) => p && p.kind === 'param' && typeof p.name === 'string' && p.name.length > 0)
    .map((p) => p.name);
  if (names.length === 0) return null;
  const properties = {};
  for (const name of names) {
    properties[name] = { type: 'string' };
  }
  return { type: 'object', properties, required: names };
}

// extractRateLimit lowers a route's rate-limit option into the wire shape.
// OBJECT form only: { max, window } with window in seconds. Anything else
// (string sugar, missing fields) → null (no rate limit).
function extractRateLimit(rateLimit) {
  if (
    rateLimit &&
    typeof rateLimit === 'object' &&
    typeof rateLimit.max === 'number' &&
    typeof rateLimit.window === 'number'
  ) {
    return { max: rateLimit.max, window: rateLimit.window };
  }
  return null;
}

// `@Upload` config (direct-storage upload). Present ONLY on upload routes — its
// presence is what marks the route as an upload route through the rest of the
// pipeline (sidecar → Go EndpointConfig.Upload → x-palbase-upload → codegen).
// Returns null for a non-upload route. bucket + pathTemplate are required;
// maxSize/allowedTypes are optional (the bucket's own limits apply when absent).
function extractUploadConfig(uploadConfig) {
  if (
    uploadConfig &&
    typeof uploadConfig === 'object' &&
    typeof uploadConfig.bucket === 'string' &&
    uploadConfig.bucket.length > 0 &&
    typeof uploadConfig.pathTemplate === 'string' &&
    uploadConfig.pathTemplate.length > 0
  ) {
    return {
      bucket: uploadConfig.bucket,
      pathTemplate: uploadConfig.pathTemplate,
      ...(typeof uploadConfig.maxSize === 'number' ? { maxSize: uploadConfig.maxSize } : {}),
      ...(Array.isArray(uploadConfig.allowedTypes) ? { allowedTypes: uploadConfig.allowedTypes } : {}),
    };
  }
  return null;
}

// deriveControllerName lowers a controller CLASS name into the dotted
// operationId namespace: strip ONE trailing "Controller" suffix
// (case-sensitive), then lower-case the first letter.
// MembersController → "members", UserProfileController → "userProfile",
// Members → "members". Returns "" when the derivation is empty (a class named
// exactly "Controller", or an anonymous class) — the sidecar then omits
// controllerName and the Go generator falls back to the flat operationId.
// MUST stay byte-for-byte identical to deriveControllerName in
// @palbase/backend's openapi/discover.ts (the dev-time spec twin).
function deriveControllerName(Ctrl) {
  let name = typeof Ctrl === 'function' && typeof Ctrl.name === 'string' ? Ctrl.name : '';
  if (name.endsWith('Controller')) name = name.slice(0, -'Controller'.length);
  if (name === '') return '';
  return name.charAt(0).toLowerCase() + name.slice(1);
}

// extractControllerMeta lowers a @Controller class into the sidecar contract.
function extractControllerMeta(Ctrl, zodToJSON, errorRegistry) {
  const controllerMeta = Ctrl[CONTROLLER_META_SYMBOL] || {};
  const basePath = typeof controllerMeta.basePath === 'string' ? controllerMeta.basePath : '';
  const controllerName = deriveControllerName(Ctrl);
  const defaultAuth = controllerMeta.defaultAuth;

  const rawRoutes = Ctrl[ROUTES_SYMBOL];
  const routeMetas = Array.isArray(rawRoutes) ? rawRoutes : [];
  // Merge buffered @Returns schemas (the SDK buffers them when @Returns runs
  // before the method decorator) — mirrors getRoutes().
  const returnBuffer = Ctrl[RETURN_BUFFER_SYMBOL];
  if (returnBuffer) {
    for (const route of routeMetas) {
      if (route && route.returnSchema === undefined && returnBuffer[route.fnName]) {
        route.returnSchema = returnBuffer[route.fnName];
      }
    }
  }

  const routes = [];
  for (const route of routeMetas) {
    if (!route || typeof route !== 'object') continue;

    const fnName = typeof route.fnName === 'string' ? route.fnName : '';
    const method = typeof route.method === 'string' ? route.method.toUpperCase() : '';
    const subpath = typeof route.subpath === 'string' ? route.subpath : '';
    // Full path = basePath + subpath, exactly as the SDK composes it.
    const fullPath = `${basePath}${subpath}`;

    // Reject Express-style :param segments — palbase uses OpenAPI {param} syntax.
    // A silent 404 (the router never matches /:id) is the worst possible failure
    // mode; make it a hard deploy error instead.
    const expressParam = fullPath.match(/\/:([\w]+)/);
    if (expressParam) {
      throw new ExtractError(
        `route "${method || route.method} ${fullPath}" uses Express-style ':${expressParam[1]}' path params — ` +
        `palbase uses OpenAPI-style braces: change ":${expressParam[1]}" to "{${expressParam[1]}}"`
      );
    }

    const options = (route.options && typeof route.options === 'object') ? route.options : {};
    const params = Array.isArray(route.params) ? route.params : [];

    // Effective auth = route.options.auth ?? controller.defaultAuth ?? true.
    const effectiveAuthValue =
      options.auth !== undefined ? options.auth :
      defaultAuth !== undefined ? defaultAuth :
      true;

    const bodyParam = findParam(params, 'body');
    const queryParam = findParam(params, 'query');
    const headersParam = findParam(params, 'headers');

    // @Body/@Query/@Headers each take a ZOD SCHEMA, not a field name. Passing a
    // string (the NestJS-style `@Query("workout_id")`) is a silent disaster: the
    // string lands as the "schema", zodToJSON returns null (no validation, no
    // OpenAPI param), and worker.js injects the WHOLE parsed query/body map into
    // the handler arg — so `findMany(q ? {col:q} : …)` runs with q = the entire
    // map and Postgres 400s ("invalid input syntax for type uuid") on EVERY call.
    // Fail the DEPLOY loudly here instead of shipping a 400-on-every-request
    // endpoint (the silent-accept that produced wave finding W3-F5).
    assertZodSchema(bodyParam, 'Body', method, fullPath);
    assertZodSchema(queryParam, 'Query', method, fullPath);
    assertZodSchema(headersParam, 'Headers', method, fullPath);

    const bodySchema = bodyParam ? zodToJSON(bodyParam.schema) : null;
    const querySchema = queryParam ? zodToJSON(queryParam.schema) : null;
    const headersSchema = headersParam ? validateHeadersSchema(zodToJSON(headersParam.schema)) : null;
    const paramsSchema = synthParamsSchema(params);
    const outputSchema = route.returnSchema ? zodToJSON(route.returnSchema) : null;

    // Inferred errors (typed backend errors): route.throws joined against the
    // registry; omitted entirely when nothing resolved.
    const errors = lowerRouteErrors(route.throws, errorRegistry || new Map(), zodToJSON);

    routes.push({
      mapKey: fnName,
      method,
      path: fullPath,
      auth: normalizeAuth(effectiveAuthValue),
      rateLimit: extractRateLimit(options.rateLimit),
      bodySchema,
      querySchema,
      paramsSchema,
      headersSchema,
      outputSchema,
      ...(errors.length > 0 ? { errors } : {}),
      // Direct-storage upload config (@Upload). Present ONLY on upload routes;
      // omitted entirely otherwise so a normal route's sidecar is unchanged.
      ...((u) => (u ? { upload: u } : {}))(extractUploadConfig(options.uploadConfig)),
      // Ordered injection plan: kind + (name for @Param). Schemas live on the
      // route-level *Schema fields, not duplicated per param.
      params: params
        .filter((p) => p && typeof p === 'object' && typeof p.index === 'number')
        .map((p) => {
          const entry = { index: p.index, kind: p.kind };
          if (p.kind === 'param' && typeof p.name === 'string') entry.name = p.name;
          return entry;
        }),
    });
  }

  return {
    controller: true,
    basePath,
    // Omitted (not "") when the derivation is empty — flat-fallback signal.
    ...(controllerName !== '' ? { controllerName } : {}),
    routes,
  };
}

main().catch(writeError);
