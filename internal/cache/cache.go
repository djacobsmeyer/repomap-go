package cache

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/djacobsmeyer/repomap-go/internal/parser"
)

// Cache is a per-project on-disk tag cache.
type Cache struct {
	db *sql.DB
	mu sync.Mutex
}

// Open creates (or opens) <projectRoot>/.repomap/tags.db. `version` is the
// parser tag-schema fingerprint (parser.CacheVersion): if the stored
// meta.parser_version differs, every cached tag row is dropped so files are
// re-parsed with the current parser instead of serving stale tags.
func Open(projectRoot, version string) (*Cache, error) {
	dir := filepath.Join(projectRoot, ".repomap")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir cache dir: %w", err)
	}
	dbPath := filepath.Join(dir, "tags.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS tags (
			file  TEXT PRIMARY KEY,
			mtime INTEGER NOT NULL,
			data  TEXT NOT NULL
		);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create meta schema: %w", err)
	}
	// Version the tag rows by parser fingerprint. Inside one transaction so a
	// crash never leaves the meta row updated while stale rows survive (or
	// vice versa).
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("begin version check: %w", err)
	}
	var stored string
	err = tx.QueryRow(`SELECT value FROM meta WHERE key = 'parser_version'`).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		tx.Rollback()
		db.Close()
		return nil, fmt.Errorf("read parser_version: %w", err)
	}
	if err == nil && stored != version {
		if _, err := tx.Exec(`DELETE FROM tags`); err != nil {
			tx.Rollback()
			db.Close()
			return nil, fmt.Errorf("purge stale tags: %w", err)
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO meta (key, value) VALUES ('parser_version', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, version); err != nil {
		tx.Rollback()
		db.Close()
		return nil, fmt.Errorf("upsert parser_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		return nil, fmt.Errorf("commit version check: %w", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		// non-fatal
		_ = err
	}
	return &Cache{db: db}, nil
}

// Get returns the cached tags for relfile if the stored mtime matches.
func (c *Cache) Get(relfile string, mtime int64) ([]parser.Tag, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var storedMtime int64
	var data string
	row := c.db.QueryRow(`SELECT mtime, data FROM tags WHERE file = ?`, relfile)
	if err := row.Scan(&storedMtime, &data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
		return nil, false
	}
	if storedMtime != mtime {
		return nil, false
	}
	var tags []parser.Tag
	if err := json.Unmarshal([]byte(data), &tags); err != nil {
		return nil, false
	}
	return tags, true
}

// Set upserts cached tags for relfile.
func (c *Cache) Set(relfile string, mtime int64, tags []parser.Tag) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tags == nil {
		tags = []parser.Tag{}
	}
	data, err := json.Marshal(tags)
	if err != nil {
		return
	}
	_, _ = c.db.Exec(`
		INSERT INTO tags (file, mtime, data) VALUES (?, ?, ?)
		ON CONFLICT(file) DO UPDATE SET mtime = excluded.mtime, data = excluded.data
	`, relfile, mtime, string(data))
}

// LoadAll returns all cached tag rows keyed by relfile. Used to rehydrate an
// evicted project's in-memory index without re-parsing every file.
func (c *Cache) LoadAll() (map[string][]parser.Tag, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, err := c.db.Query(`SELECT file, data FROM tags`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]parser.Tag)
	for rows.Next() {
		var file, data string
		if err := rows.Scan(&file, &data); err != nil {
			return nil, err
		}
		var tags []parser.Tag
		if err := json.Unmarshal([]byte(data), &tags); err != nil {
			continue
		}
		if len(tags) > 0 {
			out[file] = tags
		}
	}
	return out, rows.Err()
}

// Invalidate removes a single file's cached tags.
func (c *Cache) Invalidate(relfile string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.db.Exec(`DELETE FROM tags WHERE file = ?`, relfile)
}

// Close closes the underlying database.
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return nil
	}
	err := c.db.Close()
	c.db = nil
	return err
}
