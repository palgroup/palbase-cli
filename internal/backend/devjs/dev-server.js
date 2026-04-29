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
const ENDPOINTS_DIR = path.join(PROJECT_ROOT, 'endpoints');

const METHOD_FILE_RE = /^(get|post|put|patch|delete)\.(c?js|mjs|ts)$/i;

// ── route table ────────────────────────────────────────────────────────
//
// Each entry: { method, urlPattern, regex, paramNames, modulePath }.
// urlPattern keeps original colons for printing; regex matches incoming
// requests with named capture groups.
const routes = new Map();

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

// ── ctx — simplified local twin of prod's pipeline ctx ─────────────────

function makeCtx(req, params, body, user) {
  return {
    input: body ?? {},
    params,
    req: {
      method: req.method,
      url: req.url,
      headers: req.headers,
    },
    user: user ?? null,
    env: { ...process.env, PALBASE_PROJECT_REF: PROJECT_REF },
    logger: {
      info: (...a) => console.log('[handler]', ...a),
      warn: (...a) => console.warn('[handler]', ...a),
      error: (...a) => console.error('[handler]', ...a),
      debug: (...a) => console.log('[handler:debug]', ...a),
    },
    palbase: {
      // `dev` has no live tenant connection; ctx.palbase.* surfaces
      // throw a clear error so the user knows they must `deploy` to
      // exercise live data flows. This avoids silent partial behaviour.
      auth: forbiddenLocal('auth'),
      documents: forbiddenLocal('documents'),
      database: forbiddenLocal('database'),
      storage: forbiddenLocal('storage'),
    },
  };
}

function forbiddenLocal(name) {
  const msg = `ctx.palbase.${name} is not available in \`palbase backend dev\` (local). Deploy to test against live services.`;
  return new Proxy({}, {
    get() { throw new Error(msg); },
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

// ── auth + rate limit gates ────────────────────────────────────────────
//
// Local dev intentionally skips full JWT signature verification — we want
// the loop tight. But we honour the endpoint config's `auth.required`
// gate so the user can SEE 401 when no Authorization header is present,
// and decode the bearer (without verify) into ctx.user so handlers that
// read ctx.user.id behave like prod.

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

function resolveDevUser(req) {
  const authHeader = req.headers['authorization'] || '';
  if (!authHeader.toLowerCase().startsWith('bearer ')) return null;
  const token = authHeader.slice(7).trim();
  const claims = decodeJwtPayload(token);
  if (!claims || !claims.sub) {
    // Even an opaque/garbled token counts as "authenticated" locally —
    // the user is signalling intent. Production rejects garbage; dev
    // gives you a synthetic user so handler logic stays exercised.
    return { id: 'dev_user', email: 'dev@local', role: 'authenticated', metadata: {} };
  }
  return {
    id: claims.sub,
    email: typeof claims.email === 'string' ? claims.email : 'dev@local',
    role: typeof claims.role === 'string' ? claims.role : 'authenticated',
    metadata: {},
  };
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

  // Auth gate — enforce config.auth.required even though local dev skips
  // signature verify, so the user can probe both legs of the contract.
  const authCfg = endpoint.auth || {};
  const authRequired = authCfg.required !== false && (authCfg.required === true || authCfg.role !== undefined);
  const user = resolveDevUser(req);
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
  log(`press Ctrl+C to quit`);
});
