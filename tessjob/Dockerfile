FROM python:3.12-slim-bookworm@sha256:34386ef0cb081344d7ec1c103ba398e6e9f64e9ab3a1509accc92a4e24a07258 AS build
COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --only-binary=:all: --prefix=/install -r /tmp/requirements.txt

FROM python:3.12-slim-bookworm@sha256:34386ef0cb081344d7ec1c103ba398e6e9f64e9ab3a1509accc92a4e24a07258
COPY --from=build /install /usr/local
COPY tessvet /app/tessvet
WORKDIR /app
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 WORKDIR=/work HOME=/work/.cache/home \
    XDG_CACHE_HOME=/work/.cache/xdg XDG_CONFIG_HOME=/work/.cache/config MPLCONFIGDIR=/work/.cache/matplotlib \
    NUMBA_CACHE_DIR=/work/.cache/numba TMPDIR=/work/.cache/tmp
RUN HOME=/tmp MPLCONFIGDIR=/tmp/mpl NUMBA_CACHE_DIR=/tmp/numba python -c "import tglc.target_lightcurve, tglc.ffi_cut, minio, zstandard, tessvet.job, tessvet.tglcrun" && rm -rf /tmp/*
USER 65534:65534
ENTRYPOINT ["python", "-m", "tessvet"]
