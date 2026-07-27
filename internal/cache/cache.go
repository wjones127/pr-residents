// Package cache is the pr-detail cache, keyed on (repo, number). It lets a
// re-run skip the heavy detail query for PRs whose updatedAt has not changed
// since the last sync. State lives under the store's cache/ namespace.
package cache

import (
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go sqlite driver

	"github.com/wjones127/pr-residents/internal/prr"
)

// Entry is a cached PR: the updatedAt it was fetched at and the derived record.
type Entry struct {
	UpdatedAt string
	Record    *prr.Record
}

// Cache is the pr-detail cache seam.
type Cache interface {
	// EnsureFingerprint drops all cached records if the derivation inputs
	// (config + logic version) changed — updatedAt alone can't detect that.
	EnsureFingerprint(fingerprint string) error
	Get(repo string, number int) (*Entry, error)
	Put(repo string, number int, updatedAt, headOid string, record *prr.Record) error
	// GetMergedCount returns an author's cached merged-PR count and when it was
	// fetched; ok is false on a miss. Freshness policy is the caller's — the
	// cache only records the value and its timestamp. Independent of the record
	// fingerprint (raw GitHub data), so it survives EnsureFingerprint.
	GetMergedCount(repo, author string) (count int, fetchedAt time.Time, ok bool, err error)
	PutMergedCount(repo, author string, count int, fetchedAt time.Time) error
	Close() error
}

// SQLiteCache is a SQLite-backed Cache.
type SQLiteCache struct {
	db *sql.DB
}

// OpenSQLite opens (creating if needed) a SQLite cache at path.
func OpenSQLite(path string) (*SQLiteCache, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite is single-writer and this is a local, single-run
	// cache, so a pool only invites "database is locked".
	db.SetMaxOpenConns(1)
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS pr_cache (
			repo        TEXT NOT NULL,
			number      INTEGER NOT NULL,
			updated_at  TEXT NOT NULL,
			head_oid    TEXT NOT NULL,
			record_json TEXT NOT NULL,
			PRIMARY KEY (repo, number)
		)`,
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`,
		// Author merged-PR counts change slowly, so they persist across runs with
		// a caller-applied TTL. Kept separate from pr_cache so EnsureFingerprint's
		// wipe (a derivation-logic change) does not discard this raw GitHub fact.
		`CREATE TABLE IF NOT EXISTS merged_count (
			repo       TEXT NOT NULL,
			author     TEXT NOT NULL,
			count      INTEGER NOT NULL,
			fetched_at TEXT NOT NULL,
			PRIMARY KEY (repo, author)
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &SQLiteCache{db: db}, nil
}

func (c *SQLiteCache) EnsureFingerprint(fingerprint string) error {
	var current string
	err := c.db.QueryRow("SELECT value FROM meta WHERE key = 'fingerprint'").Scan(&current)
	if err == sql.ErrNoRows || current != fingerprint {
		if _, err := c.db.Exec("DELETE FROM pr_cache"); err != nil {
			return err
		}
		_, err := c.db.Exec(
			`INSERT INTO meta (key, value) VALUES ('fingerprint', ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fingerprint)
		return err
	}
	return err
}

func (c *SQLiteCache) Get(repo string, number int) (*Entry, error) {
	var updatedAt, recordJSON string
	err := c.db.QueryRow(
		"SELECT updated_at, record_json FROM pr_cache WHERE repo = ? AND number = ?",
		repo, number).Scan(&updatedAt, &recordJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec prr.Record
	if err := json.Unmarshal([]byte(recordJSON), &rec); err != nil {
		return nil, err
	}
	return &Entry{UpdatedAt: updatedAt, Record: &rec}, nil
}

func (c *SQLiteCache) Put(repo string, number int, updatedAt, headOid string, record *prr.Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(
		`INSERT INTO pr_cache (repo, number, updated_at, head_oid, record_json)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(repo, number) DO UPDATE SET
			updated_at = excluded.updated_at,
			head_oid = excluded.head_oid,
			record_json = excluded.record_json`,
		repo, number, updatedAt, headOid, string(data))
	return err
}

func (c *SQLiteCache) GetMergedCount(repo, author string) (int, time.Time, bool, error) {
	var count int
	var fetchedAt string
	err := c.db.QueryRow(
		"SELECT count, fetched_at FROM merged_count WHERE repo = ? AND author = ?",
		repo, author).Scan(&count, &fetchedAt)
	if err == sql.ErrNoRows {
		return 0, time.Time{}, false, nil
	}
	if err != nil {
		return 0, time.Time{}, false, err
	}
	t, err := time.Parse(time.RFC3339, fetchedAt)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	return count, t, true, nil
}

func (c *SQLiteCache) PutMergedCount(repo, author string, count int, fetchedAt time.Time) error {
	_, err := c.db.Exec(
		`INSERT INTO merged_count (repo, author, count, fetched_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(repo, author) DO UPDATE SET
			count = excluded.count,
			fetched_at = excluded.fetched_at`,
		repo, author, count, fetchedAt.UTC().Format(time.RFC3339))
	return err
}

func (c *SQLiteCache) Close() error { return c.db.Close() }

// mcEntry is a cached merged-count value with its fetch time.
type mcEntry struct {
	count     int
	fetchedAt time.Time
}

// Memory is an in-memory Cache for tests and --no-cache runs. Its methods are
// called from Sync's fetch workers, so all access is mutex-guarded.
type Memory struct {
	mu      sync.Mutex
	fp      string
	entries map[[2]any]*Entry
	merged  map[[2]any]mcEntry
}

// NewMemory returns an empty in-memory cache.
func NewMemory() *Memory {
	return &Memory{entries: map[[2]any]*Entry{}, merged: map[[2]any]mcEntry{}}
}

func (m *Memory) EnsureFingerprint(fingerprint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fp != fingerprint {
		m.entries = map[[2]any]*Entry{}
		m.fp = fingerprint
	}
	return nil
}

func (m *Memory) Get(repo string, number int) (*Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[[2]any{repo, number}], nil
}

func (m *Memory) Put(repo string, number int, updatedAt, headOid string, record *prr.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[[2]any{repo, number}] = &Entry{UpdatedAt: updatedAt, Record: record}
	return nil
}

func (m *Memory) GetMergedCount(repo, author string) (int, time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.merged[[2]any{repo, author}]
	return e.count, e.fetchedAt, ok, nil
}

func (m *Memory) PutMergedCount(repo, author string, count int, fetchedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.merged[[2]any{repo, author}] = mcEntry{count: count, fetchedAt: fetchedAt}
	return nil
}

func (m *Memory) Close() error { return nil }
