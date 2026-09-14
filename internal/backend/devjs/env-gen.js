#!/usr/bin/env node
/**
 * Palbase type generator bridge — the ONE file a checkout gets.
 *
 * Generates the project's `palbase/palbase-env.d.ts` from two sources: its
 * `db/*.ts` (the schema the author declared) and the NAMES its stack holds
 * (secrets, flags, buckets, roles), which the Go CLI reads off the stack and
 * hands over here. The rendered file carries both — one
 * `declare module "@palbase/backend/env"` block and one
 * `declare module "@palbase/backend/stack"` block — so a checkout has ONE
 * generated file rather than two.
 *
 * The schema half is the local twin of the backend-runtime's schema extraction
 * (modules/backend internal/runtime/schema_extract.js): the Go CLI writes an
 * entry importing every declaration, esbuild-bundles it to a temp CJS file
 * (with @palbase/* kept external so it resolves to the project's installed
 * package on NODE_PATH), then runs this script over that bundle.
 *
 * This script require()s the bundle (which exports `modules` — one namespace
 * per schema file — and the matching `names`), require()s the project's
 * @palbase/backend for makeEnvDts(), and writes what it returns.
 *
 * EVERY schema in ONE call. makeEnvDts names relations across schemas, so it
 * needs both ends of a foreign key in the same invocation; calling it per file
 * would emit a relation pointing at a table it thinks is absent.
 *
 * `bundle_path` is OPTIONAL, and its absence is not a mistake: a project that
 * declares no database still gets the file, with an empty `Tables` block and
 * the stack names it does have. `names` is optional too — without it the stack
 * block renders EMPTY, and the Go side keeps whatever the file on disk already
 * had rather than letting an unreachable stack narrow a project's types.
 *
 * Usage:
 *   echo '{"bundle_path":"/tmp/schema.js","out_path":"/proj/palbase/palbase-env.d.ts",
 *          "names":{"secrets":[],"flags":[],"buckets":[],"roles":[]}}' | node env-gen.js
 * Output (stdout, JSON): {} when the file was written, { "unchanged": true }
 * when the bytes on disk were already the bytes to write, { error } on failure.
 */
'use strict';

const fs = require('fs');

function writeResult(result) {
  // ‼️ `process.exit` KUYRUKTAKİ YAZIMI DÜŞÜRÜR. stdout bir BORU olduğunda yazım
  // asenkrondur: veri önce Node'un kendi kuyruğuna girer, oradan işletim
  // sistemine geçer. `exit` o anda çağrılırsa kuyrukta ne kaldıysa gider.
  //
  // Ölçüldü 26.08.2026, gerçek çağrı şekliyle (`execFileSync`, stdio pipe):
  // 128 KiB yazan bir çocuktan TAM 65 536 bayt okunuyor — hem node 26.7 hem
  // bun 1.3.9 ile. Yazımın callback'inde çıkınca 131 072'nin tamamı geliyor.
  // Kesilen JSON `JSON.parse` ile "extractor produced no JSON" oluyor, yani
  // BÜYÜK bir controller'ın metadata'sı eşiği aştığı an build sebepsiz
  // reddediliyor — ve sebep, controller'ın BOYUTU, içeriği değil.
  //
  // `write('', cb)` yeterli DEĞİL: bun'da o callback önceki yazımın arkasında
  // sıraya girmiyor ve kesilme devam ediyor (ölçüldü). Callback GERÇEK yazıma
  // bağlanmalı.
  process.stdout.write(JSON.stringify(result), () => process.exit(0));
}

function writeError(error) {
  writeResult({ error: String(error) });
}

function messageOf(err) {
  return err && err.message ? err.message : String(err);
}

/**
 * Write the rendered text — unless it is already the text on disk. Answers
 * whether the file was left as it was.
 *
 * FR-001a: rewriting a file whose bytes did not change refreshes its mtime, and
 * that mtime is what every watcher, `tsc --watch` and build cache keys on. A
 * generator that rewrites an identical file wakes all of them for nothing, on
 * every build.
 *
 * Only ENOENT is "there is no file yet": a directory in the way, or a
 * permission error, is a real fault and reaches the caller rather than being
 * read as "write it then".
 */
function landFile(outPath, text) {
  const next = Buffer.from(text, 'utf8');
  let prev = null;
  try {
    prev = fs.readFileSync(outPath);
  } catch (err) {
    if (!err || err.code !== 'ENOENT') throw err;
  }
  if (prev !== null && prev.equals(next)) return true;
  fs.writeFileSync(outPath, next);
  return false;
}

// Accept the declaration however the author exported it: the default export
// (the documented `export default defineSchema(...)`), the module object
// itself, or any named export that is itself a defineSchema() result (an
// object carrying `.tables`). Mirrors the runtime's schema_extract.js
// tolerance, applied once per file.
const pickSchema = (mod) => {
  let def = mod && mod.default ? mod.default : mod;
  if (!def || typeof def !== 'object' || !def.tables) {
    def =
      mod && typeof mod === 'object'
        ? Object.values(mod).find(
            (v) => v && typeof v === 'object' && v.tables && typeof v.tables === 'object',
          )
        : undefined;
  }
  return def && typeof def === 'object' && def.tables ? def : undefined;
};

/** The declarations in an evaluated bundle, in the order the entry listed
 * them. Throws naming the FILE when one of them exported no schema. */
function schemasOf(bundle) {
  const modules = bundle && Array.isArray(bundle.modules) ? bundle.modules : null;
  const names = bundle && Array.isArray(bundle.names) ? bundle.names : [];
  if (!modules) {
    throw new Error('the bundled schema entry exported no `modules` array');
  }

  const schemas = [];
  for (let i = 0; i < modules.length; i += 1) {
    const name = names[i] !== undefined ? names[i] : String(i);
    const def = pickSchema(modules[i]);
    if (!def) {
      // Named, not counted: "one file did not export a schema" sends somebody
      // to read all of them.
      throw new Error(
        'db/' +
          name +
          '.ts does not export a defineSchema(...) result — write `export default defineSchema("' +
          name +
          '", { tables: [ … ] })`',
      );
    }
    schemas.push(def);
  }
  return schemas;
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

  const { bundle_path: bundlePath, out_path: outPath, names } = req;
  if (!outPath) {
    writeError('out_path is required');
    return;
  }
  if (names !== undefined && (names === null || typeof names !== 'object' || Array.isArray(names))) {
    writeError('names must be an object carrying secrets, flags, buckets and roles');
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
        messageOf(e) +
        ')',
    );
    return;
  }
  if (typeof makeEnvDts !== 'function') {
    writeError('@palbase/backend does not export makeEnvDts (upgrade @palbase/backend)');
    return;
  }

  // NO BUNDLE IS A PROJECT WITH NO DATABASE, not a missing argument: the file is
  // still written, with an empty `Tables` block and whatever names the stack
  // holds. A checkout gets one generated file whether or not it declares a
  // schema.
  let schemas = [];
  if (bundlePath) {
    let bundle;
    try {
      bundle = require(bundlePath);
    } catch (err) {
      writeError('Failed to evaluate schema: ' + messageOf(err));
      return;
    }
    try {
      schemas = schemasOf(bundle);
    } catch (err) {
      writeError(messageOf(err));
      return;
    }
  }

  let dts;
  try {
    dts =
      names === undefined
        ? makeEnvDts(schemas)
        : makeEnvDts(schemas, {
            secrets: names.secrets || [],
            flags: names.flags || [],
            buckets: names.buckets || [],
            roles: names.roles || [],
          });
  } catch (err) {
    writeError('Failed to render palbase-env.d.ts: ' + messageOf(err));
    return;
  }

  // AN @palbase/backend OLDER THAN THE SINGLE FILE takes one argument: it
  // ignores the names and renders the env block alone. That render is written —
  // here, into the tree the caller named, which for `palbase build` is the
  // staging copy — and WHAT IT MEANS FOR THE CHECKOUT IS THE CALLER'S CALL:
  // landEnvTypes keeps whatever stack block the checkout's file already carries
  // and says so (build.go, TestPreserveStackBlockRefusesAnUnmarkedRenderEvenWithNames).
  //
  // THIS SCRIPT USED TO REFUSE HERE, and that refusal failed the whole build for
  // every linked project on an older SDK — reported as `✗ DEPLOY WOULD FAIL:
  // db/`, blaming the schema for a version skew (final-cli C1, D-36). A build
  // that works offline (NFR-004) and never narrows types (FR-003a) does not get
  // to fail over which major is installed: the file keeps its names, the build
  // passes, and the line the lander prints names the cure.
  let unchanged;
  try {
    unchanged = landFile(outPath, dts);
  } catch (err) {
    writeError('Failed to write palbase-env.d.ts: ' + messageOf(err));
    return;
  }

  writeResult(unchanged ? { unchanged: true } : {});
}

main().catch(writeError);
