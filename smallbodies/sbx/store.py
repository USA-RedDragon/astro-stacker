"""The mirror's S3 bucket (MinIO) and its layout.

ephemeris/<file>.zst            JPL planetary and perturber ephemerides
sources/<date>/<file>.zst       each day's MPC and JPL downloads, as fetched
catalog/<date>/catalog.npz.zst  the merged catalogue built from them
grid/<date>/meta.json           the grid epochs integrated from that catalogue
grid/<date>/<epoch>.zst         one file per grid epoch
grid/latest.json                the newest complete grid
"""

from __future__ import annotations

import io
import json
import os

import zstandard
from minio import Minio
from minio.deleteobjects import DeleteObject


class Store:
    def __init__(self, endpoint: str, access_key: str, secret_key: str, bucket: str, secure: bool = False):
        self.client = Minio(endpoint, access_key=access_key, secret_key=secret_key, secure=secure)
        self.bucket = bucket

    @staticmethod
    def from_env() -> "Store":
        return Store(
            os.environ["S3_ENDPOINT"],
            os.environ["S3_ACCESS_KEY"],
            os.environ["S3_SECRET_KEY"],
            os.environ.get("S3_BUCKET", "small-bodies"),
            os.environ.get("S3_USE_SSL", "false") == "true",
        )

    def put(self, key: str, data: bytes, content_type: str = "application/octet-stream") -> None:
        self.client.put_object(self.bucket, key, io.BytesIO(data), len(data), content_type=content_type)

    def put_file(self, key: str, path: str, content_type: str = "application/octet-stream") -> None:
        self.client.fput_object(self.bucket, key, path, content_type=content_type)

    def get(self, key: str) -> bytes:
        resp = self.client.get_object(self.bucket, key)
        try:
            return resp.read()
        finally:
            resp.close()
            resp.release_conn()

    def get_file(self, key: str, path: str) -> None:
        self.client.fget_object(self.bucket, key, path)

    def exists(self, key: str) -> bool:
        try:
            self.client.stat_object(self.bucket, key)
            return True
        except Exception:
            return False

    def put_json(self, key: str, doc) -> None:
        self.put(key, json.dumps(doc).encode(), "application/json")

    def get_json(self, key: str):
        return json.loads(self.get(key))

    def put_zst(self, key: str, data: bytes, level: int = 19) -> None:
        self.put(key, zstandard.ZstdCompressor(level=level, threads=-1).compress(data), "application/zstd")

    def get_zst(self, key: str) -> bytes:
        return zstandard.ZstdDecompressor().decompressobj().decompress(self.get(key))

    def prefixes(self, prefix: str) -> list[str]:
        return sorted(o.object_name for o in self.client.list_objects(self.bucket, prefix=prefix) if o.is_dir)

    def keys(self, prefix: str) -> list[str]:
        return [o.object_name for o in self.client.list_objects(self.bucket, prefix=prefix, recursive=True)]

    def delete_prefix(self, prefix: str) -> int:
        objs = [DeleteObject(k) for k in self.keys(prefix)]
        errors = list(self.client.remove_objects(self.bucket, objs))
        if errors:
            raise RuntimeError(f"deleting {prefix}: {errors[0]}")
        return len(objs)
