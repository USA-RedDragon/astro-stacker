FROM alpine:3.24.4@sha256:93b41ef40b1677fce8fcc64ff4af09daddfacf5040afde618bc46c1e2d1cc7d4 AS astrometry

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
FROM ghcr.io/usa-reddragon/siril:1.4.5@sha256:e8ae8edb3253c7ef36200ea7052a29eb14362c8081383f5ee166dd2ca33587f3
COPY --from=astrometry /out/runtime-deps /tmp/runtime-deps
RUN apk add --no-cache $(cat /tmp/runtime-deps) python3 py3-numpy py3-onnxruntime && rm /tmp/runtime-deps
COPY --from=astrometry /out/usr/bin /usr/bin
RUN solve-field --help > /dev/null && python3 -c "import numpy, onnxruntime"
COPY astro-stacker /astro-stacker
CMD ["/astro-stacker"]
