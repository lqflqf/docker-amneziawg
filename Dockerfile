# syntax=docker/dockerfile:1

# Dockerfile for AmneziaWG with LinuxServer.io architecture
# Multi-stage build: compile amneziawg-go, awg-tools, then create runtime image

# Upstream version defaults — override via --build-arg or CI. Each tag is
# pinned to the commit it pointed at when it was reviewed: a moved tag fails the
# build instead of shipping different code. An empty *_COMMIT (ad-hoc builds
# with a version override) skips that check.
ARG AMNEZIAWG_GO_VERSION=v3.1.20260828
ARG AMNEZIAWG_GO_COMMIT=b5928efb6ca19f0153958460c3d141f04abc5c2e
ARG AMNEZIAWG_TOOLS_VERSION=v3.1.20260812
ARG AMNEZIAWG_TOOLS_COMMIT=ee0f0a9aa34ff0a0da4b3433b9512781cfe02843

# ============================================================================
# Stage 1: Compile amneziawg-go
# ============================================================================
# Base images are pinned by digest (Dependabot keeps them current).
FROM golang:1.26.8-alpine@sha256:8ac98ca534ac3f51e1f420a1dd2c15e74c75cfa0f23f3ad27eb5d7236c349a0c AS go-builder

ARG AMNEZIAWG_GO_VERSION
ARG AMNEZIAWG_GO_COMMIT
RUN apk add --no-cache git=2.54.0-r0 build-base=0.5-r4

WORKDIR /src
RUN git clone --branch ${AMNEZIAWG_GO_VERSION} --depth 1 https://github.com/amnezia-vpn/amneziawg-go.git . && \
  HEAD_COMMIT=$(git rev-parse HEAD) && \
  if [ -n "${AMNEZIAWG_GO_COMMIT}" ] && [ "${HEAD_COMMIT}" != "${AMNEZIAWG_GO_COMMIT}" ]; then \
    echo "amneziawg-go ${AMNEZIAWG_GO_VERSION} is ${HEAD_COMMIT}, expected ${AMNEZIAWG_GO_COMMIT}" >&2; exit 1; \
  fi
RUN CGO_ENABLED=1 go build -ldflags '-linkmode external -extldflags "-fno-PIC -static"' -v -o amneziawg-go

# ============================================================================
# Stage 2: Compile awg-tools from source
# ============================================================================
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS tools-builder

ARG AMNEZIAWG_TOOLS_VERSION
ARG AMNEZIAWG_TOOLS_COMMIT
RUN apk add --no-cache git=2.54.0-r0 build-base=0.5-r4 linux-headers=7.0.0-r1 bash=5.3.9-r1

WORKDIR /src
RUN git clone --branch ${AMNEZIAWG_TOOLS_VERSION} --depth 1 https://github.com/amnezia-vpn/amneziawg-tools.git . && \
  HEAD_COMMIT=$(git rev-parse HEAD) && \
  if [ -n "${AMNEZIAWG_TOOLS_COMMIT}" ] && [ "${HEAD_COMMIT}" != "${AMNEZIAWG_TOOLS_COMMIT}" ]; then \
    echo "amneziawg-tools ${AMNEZIAWG_TOOLS_VERSION} is ${HEAD_COMMIT}, expected ${AMNEZIAWG_TOOLS_COMMIT}" >&2; exit 1; \
  fi
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
FROM ghcr.io/linuxserver/baseimage-alpine:3.24@sha256:e4772029b98af17b6670341d07cbd54138a3dc7f6323af1ef76bbc02fd0a813d

# set version label
ARG BUILD_DATE
ARG VERSION
ARG AMNEZIAWG_GO_VERSION
ARG AMNEZIAWG_GO_COMMIT
ARG AMNEZIAWG_TOOLS_VERSION
ARG AMNEZIAWG_TOOLS_COMMIT
LABEL build_version="AmneziaWG version:- ${VERSION} Build-date:- ${BUILD_DATE}"
# Override the labels inherited from the LinuxServer base image (maintainer
# included, or it reads as a LinuxServer maintainer). CI's
# metadata-action also sets source/title/url/revision; these cover local builds.
LABEL org.opencontainers.image.title="docker-amneziawg"
LABEL maintainer="lqflqf"
LABEL org.opencontainers.image.authors="lqflqf"
LABEL org.opencontainers.image.vendor="lqflqf"
LABEL org.opencontainers.image.source="https://github.com/lqflqf/docker-amneziawg"
LABEL org.opencontainers.image.url="https://github.com/lqflqf/docker-amneziawg"
LABEL org.opencontainers.image.documentation="https://github.com/lqflqf/docker-amneziawg#readme"
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

# Apply awg-quick sysctl patch to avoid errors when sysctl is already set.
# sed succeeds on zero matches, so check the patch took: an upstream change to
# that line must fail the build rather than silently drop the fix.
RUN sed -i 's|\[\[ $proto == -4 \]\] && cmd sysctl -q net\.ipv4\.conf\.all\.src_valid_mark=1|[[ $proto == -4 ]] \&\& [[ $(sysctl -n net.ipv4.conf.all.src_valid_mark) != 1 ]] \&\& cmd sysctl -q net.ipv4.conf.all.src_valid_mark=1|' /usr/bin/awg-quick && \
  grep -qF '[[ $(sysctl -n net.ipv4.conf.all.src_valid_mark) != 1 ]]' /usr/bin/awg-quick || \
  { echo "awg-quick src_valid_mark patch did not apply" >&2; exit 1; }

# Create symlink for /etc/wireguard -> /config/wg_confs
RUN \
  rm -rf /etc/wireguard && \
  ln -s /config/wg_confs /etc/wireguard

# write build version info
RUN \
  printf "AmneziaWG version: ${VERSION}\nBuild-date: ${BUILD_DATE}\namneziawg-tools: ${AMNEZIAWG_TOOLS_VERSION} (${AMNEZIAWG_TOOLS_COMMIT:-unpinned})\namneziawg-go: ${AMNEZIAWG_GO_VERSION} (${AMNEZIAWG_GO_COMMIT:-unpinned})\n" > /build_version

# add local files
COPY /root /

# Healthy only while every tunnel in /config/wg_confs is up. Docker does not
# restart unhealthy containers by itself; this is a signal for monitoring.
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
  CMD ["/app/healthcheck"]

# ports and volumes
EXPOSE 51820/udp
