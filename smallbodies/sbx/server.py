"""HTTP API.

GET /v1/cone?ra=&dec=&radius=&epoch=     objects within radius degrees
GET /v1/cone?polygon=ra,dec;ra,dec;...&epoch=   objects inside a polygon
  epoch      JD (UTC) or an ISO 8601 UTC time
  site       lat,lon,height_m (WGS84) for topocentric positions; the default is the geocentre
  parallax   lon,rho_cos_phi,rho_sin_phi (MPC style), instead of site
  vmax       faintest V to return
  aberration true for apparent directions (with annual and diurnal aberration);
             the default is astrometric, like SkyBoT's and Horizons' astrometric RA/Dec
  orbits     YYYY-MM-DD: the orbits mirrored that day instead of the newest
  format     json (default) or skybot (SkyBoT's text table)
GET /v1/status   data versions
GET /healthz
"""

from __future__ import annotations

import datetime as dt
import json
import logging
import multiprocessing
import os
import re
import socket
import tempfile
import threading
import time
import traceback
from concurrent.futures import ProcessPoolExecutor
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import numpy as np

from . import core, grid, query
from .engine import Engine
from .orbits import Orbits, calendar_jd
from .store import Store
from .update import ASTEROIDS, PLANETS, ensure_ephemerides

log = logging.getLogger("sbx")
MAX_RADIUS = 10.0
DATE = re.compile(r"^\d{4}-\d{2}-\d{2}$")


class State:
    def __init__(self, store: Store, workdir: str, workers: int, cache_dir: str):
        self.store = store
        self.workdir = workdir
        self.cache_dir = cache_dir
        os.makedirs(cache_dir, exist_ok=True)
        eph = ensure_ephemerides(store, os.path.join(workdir, "ephemeris"))
        self.pool = ProcessPoolExecutor(workers, mp_context=multiprocessing.get_context("forkserver")) if workers > 1 else None
        self.engine = Engine(eph[PLANETS], eph[ASTEROIDS], pool=self.pool)
        self.gen: query.Generation | None = None
        self.older: dict[str, query.Generation] = {}
        self.lock = threading.Lock()

    def refresh(self) -> None:
        meta = self.store.get_json("grid/latest.json")
        build = meta.get("build", meta["date"])
        if self.gen is not None and self.gen.build == build:
            return
        gen = self._load(meta)
        with self.lock:
            self.gen = gen
            self.older.clear()
        self._prune_cache(build)
        log.info("serving grid %s: %d objects, %d epochs", build, meta["objects"], len(meta["times_tdb"]))

    def _load(self, meta: dict) -> query.Generation:
        date = meta["date"]
        build = meta.get("build", date)
        cat = self._catalog(build, meta["catalog"])
        times = np.asarray(meta["times_tdb"])
        directory = os.path.join(self.cache_dir, build)
        os.makedirs(directory, exist_ok=True)

        def fetch(t: float) -> bytes:
            path = os.path.join(directory, grid.epoch_name(t) + ".zst")
            if not os.path.exists(path):
                self._download(f"grid/{build}/{grid.epoch_name(t)}.zst", path)
            with open(path, "rb") as fh:
                return fh.read()

        if len(cat) != meta["objects"]:
            raise RuntimeError(f"catalogue {meta['catalog']} has {len(cat)} orbits, its grid {meta['objects']}")
        return query.Generation(date, cat, times, fetch, build=build)

    def _catalog(self, name: str, key: str) -> Orbits:
        path = os.path.join(self.cache_dir, f"catalog-{name}.npz.zst")
        if not os.path.exists(path):
            self._download(key, path)
        return Orbits.load(path)

    def _download(self, key: str, path: str) -> None:
        """Fetch key to path through a unique temporary file, so concurrent fetches cannot clash."""
        fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path), suffix=".part")
        os.close(fd)
        try:
            self.store.get_file(key, tmp)
            os.replace(tmp, path)
        finally:
            if os.path.exists(tmp):
                os.remove(tmp)

    def orbits(self, date: str) -> query.Generation:
        if not DATE.match(date):
            raise ValueError("orbits must be a date, YYYY-MM-DD")
        with self.lock:
            g = self.older.get(date)
        if g is not None:
            return g
        cat = self._catalog(date, f"catalog/{date}/catalog.npz.zst")
        g = query.Generation(date, cat, np.array([]), lambda t: b"")
        with self.lock:
            if len(self.older) >= 2:
                self.older.pop(next(iter(self.older)))
            self.older[date] = g
        return g

    def _prune_cache(self, keep: str) -> None:
        for name in os.listdir(self.cache_dir):
            if keep in name:
                continue
            path = os.path.join(self.cache_dir, name)
            if os.path.isdir(path):
                for f in os.listdir(path):
                    os.remove(os.path.join(path, f))
                os.rmdir(path)
            elif name.startswith("catalog-"):
                os.remove(path)


def parse_epoch(s: str) -> float:
    try:
        v = float(s)
        if v > 2_000_000:
            return v
        raise ValueError("epoch must be a JD or an ISO 8601 time")
    except ValueError:
        pass
    t = dt.datetime.fromisoformat(s.replace("Z", "+00:00"))
    if t.tzinfo is not None:
        t = t.astimezone(dt.timezone.utc).replace(tzinfo=None)
    day = t.day + (t.hour + (t.minute + (t.second + t.microsecond / 1e6) / 60) / 60) / 24
    return calendar_jd(t.year, t.month, day)


def parse_site(params: dict):
    if "site" in params:
        lat, lon, h = (float(x) for x in params["site"].split(","))
        lon_, rc, rs = core.parallax_constants(lat, lon, h)
        return core.site_itrf(lon_, rc, rs)
    if "parallax" in params:
        lon, rc, rs = (float(x) for x in params["parallax"].split(","))
        return core.site_itrf(lon, rc, rs)
    obs = params.get("observer", "500")
    if obs != "500":
        raise ValueError("observer must be 500 (geocentre); give site or parallax for a topocentric position")
    return None


def handle_cone(state: State, params: dict) -> tuple[dict, list]:
    if "epoch" not in params:
        raise ValueError("epoch is required")
    jd = parse_epoch(params["epoch"])
    polygon = None
    if "polygon" in params:
        verts = [tuple(float(x) for x in p.split(",")) for p in params["polygon"].split(";") if p.strip()]
        polygon = query.Polygon(verts)
        center, radius = polygon.center, polygon.radius
        if radius > MAX_RADIUS:
            raise ValueError(f"the polygon must fit in {MAX_RADIUS} degrees of its centre")
    else:
        ra, dec, radius = float(params["ra"]), float(params["dec"]), float(params["radius"])
        if not (0 < radius <= MAX_RADIUS):
            raise ValueError(f"radius must be in (0, {MAX_RADIUS}] degrees")
        center = query.unit(ra, dec)
    site = parse_site(params)
    vmax = float(params["vmax"]) if "vmax" in params else None
    aberration = params.get("aberration", "false").lower() in ("1", "true", "yes")
    gen = state.gen
    if gen is None:
        raise RuntimeError("no grid loaded yet")
    orbits = state.orbits(params["orbits"]) if "orbits" in params and params["orbits"] != gen.date else None
    rows = query.field(gen, state.engine, jd, center, radius, polygon, site, aberration, vmax, orbits)
    meta = {
        "epoch_jd_utc": jd,
        "observer": "topocentric" if site is not None else "geocentre",
        "frame": "ICRF, apparent direction" if aberration else "ICRF, astrometric",
        "orbits": (orbits or gen).date,
        "grid": gen.build,
        "count": len(rows),
    }
    return meta, rows


def skybot_text(meta: dict, rows: list) -> str:
    """SkyBoT's conesearch text table, as astro-stacker's ParseSkyBoT reads it."""
    out = [
        "# Flag: 1",
        f"# Ticket: local {meta['orbits']}",
        "# Num | Name | RA(h) | DE(deg) | Class | Mv | Err(arcsec) | d(arcsec) | dRA(arcsec/h) | dDEC(arcsec/h) | Dg(ua) | Dh(ua) | Phase(deg) | SunElong(deg) | x(au) | y(au) | z(au) | vx(au/d) | vy(au/d) | vz(au/d) | Ref. Epoch(JD) ",
    ]
    for r in rows:
        if r["kind"] == "planet":
            continue
        helio = r.get("helio") or [0, 0, 0]
        hv = r.get("helio_vel") or [0, 0, 0]
        out.append(
            " | ".join(
                [
                    r["number"] or " -",
                    r["name"],
                    _sexa(r["ra"] / 15, False, 4),
                    _sexa(r["dec"], True, 3),
                    r["class"],
                    f"{r['v']:.1f}" if r["v"] is not None else "-",
                    "-",
                    f"{r['sep_arcsec']:.3f}",
                    f"{r['dra']:.4f}",
                    f"{r['ddec']:.3f}",
                    f"{r['delta_au']:.11f}",
                    f"{r['r_au']:.11f}",
                    f"{r['phase_deg']:.2f}",
                    f"{r['elong_deg']:.2f}",
                    *(f"{x:.9f}" for x in helio),
                    *(f"{x:.9f}" for x in hv),
                    f"{r['orbit_epoch_jd']:.1f}",
                ]
            )
        )
    return "\n".join(out) + "\n"


def _sexa(v: float, sign: bool, dec: int) -> str:
    s = "-" if v < 0 else "+"
    v = abs(v)
    d = int(v)
    m = int((v - d) * 60)
    sec = ((v - d) * 60 - m) * 60
    if round(sec, dec) >= 60:
        sec = 0.0
        m += 1
    if m >= 60:
        m = 0
        d += 1
    body = f"{d:02d} {m:02d} {sec:0{3 + dec}.{dec}f}"
    return (s if sign else ("-" if s == "-" else "")) + body


class _Server(ThreadingHTTPServer):
    address_family = socket.AF_INET6
    daemon_threads = True


class Handler(BaseHTTPRequestHandler):
    state: State = None

    def log_message(self, fmt, *args):
        pass

    def _send(self, code: int, body: bytes, ctype: str) -> None:
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        u = urlparse(self.path)
        params = {k: v[-1] for k, v in parse_qs(u.query).items()}
        try:
            if u.path == "/healthz":
                ok = self.state.gen is not None
                return self._send(200 if ok else 503, b"ok\n" if ok else b"loading\n", "text/plain")
            if u.path == "/v1/status":
                g = self.state.gen
                doc = {"grid": g.build if g else None, "orbits": g.date if g else None, "objects": len(g.cat) if g else 0}
                if g is not None:
                    doc["range_jd_utc"] = [core.tdb_to_utc(g.times[0]), core.tdb_to_utc(g.times[-1])]
                return self._send(200, json.dumps(doc).encode(), "application/json")
            if u.path == "/v1/cone":
                t0 = time.time()
                meta, rows = handle_cone(self.state, params)
                meta["seconds"] = round(time.time() - t0, 3)
                log.info(
                    "cone epoch=%s center=%s,%s radius=%s polygon=%s observer=%s -> %d in %.2fs",
                    params.get("epoch"),
                    params.get("ra"),
                    params.get("dec"),
                    params.get("radius"),
                    "yes" if "polygon" in params else "no",
                    meta["observer"],
                    meta["count"],
                    meta["seconds"],
                )
                if params.get("format") == "skybot":
                    return self._send(200, skybot_text(meta, rows).encode(), "text/plain")
                for r in rows:
                    r.pop("helio", None)
                    r.pop("helio_vel", None)
                return self._send(200, json.dumps({**meta, "bodies": rows}).encode(), "application/json")
            return self._send(404, b"not found\n", "text/plain")
        except (ValueError, KeyError, query.OutOfRange) as ex:
            return self._send(400, json.dumps({"error": str(ex)}).encode(), "application/json")
        except Exception as ex:
            log.error("request failed: %s\n%s", ex, traceback.format_exc())
            return self._send(500, json.dumps({"error": str(ex)}).encode(), "application/json")


def serve(store: Store, workdir: str, port: int, workers: int, refresh_s: float = 600) -> None:
    state = State(store, workdir, workers, os.path.join(workdir, "cache"))
    Handler.state = state

    def refresher():
        while True:
            try:
                state.refresh()
            except Exception as ex:
                log.error("refresh failed: %s", ex)
            time.sleep(refresh_s if state.gen is not None else 30)

    threading.Thread(target=refresher, daemon=True).start()
    httpd = _Server(("::", port), Handler)
    log.info("listening on %d", port)
    httpd.serve_forever()

