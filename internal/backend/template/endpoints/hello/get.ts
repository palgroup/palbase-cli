import { defineEndpoint, z } from "@palbase/backend";

// File-based routing: this file's path → URL.
//   endpoints/hello/get.ts          → GET  /hello
//   endpoints/posts/[id]/get.ts     → GET  /posts/:id
//   endpoints/items/post.ts         → POST /items
//
// `defineEndpoint` is fully type-safe: the Zod input schema flows into
// `req.input`, the output schema validates the return value. Services are
// imported as singletons, e.g. `import { Database } from "@palbase/backend"`.

export default defineEndpoint({
  method: "GET",
  input: z.object({
    name: z.string().optional(),
  }),
  output: z.object({
    message: z.string(),
    user: z.string().nullable(),
  }),
  handler: async (req) => {
    return {
      message: `hello, ${req.input.name ?? "world"}!`,
      user: req.user?.id ?? null,
    };
  },
});
