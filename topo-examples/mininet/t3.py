#!/usr/bin/env python3
"""
39 nodes: 24 hosts, 8 routers (r7-r14), 7 switches (sw0-sw6)

LAN subnets:
  sw0 — r7 GW  — 10.0.1.x/24   (h1, h2, h3, h4)
  sw1 — r9 GW  — 10.0.2.x/24   (h5, h6, h7, h8)
  sw2 — r10 GW — 10.0.3.x/24   (h9, h10, h11, h12)
  sw5 — r14 GW — 10.0.4.x/24   (h13, h14, h15, h16)
  sw4 — r13 GW — 10.0.5.x/24   (h17, h18, h19, h20)
  sw3 — r11 GW — 10.0.6.x/24   (h21, h22, h23, h24)

Router interconnects (point-to-point /30):
  r7-eth1  10.255.78.1/30  <->  r8-eth0  10.255.78.2/30
  r8-eth1  10.255.89.1/30  <->  r9-eth0  10.255.89.2/30
  r9-eth1  10.255.90.1/30  <->  r10-eth0 10.255.90.2/30
  r10-eth1 10.255.104.1/30 <->  r14-eth0 10.255.104.2/30
  r14-eth1 10.255.134.1/30 <->  r13-eth0 10.255.134.2/30
  r13-eth1 10.255.132.1/30 <->  r12-eth0 10.255.132.2/30
  r12-eth1 10.255.121.1/30 <->  r11-eth2 10.255.121.2/30  (NOTE: r12-eth1 side)
  r11-eth1 10.255.117.1/30 <->  r7-eth2  10.255.117.2/30

Transit/shared bus sw6 (10.255.100.x/24):
  r8-eth2  10.255.100.8/24
  r9-eth2  10.255.100.9/24
  r12-eth2 10.255.100.12/24
  r13-eth2 10.255.100.13/24

sw6 also bridges sw1 and sw4:
  sw1-eth0 <-> sw6-eth5   (sw1 is the 10.0.2 LAN switch, its uplink via sw6 transit)
  sw4-eth0 <-> sw6-eth4   (sw4 is the 10.0.5 LAN switch, its uplink via sw6 transit)

Run:  sudo python3 mn_t3.py
"""

import time
from mininet.net import Mininet
from mininet.node import Host, OVSBridge
from mininet.link import TCLink
from mininet.log import setLogLevel, info
from mininet.cli import CLI


class LinuxRouter(Host):
    """Host node with IP forwarding enabled."""

    def config(self, **params):
        super().config(**params)
        self.cmd('sysctl -w net.ipv4.ip_forward=1')

    def terminate(self):
        self.cmd('sysctl -w net.ipv4.ip_forward=0')
        super().terminate()


def iface(node, idx):
    """Return the Mininet interface name for a node's idx-th interface (0-based)."""
    return '%s-eth%d' % (node.name, idx)


def cfg(node, idx, cidr):
    """Flush, assign CIDR, and bring up a node interface."""
    intf = iface(node, idx)
    node.cmd('ip addr flush dev %s' % intf)
    node.cmd('ip addr add %s dev %s' % (cidr, intf))
    node.cmd('ip link set %s up' % intf)


def route(node, dst, via):
    node.cmd('ip route add %s via %s' % (dst, via))


def build():
    setLogLevel('info')

    net = Mininet(host=Host, switch=OVSBridge, link=TCLink, controller=None)

    info('*** Adding routers\n')
    r7  = net.addHost('r7',  cls=LinuxRouter, ip=None)
    r8  = net.addHost('r8',  cls=LinuxRouter, ip=None)
    r9  = net.addHost('r9',  cls=LinuxRouter, ip=None)
    r10 = net.addHost('r10', cls=LinuxRouter, ip=None)
    r11 = net.addHost('r11', cls=LinuxRouter, ip=None)
    r12 = net.addHost('r12', cls=LinuxRouter, ip=None)
    r13 = net.addHost('r13', cls=LinuxRouter, ip=None)
    r14 = net.addHost('r14', cls=LinuxRouter, ip=None)

    info('*** Adding switches\n')
    sw0 = net.addSwitch('sw0')
    sw1 = net.addSwitch('sw1')
    sw2 = net.addSwitch('sw2')
    sw3 = net.addSwitch('sw3')
    sw4 = net.addSwitch('sw4')
    sw5 = net.addSwitch('sw5')
    sw6 = net.addSwitch('sw6')

    info('*** Adding hosts\n')
    h1  = net.addHost('h1',  ip=None)
    h2  = net.addHost('h2',  ip=None)
    h3  = net.addHost('h3',  ip=None)
    h4  = net.addHost('h4',  ip=None)
    h5  = net.addHost('h5',  ip=None)
    h6  = net.addHost('h6',  ip=None)
    h7  = net.addHost('h7',  ip=None)
    h8  = net.addHost('h8',  ip=None)
    h9  = net.addHost('h9',  ip=None)
    h10 = net.addHost('h10', ip=None)
    h11 = net.addHost('h11', ip=None)
    h12 = net.addHost('h12', ip=None)
    h13 = net.addHost('h13', ip=None)
    h14 = net.addHost('h14', ip=None)
    h15 = net.addHost('h15', ip=None)
    h16 = net.addHost('h16', ip=None)
    h17 = net.addHost('h17', ip=None)
    h18 = net.addHost('h18', ip=None)
    h19 = net.addHost('h19', ip=None)
    h20 = net.addHost('h20', ip=None)
    h21 = net.addHost('h21', ip=None)
    h22 = net.addHost('h22', ip=None)
    h23 = net.addHost('h23', ip=None)
    h24 = net.addHost('h24', ip=None)

    # ---------------------------------------------------------------
    # Links — order determines the eth-index on each multi-iface node.
    # We track each node's next available eth index manually.
    # The comments show: <node>-eth<idx>
    # ---------------------------------------------------------------
    info('*** Adding links\n')

    # ---- sw0 LAN (10.0.1.x): r7-eth0 connects to sw0 ----
    # r7-eth0  sw0-eth0
    net.addLink(r7, sw0)     # r7-eth0, sw0-ethX
    # sw0 <-> hosts
    net.addLink(sw0, h1)     # sw0 <-> h1-eth0
    net.addLink(sw0, h2)     # sw0 <-> h2-eth0
    net.addLink(sw0, h3)     # sw0 <-> h3-eth0
    net.addLink(sw0, h4)     # sw0 <-> h4-eth0

    # ---- r7-r8 point-to-point ----
    # r7-eth1 <-> r8-eth0
    net.addLink(r7, r8)      # r7-eth1, r8-eth0

    # ---- r7-r11 point-to-point (r11 side is eth2 in JSON, but we add eth1 here) ----
    # r11-eth1 <-> r7-eth2
    # We add r11 first link as r11-eth0 later; let's defer and add r7-eth2 / r11-eth? by order:
    # To keep r7's ifaces in order:  eth0=sw0  eth1=r8  eth2=r11
    # We'll add r7-r11 link now so r7 gets eth2 and r11 gets eth0
    net.addLink(r7, r11)     # r7-eth2, r11-eth0

    # ---- r8-r9 point-to-point ----
    # r8-eth1 <-> r9-eth0
    net.addLink(r8, r9)      # r8-eth1, r9-eth0

    # ---- sw6 transit bus — r8-eth2 ----
    net.addLink(r8, sw6)     # r8-eth2, sw6-eth0

    # ---- r9-r10 point-to-point ----
    # r9-eth1 <-> r10-eth0
    net.addLink(r9, r10)     # r9-eth1, r10-eth0

    # ---- sw6 — r9-eth2 ----
    net.addLink(r9, sw6)     # r9-eth2, sw6-eth1

    # ---- sw1 LAN (10.0.2.x): r9 GW is via transit sw6, sw1 connected to sw6 ----
    # sw1-eth0 <-> sw6  (bus access)
    net.addLink(sw1, sw6)    # sw1-eth0, sw6-eth2
    # sw1 <-> hosts
    net.addLink(sw1, h5)     # sw1-eth1 <-> h5-eth0
    net.addLink(sw1, h6)     # sw1-eth2 <-> h6-eth0
    net.addLink(sw1, h7)     # sw1-eth3 <-> h7-eth0
    net.addLink(sw1, h8)     # sw1-eth4 <-> h8-eth0

    # ---- r10-r14 point-to-point ----
    # r10-eth1 <-> r14-eth0
    net.addLink(r10, r14)    # r10-eth1, r14-eth0

    # ---- sw2 LAN (10.0.3.x): r10-eth2 ----
    net.addLink(r10, sw2)    # r10-eth2, sw2-eth0
    # sw2 <-> hosts
    net.addLink(sw2, h9)     # sw2-eth1 <-> h9-eth0
    net.addLink(sw2, h10)    # sw2-eth2 <-> h10-eth0
    net.addLink(sw2, h11)    # sw2-eth3 <-> h11-eth0
    net.addLink(sw2, h12)    # sw2-eth4 <-> h12-eth0

    # ---- r14-r13 point-to-point ----
    # r14-eth1 <-> r13-eth0
    net.addLink(r14, r13)    # r14-eth1, r13-eth0

    # ---- sw5 LAN (10.0.4.x): r14-eth2 ----
    net.addLink(r14, sw5)    # r14-eth2, sw5-eth0
    # sw5 <-> hosts
    net.addLink(sw5, h13)    # sw5-eth1 <-> h13-eth0
    net.addLink(sw5, h14)    # sw5-eth2 <-> h14-eth0
    net.addLink(sw5, h15)    # sw5-eth3 <-> h15-eth0
    net.addLink(sw5, h16)    # sw5-eth4 <-> h16-eth0

    # ---- r13-r12 point-to-point ----
    # r13-eth1 <-> r12-eth0
    net.addLink(r13, r12)    # r13-eth1, r12-eth0

    # ---- sw6 — r13-eth2 ----
    net.addLink(r13, sw6)    # r13-eth2, sw6-eth3

    # ---- sw4 LAN (10.0.5.x): r13 GW is via transit sw6 ----
    # sw4-eth0 <-> sw6
    net.addLink(sw4, sw6)    # sw4-eth0, sw6-eth4
    # sw4 <-> hosts
    net.addLink(sw4, h17)    # sw4-eth1 <-> h17-eth0
    net.addLink(sw4, h18)    # sw4-eth2 <-> h18-eth0
    net.addLink(sw4, h19)    # sw4-eth3 <-> h19-eth0
    net.addLink(sw4, h20)    # sw4-eth4 <-> h20-eth0

    # ---- r12-r11 point-to-point ----
    # r12-eth1 <-> r11-eth1
    net.addLink(r12, r11)    # r12-eth1, r11-eth1 (r11-eth0 used above for r7 link)

    # ---- sw6 — r12-eth2 ----
    net.addLink(r12, sw6)    # r12-eth2, sw6-eth5

    # ---- sw3 LAN (10.0.6.x): r11-eth2 ----
    # r11-eth2 <-> sw3-eth0
    net.addLink(r11, sw3)    # r11-eth2, sw3-eth0
    # sw3 <-> hosts
    net.addLink(sw3, h21)    # sw3-eth1 <-> h21-eth0
    net.addLink(sw3, h22)    # sw3-eth2 <-> h22-eth0
    net.addLink(sw3, h23)    # sw3-eth3 <-> h23-eth0
    net.addLink(sw3, h24)    # sw3-eth4 <-> h24-eth0

    # ---------------------------------------------------------------
    info('*** Starting network\n')
    # ---------------------------------------------------------------
    t_start = time.time()
    net.start()
    t_end = time.time()

    info('*** Configuring routers\n')
    # Interface index tracking:
    # r7:  eth0=sw0  eth1=r8  eth2=r11
    cfg(r7,  0, '10.0.1.1/24')
    cfg(r7,  1, '10.255.78.1/30')
    cfg(r7,  2, '10.255.117.2/30')
    route(r7, '10.0.2.0/24', '10.255.78.2')
    route(r7, '10.0.3.0/24', '10.255.78.2')
    route(r7, '10.0.4.0/24', '10.255.78.2')
    route(r7, '10.0.5.0/24', '10.255.78.2')
    route(r7, '10.0.6.0/24', '10.255.117.1')

    # r8:  eth0=r7  eth1=r9  eth2=sw6
    cfg(r8,  0, '10.255.78.2/30')
    cfg(r8,  1, '10.255.89.1/30')
    cfg(r8,  2, '10.255.100.8/24')
    route(r8, '10.0.1.0/24', '10.255.78.1')
    route(r8, '10.0.2.0/24', '10.255.89.2')
    route(r8, '10.0.3.0/24', '10.255.89.2')
    route(r8, '10.0.4.0/24', '10.255.100.13')
    route(r8, '10.0.5.0/24', '10.255.100.13')
    route(r8, '10.0.6.0/24', '10.255.100.12')

    # r9:  eth0=r8  eth1=r10  eth2=sw6  eth3=sw6-via-sw1(acts as LAN GW)
    # r9 is GW for 10.0.2.x via the sw6 transit bus that sw1 is also on.
    # r9-eth2 is 10.255.100.9 (transit). r9-eth3 is 10.0.2.1 directly.
    # According to JSON: r9 has eth3=10.0.2.1/24 directly.
    # We used r9-eth0=r8, r9-eth1=r10, r9-eth2=sw6, so we need eth3 for the LAN.
    # But sw1 connects to sw6 (transit), not directly to r9. r9's 10.0.2.1 iface
    # is on the same L2 segment as sw1 via sw6. So we assign r9-eth2 (sw6 side)
    # a second address too, OR we use a dedicated link.
    # Looking at JSON more carefully: r9-eth3=10.0.2.1/24 and sw1-eth0 links to sw6.
    # This means r9 has a 4th interface directly into the sw6 transit segment... but
    # that's the same sw6. In Linux/Mininet it's simplest to put BOTH addresses on
    # the same sw6-connected interface, since they're on the same L2.
    # We'll add r9-eth2 = 10.255.100.9 AND r9-eth2 gets a second alias for 10.0.2.1,
    # OR we just add 10.0.2.1 as an alias on the same interface.
    cfg(r9,  0, '10.255.89.2/30')
    cfg(r9,  1, '10.255.90.1/30')
    cfg(r9,  2, '10.255.100.9/24')
    # Add 10.0.2.1 as an additional address on same sw6-connected iface
    r9.cmd('ip addr add 10.0.2.1/24 dev r9-eth2')
    route(r9, '10.0.1.0/24', '10.255.89.1')
    route(r9, '10.0.3.0/24', '10.255.90.2')
    route(r9, '10.0.4.0/24', '10.255.90.2')
    route(r9, '10.0.5.0/24', '10.255.100.13')
    route(r9, '10.0.6.0/24', '10.255.100.12')

    # r10: eth0=r9  eth1=r14  eth2=sw2
    cfg(r10, 0, '10.255.90.2/30')
    cfg(r10, 1, '10.255.104.1/30')
    cfg(r10, 2, '10.0.3.1/24')
    route(r10, '10.0.1.0/24', '10.255.90.1')
    route(r10, '10.0.2.0/24', '10.255.90.1')
    route(r10, '10.0.4.0/24', '10.255.104.2')
    route(r10, '10.0.5.0/24', '10.255.90.1')
    route(r10, '10.0.6.0/24', '10.255.90.1')

    # r14: eth0=r10  eth1=r13  eth2=sw5
    cfg(r14, 0, '10.255.104.2/30')
    cfg(r14, 1, '10.255.134.1/30')
    cfg(r14, 2, '10.0.4.1/24')
    route(r14, '10.0.1.0/24', '10.255.134.2')
    route(r14, '10.0.2.0/24', '10.255.104.1')
    route(r14, '10.0.3.0/24', '10.255.104.1')
    route(r14, '10.0.5.0/24', '10.255.134.2')
    route(r14, '10.0.6.0/24', '10.255.134.2')

    # r13: eth0=r14  eth1=r12  eth2=sw6  eth3=sw4 (10.0.5.1 GW)
    # Same pattern as r9: r13-eth2 is on sw6 transit (10.255.100.13/24),
    # and r13-eth3 is 10.0.5.1/24 also on sw6 (via sw4 being on sw6).
    cfg(r13, 0, '10.255.134.2/30')
    cfg(r13, 1, '10.255.132.1/30')
    cfg(r13, 2, '10.255.100.13/24')
    r13.cmd('ip addr add 10.0.5.1/24 dev r13-eth2')
    route(r13, '10.0.1.0/24', '10.255.100.8')
    route(r13, '10.0.2.0/24', '10.255.100.9')
    route(r13, '10.0.3.0/24', '10.255.100.9')
    route(r13, '10.0.4.0/24', '10.255.134.1')
    route(r13, '10.0.6.0/24', '10.255.100.12')

    # r12: eth0=r13  eth1=r11  eth2=sw6
    cfg(r12, 0, '10.255.132.2/30')
    cfg(r12, 1, '10.255.121.1/30')
    cfg(r12, 2, '10.255.100.12/24')
    route(r12, '10.0.1.0/24', '10.255.100.8')
    route(r12, '10.0.2.0/24', '10.255.100.9')
    route(r12, '10.0.3.0/24', '10.255.100.9')
    route(r12, '10.0.4.0/24', '10.255.100.13')
    route(r12, '10.0.5.0/24', '10.255.100.13')
    route(r12, '10.0.6.0/24', '10.255.121.2')

    # r11: eth0=r7  eth1=r12  eth2=sw3
    cfg(r11, 0, '10.255.117.1/30')
    cfg(r11, 1, '10.255.121.2/30')
    cfg(r11, 2, '10.0.6.1/24')
    route(r11, '10.0.1.0/24', '10.255.117.2')
    route(r11, '10.0.2.0/24', '10.255.121.1')
    route(r11, '10.0.3.0/24', '10.255.121.1')
    route(r11, '10.0.4.0/24', '10.255.121.1')
    route(r11, '10.0.5.0/24', '10.255.121.1')

    info('*** Configuring hosts (10.0.1.x — sw0)\n')
    cfg(h1, 0, '10.0.1.10/24');  h1.cmd('ip route add default via 10.0.1.1')
    cfg(h2, 0, '10.0.1.11/24');  h2.cmd('ip route add default via 10.0.1.1')
    cfg(h3, 0, '10.0.1.12/24');  h3.cmd('ip route add default via 10.0.1.1')
    cfg(h4, 0, '10.0.1.13/24');  h4.cmd('ip route add default via 10.0.1.1')

    info('*** Configuring hosts (10.0.2.x — sw1)\n')
    cfg(h5, 0, '10.0.2.10/24');  h5.cmd('ip route add default via 10.0.2.1')
    cfg(h6, 0, '10.0.2.11/24');  h6.cmd('ip route add default via 10.0.2.1')
    cfg(h7, 0, '10.0.2.12/24');  h7.cmd('ip route add default via 10.0.2.1')
    cfg(h8, 0, '10.0.2.13/24');  h8.cmd('ip route add default via 10.0.2.1')

    info('*** Configuring hosts (10.0.3.x — sw2)\n')
    cfg(h9,  0, '10.0.3.10/24'); h9.cmd('ip route add default via 10.0.3.1')
    cfg(h10, 0, '10.0.3.11/24'); h10.cmd('ip route add default via 10.0.3.1')
    cfg(h11, 0, '10.0.3.12/24'); h11.cmd('ip route add default via 10.0.3.1')
    cfg(h12, 0, '10.0.3.13/24'); h12.cmd('ip route add default via 10.0.3.1')

    info('*** Configuring hosts (10.0.4.x — sw5)\n')
    cfg(h13, 0, '10.0.4.10/24'); h13.cmd('ip route add default via 10.0.4.1')
    cfg(h14, 0, '10.0.4.11/24'); h14.cmd('ip route add default via 10.0.4.1')
    cfg(h15, 0, '10.0.4.12/24'); h15.cmd('ip route add default via 10.0.4.1')
    cfg(h16, 0, '10.0.4.13/24'); h16.cmd('ip route add default via 10.0.4.1')

    info('*** Configuring hosts (10.0.5.x — sw4)\n')
    cfg(h17, 0, '10.0.5.10/24'); h17.cmd('ip route add default via 10.0.5.1')
    cfg(h18, 0, '10.0.5.11/24'); h18.cmd('ip route add default via 10.0.5.1')
    cfg(h19, 0, '10.0.5.12/24'); h19.cmd('ip route add default via 10.0.5.1')
    cfg(h20, 0, '10.0.5.13/24'); h20.cmd('ip route add default via 10.0.5.1')

    info('*** Configuring hosts (10.0.6.x — sw3)\n')
    cfg(h21, 0, '10.0.6.10/24'); h21.cmd('ip route add default via 10.0.6.1')
    cfg(h22, 0, '10.0.6.11/24'); h22.cmd('ip route add default via 10.0.6.1')
    cfg(h23, 0, '10.0.6.12/24'); h23.cmd('ip route add default via 10.0.6.1')
    cfg(h24, 0, '10.0.6.13/24'); h24.cmd('ip route add default via 10.0.6.1')

    info('*** Topology init time: %.4f s\n' % (t_end - t_start))

    info('*** Running CLI (type "exit" or Ctrl-D to quit)\n')
    CLI(net)

    info('*** Stopping network\n')
    net.stop()


if __name__ == '__main__':
    build()
