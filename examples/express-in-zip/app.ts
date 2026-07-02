// app.ts — a legacy Express-shaped handler. Source is TypeScript; at
// startup main.go runs it through esbuild (target ES2015) and drops the
// result into zip's embedded goja VM, where JSHandler invokes it with an
// Express-shaped (req, res) pair on every request.
//
// This is the proof point of zip's TS migration path: real TS source,
// real esbuild, real goja, real Fiber, real HTTP — no rewrite required.
module.exports = function (req: any, res: any) {
  res.json({
    ok: true,
    path: req.path,
    body: req.body,
  });
};
