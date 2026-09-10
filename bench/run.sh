#!/usr/bin/env bash
# Orchestrates all four benchmark scenarios end to end: builds the gateway
# and a local dummy upstream, creates the API keys each scenario needs,
# starts one gateway instance, drives load with k6 (via Docker), and turns
# the k6 summaries into the table in bench/RESULTS.md.
#
# One gateway instance, not two: rate-limit algorithm is now chosen per API
# key (internal/limiter.Registry), not per process via LIMITER_ALGO, so a
# single running server serves the token_bucket and sliding_window keys
# concurrently — which is also a more representative setup for scenario 3
# than two separate processes would be.
#
# Requires: Docker (for grafana/k6), a reachable Postgres (TEST_DATABASE_URL
# not needed — this uses the real DATABASE_URL/REDIS_URL from .env) and
# Redis, since sliding_window only runs on Redis.
set -euo pipefail

cd "$(dirname "$0")/.."

VUS=${VUS:-100}
DURATION=${DURATION:-30s}

UPSTREAM_PORT=9000
GW_PORT=8090
ADMIN_TOKEN=bench-admin-token

BIN_DIR=$(mktemp -d)
RESULTS_DIR="bench/k6/results"
mkdir -p "$RESULTS_DIR"

PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
  rm -rf "$BIN_DIR"
}
trap cleanup EXIT

wait_for_http() {
  local url=$1 tries=30
  until curl -s -o /dev/null "$url"; do
    tries=$((tries - 1))
    if [ "$tries" -le 0 ]; then
      echo "timed out waiting for $url" >&2
      exit 1
    fi
    sleep 0.5
  done
}

echo "== Building binaries =="
go build -o "$BIN_DIR/gateway" ./cmd/server
go build -o "$BIN_DIR/keygen" ./cmd/keygen
go build -o "$BIN_DIR/upstream" ./bench/upstream

echo "== Starting dummy upstream on :$UPSTREAM_PORT =="
PORT=$UPSTREAM_PORT "$BIN_DIR/upstream" >/tmp/bench_upstream.log 2>&1 &
PIDS+=($!)
UPSTREAM_URL="http://localhost:$UPSTREAM_PORT"
wait_for_http "$UPSTREAM_URL/"

echo "== Creating API keys =="
STAMP=$(date +%s)
extract_key() { grep -o 'gw_[A-Za-z0-9_-]*'; }

KEY_TB_HIGH=$("$BIN_DIR/keygen" -name "bench-tb-high-$STAMP" -limit 100000000 -window 1s -burst 100000000 -upstream "$UPSTREAM_URL" | extract_key)
KEY_TB_LOW=$("$BIN_DIR/keygen" -name "bench-tb-low-$STAMP" -limit 1 -window 60s -burst 1 -upstream "$UPSTREAM_URL" | extract_key)
KEY_SW_HIGH=$("$BIN_DIR/keygen" -name "bench-sw-high-$STAMP" -limit 100000000 -window 1s -algo sliding_window -upstream "$UPSTREAM_URL" | extract_key)

echo "== Starting gateway on :$GW_PORT (serves both algos, per-key) =="
PORT=$GW_PORT ADMIN_TOKEN=$ADMIN_TOKEN "$BIN_DIR/gateway" >/tmp/bench_gw.log 2>&1 &
PIDS+=($!)
wait_for_http "http://localhost:$GW_PORT/health"

run_k6() {
  local name=$1 target=$2 key=$3
  echo "== k6: $name (target=$target) =="
  MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W 2>/dev/null || pwd)/bench/k6":/scripts \
    -e TARGET_URL="$target" -e API_KEY="$key" -e VUS="$VUS" -e DURATION="$DURATION" \
    grafana/k6 run --summary-export="/scripts/results/$name.json" /scripts/scenario.js
}

# Scenario 1: high limit, everything passes -> pure throughput/latency.
# Captured together with a CPU profile of this same run (see PROFILING in
# the milestone notes) since it's the busiest of the four scenarios.
# seconds=10, not e.g. 20: the server's http.Server.WriteTimeout is 15s,
# so a profile request held open longer than that gets its connection cut
# ("empty reply from server") before pprof can write the response. Worth
# noting as a real constraint, not just a benchmark-script detail.
echo "== Capturing CPU profile of the gateway during scenario 1 =="
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
  "http://localhost:$GW_PORT/debug/pprof/profile?seconds=10" \
  -o "$RESULTS_DIR/cpu_scenario1.pprof" &
PROFILE_PID=$!

run_k6 scenario1_high_limit "http://host.docker.internal:$GW_PORT/proxy/" "$KEY_TB_HIGH"
wait "$PROFILE_PID"

# Scenario 2: limit of 1 — everything after the first request is rejected
# by the rate limiter before it ever reaches the upstream.
run_k6 scenario2_low_limit "http://host.docker.internal:$GW_PORT/proxy/" "$KEY_TB_LOW"

# Scenario 3: token_bucket vs sliding_window head-to-head, identical load,
# same gateway process, same running Redis, concurrent per-key dispatch.
# Scenario 1 above IS the token_bucket leg of this comparison (same
# config: high limit, same VUS/DURATION) — it's reused rather than run
# twice, since re-running it would just be the same test again.
run_k6 scenario3_sliding_window "http://host.docker.internal:$GW_PORT/proxy/" "$KEY_SW_HIGH"

# Scenario 4: straight to the upstream, no gateway at all -> baseline.
run_k6 scenario4_baseline "http://host.docker.internal:$UPSTREAM_PORT/" ""

echo "== Report =="
go run ./bench/report \
  "1. gateway, token_bucket, high limit=bench/k6/results/scenario1_high_limit.json" \
  "2. gateway, token_bucket, low limit (denied)=bench/k6/results/scenario2_low_limit.json" \
  "3a. gateway, token_bucket (same as scenario 1)=bench/k6/results/scenario1_high_limit.json" \
  "3b. gateway, sliding_window=bench/k6/results/scenario3_sliding_window.json" \
  "4. no gateway (baseline)=bench/k6/results/scenario4_baseline.json"

# Keep the exact binary the profile was captured from — pprof needs it to
# symbolize addresses, and $BIN_DIR is removed on exit.
cp "$BIN_DIR/gateway" "$RESULTS_DIR/gateway-bench-binary"

echo
echo "== CPU profile (top 15 by flat time) =="
go tool pprof -top -nodecount=15 "$RESULTS_DIR/gateway-bench-binary" "$RESULTS_DIR/cpu_scenario1.pprof" \
  | tee "$RESULTS_DIR/cpu_scenario1_top.txt"
