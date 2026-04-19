# TODO
- Cleanup containers/networks
- Handle if docker has too many networks
- Handle IP uniqueness
- Make sure of node isolation between networks
- Build containers on startup if don't exist
- Graceful shutdown
- ping
- 3 node types (switch/router/host)
- What if host docker uses 10.251.0.0/16?
- on sigint: stop/remove containers, prune networks

# Sources

- Go: https://go.dev/
- Air: https://github.com/air-verse/air
- Docker engine API: https://pkg.go.dev/github.com/docker/docker/client
