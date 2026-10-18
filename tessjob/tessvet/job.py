"""The queue loop: one pass over vetting/tess/requests, oldest first, then exit.

Layout under vetting/cache/<slug>/tess/:
  <gaia_id>/status.json                     progress, read by the Go side
  <gaia_id>/s<NNNN>.csv.zst                 one light curve per done sector
  lc/s<NNNN>/<gaia_id>.csv.zst              every light curve fetched or fitted in the field
  cutouts/s<NNNN>/<ra><dec>/meta.json       cutout centre, WCS, tglc products, field_complete; written last
  cutouts/s<NNNN>/<ra><dec>/cutout.fits.zst the TESScut FFI cutout
  cutouts/s<NNNN>/<ra><dec>/catalog.pkl.zst tglc Source state without pixels (Gaia DR3, TIC)
  cutouts/s<NNNN>/<ra><dec>/epsf.npz.zst    the fitted ePSF per cadence

Several workers can run at once, one per node: each claims a request in vetting/tess/claims/ before
working on it (see claim.py), so no two workers fit the same star. Cutouts and field light curves are
shared; a worker only adds to them, and objects are written before the meta.json that points at them.
A worker leaves a star that could lie in the cutout another worker is making for now, so that the star
reuses the cutout and its ePSF later instead of fitting its own.

Time budget: no request or sector starts after max_minutes. Everything stops by stop_minutes:
network calls and tglc stop CHILD_RESERVE before it, tglc is killed KILL_RESERVE before it, and
the light curves and cutout written so far are saved, so the next run resumes the sector.
"""

from __future__ import annotations

import json
import logging
import math
import os
import re
import shutil
import socket
import subprocess
import sys
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone

from . import lightcurve, mast
from .claim import Claims, Lease
from .store import Base

log = logging.getLogger(__name__)

REQUESTS = "vetting/tess/requests/"
ATTEMPTS = "vetting/tess/attempts/"
CUTOUT_SIZE = 90
LIMIT_MAG = 16.0
EPSF_FACTOR = 2
EDGE_MARGIN = 10
NEIGHBOUR_HLSP_ARCSEC = 42.0
MAX_ATTEMPTS = 3
PIXEL_ARCSEC = 21.0
SHARED_CUTOUT_DEG = (CUTOUT_SIZE / 2 - EDGE_MARGIN) * math.sqrt(2) * PIXEL_ARCSEC / 3600
CHILD_RESERVE = 8 * 60
KILL_RESERVE = 4 * 60
SLUG = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
GAIA = re.compile(r"^[0-9]{1,20}$")


class Permanent(Exception):
    pass


class LeaseLost(Exception):
    pass


class OutOfTime(Exception):
    def __init__(self, msg: str, progress: bool = False):
        super().__init__(msg)
        self.progress = progress


@dataclass
class Config:
    workdir: str = "/work"
    max_minutes: float = 300.0
    field_mag: float | None = LIMIT_MAG
    threads: int = 4
    max_attempts: int = MAX_ATTEMPTS
    stop_minutes: float | None = None


@dataclass
class Request:
    key: str
    gaia_id: str
    ra: float
    dec: float
    slug: str
    neighbours: list = field(default_factory=list)

    @staticmethod
    def parse(key: str, doc: dict) -> "Request":
        gid = str(doc["gaia_id"]).strip()
        slug = str(doc["field"]["slug"])
        if not GAIA.fullmatch(gid):
            raise ValueError(f"bad gaia_id {gid!r}")
        if not SLUG.fullmatch(slug):
            raise ValueError(f"bad field slug {slug!r}")
        nb = []
        for n in doc.get("neighbours") or []:
            if isinstance(n, dict) and GAIA.fullmatch(str(n.get("gaia_id", "")).strip()):
                nb.append(dict(n, gaia_id=str(n["gaia_id"]).strip()))
        return Request(key, gid, float(doc["ra"]), float(doc["dec"]), slug, nb)


def now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _when(doc: dict, mtime: datetime) -> datetime:
    try:
        t = datetime.fromisoformat(str(doc.get("requested_at")).replace("Z", "+00:00"))
        return t if t.tzinfo else t.replace(tzinfo=timezone.utc)
    except ValueError:
        return mtime


class Job:
    def __init__(self, store: Base, client: mast.Client, cfg: Config, runner=None, claims: Claims | None = None):
        self.store = store
        self.claims = claims or Claims(store, os.environ.get("HOSTNAME") or socket.gethostname())
        self.lease: Lease | None = None
        self.mast = client
        self.cfg = cfg
        self.runner = runner or self._run_tglc
        start = time.monotonic()
        stop = cfg.stop_minutes if cfg.stop_minutes is not None else cfg.max_minutes + 30
        self.deadline = start + cfg.max_minutes * 60
        self.hard_deadline = start + max(stop, cfg.max_minutes) * 60
        self.mast.stop_at = self.hard_deadline - CHILD_RESERVE
        self.stopped = False
        os.makedirs(cfg.workdir, exist_ok=True)
        for name in os.listdir(cfg.workdir):
            if name.startswith(("tglc-", "hlsp-")):
                path = os.path.join(cfg.workdir, name)
                shutil.rmtree(path) if os.path.isdir(path) else os.unlink(path)

    def out_of_time(self) -> bool:
        return self.stopped or time.monotonic() >= self.deadline

    def child_deadline(self) -> float:
        return time.time() + (self.hard_deadline - CHILD_RESERVE - time.monotonic())

    def queue(self) -> list[tuple[str, dict]]:
        items = []
        for key, mtime in self.store.list(REQUESTS):
            if not key.endswith(".json"):
                continue
            try:
                doc = self.store.get_json(key)
            except Exception as e:
                log.error("unreadable request %s: %s", key, e)
                continue
            items.append((_when(doc, mtime) if isinstance(doc, dict) else mtime, key, doc))
        items.sort(key=lambda x: (x[0], x[1]))
        return [(k, d) for _, k, d in items]

    def lease_lost(self) -> bool:
        return self.lease is not None and self.lease.lost.is_set()

    def run(self) -> int:
        """Work through the queue, oldest first, skipping requests other workers hold.

        The queue is read again after each request, so requests added during the run are seen.
        """
        n = 0
        tried: set[str] = set()
        while True:
            if self.out_of_time():
                log.info("time budget used; the rest of the queue waits for the next run")
                break
            req = self.next_request(tried)
            if req is None:
                break
            tried.add(req.key)
            lease = self.claims.take(req.key, req.gaia_id, {"slug": req.slug, "ra": req.ra, "dec": req.dec})
            if lease is None:
                log.info("%s is claimed by another worker", req.gaia_id)
                continue
            try:
                if not self.store.exists(req.key):
                    continue
                self.lease = lease
                lease.start()
                self.process(req)
                n += 1
            finally:
                self.lease = None
                lease.release()
        return n

    def next_request(self, tried: set[str]) -> Request | None:
        """The oldest request not yet tried this run whose star is outside the cutouts other workers hold."""
        others = self.claims.others()
        waiting = 0
        for key, doc in self.queue():
            if key in tried:
                continue
            try:
                req = Request.parse(key, doc)
            except Exception as e:
                log.error("dropping malformed request %s: %s", key, e)
                self.store.delete(key)
                tried.add(key)
                continue
            if any(_shares_cutout(c, req) for c in others):
                waiting += 1
                continue
            return req
        if waiting:
            log.info("%d requests wait for the cutouts other workers are making", waiting)
        return None

    def base(self, req: Request) -> str:
        return f"vetting/cache/{req.slug}/tess"

    def process(self, req: Request) -> None:
        base = self.base(req)
        status_key = f"{base}/{req.gaia_id}/status.json"
        attempts_key = f"{ATTEMPTS}{req.gaia_id}.json"
        attempts = self.store.get_json(attempts_key) if self.store.exists(attempts_key) else {}
        status = {"gaia_id": req.gaia_id, "sectors_available": [], "sectors_done": [], "sectors_failed": {},
                  "updated_at": now()}
        if self.store.exists(status_key):
            old = self.store.get_json(status_key)
            status["sectors_done"] = sorted(int(s) for s in old.get("sectors_done", []))
            status["sectors_failed"] = {str(k): v for k, v in (old.get("sectors_failed") or {}).items()}
        try:
            sectors = self.mast.sectors(req.ra, req.dec)
        except mast.OutOfTime as e:
            log.info("%s: time budget used before the sector lookup: %s", req.gaia_id, e)
            self.stopped = True
            return
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
            if self.lease_lost():
                log.error("%s: lost the claim; another worker has the star", req.gaia_id)
                return
            if self.out_of_time():
                log.info("time budget used during %s", req.gaia_id)
                return
            tries = int(attempts.get(str(n), 0))
            if tries >= self.cfg.max_attempts:
                status["sectors_failed"][str(n)] = f"gave up after {tries} runs that did not finish"
                self._status(status_key, status)
                continue
            attempts[str(n)] = tries + 1
            self.store.put_json(attempts_key, attempts)
            t0 = time.time()
            try:
                how = self.sector(req, s)
                status["sectors_done"] = sorted(set(status["sectors_done"]) | {n})
                log.info("%s s%04d done via %s in %.0f s", req.gaia_id, n, how, time.time() - t0)
            except LeaseLost:
                log.error("%s s%04d: lost the claim; another worker has the star", req.gaia_id, n)
                return
            except (OutOfTime, mast.OutOfTime) as e:
                self.stopped = True
                if getattr(e, "progress", False):
                    attempts[str(n)] = tries
                    self.store.put_json(attempts_key, attempts)
                log.info("%s s%04d stopped at the time budget after %.0f s (%s); it resumes next run",
                         req.gaia_id, n, time.time() - t0, e)
                self._status(status_key, status)
                return
            except Permanent as e:
                status["sectors_failed"][str(n)] = str(e)
                log.warning("%s s%04d failed: %s", req.gaia_id, n, e)
            except Exception as e:
                log.warning("%s s%04d attempt %d failed: %s", req.gaia_id, n, tries + 1, e)
                if tries + 1 >= self.cfg.max_attempts:
                    status["sectors_failed"][str(n)] = f"gave up after {tries + 1} runs: {e}"[:300]
            self._status(status_key, status)
        finished = set(status["sectors_done"]) | {int(k) for k in status["sectors_failed"]}
        if finished >= set(status["sectors_available"]) and not self.lease_lost():
            self.store.delete(req.key)
            self.store.delete(attempts_key)
            log.info("%s complete; request removed", req.gaia_id)

    def _status(self, key: str, status: dict) -> None:
        status["updated_at"] = now()
        self.store.put_json(key, status)

    def sector(self, req: Request, s: dict) -> str:
        n = s["sector"]
        base = self.base(req)
        lc_key = f"{base}/lc/s{n:04d}/{req.gaia_id}.csv.zst"
        out_key = f"{base}/{req.gaia_id}/s{n:04d}.csv.zst"
        if n <= mast.HLSP_MAX_SECTOR and self._hlsp(req, s, req.gaia_id, lc_key):
            self.store.put(out_key, self.store.get(lc_key), "application/zstd")
            for nb in req.neighbours:
                if float(nb.get("sep_arcsec", 1e9)) <= NEIGHBOUR_HLSP_ARCSEC:
                    nk = f"{base}/lc/s{n:04d}/{nb['gaia_id']}.csv.zst"
                    if not self.store.exists(nk):
                        self._hlsp(req, s, str(nb["gaia_id"]), nk)
            return "TGLC HLSP"
        if self.store.exists(lc_key):
            self.store.put(out_key, self.store.get(lc_key), "application/zstd")
            return "field cache"
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
        lc_dir = f"{base}/lc/s{n:04d}/"
        work = os.path.join(self.cfg.workdir, f"tglc-{req.gaia_id}-s{n:04d}")
        shutil.rmtree(work, ignore_errors=True)
        os.makedirs(work)
        try:
            names = [req.gaia_id] + [str(nb["gaia_id"]) for nb in req.neighbours
                                     if not self.store.exists(f"{lc_dir}{nb['gaia_id']}.csv.zst")]
            cutout = os.path.join(work, "cutout.fits")
            catalog = os.path.join(work, "catalog.pkl")
            spec = {"workdir": work, "cutout": cutout, "catalog": catalog, "sector": n, "size": CUTOUT_SIZE,
                    "limit_mag": LIMIT_MAG, "factor": EPSF_FACTOR, "names": names, "edge_margin": EDGE_MARGIN,
                    "deadline": self.child_deadline()}
            prefix = self.find_cutout(req, n)
            meta = None
            if prefix:
                meta = self.store.get_json(prefix + "meta.json")
                log.info("s%04d: reusing cached cutout %s", n, prefix)
                self.store.get_file_zst(prefix + "cutout.fits.zst", cutout, work)
                with open(catalog, "wb") as f:
                    f.write(self.store.get_zst(prefix + "catalog.pkl.zst"))
                spec.update(reuse=True, field_mag=None, center=meta["center"])
                if meta.get("epsf_name"):
                    epsf_in = os.path.join(work, "epsf-in.npz")
                    with open(epsf_in, "wb") as f:
                        f.write(self.store.get_zst(prefix + "epsf.npz.zst"))
                    spec.update(epsf_in=epsf_in, epsf_name=meta["epsf_name"])
                if not meta.get("field_complete", True) and self.cfg.field_mag is not None:
                    done = [k[len(lc_dir):-len(".csv.zst")] for k in self.store.keys(lc_dir)
                            if k.endswith(".csv.zst")]
                    spec.update(field_mag=meta.get("field_mag") or self.cfg.field_mag, skip=done)
                    log.info("s%04d: resuming the field light curves; %d already cached", n, len(done))
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
            if res.get("lost"):
                raise LeaseLost(res.get("error"))
            if res.get("stopped"):
                self.stopped = True
            if not res.get("ok"):
                if res.get("permanent"):
                    raise Permanent(f"tglc: {res.get('error')}")
                if res.get("stopped"):
                    saved = self._checkpoint(base, n, spec, res, prefix, meta)
                    raise OutOfTime(f"tglc: {res.get('error')}", progress=saved)
                raise RuntimeError(f"tglc: {res.get('error')}")
            new = 0
            pos = res.get("positions", {})
            for gid, path in sorted(res["lcs"].items()):
                key = f"{lc_dir}{gid}.csv.zst"
                if gid not in names and not _inside(pos.get(gid), CUTOUT_SIZE):
                    continue
                if self.store.exists(key):
                    continue
                csv, _ = lightcurve.from_fits(path)
                self.store.put_zst(key, csv)
                new += 1
            log.info("s%04d: %d of %d light curves new to the field cache", n, new, len(res["lcs"]))
            saved = self._checkpoint(base, n, spec, res, prefix, meta) or new > 0
            if req.gaia_id in res.get("errors", {}):
                raise Permanent(f"tglc: {res['errors'][req.gaia_id]}")
            if res.get("stopped") and req.gaia_id not in res["lcs"]:
                raise OutOfTime("tglc stopped before the star's light curve", progress=saved)
            if res.get("stopped"):
                log.info("s%04d: field light curves stopped at the time budget; the rest wait for a later request", n)
        finally:
            shutil.rmtree(work, ignore_errors=True)

    def _checkpoint(self, base: str, n: int, spec: dict, res: dict, prefix: str | None, meta: dict | None) -> bool:
        """Save the cutout, catalog and ePSF a later run can resume from. True when something new was saved.

        Changes to a reused cutout merge into its meta.json as stored now, which another worker may have updated.
        """
        from astropy.io import fits

        complete = bool(res.get("field_complete", not res.get("stopped")))
        if prefix and meta is not None:
            update = self.store.get_json(prefix + "meta.json") if self.store.exists(prefix + "meta.json") else dict(meta)
            changed = False
            if res.get("epsf") and not update.get("epsf_name"):
                with open(res["epsf"], "rb") as f:
                    self.store.put_zst(prefix + "epsf.npz.zst", f.read())
                update["epsf_name"] = os.path.basename(res["epsf"])
                changed = True
            if spec.get("field_mag") is not None and complete != update.get("field_complete", True):
                update["field_complete"] = complete
                changed = True
            if changed:
                update["updated"] = now()
                self.store.put_json(prefix + "meta.json", update)
            return changed
        if not os.path.exists(spec["catalog"]) or not os.path.exists(spec["cutout"]):
            return False
        ra, dec = spec["center"]
        prefix = f"{base}/cutouts/s{n:04d}/{ra:.5f}{dec:+.5f}/"
        with fits.open(spec["cutout"], memmap=True) as h:
            wcs = h[2].header.tostring()
        self.store.put_file_zst(prefix + "cutout.fits.zst", spec["cutout"], spec["workdir"])
        with open(spec["catalog"], "rb") as f:
            self.store.put_zst(prefix + "catalog.pkl.zst", f.read())
        if res.get("epsf"):
            with open(res["epsf"], "rb") as f:
                self.store.put_zst(prefix + "epsf.npz.zst", f.read())
        else:
            log.warning("s%04d: no ePSF file to cache yet", n)
        self.store.put_json(prefix + "meta.json", {
            "center": [ra, dec], "size": spec["size"], "sector": n, "camera": res.get("camera"),
            "ccd": res.get("ccd"), "wcs": wcs, "epsf_name": os.path.basename(res["epsf"]) if res.get("epsf") else None,
            "limit_mag": spec["limit_mag"], "field_mag": spec["field_mag"], "factor": spec["factor"],
            "field_complete": complete if spec["field_mag"] is not None else True,
            "n_gaia": res.get("n_gaia"), "light_curves": len(res.get("lcs") or {}), "tglc": _tglc_version(),
            "timings": res.get("timings"), "peak_rss_mb": res.get("peak_rss_mb"), "created": now()})
        return True

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
        try:
            while True:
                pid, rc, ru = os.wait4(p.pid, os.WNOHANG)
                if pid:
                    break
                if self.lease_lost():
                    p.kill()
                    os.wait4(p.pid, 0)
                    log.error("s%04d: killed tglc because another worker took over the star", spec["sector"])
                    return {"ok": False, "permanent": False, "lost": True,
                            "error": "the claim on the star was lost"}
                if time.monotonic() > self.hard_deadline - KILL_RESERVE:
                    p.kill()
                    os.wait4(p.pid, 0)
                    log.warning("s%04d: tglc did not stop by itself; killed at the time budget", spec["sector"])
                    return {"ok": False, "permanent": False, "stopped": True,
                            "error": "tglc was killed at the time budget"}
                time.sleep(5)
        except BaseException:
            p.kill()
            p.wait()
            raise
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


def _shares_cutout(claim: dict, req: Request) -> bool:
    """Whether req's star may lie in the cutout around the claimed star, so it can reuse that cutout."""
    try:
        if claim.get("slug") != req.slug or str(claim.get("gaia_id")) == req.gaia_id:
            return False
        ra, dec = float(claim["ra"]), float(claim["dec"])
    except (KeyError, TypeError, ValueError):
        return False
    dra = (req.ra - ra + 180) % 360 - 180
    return math.hypot(dra * math.cos(math.radians(dec)), req.dec - dec) <= SHARED_CUTOUT_DEG


def _inside(pos, size: int) -> bool:
    if not pos:
        return False
    x, y = pos
    return EDGE_MARGIN <= x <= size - 1 - EDGE_MARGIN and EDGE_MARGIN <= y <= size - 1 - EDGE_MARGIN


def _tglc_version() -> str:
    try:
        from importlib.metadata import version

        return version("tglc")
    except Exception:
        return "unknown"

