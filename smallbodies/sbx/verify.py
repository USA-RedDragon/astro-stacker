"""Checks against JPL: DE440's test positions and Horizons astrometric RA/Dec."""

from __future__ import annotations

import json
import os
import time
import urllib.parse
import urllib.request

import numpy as np

from . import core
from .engine import Engine
from .orbits import KIND_COMET, Orbits

HORIZONS = "https://ssd.jpl.nasa.gov/api/horizons.api"
TESTPO_BODIES = {1: 1, 2: 2, 3: 3, 4: 5, 5: 6, 6: 7, 7: 8, 8: 9, 9: 10, 10: 4, 11: 0}


def testpo(ephem, path: str, jd_min=2287184.5, jd_max=2688976.5):
    """Largest differences from JPL's testpo.440 positions (au) and velocities (au/day).

    Only body pairs ASSIST can evaluate (planets, Moon, Sun, SSB, EMB) are used.
    """
    worst = [0.0, 0.0]
    n = 0
    started = False
    with open(path) as fh:
        for line in fh:
            if not started:
                started = line.startswith("EOT")
                continue
            f = line.split()
            jd, tgt, ctr, coord, val = float(f[2]), int(f[3]), int(f[4]), int(f[5]), float(f[6])
            if not (jd_min <= jd <= jd_max) or tgt > 13 or ctr > 13:
                continue
            a = _testpo_state(ephem, tgt, jd)
            b = _testpo_state(ephem, ctr, jd)
            got = (a - b)[coord - 1]
            k = 0 if coord <= 3 else 1
            worst[k] = max(worst[k], abs(got - val))
            n += 1
    return n, worst[0], worst[1]


def _testpo_state(ephem, body: int, jd: float) -> np.ndarray:
    t = jd - ephem.jd_ref
    if body == 12:
        return np.zeros(6)
    if body == 13:
        e = ephem.get_particle(3, t)
        m = ephem.get_particle(4, t)
        mu = 1.0 / (1.0 + ephem.EMRAT)
        return np.array([e.x, e.y, e.z, e.vx, e.vy, e.vz]) * (1 - mu) + np.array([m.x, m.y, m.z, m.vx, m.vy, m.vz]) * mu
    p = ephem.get_particle(TESTPO_BODIES[body], t)
    return np.array([p.x, p.y, p.z, p.vx, p.vy, p.vz])


def horizons_command(o: Orbits, k: int) -> str:
    if o.kind[k] == KIND_COMET:
        return f"DES={comet_key(o.name[k])};CAP;NOFRAG"
    if o.number[k]:
        return f"{o.number[k]};"
    return f"DES={o.name[k]};"


def comet_key(name: str) -> str:
    name = name.split(" (")[0].strip()
    if "/" in name and name.split("/")[0].rstrip("PDCXIA").isdigit():
        return name.split("/")[0]
    return name


def horizons_radec(command: str, jd_utc: list[float], cache_dir: str, retries: int = 5):
    """Astrometric geocentric ICRF RA/Dec from Horizons, cached as JSON."""
    key = urllib.parse.quote(command, safe="") + "_" + str(abs(hash(tuple(round(j, 7) for j in jd_utc))))
    path = os.path.join(cache_dir, key + ".json")
    if os.path.exists(path):
        with open(path) as fh:
            return json.load(fh)
    q = {
        "format": "json",
        "COMMAND": f"'{command}'",
        "OBJ_DATA": "NO",
        "MAKE_EPHEM": "YES",
        "EPHEM_TYPE": "OBSERVER",
        "CENTER": "'500@399'",
        "TLIST": " ".join(f"'{j:.9f}'" for j in jd_utc),
        "TIME_TYPE": "UT",
        "QUANTITIES": "'1'",
        "ANG_FORMAT": "DEG",
        "EXTRA_PREC": "YES",
        "CSV_FORMAT": "YES",
    }
    url = HORIZONS + "?" + urllib.parse.urlencode(q)
    for attempt in range(retries):
        try:
            with urllib.request.urlopen(url, timeout=120) as resp:
                result = json.load(resp)["result"]
            break
        except Exception:
            if attempt == retries - 1:
                raise
            time.sleep(5 * (attempt + 1))
    out = None
    if "$$SOE" in result:
        body = result[result.index("$$SOE") + 5 : result.index("$$EOE")]
        out = []
        for line in body.strip().splitlines():
            f = [x.strip() for x in line.split(",")]
            out.append([float(f[3]), float(f[4])])
    with open(path, "w") as fh:
        json.dump(out, fh)
    return out


def separation_arcsec(ra1, dec1, ra2, dec2):
    r1, d1, r2, d2 = (np.radians(np.asarray(x, dtype=float)) for x in (ra1, dec1, ra2, dec2))
    s = np.sin((d2 - d1) / 2) ** 2 + np.cos(d1) * np.cos(d2) * np.sin((r2 - r1) / 2) ** 2
    return np.degrees(2 * np.arcsin(np.sqrt(s))) * 3600


def predict(engine: Engine, o: Orbits, jd_utc: list[float]) -> np.ndarray:
    """RA/Dec (n_objects x n_epochs x 2) seen from the geocentre."""
    out = np.empty((len(o), len(jd_utc), 2))
    for j, jd in enumerate(jd_utc):
        g = engine.observe(o, jd)
        out[:, j, 0] = g["ra"]
        out[:, j, 1] = g["dec"]
    return out
