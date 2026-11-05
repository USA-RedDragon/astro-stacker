FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS astrometry

ARG ASTROMETRY_VERSION=0.97
ARG ASTROMETRY_SHA256=e4eef1b658ba5ad462282b661c0ca3a5c538ba1716e853f7970b7b9fa4a33459

RUN apk add --no-cache \
        build-base \
        bsd-compat-headers \
        bzip2-dev \
        cfitsio-dev \
        curl \
        gsl-dev \
        libjpeg-turbo-dev \
        libpng-dev \
        pax-utils \
        wcslib-dev \
        zlib-dev

WORKDIR /src
RUN curl -fsSLo /tmp/astrometry.tar.gz "https://github.com/dstndstn/astrometry.net/releases/download/${ASTROMETRY_VERSION}/astrometry.net-${ASTROMETRY_VERSION}.tar.gz" \
    && echo "${ASTROMETRY_SHA256}  /tmp/astrometry.tar.gz" | sha256sum -c - \
    && tar xzf /tmp/astrometry.tar.gz --strip-components=1

# ARCH_FLAGS empty: the default -march=native would tie the binaries to the
# build machine's CPU.
RUN make -j"$(nproc)" ARCH_FLAGS= SYSTEM_GSL=yes GSL_INC= GSL_LIB="-lgsl -lgslcblas" \
    && mkdir -p /out/usr/bin \
    && cp solver/solve-field solver/astrometry-engine /out/usr/bin/ \
    && strip /out/usr/bin/* \
    && scanelf --needed --nobanner --format '%n#p' /out/usr/bin/* \
        | tr ',' '\n' | sort -u | sed 's/^/so:/' > /out/runtime-deps

# Siril does the calibration and registration: 1.4 built from source on
# Alpine (github.com/USA-RedDragon/dockers, images/siril). It's x86_64 only,
# so this image is amd64 only. The base sets HOME and the XDG dirs to /tmp,
# which the pod mounts writable over a read-only root filesystem.
FROM ghcr.io/usa-reddragon/siril:1.4.4@sha256:b4cca04ccbff250bb76323b22139bddba79a33602a0906505115a29563133dfd
COPY --from=astrometry /out/runtime-deps /tmp/runtime-deps
RUN apk add --no-cache $(cat /tmp/runtime-deps) python3 py3-numpy py3-onnxruntime && rm /tmp/runtime-deps
COPY --from=astrometry /out/usr/bin /usr/bin
RUN solve-field --help > /dev/null && python3 -c "import numpy, onnxruntime"
COPY astro-stacker /astro-stacker
CMD ["/astro-stacker"]
