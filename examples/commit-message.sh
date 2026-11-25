#!/usr/bin/env bash
# Suggests a commit message for the staged changes.
#
#   git add -p && examples/commit-message.sh
set -euo pipefail

model="${ZHARF_MODEL:-deepseek-v4-flash}"
diff="$(git diff --cached)"
[ -n "$diff" ] || { echo "nothing staged" >&2; exit 1; }

printf '%s\n' "$diff" | zharf run "$model" --quiet --hide-thinking \
  "Write a commit message for this diff: a short subject line under 60 characters, a blank line, then one or two sentences on why. Output only the message."
