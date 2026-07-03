// Command residents is the PR-residents local app: it assembles daily PR-review
// rounds and drafts reviews with an agent, entirely on your machine. See the
// repo README for the workflow.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
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
  init       set up ~/.pr-residents/ (interactive; repos, tokens, engine)
  serve      run the local web app (rounds view + Refresh/Dispatch)
  refresh    run the deterministic pipeline once (fetch + derive PRRecords)
  dispatch   run review workups over the fresh/re-review PRs (uses your agent CLI)

Run 'residents <command> -h' for command-specific flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		os.Exit(runInit(os.Args[2:]))
	case "refresh":
		os.Exit(runRefresh(os.Args[2:]))
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "dispatch":
		os.Exit(runDispatch(os.Args[2:]))
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "residents: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func runRefresh(args []string) int {
	fs := flag.NewFlagSet("refresh", flag.ExitOnError)
	configDir := fs.String("config-dir", config.DefaultDir(), "config directory (config.yml / escalation.yml)")
	stateDir := fs.String("state-dir", "", "state tree directory (default: <config-dir>/state)")
	out := fs.String("out", "", "output path for records JSON; '-' for stdout; empty writes the state cache")
	rebuildRelevance := fs.Bool("rebuild-relevance", false, "rebuild the review-history relevance profile from the API")
	fs.Parse(args)
	*stateDir = stateDirOf(*configDir, *stateDir)

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
	panel, pwarns := relevance.BuildPanel(cfg, newRel, st, relevance.Options{Rebuild: *rebuildRelevance})
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
	configDir := fs.String("config-dir", config.DefaultDir(), "config directory (config.yml / escalation.yml)")
	stateDir := fs.String("state-dir", "", "state tree directory (default: <config-dir>/state)")
	port := fs.Int("port", 0, "port to bind on localhost (default: config server.port, else 8787)")
	open := fs.Bool("open", false, "open the web UI in your browser once it's listening")
	fs.Parse(args)
	*stateDir = stateDirOf(*configDir, *stateDir)

	if !config.Exists(*configDir) {
		fmt.Fprintf(os.Stderr, "residents: no config at %s\n", filepath.Join(*configDir, "config.yml"))
		fmt.Fprintf(os.Stderr, "  run `residents init` to set up (repos, tokens, engine).\n")
		return 1
	}

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

	bindPort := *port
	if bindPort == 0 {
		bindPort = cfg.Port
	}
	addr := fmt.Sprintf("127.0.0.1:%d", bindPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "residents: bind %s: %v\n", addr, err)
		return 1
	}
	url := "http://" + addr
	fmt.Fprintf(os.Stderr, "residents: serving %s\n", url)
	if *open {
		openBrowser(url)
	}
	if err := http.Serve(ln, srv.Handler()); err != nil {
		fmt.Fprintf(os.Stderr, "residents: serve: %v\n", err)
		return 1
	}
	return 0
}

func runDispatch(args []string) int {
	fs := flag.NewFlagSet("dispatch", flag.ExitOnError)
	configDir := fs.String("config-dir", config.DefaultDir(), "config directory (config.yml / escalation.yml)")
	stateDir := fs.String("state-dir", "", "state tree directory (default: <config-dir>/state)")
	fs.Parse(args)
	*stateDir = stateDirOf(*configDir, *stateDir)

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

// stateDirOf resolves the state tree: the flag if given, else <config-dir>/state,
// so all state lives under one directory by default.
func stateDirOf(configDir, flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return filepath.Join(configDir, "state")
}

// openBrowser best-effort opens url in the default browser; failures are silent
// since the URL is already printed.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
