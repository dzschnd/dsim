FROM alpine:3.20

RUN apk add --no-cache \
    iproute2 \
    iputils \
    traceroute \
    iperf3 \
    curl \
    bash \
    busybox-extras \
    darkhttpd

CMD ["sleep", "infinity"]

# docker build --no-cache --network=host -t dsim/host:local -f apps/api/docker/host/Dockerfile apps/api/docker/host
