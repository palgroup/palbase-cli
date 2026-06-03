#!/usr/bin/env node
/**
 * Palbase env-type generator bridge.
 *
 * Generates the project's `palbase-env.d.ts` from its `db/schema.ts`. This is
 * the local twin of the backend-runtime's schema extraction (modules/backend
 * internal/runtime/schema_extract.js): the Go CLI esbuild-bundles the project's
 * `db/schema.ts` to a temp CJS file (with @palbase/* kept external so it
 * resolves to the project's installed package on NODE_PATH), then runs this
 * script over that bundle.
 *
 * This script require()s the bundle (which default-exports a defineSchema()
 * result), require()s the project's @palbase/backend for makeEnvDts(), and
 * writes the returned `palbase-env.d.ts` text to the project root. That file
 * augments the @palbase/backend/env `Tables` interface so handlers get a typed
 * `Database.tables.*` with no import and no generic.
 *
 * Usage:
 *   echo '{"bundle_path":"/tmp/schema.js","out_path":"/proj/palbase-env.d.ts"}' \
 *     | node env-gen.js
 * Output (stdout, JSON): {} on success, { error } on failure.
 */
'use strict';

const fs = require('fs');

function writeResult(result) {
  process.stdout.write(JSON.stringify(result));
  process.exit(0);
}

function writeError(error) {
  writeResult({ error: String(error) });
}

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

  const { bundle_path: bundlePath, out_path: outPath } = req;
  if (!bundlePath) {
    writeError('bundle_path is required');
    return;
  }
  if (!outPath) {
    writeError('out_path is required');
    return;
  }

  let makeEnvDts;
  try {
    // Resolve from the PROJECT's installed @palbase/backend (on NODE_PATH set
    // by the Go caller), not a CLI-bundled copy — the generated types must
    // match the SDK version the project actually depends on.
    ({ makeEnvDts } = require('@palbase/backend'));
  } catch (e) {
    writeError(
      '@palbase/backend not found — run `npm install` in the project so its db schema can be typed (' +
        (e && e.message ? e.message : e) +
        ')',
    );
    return;
  }
  if (typeof makeEnvDts !== 'function') {
    writeError('@palbase/backend does not export makeEnvDts (upgrade @palbase/backend)');
    return;
  }

  let schemaDef;
  try {
    const mod = require(bundlePath);
    // Accept the schema however the author exported it: the default export (the
    // documented `export default defineSchema(...)`), the module object itself
    // (`module.exports = defineSchema(...)`), or any named export that is itself
    // a defineSchema() result (an object carrying `.tables`). Mirrors the
    // runtime's schema_extract.js tolerance.
    schemaDef = mod && mod.default ? mod.default : mod;
    if (!schemaDef || typeof schemaDef !== 'object' || !schemaDef.tables) {
      const named =
        mod && typeof mod === 'object'
          ? Object.values(mod).find(
              (v) => v && typeof v === 'object' && v.tables && typeof v.tables === 'object',
            )
          : undefined;
      if (named) schemaDef = named;
    }
  } catch (err) {
    writeError('Failed to evaluate schema: ' + (err && err.message ? err.message : err));
    return;
  }

  if (!schemaDef || typeof schemaDef !== 'object' || !schemaDef.tables) {
    writeError('schema module does not export a defineSchema() result with .tables');
    return;
  }

  try {
    const dts = makeEnvDts(schemaDef);
    fs.writeFileSync(outPath, dts);
  } catch (err) {
    writeError('Failed to write palbase-env.d.ts: ' + (err && err.message ? err.message : err));
    return;
  }

  writeResult({});
}

main().catch(writeError);
