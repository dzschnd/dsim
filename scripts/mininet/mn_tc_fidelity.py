#!/usr/bin/env python3

import sys, os, time, re, argparse

sys.path.insert(0, os.path.dirname(__file__))
from mn_traffic_bench import (
    make_net, parse_ping, parse_iperf_tcp, TOPO_CFG, cleanup, fmt
)
from mininet.log import setLogLevel

DELAY_MS        = 100
JITTER_MS       = 20
LOSS_PCT        = 10
BW_CAP_KBIT     = 1000
PING_COUNT      = 100
PING_COUNT_LOSS = 50
PING_INTERVAL   = 0.2
IPERF_SECS      = 10
BW_BURST        = '32kbit'
BW_LATENCY      = '400ms'

def apply_netem(host, iface, condition):
    host.cmd(f'tc qdisc del dev {iface} root 2>/dev/null || true')
    host.cmd(f'tc qdisc add dev {iface} root netem {condition}')

def apply_tbf(host, iface, rate_kbit, burst, latency):
    host.cmd(f'tc qdisc del dev {iface} root 2>/dev/null || true')
    host.cmd(f'tc qdisc add dev {iface} root tbf rate {rate_kbit}kbit burst {burst} latency {latency}')

def reset_tc(host, iface):
    host.cmd(f'tc qdisc del dev {iface} root 2>/dev/null || true')

def do_ping(src, dst_ip, count, interval=0.2):
    out = src.cmd(f'ping -c {count} -i {interval} -q {dst_ip}')
    return parse_ping(out)

def do_iperf_tcp(src, dst, dst_ip):
    dst.cmd('pkill iperf3 2>/dev/null; sleep 0.3')
    dst.sendCmd('iperf3 -s -1 -p 5203')
    time.sleep(0.5)
    out = src.cmd(f'iperf3 -c {dst_ip} -p 5203 -t {IPERF_SECS} -f m')
    dst.waitOutput()
    return parse_iperf_tcp(out)

def run_fidelity(topo_name, baseline_rtt_ms):
    cfg = TOPO_CFG[topo_name]
    src_iface = cfg['src_iface']
    dst_iface = cfg['dst'] + '-eth0'

    print(f'\n Building {topo_name.upper()} topology ...')
    cleanup()
    net = make_net(topo_name)
    time.sleep(1)

    src = net.get(cfg['src'])
    dst = net.get(cfg['dst'])
    dst_ip = cfg['dst_ip']
    results = {}

    print(f'\n [1/4] Delay  (configured: {DELAY_MS}ms on both NICs)')
    apply_netem(src, src_iface, f'delay {DELAY_MS}ms')
    apply_netem(dst, dst_iface, f'delay {DELAY_MS}ms')
    time.sleep(0.3)
    avg_rtt, _ = do_ping(src, dst_ip, PING_COUNT)
    reset_tc(src, src_iface)
    reset_tc(dst, dst_iface)
    if avg_rtt is not None and baseline_rtt_ms is not None:
        observed_delta = avg_rtt - baseline_rtt_ms
        configured_rtt_delta = DELAY_MS * 2
        error = observed_delta - configured_rtt_delta
        results['delay'] = dict(configured=configured_rtt_delta, observed=round(observed_delta, 2), error=round(error, 2))
        print(f'   baseline RTT : {baseline_rtt_ms:.2f} ms')
        print(f'   observed RTT : {avg_rtt:.2f} ms')
        print(f'   RTT delta    : {observed_delta:.2f} ms  (configured +{configured_rtt_delta} ms)')
        print(f'   error        : {error:+.2f} ms')
    else:
        print('   ERROR: ping failed')
        results['delay'] = None

    print(f'\n [2/4] Jitter (configured: delay={DELAY_MS}ms jitter={JITTER_MS}ms on both NICs)')
    apply_netem(src, src_iface, f'delay {DELAY_MS}ms {JITTER_MS}ms')
    apply_netem(dst, dst_iface, f'delay {DELAY_MS}ms {JITTER_MS}ms')
    time.sleep(0.3)
    _, mdev = do_ping(src, dst_ip, PING_COUNT)
    reset_tc(src, src_iface)
    reset_tc(dst, dst_iface)
    if mdev is not None:
        error = mdev - JITTER_MS
        results['jitter'] = dict(configured=JITTER_MS, observed=round(mdev, 2), error=round(error, 2))
        print(f'   observed mdev : {mdev:.2f} ms  (configured {JITTER_MS} ms)')
        print(f'   error         : {error:+.2f} ms')
    else:
        print('   ERROR: ping failed')
        results['jitter'] = None

    effective_loss = round(1 - (1 - LOSS_PCT/100) ** 2, 4) * 100
    print(f'\n [3/4] Loss   (configured: {LOSS_PCT}% per NIC, effective {effective_loss:.1f}% end-to-end, {PING_COUNT_LOSS} packets)')
    apply_netem(src, src_iface, f'loss {LOSS_PCT}%')
    apply_netem(dst, dst_iface, f'loss {LOSS_PCT}%')
    time.sleep(0.3)
    out = src.cmd(f'ping -c {PING_COUNT_LOSS} -i {PING_INTERVAL} -q {dst_ip}')
    reset_tc(src, src_iface)
    reset_tc(dst, dst_iface)
    m = re.search(r'([\d.]+)% packet loss', out)
    if m:
        observed_loss = float(m.group(1))
        error = observed_loss - effective_loss
        results['loss'] = dict(configured=effective_loss, observed=observed_loss, error=round(error, 2))
        print(f'   observed loss : {observed_loss:.1f}%  (effective configured {effective_loss:.1f}%)')
        print(f'   error         : {error:+.1f}%')
    else:
        print('   ERROR: could not parse loss')
        results['loss'] = None

    print(f'\n [4/4] Bandwidth cap (configured: {BW_CAP_KBIT} kbit/s on both NICs, iperf3 {IPERF_SECS}s)')
    apply_tbf(src, src_iface, BW_CAP_KBIT, BW_BURST, BW_LATENCY)
    apply_tbf(dst, dst_iface, BW_CAP_KBIT, BW_BURST, BW_LATENCY)
    time.sleep(0.3)
    observed_bw = do_iperf_tcp(src, dst, dst_ip)
    reset_tc(src, src_iface)
    reset_tc(dst, dst_iface)
    if observed_bw is not None:
        error = observed_bw - 1.0
        results['bandwidth'] = dict(configured=1.0, observed=round(observed_bw, 3), error=round(error, 3))
        print(f'   observed : {observed_bw:.3f} Mbit/s  (configured 1.000 Mbit/s)')
        print(f'   error    : {error:+.3f} Mbit/s')
    else:
        print('   ERROR: iperf3 failed')
        results['bandwidth'] = None

    net.stop()
    cleanup()
    return results

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('topo', choices=['t1', 't2', 't3'])
    parser.add_argument('--baseline', type=float, default=None)
    args = parser.parse_args()

    if os.geteuid() != 0:
        print('ERROR: must be run as root (sudo)')
        sys.exit(1)

    setLogLevel('warning')

    baseline = args.baseline
    if baseline is None:
        try:
            baseline = float(input('Enter baseline RTT (ms): ').strip())
        except ValueError:
            baseline = 0.0

    print('=' * 62)
    print(f' tc/netem fidelity : {args.topo.upper()}')
    print(f' Baseline RTT      : {baseline:.2f} ms')
    print('=' * 62)

    results = run_fidelity(args.topo, baseline)

    print()
    print('=' * 62)
    print(f' SUMMARY — {args.topo.upper()}')
    print(f'  {"Condition":<18} {"Configured":>12} {"Observed":>12} {"Error":>10}')
    print(f'  {"-"*18} {"-"*12} {"-"*12} {"-"*10}')
    for key, unit, label in [
        ('delay',     'ms',     'Delay'),
        ('jitter',    'ms',     'Jitter'),
        ('loss',      '%',      'Loss (effective)'),
        ('bandwidth', 'Mbit/s', 'Bandwidth cap'),
    ]:
        r = results.get(key)
        if r:
            print(f'  {label:<18} {str(r["configured"])+unit:>12} '
                  f'{str(r["observed"])+unit:>12} {str(r["error"]):>10}')
        else:
            print(f'  {label:<18} {"—":>12} {"—":>12} {"ERR":>10}')
    print('=' * 62)

if __name__ == '__main__':
    main()
