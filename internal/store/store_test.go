package store

import (
	"reflect"
	"strings"
	"testing"
)

func TestKeyValidation(t *testing.T) {
	s := New("/tmp/does-not-matter")
	for _, bad := range []string{"records.json", "state/x.json", "foo/bar", "/cache/x", `cache\x`} {
		if _, err := s.LocalPath(bad, false); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
	if _, err := s.LocalPath("cache/../ledger/x.json", false); err == nil {
		t.Error("expected parent traversal to be rejected")
	}
	if p, err := s.LocalPath("cache/records.json", false); err != nil || !strings.HasSuffix(p, "records.json") {
		t.Errorf("cache key: %q err=%v", p, err)
	}
	if _, err := s.LocalPath("ledger/reconcile/agreement.json", false); err != nil {
		t.Errorf("ledger key rejected: %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	s := New(t.TempDir())

	var got map[string]string
	if found, _ := s.GetJSON("cache/records.json", &got); found {
		t.Error("missing key should not be found")
	}

	if err := s.PutJSON("cache/workups/o/n/7/abc.json", map[string]string{"soap": "ok"}); err != nil {
		t.Fatal(err)
	}
	var soap map[string]string
	found, err := s.GetJSON("cache/workups/o/n/7/abc.json", &soap)
	if err != nil || !found || soap["soap"] != "ok" {
		t.Errorf("round trip: found=%v soap=%+v err=%v", found, soap, err)
	}

	if s.Exists("ledger/reconcile/agreement.json") {
		t.Error("should not exist yet")
	}
	if err := s.PutJSON("ledger/reconcile/agreement.json", map[string]any{"domains": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if !s.Exists("ledger/reconcile/agreement.json") {
		t.Error("should exist after put")
	}

	// Corrupt file is tolerated (reads as not-found).
	if err := s.PutText("cache/records.json", "{not json"); err != nil {
		t.Fatal(err)
	}
	var corrupt any
	if found, _ := s.GetJSON("cache/records.json", &corrupt); found {
		t.Error("corrupt file should read as not found")
	}

	if err := s.PutJSON("cache/panel.json", []any{}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Delete("cache/panel.json"); !ok {
		t.Error("first delete should report true")
	}
	if ok, _ := s.Delete("cache/panel.json"); ok {
		t.Error("second delete should report false")
	}

	if p, _ := s.LocalPath("cache/pr_detail.sqlite", false); !strings.HasPrefix(p, s.Root) {
		t.Errorf("local path %q not under root %q", p, s.Root)
	}
}

func TestListKeys(t *testing.T) {
	s := New(t.TempDir())
	if keys, _ := s.ListKeys("ledger/dispositions"); len(keys) != 0 {
		t.Errorf("empty prefix should return nothing, got %+v", keys)
	}

	must(t, s.PutJSON("ledger/dispositions/2026-06-22.json", map[string]any{}))
	must(t, s.PutJSON("ledger/dispositions/2026-06-23.json", map[string]any{}))
	must(t, s.PutJSON("cache/records.json", []any{})) // different namespace, excluded
	keys, _ := s.ListKeys("ledger/dispositions")
	want := []string{"ledger/dispositions/2026-06-22.json", "ledger/dispositions/2026-06-23.json"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("list recursive+sorted: %+v want %+v", keys, want)
	}

	must(t, s.PutJSON("cache/workups/o/n/7/aaa.json", map[string]any{}))
	must(t, s.PutJSON("cache/workups/o/n/8/bbb.json", map[string]any{}))
	if keys, _ := s.ListKeys("cache/workups"); len(keys) != 2 {
		t.Errorf("workups nested: %+v", keys)
	}
}

func TestArtifactKeys(t *testing.T) {
	for _, k := range []string{RecordsKey, PanelKey, RelevanceProfileKey, PRDetailDBKey} {
		if !strings.HasPrefix(k, "cache/") {
			t.Errorf("%q should be in cache/", k)
		}
	}
	for _, k := range []string{ReconcileAgreementKey, DispositionsPrefix} {
		if !strings.HasPrefix(k, "ledger/") {
			t.Errorf("%q should be in ledger/", k)
		}
	}

	k := DispositionKey("2026-06-23T06:00:00Z")
	if !strings.HasPrefix(k, "ledger/dispositions/") || !strings.HasSuffix(k, ".json") || strings.Contains(k, ":") {
		t.Errorf("disposition key not sanitized: %q", k)
	}

	if got := WorkupKey("owner/name", 7416, "deadbeef"); got != "cache/workups/owner/name/7416/deadbeef.json" {
		t.Errorf("workup key: %q", got)
	}
	if WorkupKey("o/n", 1, "sha-a") == WorkupKey("o/n", 1, "sha-b") {
		t.Error("workup key should be distinct per sha")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
