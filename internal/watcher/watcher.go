package watcher

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/yourusername/repomap-go/internal/ignore"
)

// Watcher recursively watches a directory tree and emits debounced batches of
// changed file paths (relative to root) on the Events channel.
type Watcher struct {
	fw       *fsnotify.Watcher
	root     string
	matcher  *ignore.Matcher
	eventsCh chan []string
	done     chan struct{}
	once     sync.Once

	pauseMu sync.RWMutex
	paused  bool
}

// Pause stops emitting batched change events. Pending events are dropped while
// paused. Safe to call multiple times.
func (w *Watcher) Pause() {
	w.pauseMu.Lock()
	w.paused = true
	w.pauseMu.Unlock()
}

// Resume re-enables event emission.
func (w *Watcher) Resume() {
	w.pauseMu.Lock()
	w.paused = false
	w.pauseMu.Unlock()
}

func (w *Watcher) isPaused() bool {
	w.pauseMu.RLock()
	defer w.pauseMu.RUnlock()
	return w.paused
}

// Events returns the receive-only batch channel.
func (w *Watcher) Events() <-chan []string { return w.eventsCh }

// New creates a new recursive watcher rooted at `root` using the given
// ignore matcher. The matcher may be nil — in which case only the trivial
// .db-shard filter applies.
func New(root string, m *ignore.Matcher) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		fw:       fw,
		root:     root,
		matcher:  m,
		eventsCh: make(chan []string, 8),
		done:     make(chan struct{}),
	}
	if err := w.addRecursive(root); err != nil {
		fw.Close()
		return nil, err
	}
	go w.run()
	return w, nil
}

// isIgnored returns true for paths the watcher should ignore.
// absPath is the full filesystem path; isDir indicates directory-ness.
func (w *Watcher) isIgnored(absPath string, isDir bool) bool {
	rel, err := filepath.Rel(w.root, absPath)
	if err != nil {
		return false
	}
	if rel == "" || rel == "." {
		return false
	}
	if w.matcher != nil && w.matcher.ShouldIgnore(absPath, isDir) {
		return true
	}
	base := filepath.Base(rel)
	if strings.HasSuffix(base, ".db") || strings.HasSuffix(base, ".db-wal") || strings.HasSuffix(base, ".db-shm") {
		return true
	}
	return false
}

func (w *Watcher) addRecursive(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// permission etc — skip
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		if w.isIgnored(path, true) {
			return filepath.SkipDir
		}
		_ = w.fw.Add(path)
		return nil
	})
}

func (w *Watcher) run() {
	const debounce = 300 * time.Millisecond
	pending := make(map[string]struct{})
	var timer *time.Timer
	var timerC <-chan time.Time

	flush := func() {
		if len(pending) == 0 {
			return
		}
		if w.isPaused() {
			// Drop pending events while paused.
			pending = make(map[string]struct{})
			return
		}
		batch := make([]string, 0, len(pending))
		for p := range pending {
			batch = append(batch, p)
		}
		pending = make(map[string]struct{})
		select {
		case w.eventsCh <- batch:
		case <-w.done:
		}
	}

	for {
		select {
		case <-w.done:
			if timer != nil {
				timer.Stop()
			}
			return
		case ev, ok := <-w.fw.Events:
			if !ok {
				return
			}
			rel, err := filepath.Rel(w.root, ev.Name)
			if err != nil {
				continue
			}
			// Stat once — we need both the type and ignore check.
			info, statErr := os.Stat(ev.Name)
			isDir := statErr == nil && info.IsDir()
			if w.isIgnored(ev.Name, isDir) {
				continue
			}
			// If a directory was created, start watching it.
			if ev.Op&fsnotify.Create == fsnotify.Create && isDir {
				_ = w.addRecursive(ev.Name)
				continue
			}
			// Only emit file paths.
			if isDir {
				continue
			}
			pending[rel] = struct{}{}
			if timer == nil {
				timer = time.NewTimer(debounce)
				timerC = timer.C
			} else {
				timer.Reset(debounce)
			}
		case <-timerC:
			flush()
		case _, ok := <-w.fw.Errors:
			if !ok {
				return
			}
		}
	}
}

// Close stops the watcher and releases resources.
func (w *Watcher) Close() error {
	var err error
	w.once.Do(func() {
		close(w.done)
		err = w.fw.Close()
	})
	return err
}
