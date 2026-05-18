#!/usr/bin/env python3
"""
mn_traffic_bench.py — Traffic benchmark for Mininet topology tiers.

Measures over RUNS runs each:
  - ping RTT (mean ms, mdev ms)
  - TCP throughput (Mbit/s, sender side)
  - UDP jitter (ms) and loss (%)
  - RAM + CPU during TCP throughput run

Usage:
  sudo python3 mn_traffic_bench.py t1   [--runs 7]
  sudo python3 mn_traffic_bench.py t2   [--runs 7]
  sudo python3 mn_traffic_bench.py t3   [--runs 7]

Topology configs embedded below — edit if your IPs change.
"""

import sys, os, time, re, argparse, subprocess, signal
from mininet.net import Mininet
from mininet.node import Host, OVSBridge
from mininet.link import TCLink
from mininet.log import setLogLevel

# ── Topology registry ─────────────────────────────────────────────────────────
# src_host, dst_host, dst_ip, src_iface
TOPO_CFG = {
    't1': dict(src='h1', dst='h2', dst_ip='192.168.10.11', src_iface='h1-eth0'),
    't2': dict(src='h1', dst='h4', dst_ip='192.168.20.11', src_iface='h1-eth0'),
    't3': dict(src='h1', dst='h14', dst_ip='10.0.4.11',    src_iface='h1-eth0'),
}

PING_COUNT     = 20    # packets per ping run (at -i 0.2 = 4s per ping)
PING_INTERVAL  = 0.2   # seconds between ping packets
IPERF_DURATION = 30    # seconds
UDP_BITRATE    = '100M'
BW_CAP_KBIT    = 1000  # 1 Mbit/s cap for capped TCP test
BW_BURST       = '32kbit'
BW_LATENCY     = '400ms'
CPU_SETTLE     = 5     # seconds after iperf starts before sampling RAM/CPU
CPU_SAMPLE     = 5     # seconds for CPU sample window

# ── Import the right topology builder ─────────────────────────────────────────
def import_topo(name):
    import importlib.util
    script = os.path.join(os.path.dirname(__file__), f'mn_{name}.py')
    spec = importlib.util.spec_from_file_location('topo_mod', script)
    mod  = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

# ── Mininet RSS measurement (same approach as mn_benchmark.sh) ────────────────
def mininet_rss_mb(script_name):
    total = 0
    for pid_dir in os.listdir('/proc'):
        if not pid_dir.isdigit():
            continue
        try:
            cmdline = open(f'/proc/{pid_dir}/cmdline').read().replace('\x00', ' ')
            if not any(k in cmdline for k in (script_name, 'ovsdb-server', 'ovs-vswitchd', 'mnexec')):
                continue
            for line in open(f'/proc/{pid_dir}/status'):
                if line.startswith('VmRSS:'):
                    total += int(line.split()[1])
                    break
        except (PermissionError, FileNotFoundError):
            pass
    return total / 1024.0

def read_cpu_stat():
    for line in open('/proc/stat'):
        if line.startswith('cpu '):
            vals = list(map(int, line.split()[1:]))
            idle  = vals[3] + vals[4]
            total = sum(vals)
            return idle, total
    return 0, 0

def cpu_pct(secs):
    i1, t1 = read_cpu_stat()
    time.sleep(secs)
    i2, t2 = read_cpu_stat()
    dt = t2 - t1
    return 0.0 if dt <= 0 else (1 - (i2 - i1) / dt) * 100

# ── Parse ping output ─────────────────────────────────────────────────────────
def parse_ping(output):
    # rtt min/avg/max/mdev = 0.123/0.456/0.789/0.012 ms
    m = re.search(r'rtt .* = [\d.]+/([\d.]+)/[\d.]+/([\d.]+) ms', output)
    if m:
        return float(m.group(1)), float(m.group(2))  # avg, mdev
    return None, None

# ── Parse iperf3 TCP sender line ──────────────────────────────────────────────
def parse_iperf_tcp(output):
    # Look for sender summary line: "... X Mbits/sec ... sender"
    for line in reversed(output.splitlines()):
        if 'sender' in line:
            m = re.search(r'([\d.]+)\s+Mbits/sec', line)
            if m:
                return float(m.group(1))
    return None

# ── Parse iperf3 UDP server output ────────────────────────────────────────────
def parse_iperf_udp(output):
    # Server summary line has jitter and loss
    # e.g.: "... 0.123 ms  5/1000 (0.5%)"
    jitter, loss = None, None
    for line in reversed(output.splitlines()):
        if 'receiver' in line or ('ms' in line and '%' in line):
            mj = re.search(r'([\d.]+)\s+ms', line)
            ml = re.search(r'\(([\d.]+)%\)', line)
            if mj and ml:
                jitter = float(mj.group(1))
                loss   = float(ml.group(1))
                break
    return jitter, loss

# ── Build the network (calls the topology module's build internals) ───────────
def make_net(topo_name):
    """
    Reconstruct the network from the topology script without running CLI/bench.
    We duplicate the build logic here so we have direct Python handles to nodes.
    """
    if topo_name == 't1':
        from mininet.net import Mininet
        net = Mininet(host=Host, link=TCLink, controller=None)
        h1 = net.addHost('h1', ip=None)
        h2 = net.addHost('h2', ip=None)
        net.addLink(h1, h2)
        net.start()
        h1.cmd('ip addr flush dev h1-eth0')
        h1.cmd('ip addr add 192.168.10.10/24 dev h1-eth0')
        h1.cmd('ip link set h1-eth0 up')
        h2.cmd('ip addr flush dev h2-eth0')
        h2.cmd('ip addr add 192.168.10.11/24 dev h2-eth0')
        h2.cmd('ip link set h2-eth0 up')
        return net

    elif topo_name == 't2':
        from mininet.net import Mininet

        class LinuxRouter(Host):
            def config(self, **p):
                super().config(**p)
                self.cmd('sysctl -w net.ipv4.ip_forward=1')
            def terminate(self):
                self.cmd('sysctl -w net.ipv4.ip_forward=0')
                super().terminate()

        net = Mininet(host=Host, switch=OVSBridge, link=TCLink, controller=None)
        r1 = net.addHost('r1', cls=LinuxRouter, ip=None)
        s1 = net.addSwitch('s1')
        s2 = net.addSwitch('s2')
        h1 = net.addHost('h1', ip=None)
        h2 = net.addHost('h2', ip=None)
        h3 = net.addHost('h3', ip=None)
        h4 = net.addHost('h4', ip=None)
        net.addLink(h1, s1); net.addLink(h2, s1); net.addLink(r1, s1)
        net.addLink(h3, s2); net.addLink(h4, s2); net.addLink(r1, s2)
        net.start()
        r1.cmd('ip addr flush dev r1-eth0'); r1.cmd('ip addr add 192.168.10.1/24 dev r1-eth0'); r1.cmd('ip link set r1-eth0 up')
        r1.cmd('ip addr flush dev r1-eth1'); r1.cmd('ip addr add 192.168.20.1/24 dev r1-eth1'); r1.cmd('ip link set r1-eth1 up')
        h1.cmd('ip addr flush dev h1-eth0'); h1.cmd('ip addr add 192.168.10.10/24 dev h1-eth0'); h1.cmd('ip link set h1-eth0 up'); h1.cmd('ip route add default via 192.168.10.1')
        h2.cmd('ip addr flush dev h2-eth0'); h2.cmd('ip addr add 192.168.10.11/24 dev h2-eth0'); h2.cmd('ip link set h2-eth0 up'); h2.cmd('ip route add default via 192.168.10.1')
        h3.cmd('ip addr flush dev h3-eth0'); h3.cmd('ip addr add 192.168.20.10/24 dev h3-eth0'); h3.cmd('ip link set h3-eth0 up'); h3.cmd('ip route add default via 192.168.20.1')
        h4.cmd('ip addr flush dev h4-eth0'); h4.cmd('ip addr add 192.168.20.11/24 dev h4-eth0'); h4.cmd('ip link set h4-eth0 up'); h4.cmd('ip route add default via 192.168.20.1')
        return net

    elif topo_name == 't3':
        return _build_t3()

    raise ValueError(f'Unknown topo: {topo_name}')

def _build_t3():
    from mininet.net import Mininet

    class LinuxRouter(Host):
        def config(self, **p):
            super().config(**p)
            self.cmd('sysctl -w net.ipv4.ip_forward=1')
        def terminate(self):
            self.cmd('sysctl -w net.ipv4.ip_forward=0')
            super().terminate()

    def cfg(node, idx, cidr):
        intf = '%s-eth%d' % (node.name, idx)
        node.cmd('ip addr flush dev %s' % intf)
        node.cmd('ip addr add %s dev %s' % (cidr, intf))
        node.cmd('ip link set %s up' % intf)

    def rt(node, dst, via):
        node.cmd('ip route add %s via %s' % (dst, via))

    net = Mininet(host=Host, switch=OVSBridge, link=TCLink, controller=None)

    r7  = net.addHost('r7',  cls=LinuxRouter, ip=None)
    r8  = net.addHost('r8',  cls=LinuxRouter, ip=None)
    r9  = net.addHost('r9',  cls=LinuxRouter, ip=None)
    r10 = net.addHost('r10', cls=LinuxRouter, ip=None)
    r11 = net.addHost('r11', cls=LinuxRouter, ip=None)
    r12 = net.addHost('r12', cls=LinuxRouter, ip=None)
    r13 = net.addHost('r13', cls=LinuxRouter, ip=None)
    r14 = net.addHost('r14', cls=LinuxRouter, ip=None)
    sw0 = net.addSwitch('sw0'); sw1 = net.addSwitch('sw1'); sw2 = net.addSwitch('sw2')
    sw3 = net.addSwitch('sw3'); sw4 = net.addSwitch('sw4'); sw5 = net.addSwitch('sw5')
    sw6 = net.addSwitch('sw6')
    hosts = {}
    for i in range(1, 25):
        hosts[f'h{i}'] = net.addHost(f'h{i}', ip=None)
    h = hosts

    net.addLink(r7, sw0); net.addLink(sw0, h['h1']); net.addLink(sw0, h['h2'])
    net.addLink(sw0, h['h3']); net.addLink(sw0, h['h4'])
    net.addLink(r7, r8); net.addLink(r7, r11)
    net.addLink(r8, r9); net.addLink(r8, sw6)
    net.addLink(r9, r10); net.addLink(r9, sw6)
    net.addLink(sw1, sw6); net.addLink(sw1, h['h5']); net.addLink(sw1, h['h6'])
    net.addLink(sw1, h['h7']); net.addLink(sw1, h['h8'])
    net.addLink(r10, r14); net.addLink(r10, sw2)
    net.addLink(sw2, h['h9']); net.addLink(sw2, h['h10'])
    net.addLink(sw2, h['h11']); net.addLink(sw2, h['h12'])
    net.addLink(r14, r13); net.addLink(r14, sw5)
    net.addLink(sw5, h['h13']); net.addLink(sw5, h['h14'])
    net.addLink(sw5, h['h15']); net.addLink(sw5, h['h16'])
    net.addLink(r13, r12); net.addLink(r13, sw6)
    net.addLink(sw4, sw6); net.addLink(sw4, h['h17']); net.addLink(sw4, h['h18'])
    net.addLink(sw4, h['h19']); net.addLink(sw4, h['h20'])
    net.addLink(r12, r11); net.addLink(r12, sw6)
    net.addLink(r11, sw3)
    net.addLink(sw3, h['h21']); net.addLink(sw3, h['h22'])
    net.addLink(sw3, h['h23']); net.addLink(sw3, h['h24'])

    net.start()

    cfg(r7,  0, '10.0.1.1/24');   cfg(r7,  1, '10.255.78.1/30');  cfg(r7,  2, '10.255.117.2/30')
    rt(r7, '10.0.2.0/24', '10.255.78.2');  rt(r7, '10.0.3.0/24', '10.255.78.2')
    rt(r7, '10.0.4.0/24', '10.255.78.2');  rt(r7, '10.0.5.0/24', '10.255.78.2')
    rt(r7, '10.0.6.0/24', '10.255.117.1')

    cfg(r8,  0, '10.255.78.2/30');  cfg(r8,  1, '10.255.89.1/30');  cfg(r8,  2, '10.255.100.8/24')
    rt(r8, '10.0.1.0/24', '10.255.78.1');  rt(r8, '10.0.2.0/24', '10.255.89.2')
    rt(r8, '10.0.3.0/24', '10.255.89.2');  rt(r8, '10.0.4.0/24', '10.255.100.13')
    rt(r8, '10.0.5.0/24', '10.255.100.13'); rt(r8, '10.0.6.0/24', '10.255.100.12')

    cfg(r9,  0, '10.255.89.2/30');  cfg(r9,  1, '10.255.90.1/30');  cfg(r9,  2, '10.255.100.9/24')
    r9.cmd('ip addr add 10.0.2.1/24 dev r9-eth2')
    rt(r9, '10.0.1.0/24', '10.255.89.1');  rt(r9, '10.0.3.0/24', '10.255.90.2')
    rt(r9, '10.0.4.0/24', '10.255.90.2');  rt(r9, '10.0.5.0/24', '10.255.100.13')
    rt(r9, '10.0.6.0/24', '10.255.100.12')

    cfg(r10, 0, '10.255.90.2/30');  cfg(r10, 1, '10.255.104.1/30'); cfg(r10, 2, '10.0.3.1/24')
    rt(r10, '10.0.1.0/24', '10.255.90.1'); rt(r10, '10.0.2.0/24', '10.255.90.1')
    rt(r10, '10.0.4.0/24', '10.255.104.2'); rt(r10, '10.0.5.0/24', '10.255.90.1')
    rt(r10, '10.0.6.0/24', '10.255.90.1')

    cfg(r14, 0, '10.255.104.2/30'); cfg(r14, 1, '10.255.134.1/30'); cfg(r14, 2, '10.0.4.1/24')
    rt(r14, '10.0.1.0/24', '10.255.134.2'); rt(r14, '10.0.2.0/24', '10.255.104.1')
    rt(r14, '10.0.3.0/24', '10.255.104.1'); rt(r14, '10.0.5.0/24', '10.255.134.2')
    rt(r14, '10.0.6.0/24', '10.255.134.2')

    cfg(r13, 0, '10.255.134.2/30'); cfg(r13, 1, '10.255.132.1/30'); cfg(r13, 2, '10.255.100.13/24')
    r13.cmd('ip addr add 10.0.5.1/24 dev r13-eth2')
    rt(r13, '10.0.1.0/24', '10.255.100.8'); rt(r13, '10.0.2.0/24', '10.255.100.9')
    rt(r13, '10.0.3.0/24', '10.255.100.9'); rt(r13, '10.0.4.0/24', '10.255.134.1')
    rt(r13, '10.0.6.0/24', '10.255.100.12')

    cfg(r12, 0, '10.255.132.2/30'); cfg(r12, 1, '10.255.121.1/30'); cfg(r12, 2, '10.255.100.12/24')
    rt(r12, '10.0.1.0/24', '10.255.100.8'); rt(r12, '10.0.2.0/24', '10.255.100.9')
    rt(r12, '10.0.3.0/24', '10.255.100.9'); rt(r12, '10.0.4.0/24', '10.255.100.13')
    rt(r12, '10.0.5.0/24', '10.255.100.13'); rt(r12, '10.0.6.0/24', '10.255.121.2')

    cfg(r11, 0, '10.255.117.1/30'); cfg(r11, 1, '10.255.121.2/30'); cfg(r11, 2, '10.0.6.1/24')
    rt(r11, '10.0.1.0/24', '10.255.117.2'); rt(r11, '10.0.2.0/24', '10.255.121.1')
    rt(r11, '10.0.3.0/24', '10.255.121.1'); rt(r11, '10.0.4.0/24', '10.255.121.1')
    rt(r11, '10.0.5.0/24', '10.255.121.1')

    ips_1 = ['10.0.1.10','10.0.1.11','10.0.1.12','10.0.1.13']
    ips_2 = ['10.0.2.10','10.0.2.11','10.0.2.12','10.0.2.13']
    ips_3 = ['10.0.3.10','10.0.3.11','10.0.3.12','10.0.3.13']
    ips_4 = ['10.0.4.10','10.0.4.11','10.0.4.12','10.0.4.13']
    ips_5 = ['10.0.5.10','10.0.5.11','10.0.5.12','10.0.5.13']
    ips_6 = ['10.0.6.10','10.0.6.11','10.0.6.12','10.0.6.13']

    for i, ip in enumerate(ips_1, 1):
        n = h[f'h{i}']; cfg(n, 0, ip+'/24'); n.cmd('ip route add default via 10.0.1.1')
    for i, ip in enumerate(ips_2, 5):
        n = h[f'h{i}']; cfg(n, 0, ip+'/24'); n.cmd('ip route add default via 10.0.2.1')
    for i, ip in enumerate(ips_3, 9):
        n = h[f'h{i}']; cfg(n, 0, ip+'/24'); n.cmd('ip route add default via 10.0.3.1')
    for i, ip in enumerate(ips_4, 13):
        n = h[f'h{i}']; cfg(n, 0, ip+'/24'); n.cmd('ip route add default via 10.0.4.1')
    for i, ip in enumerate(ips_5, 17):
        n = h[f'h{i}']; cfg(n, 0, ip+'/24'); n.cmd('ip route add default via 10.0.5.1')
    for i, ip in enumerate(ips_6, 21):
        n = h[f'h{i}']; cfg(n, 0, ip+'/24'); n.cmd('ip route add default via 10.0.6.1')

    return net

# ── Individual measurements ───────────────────────────────────────────────────
def measure_ping(net, cfg):
    src = net.get(cfg['src'])
    out = src.cmd(f"ping -c {PING_COUNT} -i {PING_INTERVAL} -q {cfg['dst_ip']}")
    return parse_ping(out)

def measure_tcp(net, cfg, script_name):
    dst = net.get(cfg['dst'])
    src = net.get(cfg['src'])

    dst.cmd('pkill iperf3 2>/dev/null; sleep 0.3')
    dst.sendCmd(f'iperf3 -s -1 -p 5201')
    time.sleep(0.5)

    # Start CPU baseline before iperf
    cpu_baseline_i, cpu_baseline_t = read_cpu_stat()

    src.sendCmd(f'iperf3 -c {cfg["dst_ip"]} -p 5201 -t {IPERF_DURATION} -f m')

    # Sample RAM/CPU midway through
    time.sleep(CPU_SETTLE)
    ram_mb = mininet_rss_mb(script_name)
    i1, t1 = read_cpu_stat()
    time.sleep(CPU_SAMPLE)
    i2, t2 = read_cpu_stat()
    cpu = 0.0 if (t2-t1) <= 0 else (1 - (i2-i1)/(t2-t1)) * 100

    tcp_out = src.waitOutput()
    dst.waitOutput()

    throughput = parse_iperf_tcp(tcp_out)
    return throughput, ram_mb, cpu

def measure_udp(net, cfg):
    dst = net.get(cfg['dst'])
    src = net.get(cfg['src'])

    dst.cmd('pkill iperf3 2>/dev/null; sleep 0.3')
    dst.sendCmd(f'iperf3 -s -1 -p 5202')
    time.sleep(0.5)
    src.sendCmd(f'iperf3 -c {cfg["dst_ip"]} -p 5202 -u -b {UDP_BITRATE} -t {IPERF_DURATION} -f m')

    # Wait for both to finish — server output has the jitter/loss stats
    src.waitOutput()
    udp_out = dst.waitOutput()

    return parse_iperf_udp(udp_out)

def measure_tcp_capped(net, cfg, script_name):
    """TCP throughput + RAM/CPU with a 1 Mbit/s tbf cap on src outbound NIC."""
    dst = net.get(cfg['dst'])
    src = net.get(cfg['src'])
    iface = cfg['src_iface']

    # Apply bandwidth cap
    src.cmd(f'tc qdisc del dev {iface} root 2>/dev/null || true')
    src.cmd(f'tc qdisc add dev {iface} root tbf rate {BW_CAP_KBIT}kbit burst {BW_BURST} latency {BW_LATENCY}')
    time.sleep(0.3)

    dst.cmd('pkill iperf3 2>/dev/null; sleep 0.3')
    dst.sendCmd(f'iperf3 -s -1 -p 5204')
    time.sleep(0.5)

    src.sendCmd(f'iperf3 -c {cfg["dst_ip"]} -p 5204 -t {IPERF_DURATION} -f m')

    # Sample RAM/CPU midway
    time.sleep(CPU_SETTLE)
    ram_mb = mininet_rss_mb(script_name)
    i1, t1 = read_cpu_stat()
    time.sleep(CPU_SAMPLE)
    i2, t2 = read_cpu_stat()
    cpu = 0.0 if (t2 - t1) <= 0 else (1 - (i2 - i1) / (t2 - t1)) * 100

    tcp_out = src.waitOutput()
    dst.waitOutput()

    # Remove cap
    src.cmd(f'tc qdisc del dev {iface} root 2>/dev/null || true')

    throughput = parse_iperf_tcp(tcp_out)
    return throughput, ram_mb, cpu

# ── Cleanup ───────────────────────────────────────────────────────────────────
def cleanup():
    os.system('mn -c > /dev/null 2>&1')
    time.sleep(1)

# ── Average helper ────────────────────────────────────────────────────────────
def avg(lst):
    valid = [x for x in lst if x is not None]
    return sum(valid) / len(valid) if valid else None

def fmt(val, decimals=2):
    return f'{val:.{decimals}f}' if val is not None else 'ERR'

# ── Main ──────────────────────────────────────────────────────────────────────
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('topo', choices=['t1','t2','t3'])
    parser.add_argument('--runs', type=int, default=7)
    args = parser.parse_args()

    if os.geteuid() != 0:
        print('ERROR: must be run as root (sudo)')
        sys.exit(1)

    topo_name   = args.topo
    runs        = args.runs
    cfg         = TOPO_CFG[topo_name]
    script_name = f'{topo_name}.py'

    setLogLevel('warning')  # suppress Mininet chatter

    print('=' * 62)
    print(f' Traffic benchmark : {topo_name.upper()}  ({runs} runs)')
    print(f' src={cfg["src"]}  dst={cfg["dst"]} ({cfg["dst_ip"]})')
    print('=' * 62)

    ping_avgs, ping_mdevs = [], []
    tcp_mbits, tcp_rams, tcp_cpus = [], [], []
    udp_jitters, udp_losses = [], []
    cap_mbits, cap_rams, cap_cpus = [], [], []

    for i in range(1, runs + 1):
        print(f'\n--- Run {i}/{runs} ---')
        cleanup()

        try:
            net = make_net(topo_name)
        except Exception as e:
            print(f'  Network build failed: {e}')
            cleanup()
            continue

        time.sleep(1)  # settle

        # 1. ping RTT
        print('  [1/4] ping RTT ...', end=' ', flush=True)
        avg_rtt, mdev = measure_ping(net, cfg)
        ping_avgs.append(avg_rtt)
        ping_mdevs.append(mdev)
        print(f'avg={fmt(avg_rtt)} ms  mdev={fmt(mdev)} ms')

        # 2. TCP throughput + RAM/CPU (uncapped)
        print(f'  [2/4] TCP throughput uncapped ({IPERF_DURATION}s) + RAM/CPU ...', end=' ', flush=True)
        throughput, ram, cpu_pct_val = measure_tcp(net, cfg, script_name)
        tcp_mbits.append(throughput)
        tcp_rams.append(ram)
        tcp_cpus.append(cpu_pct_val)
        print(f'{fmt(throughput)} Mbit/s  RAM={fmt(ram)} MB  CPU={fmt(cpu_pct_val)}%')

        # 3. UDP jitter + loss
        print(f'  [3/4] UDP jitter/loss ({IPERF_DURATION}s) ...', end=' ', flush=True)
        jitter, loss = measure_udp(net, cfg)
        udp_jitters.append(jitter)
        udp_losses.append(loss)
        print(f'jitter={fmt(jitter)} ms  loss={fmt(loss)}%')

        # 4. TCP capped at 1 Mbit/s + RAM/CPU
        print(f'  [4/4] TCP capped 1 Mbit/s ({IPERF_DURATION}s) + RAM/CPU ...', end=' ', flush=True)
        cap_tp, cap_ram, cap_cpu = measure_tcp_capped(net, cfg, script_name)
        cap_mbits.append(cap_tp)
        cap_rams.append(cap_ram)
        cap_cpus.append(cap_cpu)
        print(f'{fmt(cap_tp)} Mbit/s  RAM={fmt(cap_ram)} MB  CPU={fmt(cap_cpu)}%')

        net.stop()
        time.sleep(2)

    # ── Summary ───────────────────────────────────────────────────────────────
    print()
    print('=' * 62)
    print(f' AVERAGES over {runs} runs — {topo_name.upper()}')
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
    cleanup()

if __name__ == '__main__':
    main()
