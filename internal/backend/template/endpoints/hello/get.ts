import { defineEndpoint, z } from "@palbase/backend";

// File-based routing: this file's path → URL.
//   endpoints/hello/get.ts          → GET  /hello
//   endpoints/posts/[id]/get.ts     → GET  /posts/:id
//   endpoints/items/post.ts         → POST /items
//
// `defineEndpoint` is fully type-safe: the Zod input schema flows into
// `ctx.input`, the output schema validates the return value.

export default defineEndpoint({
  method: "GET",
  input: z.object({
    name: z.string().optional(),
  }),
  output: z.object({
    message: z.string(),
    user: z.string().nullable(),
  }),
  handler: async (ctx) => {
    return {
      message: `hello, ${ctx.input.name ?? "world"}!`,
      user: ctx.user?.id ?? null,
    };
  },
});
