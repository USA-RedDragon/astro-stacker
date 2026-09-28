# Siril does the calibration and registration. Fedora packages the current
# 1.4 release; Debian and Ubuntu stable only have 1.2, which can't read the
# XISF files NINA writes. Siril is x86_64 only, so the image is amd64 only.
FROM docker.io/library/fedora:46@sha256:a9bab18d01cf2c2cf62f3e79c72623405bce14ac062995cc6651a3073c802e41
RUN dnf install -y --setopt=install_weak_deps=False siril ca-certificates \
    && dnf clean all \
    && rm -rf /var/cache/dnf
# Siril writes its settings under HOME; the root filesystem is read-only in
# the cluster, so keep them in /tmp alongside the stacking work directory.
ENV HOME=/tmp XDG_CONFIG_HOME=/tmp/.config XDG_CACHE_HOME=/tmp/.cache
COPY astro-stacker /astro-stacker
CMD ["/astro-stacker"]
