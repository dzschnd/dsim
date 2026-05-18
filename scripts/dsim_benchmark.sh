#!/usr/bin/env bash
set -euo pipefail

RUNS="${1:-7}"

CONTAINER_BASE="dsim-bench"
IMAGE="dzschnd/dsim:latest"
HOST_PORT="8080"
CONTAINER_PORT="8080"
URL="http://localhost:${HOST_PORT}"

POLL_INTERVAL="0.05"
MAX_WAIT_SECONDS=60
SAMPLE_DURATION=30
SAMPLE_INTERVAL=1
GAP_SECONDS=5
CORES="$(nproc)"

if ! [[ "$RUNS" =~ ^[0-9]+$ ]] || (( RUNS < 1 )); then
  echo "Usage: $0 [runs]" >&2
  exit 2
fi

CURRENT_CONTAINER=""

cleanup() {
  if [[ -n "$CURRENT_CONTAINER" ]]; then
    docker rm -f "$CURRENT_CONTAINER" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
  fi
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

avg() {
  awk -v sum="$1" -v n="$2" 'BEGIN {
    if (n <= 0) printf "0.00"
    else printf "%.2f", sum / n
  }'
}

echo "============================================================"
echo " DSIM startup benchmark  (${RUNS} runs)"
echo "============================================================"

startup_results=()
global_app_ram=0
global_app_cpu=0
global_samples=0
success=0

for ((run = 1; run <= RUNS; run++)); do
  echo ""
  echo "--- Run ${run}/${RUNS} ---"

  container_name="${CONTAINER_BASE}-${run}-$$"
  CURRENT_CONTAINER="$container_name"
  docker rm -f "$container_name" >/dev/null 2>&1 || true

  printf "  starting DSIM ... "

  start_ms="$(date +%s%3N)"

  if ! docker run -d \
    --name "$container_name" \
    --pid host \
    --cap-add NET_ADMIN \
    -p "${HOST_PORT}:${CONTAINER_PORT}" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    "$IMAGE" >/dev/null; then
    printf "docker run failed\n"
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  deadline_s=$(( $(date +%s) + MAX_WAIT_SECONDS ))
  failed=0

  until curl -fsS --max-time 1 "$URL" >/dev/null 2>&1; do
    if (( $(date +%s) >= deadline_s )); then
      failed=1
      break
    fi
    sleep "$POLL_INTERVAL"
  done

  if (( failed )); then
    printf "timed out\n"
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    CURRENT_CONTAINER=""
    continue
  fi

  end_ms="$(date +%s%3N)"
  startup_ms=$(( end_ms - start_ms ))
  startup_s="$(awk -v ms="$startup_ms" 'BEGIN { printf "%.3f", ms / 1000 }')"
  printf "ready  %s s\n" "$startup_s"

  printf "  sampling idle RAM/CPU (%ds) ... " "$SAMPLE_DURATION"

  run_app_ram=0
  run_app_cpu=0
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

    while IFS='|' read -r name mem_usage cpu_perc; do
      [[ -n "$name" ]] || continue
      mem_used="${mem_usage%% / *}"
      mem_mib="$(mem_to_mib "$mem_used")"
      cpu_raw="${cpu_perc%\%}"
			cpu_norm="$cpu_raw"
      run_app_ram="$(awk -v a="$run_app_ram" -v b="$mem_mib" 'BEGIN { printf "%.6f", a + b }')"
      run_app_cpu="$(awk -v a="$run_app_cpu" -v b="$cpu_norm" 'BEGIN { printf "%.6f", a + b }')"
    done < <(
      docker stats --no-stream \
        --format '{{.Name}}|{{.MemUsage}}|{{.CPUPerc}}' \
        "$container_name" 2>/dev/null
    )

    run_samples=$(( run_samples + 1 ))
    now_ns="$(date +%s%N)"
    (( now_ns >= deadline_ns )) && break
    next_sample_ns=$(( next_sample_ns + SAMPLE_INTERVAL * 1000000000 ))
  done

  docker rm -f "$container_name" >/dev/null 2>&1 || true
  CURRENT_CONTAINER=""

  run_app_ram_avg="$(avg "$run_app_ram" "$run_samples")"
  run_app_cpu_avg="$(avg "$run_app_cpu" "$run_samples")"
  printf "app_RAM=%s MiB  app_CPU=%s%%\n" "$run_app_ram_avg" "$run_app_cpu_avg"

  startup_results+=("$startup_ms")
  global_app_ram="$(awk -v a="$global_app_ram" -v b="$run_app_ram" 'BEGIN { printf "%.6f", a + b }')"
  global_app_cpu="$(awk -v a="$global_app_cpu" -v b="$run_app_cpu" 'BEGIN { printf "%.6f", a + b }')"
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

overall_app_ram_avg="$(avg "$global_app_ram" "$global_samples")"
overall_app_cpu_avg="$(avg "$global_app_cpu" "$global_samples")"

printf '%s\n' "${startup_results[@]}" | sort -n | awk \
  -v count="$success" \
  -v ram="$overall_app_ram_avg" \
  -v cpu="$overall_app_cpu_avg" '
{
  values[NR] = $1
  sum += $1
}
END {
  if (count % 2 == 1) median = values[int(count / 2) + 1]
  else median = (values[count / 2] + values[count / 2 + 1]) / 2
  printf "\n"
  printf "============================================================\n"
  printf " AVERAGES over %d runs\n", count
  printf "============================================================\n"
  printf "  %-25s : %.3f s\n", "startup avg",    (sum / count) / 1000
  printf "  %-25s : %.3f s\n", "startup median", median / 1000
  printf "  %-25s : %.3f s\n", "startup min",    values[1] / 1000
  printf "  %-25s : %.3f s\n", "startup max",    values[count] / 1000
  printf "  %-25s : %s MiB\n", "app RAM avg",    ram
  printf "  %-25s : %s %%\n",  "app CPU avg",    cpu
  printf "============================================================\n"
}
'
