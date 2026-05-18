#!/usr/bin/env bash
set -euo pipefail

TOPO_FILE="${1:-}"
BASELINE="${2:-}"
RUNS="${3:-7}"

CONTAINER_BASE="dsim-tc-bench"
IMAGE="dzschnd/dsim:latest"
NODE_IMAGE="dsim/node:local"
HOST_PORT="8080"
CONTAINER_PORT="8080"
URL="http://localhost:${HOST_PORT}"

POLL_INTERVAL="0.05"
MAX_WAIT_SECONDS=60
NET_CONVERGE_SECONDS=60
GAP_SECONDS=5
DELAY_MS=100
JITTER_MS=20
LOSS_PCT=10
BW_CAP_KBIT=1000
PING_COUNT=100
PING_COUNT_LOSS=50
IPERF_SECS=10

if [[ -z "$TOPO_FILE" || -z "$BASELINE" ]]; then
  echo "Usage: $0 <topology_file> <baseline_rtt_ms> [runs]" >&2
  exit 2
fi

if [[ ! -f "$TOPO_FILE" ]]; then
  echo "Topology file not found: $TOPO_FILE" >&2
  exit 2
fi

if ! [[ "$BASELINE" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
  echo "baseline_rtt_ms must be a non-negative number" >&2
  exit 2
fi

if ! [[ "$RUNS" =~ ^[0-9]+$ ]] || (( RUNS < 1 )); then
  echo "runs must be a positive integer" >&2
  exit 2
fi

TOPO_NAME="$(basename "$TOPO_FILE" .json)"

case "$TOPO_NAME" in
  t1) SRC_NAME="h1"; DST_NAME="h2" ;;
  t2) SRC_NAME="h1"; DST_NAME="h4" ;;
  t3) SRC_NAME="h1"; DST_NAME="h14" ;;
  *) echo "Unknown topology: $TOPO_NAME (expected t1, t2, or t3)" >&2; exit 2 ;;
esac

EFFECTIVE_LOSS="$(awk -v l="$LOSS_PCT" 'BEGIN { printf "%.1f", (1 - (1 - l/100)^2) * 100 }')"
configured_delta=$(( DELAY_MS * 2 ))

CURRENT_CONTAINER=""

cleanup() {
  if [[ -n "$CURRENT_CONTAINER" ]]; then
    curl -fsS -X DELETE "${URL}/api/v1/topology" >/dev/null 2>&1 || true
    docker rm -f "$CURRENT_CONTAINER" >/dev/null 2>&1 || true
    docker ps -q --filter "ancestor=${NODE_IMAGE}" \
      | xargs -r docker rm -f >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
  fi
}

trap cleanup EXIT INT TERM

node_id_by_name() {
  local name="$1"
  curl -fsS "${URL}/api/v1/nodes" \
    | jq -r ".[] | select(.name == \"$name\") | .id"
}

node_ip_by_name() {
  local name="$1"
  curl -fsS "${URL}/api/v1/nodes" \
    | jq -r ".[] | select(.name == \"$name\") | .interfaces[0] | .ipAddress // .runtimeIpAddress"
}

cli_stdout() {
  local node_id="$1" command="$2"
  curl -fsS -X POST "${URL}/api/v1/nodes/${node_id}/cli" \
    -H 'Content-Type: application/json' \
    -d "$(jq -n --arg c "$command" '{"command":$c}')" \
    | jq -r '.stdout'
}

parse_ping_avg_ms() {
  awk '
    /time=/ {
      sub(/.*time=/, "")
      sub(/ ms.*/, "")
      sum += $0 + 0
      n++
    }
    END { if (n > 0) printf "%.2f\n", sum / n }
  '
}

parse_ping_mdev_ms() {
  awk '
    /time=/ {
      sub(/.*time=/, "")
      sub(/ ms.*/, "")
      val = $0 + 0
      vals[n] = val
      sum += val
      n++
    }
    END {
      if (n == 0) { printf "0.000"; exit }
      mean = sum / n
      sq = 0
      for (i = 0; i < n; i++) sq += (vals[i] - mean) ^ 2
      printf "%.2f\n", sqrt(sq / n)
    }
  '
}

parse_ping_loss_pct() {
  awk '/packet loss/ {
    for (i = 1; i <= NF; i++) {
      if ($i ~ /%/) {
        v = $i; gsub(/%/, "", v)
        printf "%.1f\n", v
        exit
      }
    }
  }'
}

parse_iperf_tcp_mbps() {
  awk '/sender/ && /bits\/sec/ {
    for (i = 1; i <= NF; i++) {
      if ($i ~ /bits\/sec$/ && (i-1) >= 1 && $(i-1) ~ /^[0-9.]+$/) {
        val = $(i-1)
        unit = $i
        sub(/bits\/sec$/, "", unit)
        if (unit == "K") val = val / 1000
        else if (unit == "G") val = val * 1000
        printf "%.3f\n", val
        exit
      }
    }
  }'
}

avg() {
  awk -v sum="$1" -v n="$2" 'BEGIN {
    if (n <= 0) printf "0.00"
    else printf "%.2f", sum / n
  }'
}

echo "============================================================"
printf " DSIM tc/netem fidelity : %s  (%d runs)\n" "$TOPO_NAME" "$RUNS"
printf " Baseline RTT           : %s ms\n" "$BASELINE"
printf " Effective loss (2 NICs): %s %%\n" "$EFFECTIVE_LOSS"
echo "============================================================"

sum_observed_delta=0
sum_error_delay=0
sum_mdev=0
sum_error_jitter=0
sum_observed_loss=0
sum_error_loss=0
sum_observed_bw=0
sum_error_bw=0
success=0

for ((run = 1; run <= RUNS; run++)); do
  container_name="${CONTAINER_BASE}-${run}-$$"
  CURRENT_CONTAINER="$container_name"
  docker rm -f "$container_name" >/dev/null 2>&1 || true
  docker ps -q --filter "ancestor=${NODE_IMAGE}" \
    | xargs -r docker rm -f >/dev/null 2>&1 || true

  echo ""
  echo "--- Run ${run}/${RUNS} ---"

  if ! docker run -d \
    --name "$container_name" \
    --pid host \
    --cap-add NET_ADMIN \
    -p "${HOST_PORT}:${CONTAINER_PORT}" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    "$IMAGE" >/dev/null; then
    echo "  docker run failed"
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  deadline_s=$(( $(date +%s) + MAX_WAIT_SECONDS ))
  failed=0
  until curl -fsS --max-time 1 "${URL}" >/dev/null 2>&1; do
    if (( $(date +%s) >= deadline_s )); then failed=1; break; fi
    sleep "$POLL_INTERVAL"
  done
  if (( failed )); then
    echo "  timed out waiting for app"
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  if ! curl -fsS -X POST "${URL}/api/v1/topology" \
    -H 'Content-Type: application/json' \
    --data-binary @"${TOPO_FILE}" >/dev/null; then
    echo "  topology load failed"
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  if ! curl -fsS -X POST "${URL}/api/v1/nodes/toggle-all" >/dev/null; then
    echo "  toggle-all failed"
    curl -fsS -X DELETE "${URL}/api/v1/topology" >/dev/null 2>&1 || true
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  src_id="$(node_id_by_name "$SRC_NAME")"
  dst_id="$(node_id_by_name "$DST_NAME")"
  dst_ip="$(node_ip_by_name "$DST_NAME")"

  if [[ -z "$src_id" || -z "$dst_id" || -z "$dst_ip" ]]; then
    echo "  could not resolve nodes"
    curl -fsS -X DELETE "${URL}/api/v1/topology" >/dev/null 2>&1 || true
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  converge_deadline=$(( $(date +%s) + NET_CONVERGE_SECONDS ))
  converged=0
  while (( $(date +%s) < converge_deadline )); do
    test_ping="$(cli_stdout "$src_id" "ping ${dst_ip} --count 1" 2>/dev/null || true)"
    if echo "$test_ping" | grep -q "time="; then
      converged=1; break
    fi
    sleep 1
  done
  if (( !converged )); then
    echo "  network convergence timed out — skipping run"
    curl -fsS -X DELETE "${URL}/api/v1/topology" >/dev/null 2>&1 || true
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    docker ps -q --filter "ancestor=${NODE_IMAGE}" \
      | xargs -r docker rm -f >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  printf "  [1/4] Delay  (configured: %dms on both NICs)\n" "$DELAY_MS"
  cli_stdout "$src_id" "tc set eth0 --delay ${DELAY_MS}" >/dev/null
  cli_stdout "$dst_id" "tc set eth0 --delay ${DELAY_MS}" >/dev/null
  ping_out="$(cli_stdout "$src_id" "ping ${dst_ip} --count ${PING_COUNT}")"
  cli_stdout "$src_id" "tc clear eth0" >/dev/null
  cli_stdout "$dst_id" "tc clear eth0" >/dev/null
  avg_rtt="$(echo "$ping_out" | parse_ping_avg_ms)"
  avg_rtt="${avg_rtt:-0}"
  observed_delta="$(awk -v obs="$avg_rtt" -v base="$BASELINE" 'BEGIN { printf "%.2f", obs - base }')"
  error_delay="$(awk -v obs="$observed_delta" -v cfg="$configured_delta" 'BEGIN { printf "%.2f", obs - cfg }')"
  printf "     baseline RTT : %s ms\n" "$BASELINE"
  printf "     observed RTT : %s ms\n" "$avg_rtt"
  printf "     RTT delta    : %s ms  (configured +%d ms)\n" "$observed_delta" "$configured_delta"
  printf "     error        : %s ms\n" "$error_delay"

  printf "  [2/4] Jitter (configured: delay=%dms jitter=%dms on both NICs)\n" "$DELAY_MS" "$JITTER_MS"
  cli_stdout "$src_id" "tc set eth0 --delay ${DELAY_MS} --jitter ${JITTER_MS}" >/dev/null
  cli_stdout "$dst_id" "tc set eth0 --delay ${DELAY_MS} --jitter ${JITTER_MS}" >/dev/null
  ping_out_jitter="$(cli_stdout "$src_id" "ping ${dst_ip} --count ${PING_COUNT}")"
  cli_stdout "$src_id" "tc clear eth0" >/dev/null
  cli_stdout "$dst_id" "tc clear eth0" >/dev/null
  mdev="$(echo "$ping_out_jitter" | parse_ping_mdev_ms)"
  mdev="${mdev:-0}"
  error_jitter="$(awk -v obs="$mdev" -v cfg="$JITTER_MS" 'BEGIN { printf "%.2f", obs - cfg }')"
  printf "     observed mdev : %s ms  (configured %d ms)\n" "$mdev" "$JITTER_MS"
  printf "     error         : %s ms\n" "$error_jitter"

  printf "  [3/4] Loss   (configured: %d%% per NIC, effective %s%% end-to-end, %d packets)\n" \
    "$LOSS_PCT" "$EFFECTIVE_LOSS" "$PING_COUNT_LOSS"
  cli_stdout "$src_id" "tc set eth0 --loss ${LOSS_PCT}" >/dev/null
  cli_stdout "$dst_id" "tc set eth0 --loss ${LOSS_PCT}" >/dev/null
  ping_out_loss="$(cli_stdout "$src_id" "ping ${dst_ip} --count ${PING_COUNT_LOSS}")"
  cli_stdout "$src_id" "tc clear eth0" >/dev/null
  cli_stdout "$dst_id" "tc clear eth0" >/dev/null
  observed_loss="$(echo "$ping_out_loss" | parse_ping_loss_pct)"
  observed_loss="${observed_loss:-0}"
  error_loss="$(awk -v obs="$observed_loss" -v cfg="$EFFECTIVE_LOSS" 'BEGIN { printf "%.1f", obs - cfg }')"
  printf "     observed loss : %s%%  (effective configured %s%%)\n" "$observed_loss" "$EFFECTIVE_LOSS"
  printf "     error         : %s%%\n" "$error_loss"

  printf "  [4/4] Bandwidth cap (configured: %d kbit/s on both NICs, iperf3 %ds)\n" \
    "$BW_CAP_KBIT" "$IPERF_SECS"
  cli_stdout "$src_id" "tc set eth0 --bandwidth ${BW_CAP_KBIT}" >/dev/null
  cli_stdout "$dst_id" "tc set eth0 --bandwidth ${BW_CAP_KBIT}" >/dev/null
  cli_stdout "$dst_id" "iperf server start" >/dev/null
  observed_bw="$(cli_stdout "$src_id" "iperf tcp ${dst_ip} --time ${IPERF_SECS}" | parse_iperf_tcp_mbps)"
  observed_bw="${observed_bw:-0}"
  cli_stdout "$src_id" "tc clear eth0" >/dev/null
  cli_stdout "$dst_id" "tc clear eth0" >/dev/null
  cli_stdout "$dst_id" "iperf server stop" >/dev/null 2>&1 || true
  error_bw="$(awk -v obs="$observed_bw" 'BEGIN { printf "%.3f", obs - 1.0 }')"
  printf "     observed : %s Mbit/s  (configured 1.000 Mbit/s)\n" "$observed_bw"
  printf "     error    : %s Mbit/s\n" "$error_bw"

  curl -fsS -X DELETE "${URL}/api/v1/topology" >/dev/null 2>&1 || true
  docker rm -f "$container_name" >/dev/null 2>&1 || true
  docker ps -q --filter "ancestor=${NODE_IMAGE}" \
    | xargs -r docker rm -f >/dev/null 2>&1 || true
  CURRENT_CONTAINER=""

  sum_observed_delta="$(awk -v a="$sum_observed_delta" -v b="$observed_delta" 'BEGIN { printf "%.6f", a + b }')"
  sum_error_delay="$(awk    -v a="$sum_error_delay"    -v b="$error_delay"    'BEGIN { printf "%.6f", a + b }')"
  sum_mdev="$(awk           -v a="$sum_mdev"           -v b="$mdev"           'BEGIN { printf "%.6f", a + b }')"
  sum_error_jitter="$(awk   -v a="$sum_error_jitter"   -v b="$error_jitter"   'BEGIN { printf "%.6f", a + b }')"
  sum_observed_loss="$(awk  -v a="$sum_observed_loss"  -v b="$observed_loss"  'BEGIN { printf "%.6f", a + b }')"
  sum_error_loss="$(awk     -v a="$sum_error_loss"     -v b="$error_loss"     'BEGIN { printf "%.6f", a + b }')"
  sum_observed_bw="$(awk    -v a="$sum_observed_bw"    -v b="$observed_bw"    'BEGIN { printf "%.6f", a + b }')"
  sum_error_bw="$(awk       -v a="$sum_error_bw"       -v b="$error_bw"       'BEGIN { printf "%.6f", a + b }')"
  success=$(( success + 1 ))

  if (( run < RUNS )); then
    sleep "$GAP_SECONDS"
  fi
done

if (( success == 0 )); then
  echo ""
  echo "Successful runs: 0"
  exit 1
fi

echo ""
echo "============================================================"
printf " AVERAGES over %d runs — %s\n" "$success" "$TOPO_NAME"
printf "  %-20s %12s %12s %10s\n" "Condition" "Configured" "Observed" "Error"
printf "  %-20s %12s %12s %10s\n" "--------------------" "------------" "------------" "----------"
printf "  %-20s %12s %12s %10s\n" "Delay" \
  "${configured_delta}ms" "$(avg "$sum_observed_delta" "$success")ms" "$(avg "$sum_error_delay" "$success")ms"
printf "  %-20s %12s %12s %10s\n" "Jitter" \
  "${JITTER_MS}ms" "$(avg "$sum_mdev" "$success")ms" "$(avg "$sum_error_jitter" "$success")ms"
printf "  %-20s %12s %12s %10s\n" "Loss (effective)" \
  "${EFFECTIVE_LOSS}%" "$(avg "$sum_observed_loss" "$success")%" "$(avg "$sum_error_loss" "$success")%"
printf "  %-20s %12s %12s %10s\n" "Bandwidth cap" \
  "1.0Mbit/s" "$(avg "$sum_observed_bw" "$success")Mbit/s" "$(avg "$sum_error_bw" "$success")Mbit/s"
echo "============================================================"
