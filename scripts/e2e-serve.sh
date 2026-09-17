#!/usr/bin/env bash
# Bring up the REAL stack for Playwright: a Gin API backed by a throwaway
# SQLite file, plus the production web build via vite preview. Playwright's
# webServer config calls this script; `docker compose run --rm verify`
# performs the same job inside a container.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
API_PORT="${E2E_API_PORT:-18095}"
WEB_PORT="${E2E_WEB_PORT:-4173}"
export VITE_API_ORIGIN="http://localhost:${API_PORT}"
export DB_PATH="${E2E_DB:-$(mktemp -u /tmp/gv-e2e-XXXXXX.db)}"
export API_PORT

GO="${GO:-go}"
mkdir -p "$ROOT/.cache"
"$GO" -C "$ROOT/api" build -buildvcs=false -o "$ROOT/.cache/e2e-server" ./cmd/server

if [ ! -f "$ROOT/web/dist/index.html" ]; then
  (cd "$ROOT/web" && npm run build)
fi

"$ROOT/.cache/e2e-server" &
api_pid=$!
trap 'kill "$api_pid" 2>/dev/null || true' EXIT

# Wait for the API, then serve the built SPA with the same /api proxy.
for _ in $(seq 1 50); do
  if curl -sf "http://localhost:${API_PORT}/api/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.2
done

cd "$ROOT/web"
exec npx vite preview --port "$WEB_PORT" --host 0.0.0.0
