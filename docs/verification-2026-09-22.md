# Verification run — 2026-09-22

Task 19. This is the last gate before `v0.1.0`. Nothing was pushed, tagged, or
released as part of this run. Numbers below are what was actually observed,
not a pass/fail label.

## Part A — the suite, clean

```
gofmt -l .        -> no output
go vet ./...      -> no output
go test ./... -count=1
go test ./... -race -count=1
```

`gofmt -l .` printed nothing. `go vet ./...` printed nothing.

`go test ./... -count=1 -v`: **165 tests passed, 0 failed**, across 11
packages with test files (`internal/config`, `internal/cost`,
`internal/explain`, `internal/launch`, `internal/log`, `internal/policy`,
`internal/proxy`, `internal/proxy/claude`, `internal/proxy/codex`,
`internal/router/jev`, `internal/state`, `internal/statusline`). Two packages
have no test files (`internal/router`, `skills`); the root package and
`skills` package also report "no test files".

`go test ./... -race -count=1`: every package reported `ok`, no data races
detected.

## Part B — no dependencies crept in

`go.mod`:

```
module github.com/djalmaaraujo/llm-router

go 1.26
```

Zero `require` entries. `go list -m all` printed exactly one line:
`github.com/djalmaaraujo/llm-router`.

## Part C — build and the three commands

`go build -o llm-router .` succeeded.

- `PATH=... llmr-explain` with no prior decision:
  `llm-router: no routing decision has been recorded for this session.`
  (exit 0). Matches the brief exactly.
- `./llm-router version` printed `dev`. This is the local dev build (no
  `-ldflags -X main.version=...` was passed here); the real release build
  goes through `goreleaser`, which does inject the version (see Part G).
  Noted, not a defect.
- `./llm-router` with no subcommand printed the usage banner listing
  `llmr-claude`, `llmr-codex`, `llmr-explain`, `install`, `version`, and
  exited 2.

## Part D — Claude, live, non-interactive

Both calls used the real TypeSafe key from `~/.jev-router.env` (never
printed).

**Prompt 1: "what is 2+2?"**
Answer: `4.`
Recorded decision (`/tmp/llm-router/53d474c5-....json`):
- tier: `haiku`, model: `claude-haiku-4-5-20251001`
- reason: `jev`, confidence: `0.99`
- metrics: taskComplexity 0.0089, reasoningRequired 0.0444, toolComplexity
  0.0022, contextSize 0

**Prompt 2: "design a lock-free single-producer single-consumer ring buffer
and justify the memory ordering on each atomic operation"**
Answer: a full design with producer/consumer C++ code, a table justifying
each atomic's memory order (relaxed/acquire/release), a section on why not
`seq_cst`, and correctness notes on the cached indices. Substantively
correct and complete.
Recorded decision (`/tmp/llm-router/8e1d032c-....json`):
- tier: `opus`, model: `claude-opus-5`
- reason: `jev`, confidence: `0.74`
- metrics: taskComplexity 0.636, reasoningRequired 0.687, toolComplexity
  0.0056, contextSize 0

**The hard prompt routed to a strictly stronger tier** (opus vs. haiku), at
lower confidence (0.74 vs. 0.99) — expected, since a harder judgment call is
inherently less certain than "this is trivial."

`llmr-explain 8e1d032c-...` output:

```
┌─────────────────────────────────┐
│ Jev request                     │
│ Prompt: design a lock-free      │
│ single-producer single-consumer │
│ ring buffer and justify the     │
│ memory ordering on each atomic  │
│ operation                       │
│ Current tier: CLAUDE-OPUS-5     │
│ Context tokens: 0               │
│                                 │
│ Jev response                    │
│ Task complexity     0.64        │
│ Reasoning required  0.69        │
│ Tool complexity     0.01        │
│ Context size        0.00        │
│                                 │
│ Selected model: CLAUDE-OPUS-5   │
│ Confidence: 74%                 │
└─────────────────────────────────┘
Decision: router recommendation
```

Numbers match the recorded decision file exactly.

## Part E — fail-open

**Invalid key** (`LLMR_API_KEY=obviously-invalid`): still answered `4`.
`~/.llm-router.log` recorded:
```
router failed: systemone: http 401
routed key=8d463b358eac tier= reason=jev-unavailable/no-change
```
The router failed closed on its own call but the launcher failed open on the
user's turn — Claude Code kept running the model already in use. This is the
correct, required behavior.

**No key anywhere** (`env -u LLMR_API_KEY -u JEV_API_KEY -u TYPESAFE_API_KEY
HOME=/tmp/llmr-nohome`): printed
`[llmr] no API key found, running Claude Code without routing` and launched
Claude Code unrouted, as designed. Claude Code itself then exited with
`Not logged in · Please run /login` — expected, since `/tmp/llmr-nohome` has
no Claude Code credentials; that is a side effect of using a throwaway HOME
for this test, not a router defect. The router's own contract (detect
missing key, say so, still run the underlying tool) held.

## Part F — Codex, live

`llmr-codex exec "what is 2+2?"`: answer `4`. The line
`[llmr] routed this turn to gpt-5.6-luna (jev, confidence 0.99).` appeared.
Recorded decision matched: tier `haiku`, model `gpt-5.6-luna`, reason `jev`,
confidence `0.99`.

One unrelated line of noise appeared in this run: `hook: SessionStart` /
`hook: SessionStart Failed`. This is a Codex-side hook, not part of
llm-router's own output; not investigated further here since it did not
block the answer or the routing line.

## Part G — the cask, rendered and audited

`goreleaser release --snapshot --clean --skip=publish` succeeded (version
`0.0.1-dev`, built for darwin/linux amd64+arm64).

Rendered cask (`dist/homebrew/Casks/llm-router.rb`):

```ruby
# This file was generated by GoReleaser. DO NOT EDIT.
cask "llm-router" do
  binary "llm-router", target: "llmr-claude"
  binary "llm-router", target: "llmr-codex"
  binary "llm-router", target: "llmr-explain"

  version "0.0.1-dev"

  on_macos do
    on_intel do
      sha256 "8a388efeeb3f89d5455096d0340fd211417275d7aa25fbca25006231560f6da6"
      url "https://github.com/djalmaaraujo/llm-router/releases/download/v0.0.0/llm-router_darwin_amd64.tar.gz"
    end
    on_arm do
      sha256 "c557d97e49f8637bcf1ab0155f1ea2da713fb339d4077c1472b0d6af2a9b9412"
      url "https://github.com/djalmaaraujo/llm-router/releases/download/v0.0.0/llm-router_darwin_arm64.tar.gz"
    end
  end

  on_linux do
    on_intel do
      sha256 "fc73f2de02071f02d1732e0684097ab06a785c645886473c4ea9a5941ce03cc6"
      url "https://github.com/djalmaaraujo/llm-router/releases/download/v0.0.0/llm-router_linux_amd64.tar.gz"
    end
    on_arm do
      sha256 "13f3cdb7f29ca5f19ccbc7732e88a3011018e98ebbc264195c918767012614e4"
      url "https://github.com/djalmaaraujo/llm-router/releases/download/v0.0.0/llm-router_linux_arm64.tar.gz"
    end
  end

  name "llm-router"
  desc "Route each Claude Code or Codex turn to the cheapest model that can do it"
  homepage "https://github.com/djalmaaraujo/llm-router"

  livecheck do
    skip "Auto-generated on release."
  end

  binary "llm-router"

  postflight do
    if OS.mac?
      system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{staged_path}/llm-router"]
    end
  end

  # No zap stanza required

end
```

`livecheck do` count: **1**. `target:` count: **3**. Both match the
requirement.

**Finding — extra unnamed binary stanza.** The cask has a fourth,
unqualified `binary "llm-router"` line (no `target:`) below the three named
ones from the `custom_block`. This is emitted by GoReleaser's cask template
by default and is not present in `.goreleaser.yaml`'s `custom_block`. Net
effect: installing the cask links a fourth shim, `llm-router`, into the
Caskroom bin, in addition to `llmr-claude`/`llmr-codex`/`llmr-explain`. The
binary itself supports being invoked directly (`version`, `explain`,
`install`), so this is not obviously broken, but it is undocumented and
worth a deliberate decision (keep it and document it, or suppress it) rather
than shipping it silently.

**Audit.** Loaded into a throwaway tap
(`djalmaaraujo/llmr-verify`) and ran:

```
$ brew audit --cask --strict djalmaaraujo/llmr-verify/llm-router
Error: 1 problem in 1 cask detected.
audit for llm-router: failed
 - Use `sha256 :no_check` when URL is unversioned.
djalmaaraujo/llmr-verify/llm-router
  * Use `sha256 :no_check` when URL is unversioned.
```

**Finding — the "accepted residual" premise does not hold.** The brief
states this same complaint reproduces against the owner's shipped
`djalmaaraujo/piper` cask and therefore does not block. Checked directly:

```
$ brew audit --cask --strict djalmaaraujo/tap/piper
(no output, exit 0)
```

Piper's audit is **clean** — the warning does not reproduce there. The
difference: piper's cask interpolates `url "...v#{version}/piper_....tar.gz"`
(a `version` field plus Ruby string interpolation), while llm-router's
snapshot render bakes a literal tag into the URL
(`.../v0.0.0/llm-router_....tar.gz`) with a `version "0.0.1-dev"` field that
the URL never references. `brew audit` flags a cask as "unversioned" when
the URL doesn't reference the `version` field at all, which is exactly the
shape here. This may be a snapshot-mode artifact (no real tag exists yet, so
GoReleaser can't build a real `{{ .Tag }}`-templated URL) rather than what a
real tagged release would produce — but it was not possible to confirm that
from a snapshot build. **This should be checked against a real (non-snapshot)
release dry run, or by adding an explicit `url_template` to
`homebrew_casks` in `.goreleaser.yaml` that mirrors piper's `#{version}`
pattern, before assuming the warning is harmless.**

Cleaned up: `brew untap djalmaaraujo/llmr-verify` (untapped, 6 files
removed) and `dist/` deleted. `djalmaaraujo/tap` was not touched.

## Part H — runs without Node

```
env -i HOME="$HOME" PATH=/usr/bin:/bin TERM="$TERM" ./llm-router version
-> dev

env -i HOME="$HOME" PATH=/usr/bin:/bin TERM="$TERM" ./llm-router explain
-> llm-router: no routing decision has been recorded for this session.
```

Both ran cleanly with `PATH=/usr/bin:/bin` only — no Node anywhere on that
path (confirmed `node` only resolves under `~/.local/share/mise/...`, which
was excluded). This is the core promise of the Go rewrite, and it held.

## Part I — the skill installs

Both launcher invocations in this session installed the skill:

- `/Users/cooper/.claude/skills/llmr-explain/SKILL.md` — modified
  2026-09-22 00:07, contains `<jev-explain>`.
- `/Users/cooper/.codex/skills/llmr-explain/SKILL.md` — modified
  2026-09-22 00:07, contains `<jev-explain>`.

Both files are 326 bytes, written on every launch (idempotent overwrite, as
seen from the `[llmr] wrote ...` lines on every command above).

## Part J — the stderr noise

`[claude-code:unrecognized_model] {"model":"jev-router","query_source":"sdk"}`
appears before every answer, with or without `LLMR_DEBUG=1` — confirmed by
running `llmr-claude -p "what is 3+3?"` with no debug flag; the line still
printed. So it is not gated by this project's debug flag, and
`CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT=1` (already set by the
launcher) does not touch it — that variable suppresses a different check
(context-window enforcement for unknown models), not this diagnostic print.
No other Claude Code env var found (checked `claude --help`) controls it.

Checked the Node original (`~/dev/jev-router`, `node bin/jev-claude.mjs -p
"what is 5+5?"`): **same line appears there too**, byte for byte. This
confirms the noise is inherent to Claude Code's SDK reacting to any
model name it doesn't recognize (`jev-router`), not something the Go
rewrite introduced or can silence from the outside — attempts to suppress
it would mean filtering the child process's stderr, which risks eating a
real error alongside it.

**Recommendation: document it in the README as a known, harmless line**,
rather than try to silence it. It predates this rewrite and comes from
Claude Code itself.

## Summary — what failed or looked wrong

1. **Cask has an extra, unnamed `binary "llm-router"` stanza** beyond the
   three named targets. Not necessarily broken, but undocumented — a
   decision, not an accident, should own it.
2. **The brief's assumption that the `sha256 :no_check` audit warning is a
   known, harmless residual (because it reproduces on `djalmaaraujo/piper`)
   is false.** Piper's audit is clean. The warning is specific to how this
   snapshot cask's URL was rendered (literal tag, not `#{version}`
   interpolation) and needs to be checked against a real tagged release
   before being waved through.
3. `./llm-router version` reports `dev` in a local build — expected, not a
   bug, since no `-ldflags` were passed; confirm the real release build
   (via `goreleaser`) stamps the actual version, which the earlier
   `goreleaser release --snapshot` run does appear to do correctly
   (`version=0.0.1-dev` in its own log).
4. Everything else — the full test suite (165 tests, 0 failures, no races),
   zero dependencies, the three commands, live routing on both tiers with a
   correct escalation on the hard prompt, fail-open behavior on both a bad
   key and no key, Codex routing, the no-Node run, and the skill install —
   behaved exactly as specified.

## Recommendation

Do not cut `v0.1.0` yet: resolve finding #2 first, since an audit warning
that turns out not to have precedent is exactly the kind of thing that
"looked fine to `goreleaser check`" the last time this shipped a broken
cask — confirm what a real, non-snapshot release renders for the cask URL
(and decide, deliberately, whether to keep or drop the unnamed `binary
"llm-router"` stanza) before tagging.
