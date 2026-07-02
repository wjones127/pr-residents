// Package config loads pr-residents config and resolves per-org tokens.
//
// Nothing user-specific is hardcoded: config is read by path. Tokens live only
// in the environment (GITHUB_TOKEN_<ORG>); config holds at most the var-name
// prefix. This mirrors the Python config.py contract.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lancedb/pr-residents/internal/prr"
)

// Config is the resolved pr-residents configuration.
type Config struct {
	Repos           []string
	ExcludePaths    []string
	Escalation      prr.EscalationRules
	TokenPrefix     string
	SubscribedRepos []string
	Interests       []string
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

// TokenFor returns the token for an org owner, or "" if unset.
func (c *Config) TokenFor(owner string) string {
	return os.Getenv(orgEnvVar(owner, c.TokenPrefix))
}

// EnvVarFor returns the env var name a token for owner is read from.
func (c *Config) EnvVarFor(owner string) string {
	return orgEnvVar(owner, c.TokenPrefix)
}

type reposFile struct {
	Repos        []string `yaml:"repos"`
	ExcludePaths []string `yaml:"exclude_paths"`
	SelfLogin    string   `yaml:"self_login"`
}

type userFile struct {
	GithubLogin     string   `yaml:"github_login"`
	SubscribedRepos []string `yaml:"subscribed_repos"`
	Interests       []string `yaml:"interests"`
	Env             struct {
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

// Load reads repos.yml, escalation.yml, and the optional user.yml from configDir.
func Load(configDir string) (*Config, error) {
	var repos reposFile
	if err := loadYAML(filepath.Join(configDir, "repos.yml"), &repos); err != nil {
		return nil, err
	}
	var escalation prr.EscalationRules
	if err := loadYAML(filepath.Join(configDir, "escalation.yml"), &escalation); err != nil {
		return nil, err
	}

	tokenPrefix := "GITHUB_TOKEN"
	var user userFile
	if err := loadYAML(filepath.Join(configDir, "user.yml"), &user); err != nil {
		return nil, err
	}
	if user.Env.GithubTokenPrefix != "" {
		tokenPrefix = user.Env.GithubTokenPrefix
	}

	return &Config{
		Repos:           repos.Repos,
		ExcludePaths:    repos.ExcludePaths,
		Escalation:      escalation,
		TokenPrefix:     tokenPrefix,
		SubscribedRepos: user.SubscribedRepos,
		Interests:       user.Interests,
	}, nil
}
