# llm-router Go Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Node jev-router with one Go binary that routes Claude Code and Codex turns, fixes prompt-cache loss on model switches, and installs from Homebrew with no runtime dependency.

**Architecture:** A local HTTP proxy sits between the CLI and its provider. Pure packages (`cost`, `policy`) make every decision with no IO; the proxy packages only move bytes and call them. One binary dispatches on `argv[0]` so three symlinked commands share one artifact.

**Tech Stack:** Go 1.26, standard library only (`net/http`, `encoding/json`, `os/exec`). No third-party dependencies. GoReleaser for release.

**Spec:** `docs/superpowers/specs/2026-09-21-llm-router-go-design.md`

## Global Constraints

- Go 1.26. **Standard library only** — `go.mod` must list zero `require` entries. If a task seems to need a dependency, stop and ask.
- Every JSON decode of a request or response body uses `json.Decoder` with `UseNumber()` set. A plain decode turns `max_tokens: 1000000` into `1e+06` on the way out and silently corrupts requests.
- Routing is fail-open. Any error, timeout, or unparseable answer means "keep the current model", never "block the prompt". No code path returns an error to the CLI because routing failed.
- Never log prompt text, API keys, or authorization headers. `LLMR_DEBUG` logs decisions and token counts only.
- macOS and Linux. No Windows code paths, no `.cmd` handling, no `PATHEXT`.
- Module path: `github.com/djalmaaraujo/llm-router`.
- All comments in English. Add a comment only where the *why* cannot come from a better name — never restate the code.
- Commit with `git commit -S` (signed). Conventional Commits, one line, 120 characters max. No attribution trailers, no `Co-Authored-By`.
- Work in `/Users/cooper/dev/llm-router`.
- The sentinel model id stays the literal string `jev-router`. It is a wire value Claude Code echoes back; renaming it breaks resumed sessions.

## File Structure

| File | Responsibility |
| --- | --- |
| `main.go` | `argv[0]` dispatch and subcommands |
| `internal/config/config.go` | tiers, prices, thresholds, sentinel |
| `internal/config/env.go` | env files and variable aliases |
| `internal/cost/cost.go` | break-even arithmetic; pure, no imports |
| `internal/policy/policy.go` | `Decide`; pure |
| `internal/router/router.go` | `Router` interface and shared types |
| `internal/router/jev/jev.go` | `POST /v1/systemone` |
| `internal/state/state.go` | per-session decisions under the temp dir |
| `internal/explain/explain.go` | the report |
| `internal/statusline/statusline.go` | the status line |
| `internal/proxy/proxy.go` | listener, upstream forwarding, SSE usage tap |
| `internal/proxy/claude/body.go` | request-body reading and rewriting |
| `internal/proxy/claude/claude.go` | the routing handler |
| `internal/proxy/codex/body.go` | Responses API body handling |
| `internal/proxy/codex/codex.go` | the Codex routing handler |
| `internal/launch/launch.go` | PATH lookup, env assembly, spawn |
| `internal/launch/settings.go` | Claude Code settings save and restore |
| `skills/llmr-explain/SKILL.md` | the bundled skill |

Tasks 1-7 build the pure core and its tools. Tasks 8-12 deliver a working `llmr-claude`. Tasks 13-14 add Codex. Tasks 15-18 cover packaging, parity, and the verification that gates the release.

---

### Task 1: Repository scaffold and the config package

**Files:**
- Create: `go.mod`, `.gitignore`, `internal/config/config.go`, `internal/log/log.go`
- Test: `internal/config/config_test.go`, `internal/log/log_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Tier` struct; `config.Tiers []Tier`; `config.TierNames() []string`; `config.RankOf(name string) int`; `config.IDOf(name string) string`; `config.Spec(name string) (Tier, bool)`; `config.TierOf(model string) string`; `config.AvailableTiers() []string`; `config.AutoModel` constant; `config.IsAuto(model string) bool`; `config.Thresholds` var; `config.Env(name string) string`; `log.Path() string`; `log.Debug(format string, args ...any)`; `log.Dump(body []byte)`.

- [ ] **Step 1: Create the module and ignore file**

```bash
cd /Users/cooper/dev/llm-router
go mod init github.com/djalmaaraujo/llm-router
printf 'dist/\nllm-router\n*.test\n' > .gitignore
```

- [ ] **Step 2: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import "testing"

func TestTierOfMatchesVersionsWithinAFamily(t *testing.T) {
	cases := map[string]string{
		"claude-haiku-4-5-20251001": "haiku",
		"claude-sonnet-4-6":         "sonnet",
		"claude-opus-5":             "opus",
		"claude-fable-5-1":          "fable",
		"gpt-5.6-luna":              "",
		"jev-router":                "",
	}
	for model, want := range cases {
		if got := TierOf(model); got != want {
			t.Errorf("TierOf(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestRankOfOrdersTiersCheapestFirst(t *testing.T) {
	if RankOf("haiku") >= RankOf("sonnet") || RankOf("sonnet") >= RankOf("opus") || RankOf("opus") >= RankOf("fable") {
		t.Fatal("tiers are not ordered cheapest first")
	}
	if RankOf("nope") != -1 {
		t.Error("an unknown tier must rank -1")
	}
}

func TestFableIsOptInOnly(t *testing.T) {
	t.Setenv("LLMR_ALLOW_FABLE", "")
	t.Setenv("JEV_ALLOW_FABLE", "")
	for _, name := range AvailableTiers() {
		if name == "fable" {
			t.Fatal("fable must not be available without an opt-in")
		}
	}
	t.Setenv("LLMR_ALLOW_FABLE", "1")
	found := false
	for _, name := range AvailableTiers() {
		if name == "fable" {
			found = true
		}
	}
	if !found {
		t.Error("LLMR_ALLOW_FABLE=1 must make fable available")
	}
}

func TestPricesMatchTheSpec(t *testing.T) {
	want := map[string][3]float64{
		"haiku":  {1, 5, 0.1},
		"sonnet": {2, 10, 0.1},
		"opus":   {5, 25, 0.1},
		"fable":  {10, 50, 0.025},
	}
	for _, tier := range Tiers {
		w := want[tier.Name]
		if tier.CostIn != w[0] || tier.CostOut != w[1] || tier.CacheRead != w[2] {
			t.Errorf("%s prices = %v/%v/%v, want %v", tier.Name, tier.CostIn, tier.CostOut, tier.CacheRead, w)
		}
	}
}
```

- [ ] **Step 3: Run the test and watch it fail**

Run: `go test ./internal/config/`
Expected: FAIL, `undefined: TierOf`.

- [ ] **Step 4: Write the implementation**

Create `internal/config/config.go`:

```go
// Package config holds every routing knob in one place, so the whole policy is
// reviewable without reading the proxy.
package config

import (
	"os"
	"strconv"
	"strings"
)

// Tier is one model tier. Prices are US dollars per million tokens. CacheRead is
// the multiplier applied to CostIn for tokens served from the prompt cache;
// fable reads at $0.25 per million, which is 0.025x rather than the usual 0.1x.
type Tier struct {
	Name      string
	ID        string
	Family    string
	Thinking  bool
	Effort    bool
	CostIn    float64
	CostOut   float64
	CacheRead float64
}

// Tiers are ordered cheapest first. Family is the substring used to recognise an
// older version within the same tier, such as claude-sonnet-4-6.
var Tiers = []Tier{
	{"haiku", "claude-haiku-4-5-20251001", "haiku", false, false, 1, 5, 0.1},
	{"sonnet", "claude-sonnet-5", "sonnet", true, true, 2, 10, 0.1},
	{"opus", "claude-opus-5", "opus", true, true, 5, 25, 0.1},
	{"fable", "claude-fable-5-1", "fable", true, true, 10, 50, 0.025},
}

// CacheWrite1h is the multiplier on CostIn for writing the cache. Claude Code
// uses the one-hour TTL, which costs 2x rather than the five-minute 1.25x.
const CacheWrite1h = 2.0

// AutoModel is the sentinel offered as an extra row in Claude Code's /model
// picker. Claude Code sends it verbatim behind a custom base URL, so seeing it
// in a request is an exact signal that the turn should be routed.
const AutoModel = "jev-router"

func IsAuto(model string) bool { return model == AutoModel }

func TierNames() []string {
	names := make([]string, len(Tiers))
	for i, tier := range Tiers {
		names[i] = tier.Name
	}
	return names
}

func RankOf(name string) int {
	for i, tier := range Tiers {
		if tier.Name == name {
			return i
		}
	}
	return -1
}

func Spec(name string) (Tier, bool) {
	for _, tier := range Tiers {
		if tier.Name == name {
			return tier, true
		}
	}
	return Tier{}, false
}

func IDOf(name string) string {
	tier, ok := Spec(name)
	if !ok {
		return ""
	}
	return tier.ID
}

// TierOf names the tier for a model string, or "" when it is not one of ours.
func TierOf(model string) string {
	for _, tier := range Tiers {
		if strings.Contains(model, tier.Family) {
			return tier.Name
		}
	}
	return ""
}

// AvailableTiers lists what the account can run. Fable bills extra usage
// credits, so it is opt-in; everything else is covered by a subscription.
func AvailableTiers() []string {
	allowFable := Env("ALLOW_FABLE") == "1"
	var names []string
	for _, tier := range Tiers {
		if tier.Name == "fable" && !allowFable {
			continue
		}
		names = append(names, tier.Name)
	}
	return names
}

type thresholds struct {
	MinConfidence           float64
	UncertainCeiling        string
	SwitchHorizonTurns      int
	SubAgentHorizonTurns    int
	BigContextTokens        int
	BigContextMinConfidence float64
	AssumedOutputTokens     int
	JevTimeoutMS            int
	JevDeadlineMS           int
	JevMaxRetries           int
}

// Thresholds are the tuning knobs. SwitchHorizonTurns is how many more turns a
// conversation is assumed to keep reusing its cached prefix; a short horizon
// under-switches and keeps the cache, which is the safer way to be wrong.
var Thresholds = thresholds{
	MinConfidence:           0.3,
	UncertainCeiling:        "sonnet",
	SwitchHorizonTurns:      envInt("SWITCH_HORIZON", 5),
	SubAgentHorizonTurns:    envInt("SUBAGENT_HORIZON", 2),
	BigContextTokens:        200_000,
	BigContextMinConfidence: 0.6,
	AssumedOutputTokens:     2000,
	JevTimeoutMS:            1500,
	JevDeadlineMS:           3000,
	JevMaxRetries:           1,
}

func envInt(name string, fallback int) int {
	if n, err := strconv.Atoi(Env(name)); err == nil && n > 0 {
		return n
	}
	return fallback
}

// Env reads LLMR_<name>, falling back to the legacy JEV_<name> so an existing
// setup keeps working after the rename.
func Env(name string) string {
	if v := os.Getenv("LLMR_" + name); v != "" {
		return v
	}
	return os.Getenv("JEV_" + name)
}
```

- [ ] **Step 5: Run the test and watch it pass**

Run: `go test ./internal/config/ -v`
Expected: PASS, four tests.

- [ ] **Step 6: Write the logger test**

Create `internal/log/log_test.go`:

```go
package log

import (
	"os"
	"testing"
)

func TestDebugIsSilentUnlessAskedFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LLMR_DEBUG", "")
	t.Setenv("JEV_DEBUG", "")
	Debug("routed %s", "haiku")
	if _, err := os.Stat(Path()); err == nil {
		t.Error("no log file may appear when debugging is off")
	}
}

func TestDebugWritesAnOwnerOnlyFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LLMR_DEBUG", "1")
	Debug("routed %s", "haiku")
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatalf("no log file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	body, _ := os.ReadFile(Path())
	if len(body) == 0 {
		t.Error("the line was not written")
	}
}
```

- [ ] **Step 7: Write the logger**

Create `internal/log/log.go`:

- `Path()` is `~/.llm-router.log`. Resolve the home directory on every call so `HOME` in a test takes effect.
- `Debug(format, args...)` appends `<RFC3339 timestamp> <message>\n`, opening with `os.O_APPEND|os.O_CREATE|os.O_WRONLY` and mode `0o600`, only when `config.Env("DEBUG")` is non-empty. Swallow every error: a broken log file must never take down a session.
- `Dump(body []byte)` writes `body` to `<config.Env("DUMP")>.<unix millis>.json` when `DUMP` is set, and does nothing otherwise. Claude Code's request shape is undocumented and moves; this is how a future wire change gets diagnosed.
- **Never pass prompt text, an API key, or an authorization header to `Debug`.** Decisions, tier names, token counts, and timings only. `Dump` writes whole bodies on purpose, which is why it is off by default.

- [ ] **Step 8: Run the logger tests**

Run: `go test ./internal/log/ -v`
Expected: PASS, two tests.

- [ ] **Step 9: Commit**

```bash
git add go.mod .gitignore internal/config/ internal/log/
git commit -S -m "feat: add the config package with tier prices, thresholds and a debug log"
```

---

### Task 2: The cost package

This is the cache fix. It is pure arithmetic with no imports, and the break-even table from the spec is its test fixture.

**Files:**
- Create: `internal/cost/cost.go`
- Test: `internal/cost/cost_test.go`

**Interfaces:**
- Consumes: nothing. This package imports nothing, not even `config`.
- Produces: `cost.Rates` struct with fields `Read`, `Write`, `Out` (all `float64`, dollars per million tokens); `cost.BreakEvenTurns(from, to Rates, cachedTokens, outputTokens int) (float64, bool)`; `cost.SwitchPaysOff(from, to Rates, cachedTokens, outputTokens, horizon int) bool`.

- [ ] **Step 1: Write the failing test**

Create `internal/cost/cost_test.go`:

```go
package cost

import (
	"math"
	"testing"
)

var (
	haiku  = Rates{Read: 0.1, Write: 2.0, Out: 5}
	sonnet = Rates{Read: 0.2, Write: 4.0, Out: 10}
	opus   = Rates{Read: 0.5, Write: 10.0, Out: 25}
	fable  = Rates{Read: 0.25, Write: 20.0, Out: 50}
)

// The table from the design doc. Read down a column: which pair of tiers is
// involved matters more than how big the conversation is.
func TestBreakEvenTableFromTheSpec(t *testing.T) {
	cases := []struct {
		name   string
		from   Rates
		to     Rates
		cached int
		want   float64
	}{
		{"opus->haiku 80k", opus, haiku, 80_000, 2.1},
		{"opus->haiku 100k", opus, haiku, 100_000, 2.4},
		{"opus->haiku 500k", opus, haiku, 500_000, 4.0},
		{"opus->sonnet 80k", opus, sonnet, 80_000, 5.6},
		{"opus->sonnet 100k", opus, sonnet, 100_000, 6.3},
		{"opus->sonnet 500k", opus, sonnet, 500_000, 10.6},
		{"sonnet->haiku 80k", sonnet, haiku, 80_000, 8.4},
		{"sonnet->haiku 100k", sonnet, haiku, 100_000, 9.5},
		{"sonnet->haiku 500k", sonnet, haiku, 500_000, 15.8},
	}
	for _, c := range cases {
		got, ok := BreakEvenTurns(c.from, c.to, c.cached, 2000)
		if !ok {
			t.Errorf("%s: switching never pays off, want %.1f turns", c.name, c.want)
			continue
		}
		if math.Abs(got-c.want) > 0.05 {
			t.Errorf("%s: break-even = %.2f turns, want %.1f", c.name, got, c.want)
		}
	}
}

func TestHorizonFiveAllowsOpusToHaikuAndHoldsTheRest(t *testing.T) {
	cases := []struct {
		name string
		from Rates
		to   Rates
		want bool
	}{
		{"opus->haiku", opus, haiku, true},
		{"opus->sonnet", opus, sonnet, false},
		{"sonnet->haiku", sonnet, haiku, false},
	}
	for _, c := range cases {
		if got := SwitchPaysOff(c.from, c.to, 100_000, 2000, 5); got != c.want {
			t.Errorf("%s at 100k horizon 5 = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAnUpgradeNeverPaysOff(t *testing.T) {
	if _, ok := BreakEvenTurns(haiku, opus, 100_000, 2000); ok {
		t.Error("moving to a more expensive tier must report that it never pays off")
	}
	if SwitchPaysOff(haiku, opus, 100_000, 2000, 1000) {
		t.Error("no horizon makes an upgrade pay for itself on cost alone")
	}
}

func TestFableReadsAtTheCheaperRate(t *testing.T) {
	// Fable reads at 0.025x, so leaving it saves far less than its headline
	// price suggests. Compare against a hypothetical fable priced at 0.1x.
	expensiveReads := Rates{Read: 1.0, Write: 20.0, Out: 50}
	cheap, _ := BreakEvenTurns(fable, haiku, 100_000, 2000)
	dear, _ := BreakEvenTurns(expensiveReads, haiku, 100_000, 2000)
	if !(cheap > dear) {
		t.Errorf("cheap fable reads must make leaving it slower to pay off: %.2f vs %.2f", cheap, dear)
	}
}

func TestZeroContextAlwaysSwitches(t *testing.T) {
	if !SwitchPaysOff(opus, haiku, 0, 2000, 1) {
		t.Error("with nothing cached there is no rebuild to pay for")
	}
}

func TestHorizonZeroNeverSwitches(t *testing.T) {
	if SwitchPaysOff(opus, haiku, 100_000, 2000, 0) {
		t.Error("a zero-turn horizon cannot repay any rebuild")
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/cost/`
Expected: FAIL, `undefined: Rates`.

- [ ] **Step 3: Write the implementation**

Create `internal/cost/cost.go`:

```go
// Package cost answers one question: does changing models save more than
// rebuilding the prompt cache costs? Anthropic keys the cache on the model, so
// any switch re-pays cache creation for the whole conversation prefix.
//
// It imports nothing on purpose. Every input is a number the caller measured.
package cost

// Rates are US dollars per million tokens for one tier, with the cache
// multipliers already applied.
type Rates struct {
	Read  float64 // cached input tokens
	Write float64 // input tokens written to the cache
	Out   float64 // output tokens
}

// BreakEvenTurns reports how many turns a switch needs before it has paid for
// the cache rebuild, and whether it ever does.
//
//	stay(h)   = h * (cached*from.Read + out*from.Out)
//	switch(h) = cached*to.Write + out*to.Out + (h-1)*(cached*to.Read + out*to.Out)
//
// ok is false when running on `to` costs at least as much per turn as staying,
// which is every upgrade: no horizon repays it, and the decision belongs to
// capability rather than to cost.
func BreakEvenTurns(from, to Rates, cachedTokens, outputTokens int) (float64, bool) {
	cached := float64(cachedTokens) / 1e6
	out := float64(outputTokens) / 1e6

	stay := cached*from.Read + out*from.Out
	first := cached*to.Write + out*to.Out
	later := cached*to.Read + out*to.Out

	if stay <= later {
		return 0, false
	}
	return (first - later) / (stay - later), true
}

// SwitchPaysOff reports whether the switch repays itself within horizon turns.
func SwitchPaysOff(from, to Rates, cachedTokens, outputTokens, horizon int) bool {
	turns, ok := BreakEvenTurns(from, to, cachedTokens, outputTokens)
	return ok && turns <= float64(horizon)
}
```

- [ ] **Step 4: Run the test and watch it pass**

Run: `go test ./internal/cost/ -v`
Expected: PASS, six tests.

- [ ] **Step 5: Commit**

```bash
git add internal/cost/
git commit -S -m "feat: add break-even arithmetic for prompt-cache rebuilds"
```

---

### Task 3: Wire prices into rates

**Files:**
- Modify: `internal/config/config.go` (append)
- Test: `internal/config/rates_test.go`

**Interfaces:**
- Consumes: `config.Tier`, `config.CacheWrite1h`, `cost.Rates`.
- Produces: `config.RatesFor(name string) cost.Rates`.

- [ ] **Step 1: Write the failing test**

Create `internal/config/rates_test.go`:

```go
package config

import "testing"

func TestRatesForAppliesTheCacheMultipliers(t *testing.T) {
	opus := RatesFor("opus")
	if opus.Read != 0.5 || opus.Write != 10 || opus.Out != 25 {
		t.Errorf("opus rates = %+v, want read 0.5 write 10 out 25", opus)
	}
	fable := RatesFor("fable")
	if fable.Read != 0.25 {
		t.Errorf("fable read = %v, want 0.25 from the 0.025x multiplier", fable.Read)
	}
	if got := RatesFor("nope"); got.Read != 0 || got.Write != 0 || got.Out != 0 {
		t.Errorf("an unknown tier must return the zero value, got %+v", got)
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/config/ -run RatesFor`
Expected: FAIL, `undefined: RatesFor`.

- [ ] **Step 3: Write the implementation**

Append to `internal/config/config.go`:

```go
// RatesFor turns a tier's list prices into the dollars-per-million-token rates
// the cost package works in.
func RatesFor(name string) cost.Rates {
	tier, ok := Spec(name)
	if !ok {
		return cost.Rates{}
	}
	return cost.Rates{
		Read:  tier.CostIn * tier.CacheRead,
		Write: tier.CostIn * CacheWrite1h,
		Out:   tier.CostOut,
	}
}
```

Add the import to the file's import block:

```go
import (
	"os"
	"strconv"
	"strings"

	"github.com/djalmaaraujo/llm-router/internal/cost"
)
```

- [ ] **Step 4: Run the test and watch it pass**

Run: `go test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -S -m "feat: derive cache rates from tier list prices"
```

---

### Task 4: The policy package

**Files:**
- Create: `internal/policy/policy.go`
- Test: `internal/policy/policy_test.go`

**Interfaces:**
- Consumes: `config.RankOf`, `config.TierNames`, `config.Thresholds`, `config.RatesFor`, `cost.SwitchPaysOff`, `cost.BreakEvenTurns`.
- Produces: `policy.Answer{Choice string; Confidence float64}`; `policy.Input{Prompt string; Jev *Answer; Current string; Available []string; CachedTokens int; OutputTokens int; SubAgent bool}`; `policy.Outcome{Tier string; Reason string; Changed bool; BreakEven float64; Rebuild float64; SavingPerTurn float64; Horizon int}`; `policy.Decide(in Input) Outcome`; `policy.DetectOverride(prompt string) string`.

Reason strings, which `explain` and the tests both match on: `override`, `jev-unavailable`, `low-confidence-no-downgrade`, `low-confidence-capped`, `big-context-low-confidence`, `downgrade-not-worth-cache-rebuild`, `jev`. Any of them may carry the suffix `+unavailable` or `/no-change`.

- [ ] **Step 1: Write the failing test**

Create `internal/policy/policy_test.go`:

```go
package policy

import (
	"strings"
	"testing"
)

var all = []string{"haiku", "sonnet", "opus", "fable"}

func sure(choice string) *Answer   { return &Answer{Choice: choice, Confidence: 0.95} }
func unsure(choice string) *Answer { return &Answer{Choice: choice, Confidence: 0.2} }

func base() Input {
	return Input{
		Prompt:       "refactor the parser",
		Current:      "sonnet",
		Available:    all,
		CachedTokens: 0,
		OutputTokens: 2000,
	}
}

func TestFollowsAConfidentAnswer(t *testing.T) {
	in := base()
	in.Jev = sure("opus")
	out := Decide(in)
	if out.Tier != "opus" || out.Reason != "jev" || !out.Changed {
		t.Errorf("got %+v, want opus/jev/changed", out)
	}
}

func TestAnExplicitOverrideBeatsTheRouter(t *testing.T) {
	in := base()
	in.Prompt = "use haiku to fix this typo"
	in.Jev = sure("opus")
	if out := Decide(in); out.Tier != "haiku" || out.Reason != "override" {
		t.Errorf("got %+v, want haiku/override", out)
	}
}

func TestDetectOverrideOnlyFiresOnARealInstruction(t *testing.T) {
	cases := map[string]string{
		"switch to opus":         "opus",
		"use luna":               "haiku",
		"use strong":             "opus",
		"the opus of his career": "",
	}
	for prompt, want := range cases {
		if got := DetectOverride(prompt); got != want {
			t.Errorf("DetectOverride(%q) = %q, want %q", prompt, got, want)
		}
	}
}

func TestKeepsTheCurrentModelWhenTheRouterIsUnreachable(t *testing.T) {
	in := base()
	in.Jev = nil
	out := Decide(in)
	if out.Tier != "sonnet" || out.Changed {
		t.Errorf("got %+v, want sonnet unchanged", out)
	}
	if !strings.Contains(out.Reason, "jev-unavailable") {
		t.Errorf("reason = %q, want it to mention jev-unavailable", out.Reason)
	}
}

func TestIgnoresATierTheRouterInvented(t *testing.T) {
	in := base()
	in.Jev = sure("gpt-9")
	if out := Decide(in); out.Tier != "sonnet" {
		t.Errorf("got %q, want sonnet", out.Tier)
	}
}

func TestNeverDowngradesOnALowConfidenceAnswer(t *testing.T) {
	in := base()
	in.Jev = unsure("haiku")
	out := Decide(in)
	if out.Tier != "sonnet" || !strings.Contains(out.Reason, "low-confidence-no-downgrade") {
		t.Errorf("got %+v, want sonnet held", out)
	}
}

func TestCapsALowConfidenceUpgradeAtTheSafeCeiling(t *testing.T) {
	in := base()
	in.Current = "haiku"
	in.Jev = unsure("fable")
	if out := Decide(in); out.Tier != "sonnet" || out.Reason != "low-confidence-capped" {
		t.Errorf("got %+v, want sonnet/low-confidence-capped", out)
	}
}

func TestAllowsTheDowngradeThatPaysForItself(t *testing.T) {
	in := base()
	in.Current = "opus"
	in.Jev = sure("haiku")
	in.CachedTokens = 100_000
	out := Decide(in)
	if out.Tier != "haiku" {
		t.Errorf("opus to haiku at 100k pays off in 2.4 turns, got %+v", out)
	}
	if out.BreakEven < 2.3 || out.BreakEven > 2.5 {
		t.Errorf("BreakEven = %.2f, want about 2.4", out.BreakEven)
	}
}

func TestRefusesTheDowngradeThatDoesNotPayForItself(t *testing.T) {
	in := base()
	in.Jev = sure("haiku")
	in.CachedTokens = 100_000
	out := Decide(in)
	if out.Tier != "sonnet" {
		t.Errorf("sonnet to haiku at 100k needs 9.5 turns, got %+v", out)
	}
	if !strings.Contains(out.Reason, "cache-rebuild") {
		t.Errorf("reason = %q, want it to mention cache-rebuild", out.Reason)
	}
}

func TestASubAgentSwitchesFreelyBecauseItsCacheIsCheap(t *testing.T) {
	in := base()
	in.Jev = sure("haiku")
	in.CachedTokens = 8000
	in.SubAgent = true
	if out := Decide(in); out.Tier != "haiku" {
		t.Errorf("a sub-agent with 8k cached should switch, got %+v", out)
	}
}

func TestAnUpgradeIsNeverBlockedOnCost(t *testing.T) {
	in := base()
	in.Current = "haiku"
	in.Jev = sure("opus")
	in.CachedTokens = 500_000
	if out := Decide(in); out.Tier != "opus" {
		t.Errorf("an upgrade is a capability decision, got %+v", out)
	}
}

func TestABigContextUpgradeNeedsRealConfidence(t *testing.T) {
	in := base()
	in.Jev = &Answer{Choice: "opus", Confidence: 0.4}
	in.CachedTokens = 500_000
	out := Decide(in)
	if out.Tier != "sonnet" || !strings.Contains(out.Reason, "big-context-low-confidence") {
		t.Errorf("got %+v, want sonnet held on a coin-flip upgrade", out)
	}
	in.Jev = &Answer{Choice: "opus", Confidence: 0.9}
	if out := Decide(in); out.Tier != "opus" {
		t.Errorf("a confident upgrade must still go through, got %+v", out)
	}
}

func TestSubstitutesUpwardWhenTheChosenTierIsUnavailable(t *testing.T) {
	in := base()
	in.Current = "haiku"
	in.Available = []string{"haiku", "opus"}
	in.Jev = sure("sonnet")
	out := Decide(in)
	if out.Tier != "opus" || !strings.Contains(out.Reason, "unavailable") {
		t.Errorf("got %+v, want opus/unavailable", out)
	}
}

func TestNeverSubstitutesUpwardIntoPaidFable(t *testing.T) {
	in := base()
	in.Current = "haiku"
	in.Available = []string{"haiku", "fable"}
	in.Jev = sure("opus")
	if out := Decide(in); out.Tier != "haiku" {
		t.Errorf("got %q, want haiku", out.Tier)
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/policy/`
Expected: FAIL, `undefined: Decide`.

- [ ] **Step 3: Write the implementation**

Create `internal/policy/policy.go`:

```go
// Package policy turns a router answer into the tier that will actually run.
// It is pure and total: any missing, malformed, or unavailable input falls back
// to the tier already in use.
package policy

import (
	"regexp"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/cost"
)

type Answer struct {
	Choice     string
	Confidence float64
}

type Input struct {
	Prompt       string
	Jev          *Answer
	Current      string
	Available    []string
	CachedTokens int
	OutputTokens int
	SubAgent     bool
}

// Outcome carries the decision and the arithmetic behind it, so `explain` can
// show the numbers rather than a reason code.
type Outcome struct {
	Tier          string
	Reason        string
	Changed       bool
	BreakEven     float64
	Rebuild       float64
	SavingPerTurn float64
	Horizon       int
}

var overrides = []struct {
	tier string
	re   *regexp.Regexp
}{
	{"haiku", regexp.MustCompile(`(?i)\b(?:use|switch to|with|on)\s+(?:haiku|fast|luna)\b`)},
	{"sonnet", regexp.MustCompile(`(?i)\b(?:use|switch to|with|on)\s+(?:sonnet|balanced|terra)\b`)},
	{"opus", regexp.MustCompile(`(?i)\b(?:use|switch to|with|on)\s+(?:opus|strong|sol)\b`)},
	{"fable", regexp.MustCompile(`(?i)\b(?:use|switch to|with|on)\s+(?:fable|long|astra)\b`)},
}

// DetectOverride names the tier the user asked for in the prompt, or "".
func DetectOverride(prompt string) string {
	for _, o := range overrides {
		if o.re.MatchString(prompt) {
			return o.tier
		}
	}
	return ""
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// clampToAvailable finds the nearest tier the account can run. It prefers
// stepping up rather than down, so a hard task is never handed to a weaker
// model, but it never steps up into fable, which bills extra credits.
func clampToAvailable(tier string, available []string) string {
	if contains(available, tier) {
		return tier
	}
	rank := config.RankOf(tier)
	names := config.TierNames()
	for i := rank + 1; i < len(names); i++ {
		if contains(available, names[i]) && names[i] != "fable" {
			return names[i]
		}
	}
	for i := rank - 1; i >= 0; i-- {
		if contains(available, names[i]) {
			return names[i]
		}
	}
	return ""
}

func Decide(in Input) Outcome {
	horizon := config.Thresholds.SwitchHorizonTurns
	if in.SubAgent {
		horizon = config.Thresholds.SubAgentHorizonTurns
	}
	out := in.OutputTokens
	if out <= 0 {
		out = config.Thresholds.AssumedOutputTokens
	}

	settle := func(tier, reason string) Outcome {
		final := clampToAvailable(tier, in.Available)
		if final == "" {
			final = in.Current
		}
		if final != tier {
			reason += "+unavailable"
		}
		if final == in.Current {
			reason += "/no-change"
		}
		return Outcome{Tier: final, Reason: reason, Changed: final != in.Current, Horizon: horizon}
	}

	if override := DetectOverride(in.Prompt); override != "" {
		return settle(override, "override")
	}

	if in.Jev == nil || config.RankOf(in.Jev.Choice) < 0 {
		return settle(in.Current, "jev-unavailable")
	}

	target := in.Jev.Choice
	currentRank := config.RankOf(in.Current)
	targetRank := config.RankOf(target)

	if in.Jev.Confidence < config.Thresholds.MinConfidence {
		if targetRank < currentRank {
			return settle(in.Current, "low-confidence-no-downgrade")
		}
		ceiling := config.RankOf(config.Thresholds.UncertainCeiling)
		if currentRank > ceiling {
			ceiling = currentRank
		}
		if targetRank > ceiling {
			return settle(config.TierNames()[ceiling], "low-confidence-capped")
		}
	}

	// An upgrade is a capability decision, so cost never blocks it. The one
	// brake is confidence: past this much cached context a rebuild costs real
	// money in a single request, and a coin flip should not spend it.
	if targetRank > currentRank &&
		in.CachedTokens > config.Thresholds.BigContextTokens &&
		in.Jev.Confidence < config.Thresholds.BigContextMinConfidence {
		return settle(in.Current, "big-context-low-confidence")
	}

	if targetRank < currentRank {
		from := config.RatesFor(in.Current)
		to := config.RatesFor(target)
		turns, ok := cost.BreakEvenTurns(from, to, in.CachedTokens, out)
		if !ok || turns > float64(horizon) {
			held := settle(in.Current, "downgrade-not-worth-cache-rebuild")
			held.BreakEven = turns
			held.Rebuild, held.SavingPerTurn = rebuildAndSaving(from, to, in.CachedTokens, out)
			return held
		}
		switched := settle(target, "jev")
		switched.BreakEven = turns
		switched.Rebuild, switched.SavingPerTurn = rebuildAndSaving(from, to, in.CachedTokens, out)
		return switched
	}

	return settle(target, "jev")
}

// rebuildAndSaving reports the one-off premium for rebuilding the cache on `to`
// and what each later turn saves, both in dollars, for the explanation report.
func rebuildAndSaving(from, to cost.Rates, cachedTokens, outputTokens int) (float64, float64) {
	cached := float64(cachedTokens) / 1e6
	out := float64(outputTokens) / 1e6
	stay := cached*from.Read + out*from.Out
	first := cached*to.Write + out*to.Out
	later := cached*to.Read + out*to.Out
	return first - later, stay - later
}
```

- [ ] **Step 4: Run the test and watch it pass**

Run: `go test ./internal/policy/ -v`
Expected: PASS, fourteen tests.

- [ ] **Step 5: Run everything and format**

```bash
gofmt -l . && go vet ./... && go test ./...
```
Expected: no output from `gofmt -l`, no vet findings, all packages pass.

- [ ] **Step 6: Commit**

```bash
git add internal/policy/
git commit -S -m "feat: decide the tier from cache economics rather than a token threshold"
```

---

### Task 5: The routing provider

**Files:**
- Create: `internal/config/questions.go`, `internal/router/router.go`, `internal/router/jev/jev.go`
- Test: `internal/router/jev/jev_test.go`

**Interfaces:**
- Consumes: `config.Thresholds`, `config.Env`.
- Produces:
  - `config.Model{ID, Tier, Description string}`
  - `config.QuestionsFor(models []Model) map[string]any`
  - `router.Input{Prompt string; Current string; ContextTokens int; Models []config.Model}`
  - `router.Metrics{TaskComplexity, ReasoningRequired, ToolComplexity, ContextSize float64}`
  - `router.Decision{Choice string; Confidence float64; Metrics Metrics; Request any; Response any; Elapsed time.Duration}`
  - `router.Router` interface with `Route(ctx context.Context, in Input) (*Decision, error)`
  - `jev.New(apiKey string) *Client` and `jev.Client.Route(...)`, plus `jev.NewWithBaseURL(apiKey, baseURL string) *Client` for tests.

The wire contract, confirmed by unpacking `@typesafe-ai/sdk@0.6.0`:

```
POST https://api.typesafe.ai/v1/systemone
Authorization: Bearer <key>
Content-Type: application/json

{"state": {...}, "questions": {...}, "model": "jev-latest"}
```

The response is `{"model":..., "answers": {...}, "usage": {...}}`. A `choice` answer has `choice`, `confidence`, `probabilities`. A `score` answer has `score`, `confidence`, `legend`, `probabilities`.

- [ ] **Step 1: Write the failing test**

Create `internal/router/jev/jev_test.go`:

```go
package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/router"
)

var models = []config.Model{
	{ID: "claude-haiku-4-5-20251001", Tier: "haiku", Description: "Haiku 4.5"},
	{ID: "claude-opus-5", Tier: "opus", Description: "Opus 5"},
}

func TestRouteSendsTheContractAndReadsTheAnswer(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %q, want /v1/systemone", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"jev-latest","answers":{
			"model":{"type":"choice","choice":"claude-opus-5","confidence":0.91,"probabilities":{}},
			"task_complexity":{"type":"score","score":8,"confidence":0.9},
			"reasoning_required":{"type":"score","score":9,"confidence":0.9},
			"tool_complexity":{"type":"score","score":6,"confidence":0.9}}}`))
	}))
	defer server.Close()

	client := NewWithBaseURL("secret", server.URL)
	out, err := client.Route(context.Background(), router.Input{
		Prompt: "design the cache policy", Current: "claude-haiku-4-5-20251001",
		ContextTokens: 6200, Models: models,
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if out.Choice != "claude-opus-5" || out.Confidence != 0.91 {
		t.Errorf("got %+v", out)
	}
	if out.Metrics.TaskComplexity < 0.88 || out.Metrics.TaskComplexity > 0.90 {
		t.Errorf("TaskComplexity = %v, want 8/9", out.Metrics.TaskComplexity)
	}
	if got["model"] != "jev-latest" {
		t.Errorf("request model = %v, want jev-latest", got["model"])
	}
	state := got["state"].(map[string]any)
	if state["request"] != "design the cache policy" {
		t.Errorf("state.request = %v", state["request"])
	}
}

func TestRouteFailsClosedOnAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	if _, err := NewWithBaseURL("k", server.URL).Route(context.Background(), router.Input{
		Prompt: "x", Current: "claude-opus-5", Models: models,
	}); err == nil {
		t.Error("a 500 must return an error so the caller keeps the current model")
	}
}

func TestRouteRefusesAnEmptyModelList(t *testing.T) {
	if _, err := New("k").Route(context.Background(), router.Input{Prompt: "x"}); err == nil {
		t.Error("with no models there is nothing to choose between")
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/router/...`
Expected: FAIL, package does not exist.

- [ ] **Step 3: Write the question definitions**

Create `internal/config/questions.go`:

```go
package config

// Model is one exact model the signed-in account can run. Sending the real
// catalog rather than tier names keeps model versions apart, so
// claude-opus-4-8 and claude-opus-5 stay separate choices.
type Model struct {
	ID          string
	Tier        string
	Description string
}

var complexityScale = []string{
	"None", "Very low", "Low", "Some", "Moderate",
	"Moderate to high", "High", "Very high", "Severe", "Extreme",
}

// ComplexityMaxScore normalises a raw score to 0..1 for the report.
const ComplexityMaxScore = 9

var guidance = map[string]map[string]any{
	"haiku": {
		"what":     "Trivial, mechanical, or purely factual work.",
		"signals":  []string{"Rename, reformat, comment, or run one obvious command"},
		"not_for":  "Design judgement or multi-file reasoning.",
	},
	"sonnet": {
		"what":    "Ordinary day-to-day engineering with a clear, bounded shape.",
		"signals": []string{"Implement a specified function, test existing behaviour, or fix an understood local bug"},
		"not_for": "Open-ended architecture, subtle concurrency, or unknown-cause debugging.",
	},
	"opus": {
		"what":    "Hard reasoning, ambiguity, or high blast radius.",
		"signals": []string{"Unknown-cause debugging, cross-module design, security, auth, concurrency, or migrations"},
		"not_for": "Routine work with a clear implementation.",
	},
	"fable": {
		"what":    "Very large or long-running work beyond a normal focused session.",
		"signals": []string{"Whole-repo migration, unusually large context, or multi-hour autonomous execution"},
		"not_for": "Anything a strong model can finish in one focused session.",
	},
}

func scoreQuestion(instructions string) map[string]any {
	return map[string]any{"type": "score", "instructions": instructions, "criteria": complexityScale}
}

// QuestionsFor builds the question set, including the model choice assembled
// from the exact catalog this account reported.
func QuestionsFor(models []Model) map[string]any {
	criteria := map[string]any{}
	for _, m := range models {
		entry := map[string]any{"model": m.Description}
		for k, v := range guidance[m.Tier] {
			entry[k] = v
		}
		criteria[m.ID] = entry
	}
	return map[string]any{
		"task_complexity":    scoreQuestion("How complex is the coding task overall, including ambiguity, scope, and blast radius?"),
		"reasoning_required": scoreQuestion("How much reasoning is required to complete the request correctly in one pass?"),
		"tool_complexity":    scoreQuestion("How complex is the tool use required, from no tools to many coordinated or stateful operations?"),
		"model": map[string]any{
			"type": "choice",
			"instructions": []string{
				"Pick the cheapest exact model that can fully complete this coding request in one pass, without retrying on a stronger model.",
				"Treat different model versions as separate choices. Judge required reasoning, not requested reply length.",
			},
			"criteria": criteria,
		},
	}
}

// ContextWindowTokens normalises context size to 0..1 for the report.
const ContextWindowTokens = 200000
```

- [ ] **Step 4: Write the interface**

Create `internal/router/router.go`:

```go
// Package router defines what a routing provider must do. Only Jev is
// implemented; the interface exists so a second provider does not require
// touching the policy.
package router

import (
	"context"
	"time"

	"github.com/djalmaaraujo/llm-router/internal/config"
)

type Input struct {
	Prompt        string
	Current       string
	ContextTokens int
	Models        []config.Model
}

type Metrics struct {
	TaskComplexity    float64
	ReasoningRequired float64
	ToolComplexity    float64
	ContextSize       float64
}

// Decision carries the exact request and response so the report can be
// rendered later without asking the provider again.
type Decision struct {
	Choice     string
	Confidence float64
	Metrics    Metrics
	Request    any
	Response   any
	Elapsed    time.Duration
}

type Router interface {
	Route(ctx context.Context, in Input) (*Decision, error)
}
```

- [ ] **Step 5: Write the Jev client**

Create `internal/router/jev/jev.go`:

```go
// Package jev calls TypeSafe System One. The SDK's defaults are far too slow
// for a per-prompt hot path, so the timeout, retry count, and an outer deadline
// are all pinned here.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/router"
)

const defaultBaseURL = "https://api.typesafe.ai"

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func New(apiKey string) *Client { return NewWithBaseURL(apiKey, defaultBaseURL) }

func NewWithBaseURL(apiKey, baseURL string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		http:    &http.Client{Timeout: time.Duration(config.Thresholds.JevTimeoutMS) * time.Millisecond},
	}
}

type answer struct {
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
	Score      float64 `json:"score"`
}

type result struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
}

func (c *Client) Route(ctx context.Context, in router.Input) (*router.Decision, error) {
	if len(in.Models) == 0 {
		return nil, errors.New("no models to choose between")
	}
	ids := make([]string, len(in.Models))
	for i, m := range in.Models {
		ids[i] = m.ID
	}
	body := map[string]any{
		"state": map[string]any{
			"request":     in.Prompt,
			"session":     map[string]any{"current_model": in.Current, "context_tokens": in.ContextTokens},
			"environment": map[string]any{"available_models": ids},
		},
		"questions": config.QuestionsFor(in.Models),
		"model":     "jev-latest",
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.Thresholds.JevDeadlineMS)*time.Millisecond)
	defer cancel()

	started := time.Now()
	var res *result
	var err error
	for attempt := 0; attempt <= config.Thresholds.JevMaxRetries; attempt++ {
		res, err = c.post(ctx, body)
		if err == nil || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}

	pick, ok := res.Answers["model"]
	if !ok || pick.Choice == "" {
		return nil, errors.New("no model answer")
	}
	norm := func(name string) float64 { return res.Answers[name].Score / config.ComplexityMaxScore }
	ctxSize := float64(in.ContextTokens) / config.ContextWindowTokens
	if ctxSize > 1 {
		ctxSize = 1
	}
	return &router.Decision{
		Choice:     pick.Choice,
		Confidence: pick.Confidence,
		Metrics: router.Metrics{
			TaskComplexity:    norm("task_complexity"),
			ReasoningRequired: norm("reasoning_required"),
			ToolComplexity:    norm("tool_complexity"),
			ContextSize:       ctxSize,
		},
		Request:  body,
		Response: res,
		Elapsed:  time.Since(started),
	}, nil
}

func (c *Client) post(ctx context.Context, body map[string]any) (*result, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/systemone", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("systemone: http %d", resp.StatusCode)
	}
	var out result
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
```

`UseNumber` is deliberately absent here. It is required for proxied CLI bodies, which get decoded and re-serialised onto a wire; this response decodes into a typed struct and is never written back, so a plain decode is correct.

- [ ] **Step 6: Run the tests and watch them pass**

Run: `go test ./internal/router/... -v`
Expected: PASS, three tests.

- [ ] **Step 7: Commit**

```bash
git add internal/config/questions.go internal/router/
git commit -S -m "feat: call TypeSafe System One with the standard library"
```

---

### Task 6: The decision store

**Files:**
- Create: `internal/state/state.go`
- Test: `internal/state/state_test.go`

**Interfaces:**
- Consumes: nothing beyond the standard library.
- Produces: `state.Status` struct; `state.Write(sessionID string, s Status)`; `state.WriteDecision(sessionID string, s Status)`; `state.Read(sessionID string) *Status`; `state.Dir() string`; `state.PruneStale(maxAge time.Duration, now time.Time) int`.

`state.Status` fields, all exported with JSON tags matching the Node file so a session started under either version still reads: `Tier string`, `Model string`, `Prompt string`, `Reason string`, `Confidence *float64`, `Manual bool`, `At int64`, `Metrics *router.Metrics`, `BreakEven float64`, `Rebuild float64`, `SavingPerTurn float64`, `Horizon int`, `Request any`, `Response any`, `History []Status`.

- [ ] **Step 1: Write the failing test**

Create `internal/state/state_test.go`:

```go
package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRoundTripsPerSessionAndMissesCleanly(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	Write("abc", Status{Tier: "haiku", Model: "claude-haiku-4-5-20251001"})
	got := Read("abc")
	if got == nil || got.Tier != "haiku" {
		t.Fatalf("Read = %+v, want the haiku status", got)
	}
	if Read("no-such-session") != nil {
		t.Error("an unknown session must read as nil")
	}
}

func TestFilesAreReadableOnlyByTheOwner(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	Write("abc", Status{Tier: "opus"})
	info, err := os.Stat(filepath.Join(Dir(), "abc.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600; it holds prompt text", info.Mode().Perm())
	}
	dir, err := os.Stat(Dir())
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", dir.Mode().Perm())
	}
}

func TestSessionIDsCannotEscapeTheDirectory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	Write("../../escape", Status{Tier: "opus"})
	if _, err := os.Stat(filepath.Join(Dir(), "..", "..", "escape.json")); err == nil {
		t.Fatal("a session id must never become a path traversal")
	}
}

func TestWriteDecisionKeepsTwentyEntries(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for i := 0; i < 25; i++ {
		WriteDecision("abc", Status{Tier: "haiku", Prompt: string(rune('a' + i))})
	}
	got := Read("abc")
	if got == nil || len(got.History) != 20 {
		t.Fatalf("history length = %d, want 20", len(got.History))
	}
}

func TestPruneStaleRemovesOldFilesOnly(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	Write("fresh", Status{Tier: "haiku"})
	Write("old", Status{Tier: "haiku"})
	old := filepath.Join(Dir(), "old.json")
	past := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(old, past, past)

	if removed := PruneStale(7*24*time.Hour, time.Now()); removed != 1 {
		t.Errorf("removed %d files, want 1", removed)
	}
	if Read("fresh") == nil {
		t.Error("the fresh session must survive")
	}
	if Read("old") != nil {
		t.Error("the stale session must be gone")
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/state/`
Expected: FAIL, package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/state/state.go`. Requirements the tests pin down:

- `Dir()` returns `filepath.Join(os.TempDir(), "llm-router")`. Read it on every call rather than caching, so `TMPDIR` in a test takes effect.
- The filename strips every character outside `[A-Za-z0-9_-]` from the session id, which is what stops path traversal. An id that reduces to the empty string is not written.
- `Write` creates the directory with `0o700` and the file with `0o600`, then calls `os.Chmod` on both, because the mode argument only applies at creation and files from an earlier version may be looser.
- `Write` never returns an error and never panics. Status display is cosmetic; it must not interfere with a request.
- `Write` runs `PruneStale(7*24*time.Hour, time.Now())` once per process, guarded by a `sync.Once`.
- `WriteDecision` reads the current status, appends the new one to `History`, truncates `History` to the last 20, clears `History` on the appended copy so the file does not nest, and writes the result.
- `Read` returns `nil` on any error.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/state/ -v`
Expected: PASS, five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/state/
git commit -S -m "feat: store routing decisions per session with owner-only permissions"
```

---

### Task 7: The explanation report

**Files:**
- Create: `internal/explain/explain.go`
- Test: `internal/explain/explain_test.go`

**Interfaces:**
- Consumes: `state.Status`.
- Produces: `explain.Render(s *state.Status) string`.

- [ ] **Step 1: Write the failing test**

Create `internal/explain/explain_test.go`:

```go
package explain

import (
	"strings"
	"testing"

	"github.com/djalmaaraujo/llm-router/internal/router"
	"github.com/djalmaaraujo/llm-router/internal/state"
)

func TestReportsWhenNothingHasBeenRouted(t *testing.T) {
	if got := Render(nil); !strings.Contains(got, "no routing decision") {
		t.Errorf("got %q", got)
	}
}

func TestReportsManualControl(t *testing.T) {
	if got := Render(&state.Status{Manual: true}); !strings.Contains(got, "paused") {
		t.Errorf("got %q", got)
	}
}

func TestShowsTheScoresAndTheSelectedModel(t *testing.T) {
	c := 0.94
	got := Render(&state.Status{
		Tier: "sonnet", Model: "claude-sonnet-5", Prompt: "explain the router",
		Reason: "jev", Confidence: &c,
		Metrics: &router.Metrics{TaskComplexity: 0.82, ReasoningRequired: 0.91, ToolComplexity: 0.64, ContextSize: 0.31},
	})
	for _, want := range []string{"0.82", "0.91", "0.64", "0.31", "94%", "CLAUDE-SONNET-5", "explain the router"} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
}

func TestShowsTheCacheArithmeticWhenARebuildWasWeighed(t *testing.T) {
	got := Render(&state.Status{
		Tier: "sonnet", Model: "claude-sonnet-5",
		Reason:    "downgrade-not-worth-cache-rebuild/no-change",
		Rebuild:   0.19,
		SavingPerTurn: 0.02,
		BreakEven: 9.5,
		Horizon:   5,
	})
	for _, want := range []string{"$0.190", "$0.020", "9.5", "horizon is 5"} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "cache rebuild avoided") {
		t.Error("the report must show the arithmetic, not a reason code")
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/explain/`
Expected: FAIL, package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/explain/explain.go`. Requirements:

- Render a box 33 characters wide with `┌ ─ ┐ │ └ ┘`, exactly as the Node version does, so the output is familiar.
- With `s == nil`, return `llm-router: no routing decision has been recorded for this session.`
- With `s.Manual`, return `llm-router: routing is paused because you selected a model manually.`
- Otherwise the rows are: `Jev request`; the prompt, word-wrapped to the box; `Current tier:`; `Context tokens:`; a blank row; `Jev response`; the four metrics formatted `%.2f`, or `n/a` when `Metrics` is nil; a blank row; `Selected model:` uppercased; `Confidence:` as a whole percent, or `n/a` when nil.
- When `s.BreakEven > 0`, add a block:

```
Decision: held on SONNET
  rebuild on HAIKU   $0.190 once
  saving             $0.020 per turn
  pays off in        9.5 turns, horizon is 5
```

  Use `held on` when `s.Reason` contains `cache-rebuild`, and `switched to` otherwise. Format money with `%.3f` and turns with `%.1f`.
- When `s.BreakEven == 0`, fall back to one `Decision:` line naming the reason in words: `override` becomes `prompt override`, `jev-unavailable` becomes `router unavailable; held`, `low-confidence-no-downgrade` becomes `low confidence; held`, `low-confidence-capped` becomes `low confidence; capped`, `big-context-low-confidence` becomes `large context; needs more confidence`, `unavailable` becomes `nearest available tier`, and anything else becomes `router recommendation`. Check `cache-rebuild` before the bare `unavailable` check, because reasons can carry both.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/explain/ -v`
Expected: PASS, four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/explain/
git commit -S -m "feat: show the cache arithmetic in the explanation report"
```

---

### Task 8: The status line

**Files:**
- Create: `internal/statusline/statusline.go`
- Test: `internal/statusline/statusline_test.go`

**Interfaces:**
- Consumes: `state.Read`.
- Produces: `statusline.Render(stdin []byte) string`.

Claude Code pipes session JSON on stdin and renders whatever the command prints. The fields used are `session_id`, `workspace.current_dir`, `cwd`, `context_window.used_percentage`, and `model.display_name`.

- [ ] **Step 1: Write the failing test**

Create `internal/statusline/statusline_test.go`:

```go
package statusline

import (
	"strings"
	"testing"

	"github.com/djalmaaraujo/llm-router/internal/state"
)

func stdin(sessionID string) []byte {
	return []byte(`{"session_id":"` + sessionID + `","workspace":{"current_dir":"/home/me/my-project"},` +
		`"context_window":{"used_percentage":8.4},"model":{"display_name":"Opus 4.6"}}`)
}

func TestWaitsBeforeTheFirstPrompt(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	got := Render(stdin("s1"))
	if !strings.Contains(got, "waiting for first prompt") || !strings.Contains(got, "my-project") {
		t.Errorf("got %q", got)
	}
}

func TestShowsTheRoutedModelAndConfidence(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	c := 0.98
	state.Write("s1", state.Status{Tier: "haiku", Model: "claude-haiku-4-5-20251001", Reason: "jev", Confidence: &c})
	got := Render(stdin("s1"))
	for _, want := range []string{"claude-haiku-4-5-20251001", "p=0.98", "my-project", "8% context"} {
		if !strings.Contains(got, want) {
			t.Errorf("status line is missing %q: %q", want, got)
		}
	}
}

func TestNamesTheReasonOnlyWhenRoutingDeclined(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	state.Write("s1", state.Status{Tier: "opus", Model: "claude-opus-5", Reason: "jev"})
	if strings.Contains(Render(stdin("s1")), "(") && !strings.Contains(Render(stdin("s1")), "p=") {
		t.Error("the common case must stay short")
	}
	state.Write("s2", state.Status{Tier: "opus", Model: "claude-opus-5", Reason: "downgrade-not-worth-cache-rebuild/no-change"})
	if !strings.Contains(Render(stdin("s2")), "downgrade-not-worth-cache-rebuild") {
		t.Error("a held decision must say why")
	}
}

func TestShowsManualControl(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	state.Write("s1", state.Status{Manual: true})
	got := Render(stdin("s1"))
	if !strings.Contains(got, "manual") || !strings.Contains(got, "Opus 4.6") {
		t.Errorf("got %q", got)
	}
}

func TestSurvivesMalformedInput(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	if got := Render([]byte("not json")); got == "" {
		t.Error("a broken payload must still produce a usable line")
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/statusline/`
Expected: FAIL, package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/statusline/statusline.go`. Requirements:

- Colours: dim `\x1b[2m`, reset `\x1b[0m`, haiku `\x1b[32m`, sonnet `\x1b[36m`, opus `\x1b[35m`, fable `\x1b[33m`.
- The directory shown is the last path segment of `workspace.current_dir`, falling back to `cwd`.
- The percentage is `used_percentage` rounded to a whole number.
- Layout: `<routed> <dim>·<reset> <dir> <dim>· <pct>% context<reset>`.
- `<routed>` is `jev: waiting for first prompt` dimmed when there is no status; `⏸ manual <display_name>` when `Manual`; otherwise the coloured model or tier, then ` (p=0.98)` dimmed when `Confidence` is set, then ` (<reason before the first slash>)` dimmed **only** when the reason is not `jev`, not `jev/no-change`, and does not contain `override`.
- Unmarshal failures leave the parsed struct zero-valued and the function still returns a line.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/statusline/ -v`
Expected: PASS, five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/statusline/
git commit -S -m "feat: render the Claude Code status line without a Node process"
```

---

### Task 9: Claude request-body handling

Pure functions over a decoded body. No network, no state.

**Files:**
- Create: `internal/proxy/claude/body.go`
- Test: `internal/proxy/claude/body_test.go`

**Interfaces:**
- Consumes: `config.Spec`, `config.TierOf`, `config.IDOf`.
- Produces: `claude.SanitizeSchema(node any)`; `claude.NewTurnPrompt(body map[string]any) string`; `claude.ApplyTier(body map[string]any, tier, model string)`; `claude.SessionOf(body map[string]any) string`; `claude.ConversationKey(body map[string]any) string`; `claude.ModelsFrom(catalog []map[string]any) []config.Model`.

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/claude/body_test.go`:

```go
package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSanitizeSchemaConvertsDraft04Bounds(t *testing.T) {
	body := decode(t, `{"input_schema":{"properties":{"n":{"type":"number","minimum":5,"exclusiveMinimum":true}}}}`)
	SanitizeSchema(body["input_schema"])
	prop := body["input_schema"].(map[string]any)["properties"].(map[string]any)["n"].(map[string]any)
	if fmt.Sprint(prop["exclusiveMinimum"]) != "5" {
		t.Errorf("exclusiveMinimum = %v, want the numeric bound 5", prop["exclusiveMinimum"])
	}
	if _, still := prop["minimum"]; still {
		t.Error("minimum must be removed once it became exclusiveMinimum")
	}
}

func TestSanitizeSchemaDropsAFalseBound(t *testing.T) {
	body := decode(t, `{"s":{"maximum":9,"exclusiveMaximum":false}}`)
	SanitizeSchema(body["s"])
	s := body["s"].(map[string]any)
	if _, still := s["exclusiveMaximum"]; still {
		t.Error("a false boolean bound must be dropped entirely")
	}
	if fmt.Sprint(s["maximum"]) != "9" {
		t.Error("the plain bound must survive")
	}
}

func TestNewTurnPromptFindsATypedTurn(t *testing.T) {
	body := decode(t, `{"tools":[{"name":"Read"}],"messages":[
		{"role":"user","content":"first"},
		{"role":"assistant","content":"ok"},
		{"role":"user","content":[{"type":"text","text":"fix the parser"}]}]}`)
	if got := NewTurnPrompt(body); got != "fix the parser" {
		t.Errorf("got %q", got)
	}
}

func TestNewTurnPromptStripsSystemReminders(t *testing.T) {
	body := decode(t, `{"tools":[{"name":"Read"}],"messages":[{"role":"user","content":
		"<system-reminder>noise</system-reminder>  real prompt  "}]}`)
	if got := NewTurnPrompt(body); got != "real prompt" {
		t.Errorf("got %q, want the reminder stripped and trimmed", got)
	}
}

func TestNewTurnPromptIgnoresToolLoopContinuations(t *testing.T) {
	body := decode(t, `{"tools":[{"name":"Read"}],"messages":[{"role":"user","content":
		[{"type":"tool_result","tool_use_id":"x"}]}]}`)
	if got := NewTurnPrompt(body); got != "" {
		t.Errorf("got %q, want empty: a continuation is not a new turn", got)
	}
}

func TestNewTurnPromptIgnoresAuxiliaryCalls(t *testing.T) {
	body := decode(t, `{"messages":[{"role":"user","content":"summarise this"}]}`)
	if got := NewTurnPrompt(body); got != "" {
		t.Errorf("got %q, want empty: no tools means an auxiliary call", got)
	}
}

func TestApplyTierStripsWhatHaikuCannotAccept(t *testing.T) {
	body := decode(t, `{"model":"jev-router","thinking":{"type":"adaptive"},
		"output_config":{"effort":"high"},
		"context_management":{"edits":[{"type":"clear_thinking_20251015"}]}}`)
	ApplyTier(body, "haiku", "claude-haiku-4-5-20251001")
	if body["model"] != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %v", body["model"])
	}
	for _, gone := range []string{"thinking", "output_config", "context_management"} {
		if _, still := body[gone]; still {
			t.Errorf("%s must be removed for haiku", gone)
		}
	}
}

func TestApplyTierKeepsWhatOpusAccepts(t *testing.T) {
	body := decode(t, `{"model":"jev-router","thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`)
	ApplyTier(body, "opus", "claude-opus-5")
	if _, ok := body["thinking"]; !ok {
		t.Error("opus supports thinking")
	}
	if _, ok := body["output_config"]; !ok {
		t.Error("opus supports effort")
	}
}

func TestApplyTierKeepsANonThinkingEdit(t *testing.T) {
	body := decode(t, `{"model":"jev-router","thinking":{"type":"adaptive"},
		"context_management":{"edits":[{"type":"clear_thinking_20251015"},{"type":"clear_tool_uses_20250919"}]}}`)
	ApplyTier(body, "haiku", "claude-haiku-4-5-20251001")
	cm, ok := body["context_management"].(map[string]any)
	if !ok {
		t.Fatal("an edit that does not mention thinking must survive")
	}
	if len(cm["edits"].([]any)) != 1 {
		t.Errorf("edits = %v, want only the tool-uses edit", cm["edits"])
	}
}

func TestSessionOfReadsTheNestedJSONString(t *testing.T) {
	body := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"abc-123\"}"}}`)
	if got := SessionOf(body); got != "abc-123" {
		t.Errorf("got %q", got)
	}
	if got := SessionOf(decode(t, `{"metadata":{"user_id":"not-json"}}`)); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := SessionOf(decode(t, `{}`)); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestConversationKeyIgnoresTheMovingCacheBreakpoint(t *testing.T) {
	a := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"s\"}"},"messages":[{"role":"user","content":
		[{"type":"text","text":"do the thing"}]}]}`)
	b := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"s\"}"},"messages":[{"role":"user","content":
		[{"type":"text","text":"do the thing","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)
	if ConversationKey(a) != ConversationKey(b) {
		t.Error("Claude Code moves the breakpoint between requests; the key must not follow it")
	}
}

func TestConversationKeySeparatesSessionsAndSubAgents(t *testing.T) {
	main := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"s\"}"},"messages":[{"role":"user","content":"hello"}]}`)
	sub := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"s\"}"},"messages":[{"role":"user","content":"search for X"}]}`)
	other := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"t\"}"},"messages":[{"role":"user","content":"hello"}]}`)
	if ConversationKey(main) == ConversationKey(sub) {
		t.Error("a sub-agent opens with different text and must get its own key")
	}
	if ConversationKey(main) == ConversationKey(other) {
		t.Error("the same opening text in two sessions must get two keys")
	}
}

func TestModelsFromFallsBackToStaticIDs(t *testing.T) {
	if got := ModelsFrom(nil); len(got) != 4 {
		t.Errorf("an empty catalog must fall back to the four static tiers, got %d", len(got))
	}
	catalog := []map[string]any{
		{"id": "claude-opus-5", "display_name": "Opus 5", "created_at": "2026-04-01T00:00:00Z"},
		{"id": "gpt-5.6-luna"},
	}
	got := ModelsFrom(catalog)
	if len(got) != 1 || got[0].Tier != "opus" {
		t.Fatalf("got %+v, want only the opus row", got)
	}
	if !strings.Contains(got[0].Description, "Opus 5") || !strings.Contains(got[0].Description, "2026-04-01") {
		t.Errorf("description = %q", got[0].Description)
	}
}
```

Add `"fmt"` to the test file's imports.

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/proxy/claude/`
Expected: FAIL, package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/proxy/claude/body.go`. Full code for the two functions that are easy to get subtly wrong:

```go
// ApplyTier points a request at a tier and removes the fields that tier cannot
// accept. Claude Code composes the body for whatever model it thinks it is
// talking to, so routing down to Haiku while leaving thinking in place is a
// hard 400.
func ApplyTier(body map[string]any, tier, model string) {
	spec, ok := config.Spec(tier)
	if !ok {
		return
	}
	body["model"] = model

	if !spec.Thinking {
		delete(body, "thinking")
		// A context-management strategy that prunes thinking blocks is itself
		// rejected once thinking is gone, so it has to go with it.
		if cm, ok := body["context_management"].(map[string]any); ok {
			if edits, ok := cm["edits"].([]any); ok {
				kept := edits[:0]
				for _, e := range edits {
					entry, _ := e.(map[string]any)
					kind, _ := entry["type"].(string)
					if !strings.Contains(strings.ToLower(kind), "thinking") {
						kept = append(kept, e)
					}
				}
				if len(kept) == 0 {
					delete(body, "context_management")
				} else {
					cm["edits"] = kept
				}
			}
		}
	}

	if !spec.Effort {
		if oc, ok := body["output_config"].(map[string]any); ok {
			delete(oc, "effort")
			if len(oc) == 0 {
				delete(body, "output_config")
			}
		}
	}
}

// ConversationKey identifies the conversation a request belongs to. Claude Code
// runs sub-agents through the same endpoint, so one pinned model would let a
// sub-agent's choice leak into the main conversation.
//
// Only stable fields may be used. Claude Code moves its cache_control
// breakpoint between requests and rewrites message metadata, so the key is the
// session id plus the text of the first message, which is fixed once a
// conversation starts and differs between the main agent and each sub-agent.
func ConversationKey(body map[string]any) string {
	var text strings.Builder
	if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
		if first, ok := messages[0].(map[string]any); ok {
			switch content := first["content"].(type) {
			case string:
				text.WriteString(content)
			case []any:
				for _, block := range content {
					b, _ := block.(map[string]any)
					if b["type"] == "text" {
						s, _ := b["text"].(string)
						text.WriteString(s)
					}
				}
			}
		}
	}
	sum := sha1.Sum([]byte(SessionOf(body) + "|" + text.String()))
	return hex.EncodeToString(sum[:])[:12]
}
```

The rest, described precisely:

- `SanitizeSchema(node any)` walks maps and slices. For the pairs `exclusiveMinimum`/`minimum` and `exclusiveMaximum`/`maximum`: when the exclusive key holds a `bool`, set it to the plain bound's value and delete the plain bound if the bool is `true` and the bound is a number, otherwise delete the exclusive key. Then recurse into every value. Claude Code converts these draft-04 relics itself when talking first-party but skips it behind a custom base URL, so the API rejects the request without this.
- `NewTurnPrompt(body) string` returns `""` unless `body["tools"]` is a non-empty `[]any`. It reads the last element of `body["messages"]`; unless that is a map with `role == "user"`, it returns `""`. A `string` content is the text. An `[]any` content returns `""` if any block has `type == "tool_result"`, otherwise joins the `text` of every `type == "text"` block with `"\n"`. It then removes every `<system-reminder>...</system-reminder>` span with the regexp `(?s)<system-reminder>.*?</system-reminder>` and returns the result trimmed of whitespace.
- `SessionOf(body) string` reads `body["metadata"]["user_id"]` as a `string`, unmarshals it as JSON, and returns its `session_id` field. Any failure returns `""`.
- `ModelsFrom(catalog []map[string]any) []config.Model` keeps entries whose `id` maps to a tier, building `Description` by joining the present values of `display_name`, `"released " + created_at[:10]`, and `max_input_tokens + " input tokens"` with `"; "`. When nothing survives, it returns one `config.Model` per entry in `config.Tiers` with `Description` set to the id.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/proxy/claude/ -v`
Expected: PASS, twelve tests.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/claude/
git commit -S -m "feat: read and rewrite Claude Code request bodies"
```

---

### Task 10: The proxy server and the usage tap

**Files:**
- Create: `internal/proxy/proxy.go`
- Test: `internal/proxy/proxy_test.go`

**Interfaces:**
- Consumes: nothing beyond the standard library.
- Produces: `proxy.Usage{InputTokens, CacheReadTokens, CacheCreationTokens, OutputTokens int}` with method `CachedTotal() int`; `proxy.Hooks` struct; `proxy.Start(upstream string, h Hooks) (*Server, error)`; `proxy.Server{Port int}` with `Close() error`.

```go
type Hooks struct {
	// RewritesPath reports whether a request path's JSON body should be
	// decoded, passed to RewriteRequest, and re-encoded.
	RewritesPath func(path string) bool
	// RewriteRequest may mutate body in place. It returns a correlation key
	// for ObserveUsage, or "" to skip the tap for this request.
	RewriteRequest func(path string, body map[string]any) string
	// ObserveUsage reports what the response said, once it has been streamed.
	ObserveUsage func(key string, u Usage)
	// BuffersResponse reports whether a response should be buffered whole and
	// handed to ObserveResponse instead of streamed.
	BuffersResponse func(method, path string) bool
	// ObserveResponse receives a buffered response body.
	ObserveResponse func(path string, body []byte)
}
```

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/proxy_test.go`:

```go
package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForwardsUnhandledPathsUntouched(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if string(got) != `{"a":1}` {
			t.Errorf("body = %q, want it untouched", got)
		}
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	p, err := Start(upstream.URL, Hooks{RewritesPath: func(string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	resp, err := http.Post(p.URL()+"/v1/other", "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("response = %q", body)
	}
}

func TestRewritesTheBodyAndPreservesLargeIntegers(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = string(b)
		w.Write([]byte("{}"))
	}))
	defer upstream.Close()

	p, _ := Start(upstream.URL, Hooks{
		RewritesPath: func(path string) bool { return path == "/v1/messages" },
		RewriteRequest: func(_ string, body map[string]any) string {
			body["model"] = "claude-opus-5"
			return ""
		},
	})
	defer p.Close()

	http.Post(p.URL()+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"jev-router","max_tokens":1000000,"ratio":0.30}`))

	if !strings.Contains(seen, `"max_tokens":1000000`) {
		t.Errorf("large integer was mangled: %s", seen)
	}
	if !strings.Contains(seen, `"ratio":0.30`) {
		t.Errorf("decimal was mangled: %s", seen)
	}
	if !strings.Contains(seen, `"model":"claude-opus-5"`) {
		t.Errorf("rewrite did not apply: %s", seen)
	}
}

func TestReadsUsageOffAStreamedResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":12,"+
			"\"cache_read_input_tokens\":94000,\"cache_creation_input_tokens\":300,\"output_tokens\":1}}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"delta\":{\"text\":\""+strings.Repeat("x", 9000)+"\"}}\n\n")
		io.WriteString(w, "event: message_delta\ndata: {\"usage\":{\"output_tokens\":877}}\n\n")
	}))
	defer upstream.Close()

	got := make(chan Usage, 1)
	p, _ := Start(upstream.URL, Hooks{
		RewritesPath:   func(path string) bool { return path == "/v1/messages" },
		RewriteRequest: func(string, map[string]any) string { return "conv1" },
		ObserveUsage:   func(key string, u Usage) { got <- u },
	})
	defer p.Close()

	resp, _ := http.Post(p.URL()+"/v1/messages", "application/json", strings.NewReader(`{"model":"jev-router"}`))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) < 9000 {
		t.Fatalf("the tap must not swallow the stream, got %d bytes", len(body))
	}

	u := <-got
	if u.CacheReadTokens != 94000 || u.CacheCreationTokens != 300 || u.InputTokens != 12 {
		t.Errorf("cache usage = %+v", u)
	}
	if u.OutputTokens != 877 {
		t.Errorf("OutputTokens = %d, want the final count from message_delta", u.OutputTokens)
	}
	if u.CachedTotal() != 94312 {
		t.Errorf("CachedTotal = %d, want 94312", u.CachedTotal())
	}
}

func TestBuffersTheCatalogResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"claude-opus-5"}]}`))
	}))
	defer upstream.Close()

	seen := make(chan []byte, 1)
	p, _ := Start(upstream.URL, Hooks{
		RewritesPath:    func(string) bool { return false },
		BuffersResponse: func(method, path string) bool { return method == "GET" && path == "/v1/models" },
		ObserveResponse: func(_ string, body []byte) { seen <- body },
	})
	defer p.Close()

	resp, _ := http.Get(p.URL() + "/v1/models")
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(<-seen), "claude-opus-5") {
		t.Error("the catalog must reach ObserveResponse")
	}
}

func TestAnswersTheHeadProbe(t *testing.T) {
	p, _ := Start("http://127.0.0.1:1", Hooks{RewritesPath: func(string) bool { return false }})
	defer p.Close()
	resp, err := http.Head(p.URL() + "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("HEAD = %d, want 200: Claude Code probes the base URL first", resp.StatusCode)
	}
}

func TestAnUpstreamFailureBecomesA502(t *testing.T) {
	p, _ := Start("http://127.0.0.1:1", Hooks{RewritesPath: func(string) bool { return false }})
	defer p.Close()
	resp, err := http.Get(p.URL() + "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/proxy/`
Expected: FAIL, `undefined: Start`.

- [ ] **Step 3: Write the implementation**

Create `internal/proxy/proxy.go`. Full code for the tap, which is the part with the real trap:

```go
// usageTap reads token counts out of a response without buffering it or
// delaying a byte. The cache numbers arrive in the first SSE frame
// (message_start) and the final output count in the last (message_delta), so
// it keeps a bounded head and a bounded tail and scans those at the end.
type usageTap struct {
	head []byte
	tail []byte
}

const (
	tapHead = 8192
	tapTail = 2048
)

func (t *usageTap) Write(p []byte) (int, error) {
	if len(t.head) < tapHead {
		room := tapHead - len(t.head)
		if room > len(p) {
			room = len(p)
		}
		t.head = append(t.head, p[:room]...)
	}
	t.tail = append(t.tail, p...)
	if len(t.tail) > tapTail {
		t.tail = t.tail[len(t.tail)-tapTail:]
	}
	return len(p), nil
}

var tapFields = map[string]*regexp.Regexp{
	"input":    regexp.MustCompile(`"input_tokens"\s*:\s*(\d+)`),
	"read":     regexp.MustCompile(`"cache_read_input_tokens"\s*:\s*(\d+)`),
	"creation": regexp.MustCompile(`"cache_creation_input_tokens"\s*:\s*(\d+)`),
	"output":   regexp.MustCompile(`"output_tokens"\s*:\s*(\d+)`),
}

func firstInt(re *regexp.Regexp, b []byte) int {
	if m := re.FindSubmatch(b); m != nil {
		n, _ := strconv.Atoi(string(m[1]))
		return n
	}
	return 0
}

func lastInt(re *regexp.Regexp, b []byte) int {
	all := re.FindAllSubmatch(b, -1)
	if len(all) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(string(all[len(all)-1][1]))
	return n
}

func (t *usageTap) usage() Usage {
	u := Usage{
		InputTokens:         firstInt(tapFields["input"], t.head),
		CacheReadTokens:     firstInt(tapFields["read"], t.head),
		CacheCreationTokens: firstInt(tapFields["creation"], t.head),
	}
	// The final count is in the last frame; fall back to the first frame for a
	// non-streaming response small enough to sit entirely in the head.
	if u.OutputTokens = lastInt(tapFields["output"], t.tail); u.OutputTokens == 0 {
		u.OutputTokens = lastInt(tapFields["output"], t.head)
	}
	return u
}
```

`Usage.CachedTotal()` returns `InputTokens + CacheReadTokens + CacheCreationTokens` — the whole prefix that will be cached for the next turn.

The server, described precisely:

- Listen on `127.0.0.1:0`; `Server.Port` is the chosen port and `Server.URL()` returns `http://127.0.0.1:<port>`.
- `HEAD` on any path answers `200` with an empty body. Claude Code probes the base URL before its first request.
- Copy every request header to the upstream request, replacing `Host` with the upstream host and deleting `Content-Length`, which the rewrite invalidates. **Never read, log, or alter `Authorization` or `x-api-key`.**
- When `RewritesPath(path)` is true and the method is `POST`, read the body, decode it with `json.NewDecoder(...)` after calling `dec.UseNumber()`, call `RewriteRequest`, and re-encode with `json.Marshal`. On any decode failure, forward the original bytes unchanged — a body we cannot read is not a body we may break.
- When `BuffersResponse(method, path)` is true, read the upstream response whole, call `ObserveResponse`, then write status, headers minus `Content-Length`, and the body. Also delete `Accept-Encoding` from that request so the buffered body is readable.
- Otherwise stream the response with `io.Copy` into an `io.MultiWriter` of the client and, when `RewriteRequest` returned a non-empty key and `ObserveUsage` is set, a `usageTap`. Call `ObserveUsage(key, tap.usage())` after the copy returns. Flush after each write so SSE is not held back: wrap the `http.ResponseWriter` so `Write` calls the underlying `Flush` when the writer implements `http.Flusher`.
- An upstream transport error writes `502` with `{"type":"error","error":{"message":...}}` when headers have not been sent yet.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/proxy/ -v`
Expected: PASS, six tests.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/proxy.go internal/proxy/proxy_test.go
git commit -S -m "feat: proxy requests and read token usage off the response stream"
```

---

### Task 11: The Claude routing handler

Wires policy, router, state, and the proxy together. This is where sub-agent detection lives.

**Files:**
- Create: `internal/proxy/claude/claude.go`
- Test: `internal/proxy/claude/claude_test.go`

**Interfaces:**
- Consumes: everything built so far.
- Produces: `claude.New(r router.Router) *Handler`; `claude.Handler.Hooks() proxy.Hooks`.

Per-conversation state the handler keeps, capped at 50 entries with the oldest dropped:

```go
type convo struct {
	tier     string
	model    string
	cached   int
	output   int
	subAgent bool
	baseline string
	manual   bool
}
```

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/claude/claude_test.go`:

```go
package claude

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/proxy"
	"github.com/djalmaaraujo/llm-router/internal/router"
)

type fakeRouter struct {
	choice string
	conf   float64
	err    error
	calls  int
}

func (f *fakeRouter) Route(context.Context, router.Input) (*router.Decision, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &router.Decision{Choice: f.choice, Confidence: f.conf}, nil
}

func body(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

const turn = `{"model":"jev-router","tools":[{"name":"Read"}],
	"metadata":{"user_id":"{\"session_id\":\"s1\"}"},
	"messages":[{"role":"user","content":"%s"}]}`

func TestRoutesAFreshTurnAndRewritesTheSentinel(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-opus-5", conf: 0.95})
	b := body(t, strings.Replace(turn, "%s", "design the schema", 1))
	h.Hooks().RewriteRequest("/v1/messages", b)
	if b["model"] != "claude-opus-5" {
		t.Errorf("model = %v, want the sentinel replaced", b["model"])
	}
}

func TestPassesThroughAModelTheUserPicked(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	r := &fakeRouter{choice: "claude-opus-5", conf: 0.95}
	h := New(r)
	b := body(t, `{"model":"claude-sonnet-5","tools":[{"name":"Read"}],
		"metadata":{"user_id":"{\"session_id\":\"s1\"}"},"messages":[{"role":"user","content":"hi"}]}`)
	h.Hooks().RewriteRequest("/v1/messages", b)
	if b["model"] != "claude-sonnet-5" {
		t.Errorf("model = %v, want the user's choice untouched", b["model"])
	}
	if r.calls != 0 {
		t.Error("an explicit choice must not consult the router")
	}
}

func TestDoesNotRouteAToolLoopContinuation(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	r := &fakeRouter{choice: "claude-opus-5", conf: 0.95}
	h := New(r)
	first := body(t, strings.Replace(turn, "%s", "start the work", 1))
	h.Hooks().RewriteRequest("/v1/messages", first)

	cont := body(t, `{"model":"jev-router","tools":[{"name":"Read"}],
		"metadata":{"user_id":"{\"session_id\":\"s1\"}"},
		"messages":[{"role":"user","content":"start the work"},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"x"}]}]}`)
	h.Hooks().RewriteRequest("/v1/messages", cont)

	if r.calls != 1 {
		t.Errorf("router calls = %d, want 1: a continuation reuses the turn's tier", r.calls)
	}
	if cont["model"] != "claude-opus-5" {
		t.Errorf("model = %v, want the tier pinned for the turn", cont["model"])
	}
}

func TestTheFirstConversationIsMainAndLaterOnesAreSubAgents(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-opus-5", conf: 0.95})
	main := body(t, strings.Replace(turn, "%s", "the user's first prompt", 1))
	h.Hooks().RewriteRequest("/v1/messages", main)
	sub := body(t, strings.Replace(turn, "%s", "search the repo for X", 1))
	h.Hooks().RewriteRequest("/v1/messages", sub)

	if h.IsSubAgent(ConversationKey(main)) {
		t.Error("the first routed conversation in a session is the main one")
	}
	if !h.IsSubAgent(ConversationKey(sub)) {
		t.Error("a later conversation under the same session is a sub-agent")
	}
}

func TestUsageFeedsTheNextDecision(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-haiku-4-5-20251001", conf: 0.95})
	b := body(t, strings.Replace(turn, "%s", "first", 1))
	key := h.Hooks().RewriteRequest("/v1/messages", b)
	if key == "" {
		t.Fatal("a routed request must return a correlation key")
	}
	h.Hooks().ObserveUsage(key, proxy.Usage{CacheReadTokens: 94000, OutputTokens: 800})
	if got := h.Cached(key); got != 94000 {
		t.Errorf("cached tokens = %d, want the measured 94000 rather than an estimate", got)
	}
}

func TestRouterFailureKeepsTheCurrentModel(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{err: context.DeadlineExceeded})
	b := body(t, strings.Replace(turn, "%s", "anything", 1))
	h.Hooks().RewriteRequest("/v1/messages", b)
	if config.TierOf(b["model"].(string)) == "" {
		t.Errorf("model = %v, want a real model even when routing failed", b["model"])
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/proxy/claude/ -run Handler`
Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Write the implementation**

Create `internal/proxy/claude/claude.go`. Requirements, in the order `RewriteRequest` runs:

1. Walk `body["tools"]` and call `SanitizeSchema` on each `input_schema`.
2. If `!config.IsAuto(body["model"])`, the user picked a model and an explicit choice beats the router. Return `""` after, when `body["tools"]` is present, writing `state.Status{Manual: true, At: now}` for `SessionOf(body)`. Claude Code's own cheap auxiliary calls carry no tools and must not flip the status line to manual.
3. Otherwise take `key := ConversationKey(body)` and look up or create its `convo`.
4. Register the session's main conversation: the first key seen for a session id is main; any later key under that session sets `subAgent = true`. Store this in a `map[string]string` from session id to main key, guarded by the same mutex.
5. `prompt := NewTurnPrompt(body)`. If it is empty, or contains `<jev-explain>`, skip routing entirely and go to step 8 with the pinned tier.
6. Build the model list from the catalog, filtered by `config.AvailableTiers()`. Call the router with the pinned model (or the tier default) as `Current` and `convo.cached` as `ContextTokens`. Map the answer's model id back to a tier before handing it to `policy.Decide`, passing `CachedTokens`, `OutputTokens`, and `SubAgent` from the `convo`.
7. Store the decided tier and model on the `convo` and build the `state.Status`, including `BreakEven`, `Rebuild`, `SavingPerTurn`, and `Horizon` from the `policy.Outcome`.
8. Call `ApplyTier(body, convo.tier, convo.model)`. The sentinel is not a real model, so **every** routed request is rewritten, including continuations that reuse the turn's tier. When the tier is still unset, default to `"opus"`: that is what the prompt cache was most likely built on, and guessing high is the safe direction.
9. When a fresh decision was made, write it with `state.WriteDecision`, filed under `SessionOf(body)` or, when that is empty, the conversation key. `claude -p` omits metadata on the first request, so without that fallback the decision would be dropped.
10. Return `key`.

`ObserveUsage(key, u)` sets `convo.cached = u.CachedTotal()` and `convo.output = u.OutputTokens`.

`ObserveResponse` decodes `{"data":[...]}` and keeps each entry whose `id` maps to a tier in a catalog map. `BuffersResponse` matches `GET /v1/models`.

Expose `IsSubAgent(key string) bool` and `Cached(key string) int` for the tests. Guard every map with one `sync.Mutex`.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/proxy/claude/ -v`
Expected: PASS, all tests in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/claude/
git commit -S -m "feat: route Claude turns with measured cache usage and sub-agent awareness"
```

---

### Task 12: Launching the CLI

**Files:**
- Create: `internal/config/env.go`, `internal/launch/settings.go`, `internal/launch/launch.go`
- Test: `internal/config/env_test.go`, `internal/launch/settings_test.go`

**Interfaces:**
- Consumes: `config.AutoModel`, `config.Env`.
- Produces:
  - `config.LoadEnvFiles()` — reads `./.env`, `~/.llm-router.env`, `~/.jev-router.env`, `~/.jev-claude.env` in that order, never overwriting a variable already set in the real environment.
  - `config.APIKey() string` — first non-empty of `LLMR_API_KEY`, `JEV_API_KEY`, `TYPESAFE_API_KEY`.
  - `launch.ReadSavedModel(file string) string`
  - `launch.RestoreSavedModel(previous, file string) bool`
  - `launch.UserSettingsPath() string`
  - `launch.Resolve(name string) (string, error)`
  - `launch.Run(exe string, args []string, env []string) int`

- [ ] **Step 1: Write the failing tests**

Create `internal/config/env_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvFilesDoNotOverrideTheRealEnvironment(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("LLMR_API_KEY=from-file\nLLMR_DEBUG=1\n"), 0o600)
	t.Chdir(dir)
	t.Setenv("LLMR_API_KEY", "from-shell")
	t.Setenv("LLMR_DEBUG", "")

	LoadEnvFiles()

	if got := os.Getenv("LLMR_API_KEY"); got != "from-shell" {
		t.Errorf("LLMR_API_KEY = %q, want the shell value to win", got)
	}
	if got := os.Getenv("LLMR_DEBUG"); got != "1" {
		t.Errorf("LLMR_DEBUG = %q, want the file value where the shell is empty", got)
	}
}

func TestAPIKeyFallsBackThroughTheLegacyNames(t *testing.T) {
	t.Setenv("LLMR_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "ts")
	if got := APIKey(); got != "ts" {
		t.Errorf("APIKey = %q, want ts", got)
	}
	t.Setenv("JEV_API_KEY", "jev")
	if got := APIKey(); got != "jev" {
		t.Errorf("APIKey = %q, want jev to beat TYPESAFE_API_KEY", got)
	}
}
```

Create `internal/launch/settings_test.go`:

```go
package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, contents string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(file, []byte(contents), 0o600)
	return file
}

func TestReadSavedModelIgnoresTheSentinel(t *testing.T) {
	if got := ReadSavedModel(write(t, `{"model":"jev-router"}`)); got != "" {
		t.Errorf("got %q: a sentinel left by a crashed session is not a preference", got)
	}
	if got := ReadSavedModel(write(t, `{"model":"opus"}`)); got != "opus" {
		t.Errorf("got %q, want opus", got)
	}
	if got := ReadSavedModel(filepath.Join(t.TempDir(), "missing.json")); got != "" {
		t.Errorf("got %q, want empty for a missing file", got)
	}
}

func TestRestoreSavedModelPutsThePreviousValueBack(t *testing.T) {
	file := write(t, `{"model":"jev-router","theme":"dark"}`)
	if !RestoreSavedModel("opus", file) {
		t.Fatal("restore must report that it acted")
	}
	got, _ := os.ReadFile(file)
	if !strings.Contains(string(got), `"model": "opus"`) {
		t.Errorf("settings = %s", got)
	}
	if !strings.Contains(string(got), `"theme": "dark"`) {
		t.Error("restore must not drop unrelated settings")
	}
}

func TestRestoreSavedModelRemovesTheKeyWhenThereWasNone(t *testing.T) {
	file := write(t, `{"model":"jev-router"}`)
	RestoreSavedModel("", file)
	got, _ := os.ReadFile(file)
	if strings.Contains(string(got), "model") {
		t.Errorf("settings = %s, want the key gone", got)
	}
}

func TestRestoreSavedModelLeavesARealChoiceAlone(t *testing.T) {
	file := write(t, `{"model":"claude-opus-5"}`)
	if RestoreSavedModel("haiku", file) {
		t.Error("a model the user picked during the session must survive")
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/config/ ./internal/launch/`
Expected: FAIL, `undefined: LoadEnvFiles`, `undefined: ReadSavedModel`.

- [ ] **Step 3: Write the implementations**

`internal/config/env.go`:

- `LoadEnvFiles()` reads each file as `KEY=VALUE` lines, skipping blanks and lines starting with `#`, trimming surrounding single or double quotes from the value, and calls `os.Setenv` only when `os.Getenv(key) == ""`.
- `APIKey()` returns the first non-empty of the three names.

The `internal/log` package was built in Task 1; nothing to add here.

`internal/launch/settings.go`:

- `UserSettingsPath()` is `~/.claude/settings.json`.
- `ReadSavedModel(file)` parses the file and returns `model`, except when it equals `config.AutoModel`, which returns `""`. Any error returns `""`.
- `RestoreSavedModel(previous, file)` parses the file, returns `false` unless `model == config.AutoModel`, then deletes the key when `previous == ""` or sets it otherwise, and writes the file back with `json.MarshalIndent(v, "", "  ")` plus a trailing newline. Selecting a picker row with Enter makes Claude Code save it as the default for new sessions, and a saved sentinel would break plain `claude`, which has no proxy to resolve it.

`internal/launch/launch.go`:

- `Resolve(name)` walks `PATH` for an executable file called `name` and returns its full path, or an error naming what is missing.
- `Run(exe, args, env)` starts the process with `os.Stdin`, `os.Stdout`, and `os.Stderr` inherited, forwards `SIGINT` and `SIGTERM` to it, waits, and returns its exit code (1 when it died on a signal).

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/config/ ./internal/launch/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/env.go internal/config/env_test.go internal/launch/
git commit -S -m "feat: load env files and protect the saved default model"
```

---

### Task 13: A working llmr-claude

The first milestone you can actually use. After this task, routing works end to end.

**Files:**
- Create: `main.go`, `internal/launch/claude.go`
- Test: manual, described in the steps

**Interfaces:**
- Consumes: everything so far.
- Produces: `launch.Claude(args []string) int`; the binary itself.

- [ ] **Step 1: Write the dispatcher**

Create `main.go`:

```go
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
	name := strings.TrimSuffix(filepath.Base(argv[0]), ".exe")
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
		id = os.Getenv("LLMR_STATUS_ID")
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
```

Add a temporary stub so the package compiles before Task 15: in `internal/launch/launch.go`, `func Codex(args []string) int { fmt.Fprintln(os.Stderr, "llmr-codex arrives in a later task"); return 1 }` and `func Install() int { return 0 }`.

- [ ] **Step 2: Write the Claude launcher**

Create `internal/launch/claude.go`. Requirements:

- Resolve `claude` on `PATH`. When missing, print to stderr and return 1:
  ```
  [llmr] Claude Code is not installed, or `claude` is not on your PATH.
  [llmr] llmr-claude runs the real Claude Code CLI; install it first:
  [llmr]   https://code.claude.com/docs/en/setup
  ```
- Capture `ReadSavedModel(UserSettingsPath())` **before** starting anything, and restore it with `RestoreSavedModel` in a `defer`.
- When `config.APIKey() == ""`, print a note to stderr and run `claude` with the untouched environment. Routing is optional; a missing key must never stop the session.
- Otherwise start the proxy with `claude.New(jev.New(config.APIKey()))` against `https://api.anthropic.com` and add to the environment:

  | Variable | Value |
  | --- | --- |
  | `ANTHROPIC_BASE_URL` | `http://127.0.0.1:<port>` |
  | `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY` | `1` |
  | `ANTHROPIC_CUSTOM_MODEL_OPTION` | `jev-router` |
  | `ANTHROPIC_CUSTOM_MODEL_OPTION_NAME` | `Jev Router` |
  | `ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION` | `Route each turn to the cheapest model that can do it` |
  | `ANTHROPIC_CUSTOM_MODEL_OPTION_SUPPORTED_CAPABILITIES` | `thinking,adaptive_thinking,interleaved_thinking,effort,max_effort` |
  | `CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT` | `1` |
  | `ANTHROPIC_MODEL` | `jev-router`, only when not already set |

  `ANTHROPIC_MODEL` applies to this session only and is never written to settings, so the default costs nothing permanent and a model the user set themselves still wins.
- Unless `LLMR_NO_STATUSLINE` is set, add `--settings <file>` where the file contains `{"statusLine":{"type":"command","command":"<abs path to this binary> statusline"}}`, written under `os.TempDir()/llm-router/settings.json`. Skip this when either `./.claude/settings.json` or `~/.claude/settings.json` already defines `statusLine`: a status line the user configured is a deliberate choice, and overwriting it silently would be worse than showing nothing. Get the binary path from `os.Executable()`.
- Pass every other argument through untouched, then `return Run(exe, args, env)`.

- [ ] **Step 3: Build and check the help**

```bash
cd /Users/cooper/dev/llm-router
go build -o llm-router . && ./llm-router version && ./llm-router
```
Expected: `dev`, then the usage text, exit code 2.

- [ ] **Step 4: Run it for real against Claude Code**

```bash
ln -sf "$PWD/llm-router" /tmp/llmr-claude
LLMR_DEBUG=1 /tmp/llmr-claude -p "what is 2+2?"
```
Expected: an answer from Claude Code. If it fails, the error names what is wrong — do not move on until this prints an answer.

- [ ] **Step 5: Confirm the turn was actually routed**

```bash
cat "$(node -e 'console.log(require("os").tmpdir())' 2>/dev/null || echo "$TMPDIR")/llm-router/"*.json | head -40
```
Expected: JSON holding `tier`, `model`, `reason`, and `confidence` for the turn.

- [ ] **Step 6: Commit**

```bash
git add main.go internal/launch/
git commit -S -m "feat: run Claude Code through the router from a single binary"
```

---

### Task 14: Codex request-body handling

**Files:**
- Create: `internal/proxy/codex/body.go`
- Test: `internal/proxy/codex/body_test.go`

**Interfaces:**
- Consumes: `config`.
- Produces: `codex.NewTurnPrompt(body map[string]any) string`; `codex.ConversationKey(body map[string]any) string`; `codex.AddJevModel(catalog map[string]any) map[string]any`; `codex.ModelFor(tier string) string`; `codex.CodexAutoModel` constant, value `jev-router`.

Codex differs from Claude in three ways that the tests pin down: tool definitions live inside the Responses API `input` array as an `additional_tools` item rather than in a `tools` field; `prompt_cache_key` gives a stable conversation key directly; and the tier names map to Codex model slugs.

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/codex/body_test.go`:

```go
package codex

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNewTurnPromptNeedsTheToolsItem(t *testing.T) {
	without := decode(t, `{"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	if got := NewTurnPrompt(without); got != "" {
		t.Errorf("got %q, want empty: no additional_tools means an auxiliary call", got)
	}
	with := decode(t, `{"input":[{"type":"additional_tools"},
		{"role":"user","content":[{"type":"input_text","text":"fix the parser"}]}]}`)
	if got := NewTurnPrompt(with); got != "fix the parser" {
		t.Errorf("got %q", got)
	}
}

func TestNewTurnPromptStopsAtAToolOutput(t *testing.T) {
	body := decode(t, `{"input":[{"type":"additional_tools"},
		{"role":"user","content":[{"type":"input_text","text":"start"}]},
		{"type":"function_call_output","output":"done"}]}`)
	if got := NewTurnPrompt(body); got != "" {
		t.Errorf("got %q, want empty: a continuation is not a new turn", got)
	}
}

func TestConversationKeyPrefersThePromptCacheKey(t *testing.T) {
	main := decode(t, `{"prompt_cache_key":"main","input":[]}`)
	sub := decode(t, `{"prompt_cache_key":"sub-agent","input":[]}`)
	if ConversationKey(main) == ConversationKey(sub) {
		t.Error("Codex names the conversation itself; the key must follow it")
	}
	if ConversationKey(main) != ConversationKey(decode(t, `{"prompt_cache_key":"main","input":[{"x":1}]}`)) {
		t.Error("the key must not move when the input grows")
	}
}

func TestAddJevModelAddsExactlyOneRow(t *testing.T) {
	catalog := decode(t, `{"models":[{"slug":"gpt-5.6-terra","visibility":"list"}]}`)
	once := AddJevModel(catalog)
	twice := AddJevModel(once)
	rows := twice["models"].([]any)
	seen := 0
	for _, r := range rows {
		if r.(map[string]any)["slug"] == CodexAutoModel {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("found %d sentinel rows, want exactly 1", seen)
	}
}

func TestModelForReadsTheOverrides(t *testing.T) {
	t.Setenv("LLMR_CODEX_FAST_MODEL", "")
	t.Setenv("JEV_CODEX_FAST_MODEL", "")
	if got := ModelFor("haiku"); got != "gpt-5.6-luna" {
		t.Errorf("got %q, want the default fast model", got)
	}
	t.Setenv("LLMR_CODEX_FAST_MODEL", "my-fast")
	if got := ModelFor("haiku"); got != "my-fast" {
		t.Errorf("got %q, want the override", got)
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/proxy/codex/`
Expected: FAIL, package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/proxy/codex/body.go`. Requirements:

- `CodexAutoModel = "jev-router"`.
- Tier to Codex slug, each overridable by `LLMR_CODEX_*_MODEL` then `JEV_CODEX_*_MODEL` through `config.Env`: `haiku` to `gpt-5.6-luna` (`CODEX_FAST_MODEL`), `sonnet` to `gpt-5.6-terra` (`CODEX_BALANCED_MODEL`), `opus` to `gpt-5.6-sol` (`CODEX_STRONG_MODEL`), `fable` to `gpt-6-astra` (`CODEX_LONG_MODEL`).
- `NewTurnPrompt` returns `""` unless some item in `input` has `type == "additional_tools"`. It then walks `input` backwards: an item whose `type` is `function_call_output` or `custom_tool_call_output` returns `""`; the first item with `role == "user"` yields the joined text of its content blocks, cleaned the same way as the Claude side and returned only when it is not one of Codex's own auxiliary prompts. Treat a prompt that begins with `<user_instructions>` or `<environment_context>` as auxiliary.
- `ConversationKey` hashes the first present of `prompt_cache_key`, `client_metadata["x-codex-turn-metadata"]`, or `instructions + "|" + <first user text>`, and returns the first 12 hex characters of the SHA-1.
- `AddJevModel(catalog)` returns the catalog unchanged when `models` is missing or already holds a row whose `slug` is the sentinel. Otherwise it copies the row for `gpt-5.6-terra`, or the first row with `visibility == "list"`, sets `slug` to the sentinel and the display name to `Jev Router`, and appends it.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/proxy/codex/ -v`
Expected: PASS, five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/codex/
git commit -S -m "feat: read and rewrite Codex Responses API bodies"
```

---

### Task 15: The Codex proxy and launcher

**Files:**
- Create: `internal/proxy/codex/codex.go`, `internal/launch/codex.go`
- Modify: `internal/launch/launch.go` (remove the `Codex` stub)
- Test: `internal/proxy/codex/codex_test.go`

**Interfaces:**
- Consumes: `proxy.Hooks`, `router.Router`, `policy.Decide`, `state`.
- Produces: `codex.New(r router.Router) *Handler` with `Hooks() proxy.Hooks`; `launch.Codex(args []string) int`.

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/codex/codex_test.go` with three tests, following the shape of `internal/proxy/claude/claude_test.go`:

1. `TestRoutesAFreshCodexTurn` — a body with `additional_tools` and a user item, `model` set to the sentinel, routed through a fake router answering `gpt-5.6-sol`, must come out with `model` set to `gpt-5.6-sol`.
2. `TestPassesThroughAModelTheUserPicked` — `model: "gpt-5.6-terra"` must survive and the fake router must record zero calls.
3. `TestDoesNotRouteAContinuation` — a second request ending in `function_call_output` must reuse the tier and leave the router call count at 1.

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/proxy/codex/ -run Routes`
Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Write the handler**

Create `internal/proxy/codex/codex.go`, mirroring the Claude handler with these differences:

- The rewritten path is `/responses` (and anything ending in `/responses`).
- The chosen tier maps through `ModelFor(tier)` rather than a Claude model id.
- There is no capability stripping: Codex composes its own body and the Responses API takes the model id as given.
- Codex's ChatGPT backend may stream SSE without a `Content-Type` header, so the proxy must not decide streaming from that header. Treat any response body whose first bytes begin with `event:` or `data:` as a stream.
- `BuffersResponse` matches the model catalog request, and `ObserveResponse` runs the body through `AddJevModel` before it is written back, so the sentinel appears in the native picker.

- [ ] **Step 4: Write the launcher**

Create `internal/launch/codex.go`. Requirements:

- Resolve `codex` on `PATH`, with the same shape of error message as the Claude launcher, pointing at `https://developers.openai.com/codex/cli`.
- Write a temporary Codex config that declares a provider named `jev-router` with `base_url` pointing at the proxy and `requires_openai_auth = true`, then pass it to `codex` with `--config`. The existing `codex login` credentials are forwarded by the proxy untouched; no OpenAI API key is needed.
- Select `jev-router` as the model for the session.
- Delete the temporary config on exit.
- Each fresh decision prints one commentary line to stderr: `[llmr] routed this turn to gpt-5.6-sol (jev, confidence 0.91).`
- When `config.APIKey()` is empty, run Codex untouched after a note on stderr.

Remove the `Codex` stub from `internal/launch/launch.go`.

- [ ] **Step 5: Run the tests and build**

```bash
go test ./... && go build -o llm-router .
```
Expected: all pass, binary builds.

- [ ] **Step 6: Run it for real**

```bash
ln -sf "$PWD/llm-router" /tmp/llmr-codex
LLMR_DEBUG=1 /tmp/llmr-codex exec "what is 2+2?"
```
Expected: an answer, and a `[llmr] routed this turn to ...` line on stderr.

- [ ] **Step 7: Commit**

```bash
git add internal/proxy/codex/ internal/launch/
git commit -S -m "feat: route Codex turns through a temporary custom provider"
```

---

### Task 16: The explanation skill and the install command

**Files:**
- Create: `skills/llmr-explain/SKILL.md`
- Modify: `internal/launch/launch.go` (replace the `Install` stub)
- Test: `internal/launch/install_test.go`

**Interfaces:**
- Produces: `launch.Install() int`; `launch.SkillContents() []byte`.

- [ ] **Step 1: Write the skill**

Create `skills/llmr-explain/SKILL.md`:

```markdown
---
name: llmr-explain
description: Show why llm-router selected the model used for the last prompt.
disable-model-invocation: true
allowed-tools: Bash(llm-router *)
---

<jev-explain>
Return the report below verbatim in a plain text code block. Do not add analysis or use tools.

!`llm-router explain "${CLAUDE_SESSION_ID}"`
```

The `<jev-explain>` marker must stay exactly as written. The Claude proxy matches that literal string in a prompt to skip routing the turn that asks for the report, so renaming it would make asking for an explanation cost a routing call and overwrite the decision being explained.

- [ ] **Step 2: Write the failing test**

Create `internal/launch/install_test.go`:

```go
package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallWritesTheSkillAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if code := Install(); code != 0 {
		t.Fatalf("Install returned %d", code)
	}
	path := filepath.Join(home, ".claude", "skills", "llmr-explain", "SKILL.md")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill not written: %v", err)
	}
	if !strings.Contains(string(first), "<jev-explain>") {
		t.Error("the marker the proxy matches must be present")
	}

	if code := Install(); code != 0 {
		t.Fatalf("second Install returned %d", code)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Error("installing twice must leave the same file")
	}
}
```

- [ ] **Step 3: Run the test and watch it fail**

Run: `go test ./internal/launch/ -run Install`
Expected: FAIL, `Install` writes nothing.

- [ ] **Step 4: Write the implementation**

In `internal/launch/launch.go`:

```go
//go:embed all:../../skills
var skillFS embed.FS
```

`Install()` writes `skills/llmr-explain/SKILL.md` to `~/.claude/skills/llmr-explain/SKILL.md` and `~/.codex/skills/llmr-explain/SKILL.md`, creating directories with `0o755` and the file with `0o644`, overwriting whatever is there so an upgrade refreshes it. It prints one line per path written and returns 0; a failure prints the error and returns 1.

Call `Install()` from `launch.Claude` and `launch.Codex` at startup, ignoring failure, so the skill is available from any repository without separate setup.

- [ ] **Step 5: Run the tests and watch them pass**

Run: `go test ./internal/launch/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add skills/ internal/launch/
git commit -S -m "feat: install the explanation skill for Claude Code and Codex"
```

---

### Task 17: Wire parity against the Node version

This is what proves the port. It runs in CI, so a future change cannot quietly break the wire format.

**Files:**
- Create: `testdata/bodies/README.md`, `internal/proxy/claude/parity_test.go`
- Create: `testdata/bodies/*.json` (recorded, not written by hand)

- [ ] **Step 1: Record real bodies from the Node version**

```bash
cd /Users/cooper/dev/jev-router
npm install
mkdir -p /tmp/jevdump
JEV_DUMP=/tmp/jevdump/body JEV_API_KEY="$(grep -h '^JEV_API_KEY=' ~/.jev-router.env | cut -d= -f2-)" \
  node bin/jev-claude.mjs -p "rename this variable"
```

Then run a second session that uses a sub-agent and a third with MCP tools loaded, so the corpus covers more than one shape.

- [ ] **Step 2: Copy six bodies into the corpus**

Pick one file for each shape and copy it into `/Users/cooper/dev/llm-router/testdata/bodies/` with these exact names:

| Name | Shape |
| --- | --- |
| `first-turn.json` | opening request of a session, tools present, typed user text |
| `tool-continuation.json` | request whose last message holds a `tool_result` |
| `sub-agent.json` | a request under the same session with different opening text |
| `mcp-schemas.json` | a body whose tools carry draft-04 `exclusiveMinimum` booleans |
| `thinking-and-edits.json` | a body with `thinking` and a `context_management` edit list |
| `large-integers.json` | any body with `max_tokens` at or above 1000000 |

If a shape did not occur naturally, hand-edit the closest recorded file rather than invent one from nothing, and note what was changed in `testdata/bodies/README.md`.

- [ ] **Step 3: Write the parity test**

Create `internal/proxy/claude/parity_test.go`:

```go
package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Every recorded body must survive a decode and re-encode unchanged except for
// key order. This is the guard against UseNumber being forgotten: without it,
// max_tokens comes back as 1e+06.
func TestRecordedBodiesRoundTripWithoutLoss(t *testing.T) {
	files, err := filepath.Glob("../../../testdata/bodies/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no recorded bodies found: %v", err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&decoded); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		out, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}

		var before, after any
		json.Unmarshal(raw, &before)
		json.Unmarshal(out, &after)

		var a, b bytes.Buffer
		json.NewEncoder(&a).Encode(before)
		json.NewEncoder(&b).Encode(after)
		if a.String() != b.String() {
			t.Errorf("%s changed on round trip\n before: %s\n  after: %s", filepath.Base(file), a.String(), b.String())
		}
	}
}

func TestRecordedBodiesRewriteToTheExpectedModel(t *testing.T) {
	cases := map[string]string{
		"first-turn.json":          "claude-haiku-4-5-20251001",
		"tool-continuation.json":   "claude-haiku-4-5-20251001",
		"thinking-and-edits.json":  "claude-haiku-4-5-20251001",
	}
	for name, want := range cases {
		raw, err := os.ReadFile(filepath.Join("../../../testdata/bodies", name))
		if err != nil {
			t.Skipf("%s not recorded yet", name)
		}
		var body map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		dec.Decode(&body)

		ApplyTier(body, "haiku", want)
		if body["model"] != want {
			t.Errorf("%s: model = %v, want %v", name, body["model"], want)
		}
		if _, still := body["thinking"]; still {
			t.Errorf("%s: thinking must be stripped for haiku", name)
		}
	}
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/proxy/claude/ -run Recorded -v`
Expected: PASS. A failure on the round-trip test means a decode somewhere is missing `UseNumber`.

- [ ] **Step 5: Commit**

```bash
git add testdata/ internal/proxy/claude/parity_test.go
git commit -S -m "test: prove recorded request bodies survive the Go round trip"
```

---

### Task 18: CI and the release configuration

Files only. **Nothing is released in this task.** Task 19 is the gate.

**Files:**
- Create: `.github/workflows/ci.yml`, `.goreleaser.yaml`, `.github/workflows/release.yml`, `README.md`, `LICENSE`

- [ ] **Step 1: Write CI**

Create `.github/workflows/ci.yml` running on push and pull request: check out, set up Go 1.26, then `gofmt -l .` failing when it prints anything, `go vet ./...`, and `go test ./...`.

- [ ] **Step 2: Write the release configuration**

Create `.goreleaser.yaml` modelled on `djalmaaraujo/piper`:

- one build, `main: .`, binary `llm-router`, `CGO_ENABLED=0`, ldflags `-s -w -X main.version={{.Version}}`;
- `goos: [darwin, linux]`, `goarch: [amd64, arm64]`;
- archives named `llm-router_{{ .Os }}_{{ .Arch }}.tar.gz`;
- a `homebrew_casks` entry publishing to `djalmaaraujo/homebrew-tap`, with `binary "llm-router"`, the three symlinks, a `postflight` that runs `xattr -dr com.apple.quarantine` on macOS, and `livecheck { skip "Auto-generated on release." }`.

The cask creates the command names:

```ruby
  binary "llm-router"
  binary "llm-router", target: "llmr-claude"
  binary "llm-router", target: "llmr-codex"
  binary "llm-router", target: "llmr-explain"
```

- [ ] **Step 3: Write the release workflow**

Create `.github/workflows/release.yml` triggering only on tags matching `v*`, checking out with `fetch-depth: 0`, setting up Go 1.26, and running `goreleaser release --clean` with `GITHUB_TOKEN` and a `HOMEBREW_TAP_TOKEN` secret.

- [ ] **Step 4: Validate without releasing**

```bash
goreleaser check
goreleaser build --snapshot --clean --single-target
./dist/*/llm-router version
```
Expected: `goreleaser check` reports the config is valid, the snapshot builds, and the binary prints a version.

- [ ] **Step 5: Write the README**

Cover: what it does, `brew install djalmaaraujo/tap/llm-router`, the four commands, where to put the API key, the environment variables including `LLMR_SWITCH_HORIZON`, and a short section on the cache rule with the break-even table from the spec. State plainly that a native binary does not make routing faster and why the rewrite was worth doing anyway.

- [ ] **Step 6: Commit**

```bash
git add .github/ .goreleaser.yaml README.md LICENSE
git commit -S -m "build: add CI and the GoReleaser configuration for the Homebrew cask"
```

---

### Task 19: Verification, which gates the release

**Nothing has been released. This task decides whether anything is.** Work through every check and record the result. A failure sends you back to the task that owns it; it does not get waived.

- [ ] **Step 1: The whole suite, clean**

```bash
cd /Users/cooper/dev/llm-router
gofmt -l . && go vet ./... && go test ./... -count=1
```
Expected: no `gofmt` output, no vet findings, every package passes. Record the test count.

- [ ] **Step 2: No dependencies crept in**

```bash
grep -c "^require" go.mod || echo "0 require blocks, correct"
go list -m all | wc -l
```
Expected: `go.mod` has no `require` block and `go list -m all` prints one line, the module itself.

- [ ] **Step 3: Build and link the three commands**

```bash
go build -o llm-router . && mkdir -p /tmp/llmr-bin
for n in llmr-claude llmr-codex llmr-explain; do ln -sf "$PWD/llm-router" "/tmp/llmr-bin/$n"; done
PATH="/tmp/llmr-bin:$PATH" llmr-explain
```
Expected: `llm-router: no routing decision has been recorded for this session.`

- [ ] **Step 4: Claude Code, live**

```bash
PATH="/tmp/llmr-bin:$PATH" LLMR_DEBUG=1 llmr-claude
```

In the session, confirm each of these and record what you saw:

- `/model` lists **Jev Router** and it is selected.
- Ask something trivial, such as `what is 2+2?`. The status line shows a routed model and a confidence.
- Ask something hard, such as `design a lock-free ring buffer and justify the memory ordering`. The status line shows a stronger tier than the trivial question got.
- Run `/llmr-explain`. The report renders, and the numbers match the status line.
- Keep working until `/context` passes 50 000 tokens, then compare: the context tokens in `~/.llm-router.log` must be within a few percent of what `/context` reports. **This is the check that the usage tap reads the right numbers.** If they disagree, the cache arithmetic is running on bad input and Task 10 is wrong.
- Trigger a sub-agent, for example by asking for a broad codebase search. Confirm the log shows a separate conversation key routing on its own, and that the main conversation's tier did not move because of it.
- Pick a concrete model in `/model`. The status line switches to `⏸ manual` and the log shows passthrough. Pick **Jev Router** again and confirm routing resumes.
- Exit, then run plain `claude` and confirm `/model` is back to what it was before, not `jev-router`.

- [ ] **Step 5: The cache actually survives**

```bash
grep -E "ctx~|served by" ~/.llm-router.log | tail -30
```
Expected: across a long conversation on one tier, `cache_read` stays high and no large unexplained `cache_creation` appears between turns. Where a switch did happen, the log line shows the break-even number that justified it.

- [ ] **Step 6: Routing fails open**

```bash
PATH="/tmp/llmr-bin:$PATH" LLMR_DEBUG=1 LLMR_API_KEY=obviously-invalid llmr-claude -p "what is 2+2?"
```
Expected: an answer. The log reports the routing failure and that the current model was kept. **A blocked prompt here is a release blocker.**

- [ ] **Step 7: Codex, live**

```bash
PATH="/tmp/llmr-bin:$PATH" LLMR_DEBUG=1 llmr-codex exec "what is 2+2?"
```
Expected: an answer, and a `[llmr] routed this turn to ...` line. Then start `llmr-codex` interactively and confirm `/model` lists **Jev Router**.

- [ ] **Step 8: Install rehearsal, no Node**

```bash
goreleaser build --snapshot --clean --single-target
env -i HOME="$HOME" PATH=/usr/bin:/bin TERM="$TERM" ./dist/*/llm-router version
env -i HOME="$HOME" PATH=/usr/bin:/bin TERM="$TERM" ./dist/*/llm-router explain
```
Expected: both work with a stripped environment and no Node on `PATH`.

Then install from a local tap and confirm the cask resolves:

```bash
brew tap-new local/test --no-git 2>/dev/null || true
cp dist/homebrew/Casks/llm-router.rb "$(brew --repository)/Library/Taps/local/homebrew-test/Casks/" 2>/dev/null \
  || echo "check where goreleaser wrote the cask and copy it"
brew audit --cask --strict local/test/llm-router || true
```
Expected: `brew audit` reports no errors that block installation.

- [ ] **Step 9: Write the verification record**

Create `docs/verification-2026-09-21.md` with one line per check above and what actually happened — the real numbers, not "passed". Include the context-token comparison from Step 4 and the test count from Step 1.

- [ ] **Step 10: Commit and stop**

```bash
git add docs/verification-2026-09-21.md
git commit -S -m "docs: record the verification run before the first release"
```

**Stop here.** Report the results and ask whether to cut `v0.1.0`. Pushing a tag is the only thing that publishes, and it is not part of this plan.
