FROM docker.io/library/alpine:3.21 as runtime

RUN \
  apk add --update --no-cache \
    bash \
    curl \
    ca-certificates \
    tzdata

ENTRYPOINT ["cloudscale-loadbalancer-controller"]
COPY cloudscale-loadbalancer-controller /usr/bin/

USER 65536:0
