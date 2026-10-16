"""Object storage for the TESS vetting job: MinIO in the cluster, a directory for local runs.

Both stores use the same keys, so a local run writes the layout the Go reader expects.
"""

from __future__ import annotations

import io
import json
import os
import shutil
import tempfile
from datetime import datetime, timezone
from pathlib import Path

import zstandard

ZSTD_LEVEL = 10


def compress_file(src: str, dst: str, level: int = ZSTD_LEVEL) -> None:
    with open(src, "rb") as fi, open(dst, "wb") as fo:
        zstandard.ZstdCompressor(level=level, threads=-1).copy_stream(fi, fo)


def decompress_file(src: str, dst: str) -> None:
    with open(src, "rb") as fi, open(dst, "wb") as fo:
        zstandard.ZstdDecompressor().copy_stream(fi, fo)


def zst(data: bytes, level: int = ZSTD_LEVEL) -> bytes:
    return zstandard.ZstdCompressor(level=level, threads=-1).compress(data)


def unzst(data: bytes) -> bytes:
    return zstandard.ZstdDecompressor().decompressobj().decompress(data)


class Base:
    def put(self, key: str, data: bytes, content_type: str = "application/octet-stream") -> None:
        raise NotImplementedError

    def get(self, key: str) -> bytes:
        raise NotImplementedError

    def put_file(self, key: str, path: str, content_type: str = "application/octet-stream") -> None:
        raise NotImplementedError

    def get_file(self, key: str, path: str) -> None:
        raise NotImplementedError

    def exists(self, key: str) -> bool:
        raise NotImplementedError

    def list(self, prefix: str) -> list[tuple[str, datetime]]:
        """Every key under prefix with its modification time."""
        raise NotImplementedError

    def delete(self, key: str) -> None:
        raise NotImplementedError

    def keys(self, prefix: str) -> list[str]:
        return [k for k, _ in self.list(prefix)]

    def put_json(self, key: str, doc) -> None:
        self.put(key, json.dumps(doc, indent=1).encode(), "application/json")

    def get_json(self, key: str):
        return json.loads(self.get(key))

    def put_zst(self, key: str, data: bytes) -> None:
        self.put(key, zst(data), "application/zstd")

    def get_zst(self, key: str) -> bytes:
        return unzst(self.get(key))

    def put_file_zst(self, key: str, path: str, scratch: str) -> None:
        """Compress a large file through scratch disk, never through memory."""
        fd, tmp = tempfile.mkstemp(dir=scratch, suffix=".zst")
        os.close(fd)
        try:
            compress_file(path, tmp)
            self.put_file(key, tmp, "application/zstd")
        finally:
            os.unlink(tmp)

    def get_file_zst(self, key: str, path: str, scratch: str) -> None:
        fd, tmp = tempfile.mkstemp(dir=scratch, suffix=".zst")
        os.close(fd)
        try:
            self.get_file(key, tmp)
            decompress_file(tmp, path)
        finally:
            os.unlink(tmp)


class S3Store(Base):
    def __init__(self, endpoint: str, access_key: str, secret_key: str, bucket: str, secure: bool = False):
        from minio import Minio

        self.client = Minio(endpoint, access_key=access_key, secret_key=secret_key, secure=secure)
        self.bucket = bucket

    @staticmethod
    def from_env() -> "S3Store":
        return S3Store(
            os.environ["S3_ENDPOINT"],
            os.environ["S3_ACCESS_KEY"],
            os.environ["S3_SECRET_KEY"],
            os.environ.get("S3_BUCKET", "astro-processed"),
            os.environ.get("S3_USE_SSL", "false").lower() == "true",
        )

    def put(self, key, data, content_type="application/octet-stream"):
        self.client.put_object(self.bucket, key, io.BytesIO(data), len(data), content_type=content_type)

    def get(self, key):
        resp = self.client.get_object(self.bucket, key)
        try:
            return resp.read()
        finally:
            resp.close()
            resp.release_conn()

    def put_file(self, key, path, content_type="application/octet-stream"):
        self.client.fput_object(self.bucket, key, path, content_type=content_type)

    def get_file(self, key, path):
        self.client.fget_object(self.bucket, key, path)

    def exists(self, key):
        from minio.error import S3Error

        try:
            self.client.stat_object(self.bucket, key)
            return True
        except S3Error as e:
            if e.code in ("NoSuchKey", "NoSuchObject", "NotFound"):
                return False
            raise

    def list(self, prefix):
        return [(o.object_name, o.last_modified) for o in
                self.client.list_objects(self.bucket, prefix=prefix, recursive=True)]

    def delete(self, key):
        self.client.remove_object(self.bucket, key)


class LocalStore(Base):
    def __init__(self, root: str):
        self.root = Path(root)
        self.root.mkdir(parents=True, exist_ok=True)

    def _path(self, key: str) -> Path:
        p = (self.root / key).resolve()
        if self.root.resolve() not in p.parents:
            raise ValueError(f"key outside the store: {key}")
        return p

    def put(self, key, data, content_type="application/octet-stream"):
        p = self._path(key)
        p.parent.mkdir(parents=True, exist_ok=True)
        tmp = p.with_name(p.name + ".part")
        tmp.write_bytes(data)
        os.replace(tmp, p)

    def get(self, key):
        p = self._path(key)
        if not p.is_file():
            raise KeyError(key)
        return p.read_bytes()

    def put_file(self, key, path, content_type="application/octet-stream"):
        p = self._path(key)
        p.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(path, p)

    def get_file(self, key, path):
        p = self._path(key)
        if not p.is_file():
            raise KeyError(key)
        shutil.copyfile(p, path)

    def exists(self, key):
        return self._path(key).is_file()

    def list(self, prefix):
        out = []
        for p in self.root.rglob("*"):
            if p.is_file() and not p.name.endswith(".part"):
                k = p.relative_to(self.root).as_posix()
                if k.startswith(prefix):
                    out.append((k, datetime.fromtimestamp(p.stat().st_mtime, timezone.utc)))
        return sorted(out)

    def delete(self, key):
        self._path(key).unlink(missing_ok=True)
