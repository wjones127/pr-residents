package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

func writeConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("config.yml", `
github_login: wjones127
repos:
  - lance-format/lance
  - lancedb/lancedb
exclude_paths:
  - "java/**"
subscribed_repos:
  - lancedb/lancedb
interests:
  - rust/lance-index
env:
  github_token_prefix: GITHUB_TOKEN
server:
  port: 9191
`)
	write("escalation.yml", `
path_rules:
  - id: public-api
    reason: "Public API surface"
    any_path_matches:
      - "**/*.proto"
size_rules:
  - id: large-blast-radius
    reason: "Large blast radius (XL diff)"
    min_total_lines: 1000
`)
	return dir
}

func TestLoad(t *testing.T) {
	cfg, err := Load(writeConfigDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 2 || cfg.Repos[0] != "lance-format/lance" {
		t.Errorf("repos: %+v", cfg.Repos)
	}
	if len(cfg.ExcludePaths) != 1 || cfg.ExcludePaths[0] != "java/**" {
		t.Errorf("exclude_paths: %+v", cfg.ExcludePaths)
	}
	if len(cfg.Escalation.PathRules) != 1 || cfg.Escalation.PathRules[0].ID != "public-api" {
		t.Errorf("escalation path rules: %+v", cfg.Escalation.PathRules)
	}
	if len(cfg.Escalation.SizeRules) != 1 || cfg.Escalation.SizeRules[0].MinTotalLines != 1000 {
		t.Errorf("escalation size rules: %+v", cfg.Escalation.SizeRules)
	}
	if len(cfg.Interests) != 1 || cfg.Interests[0] != "rust/lance-index" {
		t.Errorf("interests: %+v", cfg.Interests)
	}
	if cfg.GithubLogin != "wjones127" {
		t.Errorf("github_login: %q", cfg.GithubLogin)
	}
	if cfg.Port != 9191 {
		t.Errorf("server port: %d", cfg.Port)
	}
}

func TestActiveRepos(t *testing.T) {
	cfg := &Config{
		Repos:           []string{"lance-format/lance", "lancedb/lancedb"},
		SubscribedRepos: []string{"lancedb/lancedb"},
	}
	got := cfg.ActiveRepos()
	if len(got) != 1 || got[0] != "lancedb/lancedb" {
		t.Errorf("subscribed subset: %+v", got)
	}

	cfg.SubscribedRepos = nil
	if got := cfg.ActiveRepos(); len(got) != 2 {
		t.Errorf("empty subscribed = all: %+v", got)
	}
}

func TestTokenResolution(t *testing.T) {
	keyring.MockInit() // hermetic: never touch the real OS keychain
	cfg := &Config{TokenPrefix: "GITHUB_TOKEN", Dir: t.TempDir()}
	if got := cfg.EnvVarFor("lance-format"); got != "GITHUB_TOKEN_LANCE_FORMAT" {
		t.Errorf("env var name: %q", got)
	}
	t.Setenv("GITHUB_TOKEN_LANCE_FORMAT", "tok123")
	if got := cfg.TokenFor("lance-format"); got != "tok123" {
		t.Errorf("token: %q", got)
	}
	if got := cfg.TokenFor("unset-org"); got != "" {
		t.Errorf("unset token should be empty, got %q", got)
	}
}

func TestLoadMissingConfigFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("repos: [a/b]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TokenPrefix != "GITHUB_TOKEN" {
		t.Errorf("default token prefix: %q", cfg.TokenPrefix)
	}
	if cfg.Port != 8787 {
		t.Errorf("default port: %d", cfg.Port)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0] != "a/b" {
		t.Errorf("repos: %+v", cfg.Repos)
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir) {
		t.Error("empty dir should not be configured")
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("repos: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir) {
		t.Error("dir with config.yml should be configured")
	}
}
