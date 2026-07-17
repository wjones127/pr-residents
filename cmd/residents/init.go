package main

import (
	"bufio"
	"embed"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/wjones127/pr-residents/internal/config"
	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/secrets"
)

//go:embed assets/escalation.yml assets/comment-vocab.md
var assets embed.FS

// initAnswers is the config the wizard collects; kept separate from rendering so
// the YAML builder is unit-testable without a terminal.
type initAnswers struct {
	GithubLogin string
	Repos       []string
	Interests   []string
}

func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	configDir := fs.String("config-dir", config.DefaultDir(), "config directory to scaffold")
	force := fs.Bool("force", false, "overwrite an existing config.yml")
	fs.Parse(args)

	dir := *configDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "residents: create %s: %v\n", dir, err)
		return 1
	}
	// Policy files are materialized once and never clobbered — they're yours to tune.
	if err := materializeAssets(dir); err != nil {
		fmt.Fprintf(os.Stderr, "residents: %v\n", err)
		return 1
	}

	cfgPath := filepath.Join(dir, "config.yml")
	if _, err := os.Stat(cfgPath); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "residents: %s already exists (use --force to overwrite)\n", cfgPath)
		return 1
	}

	// No TTY (cron/CI): scaffold a skeleton and stop. Env vars carry the tokens.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		if err := os.WriteFile(cfgPath, []byte(skeletonConfig), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "residents: write %s: %v\n", cfgPath, err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "[ok] wrote config skeleton to %s\n", cfgPath)
		fmt.Fprintf(os.Stderr, "     edit it, set GITHUB_TOKEN_<ORG> env vars, then `residents serve`\n")
		return 0
	}

	return runWizard(dir, cfgPath)
}

func runWizard(dir, cfgPath string) int {
	in := bufio.NewReader(os.Stdin)
	fmt.Println("PR Residents setup — runs entirely under your own GitHub identity.")
	fmt.Println()

	ans := initAnswers{}
	ans.Repos = parseRepos(prompt(in, "Repos you review (owner/name, space-separated)", ""))
	for len(ans.Repos) == 0 {
		fmt.Println("  need at least one repo, e.g. lance-format/lance")
		ans.Repos = parseRepos(prompt(in, "Repos", ""))
	}
	ans.GithubLogin = strings.TrimSpace(prompt(in, "Your GitHub username", ""))
	ans.Interests = parseRepos(prompt(in, "Interest paths for cold-start relevance (optional, space-separated)", ""))

	fmt.Println()
	orgs := orgsOf(ans.Repos)
	fmt.Printf("Tokens — one read-only, fine-grained PAT per org (%s).\n", strings.Join(orgs, ", "))
	fmt.Println("Scopes: Contents:Read, Pull requests:Read, Metadata:Read. Nothing writable.")
	fmt.Println("Leave blank to skip an org (you can set GITHUB_TOKEN_<ORG> in the env instead).")
	for _, org := range orgs {
		collectToken(in, dir, org, ans.GithubLogin)
	}

	fmt.Println()
	if path, err := exec.LookPath("claude"); err == nil {
		fmt.Printf("✓ agent engine: found `claude` at %s (uses your subscription)\n", path)
	} else {
		fmt.Println("✗ agent engine: `claude` not on PATH. Install Claude Code and log in before Dispatch.")
	}

	if err := os.WriteFile(cfgPath, []byte(renderConfigYAML(ans)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "residents: write %s: %v\n", cfgPath, err)
		return 1
	}
	fmt.Println()
	fmt.Printf("[ok] wrote %s\n", cfgPath)
	fmt.Println("Next: residents serve --open")
	return 0
}

// collectToken prompts for one org's token, validates it live against the API,
// and stores it. It re-prompts on a bad token; an empty entry skips the org.
// It reports whether a token was actually stored.
func collectToken(in *bufio.Reader, dir, org, login string) bool {
	for {
		fmt.Printf("  %s token: ", org)
		tok := readSecret(in)
		if tok == "" {
			fmt.Printf("  … skipped %s\n", org)
			return false
		}
		who, err := gh.NewClient(tok).ViewerLogin()
		if err != nil {
			fmt.Printf("  ✗ token rejected: %v — try again\n", err)
			continue
		}
		if login != "" && !strings.EqualFold(who, login) {
			fmt.Printf("  ! token authenticates as %q, not %q — storing anyway\n", who, login)
		}
		src, err := secrets.Store(org, tok, dir)
		if err != nil {
			fmt.Printf("  ✗ could not store token: %v\n", err)
			return false
		}
		fmt.Printf("  ✓ %s validated as %s, saved to %s\n", org, who, src)
		return true
	}
}

func materializeAssets(dir string) error {
	for _, name := range []string{"escalation.yml", "comment-vocab.md"} {
		dst := filepath.Join(dir, name)
		if _, err := os.Stat(dst); err == nil {
			continue // never clobber a tuned policy file
		}
		data, err := assets.ReadFile("assets/" + name)
		if err != nil {
			return fmt.Errorf("read bundled %s: %w", name, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
	}
	return nil
}

func prompt(in *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// readSecret reads a token without echoing it when stdin is a real terminal,
// falling back to a plain read otherwise (so piped input still works).
func readSecret(in *bufio.Reader) string {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Println()
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	line, _ := in.ReadString('\n')
	return strings.TrimSpace(line)
}

// parseRepos splits a whitespace/comma-separated list into trimmed, deduped items.
func parseRepos(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// orgsOf returns the distinct owners of owner/name repos, in stable order.
func orgsOf(repos []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range repos {
		owner := r
		if i := strings.IndexByte(r, '/'); i > 0 {
			owner = r[:i]
		}
		if owner == "" || seen[owner] {
			continue
		}
		seen[owner] = true
		out = append(out, owner)
	}
	sort.Strings(out)
	return out
}

func yamlList(indent string, items []string) string {
	if len(items) == 0 {
		return " []"
	}
	var b strings.Builder
	b.WriteByte('\n')
	for _, it := range items {
		b.WriteString(indent + "- " + it + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderConfigYAML(a initAnswers) string {
	return fmt.Sprintf(`# PR Residents — your personal config. Safe to read or share: no secrets here.
# Tokens live in your OS keychain (see the README's Security section).

github_login: %s

repos:%s

# Path prefixes for cold-start relevance, until your review history accrues.
interests:%s

# Subset of your repos you actively review. Empty = all of them.
subscribed_repos: []

# Paths never surfaced for review (glob, matched against changed files).
exclude_paths:
  - "java/**"

dispatch:
  engine: claude        # claude | codex(planned) | copilot(planned)
  concurrency: 6        # parallel resident agents; 4–8 is a sane range
  model_routing:        # match model to lane/size; retune when pricing shifts
    default: sonnet
    fresh_xl: opus      # large core-library PRs get the strongest model
    fresh_xs: haiku     # trivial version-bump / trailing-comma PRs go cheap
    re_review: sonnet

server:
  port: 8787
`, a.GithubLogin, yamlList("  ", a.Repos), yamlList("  ", a.Interests))
}

const skeletonConfig = `# PR Residents — your personal config. Fill in, then run: residents serve
# Secrets are NOT stored here. Set GITHUB_TOKEN_<ORG> env vars, or run
# ` + "`residents init`" + ` in a terminal to save tokens to your OS keychain.

github_login: your-github-login

repos:
  - owner/repo

interests: []
subscribed_repos: []
exclude_paths:
  - "java/**"

dispatch:
  engine: claude
  concurrency: 6
  model_routing:
    default: sonnet
    fresh_xl: opus
    fresh_xs: haiku
    re_review: sonnet

server:
  port: 8787
`
