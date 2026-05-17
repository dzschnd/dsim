# dsim

Docker-based network simulator


docker build -f infra/app/Dockerfile -t dsim/app:local .
docker run --rm -p 8080:8080 \
  --cap-add=NET_ADMIN --cap-add=SYS_ADMIN \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /var/run/docker/netns:/var/run/docker/netns:ro \
  dsim/app:local

# local dev (requires elevated privileges for netlink operations)
sudo air

docker buildx build --platform linux/amd64,linux/arm64 -f infra/app/Dockerfile -t dzschnd/dsim:latest --push .

docker pull dzschnd/dsim:latest
docker run --rm -p 8080:8080 \
  --cap-add=NET_ADMIN --cap-add=SYS_ADMIN \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /var/run/docker/netns:/var/run/docker/netns:ro \
  dzschnd/dsim:latest
