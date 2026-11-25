#!/usr/bin/env bash
# Open WebUI with every ژرف model, through `zharf serve`.
#
# 1. Sign in once:          zharf login
# 2. Start the server:      zharf serve
# 3. Run this script, then open http://localhost:3000
set -euo pipefail

if [ "$(uname -s)" = Linux ]; then
  # On Linux a container reaches the host's 127.0.0.1 only with host networking.
  docker run -d --name open-webui --network host \
    -e OLLAMA_BASE_URL=http://127.0.0.1:11434 -e PORT=3000 \
    -v open-webui:/app/backend/data \
    ghcr.io/open-webui/open-webui:main
else
  # Docker Desktop (macOS, Windows) forwards host.docker.internal to this computer.
  docker run -d --name open-webui -p 3000:8080 \
    -e OLLAMA_BASE_URL=http://host.docker.internal:11434 \
    -v open-webui:/app/backend/data \
    ghcr.io/open-webui/open-webui:main
fi

echo "Open WebUI is starting at http://localhost:3000"
