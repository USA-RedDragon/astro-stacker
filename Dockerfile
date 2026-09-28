FROM ubuntu:24.04@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3 AS siril
# Siril only ships an x86_64 AppImage. The checksum is of the file from the
# official download URL; Siril doesn't publish one, so update both together.
ARG SIRIL_VERSION=1.4.4
ARG SIRIL_SHA256=47f3f299f6771888516bebed076580a57f7abe76484981f387a325115547ed78
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /tmp
RUN curl -fsSL -o siril.AppImage "https://free-astro.org/download/Siril-${SIRIL_VERSION}-x86_64.AppImage" \
    && echo "${SIRIL_SHA256}  siril.AppImage" | sha256sum -c - \
    && chmod +x siril.AppImage \
    && ./siril.AppImage --appimage-extract >/dev/null \
    && mv squashfs-root /opt/siril

FROM ubuntu:24.04@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=siril /opt/siril /opt/siril
# Siril writes its settings under HOME; the root filesystem is read-only in
# the cluster, so keep them in /tmp alongside the stacking work directory.
ENV HOME=/tmp XDG_CONFIG_HOME=/tmp/.config XDG_CACHE_HOME=/tmp/.cache
COPY astro-stacker /astro-stacker
CMD ["/astro-stacker"]
