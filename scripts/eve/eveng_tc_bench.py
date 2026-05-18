#!/usr/bin/env python3
"""
eveng_tc_bench.py — tc/netem fidelity benchmark for EVE-NG topology tiers.

Applies netem conditions one at a time and measures fidelity vs configured:
  - delay 100ms per interface
  - jitter 20ms per interface
  - loss 10% per interface
  - bandwidth cap 1000 kbit/s

Usage (run on EVE-NG VM as root):
  python3 eveng_tc_bench.py t1 <baseline_rtt_ms> [--runs 7]
  python3 eveng_tc_bench.py t2 <baseline_rtt_ms> [--runs 7]
  python3 eveng_tc_bench.py t3 <baseline_rtt_ms> [--runs 7]

baseline_rtt_ms: clean ping RTT with no tc applied (run a plain ping first).
"""

import sys, os, time, re, argparse, subprocess, math

TOPO_CFG = {
    't1': dict(src='192.168.10.10', dst='192.168.10.11'),
    't2': dict(src='192.168.10.10', dst='192.168.20.11'),
    't3': dict(src='10.0.1.10',     dst='10.0.4.11'),
}

DELAY_MS       = 100
JITTER_MS      = 20
LOSS_PCT       = 10
BW_CAP_KBIT    = 1000
PING_COUNT     = 100
PING_COUNT_LOSS= 50
IPERF_SECS     = 10
GAP_SECONDS    = 5

SSH_OPTS = ['-o', 'StrictHostKeyChecking=no',
            '-o', 'ConnectTimeout=10',
            '-o', 'PasswordAuthentication=no']

def ssh(host, cmd, timeout=120):
    r = subprocess.run(
        ['ssh'] + SSH_OPTS + [f'root@{host}', cmd],
        capture_output=True, text=True, timeout=timeout)
    return r.stdout + r.stderr

def ssh_bg(host, cmd):
    return subprocess.Popen(
        ['ssh'] + SSH_OPTS + [f'root@{host}', cmd],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

def tc_set_delay(host, delay_ms, jitter_ms=None):
    if jitter_ms:
        cmd = (f'tc qdisc del dev eth0 root 2>/dev/null; '
               f'tc qdisc add dev eth0 root netem delay {delay_ms}ms {jitter_ms}ms')
    else:
        cmd = (f'tc qdisc del dev eth0 root 2>/dev/null; '
               f'tc qdisc add dev eth0 root netem delay {delay_ms}ms')
    ssh(host, cmd)

def tc_set_loss(host, loss_pct):
    ssh(host, f'tc qdisc del dev eth0 root 2>/dev/null; '
              f'tc qdisc add dev eth0 root netem loss {loss_pct}%')

def tc_set_bw(host, kbit):
    ssh(host, f'tc qdisc del dev eth0 root 2>/dev/null; '
              f'tc qdisc add dev eth0 root tbf rate {kbit}kbit burst 32kbit latency 400ms')

def tc_clear(host):
    ssh(host, 'tc qdisc del dev eth0 root 2>/dev/null')

def parse_ping_avg(output):
    m = re.search(r'min/avg/max = [\d.]+/([\d.]+)/[\d.]+', output)
    if m:
        return float(m.group(1))
    rtts = [float(m) for m in re.findall(r'time=([\d.]+)', output)]
    return sum(rtts) / len(rtts) if rtts else None

def parse_ping_mdev(output):
    rtts = [float(x) for x in re.findall(r'time=([\d.]+)', output)]
    if rtts:
        mean = sum(rtts) / len(rtts)
        return math.sqrt(sum((r - mean)**2 for r in rtts) / len(rtts))
    m = re.search(r'min/avg/max = ([\d.]+)/([\d.]+)/([\d.]+)', output)
    if m:
        return (float(m.group(3)) - float(m.group(1))) / 4
    return None

def parse_ping_loss(output):
    m = re.search(r'(\d+)% packet loss', output)
    return float(m.group(1)) if m else None

def parse_iperf_tcp(output):
    for line in reversed(output.splitlines()):
        if 'sender' in line:
            m = re.search(r'([\d.]+)\s+Mbits/sec', line)
            if m:
                return float(m.group(1))
    return None

def avg(lst):
    valid = [x for x in lst if x is not None]
    return sum(valid) / len(valid) if valid else None

def fmt(v, d=2):
    return f'{v:.{d}f}' if v is not None else 'ERR'

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('topo', choices=['t1', 't2', 't3'])
    parser.add_argument('baseline_rtt_ms', type=float)
    parser.add_argument('--runs', type=int, default=7)
    args = parser.parse_args()

    if os.geteuid() != 0:
        print('ERROR: must be run as root'); sys.exit(1)

    topo     = args.topo
    baseline = args.baseline_rtt_ms
    runs     = args.runs
    src      = TOPO_CFG[topo]['src']
    dst      = TOPO_CFG[topo]['dst']

    # Effective loss with netem on both src and dst NICs
    eff_loss = (1 - (1 - LOSS_PCT / 100) ** 2) * 100
    cfg_delta = DELAY_MS * 2  # delay applied on both ends

    print('=' * 62)
    print(f' tc/netem fidelity : {topo.upper()}  ({runs} runs)')
    print(f' Baseline RTT      : {baseline} ms')
    print(f' Effective loss    : {eff_loss:.1f}% (netem on both NICs)')
    print('=' * 62)

    obs_deltas, err_delays = [], []
    obs_mdevs,  err_jitters = [], []
    obs_losses, err_losses  = [], []
    obs_bws,    err_bws     = [], []

    for i in range(1, runs + 1):
        print(f'\n--- Run {i}/{runs} ---')

        # [1] Delay
        print(f'  [1/4] Delay (configured: {DELAY_MS}ms on both NICs) ...',
              end=' ', flush=True)
        tc_set_delay(src, DELAY_MS)
        tc_set_delay(dst, DELAY_MS)
        out = ssh(src, f'ping -c {PING_COUNT} -i 0.1 -q {dst}',
                  timeout=PING_COUNT * 0.5 + 20)
        tc_clear(src); tc_clear(dst)
        obs_rtt  = parse_ping_avg(out)
        obs_delta = (obs_rtt - baseline) if obs_rtt else None
        err_delay = (obs_delta - cfg_delta) if obs_delta is not None else None
        obs_deltas.append(obs_delta); err_delays.append(err_delay)
        print(f'RTT={fmt(obs_rtt)} ms  delta={fmt(obs_delta)} ms  '
              f'(cfg +{cfg_delta}ms)  err={fmt(err_delay)} ms')

        # [2] Jitter
        print(f'  [2/4] Jitter (delay={DELAY_MS}ms jitter={JITTER_MS}ms) ...',
              end=' ', flush=True)
        tc_set_delay(src, DELAY_MS, JITTER_MS)
        tc_set_delay(dst, DELAY_MS, JITTER_MS)
        out = ssh(src, f'ping -c {PING_COUNT} -i 0.1 -q {dst}',
                  timeout=PING_COUNT * 0.5 + 20)
        tc_clear(src); tc_clear(dst)
        obs_mdev  = parse_ping_mdev(out)
        err_jitter = (obs_mdev - JITTER_MS) if obs_mdev is not None else None
        obs_mdevs.append(obs_mdev); err_jitters.append(err_jitter)
        print(f'mdev={fmt(obs_mdev)} ms  (cfg {JITTER_MS}ms)  '
              f'err={fmt(err_jitter)} ms')

        # [3] Loss
        print(f'  [3/4] Loss ({LOSS_PCT}% per NIC, effective {eff_loss:.1f}%) ...',
              end=' ', flush=True)
        tc_set_loss(src, LOSS_PCT)
        tc_set_loss(dst, LOSS_PCT)
        out = ssh(src, f'ping -c {PING_COUNT_LOSS} -i 0.2 {dst}',
                  timeout=PING_COUNT_LOSS * 0.5 + 20)
        tc_clear(src); tc_clear(dst)
        obs_loss  = parse_ping_loss(out)
        err_loss  = (obs_loss - eff_loss) if obs_loss is not None else None
        obs_losses.append(obs_loss); err_losses.append(err_loss)
        print(f'loss={fmt(obs_loss)}%  (cfg {eff_loss:.1f}%)  '
              f'err={fmt(err_loss)}%')

        # [4] Bandwidth cap
        print(f'  [4/4] BW cap ({BW_CAP_KBIT} kbit/s, iperf3 {IPERF_SECS}s) ...',
              end=' ', flush=True)
        tc_set_bw(src, BW_CAP_KBIT)
        tc_set_bw(dst, BW_CAP_KBIT)
        ssh(dst, 'pkill iperf3 2>/dev/null; sleep 0.2')
        srv = ssh_bg(dst, 'iperf3 -s -1 -p 5205')
        time.sleep(0.5)
        cli_out = ssh(src,
            f'iperf3 -c {dst} -p 5205 -t {IPERF_SECS} -f m',
            timeout=IPERF_SECS + 15)
        ssh(dst, 'pkill iperf3 2>/dev/null')
        tc_clear(src); tc_clear(dst)
        obs_bw  = parse_iperf_tcp(cli_out)
        err_bw  = (obs_bw - 1.0) if obs_bw is not None else None
        obs_bws.append(obs_bw); err_bws.append(err_bw)
        print(f'{fmt(obs_bw)} Mbit/s  (cfg 1.000)  err={fmt(err_bw, 3)} Mbit/s')

        if i < runs:
            time.sleep(GAP_SECONDS)

    # ── Summary ───────────────────────────────────────────────────────────────
    n = runs
    print()
    print('=' * 62)
    print(f' AVERAGES over {n} runs — {topo.upper()}')
    print(f'  {"Condition":<20} {"Configured":>12} {"Observed":>12} {"Error":>10}')
    print(f'  {"-"*20} {"-"*12} {"-"*12} {"-"*10}')
    print(f'  {"Delay":<20} {str(cfg_delta)+"ms":>12} '
          f'{fmt(avg(obs_deltas))+"ms":>12} {fmt(avg(err_delays))+"ms":>10}')
    print(f'  {"Jitter":<20} {str(JITTER_MS)+"ms":>12} '
          f'{fmt(avg(obs_mdevs))+"ms":>12} {fmt(avg(err_jitters))+"ms":>10}')
    print(f'  {"Loss (effective)":<20} {str(round(eff_loss,1))+"%":>12} '
          f'{fmt(avg(obs_losses))+"%":>12} {fmt(avg(err_losses))+"%":>10}')
    print(f'  {"BW cap":<20} {"1.0Mbit/s":>12} '
          f'{fmt(avg(obs_bws))+" Mbit/s":>12} {fmt(avg(err_bws),3)+" Mbit/s":>10}')
    print('=' * 62)

if __name__ == '__main__':
    main()
