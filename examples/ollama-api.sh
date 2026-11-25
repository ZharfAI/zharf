#!/usr/bin/env bash
# Talks to `zharf serve` with plain curl, as any Ollama client would.
# Start the server first, in another terminal:  zharf serve
set -euo pipefail

host="${OLLAMA_HOST:-http://127.0.0.1:11434}"
model="${1:-qwen3.5-plus}"

echo "== models (first five)"
curl -s "$host/api/tags" | grep -o '"name":"[^"]*"' | head -5

echo
echo "== streamed chat (NDJSON, one line per piece)"
curl -sN "$host/api/chat" -d @- <<EOF
{
  "model": "$model",
  "messages": [{ "role": "user", "content": "سلام! در یک جمله خودت را معرفی کن." }],
  "options": { "temperature": 0.3 }
}
EOF

echo
echo "== the same through the OpenAI API (any key works; zharf sends yours)"
curl -s "$host/v1/chat/completions" \
  -H "Authorization: Bearer unused" -H "Content-Type: application/json" \
  -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"Say hello in Persian.\"}]}"
echo
