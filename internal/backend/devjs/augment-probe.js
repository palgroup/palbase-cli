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
 * So this compiles a PROBE with the project's own tsconfig and the project's own
 * installed TypeScript and @palbase/backend: it imports a type that only the
 * REAL package module exports (a shadowing script loses it) and, when the stack
 * holds names, spells one of them (a mis-targeted augmentation cannot supply it).
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
    ts = require('typescript');
  } catch (e) {
    writeResult({
      error:
        'typescript is not installed in this project, so the generated types cannot be checked — ' +
        'run `npm install` (the scaffold declares it as a devDependency)',
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

  // A name unlikely to collide with anything the project owns; it never exists on disk.
  const probePath = path.join(projectDir, '__palbase_augmentation_probe__.ts');
  // ONLY WHAT EVERY SUPPORTED MAJOR EXPORTS, unconditionally. `PalbaseRoleName`
  // arrived with @palbase/backend 40: importing it always refused the build of
  // every project still on 39 — measured, `palbase init` installs the published
  // SDK and the scaffold's own build came back TS2724. A project on 39 cannot
  // render role names anyway (its makeEnvDts ignores them and env-gen.js refuses
  // that render before anything lands), so the role type is imported only when
  // this run has a role to spell.
  const lines = [
    'import type { PalbaseSecretName } from "@palbase/backend/stack";',
    'import type { Tables } from "@palbase/backend/env";',
    'export type __PalbaseProbe = [PalbaseSecretName, Tables];',
  ];
  if (req.secret) lines.push('export const __secret: PalbaseSecretName = ' + JSON.stringify(req.secret) + ';');
  if (req.role) {
    lines.push('import type { PalbaseRoleName } from "@palbase/backend/stack";');
    lines.push('export const __role: PalbaseRoleName = ' + JSON.stringify(req.role) + ';');
  }
  const probeText = lines.join('\n') + '\n';

  const options = { ...parsed.options, noEmit: true };
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
  writeResult(diagnostics.length ? { diagnostics } : {});
}

main().catch((e) => writeResult({ error: String(e && e.message ? e.message : e) }));
