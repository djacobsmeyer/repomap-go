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
{ "ts": "...", "project": "~/code/api", "type": "file_changed",     "file": "src/server.ts" }
{ "ts": "...", "project": "~/code/api", "type": "reindex_started",  "files_changed": 3 }
{ "ts": "...", "project": "~/code/api", "type": "reindex_complete", "duration_ms": 42, "tags": 1840 }
{ "ts": "...", "project": "~/code/api", "type": "cache_hit",        "file": "src/db.ts" }
{ "ts": "...", "project": "~/code/api", "type": "mcp_call",         "tool": "repo_map", "tokens": 3200 }
{ "ts": "...", "project": "~/code/api", "type": "project_idle",     "evicted": false }
```

### Subscribing to the event stream

**CLI — pretty-printed, ANSI colours:**
```bash
repomap events                                    # all projects
repomap events --project ~/code/api              # one project
```

**curl — raw SSE, all projects:**
```bash
curl -N http://localhost:7374/events
```

**curl — filtered to one project:**
```bash
curl -N "http://localhost:7374/events?project=$(pwd)"
```

**jq pipeline — watch reindex timing:**
```bash
curl -sN http://localhost:7374/events \
  | grep --line-buffered '"type"' \
  | jq -R 'fromjson | select(.type == "reindex_complete") | {project: .project, ms: .duration_ms, tags: .tags}'
```

**Shell trigger — verify a file change fires events:**
```bash
# Terminal 1
curl -N http://localhost:7374/events

# Terminal 2
touch ~/code/api/src/server.ts
# Expect: file_changed → reindex_started → reindex_complete (2–10ms)
# Second touch of same file: cache_hit → reindex_complete (0ms)
```

**Node.js / TypeScript client:**
```typescript
const res = await fetch('http://localhost:7374/events?project=/your/project')
const reader = res.body!.getReader()
const decoder = new TextDecoder()
while (true) {
  const { done, value } = await reader.read()
  if (done) break
  const lines = decoder.decode(value).split('\n')
  for (const line of lines) {
    if (line.startsWith('data: ')) {
      const event = JSON.parse(line.slice(6))
      console.log(event.type, event)
    }
  }
}
```

**Python client:**
```python
import httpx, json
with httpx.stream('GET', 'http://localhost:7374/events') as r:
    for line in r.iter_lines():
        if line.startswith('data: '):
            event = json.loads(line[6:])
            print(event['type'], event)
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
| `search_identifiers` | `project_root`, `query`, `filter` (defs/refs/both), `kinds`, `limit` | Find functions/classes/variables by name. For markdown: filter by `kinds: ["heading-1","heading-2","heading-3"]` |
| `get_blast_radius` | `project_root`, `symbol`, `file` (optional), `depth` (default 3) | Every file and symbol that transitively depends on a given symbol |
| `find_dead_code` | `project_root`, `min_rank`, `unexported_only`, `exported_only`, `kinds` | Symbols defined but never referenced; use `unexported_only: true` for actionable results |
| `get_changed_symbols` | `project_root`, `git_ref` OR `diff`, `include_blast_radius` | Symbols whose definitions fall within changed line ranges |

## Markdown / knowledge base support

`.md` and `.markdown` files are indexed alongside code. The same five MCP tools work unchanged — only the vocabulary of "symbols" shifts.

### What counts as a symbol in markdown

| Kind | Example | Def or Ref |
|------|---------|------------|
| `heading-1` … `heading-6` | `# Overview`, `## Installation` | Definition — structural landmark |
| `link` (inline link) | `[see setup](../setup.md)` | Reference — inter-document edge |
| `wikilink` | `[[Architecture Overview]]` | Reference — Obsidian/Foam vault link |

Frontmatter keys and code fence info strings are extracted for `search_identifiers` but do **not** form graph edges — they carry no inter-document reference semantics.

### How the graph works for docs

Each markdown file registers itself as a definition. An `[inline link](target.md)` or `[[Wikilink]]` in file A creates a directed edge A → target, exactly like a function call in code. PageRank over those edges identifies hub documents — files that many others link to. `find_dead_code` surfaces orphan pages (zero inbound links) and `get_blast_radius` answers "which documents link to this one?"

### Token efficiency for AI agents

Naively reading a large knowledge base costs tokens proportional to total file size. A `repo_map` call instead delivers a PageRank-sorted heading outline within a fixed token budget:

```
ALGORITHM/v6.3.0.md:          (Rank: 4.21)
  1: The Algorithm 6.3.0
  5: Doctrine — Read This First, Internalize It
  25: Effort Levels
  ...
DOCUMENTATION/Architecture.md: (Rank: 3.88)
  1: PAI Architecture Summary
  6: Overview
  14: Subsystem Reference
```

For a 500-file vault (~50 MB of prose), `repo_map(map_tokens=8192)` delivers structural orientation at ~1–2% of the raw read cost. Cross-cutting lookups like `get_blast_radius` reduce targeted searches by 5–10× versus scanning each file.

### When markdown repomap is most useful

**Works best when files link to each other** — Obsidian vaults with `[[wikilinks]]`, documentation sites with `[cross-references](other.md)`, wikis, and any corpus where documents explicitly cite related documents. PageRank identifies hub pages; orphan detection surfaces isolated content.

**Limited graph signal when files don't link** — Some knowledge bases (e.g. AI-prompt vaults loaded via `@-import` conventions, or lecture notes with no cross-refs) have sparse link graphs. In those vaults `find_dead_code` will show most files as "orphans" and `get_blast_radius` will return few dependents — not a bug, but an accurate description of the link structure. `search_identifiers` and heading extraction still work regardless of link density.

### Ignored by default in markdown projects

In addition to standard code ignores (`node_modules`, `.git`, etc.), markdown indexing skips:

| Pattern | Reason |
|---------|--------|
| `.obsidian/` | Obsidian config and plugin data |
| `.trash/` | Obsidian deleted-notes folder |
| `*.canvas` | Obsidian canvas JSON (not a text document) |
| `*.excalidraw` | Embedded diagram files |
| Image/binary extensions | `.png`, `.jpg`, `.gif`, `.pdf`, `.svg`, `.webp` — link destinations are filtered; files are not parsed |

### Link resolution

Link destinations are resolved before edges are built:

- **Relative paths** (`../auth/README.md`) — joined against the source file's directory
- **Wikilinks** (`[[Title]]`) — matched against project file basenames; exact match wins, lexicographic-first on ties; `[[Title|Alias]]` aliases are stripped
- **External URLs** (`https://...`) — dropped, no edge created
- **Image embeds** (`![alt](img.png)`) — skipped at parse time (distinct `image` node type in the grammar) and filtered by extension in the resolver

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
