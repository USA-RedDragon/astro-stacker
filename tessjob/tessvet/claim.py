"""Leases on queue requests, so several workers on different nodes each fit a different star.

A worker owns a request while vetting/tess/claims/<request name> names it and has not expired.
Claims are taken and renewed with conditional writes: create only if absent, replace only if the
ETag is unchanged. A worker that dies stops renewing, and its claim expires after TTL seconds, so
the next worker takes the request over and resumes its sectors.
"""

from __future__ import annotations

import json
import logging
import posixpath
import threading
import time
from datetime import datetime, timezone

from .store import Base

log = logging.getLogger(__name__)

CLAIMS = "vetting/tess/claims/"
TTL = 15 * 60


def claim_key(request_key: str) -> str:
    return CLAIMS + posixpath.basename(request_key)


def _iso(t: float) -> str:
    return datetime.fromtimestamp(t, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


class Lease:
    """One held claim. lost is set when a renewal finds that another worker has taken it."""

    def __init__(self, claims: "Claims", key: str, gaia_id: str, etag: str, expires: float, claimed: float):
        self.claims = claims
        self.key = key
        self.gaia_id = gaia_id
        self.etag = etag
        self.expires = expires
        self.claimed = claimed
        self.lost = threading.Event()
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self._lock = threading.Lock()

    def renew(self) -> bool:
        """Push the expiry TTL seconds ahead. False when the claim is no longer this worker's."""
        with self._lock:
            if self.lost.is_set():
                return False
            now = self.claims.clock()
            expires = now + self.claims.ttl
            try:
                etag = self.claims.store.put_if(self.key, self.claims.doc(self.gaia_id, self.claimed, expires),
                                                self.etag)
            except Exception as e:
                log.warning("could not renew the claim on %s: %s", self.gaia_id, e)
                if now >= self.expires:
                    self.lost.set()
                return not self.lost.is_set()
            if etag is None:
                log.error("the claim on %s was taken by another worker", self.gaia_id)
                self.lost.set()
                return False
            self.etag, self.expires = etag, expires
            return True

    def start(self, every: float | None = None) -> None:
        """Renew in the background every `every` seconds (a third of the TTL by default)."""
        every = self.claims.ttl / 3 if every is None else every

        def loop():
            while not self._stop.wait(every):
                if not self.renew():
                    return

        self._thread = threading.Thread(target=loop, name=f"lease-{self.gaia_id}", daemon=True)
        self._thread.start()

    def release(self) -> None:
        """Stop renewing and delete the claim if it is still this worker's."""
        self._stop.set()
        if self._thread:
            self._thread.join()
        with self._lock:
            if self.lost.is_set():
                return
            self.lost.set()
            try:
                cur = self.claims.store.get_versioned(self.key)
                if cur is not None and cur[1] == self.etag:
                    self.claims.store.delete(self.key)
            except Exception as e:
                log.warning("could not release the claim on %s; it expires by itself: %s", self.gaia_id, e)


class Claims:
    def __init__(self, store: Base, worker: str, ttl: float = TTL, clock=time.time):
        self.store = store
        self.worker = worker
        self.ttl = ttl
        self.clock = clock

    def doc(self, gaia_id: str, claimed: float, expires: float) -> bytes:
        return json.dumps({"gaia_id": gaia_id, "worker": self.worker,
                           "claimed_at": _iso(claimed), "renewed_at": _iso(self.clock()),
                           "expires_at": _iso(expires), "expires": expires}, indent=1).encode()

    def take(self, request_key: str, gaia_id: str) -> Lease | None:
        """Claim the request, or None when a live claim by another worker holds it."""
        key = claim_key(request_key)
        now = self.clock()
        data = self.doc(gaia_id, now, now + self.ttl)
        etag = self.store.put_if(key, data, None)
        if etag is None:
            cur = self.store.get_versioned(key)
            if cur is None:
                etag = self.store.put_if(key, data, None)
            else:
                try:
                    held = json.loads(cur[0])
                    expires = float(held["expires"])
                    owner = held.get("worker")
                except (ValueError, KeyError, TypeError):
                    expires, owner = 0.0, "unknown"
                if expires > now:
                    return None
                log.info("%s: taking over the claim %s let expire at %s", gaia_id, owner, _iso(expires))
                etag = self.store.put_if(key, data, cur[1])
        if etag is None:
            return None
        return Lease(self, key, gaia_id, etag, now + self.ttl, now)
