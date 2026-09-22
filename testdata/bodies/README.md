# Recorded request body corpus

These bodies were recorded from the real Node implementation
(`~/dev/jev-router`) with `JEV_DUMP` set, by running `bin/jev-claude.mjs`
against a live Claude Code session. They exist to prove the Go proxy's
decode/re-encode round trip does not lose data the original CLI actually
sent — not bodies invented by hand.

Every file was redacted before being committed: the local machine's home
directory and account username were replaced with placeholders, the
account's real email address and git author name were replaced with
placeholders, and the `device_id`/`account_uuid`/`session_id` values inside
`metadata.user_id` were replaced with fixed placeholder values. No prompt
text, tool output, or other content was otherwise changed, except where
noted below.

An earlier version of this corpus and this README claimed redactions that
had not actually been applied — the real session ids and the real macOS
username were still present in the committed files. That was caught in
review and fixed; see "Fix round 2" in the task report for what was wrong
and how it was found. The redaction list below describes the corpus as it
actually is now, not as it was first described.

## Shapes

| File | Shape | Origin |
| --- | --- | --- |
| `first-turn.json` | opening request of a session, tools present, typed user text | real recording, unedited apart from redaction |
| `tool-continuation.json` | request whose last user message holds a `tool_result` | real recording, unedited apart from redaction |
| `sub-agent.json` | a request under the same session with different opening text | real recording of a different session's opening turn; hand-edited (see below) |
| `mcp-schemas.json` | tools carrying draft-04 `exclusiveMinimum`/`exclusiveMaximum` booleans | real recording; hand-edited (see below) |
| `thinking-and-edits.json` | a body with `thinking` and a `context_management` edit list | real recording, unedited apart from redaction |
| `large-integers.json` | a body containing an integer with more than 15 digits | real recording; hand-edited (see below) |

`thinking` (`{"type": "adaptive", "display": "omitted"}`) and
`context_management.edits` (`[{"type": "clear_thinking_20251015", "keep":
"all"}]`) are present in every recorded body, not just
`thinking-and-edits.json` — that file was picked because it also has a
multi-turn `assistant`/`user` history, not because it's the only one with
those fields.

## Hand edits

- **`sub-agent.json`**: this is a real opening-turn recording — its own CLI
  invocation, a separate session from the others, with different opening
  user text. No genuine nested sub-agent transcript was ever captured: the
  CLI answered every "spawn a sub-agent" / "use the Task tool" prompt
  directly instead of actually invoking the Task tool, so nothing in this
  corpus is a real Task-tool child request. What earns this file its place
  is that its `metadata.user_id.session_id` was rewritten to a placeholder
  distinct from every other file's
  (`22222222-2222-2222-2222-222222222222`, vs. `11111111-1111-1111-1111-111111111111`
  everywhere else), so it stands for a request whose session id differs
  from the others'. Nothing else in the file was changed.

- **`mcp-schemas.json`**: none of the recorded bodies contained draft-04
  style `exclusiveMinimum`/`exclusiveMaximum` **booleans** — every recorded
  MCP tool schema used the modern JSON Schema numeric form (e.g.
  `"exclusiveMinimum": 0`). The `LSP` tool's `line` property was hand-edited
  from:

  ```json
  {"description": "The line number (1-based, as shown in editors)", "type": "integer", "exclusiveMinimum": 0, "maximum": 9007199254740991}
  ```

  to:

  ```json
  {"description": "The line number (1-based, as shown in editors)", "type": "integer", "minimum": 0, "exclusiveMinimum": true, "maximum": 9007199254740991, "exclusiveMaximum": false}
  ```

  No other property in the file was changed.

- **`large-integers.json`**: none of the recorded bodies naturally contained
  an integer over 15 digits. This file is the same recording used for
  `first-turn.json` (post-redaction), with one field injected into
  `metadata`: `"external_reference_id": 12345678901234567890` (a 20-digit
  integer). Nothing else was changed.

## What was redacted

- Local machine paths (e.g. `/Users/<real-username>/dev/jev-router`) →
  `/repo/jev-router`, and other `/Users/<real-username>/...` paths →
  `/repo/...`.
- The real macOS username, which also appeared as the file owner/group in
  the output of a real `ls -la` captured inside a `tool_result` block (in
  `tool-continuation.json` and `thinking-and-edits.json`, sixteen lines
  apiece) → replaced with `user` throughout. The directory listing's shape
  (permissions, sizes, dates, filenames) was left intact; only the
  owner/group name was replaced.
- The real account email address → `user@example.com`.
- The real git author name (as it appeared in an embedded `gitStatus`
  block) → `Test User`, and a personal reference in embedded CLAUDE.md
  content ("Djalma's base preferences") → "the user's base preferences".
- `device_id` inside `metadata.user_id` → a fixed placeholder
  (all-zero, same length as the original), shared across every file.
- `account_uuid` inside `metadata.user_id` → `00000000-0000-4000-8000-000000000000`,
  shared across every file.
- `session_id` inside `metadata.user_id` → `11111111-1111-1111-1111-111111111111`
  in every file except `sub-agent.json`, which got a distinct placeholder
  (`22222222-2222-2222-2222-222222222222`) to represent "a different
  session." These placeholders do not preserve which of the real recorded
  bodies originally shared a session with which — that information had no
  bearing on any test and was discarded along with the real ids.

Other UUID-looking or email-looking strings still present in some files
(`alex@example.com`, `john@company.com`, `test@example.org`,
`12345678-90ab-cdef-1234-567890abcdef`, and a few more) are placeholder
examples embedded in MCP tool descriptions and JSON Schema patterns that the
CLI loaded during the session (e.g. a Notion tool's usage examples, a UUID
regex pattern) — not real data — and were left as-is.
