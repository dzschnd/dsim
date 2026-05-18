FROM oven/bun:1 AS client-builder
WORKDIR /app/client

COPY client/package.json client/bun.lock ./
RUN bun install --frozen-lockfile

COPY client ./
RUN bun run build

FROM golang:1.25.5-alpine AS api-builder
WORKDIR /app/api

COPY api/go.mod api/go.sum ./
RUN go mod download

COPY api ./
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/dsim ./cmd

FROM alpine:3.20
WORKDIR /app

RUN apk add --no-cache ca-certificates

COPY --from=client-builder /app/client/dist /app/client-dist
COPY --from=api-builder /out/dsim /app/dsim
COPY infra/node /app/docker/node

ENV PORT=8080
ENV NODE_IMAGE=dsim/node:local
ENV NODE_IMAGE_PATH=/app/docker/node
ENV CLIENT_DIST_DIR=/app/client-dist

EXPOSE 8080

CMD ["/app/dsim"]
