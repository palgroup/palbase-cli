#!/usr/bin/env node
/**
 * Palbase backend dev server.
 *
 * Local equivalent of the prod backend-runtime endpoint dispatcher,
 * but tuned for hot reload + interactive output. The shape of every
 * user-facing surface (routes, ctx, defineEndpoint) matches prod so
 * what runs in `palbase backend dev` runs identically in
 * services-shared/br-<ref> after `palbase backend deploy`.
 *
 * Invocation (set by the Go CLI):
 *   PALBASE_DEV_PORT=4000 PALBASE_PROJECT_REF=foo PALBASE_DEV_ROOT=/abs/path node dev-server.js
 */
'use strict';

const fs = require('fs');
const path = require('path');
const http = require('http');
const url = require('url');

const PORT = Number(process.env.PALBASE_DEV_PORT) || 4000;
const PROJECT_ROOT = process.env.PALBASE_DEV_ROOT || process.cwd();
const PROJECT_REF = process.env.PALBASE_PROJECT_REF || 'local';
const PUBLIC_HOST = process.env.PALBASE_PUBLIC_HOST || '';
const ENDPOINTS_DIR = path.join(PROJECT_ROOT, 'endpoints');

// Capture pod-bound credentials into module-scope closures, then DELETE
// them from process.env BEFORE require()ing user code. User endpoints
// see ctx.env (a curated subset) — they cannot read process.env.PALBASE_*.
// Same security invariant as the prod runtime's worker.js, but enforced
// here for dev-server because this is a long-lived process re-loading
// user modules on file changes.
const TENANT_APIKEY = process.env.PALBASE_TENANT_APIKEY || '';
const TENANT_SERVICE_ROLE = process.env.PALBASE_TENANT_SERVICE_ROLE || '';
delete process.env.PALBASE_TENANT_APIKEY;
delete process.env.PALBASE_TENANT_SERVICE_ROLE;

// Public host the SDK calls. Same shape as prod: <ref>.<host>.
// When PROJECT_REF==='local' (no `palbase login`) we run without
// ctx.palbase live — bindings throw on use.
const PALBASE_URL = (PROJECT_REF !== 'local' && PUBLIC_HOST)
  ? `https://${PROJECT_REF}.${PUBLIC_HOST.replace(/\/+$/, '')}`
  : '';

const METHOD_FILE_RE = /^(get|post|put|patch|delete)\.(c?js|mjs|ts)$/i;

// ── route table ────────────────────────────────────────────────────────
//
// Each entry: { method, urlPattern, regex, paramNames, modulePath }.
// urlPattern keeps original colons for printing; regex matches incoming
// requests with named capture groups.
const routes = new Map();

// deriveOperationId — same dotted-segment convention the deployed
// backend-runtime uses (see modules/backend/internal/management/
// endpoint_router.go). Each endpoint gets a `/rpc/{operationId}` alias
// so iOS/TS clients can do typed `pb.backend.call("todos.list", ...)`
// against the local dev-server with the same wire shape as production.
function deriveOperationId(segments, method) {
  const parts = segments
    .map((s) => (s.startsWith(':') ? s.slice(1) : s))
    .filter((s) => s.length > 0);
  if (method && method !== 'POST') {
    parts.push(method.toLowerCase());
  }
  return parts.join('.');
}

function registerEndpoints() {
  routes.clear();
  if (!fs.existsSync(ENDPOINTS_DIR)) {
    log(`endpoints/ not found at ${ENDPOINTS_DIR}`);
    return;
  }
  walk(ENDPOINTS_DIR, (file) => {
    const rel = path.relative(ENDPOINTS_DIR, file).replace(/\\/g, '/');
    const m = METHOD_FILE_RE.exec(path.basename(rel));
    if (!m) return;
    const method = m[1].toUpperCase();
    const segments = rel.split('/').slice(0, -1).map((seg) =>
      seg.startsWith('[') && seg.endsWith(']') ? `:${seg.slice(1, -1)}` : seg,
    );
    const urlPattern = '/' + segments.join('/');
    const paramNames = [];
    const regexSrc = '^' + segments.map((s) => {
      if (s.startsWith(':')) {
        paramNames.push(s.slice(1));
        return '([^/]+)';
      }
      return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    }).join('/').replace(/^/, '/') + '/?$';
    routes.set(method + ' ' + urlPattern, {
      method,
      urlPattern,
      regex: new RegExp(regexSrc),
      paramNames,
      modulePath: file,
    });

    // RPC alias: every endpoint also responds at `POST /rpc/{operationId}`
    // regardless of its declared HTTP method. Path params are folded into
    // dotted segments — RPC clients pass them in the body, not the URL.
    const operationId = deriveOperationId(segments, method);
    if (operationId) {
      const rpcPattern = `/rpc/${operationId}`;
      routes.set('POST ' + rpcPattern, {
        method: 'POST',
        urlPattern: rpcPattern,
        regex: new RegExp('^' + rpcPattern.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '/?$'),
        paramNames: [],
        modulePath: file,
      });
    }
  });
  log(`registered ${routes.size} endpoint(s):`);
  for (const route of routes.values()) {
    log(`  ${route.method.padEnd(6)} ${route.urlPattern}  →  ${path.relative(PROJECT_ROOT, route.modulePath)}`);
  }
}

function walk(dir, cb) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(full, cb);
    else cb(full);
  }
}

// ── ctx — bit-perfect twin of prod's pipeline ctx ──────────────────────
//
// ctx.palbase is a real ServerClient hitting the same public hosts the
// deployed pod hits. Local dev = LIVE data: writes go to production
// palauth/paldocs/db. The CLI prints a banner before starting the server
// so this isn't a silent surprise.

let palbaseClientSingleton = null;
function getPalbaseClient() {
  if (!PALBASE_URL || !TENANT_APIKEY) return null;
  if (palbaseClientSingleton) return palbaseClientSingleton;
  const { createServerClient } = require('@palbase/server');
  // Mirror worker.js (the prod backend-runtime path): the apikey
  // header drives Kong's scope decision (`s` = service-role / RLS
  // bypass, `c` = anon). ctx.palbase IS the project's privileged
  // surface, so the service-role key MUST be the primary apikey
  // when present — passing the anon key here makes Kong stamp
  // role=anon on the iJWT and downstream services (paldocs)
  // SET LOCAL ROLE anon, which has read-only grants. Falls back
  // to anon only when service-role wasn't revealed.
  const primaryKey = TENANT_SERVICE_ROLE || TENANT_APIKEY;
  palbaseClientSingleton = createServerClient(primaryKey, {
    url: PALBASE_URL,
    ...(TENANT_SERVICE_ROLE ? { serviceRole: TENANT_SERVICE_ROLE } : {}),
  });
  return palbaseClientSingleton;
}

function makeCtx(req, params, body, user) {
  const client = getPalbaseClient();
  return {
    input: body ?? {},
    params,
    req: {
      method: req.method,
      url: req.url,
      headers: req.headers,
    },
    user: user ?? null,
    // ctx.env exposes a curated subset of process.env. PALBASE_TENANT_*
    // were deleted at boot; user palbase.toml env vars (whatever they
    // chose) ARE in process.env and pass through.
    env: { ...process.env, PALBASE_PROJECT_REF: PROJECT_REF },
    log: {
      info: (...a) => console.log('[handler]', ...a),
      warn: (...a) => console.warn('[handler]', ...a),
      error: (...a) => console.error('[handler]', ...a),
      debug: (...a) => console.log('[handler:debug]', ...a),
    },
    palbase: client ?? notConfiguredPalbase(),
  };
}

function notConfiguredPalbase() {
  return new Proxy({}, {
    get(_t, name) {
      return new Proxy({}, {
        get() {
          throw new Error(
            `ctx.palbase.${String(name)} unavailable: dev-server has no tenant credentials. ` +
            `Run \`palbase login\` then \`palbase backend dev\` from inside a project directory.`,
          );
        },
      });
    },
  });
}

// ── reload ──────────────────────────────────────────────────────────────

function loadEndpoint(modulePath) {
  // Bust require cache so saves take effect without restart.
  delete require.cache[require.resolve(modulePath)];
  const mod = require(modulePath);
  const exported = mod.default ?? mod;
  if (!exported || typeof exported.handler !== 'function') {
    throw new Error(`endpoint at ${modulePath} must export { default: { method, handler } }`);
  }
  return exported;
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

  let endpoint;
  try {
    endpoint = loadEndpoint(route.modulePath);
  } catch (err) {
    res.statusCode = 500;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'load_error', message: err.message }));
    log(`[${req.method}] ${parsed.pathname}  500  ${Date.now() - start}ms  — ${err.message}`);
    return;
  }

  // Auth gate — same contract as prod: optional => no token = pass with
  // ctx.user=null, present token = palauth verify. Required => must have
  // a token AND palauth must accept it.
  const authCfg = endpoint.auth || {};
  const authRequired = authCfg.required !== false && (authCfg.required === true || authCfg.role !== undefined);
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
  if (endpoint.rateLimit) {
    const ip = (req.headers['x-forwarded-for'] || req.socket.remoteAddress || 'local').toString().split(',')[0].trim();
    const result = rateLimitCheck(`${route.method} ${route.urlPattern}`, ip, endpoint.rateLimit);
    res.setHeader('x-ratelimit-limit', String(endpoint.rateLimit.max));
    res.setHeader('x-ratelimit-remaining', String(Math.max(0, result.remaining)));
    if (!result.allowed) {
      res.statusCode = 429;
      res.setHeader('retry-after', String(result.retryAfter));
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({
        error: 'rate_limited',
        message: `max ${endpoint.rateLimit.max} req / ${endpoint.rateLimit.window}s`,
        retry_after: result.retryAfter,
      }));
      log(`[${req.method}] ${parsed.pathname}  429  ${Date.now() - start}ms  — rate limited (retry in ${result.retryAfter}s)`);
      return;
    }
  }

  let result;
  try {
    const ctx = makeCtx(req, params, body, user);
    result = await endpoint.handler(ctx);
  } catch (err) {
    res.statusCode = 500;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ error: 'handler_error', message: err.message, stack: err.stack }));
    log(`[${req.method}] ${parsed.pathname}  500  ${Date.now() - start}ms  — ${err.message}`);
    return;
  }
  res.statusCode = 200;
  res.setHeader('content-type', 'application/json');
  res.end(JSON.stringify(result ?? null));
  log(`[${req.method}] ${parsed.pathname}  200  ${Date.now() - start}ms`);
});

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

function watchEndpoints() {
  if (!fs.existsSync(ENDPOINTS_DIR)) return;
  fs.watch(ENDPOINTS_DIR, { recursive: true }, (event, filename) => {
    if (!filename) return;
    log(`reload: ${event} ${filename}`);
    registerEndpoints();
  });
}

function log(msg) {
  const ts = new Date().toISOString().slice(11, 19);
  process.stdout.write(`[palbase dev ${ts}] ${msg}\n`);
}

// ── boot ────────────────────────────────────────────────────────────────

registerEndpoints();
watchEndpoints();

server.listen(PORT, () => {
  log(`listening on http://127.0.0.1:${PORT}`);
  log(`project ref: ${PROJECT_REF}`);
  log(`watching: ${ENDPOINTS_DIR}`);
  if (PALBASE_URL) {
    log('────────────────────────────────────────────────────────────');
    log(`⚠ connected to LIVE data for project ${PROJECT_REF}`);
    log(`  ctx.palbase.* writes hit ${PALBASE_URL}`);
    if (TENANT_SERVICE_ROLE) {
      log(`  scope: service-role (RLS bypass) — key ${TENANT_SERVICE_ROLE.slice(0, 16)}…`);
    } else {
      log(`  scope: anon — RLS WILL apply, writes to protected collections will fail`);
      log(`         (apikey.reveal returned no service-role; likely missing default key)`);
    }
    log(`  Auth tokens verified by ${PALBASE_URL}/auth/user`);
    log('────────────────────────────────────────────────────────────');
  } else {
    log('ctx.palbase.* disabled (no project credentials). Run `palbase login` then re-run.');
  }
  log(`press Ctrl+C to quit`);
});
