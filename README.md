# llm-router

Automatic per-turn model routing for Claude Code and OpenAI Codex. `llm-router` sends simple
work to a cheap model and hard work to a strong one, while keeping each CLI's own interface,
tools, sessions, permissions, and login untouched.

This is a Go rewrite of, and a derivative work from,
[`gargpratyush/jev-router`](https://github.com/gargpratyush/jev-router) (MIT). It ships as one
static binary with three command names, installed through Homebrew.

## Install

```bash
brew install djalmaaraujo/tap/llm-router
```

This installs one binary, `llm-router`, plus three command names that point at it:

| Command | What it does |
| --- | --- |
| `llmr-claude` | Runs Claude Code with routing turned on |
| `llmr-codex` | Runs Codex with routing turned on |
| `llmr-explain` | Prints why the last turn was routed the way it was |
| `llm-router` | The base command: `llm-router claude`, `llm-router codex`, `llm-router explain [id]`, `llm-router install`, `llm-router version` |

Both `llmr-claude` and `llmr-codex` launch the real upstream CLI. `llm-router` only chooses
the model for a fresh user turn; every argument you pass is forwarded to the underlying CLI.

The first run of `llmr-claude` or `llmr-codex` also installs a small skill,
`llmr-explain`, into Claude Code's and Codex's own skill folders. It lets you ask either
assistant "why did you route to X" and get the report back without spending a routing call
on the question.

## Building from source

```sh
./install.sh
```

Builds and installs `llm-router` plus the three command names into
`~/.local/bin`. The script removes the existing binary before copying rather
than overwriting it: macOS ties a code signature to the path, and overwriting
one in place leaves a stale association that the kernel answers with SIGKILL —
identical bytes, exit 137, no error message.

## API key

Get a key from [TypeSafe](https://docs.typesafe.ai), the routing provider, and put it in
`~/.llm-router.env`:

```bash
echo "LLMR_API_KEY=..." > ~/.llm-router.env
```

No Anthropic or OpenAI key is needed when the corresponding CLI is already logged in with a
subscription; `llm-router` only forwards your existing `claude login` or `codex login`
session. The routing key is separate and pays for the small model that makes the routing
decision itself.

For anyone upgrading from `jev-router`, the old files still work: `~/.jev-router.env` and
`~/.jev-claude.env` are read too, and `JEV_*` variables are read as a fallback wherever an
`LLMR_*` one is not set.

## Environment variables

All of these take an `LLMR_` prefix, and fall back to the legacy `JEV_` prefix if set,
except `LLMR_JEV_MODEL`: it is new, has no legacy name, and always reads `LLMR_JEV_MODEL`
only.

| Variable | Meaning |
| --- | --- |
| `LLMR_API_KEY` | The TypeSafe routing key (also read as `JEV_API_KEY` or `TYPESAFE_API_KEY`) |
| `LLMR_ALLOW_FABLE` | Set to `1` to allow routing to the `fable` tier, which bills extra usage credits |
| `LLMR_SWITCH_HORIZON` | How many more turns a conversation is assumed to keep reusing its cached prefix, when deciding if a downgrade pays off. Default `5` |
| `LLMR_SUBAGENT_HORIZON` | The same, for a sub-agent, whose context is short-lived. Default `2` |
| `LLMR_JEV_MODEL` | Overrides the TypeSafe model used to make the routing decision. No `JEV_` fallback |
| `LLMR_NO_STATUSLINE` | Set to disable the status line `llmr-claude` adds to Claude Code |
| `LLMR_DEBUG` | Set to log routing decisions to `~/.llm-router.log` |
| `LLMR_DUMP` | A path prefix; when set, each rewritten request body is dumped to `<prefix>.<unix millis>.json`, before the rewrite |
| `LLMR_CODEX_FAST_MODEL` | Overrides the Codex model slug used for the `haiku` tier |
| `LLMR_CODEX_BALANCED_MODEL` | Overrides the Codex model slug used for the `sonnet` tier |
| `LLMR_CODEX_STRONG_MODEL` | Overrides the Codex model slug used for the `opus` tier |
| `LLMR_CODEX_LONG_MODEL` | Overrides the Codex model slug used for the `fable` tier |
| `LLMR_STATUS_ID` | The status id `llmr-explain` reports on, when none is given on the command line |

## The cache rule

Switching to a cheaper model mid-conversation throws away the prompt cache for that
conversation and rebuilds it, which costs more up front. `llm-router` only switches when the
savings from the cheaper model, over the next few turns, outweigh that rebuild cost.

The break-even point depends far more on which tiers are involved than on how big the
conversation is. Break-even turns, at 2,000 output tokens per turn:

| From -> to | 80k cached | 100k cached | 500k cached |
| --- | --- | --- | --- |
| opus -> haiku | 2.1 | 2.4 | 4.0 |
| opus -> sonnet | 5.6 | 6.3 | 10.6 |
| sonnet -> haiku | 8.4 | 9.5 | 15.8 |

Read down a column, not across a row: dropping opus to haiku pays off in two to four turns at
any of these sizes, but dropping sonnet to haiku needs eight to sixteen, because sonnet's
cache reads are already cheap and there is little left to save. A single token-count
threshold cannot capture that, so `llm-router` compares actual costs instead.

## Why a native binary, plainly

A compiled binary does **not** make routing faster. The time a routing decision takes is
network latency to the routing provider, and that is the same whether the code asking for it
is Go or Node.

The real wins are smaller than "faster":

- No runtime dependency: nothing to install before `llm-router` runs, no Node version to
  match.
- The status line no longer spawns a process on every render, which used to add up over a
  long session.
- `brew install` instead of `npm install -g` plus a Node runtime.

That is the whole case for the rewrite. If you already have `jev-router` working well for
you, upgrading gets you these three things and nothing else.
