"""Field queries: a coarse pass over the grid, then exact positions for the objects near the field."""

from __future__ import annotations

import math
import threading
from collections import OrderedDict
from typing import Callable

import numpy as np

from . import core, grid
from .catalog import SOURCE_JPL
from .engine import Engine, classify
from .orbits import KIND_COMET, Orbits

EARTH_RADIUS_AU = core.EARTH_RADIUS_KM / core.AU_KM
BASE_MARGIN_DEG = 0.25
CLOSE_AU = 0.05
NEAR_SUN_AU = 0.35
"""Objects this close to the Sun move too fast for the 4-day grid's
interpolation, so they always get an exact position."""


class OutOfRange(ValueError):
    pass


def unit(ra_deg, dec_deg) -> np.ndarray:
    ra, dec = np.radians(ra_deg), np.radians(dec_deg)
    return np.stack([np.cos(dec) * np.cos(ra), np.cos(dec) * np.sin(ra), np.sin(dec)], -1)


def separation_deg(u: np.ndarray, c: np.ndarray) -> np.ndarray:
    cross = np.linalg.norm(np.cross(u, c), axis=-1)
    return np.degrees(np.arctan2(cross, u @ c))


class Polygon:
    """A spherical polygon smaller than a hemisphere, tested in a gnomonic projection about its centre."""

    def __init__(self, vertices: list[tuple[float, float]]):
        if len(vertices) < 3:
            raise ValueError("a polygon needs 3 or more vertices")
        v = unit(np.array([p[0] for p in vertices]), np.array([p[1] for p in vertices]))
        c = v.sum(0)
        self.center = c / np.linalg.norm(c)
        self.radius = float(separation_deg(v, self.center).max())
        if self.radius >= 89:
            raise ValueError("the polygon must be smaller than a hemisphere")
        self.e, self.n = _tangent_basis(self.center)
        self.xy = self._project(v)

    def _project(self, u: np.ndarray) -> np.ndarray:
        d = u @ self.center
        return np.stack([(u @ self.e) / d, (u @ self.n) / d], -1)

    def contains(self, u: np.ndarray) -> np.ndarray:
        inside = np.zeros(len(u), dtype=bool)
        front = u @ self.center > 0
        p = self._project(u[front])
        x, y = p[:, 0], p[:, 1]
        res = np.zeros(len(p), dtype=bool)
        xs, ys = self.xy[:, 0], self.xy[:, 1]
        j = len(xs) - 1
        for i in range(len(xs)):
            cond = (ys[i] > y) != (ys[j] > y)
            with np.errstate(divide="ignore", invalid="ignore"):
                xint = (xs[j] - xs[i]) * (y - ys[i]) / (ys[j] - ys[i]) + xs[i]
            res ^= cond & (x < xint)
            j = i
        inside[front] = res
        return inside


def _tangent_basis(c: np.ndarray):
    z = np.array([0.0, 0.0, 1.0])
    e = np.cross(z, c)
    if np.linalg.norm(e) < 1e-12:
        e = np.array([0.0, 1.0, 0.0])
    e /= np.linalg.norm(e)
    n = np.cross(c, e)
    return e, n


class Generation:
    """One day's catalogue and the grid integrated from it."""

    def __init__(self, date: str, cat: Orbits, times: np.ndarray, fetch: Callable[[float], bytes], keep: int = 6):
        self.date = date
        self.cat = cat
        self.source = cat.source
        self.times = times
        self.classes = classify(cat)
        self._fetch = fetch
        self._cache: OrderedDict[int, np.ndarray] = OrderedDict()
        self._keep = keep
        self._lock = threading.Lock()
        self._loading: dict[int, threading.Lock] = {}

    def states(self, k: int) -> np.ndarray:
        with self._lock:
            if k in self._cache:
                self._cache.move_to_end(k)
                return self._cache[k]
            key_lock = self._loading.setdefault(k, threading.Lock())
        with key_lock:
            with self._lock:
                if k in self._cache:
                    return self._cache[k]
            arr = grid.decode(self._fetch(float(self.times[k])))
            if len(arr) != len(self.cat):
                raise RuntimeError(f"grid epoch {self.times[k]} has {len(arr)} objects, the catalogue {len(self.cat)}")
            with self._lock:
                self._cache[k] = arr
                self._loading.pop(k, None)
                while len(self._cache) > self._keep:
                    self._cache.popitem(last=False)
        return arr


def coarse(gen: Generation, engine: Engine, t_tdb: float, center: np.ndarray, radius_deg: float) -> np.ndarray:
    """Indexes of catalogue objects that may lie within radius_deg of center at t_tdb."""
    times = gen.times
    if not times[0] <= t_tdb <= times[-1]:
        raise OutOfRange(f"epoch JD {core.tdb_to_utc(t_tdb):.5f} is outside the precomputed range JD {core.tdb_to_utc(times[0]):.1f} to {core.tdb_to_utc(times[-1]):.1f}")
    k = int(min(np.searchsorted(times, t_tdb, side="right") - 1, len(times) - 2))
    pos, vel = grid.hermite(t_tdb, times[k], times[k + 1], gen.states(k), gen.states(k + 1))
    earth, _ = core.body_state(engine.ephem, "Earth", t_tdb)
    rho = pos - earth
    dist = np.linalg.norm(rho, axis=1)
    rho -= vel * (dist / core.C_AU_DAY)[:, None]
    dist = np.linalg.norm(rho, axis=1)
    with np.errstate(invalid="ignore", divide="ignore"):
        cosang = (rho @ center) / dist
        parallax = np.degrees(np.arcsin(np.clip(1.2 * EARTH_RADIUS_AU / dist, 0, 1)))
    limit = np.radians(np.minimum(radius_deg + BASE_MARGIN_DEG + parallax, 180))
    sun, _ = core.body_state(engine.ephem, "Sun", t_tdb)
    near_sun = np.linalg.norm(pos - sun, axis=1) < NEAR_SUN_AU
    keep = (cosang >= np.cos(limit)) | (dist < CLOSE_AU) | near_sun | ~np.isfinite(cosang)
    return np.nonzero(keep)[0]


def field(
    gen: Generation,
    engine: Engine,
    jd_utc: float,
    center: np.ndarray,
    radius_deg: float,
    polygon: Polygon | None = None,
    site_km: np.ndarray | None = None,
    aberration: bool = False,
    vmax: float | None = None,
    orbits: Generation | None = None,
):
    """Objects in the field at jd_utc: dicts sorted by distance from the centre."""
    t = core.utc_to_tdb(jd_utc)
    idx = coarse(gen, engine, t, center, radius_deg)
    src_gen = gen
    if orbits is not None and orbits is not gen:
        idx = _map_rows(gen, orbits, idx)
        src_gen = orbits
    sub = src_gen.cat.take(idx)
    rows = []
    if len(idx):
        g = engine.observe(sub, jd_utc, site_km, aberration)
        u = unit(g["ra"], g["dec"])
        sep = separation_deg(u, center)
        inside = polygon.contains(u) if polygon is not None else sep <= radius_deg
        if vmax is not None:
            inside &= ~(g["v"] > vmax)
        for j in np.nonzero(inside)[0]:
            k = idx[j]
            rows.append(
                {
                    "number": "" if sub.kind[j] == KIND_COMET else str(sub.number[j]),
                    "name": str(sub.name[j]),
                    "class": str(src_gen.classes[k]),
                    "kind": "comet" if sub.kind[j] == KIND_COMET else "asteroid",
                    "ra": float(g["ra"][j]),
                    "dec": float(g["dec"][j]),
                    "v": _finite(g["v"][j]),
                    "dra": float(g["dra"][j]),
                    "ddec": float(g["ddec"][j]),
                    "sep_arcsec": float(sep[j] * 3600),
                    "delta_au": float(g["delta"][j]),
                    "r_au": float(g["r"][j]),
                    "phase_deg": float(g["phase"][j]),
                    "elong_deg": float(g["elong"][j]),
                    "u": int(sub.U[j]) if sub.U[j] >= 0 else None,
                    "orbit": "JPL" if src_gen.source[k] == SOURCE_JPL else "MPC",
                    "orbit_epoch_jd": float(sub.epoch[j]),
                    "helio": [float(x) for x in g["helio"][j]],
                    "helio_vel": [float(x) for x in g["helio_vel"][j]],
                }
            )
    rows += _planets(engine, jd_utc, center, radius_deg, polygon, site_km, aberration)
    rows.sort(key=lambda r: r["sep_arcsec"])
    return rows


def _finite(x):
    x = float(x)
    return x if math.isfinite(x) else None


def _map_rows(gen: Generation, other: Generation, idx: np.ndarray) -> np.ndarray:
    key = getattr(other, "_key_index", None)
    if key is None:
        key = {(int(k), p): i for i, (k, p) in enumerate(zip(other.cat.kind, other.cat.packed))}
        other._key_index = key
    out = [key.get((int(gen.cat.kind[i]), gen.cat.packed[i]), -1) for i in idx]
    return np.array([i for i in out if i >= 0], dtype=np.int64)


PLANET_V = {"Mercury": -0.6, "Venus": -4.4, "Moon": -12.7, "Mars": -2.0, "Jupiter": -9.4, "Saturn": -8.9, "Uranus": -7.2, "Neptune": -6.9, "Pluto": -1.0}


def _planets(engine: Engine, jd_utc, center, radius_deg, polygon, site_km, aberration):
    obs, bodies = engine.planets(jd_utc, site_km)
    out = []
    for name, pos, vel, lt in bodies:
        rho = (pos - obs.pos)[None]
        u = core.aberrate(rho, obs.vel) if aberration else rho / np.linalg.norm(rho)
        sep = float(separation_deg(u, center)[0])
        inside = bool(polygon.contains(u)[0]) if polygon is not None else sep <= radius_deg
        if not inside:
            continue
        ra, dec = core.radec(u)
        out.append(
            {
                "number": "",
                "name": name,
                "class": "Planet" if name != "Moon" else "Satellite",
                "kind": "planet",
                "ra": float(ra[0]),
                "dec": float(dec[0]),
                "v": None,
                "dra": None,
                "ddec": None,
                "sep_arcsec": sep * 3600,
                "delta_au": float(np.linalg.norm(rho)),
            }
        )
    return out
