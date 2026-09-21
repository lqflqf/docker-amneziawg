# syntax=docker/dockerfile:1

# Dockerfile for AmneziaWG with LinuxServer.io architecture
# Multi-stage build: compile amneziawg-go, awg-tools, then create runtime image

# Upstream version defaults — override via --build-arg or CI
ARG AMNEZIAWG_GO_VERSION=v3.1.20260814
ARG AMNEZIAWG_TOOLS_VERSION=v3.1.20260812

# ============================================================================
# Stage 1: Compile amneziawg-go
# ============================================================================
FROM golang:1.26.8-alpine AS go-builder

ARG AMNEZIAWG_GO_VERSION
RUN apk add --no-cache git build-base

WORKDIR /src
RUN git clone --branch ${AMNEZIAWG_GO_VERSION} --depth 1 https://github.com/amnezia-vpn/amneziawg-go.git .
RUN CGO_ENABLED=1 go build -ldflags '-linkmode external -extldflags "-fno-PIC -static"' -v -o amneziawg-go

# ============================================================================
# Stage 2: Compile awg-tools from source
# ============================================================================
FROM alpine:3.24.2 AS tools-builder

ARG AMNEZIAWG_TOOLS_VERSION
RUN apk add --no-cache git build-base linux-headers bash

WORKDIR /src
RUN git clone --branch ${AMNEZIAWG_TOOLS_VERSION} --depth 1 https://github.com/amnezia-vpn/amneziawg-tools.git .
WORKDIR /src/src
# Build awg binary and install awg-quick script
RUN make && \
    make install DESTDIR=/tools-install && \
    mkdir -p /tools-install/usr/bin && \
    cp /src/src/wg-quick/linux.bash /tools-install/usr/bin/awg-quick && \
    chmod +x /tools-install/usr/bin/awg-quick

# ============================================================================
# Stage 3: Runtime image using LinuxServer base
# ============================================================================
FROM ghcr.io/linuxserver/baseimage-alpine:3.24

# set version label
ARG BUILD_DATE
ARG VERSION
ARG AMNEZIAWG_GO_VERSION
ARG AMNEZIAWG_TOOLS_VERSION
LABEL build_version="AmneziaWG version:- ${VERSION} Build-date:- ${BUILD_DATE}"
LABEL maintainer="AYastrebov"
LABEL org.opencontainers.image.source="https://github.com/AYastrebov/docker-amneziawg"
LABEL org.opencontainers.image.description="AmneziaWG VPN container (amneziawg-tools ${AMNEZIAWG_TOOLS_VERSION}, amneziawg-go ${AMNEZIAWG_GO_VERSION})"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.version="${AMNEZIAWG_TOOLS_VERSION}"

ENV LSIO_FIRST_PARTY="false"

RUN \
  echo "**** install dependencies ****" && \
  apk add --no-cache \
    bc=1.08.2-r1 \
    ca-certificates-bundle=20260909-r0 \
    grep=3.12-r0 \
    iproute2=7.0.0-r0 \
    iptables=1.8.13-r0 \
    ip6tables=1.8.13-r0 \
    iputils=20250605-r2 \
    kmod=34.2-r1 \
    libcap-utils=2.78-r0 \
    libqrencode-tools=4.1.1-r3 \
    net-tools=2.10-r3 \
    nftables=1.1.6-r1 \
    openresolv=3.17.4-r0 \
    unbound=1.25.2-r2 && \
  echo "wireguard" >> /etc/modules && \
  echo "**** cleanup ****" && \
  rm -rf \
    /tmp/*

# Copy compiled binaries from builder stages
COPY --from=go-builder /src/amneziawg-go /usr/bin/
COPY --from=tools-builder /tools-install/usr/bin/awg /usr/bin/
COPY --from=tools-builder /tools-install/usr/bin/awg-quick /usr/bin/

# Create symlinks for WireGuard compatibility
RUN \
  ln -sf /usr/bin/awg /usr/bin/wg && \
  ln -sf /usr/bin/awg-quick /usr/bin/wg-quick && \
  chmod +x /usr/bin/awg /usr/bin/awg-quick /usr/bin/amneziawg-go

# Apply awg-quick sysctl patch to avoid errors when sysctl is already set
RUN sed -i 's|\[\[ $proto == -4 \]\] && cmd sysctl -q net\.ipv4\.conf\.all\.src_valid_mark=1|[[ $proto == -4 ]] \&\& [[ $(sysctl -n net.ipv4.conf.all.src_valid_mark) != 1 ]] \&\& cmd sysctl -q net.ipv4.conf.all.src_valid_mark=1|' /usr/bin/awg-quick

# Create symlink for /etc/wireguard -> /config/wg_confs
RUN \
  rm -rf /etc/wireguard && \
  ln -s /config/wg_confs /etc/wireguard

# write build version info
RUN \
  printf "AmneziaWG version: ${VERSION}\nBuild-date: ${BUILD_DATE}\namneziawg-tools: ${AMNEZIAWG_TOOLS_VERSION}\namneziawg-go: ${AMNEZIAWG_GO_VERSION}\n" > /build_version

# add local files
COPY /root /

# ports and volumes
EXPOSE 51820/udp
