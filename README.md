# dsim

Docker-based network simulator


docker build -f infra/app/Dockerfile -t dsim/app:local .
docker run --rm \
  --network host \
  --pid host \
  --cap-add NET_ADMIN \
  -v /var/run/docker.sock:/var/run/docker.sock \
  dsim/app:local

# local dev (requires elevated privileges for netlink operations)
sudo air

docker buildx build --platform linux/amd64,linux/arm64 -f infra/app/Dockerfile -t dzschnd/dsim:latest --push .

docker pull dzschnd/dsim:latest

docker run --rm \
  --network host \
  --pid host \
  --cap-add NET_ADMIN \
  -v /var/run/docker.sock:/var/run/docker.sock \
  dzschnd/dsim:latest
