#!/usr/bin/env bash
#
# dev.sh — open two terminal windows simultaneously, starting the backend (Go/Gin :8080) and the frontend (Vite :5173) separately
#
# Usage: run from the project root  ./dev.sh
#   - macOS Terminal.app only
#   - Backend prefers the precompiled backend/bin/webshell; otherwise go run (compiles on first run)
#   - Frontend auto-runs npm install first when node_modules is missing
#

# Project root directory = script directory
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$ROOT_DIR/backend"
FRONTEND_DIR="$ROOT_DIR/frontend"

# Backend start command
if [[ -x "$BACKEND_DIR/bin/webshell" ]]; then
  BACKEND_CMD="./bin/webshell"
else
  BACKEND_CMD="go run ./cmd/webshell"
fi

# Frontend start command
if [[ -d "$FRONTEND_DIR/node_modules" ]]; then
  FRONTEND_CMD="npm run dev"
else
  FRONTEND_CMD="npm install && npm run dev"
fi

# Open two windows via macOS Terminal.app and execute in each (printf sets window title)
osascript <<EOF
tell application "Terminal"
	activate
	do script "cd \"$BACKEND_DIR\" && printf '\\033]0;webshell backend\\007' && $BACKEND_CMD"
	do script "cd \"$FRONTEND_DIR\" && printf '\\033]0;webshell frontend\\007' && $FRONTEND_CMD"
end tell
EOF

echo "✅ Opened two terminal windows: backend(:8080) / frontend(:5173)"