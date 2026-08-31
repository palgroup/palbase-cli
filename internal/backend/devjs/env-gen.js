#!/usr/bin/env node
/**
 * Palbase env-type generator bridge.
 *
 * Generates the project's `palbase-env.d.ts` from its `db/*.ts` — one file per
 * schema. This is the local twin of the backend-runtime's schema extraction
 * (modules/backend internal/runtime/schema_extract.js): the Go CLI writes an
 * entry importing every declaration, esbuild-bundles it to a temp CJS file
 * (with @palbase/* kept external so it resolves to the project's installed
 * package on NODE_PATH), then runs this script over that bundle.
 *
 * This script require()s the bundle (which exports `modules` — one namespace
 * per schema file — and the matching `names`), require()s the project's
 * @palbase/backend for makeEnvDts(), and writes the returned
 * `palbase-env.d.ts` text to the project root. That file augments the
 * @palbase/backend/env `Tables` interface so handlers get a typed
 * `Database.tables.*` with no import and no generic.
 *
 * EVERY schema in ONE call. makeEnvDts names relations across schemas, so it
 * needs both ends of a foreign key in the same invocation; calling it per file
 * would emit a relation pointing at a table it thinks is absent.
 *
 * Usage:
 *   echo '{"bundle_path":"/tmp/schema.js","out_path":"/proj/palbase-env.d.ts"}' \
 *     | node env-gen.js
 * Output (stdout, JSON): {} on success, { error } on failure.
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

  let bundle;
  try {
    bundle = require(bundlePath);
  } catch (err) {
    writeError('Failed to evaluate schema: ' + (err && err.message ? err.message : err));
    return;
  }

  const modules = bundle && Array.isArray(bundle.modules) ? bundle.modules : null;
  const names = bundle && Array.isArray(bundle.names) ? bundle.names : [];
  if (!modules) {
    writeError('the bundled schema entry exported no `modules` array');
    return;
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

  const schemas = [];
  for (let i = 0; i < modules.length; i += 1) {
    const name = names[i] !== undefined ? names[i] : String(i);
    const def = pickSchema(modules[i]);
    if (!def) {
      // Named, not counted: "one file did not export a schema" sends somebody
      // to read all of them.
      writeError(
        'db/' +
          name +
          '.ts does not export a defineSchema(...) result — write `export default defineSchema("' +
          name +
          '", { tables: [ … ] })`',
      );
      return;
    }
    schemas.push(def);
  }

  try {
    const dts = makeEnvDts(schemas);
    fs.writeFileSync(outPath, dts);
  } catch (err) {
    writeError('Failed to write palbase-env.d.ts: ' + (err && err.message ? err.message : err));
    return;
  }

  writeResult({});
}

main().catch(writeError);
