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

# GraXpert's AI background extraction model (CC BY-NC-SA 4.0), which the
# covers run with onnxruntime to take the sky's gradients off. Set
# GRAXPERT_BGE_URL to where model.onnx of version 1.0.1 can be fetched;
# without it the image has no model and covers wait for one.
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS bgemodel

ARG GRAXPERT_BGE_URL=
ARG GRAXPERT_BGE_SHA256=4ed3ef68c9f44e821ea4e47572770bb9d4d5df447feae3e758d4c0480fca1f03

RUN mkdir -p /out \
    && if [ -n "${GRAXPERT_BGE_URL}" ]; then \
        apk add --no-cache curl \
        && curl -fsSLo /out/graxpert-bge-1.0.1.onnx "${GRAXPERT_BGE_URL}" \
        && echo "${GRAXPERT_BGE_SHA256}  /out/graxpert-bge-1.0.1.onnx" | sha256sum -c -; \
    fi

# Siril does the calibration and registration: 1.4 built from source on
# Alpine (github.com/USA-RedDragon/dockers, images/siril). It's x86_64 only,
# so this image is amd64 only. The base sets HOME and the XDG dirs to /tmp,
# which the pod mounts writable over a read-only root filesystem.
FROM ghcr.io/usa-reddragon/siril:1.4.4@sha256:7863b17b64d8c0e300d9ee3bf6cd9160efbccfca8495bff3d585372ce3e9dbe4
COPY --from=astrometry /out/runtime-deps /tmp/runtime-deps
RUN apk add --no-cache $(cat /tmp/runtime-deps) python3 py3-numpy py3-onnxruntime && rm /tmp/runtime-deps
COPY --from=bgemodel /out/ /usr/share/astro-stacker/
COPY --from=astrometry /out/usr/bin /usr/bin
RUN solve-field --help > /dev/null
COPY astro-stacker /astro-stacker
CMD ["/astro-stacker"]
