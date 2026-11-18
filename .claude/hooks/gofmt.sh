#!/usr/bin/env bash
# PostToolUse hook: gofmt any .go file Claude just edited.
# Never blocks: if jq or gofmt is missing, or the file isn't Go, it exits 0.
set -u

command -v jq >/dev/null 2>&1 || exit 0
command -v gofmt >/dev/null 2>&1 || exit 0

file=$(jq -r '.tool_input.file_path // empty' 2>/dev/null)
[[ "$file" == *.go && -f "$file" ]] || exit 0

if ! out=$(gofmt -w "$file" 2>&1); then
  # Syntax error: report to Claude (exit 2 feeds stderr back) so it fixes it.
  echo "gofmt failed for $file: $out" >&2
  exit 2
fi
exit 0
