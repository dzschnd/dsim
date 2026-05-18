# dsim

Docker-based network simulator

```bash
docker build -f infra/app/Dockerfile -t dsim/app:local .
```

```bash
docker buildx build --platform linux/amd64,linux/arm64 -f infra/app/Dockerfile -t dzschnd/dsim:latest --push .
```

```bash
docker pull dzschnd/dsim:latest

docker run --rm \
  -p 8080:8080 \
  --pid host \
  --cap-add NET_ADMIN \
  -v /var/run/docker.sock:/var/run/docker.sock \
  dzschnd/dsim:latest
```

# local dev (requires elevated privileges for netlink operations)

```bash
sudo air
```
