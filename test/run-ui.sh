#!/usr/bin/env bash
# Local UI test harness. It starts an isolated instance, a mock LLM and
# headless Chrome, then runs test/e2e.mjs. Nothing is committed or pushed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ ! -f .env ]]; then
  echo "error: .env is required for the local test run" >&2
  exit 1
fi
set -a
# shellcheck disable=SC1091
source .env
set +a

export MC_ADDR="127.0.0.1:18088"
export MC_BASE_URL="http://127.0.0.1:18088"
export MC_DB_PATH="$ROOT/test/data/e2e.db"
export MC_COOKIE_SECURE="false"
export MC_LOG_LEVEL="warn"
export MC_UNSUBSCRIBE_ENABLED="false"
export MC_TEST_BASE_URL="$MC_BASE_URL"
export MC_TEST_CDP_PORT="9223"
export MC_TEST_MOCK_LLM="http://127.0.0.1:18090/v1"

# Use a demo OAuth client so committed screenshots never contain a real one.
export GOOGLE_CLIENT_ID="1234567890-demo.apps.googleusercontent.com"
export GOOGLE_CLIENT_SECRET="demo-google-client-secret"

APP_PID=""
MOCK_PID=""
CHROME_PID=""
cleanup() {
  local status=$?
  if [[ -n "$APP_PID" ]]; then kill "$APP_PID" 2>/dev/null || true; fi
  if [[ -n "$MOCK_PID" ]]; then kill "$MOCK_PID" 2>/dev/null || true; fi
  if [[ -n "$CHROME_PID" ]]; then kill "$CHROME_PID" 2>/dev/null || true; fi
  wait 2>/dev/null || true
  exit "$status"
}
trap cleanup EXIT INT TERM

if curl -fsS --max-time 2 "$MC_BASE_URL/healthz" >/dev/null 2>&1; then
  echo "error: $MC_BASE_URL is already in use" >&2
  exit 1
fi

mkdir -p test/data test/screenshots test/bin
rm -f "$MC_DB_PATH" "$MC_DB_PATH-shm" "$MC_DB_PATH-wal"
rm -rf test/data/chrome-profile
: > test/data/app.log
: > test/data/mock-llm.log
: > test/data/chrome.log

echo "building MailController…"
GO_BIN="${GO_BIN:-go}"
if ! command -v "$GO_BIN" >/dev/null 2>&1; then
  GO_BIN="$HOME/.local/opt/go/bin/go"
fi
"$GO_BIN" build -o test/bin/mailcontroller ./cmd/mailcontroller

echo "starting mock LLM on 127.0.0.1:18090…"
python3 test/mock-llm.py --port 18090 >test/data/mock-llm.log 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 50); do
  curl -fsS --max-time 1 http://127.0.0.1:18090/health >/dev/null 2>&1 && break
  sleep 0.1
done
curl -fsS --max-time 2 http://127.0.0.1:18090/health >/dev/null

echo "starting MailController on $MC_BASE_URL…"
test/bin/mailcontroller >test/data/app.log 2>&1 &
APP_PID=$!
ready=""
for _ in $(seq 1 100); do
  if curl -fsS --max-time 1 "$MC_BASE_URL/healthz" >/dev/null 2>&1; then
    ready="yes"
    break
  fi
  if ! kill -0 "$APP_PID" 2>/dev/null; then
    echo "error: MailController exited during startup" >&2
    tail -40 test/data/app.log >&2 || true
    exit 1
  fi
  sleep 0.1
done
if [[ -z "$ready" ]]; then
  echo "error: MailController did not become healthy" >&2
  tail -40 test/data/app.log >&2 || true
  exit 1
fi

echo "starting headless Chrome on 127.0.0.1:9223…"
google-chrome \
  --headless=new \
  --disable-gpu \
  --no-first-run \
  --no-default-browser-check \
  --disable-extensions \
  --disable-background-networking \
  --user-data-dir="$ROOT/test/data/chrome-profile" \
  --remote-debugging-port=9223 \
  --remote-debugging-address=127.0.0.1 \
  --window-size=1440,900 \
  about:blank >test/data/chrome.log 2>&1 &
CHROME_PID=$!
for _ in $(seq 1 100); do
  curl -fsS --max-time 1 http://127.0.0.1:9223/json/version >/dev/null 2>&1 && break
  if ! kill -0 "$CHROME_PID" 2>/dev/null; then
    echo "error: Chrome exited during startup" >&2
    tail -40 test/data/chrome.log >&2 || true
    exit 1
  fi
  sleep 0.1
done
curl -fsS --max-time 2 http://127.0.0.1:9223/json/version >/dev/null

echo "running browser checks…"
node test/e2e.mjs
