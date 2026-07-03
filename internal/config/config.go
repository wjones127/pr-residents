// Package config loads pr-residents config and resolves per-org tokens.
//
// Config is a single personal config.yml plus a shared escalation.yml policy
// file, read from a directory (default ~/.pr-residents). Secrets are never in
// config: tokens resolve via the secrets package (env → keychain → 0600 file).
package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lancedb/pr-residents/internal/prr"
	"github.com/lancedb/pr-residents/internal/secrets"
)

// Dispatch holds review-dispatch settings: which agent engine, how many run in
// parallel, and the lane/size → model routing table.
type Dispatch struct {
	Engine       string            `yaml:"engine"`
	Concurrency  int               `yaml:"concurrency"`
	ModelRouting map[string]string `yaml:"model_routing"`
}

// Config is the resolved pr-residents configuration.
type Config struct {
	Dir             string // the config directory; token file fallback lives under it
	GithubLogin     string
	Repos           []string
	ExcludePaths    []string
	Escalation      prr.EscalationRules
	TokenPrefix     string
	SubscribedRepos []string
	Interests       []string
	Dispatch        Dispatch
	Port            int
}

// ActiveRepos is the repos to sync: the subscribed subset if any, else all.
func (c *Config) ActiveRepos() []string {
	if len(c.SubscribedRepos) == 0 {
		return c.Repos
	}
	sub := make(map[string]bool, len(c.SubscribedRepos))
	for _, r := range c.SubscribedRepos {
		sub[r] = true
	}
	var out []string
	for _, r := range c.Repos {
		if sub[r] {
			out = append(out, r)
		}
	}
	return out
}

func orgEnvVar(owner, prefix string) string {
	return prefix + "_" + strings.ReplaceAll(strings.ToUpper(owner), "-", "_")
}

// TokenFor returns the token for an org owner, or "" if unset. It resolves via
// the secrets package: env var → OS keychain → 0600 file under the config dir.
func (c *Config) TokenFor(owner string) string {
	tok, _ := secrets.Resolve(c.EnvVarFor(owner), owner, c.Dir)
	return tok
}

// EnvVarFor returns the env var name a token for owner is read from.
func (c *Config) EnvVarFor(owner string) string {
	return orgEnvVar(owner, c.TokenPrefix)
}

// configFile is the single per-user config.yml. Shared escalation policy lives
// in a sibling escalation.yml (loaded separately).
type configFile struct {
	GithubLogin     string   `yaml:"github_login"`
	Repos           []string `yaml:"repos"`
	ExcludePaths    []string `yaml:"exclude_paths"`
	SubscribedRepos []string `yaml:"subscribed_repos"`
	Interests       []string `yaml:"interests"`
	Dispatch        Dispatch `yaml:"dispatch"`
	Server          struct {
		Port int `yaml:"port"`
	} `yaml:"server"`
	Env struct {
		GithubTokenPrefix string `yaml:"github_token_prefix"`
	} `yaml:"env"`
}

// loadYAML reads and parses a YAML file into out. A missing file is not an
// error (out is left as-is), mirroring the Python `load_file(...) or {}`.
func loadYAML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return yaml.Unmarshal(data, out)
}

// DefaultDir is the config directory when none is given: ~/.pr-residents.
func DefaultDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".pr-residents")
	}
	return ".pr-residents"
}

// Exists reports whether a config.yml is present in configDir.
func Exists(configDir string) bool {
	_, err := os.Stat(filepath.Join(configDir, "config.yml"))
	return err == nil
}

// Load reads config.yml and the shared escalation.yml from configDir. Missing
// files are tolerated (empty config), so headless callers can rely on env.
func Load(configDir string) (*Config, error) {
	var cf configFile
	if err := loadYAML(filepath.Join(configDir, "config.yml"), &cf); err != nil {
		return nil, err
	}
	var escalation prr.EscalationRules
	if err := loadYAML(filepath.Join(configDir, "escalation.yml"), &escalation); err != nil {
		return nil, err
	}

	tokenPrefix := cf.Env.GithubTokenPrefix
	if tokenPrefix == "" {
		tokenPrefix = "GITHUB_TOKEN"
	}

	dispatch := cf.Dispatch
	if dispatch.Engine == "" {
		dispatch.Engine = "claude"
	}
	if dispatch.Concurrency <= 0 {
		dispatch.Concurrency = 6
	}

	port := cf.Server.Port
	if port <= 0 {
		port = 8787
	}

	return &Config{
		Dir:             configDir,
		GithubLogin:     cf.GithubLogin,
		Repos:           cf.Repos,
		ExcludePaths:    cf.ExcludePaths,
		Escalation:      escalation,
		TokenPrefix:     tokenPrefix,
		SubscribedRepos: cf.SubscribedRepos,
		Interests:       cf.Interests,
		Dispatch:        dispatch,
		Port:            port,
	}, nil
}
