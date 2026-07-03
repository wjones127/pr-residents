// Package store is the persistence seam. Callers go through it; they never open
// state files directly. Two namespaces live under the state dir and the split
// is load-bearing:
//
//	cache/   100% reconstructable from GitHub (records, relevance profile, the
//	         PR-detail SQLite, workup SOAPs). Best-effort and prunable.
//	ledger/  drafts + the accumulated learning series. Exists nowhere else, so
//	         it is durable: written once per cycle, never pruned.
//
// Keys are POSIX-style and MUST start with cache/ or ledger/. That prefix is how
// the backend decides what to prune vs keep, and the seam is what lets the
// backend swap to another store later without touching a caller.
package store

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	nsCache  = "cache"
	nsLedger = "ledger"
)

// Artifact key registry: the one place that knows the on-disk layout.
const (
	RecordsKey          = "cache/records.json"
	PanelKey            = "cache/panel.json"
	RelevanceProfileKey = "cache/relevance_profile.json"
	PRDetailDBKey       = "cache/pr_detail.sqlite"

	DispositionsPrefix    = "ledger/dispositions"
	ReconcileAgreementKey = "ledger/reconcile/agreement.json"
)

func validateKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, `\`) {
		return fmt.Errorf("bad store key: %q", key)
	}
	head := key
	if i := strings.IndexByte(key, '/'); i >= 0 {
		head = key[:i]
	}
	if head != nsCache && head != nsLedger {
		return fmt.Errorf("store key must start with cache/ or ledger/: %q", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == ".." {
			return fmt.Errorf("store key may not contain '..': %q", key)
		}
	}
	return nil
}

// FileStore is a filesystem-backed store rooted at a state dir.
type FileStore struct {
	Root string
}

// New returns a FileStore rooted at root.
func New(root string) *FileStore {
	return &FileStore{Root: root}
}

// LocalPath materializes a real filesystem path for key. Needed by callers that
// can't take bytes (e.g. sqlite). With makeParents it creates the parent dir.
func (s *FileStore) LocalPath(key string, makeParents bool) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	path := filepath.Join(append([]string{s.Root}, strings.Split(key, "/")...)...)
	if makeParents {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
	}
	return path, nil
}

// Exists reports whether key is present (false on a bad key).
func (s *FileStore) Exists(key string) bool {
	path, err := s.LocalPath(key, false)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// GetJSON decodes key into v. found=false when the file is missing or corrupt
// (mirroring the Python producer, which tolerates a corrupt cache file).
func (s *FileStore) GetJSON(key string, v any) (found bool, err error) {
	path, err := s.LocalPath(key, false)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, nil
	}
	return true, nil
}

// PutJSON writes v as indented JSON, creating parents.
func (s *FileStore) PutJSON(key string, v any) error {
	path, err := s.LocalPath(key, true)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// GetText reads key as text; found=false when missing.
func (s *FileStore) GetText(key string) (text string, found bool, err error) {
	path, err := s.LocalPath(key, false)
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

// PutText writes text to key, creating parents.
func (s *FileStore) PutText(key, text string) error {
	path, err := s.LocalPath(key, true)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// Delete removes key, reporting whether it existed.
func (s *FileStore) Delete(key string) (bool, error) {
	path, err := s.LocalPath(key, false)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

// ListKeys returns all file keys under prefix, POSIX-style, sorted.
func (s *FileStore) ListKeys(prefix string) ([]string, error) {
	if err := validateKey(strings.TrimRight(prefix, "/") + "/x"); err != nil {
		return nil, err
	}
	base := filepath.Join(append([]string{s.Root}, strings.Split(prefix, "/")...)...)
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		return []string{}, nil
	}
	var out []string
	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.Root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// safeSegment makes a string safe as a single path segment.
func safeSegment(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// DispositionKey is the ledger key for a cycle's drafted dispositions.
func DispositionKey(cycle string) string {
	return DispositionsPrefix + "/" + safeSegment(cycle) + ".json"
}

// WorkupKey is the workup-cache key: a SOAP is valid only for the exact head
// SHA it was written against (a force-push invalidates it).
func WorkupKey(repo string, number int, sha string) string {
	owner, name := repo, ""
	if i := strings.IndexByte(repo, '/'); i >= 0 {
		owner, name = repo[:i], repo[i+1:]
	}
	return "cache/workups/" + safeSegment(owner) + "/" + safeSegment(name) + "/" +
		strconv.Itoa(number) + "/" + safeSegment(sha) + ".json"
}

// EmbeddingKey is the cache key for an embedding vector, scoped by model name.
func EmbeddingKey(model, contentHash string) string {
	return "cache/embeddings/" + safeSegment(model) + "/" + safeSegment(contentHash) + ".json"
}
