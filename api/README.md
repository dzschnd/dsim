# TODO
- Cleanup containers/networks
- Handle if docker has too many networks
- Handle IP uniqueness
- Make sure of node isolation between networks
- What if host docker uses 10.251.0.0/16?
- block UI while topo is loading (OR make topo appear in real time instead of waiting for the full build before rendering the nodes: 1. return logical topo, 2. apply topo)
- unlink/link seemingly drops route config
- let users choose bitrate on iperf tcp/udp clients

+ Loss corelation
- Packet reorder/duplication/corruption
- Unstable cable connection (flapping)
- Broken NIC
- Queue overflow - tc limit
+ Blackhole routes
+ Stop validating route rules (can set unavailble routes)
+ Node freeze
- highlight active links
+ remove request timeout
- UI overhaul


# Sources

- Go: https://go.dev/
- Air: https://github.com/air-verse/air
- Docker engine API: https://pkg.go.dev/github.com/docker/docker/client
