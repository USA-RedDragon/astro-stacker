# Siril does the calibration and registration. Fedora packages the current
# 1.4 release; Debian and Ubuntu stable only have 1.2, which can't read the
# XISF files NINA writes. Siril is x86_64 only, so the image is amd64 only.
FROM docker.io/library/fedora:45@sha256:aacbc26b38361ba0cb3d10f268da40b61b36aed6ed6fe5831a35871636bedb06
RUN dnf install -y --setopt=install_weak_deps=False siril ca-certificates \
    && dnf clean all \
    && rm -rf /var/cache/dnf
# Siril writes its settings under HOME; the root filesystem is read-only in
# the cluster, so keep them in /tmp alongside the stacking work directory.
ENV HOME=/tmp XDG_CONFIG_HOME=/tmp/.config XDG_CACHE_HOME=/tmp/.cache
COPY astro-stacker /astro-stacker
CMD ["/astro-stacker"]
