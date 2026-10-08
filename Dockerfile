# Siril does the calibration and registration: 1.4 built from source on
# Alpine (github.com/USA-RedDragon/dockers, images/siril). It's x86_64 only,
# so this image is amd64 only. The base sets HOME and the XDG dirs to /tmp,
# which the pod mounts writable over a read-only root filesystem.
FROM ghcr.io/usa-reddragon/siril:1.4.4@sha256:266194257a6db0e3a0b6f86e4aef375044b99b410a105b99ef25625da444105c
COPY astro-stacker /astro-stacker
CMD ["/astro-stacker"]
