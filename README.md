# dsim

Docker-based network simulator

### build locally
```bash
# run from repo root
docker build -f app/infra/app/Dockerfile -t dsim/app:local .
```

### build & push
```bash
# run from repo root
# create the builder (skip if it already exists)
docker buildx create --name main-builder --driver docker-container --use

docker login
docker buildx build --platform linux/amd64,linux/arm64 -f app/infra/app/Dockerfile -t dzschnd/dsim:latest --push .
```

### pull app image
```bash
docker pull dzschnd/dsim:latest
```

### run app container (pulls remote image if missing, builds network node image if missing)
```bash
docker run --rm \
  -p 8080:8080 \
  --pid host \
  --cap-add NET_ADMIN \
  -v /var/run/docker.sock:/var/run/docker.sock \
  dzschnd/dsim:latest
```

### local dev (api requires elevated privileges for netlink operations)
#### API
Install [air](https://github.com/air-verse/air) for hot reloading
```bash
cd app/api
cp .env.example .env
sudo $(which air)
```

#### Client
```bash
cd app/client
cp .env.example .env
bun install
bun run dev
```
