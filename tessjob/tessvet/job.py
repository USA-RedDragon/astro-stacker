"""The queue loop: one pass over vetting/tess/requests, oldest first, then exit.

Layout under vetting/cache/<slug>/tess/:
  <gaia_id>/status.json                     progress, read by the Go side
  <gaia_id>/s<NNNN>.csv.zst                 one light curve per done sector
  lc/s<NNNN>/<gaia_id>.csv.zst              every light curve fetched or fitted in the field
  cutouts/s<NNNN>/<ra><dec>/meta.json       cutout centre, WCS, tglc products; written last
  cutouts/s<NNNN>/<ra><dec>/cutout.fits.zst the TESScut FFI cutout
  cutouts/s<NNNN>/<ra><dec>/catalog.pkl.zst tglc Source state without pixels (Gaia DR3, TIC)
  cutouts/s<NNNN>/<ra><dec>/epsf.npz.zst    the fitted ePSF per cadence
"""

from __future__ import annotations

import json
import logging
import os
import re
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone

from . import lightcurve, mast
from .store import Base

log = logging.getLogger(__name__)

REQUESTS = "vetting/tess/requests/"
CUTOUT_SIZE = 90
LIMIT_MAG = 16.0
EPSF_FACTOR = 2
EDGE_MARGIN = 10
NEIGHBOUR_HLSP_ARCSEC = 42.0
MAX_ATTEMPTS = 3
SLUG = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
GAIA = re.compile(r"^[0-9]{1,20}$")


class Permanent(Exception):
    pass


@dataclass
class Config:
    workdir: str = "/work"
    max_minutes: float = 300.0
    field_mag: float | None = LIMIT_MAG
    threads: int = 4
    max_attempts: int = MAX_ATTEMPTS


@dataclass
class Request:
    key: str
    gaia_id: str
    ra: float
    dec: float
    slug: str
    neighbours: list = field(default_factory=list)
    attempts: dict = field(default_factory=dict)
    raw: dict = field(default_factory=dict)

    @staticmethod
    def parse(key: str, doc: dict) -> "Request":
        gid = str(doc["gaia_id"]).strip()
        slug = str(doc["field"]["slug"])
        if not GAIA.match(gid):
            raise ValueError(f"bad gaia_id {gid!r}")
        if not SLUG.match(slug):
            raise ValueError(f"bad field slug {slug!r}")
        nb = [n for n in doc.get("neighbours") or [] if GAIA.match(str(n.get("gaia_id", "")))]
        return Request(key, gid, float(doc["ra"]), float(doc["dec"]), slug, nb,
                       dict(doc.get("attempts") or {}), doc)


def now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _when(doc: dict, mtime: datetime) -> datetime:
    try:
        t = datetime.fromisoformat(str(doc.get("requested_at")).replace("Z", "+00:00"))
        return t if t.tzinfo else t.replace(tzinfo=timezone.utc)
    except ValueError:
        return mtime


class Job:
    def __init__(self, store: Base, client: mast.Client, cfg: Config, runner=None):
        self.store = store
        self.mast = client
        self.cfg = cfg
        self.runner = runner or self._run_tglc
        self.deadline = time.monotonic() + cfg.max_minutes * 60
        os.makedirs(cfg.workdir, exist_ok=True)

    def out_of_time(self) -> bool:
        return time.monotonic() >= self.deadline

    def queue(self) -> list[tuple[str, dict]]:
        items = []
        for key, mtime in self.store.list(REQUESTS):
            if not key.endswith(".json"):
                continue
            try:
                doc = self.store.get_json(key)
            except (ValueError, KeyError) as e:
                log.error("unreadable request %s: %s", key, e)
                continue
            items.append((_when(doc, mtime), key, doc))
        items.sort(key=lambda x: (x[0], x[1]))
        return [(k, d) for _, k, d in items]

    def run(self) -> int:
        n = 0
        for key, doc in self.queue():
            if self.out_of_time():
                log.info("time budget used; %s waits for the next run", key)
                break
            try:
                req = Request.parse(key, doc)
            except (KeyError, TypeError, ValueError) as e:
                log.error("dropping malformed request %s: %s", key, e)
                self.store.delete(key)
                continue
            self.process(req)
            n += 1
        return n

    def base(self, req: Request) -> str:
        return f"vetting/cache/{req.slug}/tess"

    def process(self, req: Request) -> None:
        base = self.base(req)
        status_key = f"{base}/{req.gaia_id}/status.json"
        status = {"gaia_id": req.gaia_id, "sectors_available": [], "sectors_done": [], "sectors_failed": {},
                  "updated_at": now()}
        if self.store.exists(status_key):
            old = self.store.get_json(status_key)
            status["sectors_done"] = sorted(int(s) for s in old.get("sectors_done", []))
            status["sectors_failed"] = {str(k): v for k, v in (old.get("sectors_failed") or {}).items()}
        try:
            sectors = self.mast.sectors(req.ra, req.dec)
        except mast.Transient as e:
            log.warning("%s: sector lookup failed, retry next run: %s", req.gaia_id, e)
            return
        status["sectors_available"] = [s["sector"] for s in sectors]
        self._status(status_key, status)
        log.info("%s (%s): sectors %s", req.gaia_id, req.slug, status["sectors_available"])
        for s in sectors:
            n = s["sector"]
            if n in status["sectors_done"] or str(n) in status["sectors_failed"]:
                continue
            if self.out_of_time():
                log.info("time budget used during %s", req.gaia_id)
                return
            t0 = time.time()
            try:
                how = self.sector(req, s)
                status["sectors_done"] = sorted(set(status["sectors_done"]) | {n})
                log.info("%s s%04d done via %s in %.0f s", req.gaia_id, n, how, time.time() - t0)
            except Permanent as e:
                status["sectors_failed"][str(n)] = str(e)
                log.warning("%s s%04d failed: %s", req.gaia_id, n, e)
            except Exception as e:
                tries = int(req.attempts.get(str(n), 0)) + 1
                req.attempts[str(n)] = tries
                log.warning("%s s%04d attempt %d failed: %s", req.gaia_id, n, tries, e)
                if tries >= self.cfg.max_attempts:
                    status["sectors_failed"][str(n)] = f"gave up after {tries} runs: {e}"[:300]
                else:
                    self.store.put_json(req.key, dict(req.raw, attempts=req.attempts))
            self._status(status_key, status)
        finished = set(status["sectors_done"]) | {int(k) for k in status["sectors_failed"]}
        if finished >= set(status["sectors_available"]):
            self.store.delete(req.key)
            log.info("%s complete; request removed", req.gaia_id)

    def _status(self, key: str, status: dict) -> None:
        status["updated_at"] = now()
        self.store.put_json(key, status)

    def sector(self, req: Request, s: dict) -> str:
        n = s["sector"]
        base = self.base(req)
        lc_key = f"{base}/lc/s{n:04d}/{req.gaia_id}.csv.zst"
        out_key = f"{base}/{req.gaia_id}/s{n:04d}.csv.zst"
        if self.store.exists(lc_key):
            self.store.put(out_key, self.store.get(lc_key), "application/zstd")
            return "field cache"
        if n <= mast.HLSP_MAX_SECTOR and self._hlsp(req, s, req.gaia_id, lc_key):
            self.store.put(out_key, self.store.get(lc_key), "application/zstd")
            for nb in req.neighbours:
                if float(nb.get("sep_arcsec", 1e9)) <= NEIGHBOUR_HLSP_ARCSEC:
                    nk = f"{base}/lc/s{n:04d}/{nb['gaia_id']}.csv.zst"
                    if not self.store.exists(nk):
                        self._hlsp(req, s, str(nb["gaia_id"]), nk)
            return "TGLC HLSP"
        self._tglc(req, s)
        if not self.store.exists(lc_key):
            raise Permanent("tglc produced no light curve for the star")
        self.store.put(out_key, self.store.get(lc_key), "application/zstd")
        return "tglc"

    def _hlsp(self, req: Request, s: dict, gid: str, lc_key: str) -> bool:
        path = os.path.join(self.cfg.workdir, f"hlsp-{gid}-s{s['sector']:04d}.fits")
        try:
            self.mast.hlsp(gid, s["sector"], s["camera"], s["ccd"], path)
            csv, meta = lightcurve.from_fits(path)
        except mast.NotFound:
            return False
        finally:
            if os.path.exists(path):
                os.unlink(path)
        self.store.put_zst(lc_key, csv)
        log.info("HLSP %s s%04d: %d rows, %d good", gid, s["sector"], meta["rows"], meta["good"])
        return True

    def find_cutout(self, req: Request, n: int) -> str | None:
        """The prefix of a cached cutout of sector n with the star at least EDGE_MARGIN pixels inside."""
        from astropy.io.fits import Header
        from astropy.wcs import WCS

        prefix = f"{self.base(req)}/cutouts/s{n:04d}/"
        for key in sorted(k for k in self.store.keys(prefix) if k.endswith("/meta.json")):
            meta = self.store.get_json(key)
            w = WCS(Header.fromstring(meta["wcs"]))
            x, y = w.all_world2pix([[req.ra, req.dec]], 0)[0]
            size = int(meta["size"])
            if EDGE_MARGIN <= x <= size - 1 - EDGE_MARGIN and EDGE_MARGIN <= y <= size - 1 - EDGE_MARGIN:
                return key[: -len("meta.json")]
        return None

    def _tglc(self, req: Request, s: dict) -> None:
        n = s["sector"]
        base = self.base(req)
        work = os.path.join(self.cfg.workdir, f"tglc-{req.gaia_id}-s{n:04d}")
        shutil.rmtree(work, ignore_errors=True)
        os.makedirs(work)
        try:
            names = [req.gaia_id] + [str(nb["gaia_id"]) for nb in req.neighbours
                                     if not self.store.exists(f"{base}/lc/s{n:04d}/{nb['gaia_id']}.csv.zst")]
            cutout = os.path.join(work, "cutout.fits")
            catalog = os.path.join(work, "catalog.pkl")
            spec = {"workdir": work, "cutout": cutout, "catalog": catalog, "sector": n, "size": CUTOUT_SIZE,
                    "limit_mag": LIMIT_MAG, "factor": EPSF_FACTOR, "names": names}
            prefix = self.find_cutout(req, n)
            if prefix:
                meta = self.store.get_json(prefix + "meta.json")
                log.info("s%04d: reusing cached cutout %s", n, prefix)
                self.store.get_file_zst(prefix + "cutout.fits.zst", cutout, work)
                with open(catalog, "wb") as f:
                    f.write(self.store.get_zst(prefix + "catalog.pkl.zst"))
                epsf_in = os.path.join(work, "epsf-in.npz")
                with open(epsf_in, "wb") as f:
                    f.write(self.store.get_zst(prefix + "epsf.npz.zst"))
                spec.update(reuse=True, epsf_in=epsf_in, epsf_name=meta["epsf_name"], field_mag=None,
                            center=meta["center"])
            else:
                log.info("s%04d: downloading a %dx%d cutout around %s", n, CUTOUT_SIZE, CUTOUT_SIZE, req.gaia_id)
                t0 = time.time()
                try:
                    self.mast.cutout(req.ra, req.dec, CUTOUT_SIZE, n, cutout, work)
                except mast.NotFound as e:
                    raise Permanent(f"TESScut has no SPOC cutout for sector {n}") from e
                log.info("s%04d: cutout %.0f MB in %.0f s", n, os.path.getsize(cutout) / 1e6, time.time() - t0)
                spec.update(reuse=False, field_mag=self.cfg.field_mag, center=[req.ra, req.dec])
            res = self.runner(spec)
            if not res.get("ok"):
                if res.get("permanent"):
                    raise Permanent(f"tglc: {res.get('error')}")
                raise RuntimeError(f"tglc: {res.get('error')}")
            for gid, path in sorted(res["lcs"].items()):
                csv, _ = lightcurve.from_fits(path)
                self.store.put_zst(f"{base}/lc/s{n:04d}/{gid}.csv.zst", csv)
            log.info("s%04d: %d light curves cached for the field", n, len(res["lcs"]))
            if req.gaia_id in res.get("errors", {}):
                raise Permanent(f"tglc: {res['errors'][req.gaia_id]}")
            if not spec["reuse"]:
                self._save_cutout(base, n, spec, res)
        finally:
            shutil.rmtree(work, ignore_errors=True)

    def _save_cutout(self, base: str, n: int, spec: dict, res: dict) -> None:
        from astropy.io import fits

        ra, dec = spec["center"]
        prefix = f"{base}/cutouts/s{n:04d}/{ra:.5f}{dec:+.5f}/"
        with fits.open(spec["cutout"], memmap=True) as h:
            wcs = h[2].header.tostring()
        if not res.get("epsf"):
            log.warning("s%04d: no ePSF file to cache", n)
            return
        self.store.put_file_zst(prefix + "cutout.fits.zst", spec["cutout"], spec["workdir"])
        with open(spec["catalog"], "rb") as f:
            self.store.put_zst(prefix + "catalog.pkl.zst", f.read())
        with open(res["epsf"], "rb") as f:
            self.store.put_zst(prefix + "epsf.npz.zst", f.read())
        self.store.put_json(prefix + "meta.json", {
            "center": [ra, dec], "size": spec["size"], "sector": n, "camera": res.get("camera"),
            "ccd": res.get("ccd"), "wcs": wcs, "epsf_name": os.path.basename(res["epsf"]),
            "limit_mag": spec["limit_mag"], "field_mag": spec["field_mag"], "factor": spec["factor"],
            "n_gaia": res.get("n_gaia"), "light_curves": len(res["lcs"]), "tglc": _tglc_version(),
            "timings": res.get("timings"), "peak_rss_mb": res.get("peak_rss_mb"), "created": now()})

    def _run_tglc(self, spec: dict) -> dict:
        path = os.path.join(spec["workdir"], "spec.json")
        with open(path, "w") as f:
            json.dump(spec, f)
        env = dict(os.environ)
        for k in ("OMP_NUM_THREADS", "OPENBLAS_NUM_THREADS", "MKL_NUM_THREADS", "NUMBA_NUM_THREADS"):
            env[k] = str(self.cfg.threads)
        env.setdefault("TQDM_MININTERVAL", "60")
        root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        env["PYTHONPATH"] = os.pathsep.join(p for p in (root, env.get("PYTHONPATH")) if p)
        t0 = time.time()
        p = subprocess.Popen([sys.executable, "-m", "tessvet.tglcrun", path], env=env)
        _, rc, ru = os.wait4(p.pid, 0)
        p.returncode = os.waitstatus_to_exitcode(rc)
        peak = ru.ru_maxrss / 1024
        secs = time.time() - t0
        log.info("s%04d: tglc exit %s in %.0f s, peak RSS %.0f MB", spec["sector"], p.returncode, secs, peak)
        res_path = os.path.join(spec["workdir"], "result.json")
        if not os.path.exists(res_path):
            return {"ok": False, "permanent": False, "error": f"tglc process ended with exit status {p.returncode}"}
        with open(res_path) as f:
            res = json.load(f)
        res["peak_rss_mb"] = round(peak)
        res["seconds"] = round(secs)
        return res


def _tglc_version() -> str:
    try:
        from importlib.metadata import version

        return version("tglc")
    except Exception:
        return "unknown"

