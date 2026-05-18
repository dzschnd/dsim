#!/usr/bin/env python3
"""
Topology : 2 hosts connected directly (no switch, no router)
  h1  eth0  192.168.10.10/24
  h2  eth0  192.168.10.11/24
  Link: h1-eth0 <-> h2-eth0

Run:  sudo python3 mn_t1.py
"""

import time
from mininet.net import Mininet
from mininet.node import Host
from mininet.link import TCLink
from mininet.log import setLogLevel, info
from mininet.cli import CLI


def build():
    setLogLevel('info')

    net = Mininet(host=Host, link=TCLink, controller=None)

    info('*** Adding hosts\n')
    h1 = net.addHost('h1', ip=None)
    h2 = net.addHost('h2', ip=None)

    info('*** Adding links\n')
    net.addLink(h1, h2)

    info('*** Starting network\n')
    t_start = time.time()
    net.start()
    t_end = time.time()

    info('*** Configuring interfaces and routes\n')
    # h1
    h1.cmd('ip addr flush dev h1-eth0')
    h1.cmd('ip addr add 192.168.10.10/24 dev h1-eth0')
    h1.cmd('ip link set h1-eth0 up')

    # h2
    h2.cmd('ip addr flush dev h2-eth0')
    h2.cmd('ip addr add 192.168.10.11/24 dev h2-eth0')
    h2.cmd('ip link set h2-eth0 up')

    info('*** Topology init time: %.4f s\n' % (t_end - t_start))

    info('*** Running CLI (type "exit" or Ctrl-D to quit)\n')
    CLI(net)

    info('*** Stopping network\n')
    net.stop()


if __name__ == '__main__':
    build()
