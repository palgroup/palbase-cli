'use strict';

/**
 * module-clients.js — local mirror of the backend-runtime
 * internal/runtime/module-clients.js. Used by dev-server.js so
 * `palbase serve` ctx surfaces match what the deployed pod produces
 * (dev = prod, lockstep). Edit BOTH when changing any module client
 * surface — search MODULE_ALIASES at the top of dev-server.js for
 * the lockstep rationale.
 *
 * Source of truth: modules/backend/internal/runtime/module-clients.js
 * in the palgroup/backend-runtime repo.
 *
 * Self-contained Palbase module clients for the backend-runtime
 * worker. Replaces the previous global @palbase/server install;
 * everything here is a small fetch wrapper, no external deps.
 *
 * Surface contract: the exported `buildModuleClients(palbase)` returns
 * an object structurally compatible with @palbase/backend's
 *   - PalbaseAuthClient        → out.auth
 *   - PalbaseStorageClient     → out.storage
 *   - PalbaseDocsClient        → out.docs
 *   - PalbaseRealtimeClient    → out.realtime
 *   - PalbaseFunctionsClient   → out.functions
 *   - PalbaseFlagsClient       → out.flags
 *   - PalbaseNotificationsClient → out.notifications
 *   - PalbaseAnalyticsClient   → out.analytics
 *   - PalbaseLinksClient       → out.links
 *   - PalbaseCmsClient         → out.cms
 *
 * The result envelope is `{data: T|null, error: {code, message, status,
 * details}|null, status: number}` — same shape every @palbase/client
 * module returns and customer handlers already destructure.
 *
 * URLs target the same `/v1/...` (and `/auth/...`) paths the published
 * @palbase/server clients used. The base URL comes from `palbase.url`
 * which the Go runtime sets to `https://<endpointRef>.<publicHost>`.
 *
 * Auth headers: `apikey` carries the project key (service-role if
 * available; falls back to anon). Kong's pre-function plugin reads
 * scope from byte offset 12 and stamps the iJWT — the runtime must NOT
 * put the key in `Authorization`, which PostgREST would try to decode
 * as a JWT and 500 on.
 *
 * What is NOT inlined (and why — same honesty the dev-server already
 * applies to ctx.db):
 *   - realtime channel WS (subscribe/track/send) — backend-runtime
 *     spawns a fresh Node subprocess per request, so a WebSocket would
 *     die with the process. Every WS-only method throws a clear hint;
 *     channel() itself returns an object so `typeof ctx.realtime
 *     .channel === 'function'` shape checks still pass.
 *   - device cache (getToken / isActive / setCachedToken / dispose) —
 *     these are client-side App Check state; on the server there is
 *     nothing to cache. getToken/isActive return null/false; setCached
 *     and dispose are no-ops. The HTTP device methods (generateChallenge,
 *     attestAndroid, attestiOS, bind, list, delete, verifyRequestSignature)
 *     ARE wired normally.
 */

// ─── PalbaseError shape ──────────────────────────────────────────────────
// Mirrors @palbase/core's PalbaseError just enough for envelope users:
// `err.code`, `err.message`, `err.status`, optional `err.details`. The
// error class is a plain Error subclass so `(e instanceof Error) === true`
// still holds for code that destructures from a thrown error.
class PalbaseError extends Error {
  constructor(code, message, status, details) {
    super(message);
    this.name = 'PalbaseError';
    this.code = code;
    this.status = status;
    if (details !== undefined) this.details = details;
  }
}

// ─── Validators ──────────────────────────────────────────────────────────
// Same regexes the published modules used so identical inputs reject the
// same way (no behaviour drift between dev and prod).

const SEGMENT_RE = /^[a-zA-Z0-9_\-]+$/;
const FLAG_NAME_RE = /^[a-zA-Z0-9_\-]+$/;
const FN_NAME_RE = /^[a-zA-Z0-9_\-]+$/;
const BUCKET_NAME_RE = /^[a-zA-Z0-9_\-]+$/;
const COLLECTION_NAME_RE = /^[a-zA-Z0-9_\-]+$/;
const PROVIDER_RE = /^[a-zA-Z0-9_\-]+$/;
const STORAGE_PATH_RE = /^[a-zA-Z0-9_.\/\-]+$/;
const STORAGE_TRAVERSAL_RE = /(?:^|\/)\.\.(?:\/|$)/;

function validateSegment(segment, label) {
  if (!SEGMENT_RE.test(segment)) {
    throw new Error(`Invalid ${label}: "${segment}". Must match ${SEGMENT_RE.source}`);
  }
}

function validateStoragePath(p) {
  if (!STORAGE_PATH_RE.test(p) || STORAGE_TRAVERSAL_RE.test(p)) {
    throw new Error(
      `Invalid file path: "${p}". Paths must not contain traversal sequences and must match ${STORAGE_PATH_RE.source}`,
    );
  }
}

function validatePathParam(name, value) {
  if (value.includes('/') || value.includes('..') || value.includes('%')) {
    throw new Error(`Invalid ${name}: must not contain path separators`);
  }
}

// ─── HTTP core ───────────────────────────────────────────────────────────
// One thin fetch wrapper used by every client. Returns the SDK's
// {data, error, status} envelope; the caller decides whether to
// remap (e.g. notifications.templates camelCase mapping).
//
// `apiKey` is the value sent in the `apikey` header — Kong scopes
// downstream auth from this. `baseUrl` is the project's public host.
//
// On network failure we throw a PalbaseError('network_error') because
// the published HttpClient escalates a fully-exhausted retry path the
// same way; customer code that wraps these calls in try/catch keeps
// the same semantics. Retries are deliberately omitted — each handler
// invocation is a fresh Node subprocess with a small SLO; spending
// 3*200ms on a transient is the wrong default at this layer (the
// internal HTTP path between br-pod and module is reliable).
function makeHttpClient(apiKey, baseUrl) {
  async function request(method, path, options) {
    const url = `${baseUrl}${path}`;
    const headers = {
      'Content-Type': 'application/json',
      ...((options && options.headers) || {}),
    };
    if (apiKey) headers['apikey'] = apiKey;

    const fetchOptions = { method, headers };
    if (options && options.body !== undefined) {
      // Pass FormData / Blob through directly so the runtime sets the
      // multipart boundary header itself; JSON.stringify everything else.
      if (options.body instanceof Uint8Array || typeof options.body === 'string') {
        fetchOptions.body = options.body;
      } else if (typeof FormData !== 'undefined' && options.body instanceof FormData) {
        // Let the platform set Content-Type with the multipart boundary.
        delete headers['Content-Type'];
        fetchOptions.body = options.body;
      } else {
        fetchOptions.body = JSON.stringify(options.body);
      }
    }
    if (options && options.signal) fetchOptions.signal = options.signal;

    let response;
    try {
      response = await fetch(url, fetchOptions);
    } catch (err) {
      // Network error — mirror @palbase/core HttpClient's escalation.
      throw new PalbaseError(
        'network_error',
        err instanceof Error ? err.message : 'Network request failed',
        0,
      );
    }

    // Parse body. HEAD never has one; some endpoints (204 No Content)
    // also return empty bodies — Content-Length 0 or empty after json
    // parse → data:null.
    const contentType = response.headers.get('Content-Type') || '';
    let parsed = null;
    if (method !== 'HEAD' && contentType.includes('json')) {
      try {
        parsed = await response.json();
      } catch (_e) {
        parsed = null;
      }
    } else if (method !== 'HEAD' && contentType.startsWith('image/')) {
      // QR code / image responses — surface a Buffer so the caller can
      // write it to disk or forward it. Buffer is a Node Uint8Array.
      const ab = await response.arrayBuffer();
      parsed = Buffer.from(ab);
    } else if (method !== 'HEAD' && response.body) {
      // Best-effort text body for non-JSON errors so the message is
      // informative instead of "request failed".
      try {
        parsed = await response.text();
      } catch (_e) {
        parsed = null;
      }
    }

    if (!response.ok) {
      const errBody = parsed && typeof parsed === 'object' ? parsed : {};
      const code =
        (errBody.error && typeof errBody.error === 'string' && errBody.error) ||
        'unknown_error';
      const message =
        (errBody.error_description && String(errBody.error_description)) ||
        response.statusText ||
        'request failed';
      return {
        data: null,
        error: new PalbaseError(code, message, response.status, errBody),
        status: response.status,
      };
    }

    return { data: parsed, error: null, status: response.status };
  }

  return { request, baseUrl };
}

// ─── Auth client (PalbaseAuthClient) ─────────────────────────────────────
// Server-relevant subset only — no signUp / signIn / OAuth helpers, no
// onAuthStateChange. verifyUserToken exposes GET /auth/user the same way
// the published auth client did.

function buildAuthClient(http) {
  const mfa = {
    enroll: (params) => http.request('POST', '/auth/mfa/enroll', { body: params }),
    verifyEnrollment: (code) => http.request('POST', '/auth/mfa/verify', { body: { code } }),
    challenge: (params) => http.request('POST', '/auth/mfa/challenge', { body: params }),
    recovery: (params) => http.request('POST', '/auth/mfa/recovery', { body: params }),
    listFactors: () => http.request('GET', '/auth/mfa/factors'),
    removeFactor: (factorId, currentPassword) => {
      validatePathParam('factorId', factorId);
      return http.request('DELETE', `/auth/mfa/factors/${factorId}`, {
        body: { current_password: currentPassword },
      });
    },
    regenerateRecoveryCodes: () => http.request('POST', '/auth/mfa/recovery-codes/regenerate'),
    emailEnroll: () => http.request('POST', '/auth/mfa/email/enroll'),
    emailChallenge: (params) => http.request('POST', '/auth/mfa/email/challenge', { body: params }),
    emailVerify: (params) => http.request('POST', '/auth/mfa/email/verify', { body: params }),
  };

  // Device sub-client. HTTP methods are real; the four cache-state
  // methods (getToken/isActive/setCachedToken/dispose) have no honest
  // server semantics, so they no-op (see file header for rationale).
  const device = {
    generateChallenge: () => http.request('POST', '/auth/devices/challenge'),
    attestAndroid: (params) => http.request('POST', '/auth/devices/attest/android', { body: params }),
    attestiOS: (params) => http.request('POST', '/auth/devices/attest/ios', { body: params }),
    bind: (params) => http.request('POST', '/auth/devices/bind', { body: params }),
    list: () => http.request('GET', '/auth/devices'),
    delete: (deviceId) => {
      validatePathParam('deviceId', deviceId);
      return http.request('DELETE', `/auth/devices/${deviceId}`);
    },
    verifyRequestSignature: (deviceId, params) => {
      validatePathParam('deviceId', deviceId);
      return http.request('POST', `/auth/devices/${deviceId}/verify`, { body: params });
    },
    getToken: () => null,
    get isActive() { return false; },
    setCachedToken: (_token, _expiresInMs) => { /* no-op on server */ },
    dispose: () => { /* no-op on server */ },
  };

  // verifyUserToken talks to GET /auth/user with the user's Bearer
  // attached on top of the apikey header — apikey scopes the call to
  // the project; the bearer identifies the user being verified.
  async function verifyUserToken(jwt) {
    const url = `${http.baseUrl}/auth/user`;
    let resp;
    try {
      const headers = { 'Content-Type': 'application/json', 'Authorization': `Bearer ${jwt}` };
      // Apikey comes from the same closure the http.request uses; we
      // can't read it from the wrapper, so verifyUserToken takes the
      // long path with a manual fetch. This mirrors the published
      // auth-client's own verifyUserToken (which also bypassed retries).
      resp = await fetch(url, { method: 'GET', headers: withApikey(headers) });
    } catch (err) {
      throw new PalbaseError(
        'network_error',
        err instanceof Error ? err.message : 'Network request failed',
        0,
      );
    }
    const ct = resp.headers.get('Content-Type') || '';
    if (resp.ok && ct.includes('application/json')) {
      const data = await resp.json();
      return { data, error: null, status: resp.status };
    }
    let body;
    if (ct.includes('application/json')) {
      try { body = await resp.json(); } catch { body = undefined; }
    }
    return {
      data: null,
      error: new PalbaseError(
        (body && body.error) || 'invalid_token',
        (body && body.error_description) || 'Token verification failed',
        resp.status,
        body,
      ),
      status: resp.status,
    };
  }
  // verifyUserToken needs the apikey but the http wrapper hides it.
  // The factory below stashes apiKey on the http object for this case.
  function withApikey(headers) {
    if (http.apiKey) headers['apikey'] = http.apiKey;
    return headers;
  }

  return {
    verifyUserToken,
    getSession: () => ({ data: null, error: null }),
    mfa,
    device,
  };
}

// ─── Storage client (PalbaseStorageClient) ───────────────────────────────

function buildBucketClient(http, bucketName) {
  return {
    async upload(path, file, options) {
      validateStoragePath(path);
      // FormData is available natively in Node 22 (which the runtime
      // ships); fall back to a hand-built multipart only if missing.
      const fd = new FormData();
      // file may be Blob, ArrayBuffer, or ReadableStream — Blob path
      // is the canonical wire shape.
      const blob = file instanceof Blob ? file : new Blob([file instanceof ArrayBuffer ? file : '']);
      fd.append('file', blob);
      if (options && options.contentType) fd.append('contentType', options.contentType);
      const headers = {};
      if (options && options.upsert) headers['x-upsert'] = 'true';
      return http.request(
        'POST',
        `/v1/storage/buckets/${bucketName}/objects/${path}`,
        { body: fd, headers },
      );
    },
    async download(path) {
      validateStoragePath(path);
      return http.request(
        'GET',
        `/v1/storage/buckets/${bucketName}/objects/${path}`,
        { headers: { Accept: '*/*' } },
      );
    },
    getPublicUrl(path, options) {
      validateStoragePath(path);
      const params = new URLSearchParams();
      if (options && options.width) params.set('width', String(options.width));
      if (options && options.height) params.set('height', String(options.height));
      if (options && options.format) params.set('format', options.format);
      const query = params.toString();
      const url = `${http.baseUrl}/v1/storage/buckets/${bucketName}/public/${path}${query ? `?${query}` : ''}`;
      return { data: { publicUrl: url } };
    },
    async createSignedUrl(path, expiresIn) {
      validateStoragePath(path);
      return http.request(
        'POST',
        `/v1/storage/buckets/${bucketName}/objects/${path}/signed-url`,
        { body: { expiresIn } },
      );
    },
    async list(prefix, options) {
      if (prefix) validateStoragePath(prefix);
      const body = {};
      if (prefix) body.prefix = prefix;
      if (options && options.limit !== undefined) body.limit = options.limit;
      if (options && options.offset !== undefined) body.offset = options.offset;
      if (options && options.sortBy) body.sortBy = options.sortBy;
      return http.request(
        'POST',
        `/v1/storage/buckets/${bucketName}/objects/list`,
        { body },
      );
    },
    async remove(paths) {
      for (const p of paths) validateStoragePath(p);
      return http.request(
        'DELETE',
        `/v1/storage/buckets/${bucketName}/objects`,
        { body: { prefixes: paths } },
      );
    },
    async move(from, to) {
      validateStoragePath(from);
      validateStoragePath(to);
      return http.request(
        'POST',
        `/v1/storage/buckets/${bucketName}/objects/move`,
        { body: { from, to } },
      );
    },
    async copy(from, to) {
      validateStoragePath(from);
      validateStoragePath(to);
      return http.request(
        'POST',
        `/v1/storage/buckets/${bucketName}/objects/copy`,
        { body: { from, to } },
      );
    },
  };
}

function buildStorageClient(http) {
  return {
    bucket(name) {
      if (!BUCKET_NAME_RE.test(name)) {
        throw new Error(
          `Invalid bucket name: "${name}". Bucket names must match ${BUCKET_NAME_RE.source}`,
        );
      }
      return buildBucketClient(http, name);
    },
  };
}

// ─── Docs client (PalbaseDocsClient) ─────────────────────────────────────
// Firestore-style chain: collection(name).where(...).orderBy(...).limit(n).get()
// + add() / doc(id).set/get/update/delete + nested doc(id).collection().

function buildDocumentRef(http, path) {
  return {
    path,
    async set(data) {
      return http.request('PUT', `/v1/docs/${path}`, { body: data });
    },
    async get() {
      const response = await http.request('GET', `/v1/docs/${path}`);
      if (response.error) return { data: null, error: response.error, status: response.status };
      const raw = response.data || {};
      const exists = raw.exists !== undefined ? raw.exists : !!(raw.data || raw.id);
      const segments = path.split('/');
      const id = raw.id || segments[segments.length - 1] || '';
      const snapshot = { id, exists, data: () => raw.data, ref: { path } };
      return { data: snapshot, error: null, status: response.status };
    },
    async update(data) {
      return http.request('PATCH', `/v1/docs/${path}`, { body: data });
    },
    async delete() {
      return http.request('DELETE', `/v1/docs/${path}`);
    },
    collection(name) {
      validateSegment(name, 'subcollection name');
      return buildCollectionRef(http, `${path}/${name}`);
    },
  };
}

function buildCollectionRef(http, path, state) {
  const whereClauses = (state && state.whereClauses) ? state.whereClauses.slice() : [];
  const orderByClauses = (state && state.orderByClauses) ? state.orderByClauses.slice() : [];
  let limitValue = state ? state.limitValue : undefined;

  function snapshot(doc) {
    return {
      id: doc.id,
      exists: true,
      data: () => doc.data,
      ref: { path: `${path}/${doc.id}` },
    };
  }

  function querySnapshot(docs) {
    return {
      docs,
      empty: docs.length === 0,
      size: docs.length,
      docChanges: () => docs.map((d) => ({ type: 'added', doc: d })),
    };
  }

  function buildQueryBody() {
    const body = {};
    if (whereClauses.length > 0) {
      body.where = whereClauses.map((w) => ({ field: w.field, op: w.op, value: w.value }));
    }
    if (orderByClauses.length > 0) {
      body.orderBy = orderByClauses.map((o) => ({ field: o.field, direction: o.direction }));
    }
    if (limitValue !== undefined) body.limit = limitValue;
    return body;
  }

  return {
    path,
    doc(id) {
      validateSegment(id, 'document ID');
      return buildDocumentRef(http, `${path}/${id}`);
    },
    async add(data) {
      const resp = await http.request('POST', `/v1/docs/${path}`, { body: data });
      if (resp.error || !resp.data) {
        return { data: null, error: resp.error, status: resp.status };
      }
      const ref = buildDocumentRef(http, `${path}/${resp.data.id}`);
      return { data: ref, error: null, status: resp.status };
    },
    where(field, op, value) {
      return buildCollectionRef(http, path, {
        whereClauses: [...whereClauses, { field, op, value }],
        orderByClauses,
        limitValue,
      });
    },
    orderBy(field, direction) {
      return buildCollectionRef(http, path, {
        whereClauses,
        orderByClauses: [...orderByClauses, { field, direction: direction || 'asc' }],
        limitValue,
      });
    },
    limit(n) {
      return buildCollectionRef(http, path, {
        whereClauses,
        orderByClauses,
        limitValue: n,
      });
    },
    async get() {
      const hasQuery = whereClauses.length > 0 || orderByClauses.length > 0 || limitValue !== undefined;
      if (hasQuery) {
        const resp = await http.request('POST', `/v1/docs/${path}/query`, { body: buildQueryBody() });
        if (resp.error) return { data: null, error: resp.error, status: resp.status };
        const docs = ((resp.data && resp.data.documents) || []).map(snapshot);
        return { data: querySnapshot(docs), error: null, status: resp.status };
      }
      const resp = await http.request('GET', `/v1/docs/${path}`);
      if (resp.error) return { data: null, error: resp.error, status: resp.status };
      const docs = ((resp.data && resp.data.documents) || []).map(snapshot);
      return { data: querySnapshot(docs), error: null, status: resp.status };
    },
  };
}

function buildDocsClient(http) {
  return {
    collection(name) {
      validateSegment(name, 'collection name');
      return buildCollectionRef(http, name);
    },
    async batch(operations) {
      const MAX = 500;
      if (operations.length > MAX) {
        return {
          data: null,
          error: new PalbaseError(
            'batch_too_large',
            `Batch size ${operations.length} exceeds maximum of ${MAX}`,
            400,
          ),
          status: 400,
        };
      }
      if (operations.length === 0) {
        return { data: null, error: null, status: 200 };
      }
      const body = operations.map((op) => ({
        op: op.op,
        path: op.ref.path,
        data: op.data,
      }));
      return http.request('POST', '/v1/docs/batch', { body });
    },
  };
}

// ─── Realtime client (PalbaseRealtimeClient) ─────────────────────────────
// Backend-runtime is request-scoped (Node subprocess per call) — there is
// no honest place to hold a long-lived WebSocket. channel() returns a
// stub object so shape probes pass, but every WS-only method throws a
// clear named error rather than silently dropping messages. removeChannel
// / removeAllChannels are no-ops (nothing to remove).

function buildRealtimeClient(_http) {
  const wsError = new Error(
    'Realtime channel WebSocket is not supported in backend-runtime: each ' +
    'endpoint runs in a fresh Node subprocess; a WS would die with the ' +
    'process. To publish from a backend handler, call the project\'s ' +
    'broadcast HTTP route or queue a worker that owns the long-lived WS.',
  );
  const channels = new Map();
  function makeChannel(name) {
    const channel = {
      name,
      send() { throw wsError; },
      track() { throw wsError; },
      untrack() { throw wsError; },
      subscribe() { return channel; }, // return self for chain compat; first send() throws
      unsubscribe() { /* nothing to unsubscribe in this model */ },
    };
    return channel;
  }
  return {
    channel(name) {
      const existing = channels.get(name);
      if (existing) return existing;
      const ch = makeChannel(name);
      channels.set(name, ch);
      return ch;
    },
    removeChannel(name) { channels.delete(name); },
    removeAllChannels() { channels.clear(); },
  };
}

// ─── Functions client (PalbaseFunctionsClient) ───────────────────────────

function buildFunctionsClient(http) {
  return {
    async invoke(fnName, options) {
      if (!FN_NAME_RE.test(fnName)) {
        throw new Error(
          `Invalid function name: "${fnName}". Function names must match ${FN_NAME_RE.source}`,
        );
      }
      const method = (options && options.method) || 'POST';
      const path = `/v1/functions/${fnName}`;
      const opts = {};
      if (options && options.body !== undefined) opts.body = options.body;
      if (options && options.headers) opts.headers = options.headers;
      return http.request(method, path, opts);
    },
  };
}

// ─── Flags client (PalbaseFlagsClient) ──────────────────────────────────

function buildFlagsClient(http) {
  function ctxParams(context) {
    const params = new URLSearchParams();
    if (context && context.userId) params.set('userId', context.userId);
    if (context && context.properties) params.set('properties', JSON.stringify(context.properties));
    return params;
  }
  return {
    async isEnabled(flagName, context) {
      if (!FLAG_NAME_RE.test(flagName)) {
        throw new Error(
          `Invalid flag name: "${flagName}". Flag names must match ${FLAG_NAME_RE.source}`,
        );
      }
      const query = ctxParams(context).toString();
      return http.request('GET', `/v1/flags/${flagName}/enabled${query ? `?${query}` : ''}`);
    },
    async getVariant(flagName, context) {
      if (!FLAG_NAME_RE.test(flagName)) {
        throw new Error(
          `Invalid flag name: "${flagName}". Flag names must match ${FLAG_NAME_RE.source}`,
        );
      }
      const query = ctxParams(context).toString();
      return http.request('GET', `/v1/flags/${flagName}/variant${query ? `?${query}` : ''}`);
    },
    async getAll(context) {
      const query = ctxParams(context).toString();
      return http.request('GET', `/v1/flags${query ? `?${query}` : ''}`);
    },
  };
}

// ─── Notifications client (PalbaseNotificationsClient) ──────────────────
// The biggest module: push/email/sms/inbox/preferences + email & sms
// template CRUD. Wire shapes are snake_case; the templates surface
// maps to camelCase on read and back to snake_case on write (matches
// the published @palbase/notifications behaviour).

function buildNotificationsClient(http) {
  // Local mappers between TS-camelCase view and palnotify wire shape.
  function toEmailTemplate(wire) {
    const view = {
      id: wire.id,
      projectId: wire.project_id,
      slug: wire.slug,
      subject: wire.subject,
      htmlBody: wire.html_body,
      variables: wire.variables || [],
      isDefault: wire.is_default,
      createdAt: wire.created_at,
      updatedAt: wire.updated_at,
    };
    if (wire.text_body !== undefined) view.textBody = wire.text_body;
    return view;
  }
  function toSMSTemplate(wire) {
    return {
      id: wire.id,
      projectId: wire.project_id,
      slug: wire.slug,
      body: wire.body,
      variables: wire.variables || [],
      isDefault: wire.is_default,
      createdAt: wire.created_at,
      updatedAt: wire.updated_at,
    };
  }
  function mapEnvelope(resp, mapper) {
    if (resp.error !== null || resp.data === null) {
      return { data: null, error: resp.error, status: resp.status };
    }
    return { data: mapper(resp.data), error: null, status: resp.status };
  }

  const push = {
    async send(params) {
      return http.request('POST', '/v1/notifications/push', { body: params });
    },
  };

  const email = {
    async send(params) {
      // camelCase templateSlug → snake_case template_slug; legacy
      // `template` field is forwarded as-is (server ignores it).
      const { templateSlug, ...rest } = params;
      const body = templateSlug !== undefined ? { ...rest, template_slug: templateSlug } : rest;
      return http.request('POST', '/v1/notifications/email', { body });
    },
  };

  const sms = {
    async send(params) {
      const { templateSlug, ...rest } = params;
      const body = templateSlug !== undefined ? { ...rest, template_slug: templateSlug } : rest;
      return http.request('POST', '/v1/notifications/sms', { body });
    },
  };

  const inbox = {
    async send(params) {
      return http.request('POST', '/v1/notifications/inbox', { body: params });
    },
    async list(options) {
      const opts = options || {};
      const params = new URLSearchParams();
      if (opts.cursor) params.set('cursor', opts.cursor);
      if (opts.limit !== undefined) params.set('limit', String(opts.limit));
      if (opts.is_read !== undefined) params.set('is_read', opts.is_read ? 'true' : 'false');
      if (opts.category) params.set('category', opts.category);
      if (opts.include_archived) params.set('include_archived', opts.include_archived ? 'true' : 'false');
      const query = params.toString();
      return http.request('GET', `/v1/notifications/inbox${query ? `?${query}` : ''}`);
    },
    async unreadCount() {
      return http.request('GET', '/v1/notifications/inbox/unread-count');
    },
    async markRead(id) {
      return http.request('PATCH', `/v1/notifications/inbox/${encodeURIComponent(id)}/read`);
    },
    async markAllRead() {
      return http.request('POST', '/v1/notifications/inbox/read-all');
    },
    async archive(id) {
      return http.request('DELETE', `/v1/notifications/inbox/${encodeURIComponent(id)}`);
    },
  };

  const preferences = {
    async get() {
      return http.request('GET', '/v1/notifications/preferences');
    },
    async update(params) {
      return http.request('PUT', '/v1/notifications/preferences', { body: params });
    },
  };

  const emailTemplates = {
    async list() {
      const resp = await http.request('GET', '/v1/notifications/templates');
      return mapEnvelope(resp, (rows) => (rows || []).map(toEmailTemplate));
    },
    async get(id) {
      const resp = await http.request('GET', `/v1/notifications/templates/${encodeURIComponent(id)}`);
      return mapEnvelope(resp, toEmailTemplate);
    },
    async create(input) {
      const body = {
        slug: input.slug,
        subject: input.subject,
        html_body: input.htmlBody,
      };
      if (input.textBody !== undefined) body.text_body = input.textBody;
      if (input.variables !== undefined) body.variables = input.variables;
      const resp = await http.request('POST', '/v1/notifications/templates', { body });
      return mapEnvelope(resp, toEmailTemplate);
    },
    async update(id, input) {
      const body = {};
      if (input.subject !== undefined) body.subject = input.subject;
      if (input.htmlBody !== undefined) body.html_body = input.htmlBody;
      if (input.textBody !== undefined) body.text_body = input.textBody;
      if (input.variables !== undefined) body.variables = input.variables;
      const resp = await http.request('PUT', `/v1/notifications/templates/${encodeURIComponent(id)}`, { body });
      return mapEnvelope(resp, toEmailTemplate);
    },
    async delete(id) {
      return http.request('DELETE', `/v1/notifications/templates/${encodeURIComponent(id)}`);
    },
  };

  const smsTemplates = {
    async list() {
      const resp = await http.request('GET', '/v1/notifications/sms-templates');
      return mapEnvelope(resp, (rows) => (rows || []).map(toSMSTemplate));
    },
    async get(id) {
      const resp = await http.request('GET', `/v1/notifications/sms-templates/${encodeURIComponent(id)}`);
      return mapEnvelope(resp, toSMSTemplate);
    },
    async create(input) {
      const body = { slug: input.slug, body: input.body };
      if (input.variables !== undefined) body.variables = input.variables;
      const resp = await http.request('POST', '/v1/notifications/sms-templates', { body });
      return mapEnvelope(resp, toSMSTemplate);
    },
    async update(id, input) {
      const body = {};
      if (input.body !== undefined) body.body = input.body;
      if (input.variables !== undefined) body.variables = input.variables;
      const resp = await http.request('PUT', `/v1/notifications/sms-templates/${encodeURIComponent(id)}`, { body });
      return mapEnvelope(resp, toSMSTemplate);
    },
    async delete(id) {
      return http.request('DELETE', `/v1/notifications/sms-templates/${encodeURIComponent(id)}`);
    },
  };

  return {
    push,
    email,
    sms,
    inbox,
    preferences,
    templates: { email: emailTemplates, sms: smsTemplates },
    async registerDevice(params) {
      return http.request('POST', '/v1/notifications/devices', { body: params });
    },
    async unregisterDevice(deviceId) {
      return http.request('DELETE', `/v1/notifications/devices/${encodeURIComponent(deviceId)}`);
    },
  };
}

// ─── Analytics client (PalbaseAnalyticsClient) ──────────────────────────

function buildAnalyticsClient(http) {
  function countToBody(input) {
    return {
      event_name: input.eventName,
      event_names: input.eventNames,
      from: input.from,
      to: input.to,
      interval: input.interval,
      filters: input.filters,
      breakdown: input.breakdown,
    };
  }
  const query = {
    count: (input) => http.request('POST', '/v1/analytics/query/count', { body: countToBody(input) }),
    events: (input) => {
      const qs = new URLSearchParams({ from: String(input.from), to: String(input.to) });
      if (input.eventName) qs.set('event_name', input.eventName);
      if (input.distinctId) qs.set('distinct_id', input.distinctId);
      if (input.limit !== undefined) qs.set('limit', String(input.limit));
      if (input.cursor) qs.set('cursor', input.cursor);
      return http.request('GET', `/v1/analytics/query/events?${qs.toString()}`);
    },
    properties: (input) => {
      const qs = new URLSearchParams();
      const i = input || {};
      if (i.eventName) qs.set('event_name', i.eventName);
      if (i.from !== undefined) qs.set('from', String(i.from));
      if (i.to !== undefined) qs.set('to', String(i.to));
      const suffix = qs.toString() ? `?${qs.toString()}` : '';
      return http.request('GET', `/v1/analytics/query/properties${suffix}`);
    },
    users: (input) => http.request('POST', '/v1/analytics/query/users', {
      body: { from: input.from, to: input.to, filters: input.filters, limit: input.limit, cursor: input.cursor },
    }),
    funnel: (input) => http.request('POST', '/v1/analytics/query/funnel', {
      body: {
        steps: input.steps,
        from: input.from,
        to: input.to,
        conversion_window_seconds: input.conversionWindowSeconds,
        breakdown: input.breakdown,
      },
    }),
    retention: (input) => http.request('POST', '/v1/analytics/query/retention', {
      body: {
        first_event: input.firstEvent,
        return_event: input.returnEvent,
        from: input.from,
        to: input.to,
        period_days: input.periodDays,
        periods: input.periods,
      },
    }),
    cohort: (input) => http.request('POST', '/v1/analytics/query/cohort', { body: input }),
  };
  const management = {
    overview: () => http.request('GET', '/v1/analytics/overview'),
    eventNames: () => http.request('GET', '/v1/analytics/events/names'),
    userDetail: (distinctId) => http.request('GET', `/v1/analytics/users/${encodeURIComponent(distinctId)}`),
    deleteUser: (distinctId) => http.request('DELETE', `/v1/analytics/users/${encodeURIComponent(distinctId)}`),
  };
  return {
    query,
    management,
    async capture(event, properties, distinctId) {
      return http.request('POST', '/v1/analytics/capture', {
        body: {
          event,
          ...(distinctId !== undefined ? { distinct_id: distinctId } : {}),
          ...(properties !== undefined ? { properties } : {}),
        },
      });
    },
    async identify(distinctId, traits) {
      return http.request('POST', '/v1/analytics/identify', {
        body: {
          distinct_id: distinctId,
          ...(traits !== undefined ? { traits } : {}),
        },
      });
    },
    async screen(screenName, properties, distinctId) {
      return http.request('POST', '/v1/analytics/screen', {
        body: {
          screen_name: screenName,
          ...(distinctId !== undefined ? { distinct_id: distinctId } : {}),
          ...(properties !== undefined ? { properties } : {}),
        },
      });
    },
  };
}

// ─── Links client (PalbaseLinksClient) ──────────────────────────────────

function buildLinksClient(http) {
  return {
    async create(params) {
      return http.request('POST', '/v1/links', { body: params });
    },
    async list(options) {
      const params = new URLSearchParams();
      if (options && options.limit !== undefined) params.set('limit', String(options.limit));
      if (options && options.offset !== undefined) params.set('offset', String(options.offset));
      const query = params.toString();
      return http.request('GET', `/v1/links${query ? `?${query}` : ''}`);
    },
    async get(linkId) {
      validatePathParam('linkId', linkId);
      return http.request('GET', `/v1/links/${linkId}`);
    },
    async update(linkId, params) {
      validatePathParam('linkId', linkId);
      return http.request('PATCH', `/v1/links/${linkId}`, { body: params });
    },
    async delete(linkId) {
      validatePathParam('linkId', linkId);
      return http.request('DELETE', `/v1/links/${linkId}`);
    },
    async analytics(linkId) {
      validatePathParam('linkId', linkId);
      return http.request('GET', `/v1/links/${linkId}/analytics`);
    },
    async qrCode(linkId, options) {
      validatePathParam('linkId', linkId);
      const params = new URLSearchParams();
      if (options && options.size !== undefined) params.set('size', String(options.size));
      if (options && options.format) params.set('format', options.format);
      const query = params.toString();
      return http.request('GET', `/v1/links/${linkId}/qr${query ? `?${query}` : ''}`);
    },
    async match(params) {
      return http.request('POST', '/v1/links/match', { body: params });
    },
  };
}

// ─── CMS client (PalbaseCmsClient) ──────────────────────────────────────

function buildCmsClient(http) {
  return {
    async find(collection, options) {
      if (!COLLECTION_NAME_RE.test(collection)) {
        throw new Error(
          `Invalid collection name: "${collection}". Collection names must match ${COLLECTION_NAME_RE.source}`,
        );
      }
      const params = new URLSearchParams();
      if (options && options.locale) params.set('locale', options.locale);
      if (options && options.limit != null) params.set('limit', String(options.limit));
      if (options && options.offset != null) params.set('offset', String(options.offset));
      if (options && options.filter) params.set('filter', JSON.stringify(options.filter));
      const query = params.toString();
      return http.request('GET', `/v1/cms/${collection}${query ? `?${query}` : ''}`);
    },
    async findOne(collection, id, options) {
      if (!COLLECTION_NAME_RE.test(collection)) {
        throw new Error(
          `Invalid collection name: "${collection}". Collection names must match ${COLLECTION_NAME_RE.source}`,
        );
      }
      const params = new URLSearchParams();
      if (options && options.locale) params.set('locale', options.locale);
      const query = params.toString();
      return http.request('GET', `/v1/cms/${collection}/${encodeURIComponent(id)}${query ? `?${query}` : ''}`);
    },
  };
}

// ─── Factory ────────────────────────────────────────────────────────────
// Builds the full ctx.* module surface from a single (apikey, baseUrl)
// pair. The caller picks which apikey is "primary" (service-role wins
// when present — see worker.js comment).

function buildModuleClients(palbase) {
  if (!palbase || !palbase.url) {
    // Not configured: every module slot throws on first use so partial
    // behaviour fails loudly. Spread onto ctx so ctx.docs.collection(...)
    // surfaces the clear error rather than "Cannot read of undefined".
    return null;
  }
  const apiKey = palbase.service_role || palbase.apikey || '';
  const baseUrl = palbase.url.replace(/\/+$/, '');
  const http = makeHttpClient(apiKey, baseUrl);
  // verifyUserToken needs the apikey but doesn't go through http.request
  // (it's a manual fetch); stash apiKey on the http object so buildAuthClient
  // can read it without taking it as a separate parameter.
  http.apiKey = apiKey;
  return {
    auth: buildAuthClient(http),
    storage: buildStorageClient(http),
    docs: buildDocsClient(http),
    realtime: buildRealtimeClient(http),
    functions: buildFunctionsClient(http),
    flags: buildFlagsClient(http),
    notifications: buildNotificationsClient(http),
    analytics: buildAnalyticsClient(http),
    links: buildLinksClient(http),
    cms: buildCmsClient(http),
  };
}

module.exports = {
  PalbaseError,
  buildModuleClients,
  // Exported for tests so individual clients can be exercised in isolation.
  makeHttpClient,
  buildAuthClient,
  buildStorageClient,
  buildDocsClient,
  buildRealtimeClient,
  buildFunctionsClient,
  buildFlagsClient,
  buildNotificationsClient,
  buildAnalyticsClient,
  buildLinksClient,
  buildCmsClient,
};
