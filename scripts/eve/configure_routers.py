import socket, struct, time, sys, subprocess

SHIFT_CHARS = {
    '!':'1','@':'2','#':'3','$':'4','%':'5',
    '^':'6','&':'7','*':'8','(':'9',')':'0',
    '_':'-','+':'=','{':'[','}':']','|':'\\',
    ':':';','"':"'",'<':',','>':'.','?':'/',
    '~':'`',
}
KEY_SHIFT = 0xffe1
KEY_RETURN = 0xff0d

def vnc_send(port, commands, delay=2.0):
    s = socket.socket()
    s.connect(('127.0.0.1', port))
    s.settimeout(15)
    s.recv(12); s.send(b'RFB 003.003\n')
    sec = struct.unpack('>I', s.recv(4))[0]
    if sec == 2: s.recv(16); s.send(b'\x00'*16); s.recv(4)
    s.send(b'\x01'); time.sleep(0.5)
    try: s.recv(4096)
    except: pass
    def send_key(k, down):
        s.send(struct.pack('>BBHi', 4, 1 if down else 0, 0, k))
    def type_char(c):
        if c == '\n':
            send_key(KEY_RETURN, True); time.sleep(0.02)
            send_key(KEY_RETURN, False); time.sleep(0.05)
        elif c in SHIFT_CHARS:
            base = ord(SHIFT_CHARS[c])
            send_key(KEY_SHIFT, True); time.sleep(0.01)
            send_key(base, True); time.sleep(0.02)
            send_key(base, False); time.sleep(0.01)
            send_key(KEY_SHIFT, False); time.sleep(0.03)
        else:
            send_key(ord(c), True); time.sleep(0.02)
            send_key(ord(c), False); time.sleep(0.03)
    time.sleep(1)
    for cmd in commands:
        for c in cmd: type_char(c)
        type_char('\n')
        time.sleep(delay)
    s.close()

def vyos_cmds(interfaces, routes=[]):
    cmds = ["vyos", "vyos", "configure"]
    for iface, addr in interfaces:
        cmds.append(f"set interfaces ethernet {iface} address '{addr}'")
    for dst, nh in routes:
        cmds.append(f"set protocols static route {dst} next-hop {nh}")
    cmds += ["set service ssh port 22", "commit", "save", "exit", "echo DONE"]
    return cmds

def verify_ssh(ip, user='vyos'):
    r = subprocess.run(
        ['ssh', '-o', 'StrictHostKeyChecking=no', '-o', 'ConnectTimeout=5',
         f'{user}@{ip}', 'show interfaces'],
        capture_output=True, text=True, timeout=15)
    return r.returncode == 0, r.stdout

# ── Router configs ────────────────────────────────────────────────────────────
ROUTERS = {
    't2': [
        {
            'name': 'r1', 'port': 32769, 'verify_ip': '192.168.10.1',
            'interfaces': [('eth0','192.168.10.1/24'), ('eth1','192.168.20.1/24')],
            'routes': []
        },
    ],
    't3': [
        {
            'name': 'r7', 'port': 32769, 'verify_ip': '10.0.1.1',
            'interfaces': [('eth0','10.0.1.1/24'),('eth1','10.255.78.1/30'),('eth2','10.255.117.2/30')],
            'routes': [('10.0.2.0/24','10.255.78.2'),('10.0.3.0/24','10.255.78.2'),
                       ('10.0.4.0/24','10.255.78.2'),('10.0.5.0/24','10.255.78.2'),
                       ('10.0.6.0/24','10.255.117.1')]
        },
        {
            'name': 'r8', 'port': 32770, 'verify_ip': '10.255.78.2',
            'interfaces': [('eth0','10.255.78.2/30'),('eth1','10.255.89.1/30'),('eth2','10.255.100.8/24')],
            'routes': [('10.0.1.0/24','10.255.78.1'),('10.0.2.0/24','10.255.89.2'),
                       ('10.0.3.0/24','10.255.89.2'),('10.0.4.0/24','10.255.100.13'),
                       ('10.0.5.0/24','10.255.100.13'),('10.0.6.0/24','10.255.100.12')]
        },
        {
            'name': 'r9', 'port': 32771, 'verify_ip': '10.0.2.1',
            'interfaces': [('eth0','10.255.89.2/30'),('eth1','10.255.90.1/30'),
                           ('eth2','10.255.100.9/24'),('eth3','10.0.2.1/24')],
            'routes': [('10.0.1.0/24','10.255.89.1'),('10.0.3.0/24','10.255.90.2'),
                       ('10.0.4.0/24','10.255.90.2'),('10.0.5.0/24','10.255.100.13'),
                       ('10.0.6.0/24','10.255.100.12')]
        },
        {
            'name': 'r10', 'port': 32772, 'verify_ip': '10.0.3.1',
            'interfaces': [('eth0','10.255.90.2/30'),('eth1','10.255.104.1/30'),('eth2','10.0.3.1/24')],
            'routes': [('10.0.1.0/24','10.255.90.1'),('10.0.2.0/24','10.255.90.1'),
                       ('10.0.4.0/24','10.255.104.2'),('10.0.5.0/24','10.255.90.1'),
                       ('10.0.6.0/24','10.255.90.1')]
        },
        {
            'name': 'r11', 'port': 32773, 'verify_ip': '10.0.6.1',
            'interfaces': [('eth0','10.0.6.1/24'),('eth1','10.255.121.2/30'),('eth2','10.255.117.1/30')],
            'routes': [('10.0.1.0/24','10.255.117.2'),('10.0.2.0/24','10.255.121.1'),
                       ('10.0.3.0/24','10.255.121.1'),('10.0.4.0/24','10.255.121.1'),
                       ('10.0.5.0/24','10.255.121.1')]
        },
        {
            'name': 'r12', 'port': 32774, 'verify_ip': '10.255.100.12',
            'interfaces': [('eth0','10.255.132.2/30'),('eth1','10.255.121.1/30'),('eth2','10.255.100.12/24')],
            'routes': [('10.0.1.0/24','10.255.100.8'),('10.0.2.0/24','10.255.100.9'),
                       ('10.0.3.0/24','10.255.100.9'),('10.0.4.0/24','10.255.100.13'),
                       ('10.0.5.0/24','10.255.100.13'),('10.0.6.0/24','10.255.121.2')]
        },
        {
            'name': 'r13', 'port': 32775, 'verify_ip': '10.0.5.1',
            'interfaces': [('eth0','10.255.134.2/30'),('eth1','10.255.132.1/30'),
                           ('eth2','10.255.100.13/24'),('eth3','10.0.5.1/24')],
            'routes': [('10.0.1.0/24','10.255.100.8'),('10.0.2.0/24','10.255.100.9'),
                       ('10.0.3.0/24','10.255.100.9'),('10.0.4.0/24','10.255.134.1'),
                       ('10.0.6.0/24','10.255.100.12')]
        },
        {
            'name': 'r14', 'port': 32776, 'verify_ip': '10.0.4.1',
            'interfaces': [('eth0','10.255.104.2/30'),('eth1','10.255.134.1/30'),('eth2','10.0.4.1/24')],
            'routes': [('10.0.1.0/24','10.255.134.2'),('10.0.2.0/24','10.255.104.1'),
                       ('10.0.3.0/24','10.255.104.1'),('10.0.5.0/24','10.255.134.2'),
                       ('10.0.6.0/24','10.255.134.2')]
        },
    ],
}

# ── Main ──────────────────────────────────────────────────────────────────────
mode = sys.argv[1] if len(sys.argv) > 1 else 'help'
if mode not in ('t2', 't3'):
    print("Usage: python3 configure_routers.py [t2|t3]")
    sys.exit(1)

routers = ROUTERS[mode]
print(f"Configuring {len(routers)} router(s) for {mode.upper()}...")

for r in routers:
    print(f"\n  {r['name']} (VNC :{r['port']})...", end=' ', flush=True)
    cmds = vyos_cmds(r['interfaces'], r['routes'])
    vnc_send(r['port'], cmds, delay=2.0)
    print("commands sent, waiting 30s...", end=' ', flush=True)
    time.sleep(30)
    ok, out = verify_ssh(r['verify_ip'])
    if ok:
        print(f"✓ SSH verified")
    else:
        print(f"✗ SSH failed (may need more time)")

print("\nAll done!")
