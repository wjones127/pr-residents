package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/wjones127/pr-residents/internal/config"
	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/secrets"
)

const tokenUsage = `residents token — inspect and update per-org GitHub tokens

Usage:
  residents token list           check every configured org's token (live)
  residents token set <org>      validate and store a token for one org
  residents token rm <org>       remove a stored token for one org

Tokens resolve env → keychain → 0600 file; env always wins. 'set'/'rm' only
touch the keychain/file — never config.yml, never the environment.
`

func runToken(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, tokenUsage)
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("token "+sub, flag.ExitOnError)
	configDir := fs.String("config-dir", config.DefaultDir(), "config directory (config.yml)")
	fs.Parse(args[1:])
	rest := fs.Args()

	cfg, err := config.Load(*configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "residents: load config: %v\n", err)
		return 1
	}

	switch sub {
	case "list", "ls":
		return runTokenList(cfg)
	case "set":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "usage: residents token set <org>")
			return 2
		}
		return runTokenSet(cfg, rest[0])
	case "rm", "remove", "delete":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "usage: residents token rm <org>")
			return 2
		}
		return runTokenRm(cfg, rest[0])
	case "-h", "--help", "help":
		fmt.Print(tokenUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "residents token: unknown subcommand %q\n\n%s", sub, tokenUsage)
		return 2
	}
}

// runTokenList resolves and live-validates the token for every org derived from
// the configured repos, reporting where each resolves from and whether it works.
func runTokenList(cfg *config.Config) int {
	orgs := orgsOf(cfg.Repos)
	if len(orgs) == 0 {
		fmt.Println("no repos configured — nothing to check (run `residents init`)")
		return 0
	}
	allOK := true
	for _, org := range orgs {
		envVar := cfg.EnvVarFor(org)
		tok, src := secrets.Resolve(envVar, org, cfg.Dir)
		if tok == "" {
			allOK = false
			fmt.Printf("  %-24s ✗ no token — set $%s or `residents token set %s`\n", org, envVar, org)
			continue
		}
		who, err := gh.NewClient(tok).ViewerLogin()
		if err != nil {
			allOK = false
			fmt.Printf("  %-24s ✗ token from %s rejected: %v\n", org, src, err)
			continue
		}
		fmt.Printf("  %-24s ✓ %s (via %s)\n", org, who, src)
	}
	if !allOK {
		return 1
	}
	return 0
}

// runTokenSet validates a pasted token and stores it for org, leaving config.yml
// and the environment untouched.
func runTokenSet(cfg *config.Config, org string) int {
	org = strings.TrimSpace(org)
	if org == "" {
		fmt.Fprintln(os.Stderr, "usage: residents token set <org>")
		return 2
	}
	if v := strings.TrimSpace(os.Getenv(cfg.EnvVarFor(org))); v != "" {
		fmt.Printf("note: $%s is set and overrides stored tokens; storage below won't take effect until you unset it\n", cfg.EnvVarFor(org))
	}
	in := bufio.NewReader(os.Stdin)
	fmt.Printf("Read-only GitHub token for %q (input hidden):\n", org)
	if !collectToken(in, cfg.Dir, org, cfg.GithubLogin) {
		return 1
	}
	return 0
}

// runTokenRm deletes any stored token for org from the keychain and file
// fallback, and warns if an env override would still shadow the removal.
func runTokenRm(cfg *config.Config, org string) int {
	org = strings.TrimSpace(org)
	if org == "" {
		fmt.Fprintln(os.Stderr, "usage: residents token rm <org>")
		return 2
	}
	if err := secrets.Delete(org, cfg.Dir); err != nil {
		fmt.Fprintf(os.Stderr, "residents: remove token for %s: %v\n", org, err)
		return 1
	}
	fmt.Printf("[ok] removed stored token for %s (keychain + file)\n", org)
	if v := strings.TrimSpace(os.Getenv(cfg.EnvVarFor(org))); v != "" {
		fmt.Printf("note: $%s is still set and will keep providing a token until you unset it\n", cfg.EnvVarFor(org))
	}
	return 0
}
