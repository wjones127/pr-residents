package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestResolveEnvWins(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	if _, err := Store("lance-format", "keychain-tok", dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_TOKEN_LANCE_FORMAT", "env-tok")

	tok, src := Resolve("GITHUB_TOKEN_LANCE_FORMAT", "lance-format", dir)
	if tok != "env-tok" || src != SourceEnv {
		t.Errorf("env should win: %q from %q", tok, src)
	}
}

func TestResolveKeychain(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	if _, err := Store("lancedb", "kc", dir); err != nil {
		t.Fatal(err)
	}
	tok, src := Resolve("UNSET_VAR", "lancedb", dir)
	if tok != "kc" || src != SourceKeychain {
		t.Errorf("keychain: %q from %q", tok, src)
	}
}

func TestResolveFileFallback(t *testing.T) {
	keyring.MockInitWithError(keyring.ErrUnsupportedPlatform) // force file path
	dir := t.TempDir()
	src, err := Store("acme", "file-tok", dir)
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceFile {
		t.Fatalf("store should fall back to file, got %q", src)
	}
	// The file must be 0600.
	info, err := os.Stat(filepath.Join(dir, "tokens", "acme"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file perm = %o, want 600", perm)
	}
	tok, resSrc := Resolve("UNSET_VAR", "acme", dir)
	if tok != "file-tok" || resSrc != SourceFile {
		t.Errorf("file fallback: %q from %q", tok, resSrc)
	}
}

func TestResolveNone(t *testing.T) {
	keyring.MockInit()
	if tok, src := Resolve("UNSET_VAR", "nobody", t.TempDir()); tok != "" || src != SourceNone {
		t.Errorf("missing token should be empty/none: %q %q", tok, src)
	}
}

func TestDelete(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	if _, err := Store("gone", "x", dir); err != nil {
		t.Fatal(err)
	}
	if err := Delete("gone", dir); err != nil {
		t.Fatal(err)
	}
	if tok, _ := Resolve("UNSET_VAR", "gone", dir); tok != "" {
		t.Errorf("token should be gone, got %q", tok)
	}
}
