#!/usr/bin/env python3
"""
eveng_topo_bench.py — Startup time + RAM/CPU idle benchmark for EVE-NG.

Runs from LOCAL machine. Measures vmware-vmx process RAM + CPU
using the same methodology as ram-cpu.sh.

Measures over RUNS runs:
  - start-all API call duration (wall clock)
  - RAM (MB) and CPU (%) of vmware-vmx process during 30s idle window

Usage (run on LOCAL machine, no sudo needed):
  python3 eveng_topo_bench.py t1 [--runs 7]
  python3 eveng_topo_bench.py t2 [--runs 7]
  python3 eveng_topo_bench.py t3 [--runs 7]
"""

import sys, os, time, argparse, statistics, json
import urllib.request, subprocess

SAMPLE_DURATION = 30
SAMPLE_INTERVAL = 1
GAP_SECONDS     = 5

EVE_HOST = '172.16.240.128'
EVE_API  = f'http://{EVE_HOST}'
EVE_USER = 'admin'
EVE_PASS = 'eve'
VMX_PATH = os.path.expanduser('~/Study/Thesis/eve-ng/st/Ubuntu 64-bit.vmx')

CLK_TCK  = os.sysconf('SC_CLK_TCK')
PAGE_SIZE = os.sysconf('SC_PAGE_SIZE')
CORES    = os.cpu_count()

# ── EVE-NG API ────────────────────────────────────────────────────────────────
_cookie = ''

def api_login():
    global _cookie
    data = json.dumps({'username': EVE_USER, 'password': EVE_PASS,
                       'html5': '-1'}).encode()
    req = urllib.request.Request(
        f'{EVE_API}/api/auth/login', data=data,
        headers={'Content-Type': 'application/json'}, method='POST')
    with urllib.request.urlopen(req, timeout=10) as r:
        _cookie = r.getheader('Set-Cookie', '').split(';')[0]

def api_timed(path):
    req = urllib.request.Request(
        f'{EVE_API}{path}', headers={'Cookie': _cookie})
    t0 = time.time()
    with urllib.request.urlopen(req, timeout=60) as r:
        r.read()
    return time.time() - t0

# ── VMware-vmx process measurement ───────────────────────────────────────────
def get_vmx_pids():
    vmx_real = os.path.realpath(VMX_PATH)
    pids = []
    for entry in os.listdir('/proc'):
        if not entry.isdigit():
            continue
        try:
            comm = open(f'/proc/{entry}/comm').read().strip()
            if comm != 'vmware-vmx':
                continue
            cmd = open(f'/proc/{entry}/cmdline').read().replace('\x00', ' ')
            if vmx_real in cmd:
                pids.append(entry)
        except (PermissionError, FileNotFoundError):
            pass
    return pids

def read_vmx_stats(pids):
    total_cpu = 0
    total_rss = 0
    for pid in pids:
        try:
            stat = open(f'/proc/{pid}/stat').read().split(')')[-1].split()
            total_cpu += int(stat[11]) + int(stat[12])
        except (FileNotFoundError, IndexError, ValueError):
            pass
        try:
            total_rss += int(open(f'/proc/{pid}/statm').read().split()[1])
        except (FileNotFoundError, IndexError, ValueError):
            pass
    return total_cpu, total_rss

def sample_vmx(duration):
    pids = get_vmx_pids()
    if not pids:
        return None, None

    ram_pages_sum = 0
    ram_samples = 0
    cpu_start, _ = read_vmx_stats(pids)
    t_start = time.monotonic_ns()

    end = time.time() + duration
    while time.time() < end:
        _, rss = read_vmx_stats(pids)
        ram_pages_sum += rss
        ram_samples += 1
        time.sleep(SAMPLE_INTERVAL)

    cpu_end, _ = read_vmx_stats(pids)
    t_end = time.monotonic_ns()

    elapsed_ns = t_end - t_start
    cpu_ticks = cpu_end - cpu_start

    ram_mb = (ram_pages_sum / ram_samples) * PAGE_SIZE / 1024 / 1024 if ram_samples else 0
    cpu_pct = (cpu_ticks / CLK_TCK) / (elapsed_ns / 1e9) * 100 / CORES if elapsed_ns > 0 else 0

    return ram_mb, cpu_pct

def avg(lst):
    return sum(lst) / len(lst) if lst else 0.0

def fmt(v, d=2):
    return f'{v:.{d}f}' if v is not None else 'ERR'

# ── Main ──────────────────────────────────────────────────────────────────────
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('topo', choices=['t1', 't2', 't3'])
    parser.add_argument('--runs', type=int, default=7)
    args = parser.parse_args()

    topo = args.topo
    runs = args.runs
    lab  = f'{topo}.unl'

    if not get_vmx_pids():
        print(f'ERROR: vmware-vmx not found for {VMX_PATH}')
        sys.exit(1)

    print('=' * 62)
    print(f' Topo bench : {topo.upper()}  ({runs} runs)')
    print('=' * 62)

    start_times = []
    all_rams    = []
    all_cpus    = []

    for i in range(1, runs + 1):
        print(f'\n--- Run {i}/{runs} ---')

        api_login()

        # Stop all nodes
        print('  Stopping nodes...', end=' ', flush=True)
        try:
            api_timed(f'/api/labs/{lab}/nodes/stop')
            print('done')
        except Exception as e:
            print(f'FAILED: {e}'); continue
        time.sleep(3)

        # Start all nodes — timed
        print('  Starting nodes...', end=' ', flush=True)
        try:
            elapsed = api_timed(f'/api/labs/{lab}/nodes/start')
            start_times.append(elapsed)
            print(f'{fmt(elapsed, 3)} s')
        except Exception as e:
            print(f'FAILED: {e}'); continue

        # Sample vmware-vmx RAM/CPU at idle
        print(f'  Sampling RAM/CPU ({SAMPLE_DURATION}s)...', end=' ', flush=True)
        r_avg, c_avg = sample_vmx(SAMPLE_DURATION)
        all_rams.append(r_avg)
        all_cpus.append(c_avg)
        print(f'RAM={fmt(r_avg)} MB  CPU={fmt(c_avg)}%')

        if i < runs:
            time.sleep(GAP_SECONDS)

    print()
    print('=' * 62)
    print(f' AVERAGES over {runs} runs — {topo.upper()}')
    print('=' * 62)
    if start_times:
        print(f'  start-all API avg   : {fmt(avg(start_times), 3)} s')
        print(f'  start-all API median: {fmt(statistics.median(start_times), 3)} s')
        print(f'  start-all API min   : {fmt(min(start_times), 3)} s')
        print(f'  start-all API max   : {fmt(max(start_times), 3)} s')
    if all_rams:
        print(f'  RAM avg             : {fmt(avg(all_rams))} MB')
        print(f'  RAM median          : {fmt(statistics.median(all_rams))} MB')
        print(f'  RAM min             : {fmt(min(all_rams))} MB')
        print(f'  RAM max             : {fmt(max(all_rams))} MB')
        print(f'  CPU avg             : {fmt(avg(all_cpus))} %')
        print(f'  CPU median          : {fmt(statistics.median(all_cpus))} %')
        print(f'  CPU min             : {fmt(min(all_cpus))} %')
        print(f'  CPU max             : {fmt(max(all_cpus))} %')
    print('=' * 62)

if __name__ == '__main__':
    main()
