FROM alpine:3.20

RUN apk add --no-cache \
    iproute2 \
    iputils \
    traceroute \
    iperf3 \
    curl \
    bash \
    coreutils \
    busybox-extras \
    darkhttpd \
    socat

CMD ["sleep", "infinity"]

# docker build --no-cache --network=host -t dsim/node:local -f apps/api/docker/node/Dockerfile apps/api/docker/node
