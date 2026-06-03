# Palbase backend project

This project runs inside the Palbase managed backend runtime. You write
TypeScript files; Palbase discovers and serves them.

## How it works (read before generating code)

- This is **not** Express or a Supabase Edge Function. No `app.get(...)`, no
  `import express`, no manual route registration.
- **Endpoints** live in `endpoints/<path>/<method>.ts` (file-based routing:
  `endpoints/hello/get.ts` → `GET /hello`, `endpoints/posts/[id]/get.ts` →
  `GET /posts/:id`). The handler receives one arg, `req`. Import services as
  singletons: `import { defineEndpoint, z, Database } from "@palbase/backend"`.
- **Workers, jobs, hooks, webhooks** use a `ctx` object instead (`ctx.db`,
  `ctx.log`, `ctx.cache`, `ctx.queue`).

## Commands

- `palbase serve` — run locally with hot reload.
- Deploy is GitHub-native: commit and `git push` to the project's GitHub repo.
  A webhook tells Palbase to build + deploy the pushed code (and apply the
  config-as-code committed in the repo). Push to a branch to deploy that branch.

## Full SDK documentation

See the `@palbase/backend` docs for the complete reference (endpoints, schema,
services, errors, workers, jobs, hooks, webhooks):
<https://palbase.studio/docs/backend>
