FROM python:3.15-slim-bookworm@sha256:2a41267d9de9d358f77a1de356e0f16ecba8d39827855be0ce9ef4792edce402 AS build
RUN apt-get update \
    && apt-get install -y --no-install-recommends gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*
COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --prefix=/install -r /tmp/requirements.txt

FROM python:3.15-slim-bookworm@sha256:2a41267d9de9d358f77a1de356e0f16ecba8d39827855be0ce9ef4792edce402
COPY --from=build /install /usr/local
COPY sbx /app/sbx
WORKDIR /app
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
RUN python -c "import assist, rebound, erfa, minio, zstandard, sbx.server"
USER 65534:65534
ENTRYPOINT ["python", "-m", "sbx"]
CMD ["serve"]
