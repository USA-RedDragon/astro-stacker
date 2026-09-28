# Siril does the calibration and registration: 1.4 built from source on
# Alpine (github.com/USA-RedDragon/dockers, images/siril). It's x86_64 only,
# so this image is amd64 only. The base sets HOME and the XDG dirs to /tmp,
# which the pod mounts writable over a read-only root filesystem.
FROM ghcr.io/usa-reddragon/siril:1.4.4@sha256:b1afaf6b4bffa570a3d5599bd31f1d8b2459b290e76d48ea82c5a2ac1205c3c1
COPY astro-stacker /astro-stacker
CMD ["/astro-stacker"]
