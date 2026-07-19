---
name: repomap-facts
description: "Verified repomap-go MCP server facts — tool names, the explain prompt, daemon model, registration, and current gaps"
metadata: 
  node_type: memory
  type: project
  originSessionId: d6310abd-f792-4926-8ea7-4bb08ecf623a
  modified: 2026-07-19T18:12:39.966Z
---

repomap-go (`~/sourcecode/repomap-go`) exposes exactly 5 MCP tools, verified against
`internal/mcp/mcp.go`: `repo_map` (PageRank-ranked structural map), `search_identifiers`
(find defs/refs by name), `get_blast_radius` (transitive dependents of a symbol — call before
renaming/deleting), `find_dead_code` (unreferenced symbols), `get_changed_symbols` (symbols in
changed line ranges, for PR review).

As of commit f62b6d0, the server also exposes an MCP prompt named `explain` (via
`prompts/list` / `prompts/get`) that self-teaches an agent the daemon model, all 5 tools, and
worked examples. In a Claude Code session it's invoked as `/mcp__<server-name>__explain` —
e.g. `/mcp__repomap__explain` when the server was registered under the name `repomap`.

**Daemon model:** one persistent daemon process per machine; each registered project gets a
goroutine + in-memory index + fsnotify watcher. The STDIO proxy (`repomap mcp <path>`) auto-starts
the daemon if it isn't running. The **first MCP tool call** from a project auto-registers and
indexes it — no manual `repomap add` step needed. SSE event stream for humans/orchestrators to
watch indexing live: `localhost:7374` (`repomap events` or `curl -N http://localhost:7374/events`).

**Registration:** `claude mcp add repomap -- <ABS_PATH>/repomap mcp <ABS_PROJECT_PATH>`, local
scope — stored in `~/.claude.json` under `projects.<path>.mcpServers`, never a tracked
`.mcp.json` in the target repo.

**Current gaps (as of 2026-07-19):** the `repomap` binary is not on `$PATH` — registration
must use the absolute path to the binary. No launchd persistence yet — the daemon dies on
reboot/logout and must be restarted by hand (`repomap daemon start`).

See also [[repomap-orchestrator-skill]] — the orchestrator skill section built from these facts.
