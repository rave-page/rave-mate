# Reproducible Linux (amd64) build box for rave-mate. Debian 13 (trixie) glibc 2.41
# is <= Arch's, so the resulting binary runs on the `sus` sender. Base image digest +
# apt snapshot are pinned and >7 days old (supply-chain soak); the Go toolchain is the
# version go.mod requires, tarball SHA-256-verified. Same base digest + snapshot as
# spout2pw/contrib/build so the pull is shared.
FROM debian:trixie-20261027-slim@sha256:f6b08b09a05725f9431502d3b5714f5c4576e82c1a84fa1debe0c24a7e0c3ec4
ARG APT_SNAPSHOT=20261027T000000Z
RUN set -eux; \
    rm -f /etc/apt/sources.list.d/debian.sources; \
    printf 'deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/%s trixie main\n' "$APT_SNAPSHOT" > /etc/apt/sources.list; \
    printf 'deb [check-valid-until=no] http://snapshot.debian.org/archive/debian-security/%s trixie-security main\n' "$APT_SNAPSHOT" >> /etc/apt/sources.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates curl git pkg-config build-essential xz-utils \
        libgl1-mesa-dev xorg-dev libasound2-dev libwayland-dev \
        libxkbcommon-dev libxkbcommon-x11-dev libvulkan-dev libegl1-mesa-dev \
        libx11-xcb-dev libxcursor-dev libxfixes-dev \
        libpipewire-0.3-dev libgbm-dev libdrm-dev libva-dev; \
    rm -rf /var/lib/apt/lists/*

# Go toolchain: the exact version go.mod requires, tarball SHA-256-pinned.
ARG GO_VERSION=1.26.6
ARG GO_SHA256=708effb774be8237570d0add163225abbdfaf4fca28b2611df167beba4feef89
RUN set -eux; cd /tmp; \
    curl -fsSLO "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"; \
    echo "${GO_SHA256}  go${GO_VERSION}.linux-amd64.tar.gz" | sha256sum -c -; \
    tar -C /usr/local -xzf "go${GO_VERSION}.linux-amd64.tar.gz"; \
    rm -f "go${GO_VERSION}.linux-amd64.tar.gz"
# Zig toolchain: builds native/zigmedia (realtime TX C ABI, libravemedia.a). Tarball SHA-256-pinned.
ARG ZIG_VERSION=0.16.0
ARG ZIG_SHA256=70e49664a74374b48b51e6f3fdfbf437f6395d42509050588bd49abe52ba3d00
RUN set -eux; cd /tmp; \
    curl -fsSLO "https://ziglang.org/download/${ZIG_VERSION}/zig-x86_64-linux-${ZIG_VERSION}.tar.xz"; \
    echo "${ZIG_SHA256}  zig-x86_64-linux-${ZIG_VERSION}.tar.xz" | sha256sum -c -; \
    tar -C /usr/local -xJf "zig-x86_64-linux-${ZIG_VERSION}.tar.xz"; \
    ln -s "/usr/local/zig-x86_64-linux-${ZIG_VERSION}/zig" /usr/local/bin/zig; \
    rm -f "zig-x86_64-linux-${ZIG_VERSION}.tar.xz"; \
    zig version

ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH \
    GOTOOLCHAIN=local \
    GOFLAGS=-mod=readonly \
    GOWORK=off \
    CGO_ENABLED=1

COPY --chmod=0755 build-in-container.sh /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/build-in-container.sh"]
