#!/usr/bin/env bash
# Summarises each text file given, writing <file>.summary.md beside it. Piped text comes
# first in the prompt, then the instruction.
#
#   examples/summarise-files.sh notes/*.md
set -euo pipefail

model="${ZHARF_MODEL:-gpt-5-mini}"
[ $# -gt 0 ] || { echo "usage: summarise-files.sh FILE..." >&2; exit 2; }

for file in "$@"; do
  echo "summarising $file" >&2
  zharf run "$model" --quiet \
    "Summarise the text above in five bullet points, in the language it is written in." \
    < "$file" > "$file.summary.md"
done
