// Package scaffold provides `palbase init` — it writes a minimal, working
// Palbase backend codebase skeleton (class-controller model, @palbase/backend
// ^9.0.0) into a directory: package.json, tsconfig.json, db/schema.ts,
// controllers/hello.controller.ts, and .gitignore. Purely local file
// authoring — no Studio / network, mirroring internal/storage's local-only
// command style. The generated contents are verified against the live
// todoapp reference project and the @palbase/backend SDK exports.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/palgroup/palbase-cli/internal/hook"
	"github.com/spf13/cobra"
)

// invalidNameChars collapses every run of characters not allowed in an npm
// package name into a single dash (after lowercasing), so "My App" → "my-app".
var invalidNameChars = regexp.MustCompile(`[^a-z0-9._-]+`)

// sanitizeName derives an npm-safe package name from a directory base name.
func sanitizeName(base string) string {
	n := strings.ToLower(base)
	n = invalidNameChars.ReplaceAllString(n, "-")
	n = strings.Trim(n, "-._")
	if n == "" {
		return "palbase-backend"
	}
	return n
}

// packageJSON matches todoapp's minimum field set; the dependency major must
// track the backend-runtime's @palbase/backend major (9.x today).
const packageJSON = `{
  "name": "%s",
  "version": "0.1.0",
  "private": true,
  "scripts": {
    "dev": "palbase serve",
    "deploy": "palbase push",
    "typecheck": "tsc --noEmit",
    "prepare": "git config core.hooksPath hooks 2>/dev/null && chmod +x hooks/* 2>/dev/null || true"
  },
  "dependencies": {
    "@palbase/backend": "^9.0.0"
  },
  "devDependencies": {
    "@types/node": "^22.19.19",
    "typescript": "^5.9.3"
  }
}
`

// tsconfigJSON mirrors the todoapp reference project. experimentalDecorators
// is REQUIRED for the @Controller/@Get class-decorator model.
const tsconfigJSON = `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "lib": ["ES2022"],
    "strict": true,
    "experimentalDecorators": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "types": ["node"]
  },
  "include": [
    "controllers/**/*.ts",
    "models/**/*.ts",
    "services/**/*.ts",
    "resources/**/*.ts",
    "seeds/**/*.ts",
    "db/**/*.ts",
    "config/**/*.ts",
    "*.d.ts"
  ]
}
`

const schemaTS = `import { defineSchema, uuid, text, boolean, timestamp } from "@palbase/backend";

// Single source of truth for your project's Postgres schema (config-as-code).
// The deploy diffs this file against the live database and auto-applies
// additive changes (new tables, new nullable columns) as migrations.
export default defineSchema({
  tables: {
    todos: {
      columns: {
        id: uuid().primaryKey().defaultRandom(),
        title: text().notNull(),
        completed: boolean().default(false),
        created_at: timestamp().defaultNow(),
      },
    },
  },
});
`

// helloModelTS names the response schema in a model file: the runtime's
// metadata extraction REQUIRES a NAMED zod schema as the return type (inline
// object types are rejected with "return type must be a NAMED zod schema" and
// the endpoint silently registers 0 routes).
const helloModelTS = `import { z } from "@palbase/backend";

// GET /hello response → operationId getHello → pb.getHello().
export const HelloSchema = z.object({
  message: z.string(),
});
export type HelloSchema = z.infer<typeof HelloSchema>;
`

const helloControllerTS = `import { Controller, Get } from "@palbase/backend";
import { HelloSchema } from "../models/hello/shared";

// Controllers live in controllers/*.controller.ts and MUST be the file's
// default export. Endpoints are secure by default (a signed-in user is
// required); { auth: false } opts this one out so a plain anon-key request
// works right after deploy. The response schema comes from the method's
// return type — it must be a NAMED zod schema (see models/hello/shared.ts).
@Controller("/hello", { auth: false })
export default class HelloController {
  @Get("")
  hello(): HelloSchema {
    return { message: "Hello from Palbase!" };
  }
}
`

const gitignore = `node_modules/
.palbase/config.json
.env.local
# Deno/edge-runtime residue — the isolate host, not the tenant, owns these.
deno.lock
deno.json
`

// Cmd returns the `palbase init` command. It takes no resolvers: scaffolding
// is purely local file I/O (no Studio / network).
func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Scaffold a new Palbase backend codebase",
		Long: `Scaffold a new Palbase backend codebase in [dir] (default: current directory).

Writes a minimal working skeleton:

  package.json                      @palbase/backend dependency + scripts
  tsconfig.json                     experimentalDecorators enabled
  db/schema.ts                      config-as-code Postgres schema
  models/hello/shared.ts            named zod response schema
  controllers/hello.controller.ts   example class controller
  hooks/pre-push                    deploy-validation gate (wired on npm install)
  .gitignore

Refuses to run in a directory that already contains a package.json.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "."
			if len(args) == 1 {
				target = args[0]
			}
			dir, err := filepath.Abs(target)
			if err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
				return fmt.Errorf("%s is already a project (package.json exists) — refusing to scaffold", dir)
			}

			name := sanitizeName(filepath.Base(dir))
			files := []struct {
				path    string
				content string
				mode    os.FileMode
			}{
				{"package.json", fmt.Sprintf(packageJSON, name), 0o644},
				{"tsconfig.json", tsconfigJSON, 0o644},
				{filepath.Join("db", "schema.ts"), schemaTS, 0o644},
				{filepath.Join("models", "hello", "shared.ts"), helloModelTS, 0o644},
				{filepath.Join("controllers", "hello.controller.ts"), helloControllerTS, 0o644},
				{".gitignore", gitignore, 0o644},
				// The pre-push deploy-validation hook, wired by package.json's
				// "prepare" (core.hooksPath=hooks) on `npm install`. Executable.
				{filepath.Join("hooks", "pre-push"), hook.Body, 0o755},
			}

			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "✓ scaffolding Palbase backend codebase %q in %s\n", name, dir); err != nil {
				return err
			}
			for _, f := range files {
				abs := filepath.Join(dir, f.path)
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(abs, []byte(f.content), f.mode); err != nil {
					return err
				}
				fmt.Fprintf(out, "  created %s\n", f.path)
			}

			fmt.Fprintln(out, "\nnext steps:")
			if target != "." {
				fmt.Fprintf(out, "  cd %s\n", target)
			}
			fmt.Fprintln(out, "  npm install")
			fmt.Fprintln(out, "  palbase serve                                # run locally")
			fmt.Fprintln(out, "  palbase project create <ref> --name \"...\"    # create the project")
			fmt.Fprintln(out, "  palbase push                                 # deploy")
			return nil
		},
	}
	return cmd
}
