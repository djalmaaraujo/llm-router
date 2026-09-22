#!/bin/sh
# Build and install the four commands into ~/.local/bin.
#
# Removes the existing binary before copying: macOS ties a code signature to
# the path, and overwriting one in place leaves a stale association that the
# kernel answers with SIGKILL — identical bytes, exit 137, no error message.
set -e

BIN="${BIN:-$HOME/.local/bin}"
VERSION="${VERSION:-0.1.0-dev}"

go build -ldflags "-s -w -X main.version=$VERSION" -o llm-router .

mkdir -p "$BIN"
rm -f "$BIN/llm-router"
cp llm-router "$BIN/llm-router"
codesign --force -s - "$BIN/llm-router" 2>/dev/null || true

for name in llmr-claude llmr-codex llmr-explain; do
	ln -sf "$BIN/llm-router" "$BIN/$name"
done

"$BIN/llm-router" version
echo "installed in $BIN: llm-router llmr-claude llmr-codex llmr-explain"
