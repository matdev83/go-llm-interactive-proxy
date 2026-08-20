---
name: codegraph
description: Use CodeGraph to understand code through its AST-derived symbol and relationship graph. Use for architecture, implementations, callers/callees, call paths, dependency direction, change impact, affected tests, or structural code navigation in an indexed repository. CodeGraph is a read-only exploration tool, never an agent or delegation target.
---

# CodeGraph

Query CodeGraph directly. It is a local code index, not a worker: never delegate, assign, or prompt it to perform tasks.

## Workflow

1. Check `codegraph status`. In a new worktree for this repository, run `codegraph init` if `.codegraph/` is absent. Run `codegraph sync` only when status is stale or no watcher is active.
2. Start with one `codegraph_explore` MCP call, or its CLI equivalent:

   ```text
   codegraph explore --max-files 8 "internal/infra/runtimebundle/host_build.go: BuildHost -> buildProcessServicesOp -> NewProcessServices; show the call path, implementations, ownership, tests, and blast radius"
   ```

   Name the relevant file/package and 2–6 distinctive symbols. State the relationship or flow sought. Avoid broad names such as `Request`, `Context`, or `Stream`.
3. Treat returned line-numbered source as already read. If it answers the question, stop; do not repeat the lookup with file reads, FFF, or ripgrep.
4. If incomplete, issue one narrower `explore` using symbols learned from the first result. Use specialized commands only for a precise follow-up:
   - `node SYMBOL -f FILE`: exact body plus caller/callee trail; disambiguate common names.
   - `query NAME --kind KIND`: locate an unknown symbol name.
   - `callers` / `callees`: one graph hop.
   - `impact SYMBOL --depth N`: transitive pre-edit blast radius.
   - `affected FILE...`: candidate tests for changed source files.
   - Add `--json` only for machine processing.
5. Keep `--max-files` as small as useful. Verify any ambiguous or heuristic edge against the returned source. Heed stale-index warnings.

Use FFF/ripgrep instead for exact text, filenames, configuration, documentation, or logs. Fall back to file reads only when the graph is absent, stale for the referenced file, unsupported, or genuinely incomplete.
