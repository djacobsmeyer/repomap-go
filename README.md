# repomap-go

A Go implementation of a repo map MCP server with a persistent daemon, per-project file watchers, incremental indexing, and an SSE event stream.

## Why

The reference Python implementation (pdavis68/RepoMapper) is community-maintained with uncertain longevity. Go gives 5ms startup, trivial goroutine-per-project concurrency, and the same Tree-sitter C core underneath — with a persistent daemon so indexes survive between Claude Code sessions.

## Architecture

### Process model

One daemon binary. Each project gets a goroutine, an in-memory index, and an fsnotify watcher. Claude Code connects via the standard STDIO MCP transport using a thin proxy command (`repomap mcp <path>`) that bridges to the daemon's Unix control socket. The daemon stays alive; the proxy is ephemeral.

```
repomap-daemon (persistent)
  ├── project: ~/code/api     → goroutine + index + fsnotify watcher
  ├── project: ~/code/web     → goroutine + index + fsnotify watcher
  ├── project: ~/code/shared  → goroutine + index + fsnotify watcher
  └── SSE stream: localhost:7374/events  (all projects, filterable by ?project=)

Claude Code (per project, STDIO):
  claude mcp add api-map -- repomap mcp ~/code/api
  claude mcp add web-map -- repomap mcp ~/code/web
```

### Memory model

- Only tags in memory — filenames, line numbers, symbol names, kind (def/ref). No file content.
- PageRank vectors use `float32` (half the size of float64, no ranking precision loss).
- SQLite cache on disk — cold projects don't consume heap; survives restarts.
- LRU eviction — projects idle >30min have in-memory index dropped and watcher paused; reactivated on next MCP call.
- Watcher debounce — 300ms window collapses burst edits (save + lint + format) into one reindex.

### Event stream

```jsonc
// GET localhost:7374/events?project=<path>  (omit filter = all projects)
{ "ts": "...", "project": "~/code/api", "type": "file_changed",    "file": "src/server.ts" }
{ "ts": "...", "project": "~/code/api", "type": "reindex_started",  "files_changed": 3 }
{ "ts": "...", "project": "~/code/api", "type": "reindex_complete", "duration_ms": 42, "tags": 1840 }
{ "ts": "...", "project": "~/code/api", "type": "cache_hit",        "file": "src/db.ts" }
{ "ts": "...", "project": "~/code/api", "type": "mcp_call",         "tool": "repo_map", "tokens": 3200 }
{ "ts": "...", "project": "~/code/api", "type": "project_idle",     "evicted": false }
```

## Project structure

```
repomap-go/
├── cmd/repomap/           # single binary — daemon + CLI + MCP proxy
│   └── main.go
├── internal/
│   ├── daemon/            # control socket, project lifecycle manager
│   ├── project/           # per-project struct: index + goroutine + watcher
│   ├── parser/            # Tree-sitter wrapper, tag extraction
│   ├── graph/             # bipartite file graph + PageRank (float32)
│   ├── cache/             # SQLite tag cache per project
│   ├── watcher/           # fsnotify + 300ms debounce
│   ├── mcp/               # JSON-RPC 2.0 MCP tool definitions
│   ├── events/            # internal channel bus + SSE HTTP handler
│   └── proxy/             # STDIO ↔ Unix socket bridge
├── go.mod
└── README.md
```

## CLI

```bash
repomap daemon start          # start background daemon (SSE on :7374)
repomap daemon stop
repomap daemon status         # uptime, active projects, memory per-project

repomap add ~/code/api        # register project → spawn goroutine + watcher
repomap remove ~/code/api
repomap list                  # all active projects, index size, last reindex time

repomap mcp ~/code/api        # STDIO proxy — invoked by Claude Code
repomap events                # pretty-print SSE stream to terminal
```

## MCP tools

| Tool | Description |
|------|-------------|
| `repo_map` | Ranked structural map of the project. Params: `project_root`, `map_tokens` (default 8192), `chat_files`, `force_refresh` |
| `search_identifiers` | Find functions/classes/variables by name. Params: `project_root`, `query`, `filter` (defs/refs/both), `limit` |

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/smacker/go-tree-sitter` | Tree-sitter C bindings + language grammars |
| `github.com/fsnotify/fsnotify` | Cross-platform file watching |
| `modernc.org/sqlite` | Pure-Go SQLite for tag cache |
| `github.com/spf13/cobra` | CLI |
| `golang.org/x/sync/errgroup` | Goroutine lifecycle |

## Build phases

| Phase | Scope | Est. |
|-------|-------|------|
| 1 | Skeleton + Cobra CLI + daemon control socket + PID file | ~1hr |
| 2 | Parser — Tree-sitter integration, tag extraction, language map | ~1.5hr |
| 3 | Graph — bipartite file graph + float32 PageRank + token budget | ~1hr |
| 4 | Cache — SQLite schema, mtime invalidation, batch upsert | ~45min |
| 5 | Watcher + Project goroutine — fsnotify, debounce, LRU eviction | ~1hr |
| 6 | Events bus + SSE — typed events, fan-out channels, HTTP handler | ~45min |
| 7 | MCP layer — JSON-RPC 2.0, repo_map + search_identifiers tools | ~1hr |
| 8 | STDIO proxy — repomap mcp <path> bridges stdin/stdout to daemon | ~30min |
| 9 | Integration + status command memory report | ~30min |

## Phase 2 — Planned: Impact Analysis Tools

Adds three new MCP tools to cover the capabilities currently requiring a separate jcodemunch/code-review-graph installation. All built on the graph already constructed in Phase 1 — no new parsing required.

### New MCP tools

| Tool | Description |
|------|-------------|
| `get_blast_radius` | Given a symbol name (+ optional file), returns every file and symbol that transitively depends on it |
| `find_dead_code` | Returns symbols defined but never referenced anywhere in the project |
| `get_changed_symbols` | Given a unified diff (or a git ref like `HEAD~1`), returns the symbols whose definitions fall within changed line ranges, plus their blast radius |

### `get_blast_radius`

**Input:** `symbol string`, `file string` (optional — narrows to a specific definition when the symbol is overloaded), `depth int` (default 3, max 10)

**Algorithm:**
1. Build the **inverted graph** at query time: for every edge A→B (A references something in B), add B→A to the inverted map. The inverted graph is derived from the existing `FileGraph.Edges` — O(E), computed once per index and cached on the `Project`.
2. Find the seed: locate all tags where `Name == symbol && Kind == "def"` (filtered by file if provided).
3. BFS/DFS from the seed file(s) through the inverted graph up to `depth` hops.
4. At each hop, collect the referencing symbols (the specific `ref` tags that name our symbol) — not just the files.

**Output:**
```json
{
  "symbol": "handleRequest",
  "defined_in": "internal/mcp/mcp.go:45",
  "direct_dependents": [
    { "file": "internal/daemon/daemon.go", "symbol": "AddProject", "line": 112 }
  ],
  "transitive_dependents": [
    { "file": "cmd/repomap/main.go", "symbol": "daemonCmd", "line": 34, "depth": 2 }
  ],
  "total_files_affected": 3
}
```

**Graph change needed:** Add `InvertedEdges map[string]map[string]float32` to `FileGraph`. Rebuild when the main graph rebuilds. Adds ~same memory as the forward graph.

---

### `find_dead_code`

**Input:** `min_rank float32` (default 0.001 — skip genuinely isolated files like standalone scripts), `kinds []string` (default `["def"]`, could filter to `["function", "class"]` etc.)

**Algorithm:**
1. Collect all `def` tag names across the project into a set: `defined`.
2. Collect all `ref` tag names into a set: `referenced`.
3. Dead symbols = `defined \ referenced` (defined but never referenced).
4. Filter by `min_rank` — exclude files whose PageRank is below threshold (entry points, scripts, and test files legitimately have no callers).
5. Optionally: also report **orphan files** — files with zero inbound edges in the graph (no other file imports them). These are file-level dead code.

**Output:**
```json
{
  "dead_symbols": [
    { "file": "internal/cache/cache.go", "name": "legacyMigrate", "line": 203, "kind": "def" }
  ],
  "orphan_files": [
    { "file": "internal/util/scratch.go", "rank": 0.0001 }
  ],
  "summary": "4 dead symbols, 1 orphan file"
}
```

**Caveats to encode in the tool description:** reflection-called symbols and exported public API symbols appear "dead" by static analysis but aren't. The tool should note this and suggest filtering to unexported symbols first (`name[0] >= 'a' && name[0] <= 'z'` in Go, `_`-prefixed or non-exported in Python, etc.).

---

### `get_changed_symbols`

**Input:** `diff string` (unified diff text) OR `git_ref string` (e.g. `"HEAD~1"`, `"main"`) — one required. `include_blast_radius bool` (default false — can be expensive).

**Algorithm:**
1. If `git_ref` provided: shell out to `git diff <ref> HEAD --unified=0` in the project root to get the diff. (Security: validate `git_ref` against `[a-zA-Z0-9._~^/-]+` — no shell injection.)
2. Parse unified diff: extract `+++ b/<file>` headers and `@@ -old +new,count @@` hunks → build map of `file → []changedLineRange`.
3. For each changed file, walk its `def` tags: if `tag.Line` falls within any changed range, include it.
4. If `include_blast_radius`: for each matched symbol, call the blast-radius BFS (depth 2 to keep it fast).

**Output:**
```json
{
  "changed_symbols": [
    {
      "file": "internal/graph/graph.go",
      "symbol": "PageRank",
      "line": 44,
      "kind": "def",
      "blast_radius": { "total_files_affected": 2, "direct_dependents": [...] }
    }
  ],
  "unchanged_files_affected": ["internal/project/project.go"]
}
```

---

### Implementation plan

| Sub-phase | Scope | Est. |
|-----------|-------|------|
| 2a | Add `InvertedEdges` to `FileGraph`, rebuild on every reindex | ~30min |
| 2b | `get_blast_radius` MCP tool + BFS traversal | ~1hr |
| 2c | `find_dead_code` MCP tool + set difference + orphan detection | ~45min |
| 2d | `get_changed_symbols` MCP tool + diff parser + git_ref shelling | ~1hr |
| 2e | Wire all three into the per-project MCP handler | ~30min |

**Total: ~3.5 hours**

No new dependencies required. The diff parser is straightforward enough for stdlib. The git shell-out is already safe-patternted in the security notes below.

---

## Phase 3 — Planned: Dead Code Filter Improvements

Follow-on to Phase 2 based on observed behavior: `find_dead_code` returns many false positives from exported symbols that appear unreferenced within the project but are part of the public API or called externally.

### Changes to `find_dead_code`

Add two new params:

| Param | Type | Default | Effect |
|-------|------|---------|--------|
| `unexported_only` | bool | false | Only return symbols whose name starts with a lowercase letter (Go), `_` prefix (Python), or equivalent per-language unexported convention. These can only be called within their package — if unused there, genuinely dead. |
| `exported_only` | bool | false | Only return exported symbols. Useful for auditing public API surface that may have been abandoned. Caller should understand false positive rate is high. |

Both default false = current behavior (all symbols). Mutually exclusive — return error if both set.

Add per-language unexported detection to `internal/graph/graph.go`:
- Go: `name[0] >= 'a' && name[0] <= 'z'`
- Python: name starts with `_` but not `__` (dunder methods are framework-called)
- TypeScript/JS: no enforced convention — skip filter for these languages, or treat leading `_` as unexported by convention
- Rust: items without `pub` keyword — would require AST-level visibility info not currently in tags; skip for now, document limitation

Also add: `kinds []string` filter param (e.g. `["function", "method"]`) to narrow result to specific symbol types. Requires tagging Kind more granularly than just "def"/"ref" — that's a parser change.

### Sub-phase plan

| Sub-phase | Scope | Est. |
|-----------|-------|------|
| 3a | Add `unexported_only` + `exported_only` params to `find_dead_code` | ~30min |
| 3b | Per-language unexported detection in graph package | ~30min |
| 3c | Granular Kind tags in parser (function/method/class/type/variable/constant) | ~1hr |
| 3d | `kinds []string` filter on `find_dead_code` and `search_identifiers` | ~30min |

**Total: ~2.5 hours**

---

## Prerequisites

```bash
brew install go   # requires Go 1.22+
```

## Security notes

- `project_root` parameter is validated to be an absolute path within a registered project — no path traversal
- `.env` files are excluded from parsing (no Tree-sitter grammar; explicitly skipped in file filter)
- SSE stream is localhost-only by default
- `git_ref` parameter (Phase 2) validated against `[a-zA-Z0-9._~^/-]+` before shell execution
