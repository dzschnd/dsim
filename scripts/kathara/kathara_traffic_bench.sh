#!/usr/bin/env bash

LAB_DIR="${1:-}"
RUNS="${2:-7}"
KATHARA_BIN="/home/dzschnd/Study/Thesis/kathara-env/bin/python -m kathara"
PING_COUNT=20
PING_INTERVAL=0.2
IPERF_DURATION=30
UDP_BITRATE="100M"
BW_CAP_KBIT=1000
BW_BURST="32kbit"
BW_LATENCY="400ms"
CPU_SETTLE=5
CPU_SAMPLE=5

if [[ -z "$LAB_DIR" ]]; then
    echo "Usage: sudo bash kathara_traffic_bench.sh <lab_dir> [runs=7]"
    exit 1
fi
if [[ $EUID -ne 0 ]]; then
    echo "ERROR: Must be run as root (sudo)."
    exit 1
fi
if [[ ! -f "$LAB_DIR/lab.conf" ]]; then
    echo "ERROR: No lab.conf in $LAB_DIR"
    exit 1
fi

LAB_DIR="$(cd "$LAB_DIR" && pwd)"
LAB_NAME="$(basename "$LAB_DIR")"

case "$LAB_NAME" in
    t1) SRC="h1"; DST="h2"; DST_IP="192.168.10.11"; SRC_IFACE="eth0"; DST_IFACE="eth0" ;;
    t2) SRC="h1"; DST="h4"; DST_IP="192.168.20.11"; SRC_IFACE="eth0"; DST_IFACE="eth0" ;;
    t3) SRC="h1"; DST="h14"; DST_IP="10.0.4.11";   SRC_IFACE="eth0"; DST_IFACE="eth0" ;;
    *)  echo "ERROR: Unknown topology '$LAB_NAME'. Expected t1, t2 or t3."; exit 1 ;;
esac

kexec() {
    local device="$1"; shift
    $KATHARA_BIN exec --directory "$LAB_DIR" "$device" -- "$@" 2>/dev/null
}

kexec_detach() {
    local device="$1"; shift
    $KATHARA_BIN exec --directory "$LAB_DIR" "$device" -- bash -c "nohup $* >/dev/null 2>&1 &" 2>/dev/null
    sleep 1
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
            { split($2, parts, " / "); total += to_mib(parts[1]) }
            END { printf "%.2f", total }
        '
}

cleanup() {
    ( cd "$LAB_DIR" && $KATHARA_BIN lclean ) > /dev/null 2>&1 || true
    sleep 2
}

start_lab() {
    ( cd "$LAB_DIR" && $KATHARA_BIN lstart ) > /dev/null 2>&1
    sleep 3
}

avg() {
    local sum="${1:-0}" n="${2:-0}"
    [[ -z "$sum" || "$sum" == "ERR" ]] && sum=0
    awk -v sum="$sum" -v n="$n" 'BEGIN { if (n<=0) print "ERR"; else printf "%.2f", sum/n }'
}

run_iperf_with_resources() {
    local src_dev="$1" dst_dev="$2" dst_ip="$3" port="$4" extra_flags="$5"
    local cpu_baseline cpu_raw cpu_pct ram_mb iperf_out

    cpu_baseline=$(cpu_pct_over_window "$CPU_SAMPLE")

    kexec_detach "$dst_dev" "iperf3 -s -1 -p $port"

    ( kexec "$src_dev" iperf3 -c "$dst_ip" -p "$port" -t "$IPERF_DURATION" -f m $extra_flags > /tmp/iperf_out.txt 2>/dev/null ) &
    local iperf_pid=$!

    sleep "$CPU_SETTLE"
    ram_mb=$(lab_ram_mb)

    cpu_raw=$(cpu_pct_over_window "$CPU_SAMPLE")
    cpu_pct=$(awk "BEGIN{printf \"%.2f\", $cpu_raw - $cpu_baseline}")

    wait "$iperf_pid"
    iperf_out=$(cat /tmp/iperf_out.txt)

    LAST_IPERF_OUT="$iperf_out"
    LAST_IPERF_RAM="$ram_mb"
    LAST_IPERF_CPU="$cpu_pct"
}

run_once() {
    local run_num=$1

    cleanup
    start_lab

    local ping_out ping_avg ping_mdev
    printf "  [1/4] ping RTT ... "
    ping_out=$(kexec "$SRC" ping -c "$PING_COUNT" -i "$PING_INTERVAL" -q "$DST_IP" 2>/dev/null)
    ping_avg=$(echo "$ping_out"  | grep -oP 'rtt.*= \K[\d.]+(?=/[\d.]+/[\d.]+/[\d.]+ ms)')
    ping_mdev=$(echo "$ping_out" | grep -oP 'rtt.*/.*/.*?/\K[\d.]+(?= ms)')
    printf "avg=%s ms  mdev=%s ms\n" "${ping_avg:-ERR}" "${ping_mdev:-ERR}"

    printf "  [2/4] TCP uncapped (%ds) + RAM/CPU ... " "$IPERF_DURATION"
    run_iperf_with_resources "$SRC" "$DST" "$DST_IP" 5201 ""
    local tcp_mbits tcp_ram tcp_cpu
    tcp_mbits=$(echo "$LAST_IPERF_OUT" | grep -oP '[\d.]+(?= Mbits/sec.*sender)' | tail -1)
    tcp_ram="$LAST_IPERF_RAM"
    tcp_cpu="$LAST_IPERF_CPU"
    printf "%s Mbit/s  RAM=%s MB  CPU=%s%%\n" "${tcp_mbits:-ERR}" "${tcp_ram:-ERR}" "$tcp_cpu"

    local udp_out udp_jitter udp_loss
    printf "  [3/4] UDP jitter/loss (%ds) ... " "$IPERF_DURATION"
    kexec_detach "$DST" "iperf3 -s -1 -p 5202"
    udp_out=$(kexec "$SRC" iperf3 -c "$DST_IP" -p 5202 -u -b "$UDP_BITRATE" -t "$IPERF_DURATION" -f m 2>/dev/null)
    udp_jitter=$(echo "$udp_out" | grep -oP '[\d.]+(?= ms\s+\d+/\d+)' | tail -1)
    udp_loss=$(echo "$udp_out"   | grep -oP '\(([\d.]+)%\)'            | tail -1 | tr -d '()%')
    printf "jitter=%s ms  loss=%s%%\n" "${udp_jitter:-ERR}" "${udp_loss:-ERR}"

    printf "  [4/4] TCP capped 1Mbit/s (%ds) + RAM/CPU ... " "$IPERF_DURATION"
    kexec "$SRC" tc qdisc del dev "$SRC_IFACE" root 2>/dev/null || true
    kexec "$DST" tc qdisc del dev "$DST_IFACE" root 2>/dev/null || true
    kexec "$SRC" tc qdisc add dev "$SRC_IFACE" root tbf rate "${BW_CAP_KBIT}kbit" burst "$BW_BURST" latency "$BW_LATENCY"
    kexec "$DST" tc qdisc add dev "$DST_IFACE" root tbf rate "${BW_CAP_KBIT}kbit" burst "$BW_BURST" latency "$BW_LATENCY"
    run_iperf_with_resources "$SRC" "$DST" "$DST_IP" 5203 ""
    local cap_mbits cap_ram cap_cpu
    cap_mbits=$(echo "$LAST_IPERF_OUT" | grep -oP '[\d.]+(?= Mbits/sec.*sender)' | tail -1)
    cap_ram="$LAST_IPERF_RAM"
    cap_cpu="$LAST_IPERF_CPU"
    kexec "$SRC" tc qdisc del dev "$SRC_IFACE" root 2>/dev/null || true
    kexec "$DST" tc qdisc del dev "$DST_IFACE" root 2>/dev/null || true
    printf "%s Mbit/s  RAM=%s MB  CPU=%s%%\n" "${cap_mbits:-ERR}" "${cap_ram:-ERR}" "$cap_cpu"

    LAST_PING_AVG="$ping_avg"
    LAST_PING_MDEV="$ping_mdev"
    LAST_TCP="$tcp_mbits"
    LAST_RAM="$tcp_ram"
    LAST_CPU="$tcp_cpu"
    LAST_UDP_JITTER="$udp_jitter"
    LAST_UDP_LOSS="$udp_loss"
    LAST_CAP_TCP="$cap_mbits"
    LAST_CAP_RAM="$cap_ram"
    LAST_CAP_CPU="$cap_cpu"
}

echo "============================================================"
echo " Kathara traffic benchmark : $LAB_NAME  ($RUNS runs)"
echo " src=$SRC  dst=$DST ($DST_IP)"
echo "============================================================"

cleanup

sum_ping_avg=0;   sum_ping_mdev=0
sum_tcp=0;        sum_ram=0;       sum_cpu=0
sum_udp_jitter=0; sum_udp_loss=0
sum_cap_tcp=0;    sum_cap_ram=0;   sum_cap_cpu=0
success=0

for i in $(seq 1 "$RUNS"); do
    echo ""
    echo "--- Run $i/$RUNS ---"
    run_once "$i"
    [[ -z "${LAST_PING_AVG:-}" ]] && { sleep 2; continue; }
    sum_ping_avg=$(awk   "BEGIN{printf \"%.6f\", $sum_ping_avg   + ${LAST_PING_AVG:-0}}")
    sum_ping_mdev=$(awk  "BEGIN{printf \"%.6f\", $sum_ping_mdev  + ${LAST_PING_MDEV:-0}}")
    sum_tcp=$(awk        "BEGIN{printf \"%.6f\", $sum_tcp         + ${LAST_TCP:-0}}")
    sum_ram=$(awk        "BEGIN{printf \"%.6f\", $sum_ram         + ${LAST_RAM:-0}}")
    sum_cpu=$(awk        "BEGIN{printf \"%.6f\", $sum_cpu         + ${LAST_CPU:-0}}")
    sum_udp_jitter=$(awk "BEGIN{printf \"%.6f\", $sum_udp_jitter  + ${LAST_UDP_JITTER:-0}}")
    sum_udp_loss=$(awk   "BEGIN{printf \"%.6f\", $sum_udp_loss    + ${LAST_UDP_LOSS:-0}}")
    sum_cap_tcp=$(awk    "BEGIN{printf \"%.6f\", $sum_cap_tcp     + ${LAST_CAP_TCP:-0}}")
    sum_cap_ram=$(awk    "BEGIN{printf \"%.6f\", $sum_cap_ram     + ${LAST_CAP_RAM:-0}}")
    sum_cap_cpu=$(awk    "BEGIN{printf \"%.6f\", $sum_cap_cpu     + ${LAST_CAP_CPU:-0}}")
    success=$(( success + 1 ))
    sleep 2
done

cleanup

echo ""
echo "============================================================"
echo " AVERAGES over $success runs — $LAB_NAME"
echo "============================================================"
printf "  ping RTT avg             : %s ms\n"     "$(avg "$sum_ping_avg"   "$success")"
printf "  ping RTT mdev            : %s ms\n"     "$(avg "$sum_ping_mdev"  "$success")"
printf "  TCP throughput (uncapped): %s Mbit/s\n" "$(avg "$sum_tcp"        "$success")"
printf "  RAM under TCP (uncapped) : %s MB\n"     "$(avg "$sum_ram"        "$success")"
printf "  CPU under TCP (uncapped) : %s %%\n"     "$(avg "$sum_cpu"        "$success")"
printf "  UDP jitter               : %s ms\n"     "$(avg "$sum_udp_jitter" "$success")"
printf "  UDP loss                 : %s %%\n"     "$(avg "$sum_udp_loss"   "$success")"
printf "  TCP throughput (1Mbit/s) : %s Mbit/s\n" "$(avg "$sum_cap_tcp"   "$success")"
printf "  RAM under TCP (1Mbit/s)  : %s MB\n"     "$(avg "$sum_cap_ram"   "$success")"
printf "  CPU under TCP (1Mbit/s)  : %s %%\n"     "$(avg "$sum_cap_cpu"   "$success")"
echo "============================================================"
