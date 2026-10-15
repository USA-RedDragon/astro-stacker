"""Exact ephemerides of catalogue objects for one time and observer."""

from __future__ import annotations

import math
from concurrent.futures import Executor

import numpy as np

from . import core
from .orbits import KIND_COMET, MPC_TYPES, Orbits

PERTURBERS = ["107", "1", "65", "511", "15", "31", "52", "10", "704", "7", "3", "2", "16", "87", "88", "4"]
PERTURBER_INDEX = {n: 11 + k for k, n in enumerate(PERTURBERS)}
PLANETS = [
    ("Mercury", 1),
    ("Venus", 2),
    ("Moon", 4),
    ("Mars", 5),
    ("Jupiter", 6),
    ("Saturn", 7),
    ("Uranus", 8),
    ("Neptune", 9),
    ("Pluto", 10),
]
GR_MODEL = "GR_EIH"


SINGLE_Q = 1.3
SMALL_BATCH_Q = 1.7


def batch_size(q: float, kind: int, large: int) -> int:
    """Objects one simulation integrates together.

    IAS15's step is the shortest any of its objects needs, so near-Earth
    objects and comets, whose close approaches need short steps, go alone.
    """
    if kind == KIND_COMET or q < SINGLE_Q:
        return 1
    if q < SMALL_BATCH_Q:
        return 16
    return large


def classify(o: Orbits) -> np.ndarray:
    """SkyBoT-style dynamical classes from the elements and MPC's orbit type."""
    q, e = o.q, o.e
    with np.errstate(divide="ignore", invalid="ignore"):
        a = np.where(e < 1, q / (1 - e), np.inf)
    out = np.full(len(o), "MB>Middle", dtype=object)
    out[a < 2.5] = "MB>Inner"
    out[(a >= 2.82) & (a < 3.27)] = "MB>Outer"
    out[(a >= 3.27) & (a < 3.7)] = "MB>Cybele"
    out[(a >= 3.7) & (a < 4.2)] = "MB>Hilda"
    out[(a >= 4.2) & (a < 5.05)] = "MB>Outer"
    out[(a >= 5.05) & (a < 5.35)] = "Trojan"
    out[(a >= 5.35) & (a < 30.1)] = "Centaur"
    out[a >= 30.1] = "KBO"
    out[(a >= 30.1) & (q > 30) & (e < 0.24)] = "KBO>Classical"
    out[(a >= 49) & (e >= 0.24)] = "KBO>SDO"
    out[(a >= 38.5) & (a <= 40.5) & (q <= 36)] = "KBO>Resonant>3:2"
    out[(a < 2.0) & (q > 1.3)] = "Hungaria"
    out[(q > 1.3) & (q < 1.666) & (a < 3.27)] = "Mars-Crosser"
    for code, name in MPC_TYPES.items():
        out[o.mpc_type == code] = name
    out[e >= 1] = "Hyperbolic"
    out[o.kind == KIND_COMET] = "Comet"
    return out


def magnitude(o: Orbits, r: np.ndarray, delta: np.ndarray, phase: np.ndarray) -> np.ndarray:
    """V for asteroids (IAU H, G) and total magnitude for comets (H + 5 log D + 2.5 K log r)."""
    tan_half = np.tan(phase / 2)
    phi1 = np.exp(-3.33 * tan_half**0.63)
    phi2 = np.exp(-1.87 * tan_half**1.22)
    g = np.where(np.isnan(o.G), 0.15, o.G)
    with np.errstate(divide="ignore", invalid="ignore"):
        ast = o.H + 5 * np.log10(r * delta) - 2.5 * np.log10((1 - g) * phi1 + g * phi2)
        com = o.H + 5 * np.log10(delta) + 2.5 * g * np.log10(r)
    return np.where(o.kind == KIND_COMET, com, ast)


def perturbers(o: Orbits) -> np.ndarray:
    """Which orbits are the massive asteroids in the perturber file, read from it rather than integrated."""
    return np.array([n in PERTURBER_INDEX for n in o.number], dtype=bool) & (o.kind != KIND_COMET)


def perturber_state(ephem, index: int, t_tdb: float, h: float = 0.01) -> np.ndarray:
    """Position from the SPK file; velocity by central difference, as ASSIST gives none for these."""
    t = t_tdb - ephem.jd_ref
    p = ephem.get_particle(index, t)
    a = ephem.get_particle(index, t + h)
    b = ephem.get_particle(index, t - h)
    return np.array([p.x, p.y, p.z, (a.x - b.x) / (2 * h), (a.y - b.y) / (2 * h), (a.z - b.z) / (2 * h)])


def _propagate_group(args):
    planets, asteroids, epoch, states, t_tdb, nongrav, comet = args
    eph = _worker_ephem(planets, asteroids)
    return core.propagate(eph, epoch, states, t_tdb, GR_MODEL, nongrav, comet)


_EPHEM = {}


def _worker_ephem(planets, asteroids):
    key = (planets, asteroids)
    if key not in _EPHEM:
        _EPHEM[key] = core.load_ephem(planets, asteroids)
    return _EPHEM[key]


class Engine:
    """Positions of catalogue objects from ASSIST n-body integrations."""

    def __init__(self, planets: str, asteroids: str, pool: Executor | None = None, chunk: int = 64):
        self.planets_path = planets
        self.asteroids_path = asteroids
        self.ephem = _worker_ephem(planets, asteroids)
        self.pool = pool
        self.chunk = chunk

    def states_at(self, o: Orbits, t_tdb: float) -> np.ndarray:
        """Barycentric ICRF states of each orbit at t_tdb."""
        out = np.empty((len(o), 6))
        pert = perturbers(o)
        for k in np.nonzero(pert)[0]:
            out[k] = perturber_state(self.ephem, PERTURBER_INDEX[o.number[k]], t_tdb)
        rest = np.nonzero(~pert)[0]
        if not len(rest):
            return out
        sub = o.take(rest)
        init = core.barycentric_states(self.ephem, sub)
        jobs = []
        for kind in np.unique(sub.kind):
            for ep in np.unique(sub.epoch[sub.kind == kind]):
                idx = np.nonzero((sub.epoch == ep) & (sub.kind == kind))[0]
                order = idx[np.argsort(sub.q[idx])]
                q = sub.q[order]
                for group in (order[q < SINGLE_Q], order[(q >= SINGLE_Q) & (q < SMALL_BATCH_Q)], order[q >= SMALL_BATCH_Q]):
                    if not len(group):
                        continue
                    n = batch_size(float(sub.q[group[0]]), int(kind), self.chunk)
                    for s in range(0, len(group), n):
                        part = group[s : s + n]
                        args = (self.planets_path, self.asteroids_path, float(ep), init[part], t_tdb, sub.nongrav[part], bool(kind == KIND_COMET))
                        jobs.append((part, args))
        if self.pool is not None and len(jobs) > 1:
            results = list(self.pool.map(_propagate_group, [j[1] for j in jobs]))
        else:
            results = [_propagate_group(j[1]) for j in jobs]
        for (part, _), res in zip(jobs, results):
            out[rest[part]] = res
        return out

    def observe(self, o: Orbits, jd_utc: float, site_km: np.ndarray | None = None, aberration: bool = False, states=None):
        """Astrometric ICRF positions, rates and geometry seen at jd_utc."""
        t = core.utc_to_tdb(jd_utc)
        obs = core.observer(self.ephem, t, site_km)
        if states is None:
            states = self.states_at(o, t)
        sun, sun_vel = core.body_state(self.ephem, "Sun", t)
        rp, vp, lt = core.light_time_correct(states, obs.pos, sun)
        return self._geometry(o, t, obs, rp, vp, lt, aberration, sun, sun_vel)

    def _geometry(self, o, t, obs, rp, vp, lt, aberration, sun, sun_vel):
        rho = rp - obs.pos
        dist = np.linalg.norm(rho, axis=1)
        u = rho / dist[:, None]
        if aberration:
            u = core.aberrate(rho, obs.vel)
        ra, dec = core.radec(u)
        helio = rp - sun
        r = np.linalg.norm(helio, axis=1)
        obs_sun = sun - obs.pos
        cos_phase = np.sum(helio * rho, axis=1) / (r * dist)
        phase = np.arccos(np.clip(cos_phase, -1, 1))
        cos_el = (rho @ obs_sun) / (dist * np.linalg.norm(obs_sun))
        elong = np.degrees(np.arccos(np.clip(cos_el, -1, 1)))
        dlt = (np.sum(rho * (vp - obs.vel), axis=1) / dist) / core.C_AU_DAY
        drho = vp * (1 - dlt)[:, None] - obs.vel
        dudt = drho / dist[:, None] - (np.sum(drho * u, axis=1) / dist)[:, None] * u
        sa, ca = np.sin(np.radians(ra)), np.cos(np.radians(ra))
        sd, cd = np.sin(np.radians(dec)), np.cos(np.radians(dec))
        east = np.stack([-sa, ca, np.zeros_like(sa)], 1)
        north = np.stack([-sd * ca, -sd * sa, cd], 1)
        k = math.degrees(1) * 3600 / 24
        return {
            "ra": ra,
            "dec": dec,
            "dra": np.sum(dudt * east, 1) * k,
            "ddec": np.sum(dudt * north, 1) * k,
            "delta": dist,
            "r": r,
            "phase": np.degrees(phase),
            "elong": elong,
            "v": magnitude(o, r, dist, phase),
            "lt": lt,
            "helio": helio,
            "helio_vel": vp - sun_vel,
        }

    def planets(self, jd_utc: float, site_km: np.ndarray | None = None, aberration: bool = False):
        """Major planets, Pluto and the Moon, light-time corrected from the planetary ephemeris."""
        t = core.utc_to_tdb(jd_utc)
        obs = core.observer(self.ephem, t, site_km)
        rows = []
        for name, idx in PLANETS:
            lt = 0.0
            for _ in range(4):
                p = self.ephem.get_particle(idx, t - lt - self.ephem.jd_ref)
                pos = np.array([p.x, p.y, p.z])
                lt = np.linalg.norm(pos - obs.pos) / core.C_AU_DAY
            vel = np.array([p.vx, p.vy, p.vz])
            rows.append((name, pos, vel, lt))
        return obs, rows
