#!/usr/bin/env node
/**
 * RETIRED — there is no separate stack type file any more.
 *
 * The names a stack holds (secrets, flags, buckets, roles) are rendered into
 * `palbase/palbase-env.d.ts`, in the same call that renders the schema: ONE
 * generated file in a checkout, and one bridge that writes it — `env-gen.js`,
 * which takes those names as its `names` field. Two writers meant two files to
 * keep in step, two chances to leave one of them stale, and a second path
 * writing into the customer's tree.
 *
 * IT ANSWERS RATHER THAN DISAPPEARING. A caller that still asks for the old
 * file is told where the names went and nothing is written: a retired bridge
 * that returned success would leave its caller believing a file exists, and a
 * missing script would answer with a spawn error that names nothing.
 *
 * Usage (any request): the reason, on stdout, as JSON.
 * Output (stdout, JSON): { error }.
 */
'use strict';

const RETIRED =
  "stack-gen.js is retired: a stack's names (secrets, flags, buckets, roles) are rendered into " +
  'palbase/palbase-env.d.ts by env-gen.js — pass them as its `names` field. Nothing was written.';

function writeResult(result) {
  // Same `process.exit` hazard env-gen.js documents: on a pipe the write is
  // async, and exiting before it drains truncates the JSON. Exit from the write
  // callback, not beside it.
  process.stdout.write(JSON.stringify(result), () => process.exit(0));
}

async function main() {
  // The request is drained before the refusal is written: a caller still
  // sending its JSON must read the reason, not an EPIPE from a bridge that had
  // already gone.
  for await (const chunk of process.stdin) {
    void chunk;
  }
  writeResult({ error: RETIRED });
}

main().catch(() => writeResult({ error: RETIRED }));
