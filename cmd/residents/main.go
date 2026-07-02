// Command residents is the PR-residents local app: it assembles daily PR-review
// rounds and drafts reviews with an agent, entirely on your machine. See the
// repo README for the workflow.
//
// This is the Phase-0 skeleton: the deterministic pipeline is being ported from
// the Python skills. `refresh` is wired; serve/dispatch are stubs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/lancedb/pr-residents/internal/agent"
	"github.com/lancedb/pr-residents/internal/cache"
	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/gh"
	"github.com/lancedb/pr-residents/internal/pipeline"
	"github.com/lancedb/pr-residents/internal/prr"
	"github.com/lancedb/pr-residents/internal/relevance"
	"github.com/lancedb/pr-residents/internal/store"
	"github.com/lancedb/pr-residents/internal/web"
)

const usage = `residents — local PR-review rounds

Usage:
  residents <command> [flags]

Commands:
  serve      run the local web app (rounds view + Refresh button)
  refresh    run the deterministic pipeline once (fetch + derive PRRecords)
  dispatch   run review workups over the fresh/re-review PRs (uses your agent CLI)
  init       scaffold ~/.pr-residents/ (not yet implemented)

Run 'residents <command> -h' for command-specific flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "refresh":
		os.Exit(runRefresh(os.Args[2:]))
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "dispatch":
		os.Exit(runDispatch(os.Args[2:]))
	case "init":
		fmt.Fprintf(os.Stderr, "residents: %q not yet implemented\n", os.Args[1])
		os.Exit(1)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "residents: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func runRefresh(args []string) int {
	fs := flag.NewFlagSet("refresh", flag.ExitOnError)
	configDir := fs.String("config-dir", "config", "directory holding repos.yml / escalation.yml / user.yml")
	stateDir := fs.String("state-dir", "state", "directory for the cache/ledger state tree")
	out := fs.String("out", "", "output path for records JSON; '-' for stdout; empty writes the state cache")
	fs.Parse(args)

	cfg, err := config.Load(*configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "residents: load config: %v\n", err)
		return 1
	}

	st := store.New(*stateDir)
	dbPath, err := st.LocalPath(store.PRDetailDBKey, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "residents: cache path: %v\n", err)
		return 1
	}
	c, err := cache.OpenSQLite(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "residents: open cache: %v\n", err)
		return 1
	}

	newClient := func(token string) pipeline.API { return gh.NewClient(token) }
	records, warns := pipeline.Sync(cfg, newClient, c, time.Now().UTC())
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, w)
	}
	if records == nil {
		records = []*prr.Record{}
	}

	payload, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "residents: marshal records: %v\n", err)
		return 1
	}

	switch *out {
	case "-":
		fmt.Println(string(payload))
	case "":
		if err := st.PutJSON(store.RecordsKey, records); err != nil {
			fmt.Fprintf(os.Stderr, "residents: write records: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "[ok] wrote %d records to %s\n", len(records), store.RecordsKey)
	default:
		if err := os.WriteFile(*out, payload, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "residents: write %s: %v\n", *out, err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "[ok] wrote %d records to %s\n", len(records), *out)
	}

	// Triage panel: self-requested relevance candidates (deterministic, no LLM).
	newRel := func(token string) relevance.API { return gh.NewClient(token) }
	panel, pwarns := relevance.BuildPanel(cfg, newRel, st, relevance.Options{})
	for _, warn := range pwarns {
		fmt.Fprintln(os.Stderr, warn)
	}
	if panel == nil {
		panel = []relevance.Candidate{}
	}
	if err := st.PutJSON(store.PanelKey, panel); err != nil {
		fmt.Fprintf(os.Stderr, "residents: write panel: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "[ok] wrote %d triage candidates to %s\n", len(panel), store.PanelKey)
	}
	return 0
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configDir := fs.String("config-dir", "config", "directory holding repos.yml / escalation.yml / user.yml")
	stateDir := fs.String("state-dir", "state", "directory for the cache/ledger state tree")
	port := fs.Int("port", 8787, "port to bind on localhost")
	fs.Parse(args)

	cfg, err := config.Load(*configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "residents: load config: %v\n", err)
		return 1
	}
	srv, err := web.NewServer(store.New(*stateDir), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "residents: init server: %v\n", err)
		return 1
	}

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	fmt.Fprintf(os.Stderr, "residents: serving http://%s\n", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		fmt.Fprintf(os.Stderr, "residents: serve: %v\n", err)
		return 1
	}
	return 0
}

func runDispatch(args []string) int {
	fs := flag.NewFlagSet("dispatch", flag.ExitOnError)
	configDir := fs.String("config-dir", "config", "directory holding repos.yml / escalation.yml / user.yml")
	stateDir := fs.String("state-dir", "state", "directory for the cache/ledger state tree")
	fs.Parse(args)

	cfg, err := config.Load(*configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "residents: load config: %v\n", err)
		return 1
	}
	st := store.New(*stateDir)
	var records []*prr.Record
	if found, err := st.GetJSON(store.RecordsKey, &records); err != nil || !found {
		fmt.Fprintf(os.Stderr, "residents: no records at %s — run `residents refresh` first\n", store.RecordsKey)
		return 1
	}

	// Ctrl-C cancels: in-flight reviews stop, completed SOAPs are kept.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ag := agent.NewClaudeAgent()
	newFetcher := func(token string) agent.Fetcher { return gh.NewClient(token) }
	progress := func(ev agent.DispatchEvent) {
		fmt.Fprintf(os.Stderr, "[%d/%d] %s#%d %s %s (tok %d/%d)\n",
			ev.Done, ev.Total, ev.Repo, ev.Number, ev.Phase, ev.Message, ev.TokensIn, ev.TokensOut)
	}

	res := agent.Dispatch(ctx, cfg, st, ag, newFetcher, records, time.Now().UTC(), progress)
	fmt.Fprintf(os.Stderr, "[done] reviewed %d · cached %d · failed %d · skipped %d · tokens %d in / %d out\n",
		res.Reviewed, res.Cached, res.Failed, res.Skipped, res.TokensIn, res.TokensOut)
	return 0
}
