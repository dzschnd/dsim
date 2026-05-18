#!/usr/bin/env bash
set -euo pipefail

TOPO_FILE="${1:-}"
RUNS="${2:-7}"

CONTAINER_BASE="dsim-traffic-bench"
IMAGE="dzschnd/dsim:latest"
NODE_IMAGE="dsim/node:local"
HOST_PORT="8080"
CONTAINER_PORT="8080"
URL="http://localhost:${HOST_PORT}"

POLL_INTERVAL="0.05"
MAX_WAIT_SECONDS=60
NET_CONVERGE_SECONDS=120
PING_COUNT=20
IPERF_DURATION=30
UDP_BITRATE="100M"
BW_CAP_KBIT=1000
CPU_SETTLE=5
CPU_SAMPLE=5
GAP_SECONDS=5
CORES="$(nproc)"

if [[ -z "$TOPO_FILE" ]] || ! [[ "$RUNS" =~ ^[0-9]+$ ]] || (( RUNS < 1 )); then
  echo "Usage: $0 <topology_file> [runs]" >&2
  exit 2
fi

if [[ ! -f "$TOPO_FILE" ]]; then
  echo "Topology file not found: $TOPO_FILE" >&2
  exit 2
fi

TOPO_NAME="$(basename "$TOPO_FILE" .json)"

case "$TOPO_NAME" in
  t1) SRC_NAME="h1"; DST_NAME="h2" ;;
  t2) SRC_NAME="h1"; DST_NAME="h4" ;;
  t3) SRC_NAME="h1"; DST_NAME="h14" ;;
  *) echo "Unknown topology: $TOPO_NAME (expected t1, t2, or t3)" >&2; exit 2 ;;
esac

CURRENT_CONTAINER=""
TMP_FILES=()

cleanup() {
  if [[ -n "$CURRENT_CONTAINER" ]]; then
    curl -fsS -X DELETE "${URL}/api/v1/topology" >/dev/null 2>&1 || true
    docker rm -f "$CURRENT_CONTAINER" >/dev/null 2>&1 || true
    docker ps -q --filter "ancestor=${NODE_IMAGE}" \
      | xargs -r docker rm -f >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
  fi
  for f in "${TMP_FILES[@]+"${TMP_FILES[@]}"}"; do
    rm -f "$f" 2>/dev/null || true
  done
}

trap cleanup EXIT INT TERM

mem_to_mib() {
  local mem="$1"
  awk -v mem="$mem" '
    function trim(s) {
      gsub(/^[ \t]+|[ \t]+$/, "", s)
      return s
    }
    BEGIN {
      mem = trim(mem)
      value = mem
      unit = mem
      gsub(/[^0-9.]/, "", value)
      gsub(/[0-9.]/, "", unit)
      value += 0
      if (unit == "B")   printf "%.6f", value / 1024 / 1024
      else if (unit == "KiB") printf "%.6f", value / 1024
      else if (unit == "MiB") printf "%.6f", value
      else if (unit == "GiB") printf "%.6f", value * 1024
      else if (unit == "TiB") printf "%.6f", value * 1024 * 1024
      else printf "%.6f", value
    }
  '
}

sample_once() {
  local app_name="$1"
  local app_ram=0 app_cpu=0 nodes_ram=0 nodes_cpu=0 total_ram=0 total_cpu=0

  mapfile -t node_names < <(
    docker ps --filter "ancestor=${NODE_IMAGE}" --format '{{.Names}}' | sort
  )

  local containers=("$app_name")
  if (( ${#node_names[@]} > 0 )); then
    containers+=("${node_names[@]}")
  fi

  local name mem_usage cpu_perc mem_used mem_mib cpu_raw cpu_norm is_node

  while IFS='|' read -r name mem_usage cpu_perc; do
    [[ -n "$name" ]] || continue
    mem_used="${mem_usage%% / *}"
    mem_mib="$(mem_to_mib "$mem_used")"
    cpu_raw="${cpu_perc%\%}"
    cpu_norm="$(awk -v cpu="$cpu_raw" -v cores="$CORES" 'BEGIN { printf "%.6f", cpu / cores }')"

    total_ram="$(awk -v a="$total_ram" -v b="$mem_mib" 'BEGIN { printf "%.6f", a + b }')"
    total_cpu="$(awk -v a="$total_cpu" -v b="$cpu_norm" 'BEGIN { printf "%.6f", a + b }')"

    if [[ "$name" == "$app_name" ]]; then
      app_ram="$(awk -v a="$app_ram" -v b="$mem_mib" 'BEGIN { printf "%.6f", a + b }')"
      app_cpu="$(awk -v a="$app_cpu" -v b="$cpu_norm" 'BEGIN { printf "%.6f", a + b }')"
    else
      is_node=0
      for node in "${node_names[@]}"; do
        if [[ "$name" == "$node" ]]; then is_node=1; break; fi
      done
      if (( is_node )); then
        nodes_ram="$(awk -v a="$nodes_ram" -v b="$mem_mib" 'BEGIN { printf "%.6f", a + b }')"
        nodes_cpu="$(awk -v a="$nodes_cpu" -v b="$cpu_norm" 'BEGIN { printf "%.6f", a + b }')"
      fi
    fi
  done < <(
    docker stats --no-stream \
      --format '{{.Name}}|{{.MemUsage}}|{{.CPUPerc}}' \
      "${containers[@]}" 2>/dev/null
  )

  printf '%s %s %s %s %s %s\n' \
    "$app_ram" "$app_cpu" "$nodes_ram" "$nodes_cpu" "$total_ram" "$total_cpu"
}

avg() {
  awk -v sum="$1" -v n="$2" 'BEGIN {
    if (n <= 0) printf "0.00"
    else printf "%.2f", sum / n
  }'
}

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
    END { if (n > 0) printf "%.3f\n", sum / n }
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
      printf "%.3f\n", sqrt(sq / n)
    }
  '
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

parse_iperf_udp_jitter_ms() {
  awk '/receiver/ {
    for (i = 1; i <= NF; i++) {
      if ($i == "ms" && (i-1) >= 1 && $(i-1) ~ /^[0-9.]+$/) {
        printf "%.3f\n", $(i-1)
        exit
      }
    }
  }'
}

parse_iperf_udp_loss_pct() {
  awk '/receiver/ {
    for (i = 1; i <= NF; i++) {
      if ($i ~ /^\([0-9.]+%\)$/) {
        v = $i
        gsub(/[()%]/, "", v)
        printf "%.2f\n", v
        exit
      }
    }
  }'
}

DST_IP_DISPLAY=""

echo "============================================================"
echo " DSIM traffic benchmark : ${TOPO_NAME}  (${RUNS} runs)"

sum_ping_avg=0
sum_ping_mdev=0
sum_tcp=0
sum_tcp_ram=0
sum_tcp_cpu=0
sum_udp_jitter=0
sum_udp_loss=0
sum_cap_tcp=0
sum_cap_ram=0
sum_cap_cpu=0
success=0

for ((run = 1; run <= RUNS; run++)); do
  container_name="${CONTAINER_BASE}-${run}-$$"
  CURRENT_CONTAINER="$container_name"
  docker rm -f "$container_name" >/dev/null 2>&1 || true
  docker ps -q --filter "ancestor=${NODE_IMAGE}" \
    | xargs -r docker rm -f >/dev/null 2>&1 || true

  if ! docker run -d \
    --name "$container_name" \
    --pid host \
    --cap-add NET_ADMIN \
    -p "${HOST_PORT}:${CONTAINER_PORT}" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    "$IMAGE" >/dev/null; then
    echo ""
    echo "--- Run ${run}/${RUNS} ---"
    echo "  docker run failed"
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  deadline_s=$(( $(date +%s) + MAX_WAIT_SECONDS ))
  failed=0

  until curl -fsS --max-time 1 "${URL}" >/dev/null 2>&1; do
    if (( $(date +%s) >= deadline_s )); then
      failed=1
      break
    fi
    sleep "$POLL_INTERVAL"
  done

  if (( failed )); then
    echo ""
    echo "--- Run ${run}/${RUNS} ---"
    echo "  timed out waiting for app"
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  if ! curl -fsS -X POST "${URL}/api/v1/topology" \
    -H 'Content-Type: application/json' \
    --data-binary @"${TOPO_FILE}" >/dev/null; then
    echo ""
    echo "--- Run ${run}/${RUNS} ---"
    echo "  topology load failed"
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  if ! curl -fsS -X POST "${URL}/api/v1/nodes/toggle-all" >/dev/null; then
    echo ""
    echo "--- Run ${run}/${RUNS} ---"
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
    echo ""
    echo "--- Run ${run}/${RUNS} ---"
    echo "  could not resolve nodes"
    curl -fsS -X DELETE "${URL}/api/v1/topology" >/dev/null 2>&1 || true
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  if [[ -z "$DST_IP_DISPLAY" ]]; then
    DST_IP_DISPLAY="$dst_ip"
    printf " src=%s  dst=%s (%s)\n" "$SRC_NAME" "$DST_NAME" "$dst_ip"
    echo "============================================================"
  fi

  echo ""
  echo "--- Run ${run}/${RUNS} ---"

  converge_deadline=$(( $(date +%s) + NET_CONVERGE_SECONDS ))
  converged=0
  while (( $(date +%s) < converge_deadline )); do
    test_ping="$(cli_stdout "$src_id" "ping ${dst_ip} --count 1" 2>/dev/null || true)"
    if echo "$test_ping" | grep -q "time="; then
      converged=1
      break
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

  printf "  [1/4] ping RTT ... "
  ping_raw="$(cli_stdout "$src_id" "ping ${dst_ip} --count ${PING_COUNT}")"
  ping_avg="$(echo "$ping_raw" | parse_ping_avg_ms)"
  ping_avg="${ping_avg:-ERR}"
  ping_mdev="$(echo "$ping_raw" | parse_ping_mdev_ms)"
  ping_mdev="${ping_mdev:-ERR}"
  printf "avg=%s ms  mdev=%s ms\n" "$ping_avg" "$ping_mdev"

  printf "  [2/4] TCP uncapped (%ds) + RAM/CPU ... " "$IPERF_DURATION"

  cli_stdout "$dst_id" "iperf server start" >/dev/null

  tmp_iperf="$(mktemp)"
  TMP_FILES+=("$tmp_iperf")

  curl -fsS -X POST "${URL}/api/v1/nodes/${src_id}/cli" \
    -H 'Content-Type: application/json' \
    -d "$(jq -n --arg c "iperf tcp ${dst_ip} --time ${IPERF_DURATION}" '{"command":$c}')" \
    > "$tmp_iperf" &
  iperf_pid=$!

  sleep "$CPU_SETTLE"

  run_total_ram=0; run_total_cpu=0
  run_samples=0

  for (( s = 0; s < CPU_SAMPLE; s++ )); do
    read -r app_ram app_cpu nodes_ram nodes_cpu total_ram total_cpu < <(
      sample_once "$container_name"
    )
    run_total_ram="$(awk -v a="$run_total_ram" -v b="$total_ram" 'BEGIN { printf "%.6f", a + b }')"
    run_total_cpu="$(awk -v a="$run_total_cpu" -v b="$total_cpu" 'BEGIN { printf "%.6f", a + b }')"
    run_samples=$(( run_samples + 1 ))
    sleep 1
  done

  wait "$iperf_pid" || true
  tcp_mbps="$(jq -r '.stdout' "$tmp_iperf" | parse_iperf_tcp_mbps)"
  tcp_mbps="${tcp_mbps:-ERR}"
  rm -f "$tmp_iperf"
  TMP_FILES=("${TMP_FILES[@]/$tmp_iperf}")

  tcp_ram_avg="$(avg "$run_total_ram" "$run_samples")"
  tcp_cpu_avg="$(avg "$run_total_cpu" "$run_samples")"
  printf "%s Mbit/s  RAM=%s MiB  CPU=%s%%\n" \
    "$tcp_mbps" "$tcp_ram_avg" "$tcp_cpu_avg"

  printf "  [3/4] UDP jitter/loss (%ds) ... " "$IPERF_DURATION"
  cli_stdout "$dst_id" "iperf server start" >/dev/null
  udp_out="$(cli_stdout "$src_id" "iperf udp ${dst_ip} --time ${IPERF_DURATION} --bitrate ${UDP_BITRATE}")"
  udp_jitter="$(echo "$udp_out" | parse_iperf_udp_jitter_ms)"
  udp_jitter="${udp_jitter:-ERR}"
  udp_loss="$(echo "$udp_out" | parse_iperf_udp_loss_pct)"
  udp_loss="${udp_loss:-ERR}"
  printf "jitter=%s ms  loss=%s%%\n" "$udp_jitter" "$udp_loss"

  printf "  [4/4] TCP capped 1Mbit/s (%ds) + RAM/CPU ... " "$IPERF_DURATION"
  cli_stdout "$src_id" "tc set eth0 --bandwidth ${BW_CAP_KBIT}" >/dev/null
  cli_stdout "$dst_id" "tc set eth0 --bandwidth ${BW_CAP_KBIT}" >/dev/null
  cli_stdout "$dst_id" "iperf server start" >/dev/null

  tmp_iperf_cap="$(mktemp)"
  TMP_FILES+=("$tmp_iperf_cap")

  curl -fsS -X POST "${URL}/api/v1/nodes/${src_id}/cli" \
    -H 'Content-Type: application/json' \
    -d "$(jq -n --arg c "iperf tcp ${dst_ip} --time ${IPERF_DURATION}" '{"command":$c}')" \
    > "$tmp_iperf_cap" &
  iperf_cap_pid=$!

  sleep "$CPU_SETTLE"

  cap_total_ram=0; cap_total_cpu=0
  cap_samples=0

  for (( s = 0; s < CPU_SAMPLE; s++ )); do
    read -r app_ram app_cpu nodes_ram nodes_cpu total_ram total_cpu < <(
      sample_once "$container_name"
    )
    cap_total_ram="$(awk -v a="$cap_total_ram" -v b="$total_ram" 'BEGIN { printf "%.6f", a + b }')"
    cap_total_cpu="$(awk -v a="$cap_total_cpu" -v b="$total_cpu" 'BEGIN { printf "%.6f", a + b }')"
    cap_samples=$(( cap_samples + 1 ))
    sleep 1
  done

  wait "$iperf_cap_pid" || true
  cap_tcp_mbps="$(jq -r '.stdout' "$tmp_iperf_cap" | parse_iperf_tcp_mbps)"
  cap_tcp_mbps="${cap_tcp_mbps:-ERR}"
  rm -f "$tmp_iperf_cap"
  TMP_FILES=("${TMP_FILES[@]/$tmp_iperf_cap}")

  cli_stdout "$src_id" "tc clear eth0" >/dev/null
  cli_stdout "$dst_id" "tc clear eth0" >/dev/null

  cap_ram_avg="$(avg "$cap_total_ram" "$cap_samples")"
  cap_cpu_avg="$(avg "$cap_total_cpu" "$cap_samples")"
  printf "%s Mbit/s  RAM=%s MiB  CPU=%s%%\n" \
    "$cap_tcp_mbps" "$cap_ram_avg" "$cap_cpu_avg"

  cli_stdout "$dst_id" "iperf server stop" >/dev/null 2>&1 || true

  curl -fsS -X DELETE "${URL}/api/v1/topology" >/dev/null 2>&1 || true
  docker rm -f "$container_name" >/dev/null 2>&1 || true
  docker ps -q --filter "ancestor=${NODE_IMAGE}" \
    | xargs -r docker rm -f >/dev/null 2>&1 || true
  CURRENT_CONTAINER=""

  [[ "$ping_avg" == "ERR" || "$tcp_mbps" == "ERR" ]] && { sleep 2; continue; }

  sum_ping_avg="$(awk      "BEGIN{printf \"%.6f\", $sum_ping_avg      + ${ping_avg:-0}}")"
  sum_ping_mdev="$(awk     "BEGIN{printf \"%.6f\", $sum_ping_mdev     + ${ping_mdev:-0}}")"
  sum_tcp="$(awk        "BEGIN{printf \"%.6f\", $sum_tcp     + ${tcp_mbps:-0}}")"
  sum_tcp_ram="$(awk   "BEGIN{printf \"%.6f\", $sum_tcp_ram + ${tcp_ram_avg:-0}}")"
  sum_tcp_cpu="$(awk   "BEGIN{printf \"%.6f\", $sum_tcp_cpu + ${tcp_cpu_avg:-0}}")"
  sum_udp_jitter="$(awk "BEGIN{printf \"%.6f\", $sum_udp_jitter + ${udp_jitter:-0}}")"
  sum_udp_loss="$(awk  "BEGIN{printf \"%.6f\", $sum_udp_loss   + ${udp_loss:-0}}")"
  sum_cap_tcp="$(awk   "BEGIN{printf \"%.6f\", $sum_cap_tcp    + ${cap_tcp_mbps:-0}}")"
  sum_cap_ram="$(awk   "BEGIN{printf \"%.6f\", $sum_cap_ram    + ${cap_ram_avg:-0}}")"
  sum_cap_cpu="$(awk   "BEGIN{printf \"%.6f\", $sum_cap_cpu    + ${cap_cpu_avg:-0}}")"
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
echo "============================================================"
printf "  %-30s : %s ms\n"     "ping RTT avg"              "$(avg "$sum_ping_avg"       "$success")"
printf "  %-30s : %s ms\n"     "ping RTT mdev"             "$(avg "$sum_ping_mdev"      "$success")"
printf "  %-30s : %s Mbit/s\n" "TCP throughput (uncapped)" "$(avg "$sum_tcp"         "$success")"
printf "  %-30s : %s MiB\n"    "RAM (uncapped)"            "$(avg "$sum_tcp_ram"    "$success")"
printf "  %-30s : %s %%\n"     "CPU (uncapped)"            "$(avg "$sum_tcp_cpu"    "$success")"
printf "  %-30s : %s ms\n"     "UDP jitter"                "$(avg "$sum_udp_jitter" "$success")"
printf "  %-30s : %s %%\n"     "UDP loss"                  "$(avg "$sum_udp_loss"   "$success")"
printf "  %-30s : %s Mbit/s\n" "TCP throughput (1Mbit/s)"  "$(avg "$sum_cap_tcp"    "$success")"
printf "  %-30s : %s MiB\n"    "RAM (1Mbit/s)"             "$(avg "$sum_cap_ram"    "$success")"
printf "  %-30s : %s %%\n"     "CPU (1Mbit/s)"             "$(avg "$sum_cap_cpu"    "$success")"
echo "============================================================"
