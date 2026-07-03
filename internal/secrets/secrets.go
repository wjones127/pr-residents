// Package secrets resolves and stores the only secret pr-residents holds: a
// per-org, read-only GitHub token. Resolution order is env → OS keychain →
// 0600 file, so a cron/headless override (env) always wins and the keychain is
// the default the wizard writes to. Nothing here ever touches config.yml.
package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

// keychainService is the service name every token is filed under; the account
// key is the org (owner) slug.
const keychainService = "pr-residents"

// Source records where a resolved token came from, for diagnostics and wizard
// feedback. Empty means not found.
type Source string

const (
	SourceEnv      Source = "env"
	SourceKeychain Source = "keychain"
	SourceFile     Source = "file"
	SourceNone     Source = ""
)

// account normalizes an org owner into a stable keychain account / file name.
func account(org string) string { return strings.ToLower(strings.TrimSpace(org)) }

// Resolve returns the token for org, trying envVar first (so a cron override on
// disk-free machines wins), then the keychain, then the 0600 file under dir.
// The second return says where it was found; SourceNone means no token.
func Resolve(envVar, org, dir string) (string, Source) {
	if envVar != "" {
		if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
			return v, SourceEnv
		}
	}
	if tok, err := keyring.Get(keychainService, account(org)); err == nil && tok != "" {
		return tok, SourceKeychain
	}
	if tok, ok := readFileToken(dir, org); ok {
		return tok, SourceFile
	}
	return "", SourceNone
}

// Store writes token to the OS keychain; if the keychain is unavailable (e.g.
// headless Linux with no Secret Service) it falls back to a 0600 file under
// dir. It returns where the token landed.
func Store(org, token, dir string) (Source, error) {
	if err := keyring.Set(keychainService, account(org), token); err == nil {
		return SourceKeychain, nil
	}
	if err := writeFileToken(dir, org, token); err != nil {
		return SourceNone, err
	}
	return SourceFile, nil
}

// Delete removes a stored token from both the keychain and the file fallback.
// A not-found in either place is not an error.
func Delete(org, dir string) error {
	if err := keyring.Delete(keychainService, account(org)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		// keychain unavailable is fine; the file fallback may still hold it.
	}
	path := tokenFile(dir, org)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func tokenFile(dir, org string) string {
	return filepath.Join(dir, "tokens", account(org))
}

func readFileToken(dir, org string) (string, bool) {
	data, err := os.ReadFile(tokenFile(dir, org))
	if err != nil {
		return "", false
	}
	tok := strings.TrimSpace(string(data))
	return tok, tok != ""
}

func writeFileToken(dir, org, token string) error {
	tokDir := filepath.Join(dir, "tokens")
	if err := os.MkdirAll(tokDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(tokenFile(dir, org), []byte(token+"\n"), 0o600)
}
