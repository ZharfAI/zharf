#!/usr/bin/env bash
# An interactive chat with a system message. Inside the chat:
#   """ … """      a multi-line message
#   /model <id>    switch model, keeping the conversation
#   /cost          what the chat has cost so far
#   /bye           leave
set -euo pipefail

model="${1:-qwen3.5-plus}"
zharf run "$model" --system "You are a concise assistant for an Iranian developer. Answer in the language of the question."
