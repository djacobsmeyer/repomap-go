package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yourusername/repomap-go/internal/cache"
	"github.com/yourusername/repomap-go/internal/events"
	"github.com/yourusername/repomap-go/internal/graph"
	"github.com/yourusername/repomap-go/internal/parser"
	"github.com/yourusername/repomap-go/internal/watcher"
)

// Project owns a single repository's index, graph, watcher and MCP socket.
type Project struct {
	Root string

	mu      sync.RWMutex
	index   map[string][]parser.Tag
	graph   *graph.FileGraph
	ranks   map[string]float32
	cache   *cache.Cache
	watcher *watcher.Watcher
	bus     *events.Bus

	lastMCPCall    time.Time
	lastReindex    time.Time
	socketPath     string
	mcpListener    net.Listener
	mcpHandler     func(net.Conn)

	cancel context.CancelFunc
	done   chan struct{}
}

// New constructs a Project rooted at `root`. Root must be an absolute path.
func New(root string, bus *events.Bus) (*Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", abs)
	}
	hash := sha256.Sum256([]byte(abs))
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("repomap-%s.sock", hex.EncodeToString(hash[:])[:8]))

	return &Project{
		Root:       abs,
		index:      make(map[string][]parser.Tag),
		bus:        bus,
		socketPath: sock,
		done:       make(chan struct{}),
	}, nil
}

// SocketPath returns the per-project MCP Unix socket path.
func (p *Project) SocketPath() string { return p.socketPath }

// SetMCPHandler installs the callback invoked for each MCP socket connection.
// If nil, connections are simply closed.
func (p *Project) SetMCPHandler(h func(net.Conn)) {
	p.mu.Lock()
	p.mcpHandler = h
	p.mu.Unlock()
}

// Start performs the initial index, builds the graph, then runs the event loop
// until ctx is cancelled. It returns once startup tasks (initial index + graph)
// are complete; the long-running loop runs in a background goroutine.
func (p *Project) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	c, err := cache.Open(p.Root)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	p.mu.Lock()
	p.cache = c
	p.mu.Unlock()

	if err := p.initialIndex(); err != nil {
		return fmt.Errorf("initial index: %w", err)
	}
	p.rebuildGraph()

	if p.bus != nil {
		p.bus.Emit(p.Root, "project_added", map[string]interface{}{
			"tags": p.TagCount(),
		})
	}

	w, err := watcher.New(p.Root)
	if err != nil {
		return fmt.Errorf("watcher: %w", err)
	}
	p.mu.Lock()
	p.watcher = w
	p.mu.Unlock()

	if err := p.startMCPListener(); err != nil {
		// non-fatal; log via event
		if p.bus != nil {
			p.bus.Emit(p.Root, "mcp_listener_error", map[string]interface{}{"error": err.Error()})
		}
	}

	go p.run(ctx)
	return nil
}

// Stop tears the project down.
func (p *Project) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.watcher != nil {
		p.watcher.Close()
		p.watcher = nil
	}
	if p.mcpListener != nil {
		p.mcpListener.Close()
		p.mcpListener = nil
	}
	_ = os.Remove(p.socketPath)
	if p.cache != nil {
		p.cache.Close()
		p.cache = nil
	}
	return nil
}

func (p *Project) run(ctx context.Context) {
	defer close(p.done)
	idleTicker := time.NewTicker(5 * time.Minute)
	defer idleTicker.Stop()

	p.mu.RLock()
	w := p.watcher
	p.mu.RUnlock()

	var evCh <-chan []string
	if w != nil {
		evCh = w.Events()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case batch := <-evCh:
			p.handleChanges(batch)
		case <-idleTicker.C:
			p.maybeEvict()
		}
	}
}

// maybeEvict drops the in-memory index/graph/ranks if the project is idle.
// The watcher is paused; the cache (SQLite) stays open so we can rehydrate
// cheaply on the next MCP call.
func (p *Project) maybeEvict() {
	if !p.IsIdle() {
		return
	}
	p.mu.Lock()
	if p.index == nil {
		p.mu.Unlock()
		return // already evicted
	}
	p.index = nil
	p.graph = nil
	p.ranks = nil
	w := p.watcher
	p.mu.Unlock()
	if w != nil {
		w.Pause()
	}
	if p.bus != nil {
		p.bus.Emit(p.Root, "project_idle", map[string]interface{}{
			"evicted": true,
		})
	}
}

// ensureHydrated rebuilds the in-memory index from the on-disk cache if it has
// been evicted. Cheap relative to a full reparse — just JSON-decodes cached
// tag rows and rebuilds the graph + PageRank.
func (p *Project) ensureHydrated() {
	p.mu.RLock()
	hydrated := p.index != nil
	p.mu.RUnlock()
	if hydrated {
		return
	}

	p.mu.Lock()
	if p.index != nil {
		p.mu.Unlock()
		return
	}
	c := p.cache
	w := p.watcher
	p.mu.Unlock()

	if c == nil {
		return
	}
	loaded, err := c.LoadAll()
	if err != nil || loaded == nil {
		loaded = make(map[string][]parser.Tag)
	}
	p.mu.Lock()
	p.index = loaded
	p.mu.Unlock()
	p.rebuildGraph()
	if w != nil {
		w.Resume()
	}
	if p.bus != nil {
		p.bus.Emit(p.Root, "project_rehydrated", map[string]interface{}{
			"tags": p.TagCount(),
		})
	}
}

func (p *Project) handleChanges(files []string) {
	if len(files) == 0 {
		return
	}
	start := time.Now()
	if p.bus != nil {
		p.bus.Emit(p.Root, "reindex_started", map[string]interface{}{
			"files_changed": len(files),
		})
	}
	for _, rel := range files {
		abs := filepath.Join(p.Root, rel)
		if p.bus != nil {
			p.bus.Emit(p.Root, "file_changed", map[string]interface{}{"file": rel})
		}
		info, err := os.Stat(abs)
		if err != nil {
			// deleted
			p.mu.Lock()
			delete(p.index, rel)
			p.mu.Unlock()
			if p.cache != nil {
				p.cache.Invalidate(rel)
			}
			continue
		}
		if info.IsDir() {
			continue
		}
		if parser.FilenameToLang(rel) == "" {
			continue
		}
		mtime := info.ModTime().UnixNano()
		var tags []parser.Tag
		if cached, ok := p.cache.Get(rel, mtime); ok {
			tags = cached
			if p.bus != nil {
				p.bus.Emit(p.Root, "cache_hit", map[string]interface{}{"file": rel})
			}
		} else {
			parsed, _ := parser.ParseFile(p.Root, rel)
			tags = parsed
			p.cache.Set(rel, mtime, tags)
		}
		p.mu.Lock()
		if len(tags) == 0 {
			delete(p.index, rel)
		} else {
			p.index[rel] = tags
		}
		p.mu.Unlock()
	}
	p.rebuildGraph()
	if p.bus != nil {
		p.bus.Emit(p.Root, "reindex_complete", map[string]interface{}{
			"duration_ms": time.Since(start).Milliseconds(),
			"tags":        p.TagCount(),
		})
	}
}

func (p *Project) initialIndex() error {
	return filepath.Walk(p.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(p.Root, path)
		if info.IsDir() {
			if shouldSkipDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if parser.FilenameToLang(rel) == "" {
			return nil
		}
		mtime := info.ModTime().UnixNano()
		var tags []parser.Tag
		if cached, ok := p.cache.Get(rel, mtime); ok {
			tags = cached
		} else {
			parsed, _ := parser.ParseFile(p.Root, rel)
			tags = parsed
			p.cache.Set(rel, mtime, tags)
		}
		if len(tags) > 0 {
			p.mu.Lock()
			p.index[rel] = tags
			p.mu.Unlock()
		}
		return nil
	})
}

func shouldSkipDir(rel string) bool {
	if rel == "" || rel == "." {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, p := range parts {
		switch p {
		case ".git", "node_modules", ".repomap", "vendor", "dist", "build":
			return true
		}
	}
	return false
}

func (p *Project) rebuildGraph() {
	p.mu.Lock()
	snapshot := make(map[string][]parser.Tag, len(p.index))
	for k, v := range p.index {
		snapshot[k] = v
	}
	p.mu.Unlock()

	g := graph.Build(snapshot)
	r := graph.PageRank(g, 15, 0.85)

	p.mu.Lock()
	p.graph = g
	p.ranks = r
	p.lastReindex = time.Now()
	p.mu.Unlock()
}

// RepoMap returns a human-readable repo map respecting tokenBudget.
// chatFiles and forceRefresh are reserved for future use (boost ranks for
// files referenced in chat, force a full rebuild). For phases 1-5 they are
// accepted but only forceRefresh triggers a rebuild.
func (p *Project) RepoMap(tokenBudget int, chatFiles []string, forceRefresh bool) string {
	p.ensureHydrated()
	if forceRefresh {
		p.rebuildGraph()
	}
	p.mu.RLock()
	idx := make(map[string][]parser.Tag, len(p.index))
	for k, v := range p.index {
		idx[k] = v
	}
	ranks := p.ranks
	p.mu.RUnlock()

	p.touchMCP()
	out := graph.RenderMap(ranks, idx, tokenBudget)
	if p.bus != nil {
		p.bus.Emit(p.Root, "mcp_call", map[string]interface{}{
			"tool":   "repo_map",
			"tokens": len(out) / 4,
		})
	}
	return out
}

// SearchIdentifiers finds tags whose Name contains `query` (case-insensitive).
// filter: "defs" | "refs" | "both" (default "both").
func (p *Project) SearchIdentifiers(query, filter string, limit int) []parser.Tag {
	p.ensureHydrated()
	p.touchMCP()
	if limit <= 0 {
		limit = 50
	}
	if filter == "" {
		filter = "both"
	}
	q := strings.ToLower(query)
	p.mu.RLock()
	defer p.mu.RUnlock()

	var out []parser.Tag
	for _, tags := range p.index {
		for _, t := range tags {
			if filter == "defs" && t.Kind != "def" {
				continue
			}
			if filter == "refs" && t.Kind != "ref" {
				continue
			}
			if q == "" || strings.Contains(strings.ToLower(t.Name), q) {
				out = append(out, t)
				if len(out) >= limit {
					return out
				}
			}
		}
	}
	return out
}

// BlastRadius returns transitive dependents of `symbol`.
func (p *Project) BlastRadius(symbol, file string, maxDepth int) graph.BlastRadiusResult {
	p.ensureHydrated()
	p.touchMCP()
	p.mu.RLock()
	idx := make(map[string][]parser.Tag, len(p.index))
	for k, v := range p.index {
		idx[k] = v
	}
	g := p.graph
	p.mu.RUnlock()
	res := graph.BlastRadius(g, idx, symbol, file, maxDepth)
	if p.bus != nil {
		p.bus.Emit(p.Root, "mcp_call", map[string]interface{}{"tool": "get_blast_radius"})
	}
	return res
}

// FindDeadCode returns symbols defined but never referenced.
func (p *Project) FindDeadCode(minRank float32) graph.DeadCodeResult {
	p.ensureHydrated()
	p.touchMCP()
	p.mu.RLock()
	idx := make(map[string][]parser.Tag, len(p.index))
	for k, v := range p.index {
		idx[k] = v
	}
	g := p.graph
	ranks := p.ranks
	p.mu.RUnlock()
	res := graph.FindDeadCode(g, idx, ranks, minRank)
	if p.bus != nil {
		p.bus.Emit(p.Root, "mcp_call", map[string]interface{}{"tool": "find_dead_code"})
	}
	return res
}

// ChangedSymbols returns def-tags falling within changed line ranges.
// Either `diff` (raw unified diff text) or `gitRef` must be provided.
func (p *Project) ChangedSymbols(diff, gitRef string, includeBlastRadius bool) graph.ChangedSymbolsResult {
	p.ensureHydrated()
	p.touchMCP()
	rawDiff := diff
	if rawDiff == "" && gitRef != "" {
		d, err := graph.GitDiff(p.Root, gitRef)
		if err != nil {
			return graph.ChangedSymbolsResult{
				ChangedSymbols: []graph.ChangedSymbol{},
				Summary:        "git diff error: " + err.Error(),
			}
		}
		rawDiff = d
	}
	parsed := graph.ParseDiff(rawDiff)
	p.mu.RLock()
	idx := make(map[string][]parser.Tag, len(p.index))
	for k, v := range p.index {
		idx[k] = v
	}
	g := p.graph
	p.mu.RUnlock()
	res := graph.ChangedSymbols(idx, parsed, g, includeBlastRadius, 3)
	if p.bus != nil {
		p.bus.Emit(p.Root, "mcp_call", map[string]interface{}{"tool": "get_changed_symbols"})
	}
	return res
}

// MemoryEstimateBytes returns a rough heap-cost estimate for this project.
func (p *Project) MemoryEstimateBytes() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var n int64
	for _, tags := range p.index {
		n += int64(len(tags))
	}
	return n * 80
}

// TagCount returns the total number of indexed tags.
func (p *Project) TagCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for _, tags := range p.index {
		n += len(tags)
	}
	return n
}

// LastReindexTime returns the time of the most recent graph rebuild.
func (p *Project) LastReindexTime() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastReindex
}

// IsIdle reports whether the project has had no MCP call in >30min.
func (p *Project) IsIdle() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.lastMCPCall.IsZero() {
		return false
	}
	return time.Since(p.lastMCPCall) > 30*time.Minute
}

func (p *Project) touchMCP() {
	p.mu.Lock()
	p.lastMCPCall = time.Now()
	p.mu.Unlock()
}

// startMCPListener begins listening on the per-project Unix socket.
func (p *Project) startMCPListener() error {
	_ = os.Remove(p.socketPath)
	l, err := net.Listen("unix", p.socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(p.socketPath, 0o600); err != nil {
		l.Close()
		return err
	}
	p.mu.Lock()
	p.mcpListener = l
	p.mu.Unlock()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			p.mu.RLock()
			h := p.mcpHandler
			p.mu.RUnlock()
			if h == nil {
				conn.Close()
				continue
			}
			go h(conn)
		}
	}()
	return nil
}

// Snapshot returns a JSON-serializable status snapshot.
func (p *Project) Snapshot() map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return map[string]interface{}{
		"root":          p.Root,
		"tags":          p.tagCountLocked(),
		"files":         len(p.index),
		"last_reindex":  p.lastReindex.Format(time.RFC3339Nano),
		"last_mcp_call": p.lastMCPCall.Format(time.RFC3339Nano),
		"socket":        p.socketPath,
		"memory_bytes":  p.memoryLocked(),
	}
}

func (p *Project) tagCountLocked() int {
	n := 0
	for _, tags := range p.index {
		n += len(tags)
	}
	return n
}

func (p *Project) memoryLocked() int64 {
	var n int64
	for _, tags := range p.index {
		n += int64(len(tags))
	}
	return n * 80
}

// MarshalJSON makes Project status JSON-friendly via Snapshot.
func (p *Project) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.Snapshot())
}
