# Execute error classification (shared frontends)

Bundled HTTP frontends map executor failures through [`internal/plugins/frontends/execerr`](../internal/plugins/frontends/execerr/execerr.go) to a small set: **reject** (4xx-class) vs **internal** (5xx-class). Protocol-specific status text and bodies stay inside each frontend adapter.

For **internal** (5xx) outcomes, the wire `Message` is a fixed, non-revealing string (`internal error`); the original error is retained on the `Outcome` for structured **server-side** logging. Frontends must log `out.Err` at error severity when present and must not echo arbitrary executor/upstream strings in JSON error bodies.

## Richer shared kinds (deferred)

Future work may add more shared kinds (for example: cancellation, upstream unavailable, quota exhaustion, hook mutation) **without** moving protocol strings into `internal/core`. Any expansion should:

1. extend the shared classifier in `execerr` only,
2. keep wire encoding in `internal/plugins/frontends/<name>/`,
3. add regression tests per new kind before broad use.

No additional shared kinds are required for stage-three closure; this note records the decision to defer.
