#!/usr/bin/env node
/**
 * Palbase augmentation probe — does the generated file AUGMENT the package, or
 * merely exist beside it? (FR-006)
 *
 * `palbase/palbase-env.d.ts` is worth nothing unless TypeScript applies it. Two
 * ways it silently does not: the trailing `export {};` is lost, and the file
 * becomes an ambient script whose `declare module "@palbase/backend/stack"`
 * REPLACES the package's module instead of adding to it (FR-005); or the module
 * specifier drifts, and TypeScript augments a module that does not exist. In
 * both cases the file is on disk and the build is green.
 *
 * So this reads the file with the compiler's parser — is it a module, and does
 * every module it augments resolve? — and then compiles a PROBE with the
 * project's own tsconfig and installed @palbase/backend: for each module the file
 * augments it imports a type only the REAL module exports (a shadowing script
 * loses it) and, when the stack holds names, spells one of them (a mis-targeted
 * augmentation cannot supply it). Only the blocks the file declares are asked
 * about, so an SDK older than a module is not refused for lacking it.
 *
 * THE PROBE IS NEVER WRITTEN. It is served to the compiler from memory under a
 * path inside the project directory, so `@palbase/backend` resolves from the
 * project's node_modules exactly as the project's own code does — no file in the
 * checkout, no scratch tree, and no symbolic link (which Windows refuses to an
 * ordinary account).
 *
 * Input (stdin, JSON): { "project_dir": "...", "env_file": ".../palbase/palbase-env.d.ts",
 *                        "secret": "STRIPE_KEY" | "", "role": "admin" | "" }
 * Output (stdout, JSON): {} when the probe compiled, { "diagnostics": [ ... ] } when it
 * did not, { "error": "..." } when the probe itself could not run.
 */
'use strict';

const path = require('path');

function writeResult(result) {
  // Exit in the write's callback: a pipe drops queued output on process.exit
  // (the same measured cut env-gen.js documents).
  process.stdout.write(JSON.stringify(result), () => process.exit(0));
}

async function main() {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  let req;
  try {
    req = JSON.parse(Buffer.concat(chunks).toString());
  } catch (e) {
    writeResult({ error: 'Invalid JSON input: ' + e.message });
    return;
  }
  const projectDir = req.project_dir;
  const envFile = req.env_file;
  if (!projectDir || !envFile) {
    writeResult({ error: 'project_dir and env_file are required' });
    return;
  }

  let ts;
  try {
    // NODE_PATH decides which: the CLI's pinned parser TypeScript first, the
    // project's own second — the order build-check.js loads it in, because a
    // project's `typescript` may be absent or a 7.x with no compiler API.
    ts = require('typescript');
  } catch (e) {
    ts = undefined;
  }
  // A PACKAGE NAMED typescript IS NOT A COMPILER (review-T012 I1). TypeScript 7's
  // CJS entry exports `{ version, versionMajorMinor }` and nothing else, so a
  // successful require is not the question — the compiler API is.
  if (!ts || typeof ts.createProgram !== 'function' || !ts.sys) {
    writeResult({
      error:
        'typescript is not installed where the probe can load it (neither the CLI\'s parser nor this project has one ' +
        'with a compiler API), so the generated types cannot be checked — run `npm install` (the scaffold declares it as a devDependency)',
    });
    return;
  }

  const configPath = ts.findConfigFile(projectDir, ts.sys.fileExists, 'tsconfig.json');
  if (!configPath) {
    writeResult({ error: 'no tsconfig.json in ' + projectDir + ' — the probe compiles with the project\'s own settings' });
    return;
  }
  const parsed = ts.getParsedCommandLineOfConfigFile(configPath, {}, {
    ...ts.sys,
    onUnRecoverableConfigFileDiagnostic: (d) => {
      throw new Error(ts.flattenDiagnosticMessageText(d.messageText, ' '));
    },
  });

  const options = { ...parsed.options, noEmit: true };
  const rel = path.relative(projectDir, envFile);

  // THE BLOCKS THE FILE DECLARES, read with the compiler's own parser — and
  // measured against THOSE, never against what the newest SDK would render. A
  // probe that imported a stack type unconditionally refused every project on an
  // SDK from before `@palbase/backend/stack` existed (22.1.0): measured, a
  // correct env-only file on 12.0.1 came back TS2307 (D-23).
  const envText = ts.sys.readFile(envFile);
  if (envText === undefined) {
    writeResult({ error: 'cannot read ' + envFile });
    return;
  }
  const envSource = ts.createSourceFile(envFile, envText, ts.ScriptTarget.Latest, true);
  const augmented = envSource.statements
    .filter((s) => ts.isModuleDeclaration(s) && ts.isStringLiteral(s.name))
    .map((s) => s.name.text);

  const findings = [];
  // A SCRIPT, NOT A MODULE: every `declare module` in it is an ambient
  // declaration that REPLACES the package's module (FR-005). Asked of the parser
  // directly, so it is caught whichever SDK is installed.
  if (!ts.isExternalModule(envSource)) {
    findings.push(
      rel + ': is not a module — without its trailing `export {};` every `declare module` in it replaces ' +
        "the @palbase/backend module it names instead of augmenting it",
    );
  }
  // A DRIFTED SPECIFIER augments a module that does not exist, silently — and
  // with no stack name to spell, nothing else here would notice.
  //
  // IN THE FILE'S OWN MODULE FORMAT (review-T012 C1). Asked with no mode,
  // node16/nodenext fall back to the "require" condition, and a package whose
  // types sit only under "import" read as unresolvable while `tsc -p` compiled
  // the project clean. The implied format of the generated file is the mode the
  // compiler resolves its augmentations in.
  const mode = ts.getImpliedNodeFormatForFile(envFile, undefined, ts.sys, options);
  for (const spec of augmented) {
    if (!ts.resolveModuleName(spec, envFile, options, ts.sys, undefined, undefined, mode).resolvedModule) {
      findings.push(rel + ': augments ' + JSON.stringify(spec) + ', which TypeScript cannot resolve from this project — the block types nothing');
    }
  }

  // A name unlikely to collide with anything the project owns; it never exists on disk.
  const probePath = path.join(projectDir, '__palbase_augmentation_probe__.ts');
  // A TYPE ONLY THE REAL MODULE EXPORTS, for each module the file augments: a
  // shadowing script loses it. The stack's type is also imported whenever this
  // run spells a name — names only come from an SDK that renders the stack
  // block. `PalbaseRoleName` arrived with @palbase/backend 40, so it is imported
  // only when there is a role to spell (a project on 39 renders none: its
  // makeEnvDts ignores names and env-gen.js refuses that render first).
  const lines = [];
  const probed = [];
  if (augmented.includes('@palbase/backend/env')) {
    lines.push('import type { Tables } from "@palbase/backend/env";');
    probed.push('Tables');
  }
  if (augmented.includes('@palbase/backend/stack') || req.secret || req.role) {
    lines.push('import type { PalbaseSecretName } from "@palbase/backend/stack";');
    probed.push('PalbaseSecretName');
  }
  lines.push(probed.length ? 'export type __PalbaseProbe = [' + probed.join(', ') + '];' : 'export {};');
  if (req.secret) lines.push('export const __secret: PalbaseSecretName = ' + JSON.stringify(req.secret) + ';');
  if (req.role) {
    lines.push('import type { PalbaseRoleName } from "@palbase/backend/stack";');
    lines.push('export const __role: PalbaseRoleName = ' + JSON.stringify(req.role) + ';');
  }
  const probeText = lines.join('\n') + '\n';

  const host = ts.createCompilerHost(options);
  const readFile = host.readFile.bind(host);
  const fileExists = host.fileExists.bind(host);
  const getSourceFile = host.getSourceFile.bind(host);
  const same = (p) => path.resolve(p) === probePath;
  host.readFile = (p) => (same(p) ? probeText : readFile(p));
  host.fileExists = (p) => same(p) || fileExists(p);
  host.getSourceFile = (p, lang, onError, fresh) =>
    same(p) ? ts.createSourceFile(p, probeText, lang, true) : getSourceFile(p, lang, onError, fresh);

  const program = ts.createProgram([probePath, envFile], options, host);
  const diagnostics = [probePath, envFile]
    .flatMap((f) => {
      const sf = program.getSourceFile(f);
      return sf ? [...program.getSyntacticDiagnostics(sf), ...program.getSemanticDiagnostics(sf)] : [];
    })
    .map((d) => {
      const where = d.file ? path.relative(projectDir, d.file.fileName) : '';
      return `${where}: TS${d.code}: ${ts.flattenDiagnosticMessageText(d.messageText, ' ')}`;
    });
  const all = [...findings, ...diagnostics];
  writeResult(all.length ? { diagnostics: all } : {});
}

main().catch((e) => writeResult({ error: String(e && e.message ? e.message : e) }));
