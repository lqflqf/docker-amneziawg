# syntax=docker/dockerfile:1

# Dockerfile for AmneziaWG on Alpine Linux with s6-overlay
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
FROM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS go-builder

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
# Stage 3: Runtime image: Alpine + Alpine's s6-overlay package
# ============================================================================
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

# set version label
ARG BUILD_DATE
ARG VERSION
ARG AMNEZIAWG_GO_VERSION
ARG AMNEZIAWG_GO_COMMIT
ARG AMNEZIAWG_TOOLS_VERSION
ARG AMNEZIAWG_TOOLS_COMMIT
LABEL build_version="AmneziaWG version:- ${VERSION} Build-date:- ${BUILD_DATE}"
# CI's metadata-action also sets source/title/url/revision; these cover local
# builds.
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

# s6-overlay 3.2+ already waits forever when S6_CMD_WAIT_FOR_SERVICES_MAXTIME
# is unset; it is set to make that explicit: a limit would fail stage 2 while
# init-amneziawg-confs (IP detection) or svc-amneziawg (retries) still runs.
ENV HOME="/root" \
  TERM="xterm" \
  S6_CMD_WAIT_FOR_SERVICES_MAXTIME="0" \
  S6_VERBOSITY="1"

RUN \
  echo "**** install s6-overlay (whole stack pinned) ****" && \
  apk add --no-cache \
    execline=2.9.9.1-r0 \
    s6=2.15.0.0-r0 \
    s6-linux-init=1.2.0.1-r0 \
    s6-linux-utils=2.6.4.1-r0 \
    s6-overlay=3.2.3.0-r0 \
    s6-overlay-helpers=0.1.2.2-r0 \
    s6-portable-utils=2.3.1.2-r0 \
    s6-rc=0.6.1.1-r0 \
    skalibs-libs=2.15.0.0-r0 && \
  echo "**** install dependencies ****" && \
  apk add --no-cache \
    bash=5.3.9-r1 \
    bc=1.08.2-r1 \
    ca-certificates-bundle=20260909-r0 \
    coreutils=9.11-r0 \
    curl=8.22.0-r0 \
    dnsmasq=2.92_p2-r0 \
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
    shadow=4.18.0-r1 \
    tzdata=2026d-r0 && \
  echo "**** create abc user (PUID/PGID are applied at start) ****" && \
  groupadd -g 911 abc && \
  useradd -u 911 -g abc -G users -d /config -s /bin/false -M abc && \
  mkdir -p /app /config /defaults && \
  echo "**** cleanup ****" && \
  rm -rf \
    /tmp/*

# Copy compiled binaries from builder stages
COPY --from=go-builder /src/amneziawg-go /usr/bin/
COPY --from=tools-builder /tools-install/usr/bin/awg /usr/bin/
COPY --from=tools-builder /tools-install/usr/bin/awg-quick /usr/bin/

RUN chmod +x /usr/bin/awg /usr/bin/awg-quick /usr/bin/amneziawg-go

# Apply awg-quick sysctl patch to avoid errors when sysctl is already set.
# sed succeeds on zero matches, so check the patch took: an upstream change to
# that line must fail the build rather than silently drop the fix.
RUN sed -i 's|\[\[ $proto == -4 \]\] && cmd sysctl -q net\.ipv4\.conf\.all\.src_valid_mark=1|[[ $proto == -4 ]] \&\& [[ $(sysctl -n net.ipv4.conf.all.src_valid_mark) != 1 ]] \&\& cmd sysctl -q net.ipv4.conf.all.src_valid_mark=1|' /usr/bin/awg-quick && \
  grep -qF '[[ $(sysctl -n net.ipv4.conf.all.src_valid_mark) != 1 ]]' /usr/bin/awg-quick || \
  { echo "awg-quick src_valid_mark patch did not apply" >&2; exit 1; }

# write build version info
RUN \
  printf "AmneziaWG version: ${VERSION}\nBuild-date: ${BUILD_DATE}\namneziawg-tools: ${AMNEZIAWG_TOOLS_VERSION} (${AMNEZIAWG_TOOLS_COMMIT:-unpinned})\namneziawg-go: ${AMNEZIAWG_GO_VERSION} (${AMNEZIAWG_GO_COMMIT:-unpinned})\ns6: %s\n" \
    "$(apk info -v 2>/dev/null | grep -E '^(s6-overlay|s6|s6-rc|s6-linux-init)-[0-9]' | sort | tr '\n' ' ' | sed 's/ $//')" > /build_version

# add local files
COPY /root /

# Healthy only while every tunnel in /config/wg_confs is up. Docker does not
# restart unhealthy containers by itself; this is a signal for monitoring.
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
  CMD ["/app/healthcheck"]

# ports and volumes
EXPOSE 51820/udp

# Setting ENTRYPOINT also clears alpine's inherited CMD ["/bin/sh"]; a CMD
# would make /init stop the container when it exits.
ENTRYPOINT ["/init"]
