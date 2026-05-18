#!/usr/bin/env bash
# =============================================================================
# mn_benchmark.sh — Measure Mininet topology init time, idle RAM, idle CPU
#
# Usage:
#   sudo bash mn_benchmark.sh <topology_script.py> [runs=7]
#
# Examples:
#   sudo bash mn_benchmark.sh mn_t1.py
#   sudo bash mn_benchmark.sh mn_t2.py 7
#   sudo bash mn_benchmark.sh mn_t3.py 7
#
# Metrics:
#   Time — wall-clock of net.start() printed by the topology script
#   RAM  — RSS of the topology python3 process + all its child processes
#   CPU  — %CPU (non-idle) during 2s idle window minus 2s pre-start baseline
#
# The topology scripts must support --bench flag (they sleep 8s while up,
# allowing the benchmark to sample RAM/CPU, then exit cleanly).
# =============================================================================

TOPO_SCRIPT="${1:-}"
RUNS="${2:-7}"
SETTLE_SECS=2    # seconds to wait after topology up before sampling
SAMPLE_SECS=2    # seconds for each CPU sample window
# bench_secs in the python script must be > SETTLE_SECS + SAMPLE_SECS + margin

if [[ -z "$TOPO_SCRIPT" ]]; then
    echo "Usage: sudo bash mn_benchmark.sh <topology_script.py> [runs=7]"
    exit 1
fi
if [[ $EUID -ne 0 ]]; then
    echo "ERROR: Must be run as root (sudo)."
    exit 1
fi
if [[ ! -f "$TOPO_SCRIPT" ]]; then
    echo "ERROR: File not found: $TOPO_SCRIPT"
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$TOPO_SCRIPT")" && pwd)"
SCRIPT_NAME="$(basename "$TOPO_SCRIPT")"

# ---------------------------------------------------------------------------
# RSS of a process and all its children, in kB
# ---------------------------------------------------------------------------
tree_rss_kb() {
    local root_pid=$1
    local all_pids
    # recursive: get children of children too
    all_pids=$(pstree -p "$root_pid" 2>/dev/null \
        | grep -oP '\(\d+\)' | tr -d '()' || true)
    all_pids="$root_pid $all_pids"

    local total=0
    for p in $all_pids; do
        [[ -z "$p" ]] && continue
        local rss
        rss=$(awk '/^VmRSS:/{print $2; exit}' /proc/"$p"/status 2>/dev/null || echo 0)
        total=$(( total + rss ))
    done
    echo "$total"
}

# ---------------------------------------------------------------------------
# /proc/stat -> "idle_ticks total_ticks" for aggregate cpu line
# ---------------------------------------------------------------------------
read_cpu_stat() {
    awk '/^cpu /{
        idle = $5 + $6;
        total = 0; for (i=2; i<=NF; i++) total += $i;
        printf "%d %d\n", idle, total;
        exit
    }' /proc/stat
}

# ---------------------------------------------------------------------------
# %CPU (non-idle) over a window of $1 seconds
# ---------------------------------------------------------------------------
cpu_pct_over_window() {
    local secs=$1
    local idle1 total1 idle2 total2
    read -r idle1 total1 <<< "$(read_cpu_stat)"
    sleep "$secs"
    read -r idle2 total2 <<< "$(read_cpu_stat)"
    local dtotal=$(( total2 - total1 ))
    local didle=$(( idle2 - idle1 ))
    if [[ $dtotal -le 0 ]]; then
        echo "0.00"
    else
        awk "BEGIN{printf \"%.2f\", (1 - $didle/$dtotal)*100}"
    fi
}

# ---------------------------------------------------------------------------
# Cleanup — only run between runs, never while a topo process is alive
# ---------------------------------------------------------------------------
cleanup() {
    mn -c > /dev/null 2>&1 || true
    sleep 1
}

# ---------------------------------------------------------------------------
# Single run — sets globals LAST_TIME, LAST_RAM, LAST_CPU
# ---------------------------------------------------------------------------
run_once() {
    local run_num=$1

    # CPU baseline BEFORE topology starts
    local cpu_baseline
    cpu_baseline=$(cpu_pct_over_window "$SAMPLE_SECS")

    local tmpout
    tmpout=$(mktemp /tmp/mn_bench_XXXXXX.log)

    # Launch topology with --bench: it stays up for 8s after init then exits
    ( cd "$SCRIPT_DIR" && python3 "$SCRIPT_NAME" --bench ) > "$tmpout" 2>&1 &
    local topo_pid=$!

    # Wait for init-time line (topology is fully up)
    local deadline=$(( $(date +%s) + 120 ))
    local init_time=""
    while [[ -z "$init_time" ]]; do
        if [[ $(date +%s) -gt $deadline ]]; then
            echo "  run=$run_num  ERROR: topology did not come up within 120s" >&2
            echo "  --- script output ---" >&2
            cat "$tmpout" >&2
            echo "  ---------------------" >&2
            kill "$topo_pid" 2>/dev/null || true
            wait "$topo_pid" 2>/dev/null || true
            cleanup
            rm -f "$tmpout"
            LAST_TIME=""
            LAST_RAM=""
            LAST_CPU=""
            return 1
        fi
        init_time=$(grep -oP '(?<=Topology init time: )\d+\.\d+' "$tmpout" 2>/dev/null || true)
        sleep 0.1
    done

    # Settle, then sample while topology is still sleeping (--bench keeps it up)
    sleep "$SETTLE_SECS"

    # RAM: RSS of python3 + all Mininet child processes
    local ram_kb
    ram_kb=$(tree_rss_kb "$topo_pid")
    local ram_mb
    ram_mb=$(awk "BEGIN{printf \"%.2f\", $ram_kb / 1024}")

    # CPU: idle window minus baseline
    local cpu_raw
    cpu_raw=$(cpu_pct_over_window "$SAMPLE_SECS")
    local cpu_pct
    cpu_pct=$(awk "BEGIN{printf \"%.2f\", $cpu_raw - $cpu_baseline}")

    # Wait for the topology script to finish its bench sleep and exit cleanly
    wait "$topo_pid" 2>/dev/null || true
    rm -f "$tmpout"

    # Now safe to clean up OVS state for next run
    cleanup

    printf "  run=%-2d  time=%-8s s  RAM=%-8s MB  CPU=%s%%\n" \
        "$run_num" "$init_time" "$ram_mb" "$cpu_pct"

    LAST_TIME="$init_time"
    LAST_RAM="$ram_mb"
    LAST_CPU="$cpu_pct"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
echo "============================================================"
echo " Benchmark : $SCRIPT_NAME"
echo " Runs      : $RUNS"
echo " RAM method: RSS of topology process tree (python3 + children)"
echo " CPU method: topo idle delta minus pre-start baseline, ${SAMPLE_SECS}s each"
echo "============================================================"

cleanup   # clean slate

sum_time=0
sum_ram=0
sum_cpu=0
success=0

for i in $(seq 1 "$RUNS"); do
    run_once "$i"
    if [[ -z "${LAST_TIME:-}" ]]; then
        echo "  run=$i skipped due to error" >&2
        sleep 2
        continue
    fi
    sum_time=$(awk "BEGIN{printf \"%.6f\", $sum_time + $LAST_TIME}")
    sum_ram=$( awk "BEGIN{printf \"%.6f\", $sum_ram  + $LAST_RAM}")
    sum_cpu=$( awk "BEGIN{printf \"%.6f\", $sum_cpu  + $LAST_CPU}")
    success=$(( success + 1 ))
    sleep 2   # cooldown between runs
done

echo "------------------------------------------------------------"
if [[ $success -gt 0 ]]; then
    avg_time=$(awk "BEGIN{printf \"%.2f\", $sum_time / $success}")
    avg_ram=$( awk "BEGIN{printf \"%.2f\", $sum_ram  / $success}")
    avg_cpu=$( awk "BEGIN{printf \"%.2f\", $sum_cpu  / $success}")
    printf " >>> AVERAGE over %d runs:\n"  "$success"
    printf "     Time = %s s\n"  "$avg_time"
    printf "     RAM  = %s MB\n" "$avg_ram"
    printf "     CPU  = %s %%\n" "$avg_cpu"
else
    echo " >>> No successful runs to average."
fi
echo "============================================================"
