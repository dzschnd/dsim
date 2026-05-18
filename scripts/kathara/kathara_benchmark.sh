#!/usr/bin/env bash

LAB_DIR="${1:-}"
RUNS="${2:-7}"
SETTLE_SECS=2
SAMPLE_SECS=2
KATHARA_BIN="/home/dzschnd/Study/Thesis/kathara-env/bin/python -m kathara"

if [[ -z "$LAB_DIR" ]]; then
    echo "Usage: sudo bash kathara_benchmark.sh <lab_dir> [runs=7]"
    exit 1
fi
if [[ $EUID -ne 0 ]]; then
    echo "ERROR: Must be run as root (sudo)."
    exit 1
fi
if [[ ! -d "$LAB_DIR" ]]; then
    echo "ERROR: Directory not found: $LAB_DIR"
    exit 1
fi
if [[ ! -f "$LAB_DIR/lab.conf" ]]; then
    echo "ERROR: No lab.conf found in $LAB_DIR"
    exit 1
fi

LAB_DIR="$(cd "$LAB_DIR" && pwd)"
LAB_NAME="$(basename "$LAB_DIR")"

expected_devices() {
    grep -oP '^\w+(?=\[)' "$LAB_DIR/lab.conf" | sort -u | wc -l
}

running_devices() {
    docker ps --format '{{.Names}}' 2>/dev/null \
        | grep "kathara_dzschnd" \
        | wc -l
}

lab_ram_mb() {
    docker stats --no-stream --format '{{.Name}}|{{.MemUsage}}' 2>/dev/null \
        | grep "kathara_dzschnd" \
        | awk -F'|' '
            function to_mib(s,    val, unit) {
                gsub(/^[ \t]+|[ \t]+$/, "", s)
                val = s; unit = s
                gsub(/[^0-9.]/, "", val)
                gsub(/[0-9.]/, "", unit)
                val += 0
                if (unit == "B")   return val / 1024 / 1024
                if (unit == "KiB") return val / 1024
                if (unit == "MiB") return val
                if (unit == "GiB") return val * 1024
                return val
            }
            {
                split($2, parts, " / ")
                total += to_mib(parts[1])
            }
            END { printf "%.2f", total }
        '
}

read_cpu_stat() {
    awk '/^cpu /{
        idle = $5 + $6;
        total = 0; for (i=2; i<=NF; i++) total += $i;
        printf "%d %d\n", idle, total;
        exit
    }' /proc/stat
}

cpu_pct_over_window() {
    local secs=$1
    local idle1 total1 idle2 total2
    read -r idle1 total1 <<< "$(read_cpu_stat)"
    sleep "$secs"
    read -r idle2 total2 <<< "$(read_cpu_stat)"
    local dtotal=$(( total2 - total1 ))
    local didle=$(( idle2 - idle1 ))
    if [[ $dtotal -le 0 ]]; then echo "0.00"
    else awk "BEGIN{printf \"%.2f\", (1 - $didle/$dtotal)*100}"
    fi
}

cleanup() {
    ( cd "$LAB_DIR" && $KATHARA_BIN lclean ) > /dev/null 2>&1 || true
    sleep 2
}

run_once() {
    local run_num=$1
    local expected
    expected=$(expected_devices)

    local cpu_baseline
    cpu_baseline=$(cpu_pct_over_window "$SAMPLE_SECS")

    local t_start t_end init_time
    t_start=$(date +%s%3N)

    ( cd "$LAB_DIR" && $KATHARA_BIN lstart ) > /dev/null 2>&1

    t_end=$(date +%s%3N)
    init_time=$(awk "BEGIN{printf \"%.4f\", ($t_end - $t_start) / 1000}")

    local deadline=$(( $(date +%s) + 60 ))
    while true; do
        local running
        running=$(running_devices)
        [[ "$running" -ge "$expected" ]] && break
        if [[ $(date +%s) -gt $deadline ]]; then
            echo "  run=$run_num  ERROR: devices did not all start within 60s" >&2
            cleanup
            LAST_TIME=""; LAST_RAM=""; LAST_CPU=""
            return 1
        fi
        sleep 0.5
    done

    sleep "$SETTLE_SECS"

    local ram_mb
    ram_mb=$(lab_ram_mb)

    local cpu_raw cpu_pct
    cpu_raw=$(cpu_pct_over_window "$SAMPLE_SECS")
    cpu_pct=$(awk "BEGIN{printf \"%.2f\", $cpu_raw - $cpu_baseline}")

    cleanup

    printf "  run=%-2d  time=%-8s s  RAM=%-8s MB  CPU=%s%%\n" \
        "$run_num" "$init_time" "$ram_mb" "$cpu_pct"

    LAST_TIME="$init_time"
    LAST_RAM="$ram_mb"
    LAST_CPU="$cpu_pct"
}

echo "============================================================"
echo " Benchmark : $LAB_NAME"
echo " Runs      : $RUNS"
echo " Devices   : $(expected_devices)"
echo "============================================================"

cleanup

sum_time=0
sum_ram=0
sum_cpu=0
success=0

for i in $(seq 1 "$RUNS"); do
    run_once "$i"
    if [[ -z "${LAST_TIME:-}" ]]; then
        sleep 2
        continue
    fi
    sum_time=$(awk "BEGIN{printf \"%.6f\", $sum_time + $LAST_TIME}")
    sum_ram=$( awk "BEGIN{printf \"%.6f\", $sum_ram  + $LAST_RAM}")
    sum_cpu=$( awk "BEGIN{printf \"%.6f\", $sum_cpu  + $LAST_CPU}")
    success=$(( success + 1 ))
    sleep 2
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
