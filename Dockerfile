FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a AS astrometry

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
FROM ghcr.io/usa-reddragon/siril:1.4.4@sha256:7863b17b64d8c0e300d9ee3bf6cd9160efbccfca8495bff3d585372ce3e9dbe4
COPY --from=astrometry /out/runtime-deps /tmp/runtime-deps
RUN apk add --no-cache $(cat /tmp/runtime-deps) python3 py3-numpy py3-onnxruntime && rm /tmp/runtime-deps
COPY --from=astrometry /out/usr/bin /usr/bin
RUN solve-field --help > /dev/null && python3 -c "import numpy, onnxruntime"
COPY astro-stacker /astro-stacker
CMD ["/astro-stacker"]
