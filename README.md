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

# `repomap daemon start` refuses with `daemon already running (pid N, socket
# P)` when another daemon owns the control socket (it probes the socket
# before starting); stop that daemon first.

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
| `find_dead_code` | `project_root`, `min_rank`, `unexported_only`, `exported_only`, `kinds`, `include_test_orphans`, `include_doc_orphans`, `include_test_symbols` | Symbols defined but never referenced, plus orphan files (each with `lang` and `category`: source/test/docs; dead symbols carry `category` too). Recommended: `unexported_only: true` with `kinds: ["function","method","class"]`; test and docs orphans and test-file symbols are hidden by default |
| `get_changed_symbols` | `project_root`, `git_ref` OR `diff`, `include_blast_radius` | Symbols whose definitions fall within changed line ranges |

### Accuracy notes

- **Python locals are excluded** — a `variable` definition is only tagged at module or class scope; assignments inside functions/lambdas are locals and never appear in results.
- **Bare-name uses count as references** — a name used as a callback argument, dict/list value, assignment RHS, default parameter, decorator, or attribute/subscript object keeps its definition alive.
- **Orphan files are categorized** — each `orphan_files` entry has `lang` and `category` (`source`|`test`|`docs`); test and docs files are hidden by default and revealed with `include_test_orphans` / `include_doc_orphans`. A file is classified `test` when its basename matches `test_*.py`, `*_test.py`, `conftest.py`, `*_test.go`, `*.test.{js,jsx,ts,tsx}`, or `*.spec.{js,jsx,ts,tsx}`, or when a directory segment is `tests`, `__tests__`, or `testdata` anywhere in the path, or `test` as the file's immediate parent directory (`internal/test/harness.go` is test, `internal/test/util/helpers.go` is source; `spec/` is never a test directory, so `myapp/spec/openapi.py` is source).
- **Dead symbols are categorized and test-file symbols are hidden** — each `dead_symbols` entry carries `category` (`source`|`test`|`docs`). Symbols defined in `test` files are hidden by default because test runners discover them and they are never referenced by name (every pytest `test_*` function or Go `TestXxx` would otherwise look dead); set `include_test_symbols: true` to reveal them. Docs symbols (markdown headings) are never hidden — knowledge-base users rely on them.

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

Each markdown file registers itself as a definition. An `[inline link](target.md)` or `[[Wikilink]]` in file A creates a directed edge A → target, exactly like a function call in code. PageRank over those edges identifies hub documents — files that many others link to. `find_dead_code` surfaces orphan pages (zero inbound links) and `get_blast_radius` answers "which documents link to this one?" Since nothing imports a doc, docs orphans are **hidden from `find_dead_code` by default** — pass `include_doc_orphans: true` to include them (each orphan carries `category: "docs"` so you can filter them back out).

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

For a 500-file vault (~2–3 MB of prose, ~500K tokens), `repo_map(map_tokens=8192)` delivers structural orientation at ~1–2% of the raw read cost. Cross-cutting lookups like `get_blast_radius` reduce targeted searches by 5–10× versus scanning each file.

### When markdown repomap is most useful

**Works best when files link to each other** — Obsidian vaults with `[[wikilinks]]`, documentation sites with `[cross-references](other.md)`, wikis, and any corpus where documents explicitly cite related documents. PageRank identifies hub pages; orphan detection surfaces isolated content.

**Limited graph signal when files don't link** — Some knowledge bases (e.g. AI-prompt vaults loaded via `@-import` conventions, or lecture notes with no cross-refs) have sparse link graphs. In those vaults `find_dead_code` would show most files as "orphans" — but docs orphans are now **hidden by default** (pass `include_doc_orphans: true` to see them), so a sparse vault returns a quiet result instead of a wall of noise; `get_blast_radius` will still return few dependents, which is an accurate description of the link structure. `search_identifiers` and heading extraction still work regardless of link density.

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

- Go 1.22+ (install however you prefer — e.g. https://go.dev/dl/, your OS package
  manager, or a version manager like `mise`)
- A C compiler (`cc`) on `PATH` — the Tree-sitter parser bindings use cgo. macOS:
  Xcode Command Line Tools (`xcode-select --install`). Linux: `build-essential` /
  `gcc` from your distro's package manager.

## Installation

Any of the following gets you a working `repomap` binary. None of them assume a
particular tool beyond the Go toolchain itself.

### Option A: `go install` (zero extra tooling)

```bash
go install github.com/djacobsmeyer/repomap-go/cmd/repomap@latest
```

This is the canonical Go-native path: it builds and drops the binary at
`$(go env GOBIN)`, or `$(go env GOPATH)/bin` if `GOBIN` is unset (typically
`~/go/bin`). Make sure that directory is on your `PATH`. From a local clone, drop
the `@latest` and run `go install ./cmd/repomap` instead.

If you manage Go through `mise`, route the install through it instead of calling
`go` directly, and set `GOBIN` *inside* the `mise exec` command rather than
before it — `mise exec` sets up its own environment and a `GOBIN=...` set before
`mise exec --` on the command line gets overridden, not passed through:

```bash
mise exec -- env GOBIN=~/go/bin go install ./cmd/repomap
```

### Option B: `make install`

```bash
make build            # builds ./bin/repomap
make install           # installs to $(PREFIX)/bin, PREFIX defaults to /usr/local
make install PREFIX=~/.local   # or any other prefix you have write access to
make uninstall          # removes the installed binary
make clean              # removes ./bin
```

`make install` just copies the built binary into `$(PREFIX)/bin` — no package
manager, no OS-specific logic.

### Running the daemon persistently

repomap is a centralized daemon that serves many projects, so it's meant to run
continuously rather than be started by hand each session. Service templates are
provided for both major desktop/server platforms, with the binary path
parameterized rather than hardcoded:

- `contrib/launchd/com.repomap.daemon.plist.tmpl` — macOS `launchd` LaunchAgent
- `contrib/systemd/repomap.service.tmpl` — Linux `systemd` user unit

Render the template for your OS with the real installed binary path filled in:

```bash
make install-service PREFIX=~/.local
# or directly: scripts/install-service.sh /path/to/installed/repomap
```

This writes the rendered file to `dist/service/` and prints the exact commands to
install and enable it (`launchctl load ...` on macOS, `systemctl --user enable
--now ...` on Linux). It does not copy into your system service directories or
load/enable anything itself — that step is left to you.

### Upgrading or restarting the daemon

A newly installed binary is not used until the daemon restarts: the STDIO proxy
(`repomap mcp`) only ever talks to a running daemon and never replaces one, so
an upgrade that doesn't restart the daemon keeps serving the old code.

To pick up a new binary, restart the daemon. On macOS with the launchd
LaunchAgent, use a kickstart — it kills and respawns the job in one action. Do
not use `launchctl bootout` + `bootstrap` for upgrades: it leaves a window with
no daemon and can strand a duplicate process that launchd no longer owns
(which is also why `bootout` can appear to "fail to stop" a daemon — it only
signals the process launchd itself spawned):

```bash
launchctl kickstart -k gui/$(id -u)/com.repomap.daemon
```

Equivalently, `repomap daemon stop` and let `KeepAlive` respawn it (on Linux:
`systemctl --user restart repomap`).

Downgrade caveat: an older binary ignores the cache's `meta` table and will
trust newer-parser rows by mtime — after downgrading, delete `.repomap/tags.db`
in affected projects (or reindex) so stale rows aren't served.

### Registering with Claude Code (MCP)

Once `repomap` is on `PATH` (via either install option above), register it per
project using the bare command name — no absolute path needed:

```bash
claude mcp add repomap -- repomap mcp <ABS_PROJECT_PATH>
```

This is local scope by default: the registration is stored in your user config
for that project path, not as a tracked `.mcp.json` in the project itself. The
first MCP tool call from that project auto-registers and indexes it with the
daemon — no separate `repomap add` step required.

## Security notes

- `project_root` parameter is validated to be an absolute path within a registered project — no path traversal
- `.env` files are excluded from parsing (no Tree-sitter grammar; explicitly skipped in file filter)
- SSE stream is localhost-only by default
- `git_ref` parameter (Phase 2) validated against `[a-zA-Z0-9._~^/-]+` before shell execution

## License

MIT — see [LICENSE](LICENSE).
