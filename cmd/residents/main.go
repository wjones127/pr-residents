// Command residents is the PR-residents local app: it assembles daily PR-review
// rounds and drafts reviews with an agent, entirely on your machine. See the
// repo README for the workflow.
//
// This is the Phase-0 skeleton: the deterministic pipeline is being ported from
// the Python skills; the serve/refresh/dispatch verbs are wired incrementally.
package main

import (
	"fmt"
	"os"
)

const usage = `residents — local PR-review rounds

Usage:
  residents <command>

Commands:
  init       scaffold ~/.pr-residents/ and bundled skills
  serve      run the local web app
  refresh    run the deterministic pipeline once (headless)
  dispatch   run a review round (headless)

Run 'residents <command> -h' for command-specific flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init", "serve", "refresh", "dispatch":
		fmt.Fprintf(os.Stderr, "residents: %q not yet implemented (Phase 0 in progress)\n", os.Args[1])
		os.Exit(1)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "residents: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
