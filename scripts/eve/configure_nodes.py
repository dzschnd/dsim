import socket
import time
import struct

def send_vnc_keys(port, commands, delay=0.05):
    """Connect to VNC port and send keystrokes"""
    
    # VNC key codes
    KEY_RETURN = 0xff0d
    KEY_BACKSPACE = 0xff08
    
    def char_to_key(c):
        if c == '\n':
            return KEY_RETURN
        return ord(c)
    
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.connect(('127.0.0.1', port))
    s.settimeout(10)
    
    # RFB handshake
    ver = s.recv(12)
    s.send(b'RFB 003.003\n')
    
    # Security type
    sec = s.recv(4)
    sec_type = struct.unpack('>I', sec)[0]
    
    if sec_type == 2:  # VNC auth
        challenge = s.recv(16)
        # No password - send empty response
        import hashlib
        s.send(b'\x00' * 16)
        s.recv(4)  # auth result
    
    # ClientInit
    s.send(b'\x01')  # shared
    
    # ServerInit - read and discard
    time.sleep(0.5)
    try:
        s.recv(4096)
    except:
        pass
    
    def send_key(keysym, down=True):
        # KeyEvent message: type=4, down, padding, keysym
        msg = struct.pack('>BBHi', 4, 1 if down else 0, 0, keysym)
        s.send(msg)
    
    def type_char(c):
        key = char_to_key(c)
        send_key(key, True)
        time.sleep(0.02)
        send_key(key, False)
        time.sleep(delay)
    
    def type_string(text):
        for c in text:
            if c == '\n':
                send_key(KEY_RETURN, True)
                time.sleep(0.02)
                send_key(KEY_RETURN, False)
                time.sleep(0.1)
            else:
                type_char(c)
    
    # Wait for node to be ready at login prompt
    time.sleep(2)
    
    for cmd in commands:
        type_string(cmd + '\n')
        time.sleep(0.3)
    
    time.sleep(1)
    s.close()

# Node configurations: (port, [commands])
# T2 hosts
t2_hosts = [
    (32770, ["root", "ip link set eth0 up", "ip addr add 192.168.10.10/24 dev eth0", "ip route add default via 192.168.10.1"]),
    (32771, ["root", "ip link set eth0 up", "ip addr add 192.168.10.11/24 dev eth0", "ip route add default via 192.168.10.1"]),
    (32772, ["root", "ip link set eth0 up", "ip addr add 192.168.20.10/24 dev eth0", "ip route add default via 192.168.20.1"]),
    (32773, ["root", "ip link set eth0 up", "ip addr add 192.168.20.11/24 dev eth0", "ip route add default via 192.168.20.1"]),
]

# T3 hosts
t3_hosts = [
    (32777, ["root", "ip link set eth0 up", "ip addr add 10.0.1.10/24 dev eth0", "ip route add default via 10.0.1.1"]),
    (32778, ["root", "ip link set eth0 up", "ip addr add 10.0.1.11/24 dev eth0", "ip route add default via 10.0.1.1"]),
    (32779, ["root", "ip link set eth0 up", "ip addr add 10.0.1.12/24 dev eth0", "ip route add default via 10.0.1.1"]),
    (32780, ["root", "ip link set eth0 up", "ip addr add 10.0.1.13/24 dev eth0", "ip route add default via 10.0.1.1"]),
    (32781, ["root", "ip link set eth0 up", "ip addr add 10.0.2.10/24 dev eth0", "ip route add default via 10.0.2.1"]),
    (32782, ["root", "ip link set eth0 up", "ip addr add 10.0.2.11/24 dev eth0", "ip route add default via 10.0.2.1"]),
    (32783, ["root", "ip link set eth0 up", "ip addr add 10.0.2.12/24 dev eth0", "ip route add default via 10.0.2.1"]),
    (32784, ["root", "ip link set eth0 up", "ip addr add 10.0.2.13/24 dev eth0", "ip route add default via 10.0.2.1"]),
    (32785, ["root", "ip link set eth0 up", "ip addr add 10.0.3.10/24 dev eth0", "ip route add default via 10.0.3.1"]),
    (32786, ["root", "ip link set eth0 up", "ip addr add 10.0.3.11/24 dev eth0", "ip route add default via 10.0.3.1"]),
    (32787, ["root", "ip link set eth0 up", "ip addr add 10.0.3.12/24 dev eth0", "ip route add default via 10.0.3.1"]),
    (32788, ["root", "ip link set eth0 up", "ip addr add 10.0.3.13/24 dev eth0", "ip route add default via 10.0.3.1"]),
    (32789, ["root", "ip link set eth0 up", "ip addr add 10.0.4.10/24 dev eth0", "ip route add default via 10.0.4.1"]),
    (32790, ["root", "ip link set eth0 up", "ip addr add 10.0.4.11/24 dev eth0", "ip route add default via 10.0.4.1"]),
    (32791, ["root", "ip link set eth0 up", "ip addr add 10.0.4.12/24 dev eth0", "ip route add default via 10.0.4.1"]),
    (32792, ["root", "ip link set eth0 up", "ip addr add 10.0.4.13/24 dev eth0", "ip route add default via 10.0.4.1"]),
    (32793, ["root", "ip link set eth0 up", "ip addr add 10.0.5.10/24 dev eth0", "ip route add default via 10.0.5.1"]),
    (32794, ["root", "ip link set eth0 up", "ip addr add 10.0.5.11/24 dev eth0", "ip route add default via 10.0.5.1"]),
    (32795, ["root", "ip link set eth0 up", "ip addr add 10.0.5.12/24 dev eth0", "ip route add default via 10.0.5.1"]),
    (32796, ["root", "ip link set eth0 up", "ip addr add 10.0.5.13/24 dev eth0", "ip route add default via 10.0.5.1"]),
    (32797, ["root", "ip link set eth0 up", "ip addr add 10.0.6.10/24 dev eth0", "ip route add default via 10.0.6.1"]),
    (32798, ["root", "ip link set eth0 up", "ip addr add 10.0.6.11/24 dev eth0", "ip route add default via 10.0.6.1"]),
    (32799, ["root", "ip link set eth0 up", "ip addr add 10.0.6.12/24 dev eth0", "ip route add default via 10.0.6.1"]),
    (32800, ["root", "ip link set eth0 up", "ip addr add 10.0.6.13/24 dev eth0", "ip route add default via 10.0.6.1"]),
]

# T3 routers (VyOS)
t3_routers = [
    (32769, ["vyos", "vyos", "configure",
             "set interfaces ethernet eth0 address '10.0.1.1/24'",
             "set interfaces ethernet eth1 address '10.255.78.1/30'",
             "set interfaces ethernet eth2 address '10.255.117.2/30'",
             "set protocols static route 10.0.2.0/24 next-hop 10.255.78.2",
             "set protocols static route 10.0.3.0/24 next-hop 10.255.78.2",
             "set protocols static route 10.0.4.0/24 next-hop 10.255.78.2",
             "set protocols static route 10.0.5.0/24 next-hop 10.255.78.2",
             "set protocols static route 10.0.6.0/24 next-hop 10.255.117.1",
             "set service ssh port 22",
             "commit", "save", "exit"]),
    (32770, ["vyos", "vyos", "configure",
             "set interfaces ethernet eth0 address '10.255.78.2/30'",
             "set interfaces ethernet eth1 address '10.255.89.1/30'",
             "set interfaces ethernet eth2 address '10.255.100.8/24'",
             "set protocols static route 10.0.1.0/24 next-hop 10.255.78.1",
             "set protocols static route 10.0.2.0/24 next-hop 10.255.89.2",
             "set protocols static route 10.0.3.0/24 next-hop 10.255.89.2",
             "set protocols static route 10.0.4.0/24 next-hop 10.255.100.13",
             "set protocols static route 10.0.5.0/24 next-hop 10.255.100.13",
             "set protocols static route 10.0.6.0/24 next-hop 10.255.100.12",
             "set service ssh port 22",
             "commit", "save", "exit"]),
    (32771, ["vyos", "vyos", "configure",
             "set interfaces ethernet eth0 address '10.255.89.2/30'",
             "set interfaces ethernet eth1 address '10.255.90.1/30'",
             "set interfaces ethernet eth2 address '10.255.100.9/24'",
             "set interfaces ethernet eth3 address '10.0.2.1/24'",
             "set protocols static route 10.0.1.0/24 next-hop 10.255.89.1",
             "set protocols static route 10.0.3.0/24 next-hop 10.255.90.2",
             "set protocols static route 10.0.4.0/24 next-hop 10.255.90.2",
             "set protocols static route 10.0.5.0/24 next-hop 10.255.100.13",
             "set protocols static route 10.0.6.0/24 next-hop 10.255.100.12",
             "set service ssh port 22",
             "commit", "save", "exit"]),
    (32772, ["vyos", "vyos", "configure",
             "set interfaces ethernet eth0 address '10.255.90.2/30'",
             "set interfaces ethernet eth1 address '10.255.104.1/30'",
             "set interfaces ethernet eth2 address '10.0.3.1/24'",
             "set protocols static route 10.0.1.0/24 next-hop 10.255.90.1",
             "set protocols static route 10.0.2.0/24 next-hop 10.255.90.1",
             "set protocols static route 10.0.4.0/24 next-hop 10.255.104.2",
             "set protocols static route 10.0.5.0/24 next-hop 10.255.90.1",
             "set protocols static route 10.0.6.0/24 next-hop 10.255.90.1",
             "set service ssh port 22",
             "commit", "save", "exit"]),
    (32773, ["vyos", "vyos", "configure",
             "set interfaces ethernet eth0 address '10.0.6.1/24'",
             "set interfaces ethernet eth1 address '10.255.121.2/30'",
             "set interfaces ethernet eth2 address '10.255.117.1/30'",
             "set protocols static route 10.0.1.0/24 next-hop 10.255.117.2",
             "set protocols static route 10.0.2.0/24 next-hop 10.255.121.1",
             "set protocols static route 10.0.3.0/24 next-hop 10.255.121.1",
             "set protocols static route 10.0.4.0/24 next-hop 10.255.121.1",
             "set protocols static route 10.0.5.0/24 next-hop 10.255.121.1",
             "set service ssh port 22",
             "commit", "save", "exit"]),
    (32774, ["vyos", "vyos", "configure",
             "set interfaces ethernet eth0 address '10.255.132.2/30'",
             "set interfaces ethernet eth1 address '10.255.121.1/30'",
             "set interfaces ethernet eth2 address '10.255.100.12/24'",
             "set protocols static route 10.0.1.0/24 next-hop 10.255.100.8",
             "set protocols static route 10.0.2.0/24 next-hop 10.255.100.9",
             "set protocols static route 10.0.3.0/24 next-hop 10.255.100.9",
             "set protocols static route 10.0.4.0/24 next-hop 10.255.100.13",
             "set protocols static route 10.0.5.0/24 next-hop 10.255.100.13",
             "set protocols static route 10.0.6.0/24 next-hop 10.255.121.2",
             "set service ssh port 22",
             "commit", "save", "exit"]),
    (32775, ["vyos", "vyos", "configure",
             "set interfaces ethernet eth0 address '10.255.134.2/30'",
             "set interfaces ethernet eth1 address '10.255.132.1/30'",
             "set interfaces ethernet eth2 address '10.255.100.13/24'",
             "set interfaces ethernet eth3 address '10.0.5.1/24'",
             "set protocols static route 10.0.1.0/24 next-hop 10.255.100.8",
             "set protocols static route 10.0.2.0/24 next-hop 10.255.100.9",
             "set protocols static route 10.0.3.0/24 next-hop 10.255.100.9",
             "set protocols static route 10.0.4.0/24 next-hop 10.255.134.1",
             "set protocols static route 10.0.6.0/24 next-hop 10.255.100.12",
             "set service ssh port 22",
             "commit", "save", "exit"]),
    (32776, ["vyos", "vyos", "configure",
             "set interfaces ethernet eth0 address '10.255.104.2/30'",
             "set interfaces ethernet eth1 address '10.255.134.1/30'",
             "set interfaces ethernet eth2 address '10.0.4.1/24'",
             "set protocols static route 10.0.1.0/24 next-hop 10.255.134.2",
             "set protocols static route 10.0.2.0/24 next-hop 10.255.104.1",
             "set protocols static route 10.0.3.0/24 next-hop 10.255.104.1",
             "set protocols static route 10.0.5.0/24 next-hop 10.255.134.2",
             "set protocols static route 10.0.6.0/24 next-hop 10.255.134.2",
             "set service ssh port 22",
             "commit", "save", "exit"]),
]

import sys

mode = sys.argv[1] if len(sys.argv) > 1 else "help"

if mode == "t2-hosts":
    nodes = t2_hosts
elif mode == "t3-hosts":
    nodes = t3_hosts
elif mode == "t3-routers":
    nodes = t3_routers
else:
    print("Usage: python3 configure_nodes.py [t2-hosts|t3-hosts|t3-routers]")
    sys.exit(1)

for port, commands in nodes:
    print(f"Configuring port {port}...")
    try:
        send_vnc_keys(port, commands)
        print(f"  Done port {port}")
    except Exception as e:
        print(f"  Error on port {port}: {e}")

print("Finished!")
