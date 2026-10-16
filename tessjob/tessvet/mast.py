"""MAST access: TESS sector coverage, TGLC HLSP light curves and TESScut FFI cutouts.

Requests go one at a time and retry server errors with backoff.
"""

from __future__ import annotations

import logging
import os
import time
import zipfile

import requests

log = logging.getLogger(__name__)

TESSCUT = "https://mast.stsci.edu/tesscut/api/v0.1"
HLSP = "https://archive.stsci.edu/hlsps/tglc"
HLSP_MAX_SECTOR = 55
RETRY = (requests.ConnectionError, requests.Timeout, requests.exceptions.ChunkedEncodingError)
USER_AGENT = "astro-tess-vetting/1 (+https://github.com/USA-RedDragon/astro-stacker)"


class NotFound(Exception):
    pass


class Transient(Exception):
    """A server or network error that may clear on a later run."""


class OutOfTime(Transient):
    """The run's time budget ended before the request finished."""


class Client:
    def __init__(self, session: requests.Session | None = None, attempts: int = 5, backoff: float = 30.0,
                 sleep=time.sleep):
        self.session = session or requests.Session()
        self.session.headers.setdefault("User-Agent", USER_AGENT)
        self.attempts = attempts
        self.backoff = backoff
        self.sleep = sleep
        self.stop_at: float | None = None
        self.clock = time.monotonic

    def remaining(self) -> float:
        return float("inf") if self.stop_at is None else self.stop_at - self.clock()

    def _check(self, url: str) -> float:
        left = self.remaining()
        if left <= 0:
            raise OutOfTime(f"{url}: time budget used")
        return left

    def _get(self, url: str, params=None, timeout=(30, 300), stream=False) -> requests.Response:
        last = None
        for attempt in range(self.attempts):
            left = self._check(url)
            t = (min(timeout[0], left), min(timeout[1], left))
            try:
                r = self.session.get(url, params=params, timeout=t, stream=stream)
                if r.status_code == 404:
                    r.close()
                    raise NotFound(url)
                if r.status_code < 500 and r.status_code != 429:
                    r.raise_for_status()
                    return r
                last = f"HTTP {r.status_code}"
                r.close()
            except RETRY as e:
                last = type(e).__name__
            if attempt + 1 < self.attempts:
                wait = self.backoff * 2 ** attempt
                if wait >= self.remaining():
                    raise OutOfTime(f"{url}: {last}; no time left to retry")
                log.warning("GET %s failed (%s); retry in %.0f s", url, last, wait)
                self.sleep(wait)
        raise Transient(f"{url}: {last} after {self.attempts} attempts")

    def sectors(self, ra: float, dec: float) -> list[dict]:
        """Sectors whose FFIs cover the position, as [{sector, camera, ccd}] sorted by sector."""
        r = self._get(f"{TESSCUT}/sector", params={"ra": f"{ra:.6f}", "dec": f"{dec:.6f}", "radius": "0m"})
        out = {}
        for row in r.json().get("results", []):
            s = int(row["sector"])
            out.setdefault(s, {"sector": s, "camera": int(row["camera"]), "ccd": int(row["ccd"])})
        return [out[s] for s in sorted(out)]

    def hlsp(self, gaia_id: str, sector: int, camera: int, ccd: int, path: str) -> None:
        """Download the TGLC HLSP light curve (DOI 10.17909/610m-9474); NotFound if MAST has none."""
        self._download(hlsp_url(gaia_id, sector, camera, ccd), path, timeout=(30, 300))

    def cutout(self, ra: float, dec: float, size: int, sector: int, path: str, scratch: str) -> None:
        """Download a size x size SPOC FFI cutout for one sector and unpack its FITS file to path."""
        zpath = path + ".zip"
        params = {"ra": f"{ra:.6f}", "dec": f"{dec:.6f}", "x": size, "y": size, "units": "px",
                  "product": "SPOC", "sector": sector}
        try:
            self._download(f"{TESSCUT}/astrocut", zpath, params=params, timeout=(30, 3600))
            with zipfile.ZipFile(zpath) as z:
                names = [n for n in z.namelist() if n.endswith(".fits")]
                if len(names) != 1:
                    raise NotFound(f"TESScut returned {len(names)} FITS files for sector {sector}")
                with z.open(names[0]) as fi, open(path, "wb") as fo:
                    while chunk := fi.read(1 << 22):
                        fo.write(chunk)
        finally:
            if os.path.exists(zpath):
                os.unlink(zpath)

    def _download(self, url, path, params=None, timeout=(30, 300), streams=3):
        tmp = path + ".part"
        for attempt in range(streams):
            r = self._get(url, params=params, timeout=timeout, stream=True)
            try:
                with open(tmp, "wb") as f:
                    for chunk in r.iter_content(1 << 22):
                        f.write(chunk)
                        self._check(url)
                os.replace(tmp, path)
                return
            except RETRY as e:
                if attempt + 1 == streams:
                    raise Transient(f"{url}: {type(e).__name__} while downloading") from e
                wait = self.backoff * 2 ** attempt
                if wait >= self.remaining():
                    raise OutOfTime(f"{url}: {type(e).__name__}; no time left to retry") from e
                log.warning("download of %s broke (%s); retry", url, type(e).__name__)
                self.sleep(wait)
            finally:
                r.close()
                if os.path.exists(tmp):
                    os.unlink(tmp)


def hlsp_url(gaia_id: str, sector: int, camera: int, ccd: int) -> str:
    p = f"00{gaia_id}"
    return (f"{HLSP}/s{sector:04d}/cam{camera}-ccd{ccd}/{p[0:4]}/{p[4:8]}/{p[8:12]}/{p[12:16]}/"
            f"hlsp_tglc_tess_ffi_gaiaid-{gaia_id}-s{sector:04d}-cam{camera}-ccd{ccd}_tess_v1_llc.fits")
