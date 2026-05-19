package watcher

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher recursively watches a directory tree and emits debounced batches of
// changed file paths (relative to root) on the Events channel.
type Watcher struct {
	fw       *fsnotify.Watcher
	root     string
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

// New creates a new recursive watcher rooted at `root`.
func New(root string) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		fw:       fw,
		root:     root,
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
func isIgnored(rel string) bool {
	if rel == "" || rel == "." {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, p := range parts {
		switch p {
		case ".git", "node_modules", ".repomap", "vendor":
			return true
		}
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
		rel, _ := filepath.Rel(root, path)
		if isIgnored(rel) {
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
			if isIgnored(rel) {
				continue
			}
			// If a directory was created, start watching it.
			if ev.Op&fsnotify.Create == fsnotify.Create {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = w.addRecursive(ev.Name)
					continue
				}
			}
			// Only emit file paths.
			if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
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
