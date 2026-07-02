// Package cache is the pr-detail cache, keyed on (repo, number). It lets a
// re-run skip the heavy detail query for PRs whose updatedAt has not changed
// since the last sync. State lives under the store's cache/ namespace.
package cache

import (
	"database/sql"
	"encoding/json"

	_ "modernc.org/sqlite" // pure-Go sqlite driver

	"github.com/lancedb/pr-residents/internal/prr"
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

func (c *SQLiteCache) Close() error { return c.db.Close() }

// Memory is an in-memory Cache for tests and --no-cache runs.
type Memory struct {
	fp      string
	entries map[[2]any]*Entry
}

// NewMemory returns an empty in-memory cache.
func NewMemory() *Memory {
	return &Memory{entries: map[[2]any]*Entry{}}
}

func (m *Memory) EnsureFingerprint(fingerprint string) error {
	if m.fp != fingerprint {
		m.entries = map[[2]any]*Entry{}
		m.fp = fingerprint
	}
	return nil
}

func (m *Memory) Get(repo string, number int) (*Entry, error) {
	return m.entries[[2]any{repo, number}], nil
}

func (m *Memory) Put(repo string, number int, updatedAt, headOid string, record *prr.Record) error {
	m.entries[[2]any{repo, number}] = &Entry{UpdatedAt: updatedAt, Record: record}
	return nil
}

func (m *Memory) Close() error { return nil }
