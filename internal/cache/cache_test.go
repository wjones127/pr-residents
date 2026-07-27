package cache

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wjones127/pr-residents/internal/prr"
)

func TestSQLiteMergedCountRoundTrip(t *testing.T) {
	c, err := OpenSQLite(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if _, _, ok, err := c.GetMergedCount("o/r", "alice"); err != nil || ok {
		t.Fatalf("miss expected, got ok=%v err=%v", ok, err)
	}

	at := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	if err := c.PutMergedCount("o/r", "alice", 7, at); err != nil {
		t.Fatal(err)
	}
	count, fetchedAt, ok, err := c.GetMergedCount("o/r", "alice")
	if err != nil || !ok {
		t.Fatalf("hit expected, got ok=%v err=%v", ok, err)
	}
	if count != 7 || !fetchedAt.Equal(at) {
		t.Errorf("round-trip: count=%d fetchedAt=%v want 7 / %v", count, fetchedAt, at)
	}

	// Upsert overwrites count and timestamp.
	later := at.Add(48 * time.Hour)
	if err := c.PutMergedCount("o/r", "alice", 9, later); err != nil {
		t.Fatal(err)
	}
	if count, fetchedAt, _, _ := c.GetMergedCount("o/r", "alice"); count != 9 || !fetchedAt.Equal(later) {
		t.Errorf("upsert: count=%d fetchedAt=%v want 9 / %v", count, fetchedAt, later)
	}
}

// The merged-count table is raw GitHub data, independent of the record
// fingerprint, so a fingerprint change (which wipes pr_cache) must not drop it.
func TestSQLiteMergedCountSurvivesFingerprintChange(t *testing.T) {
	c, err := OpenSQLite(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.EnsureFingerprint("fp1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("o/r", 1, "u1", "h1", &prr.Record{Repo: "o/r", Number: 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMergedCount("o/r", "alice", 7, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	if err := c.EnsureFingerprint("fp2"); err != nil {
		t.Fatal(err)
	}
	// pr_cache is wiped...
	if entry, _ := c.Get("o/r", 1); entry != nil {
		t.Errorf("pr_cache should be wiped on fingerprint change, got %+v", entry)
	}
	// ...but the merged-count survives.
	if count, _, ok, _ := c.GetMergedCount("o/r", "alice"); !ok || count != 7 {
		t.Errorf("merged-count should survive fingerprint change, got ok=%v count=%d", ok, count)
	}
}
