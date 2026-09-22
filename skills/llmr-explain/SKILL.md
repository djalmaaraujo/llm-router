---
name: llmr-explain
description: Show why llm-router selected the model used for the last prompt.
disable-model-invocation: true
allowed-tools: Bash(llm-router *)
---

<jev-explain>
Return the report below verbatim in a plain text code block. Do not add analysis or use tools.

!`llm-router explain "${CLAUDE_SESSION_ID}"`
