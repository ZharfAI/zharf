#!/usr/bin/env bash
# One question, one answer: the answer goes to stdout, the cost to stderr.
#
#   examples/ask.sh "پایتخت استرالیا کجاست؟"
#   examples/ask.sh "Explain HTTP/3 in two sentences" gpt-5-mini
set -euo pipefail

question="${1:?usage: ask.sh \"question\" [model]}"
model="${2:-qwen3.5-plus}"

zharf run "$model" "$question"
