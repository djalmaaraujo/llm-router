package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/explain"
	"github.com/djalmaaraujo/llm-router/internal/launch"
	"github.com/djalmaaraujo/llm-router/internal/state"
	"github.com/djalmaaraujo/llm-router/internal/statusline"
)

var version = "dev"

func main() { os.Exit(run(os.Args)) }

// run dispatches on argv[0] first, so the symlinked names work, then on the
// first argument.
func run(argv []string) int {
	config.LoadEnvFiles()
	name := filepath.Base(argv[0])
	args := argv[1:]

	switch {
	case name == "llmr-claude":
		return launch.Claude(args)
	case name == "llmr-codex":
		return launch.Codex(args)
	case name == "llmr-explain":
		return runExplain(args)
	}

	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "claude":
		return launch.Claude(args[1:])
	case "codex":
		return launch.Codex(args[1:])
	case "explain":
		return runExplain(args[1:])
	case "statusline":
		in, _ := io.ReadAll(os.Stdin)
		fmt.Println(statusline.Render(in))
		return 0
	case "install":
		return launch.Install()
	case "version", "--version", "-v":
		fmt.Println(version)
		return 0
	default:
		usage()
		return 2
	}
}

func runExplain(args []string) int {
	id := ""
	if len(args) > 0 {
		id = args[0]
	} else {
		id = config.Env("STATUS_ID")
	}
	fmt.Println(explain.Render(state.Read(id)))
	return 0
}

func usage() {
	fmt.Fprint(os.Stderr, `llm-router - route each turn to the cheapest model that can do it

  llmr-claude [args]    run Claude Code with routing   (llm-router claude)
  llmr-codex [args]     run Codex with routing         (llm-router codex)
  llmr-explain [id]     why the last turn was routed   (llm-router explain)
  llm-router install    register the explanation skill
  llm-router version
`)
}
