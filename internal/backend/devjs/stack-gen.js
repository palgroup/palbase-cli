#!/usr/bin/env node
/**
 * Palbase stack-type generator bridge.
 *
 * Generates the project's `palbase-stack.d.ts` from the NAMES its stack holds:
 * the secrets in its vault, the flags in its store, the buckets in its storage.
 * The Go CLI reads those three lists off the management API and hands them here
 * as JSON; this script require()s the project's @palbase/backend for
 * makeStackDts() and writes the returned text.
 *
 * SIMPLER THAN env-gen.js ON PURPOSE, and the difference is the whole point of
 * this change: env-gen has to esbuild-bundle `db/*.ts` because the schema
 * is a DECLARATION the author wrote. There is no file to bundle here. The stack
 * is the authority on which names exist, so this bridge only renders what it is
 * handed.
 *
 * Rendering is delegated to the SDK rather than done in Go for the reason
 * `makePurchasesDts` exists: one renderer, so the emitted bytes cannot drift
 * from the augmentation target they have to match.
 *
 * Usage:
 *   echo '{"names":{"secrets":[],"flags":[],"buckets":[]},"out_path":"/p/palbase-stack.d.ts"}' \
 *     | node stack-gen.js
 * Output (stdout, JSON): {} on success, { error } on failure.
 */
'use strict';

const fs = require('fs');

function writeResult(result) {
  // Same `process.exit` hazard env-gen.js documents: on a pipe the write is
  // async, and exiting before it drains truncates the JSON. Exit from the write
  // callback, not beside it.
  process.stdout.write(JSON.stringify(result), () => process.exit(0));
}

function writeError(error) {
  writeResult({ error: String(error) });
}

async function main() {
  const chunks = [];
  for await (const chunk of process.stdin) {
    chunks.push(chunk);
  }

  let req;
  try {
    req = JSON.parse(Buffer.concat(chunks).toString());
  } catch (e) {
    writeError('Invalid JSON input: ' + e.message);
    return;
  }

  const { names, out_path: outPath } = req;
  if (!names || typeof names !== 'object') {
    writeError('names is required');
    return;
  }
  if (!outPath) {
    writeError('out_path is required');
    return;
  }

  let makeStackDts;
  try {
    // The PROJECT's installed @palbase/backend (on NODE_PATH), not a CLI copy:
    // the generated file augments that package's interfaces, so it has to be
    // rendered by the same version.
    ({ makeStackDts } = require('@palbase/backend'));
  } catch (e) {
    writeError(
      '@palbase/backend not found — run `npm install` in the project so its stack names can be typed (' +
        (e && e.message ? e.message : e) +
        ')',
    );
    return;
  }
  if (typeof makeStackDts !== 'function') {
    writeError('@palbase/backend does not export makeStackDts (upgrade @palbase/backend)');
    return;
  }

  try {
    const dts = makeStackDts({
      secrets: names.secrets || [],
      flags: names.flags || [],
      buckets: names.buckets || [],
    });
    fs.writeFileSync(outPath, dts);
  } catch (err) {
    writeError('Failed to write palbase-stack.d.ts: ' + (err && err.message ? err.message : err));
    return;
  }

  writeResult({});
}

main().catch(writeError);
