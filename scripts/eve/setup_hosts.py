"""
setup_hosts_slim.py - Install iperf3+SSH only on src/dst hosts for benchmarking.

Longest-path pairs:
  t1: h1 (192.168.10.10) <-> h2 (192.168.10.11)
  t2: h1 (192.168.10.10) <-> h4 (192.168.20.11)
  t3: h1 (10.0.1.10)     <-> h14 (10.0.4.11)

Before running, start the HTTP server (one time only):
  mkdir -p /tmp/apks && cd /tmp/apks
  wget -q https://dl-cdn.alpinelinux.org/alpine/v3.23/main/x86_64/iperf3-3.19.1-r1.apk
  wget -q https://dl-cdn.alpinelinux.org/alpine/v3.23/main/x86_64/openssh-10.2_p1-r0.apk
  wget -q https://dl-cdn.alpinelinux.org/alpine/v3.23/main/x86_64/openssh-keygen-10.2_p1-r0.apk
  wget -q https://dl-cdn.alpinelinux.org/alpine/v3.23/main/x86_64/openssh-client-common-10.2_p1-r0.apk
  wget -q https://dl-cdn.alpinelinux.org/alpine/v3.23/main/x86_64/openssh-client-default-10.2_p1-r0.apk
  wget -q https://dl-cdn.alpinelinux.org/alpine/v3.23/main/x86_64/openssh-server-common-10.2_p1-r0.apk
  wget -q https://dl-cdn.alpinelinux.org/alpine/v3.23/main/x86_64/openssh-server-10.2_p1-r0.apk
  python3 -m http.server 8081 &

Usage:
  python3 setup_hosts_slim.py t1
  python3 setup_hosts_slim.py t2
  python3 setup_hosts_slim.py t3
  python3 setup_hosts_slim.py all
"""

import socket, struct, time, subprocess, sys

HTTP_PORT = 8081
PACKAGES = [
    "iperf3-3.19.1-r1.apk",
    "openssh-10.2_p1-r0.apk",
    "openssh-keygen-10.2_p1-r0.apk",
    "openssh-client-common-10.2_p1-r0.apk",
    "openssh-client-default-10.2_p1-r0.apk",
    "openssh-server-common-10.2_p1-r0.apk",
    "openssh-server-10.2_p1-r0.apk",
]

# Only src and dst hosts per topology
# (bridge, gw_ip, subnet, host_ip, vnc_port, label)
TOPOLOGIES = {
    "t1": [
        ("vnet0_1", "192.168.10.254", "192.168.10.254/24", "192.168.10.10", 32769, "h1-src"),
        ("vnet0_1", "192.168.10.254", "192.168.10.254/24", "192.168.10.11", 32770, "h2-dst"),
    ],
    "t2": [
        ("vnet0_1", "192.168.10.254", "192.168.10.254/24", "192.168.10.10", 32770, "h1-src"),
        ("vnet0_2", "192.168.20.254", "192.168.20.254/24", "192.168.20.11", 32773, "h4-dst"),
    ],
    "t3": [
        ("vnet0_1", "10.0.1.254", "10.0.1.254/24", "10.0.1.10", 32777, "h1-src"),
        ("vnet0_4", "10.0.4.254", "10.0.4.254/24", "10.0.4.11", 32790, "h14-dst"),
    ],
}

# ── VNC key sender with proper shift handling ─────────────────────────────────
SHIFT_CHARS = {
    '!':'1','@':'2','#':'3','$':'4','%':'5',
    '^':'6','&':'7','*':'8','(':'9',')':'0',
    '_':'-','+':'=','{':'[','}':']','|':'\\',
    ':':';','"':"'",'<':',','>':'.','?':'/',
    '~':'`',
}
KEY_SHIFT  = 0xffe1
KEY_RETURN = 0xff0d

def send_vnc_keys(port, commands, inter_cmd_delay=0.5):
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.connect(('127.0.0.1', port))
    s.settimeout(15)
    s.recv(12)
    s.send(b'RFB 003.003\n')
    sec = struct.unpack('>I', s.recv(4))[0]
    if sec == 2:
        s.recv(16); s.send(b'\x00' * 16); s.recv(4)
    s.send(b'\x01')
    time.sleep(0.5)
    try: s.recv(4096)
    except: pass

    def send_key(keysym, down):
        s.send(struct.pack('>BBHi', 4, 1 if down else 0, 0, keysym))

    def type_char(c):
        if c == '\n':
            send_key(KEY_RETURN, True);  time.sleep(0.02)
            send_key(KEY_RETURN, False); time.sleep(0.05)
        elif c in SHIFT_CHARS:
            base = ord(SHIFT_CHARS[c])
            send_key(KEY_SHIFT, True);  time.sleep(0.01)
            send_key(base, True);       time.sleep(0.02)
            send_key(base, False);      time.sleep(0.01)
            send_key(KEY_SHIFT, False); time.sleep(0.03)
        else:
            send_key(ord(c), True);  time.sleep(0.02)
            send_key(ord(c), False); time.sleep(0.03)

    time.sleep(1)
    for cmd in commands:
        for c in cmd: type_char(c)
        type_char('\n')
        time.sleep(inter_cmd_delay)
    time.sleep(1)
    s.close()

# ── Bridge IP ─────────────────────────────────────────────────────────────────
def add_bridge_ip(bridge, subnet):
    r = subprocess.run(['ip','addr','show',bridge], capture_output=True, text=True)
    ip = subnet.split('/')[0]
    if ip in r.stdout:
        print(f"  {bridge}: {ip} already set"); return
    subprocess.run(['ip','addr','add',subnet,'dev',bridge], capture_output=True)
    print(f"  {bridge}: added {subnet}")

# ── Install commands ──────────────────────────────────────────────────────────
def make_install_commands(host_ip, gw_ip):
    base = f"http://{gw_ip}:{HTTP_PORT}"
    cmds = [
        f"ip addr add {host_ip}/24 dev eth0 2>/dev/null",
        "ip link set eth0 up",
    ]
    for pkg in PACKAGES:
        cmds.append(f"wget -q {base}/{pkg}")
    cmds += [
        "apk add --force-non-repository --allow-untrusted *.apk",
        "ssh-keygen -A",
        "echo PermitRootLogin yes >> /etc/ssh/sshd_config",
        "/usr/sbin/sshd",
        "echo root:root | chpasswd",
        "lbu commit",
        "echo SETUP_DONE",
    ]
    return cmds

# ── Wait for SSH ──────────────────────────────────────────────────────────────
def wait_for_ssh(host_ip, timeout=120):
    print(f"    Waiting for SSH on {host_ip}...", end='', flush=True)
    start = time.time()
    while time.time() - start < timeout:
        try:
            s = socket.socket(); s.settimeout(3)
            s.connect((host_ip, 22))
            if b'SSH' in s.recv(256): s.close(); print(" up!"); return True
            s.close()
        except: pass
        print('.', end='', flush=True); time.sleep(3)
    print(" TIMEOUT"); return False

# ── Setup SSH key ─────────────────────────────────────────────────────────────
def setup_ssh_key(host_ip):
    subprocess.run(
        ['ssh-keygen','-t','rsa','-N','','-f','/root/.ssh/id_rsa'],
        capture_output=True
    )
    r = subprocess.run(
        ['ssh-copy-id','-o','StrictHostKeyChecking=no',
         '-o','PasswordAuthentication=yes',
         f'root@{host_ip}'],
        input=b'root\n', capture_output=True, timeout=15
    )
    # Verify passwordless
    r2 = subprocess.run(
        ['ssh','-o','StrictHostKeyChecking=no',
         '-o','PasswordAuthentication=no',
         f'root@{host_ip}','echo OK'],
        capture_output=True, text=True, timeout=10
    )
    return 'OK' in r2.stdout

# ── Process one host ──────────────────────────────────────────────────────────
def setup_host(bridge, gw_ip, subnet, host_ip, vnc_port, label):
    print(f"\n  → {label} {host_ip} (VNC :{vnc_port})")
    add_bridge_ip(bridge, subnet)

    # Check if already set up
    r = subprocess.run(
        ['ssh','-o','StrictHostKeyChecking=no',
         '-o','ConnectTimeout=3',
         '-o','PasswordAuthentication=no',
         f'root@{host_ip}','iperf3 --version'],
        capture_output=True, text=True, timeout=8
    )
    if 'iperf' in r.stdout.lower():
        print(f"    ✓ Already configured, skipping")
        return True

    cmds = make_install_commands(host_ip, gw_ip)
    try:
        send_vnc_keys(vnc_port, ["root"], inter_cmd_delay=0.5)
        time.sleep(2)
        send_vnc_keys(vnc_port, cmds, inter_cmd_delay=1.5)
        print(f"    VNC commands sent")
    except Exception as e:
        print(f"    VNC error: {e}"); return False

    time.sleep(15)
    if not wait_for_ssh(host_ip):
        return False

    # Setup passwordless SSH
    subprocess.run(['ssh-keygen','-t','rsa','-N','','-f','/root/.ssh/id_rsa'], capture_output=True)
    subprocess.run(
        f"sshpass -p root ssh-copy-id -o StrictHostKeyChecking=no root@{host_ip}",
        shell=True, capture_output=True
    )

    # Verify
    r = subprocess.run(
        ['ssh','-o','StrictHostKeyChecking=no',f'root@{host_ip}','iperf3 --version'],
        capture_output=True, text=True, timeout=10
    )
    if 'iperf' in r.stdout.lower():
        print(f"    ✓ iperf3 verified, passwordless SSH ready")
        return True
    else:
        print(f"    ✗ iperf3 check failed")
        return False

# ── Main ─────────────────────────────────────────────────────────────────────
def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "help"
    if mode not in ("t1","t2","t3","all"):
        print(__doc__); sys.exit(0)

    try:
        s = socket.socket(); s.settimeout(2)
        s.connect(('127.0.0.1', HTTP_PORT)); s.close()
    except:
        print(f"ERROR: HTTP server not running on port {HTTP_PORT}")
        print(f"Run:  cd /tmp/apks && python3 -m http.server {HTTP_PORT} &")
        sys.exit(1)

    print(f"✓ HTTP server on port {HTTP_PORT}")

    topos = ["t1","t2","t3"] if mode == "all" else [mode]
    for t in topos:
        print(f"\n{'━'*50}\n Setting up {t.upper()} (src + dst only)\n{'━'*50}")
        for entry in TOPOLOGIES[t]:
            setup_host(*entry)

    print("\n✓ All done!")

if __name__ == '__main__':
    main()
