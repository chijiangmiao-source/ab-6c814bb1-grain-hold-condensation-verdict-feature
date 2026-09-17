#!/usr/bin/env bash
# One-shot acceptance entrypoint, executed by `docker compose run --rm verify`.
# It exercises the REAL compose stack (api + web) and never mocks the
# calculation: Go tests, an independent recomputation probe and browser E2E.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
API="${ASSERT_API_ORIGIN:-http://api:8080}"
WEB="${ASSERT_WEB_ORIGIN:-http://web:80}"

echo "==> [1/4] Go unit/integration tests (testify)"
(cd "$ROOT/api" && go test ./... -count=1)

echo "==> [2/4] Waiting for the compose stack"
for url in "$API/api/healthz" "$WEB/"; do
  for i in $(seq 1 60); do
    if curl -fsS "$url" >/dev/null 2>&1; then break; fi
    sleep 1
    [ "$i" = 60 ] && { echo "timeout waiting for $url"; exit 1; }
  done
  echo "    $url reachable"
done

echo "==> [3/4] Independent acceptance probe (recomputes Magnus formula)"
ASSERT_API_ORIGIN="$API" node "$ROOT/verify/acceptance.mjs"

echo "==> [4/4] Browser E2E (Playwright/Chromium) through nginx -> Gin -> SQLite"
cd "$ROOT/web"
# Always (re)install inside the container: a bind-mounted node_modules may
# contain host-OS binaries (e.g. macOS esbuild). PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD
# keeps npm's postinstall from fetching browsers (they are baked into the image).
npm ci --no-audit --no-fund || npm install --no-audit --no-fund

# Guarantee the browser revision matches the pinned @playwright/test package:
# with versions aligned this is an instant no-op; if they ever drift it
# downloads the matching Chromium instead of failing every page test with a
# browser/driver version mismatch.
echo "==> Ensuring Playwright browser revision matches $(node -p "require('@playwright/test/package.json').version")"
PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD= npx playwright install chromium
PLAYWRIGHT_BASE_URL="$WEB" npx playwright test

echo
echo "VERIFY OK: go tests + acceptance probe + browser E2E all passed"
