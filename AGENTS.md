# AGENTS.md

A local HTTP proxy between a coding CLI (Claude Code, OpenAI Codex) and its model
provider. It routes each fresh user turn to the cheapest model that can do it, and
refuses switches that cost more in discarded prompt cache than they save.

## Non-negotiables

- **Standard library only.** `go.mod` has zero `require` entries and must keep them.
  If a task seems to need a dependency, say so instead of adding one.
- **Every request/response body decode uses `json.Decoder` with `UseNumber()`.**
  A plain decode stores numbers as `float64`, whose 53-bit mantissa silently
  truncates large integers: `12345678901234567890` comes back as
  `...567000`. The user never sees it; the provider gets a different request.
- **Routing is fail-open.** Any error, timeout, or unparseable answer means "keep the
  current model", never "block the prompt". No code path returns an error to the CLI
  because routing failed.
- **Never log prompt text, API keys, or the `Authorization` header.** `log.Debug`
  takes tier names, token counts, reasons and timings. `log.Dump` writes whole bodies
  on purpose and is off by default.
- **macOS and Linux only.** No Windows code paths, no `PATHEXT`, no `.cmd` handling.

## Architecture

Dependency direction, which must not invert:

```
cost  →  (nothing)
config → cost
policy → config, cost
proxy  → (stdlib only; generic, knows no provider)
proxy/claude, proxy/codex → the pure packages, never each other
```

`cost` and `policy` hold every decision and touch no IO. They are the parts worth
testing hardest and can be tested without a network or a process.

`internal/cost`'s break-even table in `cost_test.go` is the fixture, not an
illustration. If the code and the table disagree, one of them is a bug and the test
says which. The same nine numbers appear in `README.md`; keep them in sync.

## The two handlers

`proxy/claude` and `proxy/codex` were built from one shape. Six behaviours are
carried by hand between them — model-list filtering by `AvailableTiers()`, ignoring
`Usage.Complete == false`, copying `Outcome.Target`, copying `Metrics` pointers
without dereferencing, passing the real (possibly empty) pinned tier to
`policy.Decide`, and LRU conversation eviction.

**Changing one handler's copy of those means changing the other.** They have already
drifted into bugs twice. Genuinely divergent parts — wire format, capability
stripping, catalog rewriting — should stay separate.

## Testing

A test that cannot fail when the protection is removed is not protecting anything.
Before trusting a test that guards an invariant, break the production code, watch the
test fail, and restore it. Several tests in this repo passed against deliberately
broken code until someone checked.

Unit tests are not enough here. A build once passed 127 green tests while routing
nothing at all, because the CLI appends a trailing `role: "system"` message that the
turn detector rejected. Run the thing:

```
go build -o llm-router . && LLMR_DEBUG=1 ./llm-router claude -p "what is 2+2?"
cat "${TMPDIR:-/tmp}/llm-router/"*.json    # the recorded decision
```

Before committing: `gofmt -l . && go vet ./... && go test ./... -v`.
For the proxy and handler packages, also `go test ./internal/proxy/... -race`.

## Do not rename

- `jev-router` — the sentinel model id. The CLI echoes it back verbatim, and matching
  it is how the proxy tells "route this" from "the user picked a model".
- `<jev-explain>` in `skills/llmr-explain/SKILL.md` — the Claude handler matches that
  literal string to skip routing the turn that asks for the report.
- The `LLMR_*` variables' `JEV_*` fallbacks, which keep an existing
  `~/.jev-router.env` working. `LLMR_JEV_MODEL` is the one exception: it is new, has
  no legacy name, and reads `os.Getenv` directly.

## Comments and commits

Comments explain *why*, never *what*. First move is a better name. A third-party
limitation workaround carries a link to the limitation.

Commits: Conventional Commits, one line, 120 characters max, signed (`-S`). No
attribution trailer, no `Co-Authored-By`, no generated-with line, anywhere.

## Provenance

A Go rewrite of and derivative work from
[gargpratyush/jev-router](https://github.com/gargpratyush/jev-router) (MIT). Keep both
copyright lines in `LICENSE`.
