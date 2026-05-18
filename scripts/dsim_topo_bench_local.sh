#!/usr/bin/env bash
set -euo pipefail

TOPO_FILE="${1:-}"
RUNS="${2:-7}"

NODE_IMAGE="dsim/node:local"
API_URL="http://localhost:8080"
HEALTH_URL="http://localhost:3003"

POLL_INTERVAL="0.05"
MAX_WAIT_SECONDS=30
SAMPLE_DURATION=30
SAMPLE_INTERVAL=1
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

cleanup_nodes() {
  curl -fsS -X DELETE "${API_URL}/api/v1/topology" >/dev/null 2>&1 || true
  docker ps -q --filter "ancestor=${NODE_IMAGE}" \
    | xargs -r docker rm -f >/dev/null 2>&1 || true
}

trap cleanup_nodes EXIT INT TERM

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
  local nodes_ram=0 nodes_cpu=0

  mapfile -t node_names < <(
    docker ps --filter "ancestor=${NODE_IMAGE}" --format '{{.Names}}' | sort
  )

  if (( ${#node_names[@]} == 0 )); then
    printf '0 0\n'
    return
  fi

  local name mem_usage cpu_perc mem_used mem_mib cpu_raw cpu_norm

  while IFS='|' read -r name mem_usage cpu_perc; do
    [[ -n "$name" ]] || continue
    mem_used="${mem_usage%% / *}"
    mem_mib="$(mem_to_mib "$mem_used")"
    cpu_raw="${cpu_perc%\%}"
    cpu_norm="$(awk -v cpu="$cpu_raw" -v cores="$CORES" 'BEGIN { printf "%.6f", cpu / cores }')"
    nodes_ram="$(awk -v a="$nodes_ram" -v b="$mem_mib" 'BEGIN { printf "%.6f", a + b }')"
    nodes_cpu="$(awk -v a="$nodes_cpu" -v b="$cpu_norm" 'BEGIN { printf "%.6f", a + b }')"
  done < <(
    docker stats --no-stream \
      --format '{{.Name}}|{{.MemUsage}}|{{.CPUPerc}}' \
      "${node_names[@]}" 2>/dev/null
  )

  printf '%s %s\n' "$nodes_ram" "$nodes_cpu"
}

avg() {
  awk -v sum="$1" -v n="$2" 'BEGIN {
    if (n <= 0) printf "0.00"
    else printf "%.2f", sum / n
  }'
}

printf "Waiting for DSIM at %s ... " "$HEALTH_URL"
deadline_s=$(( $(date +%s) + MAX_WAIT_SECONDS ))
until curl -fsS --max-time 1 "${HEALTH_URL}" >/dev/null 2>&1; do
  if (( $(date +%s) >= deadline_s )); then
    printf "timed out\n" >&2
    exit 1
  fi
  sleep "$POLL_INTERVAL"
done
printf "ready\n"

echo "============================================================"
echo " DSIM topology benchmark (local) : ${TOPO_NAME}  (${RUNS} runs)"
echo "============================================================"

import_results=()
init_results=()
global_nodes_ram=0
global_nodes_cpu=0
global_samples=0
success=0

for ((run = 1; run <= RUNS; run++)); do
  echo ""
  echo "--- Run ${run}/${RUNS} ---"

  curl -fsS -X DELETE "${API_URL}/api/v1/topology" >/dev/null 2>&1 || true
  docker ps -q --filter "ancestor=${NODE_IMAGE}" \
    | xargs -r docker rm -f >/dev/null 2>&1 || true

  printf "  loading topology ... "

  t_import_start="$(date +%s%3N)"

  if ! curl -fsS -X POST "${API_URL}/api/v1/topology" \
    -H 'Content-Type: application/json' \
    --data-binary @"${TOPO_FILE}" >/dev/null; then
    printf "failed\n"
    continue
  fi

  t_import_end="$(date +%s%3N)"
  import_ms=$(( t_import_end - t_import_start ))
  import_s="$(awk -v ms="$import_ms" 'BEGIN { printf "%.3f", ms / 1000 }')"
  printf "done  %s s\n" "$import_s"
  printf "  starting nodes ... "

  t_start="$(date +%s%3N)"

  if ! curl -fsS -X POST "${API_URL}/api/v1/nodes/toggle-all" >/dev/null; then
    printf "failed\n"
    curl -fsS -X DELETE "${API_URL}/api/v1/topology" >/dev/null 2>&1 || true
    continue
  fi

  t_end="$(date +%s%3N)"
  init_ms=$(( t_end - t_start ))
  init_s="$(awk -v ms="$init_ms" 'BEGIN { printf "%.3f", ms / 1000 }')"
  printf "done  %s s\n" "$init_s"
  printf "  sampling RAM/CPU (%ds) ... " "$SAMPLE_DURATION"

  run_nodes_ram=0
  run_nodes_cpu=0
  run_samples=0

  start_ns="$(date +%s%N)"
  deadline_ns=$(( start_ns + SAMPLE_DURATION * 1000000000 ))
  next_sample_ns=$(( start_ns + SAMPLE_INTERVAL * 1000000000 ))

  while true; do
    now_ns="$(date +%s%N)"
    (( now_ns >= deadline_ns )) && break

    if (( now_ns < next_sample_ns )); then
      sleep_sec="$(awk -v ns=$(( next_sample_ns - now_ns )) 'BEGIN { printf "%.3f", ns / 1000000000 }')"
      sleep "$sleep_sec"
    fi

    read -r nodes_ram nodes_cpu < <(sample_once)

    run_nodes_ram="$(awk -v a="$run_nodes_ram" -v b="$nodes_ram" 'BEGIN { printf "%.6f", a + b }')"
    run_nodes_cpu="$(awk -v a="$run_nodes_cpu" -v b="$nodes_cpu" 'BEGIN { printf "%.6f", a + b }')"
    run_samples=$(( run_samples + 1 ))

    now_ns="$(date +%s%N)"
    (( now_ns >= deadline_ns )) && break
    next_sample_ns=$(( next_sample_ns + SAMPLE_INTERVAL * 1000000000 ))
  done

  run_nodes_ram_avg="$(avg "$run_nodes_ram" "$run_samples")"
  run_nodes_cpu_avg="$(avg "$run_nodes_cpu" "$run_samples")"
  printf "nodes=%s MiB/%s%%\n" "$run_nodes_ram_avg" "$run_nodes_cpu_avg"

  curl -fsS -X DELETE "${API_URL}/api/v1/topology" >/dev/null 2>&1 || true
  docker ps -q --filter "ancestor=${NODE_IMAGE}" \
    | xargs -r docker rm -f >/dev/null 2>&1 || true

  import_results+=("$import_ms")
  init_results+=("$init_ms")
  global_nodes_ram="$(awk -v a="$global_nodes_ram" -v b="$run_nodes_ram" 'BEGIN { printf "%.6f", a + b }')"
  global_nodes_cpu="$(awk -v a="$global_nodes_cpu" -v b="$run_nodes_cpu" 'BEGIN { printf "%.6f", a + b }')"
  global_samples=$(( global_samples + run_samples ))
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

overall_nodes_ram_avg="$(avg "$global_nodes_ram" "$global_samples")"
overall_nodes_cpu_avg="$(avg "$global_nodes_cpu" "$global_samples")"

import_summary="$(printf '%s\n' "${import_results[@]}" | sort -n | awk \
  -v count="$success" '
{
  values[NR] = $1
  sum += $1
}
END {
  if (count % 2 == 1) median = values[int(count / 2) + 1]
  else median = (values[count / 2] + values[count / 2 + 1]) / 2
  printf "%.3f %.3f %.3f %.3f", (sum/count)/1000, median/1000, values[1]/1000, values[count]/1000
}
')"
read -r imp_avg imp_med imp_min imp_max <<< "$import_summary"

printf '%s\n' "${init_results[@]}" | sort -n | awk \
  -v count="$success" \
  -v topo="$TOPO_NAME" \
  -v imp_avg="$imp_avg" -v imp_med="$imp_med" -v imp_min="$imp_min" -v imp_max="$imp_max" \
  -v nram="$overall_nodes_ram_avg" -v ncpu="$overall_nodes_cpu_avg" '
{
  values[NR] = $1
  sum += $1
}
END {
  if (count % 2 == 1) median = values[int(count / 2) + 1]
  else median = (values[count / 2] + values[count / 2 + 1]) / 2
  printf "\n"
  printf "============================================================\n"
  printf " AVERAGES over %d runs — %s (local)\n", count, topo
  printf "============================================================\n"
  printf "  %-25s : %s s\n",  "import time avg",    imp_avg
  printf "  %-25s : %s s\n",  "import time median", imp_med
  printf "  %-25s : %s s\n",  "import time min",    imp_min
  printf "  %-25s : %s s\n",  "import time max",    imp_max
  printf "  %-25s : %.3f s\n", "start time avg",    (sum / count) / 1000
  printf "  %-25s : %.3f s\n", "start time median", median / 1000
  printf "  %-25s : %.3f s\n", "start time min",    values[1] / 1000
  printf "  %-25s : %.3f s\n", "start time max",    values[count] / 1000
  printf "  %-25s : %s MiB\n", "nodes RAM",         nram
  printf "  %-25s : %s %%\n",  "nodes CPU",         ncpu
  printf "============================================================\n"
}
'
