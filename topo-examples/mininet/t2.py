#!/usr/bin/env python3
"""
Topology:
  Hosts   : h1 192.168.10.10/24 gw 192.168.10.1
             h2 192.168.10.11/24 gw 192.168.10.1
             h3 192.168.20.10/24 gw 192.168.20.1
             h4 192.168.20.11/24 gw 192.168.20.1
  Switches: s1 (LAN 192.168.10.x), s2 (LAN 192.168.20.x)
  Router  : r1  eth0 192.168.10.1/24 — s1
                eth1 192.168.20.1/24 — s2

  Links:
    h1 — s1
    h2 — s1
    r1 — s1  (r1-eth0)
    h3 — s2
    h4 — s2
    r1 — s2  (r1-eth1)

Run:  sudo python3 mn_t2.py
"""

import time
from mininet.net import Mininet
from mininet.node import Host, OVSBridge
from mininet.link import TCLink
from mininet.log import setLogLevel, info
from mininet.cli import CLI


class LinuxRouter(Host):
    """A Host with IP forwarding enabled — acts as a Linux router."""

    def config(self, **params):
        super().config(**params)
        self.cmd('sysctl -w net.ipv4.ip_forward=1')

    def terminate(self):
        self.cmd('sysctl -w net.ipv4.ip_forward=0')
        super().terminate()


def build():
    setLogLevel('info')

    net = Mininet(host=Host, switch=OVSBridge, link=TCLink, controller=None)

    info('*** Adding nodes\n')
    r1 = net.addHost('r1', cls=LinuxRouter, ip=None)
    s1 = net.addSwitch('s1')
    s2 = net.addSwitch('s2')
    h1 = net.addHost('h1', ip=None)
    h2 = net.addHost('h2', ip=None)
    h3 = net.addHost('h3', ip=None)
    h4 = net.addHost('h4', ip=None)

    info('*** Adding links\n')
    # h1 — s1
    net.addLink(h1, s1)
    # h2 — s1
    net.addLink(h2, s1)
    # r1-eth0 — s1
    net.addLink(r1, s1)
    # h3 — s2
    net.addLink(h3, s2)
    # h4 — s2
    net.addLink(h4, s2)
    # r1-eth1 — s2
    net.addLink(r1, s2)

    info('*** Starting network\n')
    t_start = time.time()
    net.start()
    t_end = time.time()

    info('*** Configuring interfaces and routes\n')

    # --- r1 ---
    # r1-eth0 is the first link added for r1: connects to s1
    # r1-eth1 is the second link: connects to s2
    r1.cmd('ip addr flush dev r1-eth0')
    r1.cmd('ip addr add 192.168.10.1/24 dev r1-eth0')
    r1.cmd('ip link set r1-eth0 up')

    r1.cmd('ip addr flush dev r1-eth1')
    r1.cmd('ip addr add 192.168.20.1/24 dev r1-eth1')
    r1.cmd('ip link set r1-eth1 up')

    # --- h1 ---
    h1.cmd('ip addr flush dev h1-eth0')
    h1.cmd('ip addr add 192.168.10.10/24 dev h1-eth0')
    h1.cmd('ip link set h1-eth0 up')
    h1.cmd('ip route add default via 192.168.10.1')

    # --- h2 ---
    h2.cmd('ip addr flush dev h2-eth0')
    h2.cmd('ip addr add 192.168.10.11/24 dev h2-eth0')
    h2.cmd('ip link set h2-eth0 up')
    h2.cmd('ip route add default via 192.168.10.1')

    # --- h3 ---
    h3.cmd('ip addr flush dev h3-eth0')
    h3.cmd('ip addr add 192.168.20.10/24 dev h3-eth0')
    h3.cmd('ip link set h3-eth0 up')
    h3.cmd('ip route add default via 192.168.20.1')

    # --- h4 ---
    h4.cmd('ip addr flush dev h4-eth0')
    h4.cmd('ip addr add 192.168.20.11/24 dev h4-eth0')
    h4.cmd('ip link set h4-eth0 up')
    h4.cmd('ip route add default via 192.168.20.1')

    info('*** Topology init time: %.4f s\n' % (t_end - t_start))

    info('*** Running CLI (type "exit" or Ctrl-D to quit)\n')
    CLI(net)

    info('*** Stopping network\n')
    net.stop()


if __name__ == '__main__':
    build()
