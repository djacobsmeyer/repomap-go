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

| Tool | Params | Description |
|------|--------|-------------|
| `repo_map` | `project_root`, `map_tokens` (default 8192), `chat_files`, `force_refresh` | Ranked structural map of the project — definitions only, sorted by PageRank |
| `search_identifiers` | `project_root`, `query`, `filter` (defs/refs/both), `kinds`, `limit` | Find functions/classes/variables by name |
| `get_blast_radius` | `project_root`, `symbol`, `file` (optional), `depth` (default 3) | Every file and symbol that transitively depends on a given symbol |
| `find_dead_code` | `project_root`, `min_rank`, `unexported_only`, `exported_only`, `kinds` | Symbols defined but never referenced; use `unexported_only: true` for actionable results |
| `get_changed_symbols` | `project_root`, `git_ref` OR `diff`, `include_blast_radius` | Symbols whose definitions fall within changed line ranges |

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/smacker/go-tree-sitter` | Tree-sitter C bindings + language grammars |
| `github.com/fsnotify/fsnotify` | Cross-platform file watching |
| `modernc.org/sqlite` | Pure-Go SQLite for tag cache |
| `github.com/spf13/cobra` | CLI |
| `golang.org/x/sync/errgroup` | Goroutine lifecycle |

## Prerequisites

```bash
brew install go   # requires Go 1.22+
```

## Security notes

- `project_root` parameter is validated to be an absolute path within a registered project — no path traversal
- `.env` files are excluded from parsing (no Tree-sitter grammar; explicitly skipped in file filter)
- SSE stream is localhost-only by default
- `git_ref` parameter (Phase 2) validated against `[a-zA-Z0-9._~^/-]+` before shell execution
