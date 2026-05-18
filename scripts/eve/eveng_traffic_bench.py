#!/usr/bin/env python3
"""
eveng_traffic_bench.py — Traffic benchmark for EVE-NG topology tiers.

Runs from LOCAL machine. Measures vmware-vmx process RAM + CPU
using the same methodology as ram-cpu.sh.

Usage (run on LOCAL machine, no sudo needed):
  python3 eveng_traffic_bench.py t1 [--runs 7]
  python3 eveng_traffic_bench.py t2 [--runs 7]
  python3 eveng_traffic_bench.py t3 [--runs 7]
"""
import sys, os, time, re, argparse, subprocess, shlex

TOPO_CFG = {
    't1': dict(src='192.168.10.10', dst='192.168.10.11'),
    't2': dict(src='192.168.10.10', dst='192.168.20.11'),
    't3': dict(src='10.0.1.10',     dst='10.0.4.11'),
}

EVE_HOST       = '172.16.240.128'
VMX_PATH       = os.path.expanduser('~/Study/Thesis/eve-ng/st/Ubuntu 64-bit.vmx')
PING_COUNT     = 20
PING_INTERVAL  = 0.2
IPERF_DURATION = 30
UDP_BITRATE    = '100M'
BW_CAP_KBIT    = 1000
CPU_SETTLE     = 5
CPU_SAMPLE     = 5
GAP_SECONDS    = 5
CLK_TCK        = os.sysconf('SC_CLK_TCK')
PAGE_SIZE      = os.sysconf('SC_PAGE_SIZE')
CORES          = os.cpu_count()

EVE_SSH  = 'sshpass -p eve ssh -o StrictHostKeyChecking=no -o ControlMaster=no -o PubkeyAuthentication=no -o PasswordAuthentication=yes -o ConnectTimeout=10'
NODE_SSH = 'ssh -o StrictHostKeyChecking=no -o ControlMaster=no -o PasswordAuthentication=no -o ConnectTimeout=10'

def node_cmd(host, cmd, timeout=30):
    full = f'{EVE_SSH} root@{EVE_HOST} "{NODE_SSH} root@{host} {shlex.quote(cmd)}"'
    r = subprocess.run(full, shell=True, capture_output=True, text=True, timeout=timeout)
    return r.stdout + r.stderr

def kill_iperf(host):
    node_cmd(host, 'pkill -9 iperf3 2>/dev/null; sleep 0.3', timeout=15)

def start_server(host, port):
    kill_iperf(host)
    node_cmd(host, f'nohup iperf3 -s -p {port} > /tmp/iperf3_{port}.log 2>&1 &', timeout=10)
    time.sleep(1)

def run_client_bg(host, cmd, logfile):
    node_cmd(host, f'nohup {cmd} > {logfile} 2>&1 &', timeout=10)

def wait_and_get_log(host, logfile, wait_secs):
    time.sleep(max(wait_secs, 0))
    for _ in range(30):
        out = node_cmd(host, f'cat {logfile} 2>/dev/null', timeout=10)
        if 'iperf Done' in out or 'sender' in out:
            return out
        time.sleep(2)
    return node_cmd(host, f'cat {logfile} 2>/dev/null', timeout=10)

# ── VMware-vmx process measurement ───────────────────────────────────────────
def get_vmx_pids():
    """Get PIDs of vmware-vmx processes running our VMX file."""
    vmx_real = os.path.realpath(VMX_PATH)
    pids = []
    for entry in os.listdir('/proc'):
        if not entry.isdigit():
            continue
        pid = entry
        try:
            comm = open(f'/proc/{pid}/comm').read().strip()
            if comm != 'vmware-vmx':
                continue
            cmd = open(f'/proc/{pid}/cmdline').read().replace('\x00', ' ')
            if vmx_real in cmd:
                pids.append(pid)
        except (PermissionError, FileNotFoundError):
            pass
    return pids

def read_vmx_stats(pids):
    """Read CPU ticks and RSS pages for given PIDs."""
    total_cpu = 0
    total_rss = 0
    for pid in pids:
        try:
            stat = open(f'/proc/{pid}/stat').read().split(')')[-1].split()
            utime = int(stat[11])
            stime = int(stat[12])
            total_cpu += utime + stime
        except (FileNotFoundError, IndexError, ValueError):
            pass
        try:
            statm = open(f'/proc/{pid}/statm').read().split()
            total_rss += int(statm[1])
        except (FileNotFoundError, IndexError, ValueError):
            pass
    return total_cpu, total_rss

def sample_vmx(duration):
    """Sample vmware-vmx RAM and CPU over duration seconds."""
    pids = get_vmx_pids()
    if not pids:
        return None, None

    ram_pages_sum = 0
    ram_samples = 0
    cpu_ticks_start, _ = read_vmx_stats(pids)
    t_start = time.monotonic_ns()

    end = time.time() + duration
    while time.time() < end:
        _, rss = read_vmx_stats(pids)
        ram_pages_sum += rss
        ram_samples += 1
        time.sleep(1)

    cpu_ticks_end, _ = read_vmx_stats(pids)
    t_end = time.monotonic_ns()

    elapsed_ns = t_end - t_start
    cpu_ticks = cpu_ticks_end - cpu_ticks_start

    ram_mb = (ram_pages_sum / ram_samples) * PAGE_SIZE / 1024 / 1024 if ram_samples else 0
    cpu_pct = (cpu_ticks / CLK_TCK) / (elapsed_ns / 1e9) * 100 / CORES if elapsed_ns > 0 else 0

    return ram_mb, cpu_pct

# ── Parse output ──────────────────────────────────────────────────────────────
def parse_ping(output):
    m = re.search(r'min/avg/max = [\d.]+/([\d.]+)/([\d.]+)', output)
    if m:
        avg_rtt = float(m.group(1))
        rtts = [float(x) for x in re.findall(r'time=([\d.]+)', output)]
        if rtts:
            mean = sum(rtts) / len(rtts)
            mdev = (sum((r - mean) ** 2 for r in rtts) / len(rtts)) ** 0.5
        else:
            mdev = (float(m.group(2)) - avg_rtt) / 4
        return avg_rtt, mdev
    return None, None

def parse_iperf_tcp(output):
    for line in reversed(output.splitlines()):
        if 'sender' in line:
            m = re.search(r'([\d.]+)\s+Mbits/sec', line)
            if m:
                return float(m.group(1))
    return None

def parse_iperf_udp(output):
    jitter, loss = None, None
    for line in reversed(output.splitlines()):
        if 'receiver' in line:
            mj = re.search(r'([\d.]+)\s+ms', line)
            ml = re.search(r'\(([\d.]+)%\)', line)
            if mj and ml:
                jitter = float(mj.group(1))
                loss   = float(ml.group(1))
                break
    return jitter, loss

def avg(lst):
    valid = [x for x in lst if x is not None]
    return sum(valid) / len(valid) if valid else None

def fmt(v, d=2):
    return f'{v:.{d}f}' if v is not None else 'ERR'

# ── Measurements ──────────────────────────────────────────────────────────────
def measure_ping(src, dst):
    out = node_cmd(src, f'ping -c {PING_COUNT} -i {PING_INTERVAL} -q {dst}',
                   timeout=PING_COUNT * 2 + 15)
    return parse_ping(out)

def measure_tcp(src, dst):
    start_server(dst, 5201)
    run_client_bg(src,
        f'iperf3 -c {dst} -p 5201 -t {IPERF_DURATION} -f m',
        '/tmp/iperf3_tcp.log')
    time.sleep(CPU_SETTLE)
    ram_mb, cpu = sample_vmx(CPU_SAMPLE)
    tcp_out = wait_and_get_log(src, '/tmp/iperf3_tcp.log',
                               IPERF_DURATION - CPU_SETTLE - CPU_SAMPLE + 5)
    kill_iperf(dst)
    return parse_iperf_tcp(tcp_out), ram_mb, cpu

def measure_udp(src, dst):
    start_server(dst, 5202)
    run_client_bg(src,
        f'iperf3 -c {dst} -p 5202 -u -b {UDP_BITRATE} -t {IPERF_DURATION} -f m',
        '/tmp/iperf3_udp.log')
    udp_out = wait_and_get_log(src, '/tmp/iperf3_udp.log', IPERF_DURATION + 5)
    time.sleep(1)
    srv_out = node_cmd(dst, 'cat /tmp/iperf3_5202.log', timeout=10)
    kill_iperf(dst)
    jitter, loss = parse_iperf_udp(srv_out)
    if jitter is None:
        jitter, loss = parse_iperf_udp(udp_out)
    return jitter, loss

def measure_tcp_capped(src, dst):
    node_cmd(src, 'tc qdisc del dev eth0 root 2>/dev/null', timeout=10)
    node_cmd(src,
        f'tc qdisc add dev eth0 root tbf rate {BW_CAP_KBIT}kbit '
        f'burst 32kbit latency 400ms', timeout=10)
    time.sleep(0.3)
    start_server(dst, 5204)
    run_client_bg(src,
        f'iperf3 -c {dst} -p 5204 -t {IPERF_DURATION} -f m',
        '/tmp/iperf3_cap.log')
    time.sleep(CPU_SETTLE)
    ram_mb, cpu = sample_vmx(CPU_SAMPLE)
    tcp_out = wait_and_get_log(src, '/tmp/iperf3_cap.log',
                               IPERF_DURATION - CPU_SETTLE - CPU_SAMPLE + 5)
    kill_iperf(dst)
    node_cmd(src, 'tc qdisc del dev eth0 root 2>/dev/null', timeout=10)
    return parse_iperf_tcp(tcp_out), ram_mb, cpu

# ── Main ──────────────────────────────────────────────────────────────────────
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('topo', choices=['t1', 't2', 't3'])
    parser.add_argument('--runs', type=int, default=7)
    args = parser.parse_args()

    topo = args.topo
    runs = args.runs
    src  = TOPO_CFG[topo]['src']
    dst  = TOPO_CFG[topo]['dst']

    if not get_vmx_pids():
        print(f'ERROR: vmware-vmx not found for {VMX_PATH}')
        print('Make sure VMware is running with the EVE-NG VM.')
        sys.exit(1)

    # Clear any leftover tc rules
    node_cmd(src, 'tc qdisc del dev eth0 root 2>/dev/null', timeout=10)

    print('=' * 62)
    print(f' Traffic benchmark : {topo.upper()}  ({runs} runs)')
    print(f' src={src}  dst={dst}')
    print('=' * 62)

    ping_avgs, ping_mdevs = [], []
    tcp_mbits, tcp_rams, tcp_cpus = [], [], []
    udp_jitters, udp_losses = [], []
    cap_mbits, cap_rams, cap_cpus = [], [], []

    for i in range(1, runs + 1):
        print(f'\n--- Run {i}/{runs} ---')

        print('  [1/4] ping RTT ...', end=' ', flush=True)
        p_avg, p_mdev = measure_ping(src, dst)
        ping_avgs.append(p_avg); ping_mdevs.append(p_mdev)
        print(f'avg={fmt(p_avg)} ms  mdev={fmt(p_mdev)} ms')

        print(f'  [2/4] TCP uncapped ({IPERF_DURATION}s) + RAM/CPU ...',
              end=' ', flush=True)
        tp, ram, cpu = measure_tcp(src, dst)
        tcp_mbits.append(tp); tcp_rams.append(ram); tcp_cpus.append(cpu)
        print(f'{fmt(tp)} Mbit/s  RAM={fmt(ram)} MB  CPU={fmt(cpu)}%')

        print(f'  [3/4] UDP jitter/loss ({IPERF_DURATION}s) ...',
              end=' ', flush=True)
        jitter, loss = measure_udp(src, dst)
        udp_jitters.append(jitter); udp_losses.append(loss)
        print(f'jitter={fmt(jitter)} ms  loss={fmt(loss)}%')

        print(f'  [4/4] TCP capped 1Mbit/s ({IPERF_DURATION}s) + RAM/CPU ...',
              end=' ', flush=True)
        cap_tp, cap_ram, cap_cpu = measure_tcp_capped(src, dst)
        cap_mbits.append(cap_tp); cap_rams.append(cap_ram); cap_cpus.append(cap_cpu)
        print(f'{fmt(cap_tp)} Mbit/s  RAM={fmt(cap_ram)} MB  CPU={fmt(cap_cpu)}%')

        if i < runs:
            time.sleep(GAP_SECONDS)

    print()
    print('=' * 62)
    print(f' AVERAGES over {runs} runs — {topo.upper()}')
    print('=' * 62)
    print(f'  ping RTT avg             : {fmt(avg(ping_avgs))} ms')
    print(f'  ping RTT mdev            : {fmt(avg(ping_mdevs))} ms')
    print(f'  TCP throughput (uncapped): {fmt(avg(tcp_mbits))} Mbit/s')
    print(f'  RAM under TCP (uncapped) : {fmt(avg(tcp_rams))} MB')
    print(f'  CPU under TCP (uncapped) : {fmt(avg(tcp_cpus))} %')
    print(f'  UDP jitter               : {fmt(avg(udp_jitters))} ms')
    print(f'  UDP loss                 : {fmt(avg(udp_losses))} %')
    print(f'  TCP throughput (1Mbit/s) : {fmt(avg(cap_mbits))} Mbit/s')
    print(f'  RAM under TCP (1Mbit/s)  : {fmt(avg(cap_rams))} MB')
    print(f'  CPU under TCP (1Mbit/s)  : {fmt(avg(cap_cpus))} %')
    print('=' * 62)

if __name__ == '__main__':
    main()
