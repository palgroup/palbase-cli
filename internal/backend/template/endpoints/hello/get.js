// Sample endpoint scaffolded by `palbase backend init`.
// Replace it with your own handlers under endpoints/, then
// `palbase backend deploy` to publish.
//
// Each file's name is the HTTP method (get|post|put|patch|delete);
// the file's directory becomes the URL path. So
//   endpoints/hello/get.js     → GET  /hello
//   endpoints/posts/[id]/get.js → GET  /posts/:id

module.exports = {
  default: {
    method: 'GET',
    handler: async (ctx) => {
      return {
        ok: true,
        message: 'hello from palbase',
        project: ctx.env?.PALBASE_PROJECT_REF ?? null,
      };
    },
  },
};
