#!/usr/bin/env bash

LAB_DIR="${1:-}"
BASELINE="${2:-}"
KATHARA_BIN="/home/dzschnd/Study/Thesis/kathara-env/bin/python -m kathara"
DELAY_MS=100
JITTER_MS=20
LOSS_PCT=10
BW_CAP_KBIT=1000
BW_BURST="32kbit"
BW_LATENCY="400ms"
PING_COUNT=100
PING_COUNT_LOSS=50
PING_INTERVAL=0.2
IPERF_SECS=10

if [[ -z "$LAB_DIR" ]]; then
    echo "Usage: sudo bash kathara_tc_bench.sh <lab_dir> <baseline_rtt_ms>"
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
if [[ -z "$BASELINE" ]]; then
    read -rp "Enter baseline RTT (ms): " BASELINE
fi

LAB_DIR="$(cd "$LAB_DIR" && pwd)"
LAB_NAME="$(basename "$LAB_DIR")"

case "$LAB_NAME" in
    t1) SRC="h1"; DST="h2"; DST_IP="192.168.10.11"; SRC_IFACE="eth0"; DST_IFACE="eth0" ;;
    t2) SRC="h1"; DST="h4"; DST_IP="192.168.20.11"; SRC_IFACE="eth0"; DST_IFACE="eth0" ;;
    t3) SRC="h1"; DST="h14"; DST_IP="10.0.4.11";   SRC_IFACE="eth0"; DST_IFACE="eth0" ;;
    *)  echo "ERROR: Unknown topology '$LAB_NAME'."; exit 1 ;;
esac

EFFECTIVE_LOSS=$(awk "BEGIN{printf \"%.1f\", (1-(1-$LOSS_PCT/100)^2)*100}")

kexec() {
    local device="$1"; shift
    $KATHARA_BIN exec --directory "$LAB_DIR" "$device" -- "$@" 2>/dev/null
}

kexec_detach() {
    local device="$1"; shift
    $KATHARA_BIN exec --directory "$LAB_DIR" "$device" -- bash -c "nohup $* >/dev/null 2>&1 &" 2>/dev/null
    sleep 1
}

apply_netem() {
    local device="$1" iface="$2" condition="$3"
    kexec "$device" tc qdisc del dev "$iface" root 2>/dev/null || true
    kexec "$device" tc qdisc add dev "$iface" root netem $condition
}

apply_tbf() {
    local device="$1" iface="$2"
    kexec "$device" tc qdisc del dev "$iface" root 2>/dev/null || true
    kexec "$device" tc qdisc add dev "$iface" root tbf rate "${BW_CAP_KBIT}kbit" burst "$BW_BURST" latency "$BW_LATENCY"
}

reset_tc() {
    local device="$1" iface="$2"
    kexec "$device" tc qdisc del dev "$iface" root 2>/dev/null || true
}

cleanup() {
    ( cd "$LAB_DIR" && $KATHARA_BIN lclean ) > /dev/null 2>&1 || true
    sleep 2
}

start_lab() {
    ( cd "$LAB_DIR" && $KATHARA_BIN lstart ) > /dev/null 2>&1
    sleep 3
}

echo "============================================================"
echo " Kathara tc/netem fidelity : $LAB_NAME"
echo " Baseline RTT              : $BASELINE ms"
echo " Effective loss (2 NICs)   : $EFFECTIVE_LOSS %"
echo "============================================================"

cleanup
start_lab

printf "\n [1/4] Delay  (configured: %dms on both NICs)\n" "$DELAY_MS"
apply_netem "$SRC" "$SRC_IFACE" "delay ${DELAY_MS}ms"
apply_netem "$DST" "$DST_IFACE" "delay ${DELAY_MS}ms"
sleep 0.3
ping_out=$(kexec "$SRC" ping -c "$PING_COUNT" -i "$PING_INTERVAL" -q "$DST_IP")
reset_tc "$SRC" "$SRC_IFACE"
reset_tc "$DST" "$DST_IFACE"
avg_rtt=$(echo "$ping_out" | grep -oP 'rtt.*= \K[\d.]+(?=/[\d.]+/[\d.]+/[\d.]+ ms)')
configured_delta=$(( DELAY_MS * 2 ))
observed_delta=$(awk "BEGIN{printf \"%.2f\", ${avg_rtt:-0} - $BASELINE}")
error_delay=$(awk "BEGIN{printf \"%.2f\", $observed_delta - $configured_delta}")
printf "   baseline RTT : %s ms\n" "$BASELINE"
printf "   observed RTT : %s ms\n" "${avg_rtt:-ERR}"
printf "   RTT delta    : %s ms  (configured +%d ms)\n" "$observed_delta" "$configured_delta"
printf "   error        : %s ms\n" "$error_delay"

printf "\n [2/4] Jitter (configured: delay=%dms jitter=%dms on both NICs)\n" "$DELAY_MS" "$JITTER_MS"
apply_netem "$SRC" "$SRC_IFACE" "delay ${DELAY_MS}ms ${JITTER_MS}ms"
apply_netem "$DST" "$DST_IFACE" "delay ${DELAY_MS}ms ${JITTER_MS}ms"
sleep 0.3
ping_out=$(kexec "$SRC" ping -c "$PING_COUNT" -i "$PING_INTERVAL" -q "$DST_IP")
reset_tc "$SRC" "$SRC_IFACE"
reset_tc "$DST" "$DST_IFACE"
mdev=$(echo "$ping_out" | grep -oP 'rtt.*/.*/.*?/\K[\d.]+(?= ms)')
error_jitter=$(awk "BEGIN{printf \"%.2f\", ${mdev:-0} - $JITTER_MS}")
printf "   observed mdev : %s ms  (configured %d ms)\n" "${mdev:-ERR}" "$JITTER_MS"
printf "   error         : %s ms\n" "$error_jitter"

printf "\n [3/4] Loss   (configured: %d%% per NIC, effective %s%% end-to-end, %d packets)\n" \
    "$LOSS_PCT" "$EFFECTIVE_LOSS" "$PING_COUNT_LOSS"
apply_netem "$SRC" "$SRC_IFACE" "loss ${LOSS_PCT}%"
apply_netem "$DST" "$DST_IFACE" "loss ${LOSS_PCT}%"
sleep 0.3
ping_out=$(kexec "$SRC" ping -c "$PING_COUNT_LOSS" -i "$PING_INTERVAL" -q "$DST_IP")
reset_tc "$SRC" "$SRC_IFACE"
reset_tc "$DST" "$DST_IFACE"
observed_loss=$(echo "$ping_out" | grep -oP '[\d.]+(?=% packet loss)')
error_loss=$(awk "BEGIN{printf \"%.1f\", ${observed_loss:-0} - $EFFECTIVE_LOSS}")
printf "   observed loss : %s%%  (effective configured %s%%)\n" "${observed_loss:-ERR}" "$EFFECTIVE_LOSS"
printf "   error         : %s%%\n" "$error_loss"

printf "\n [4/4] Bandwidth cap (configured: %d kbit/s on both NICs, iperf3 %ds)\n" \
    "$BW_CAP_KBIT" "$IPERF_SECS"
apply_tbf "$SRC" "$SRC_IFACE"
apply_tbf "$DST" "$DST_IFACE"
sleep 0.3
kexec_detach "$DST" "iperf3 -s -1 -p 5203"
iperf_out=$(kexec "$SRC" iperf3 -c "$DST_IP" -p 5203 -t "$IPERF_SECS" -f m 2>/dev/null)
reset_tc "$SRC" "$SRC_IFACE"
reset_tc "$DST" "$DST_IFACE"
observed_bw=$(echo "$iperf_out" | grep -oP '[\d.]+(?= Mbits/sec.*sender)' | tail -1)
error_bw=$(awk "BEGIN{printf \"%.3f\", ${observed_bw:-0} - 1.0}")
printf "   observed : %s Mbit/s  (configured 1.000 Mbit/s)\n" "${observed_bw:-ERR}"
printf "   error    : %s Mbit/s\n" "$error_bw"

cleanup

echo ""
echo "============================================================"
echo " SUMMARY — $LAB_NAME"
printf "  %-20s %12s %12s %10s\n" "Condition" "Configured" "Observed" "Error"
printf "  %-20s %12s %12s %10s\n" "--------------------" "------------" "------------" "----------"
printf "  %-20s %12s %12s %10s\n" "Delay" "${configured_delta}ms" "${observed_delta}ms" "$error_delay"
printf "  %-20s %12s %12s %10s\n" "Jitter" "${JITTER_MS}ms" "${mdev:-ERR}ms" "$error_jitter"
printf "  %-20s %12s %12s %10s\n" "Loss (effective)" "${EFFECTIVE_LOSS}%" "${observed_loss:-ERR}%" "$error_loss"
printf "  %-20s %12s %12s %10s\n" "Bandwidth cap" "1.0Mbit/s" "${observed_bw:-ERR}Mbit/s" "$error_bw"
echo "============================================================"
