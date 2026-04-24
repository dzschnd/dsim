# dsim

Docker-based network simulator


docker build -f infra/app/Dockerfile -t dsim/app:local .
docker run --rm -p 8080:8080 -v /var/run/docker.sock:/var/run/docker.sock dsim/app:local

docker buildx build --platform linux/amd64,linux/arm64 -f infra/app/Dockerfile -t dzschnd/dsim:latest --push .

docker run --rm -p 8080:8080 -v /var/run/docker.sock:/var/run/docker.sock dzschnd/dsim:latest
