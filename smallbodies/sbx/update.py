"""The daily update: mirror the sources, merge the catalogue, integrate the grid."""

from __future__ import annotations

import datetime as dt
import gzip
import hashlib
import json
import os
import shutil
import time

import numpy as np
import zstandard

from . import catalog, core, grid, verify
from .orbits import Orbits
from .store import Store

EPHEMERIDES = {
    "linux_p1550p2650.440": (
        "https://ssd.jpl.nasa.gov/ftp/eph/planets/Linux/de440/linux_p1550p2650.440",
        "29915576d0a6555766b99485ac3056ee415e86df4fce282611c31afb329ad062",
    ),
    "sb441-n16.bsp": (
        "https://ssd.jpl.nasa.gov/ftp/eph/small_bodies/asteroids_de441/sb441-n16.bsp",
        "919d612ce3c72a78fc7158f9120156542d0f21e6b8b052e4c1339c759747fd90",
    ),
    "testpo.440": (
        "https://ssd.jpl.nasa.gov/ftp/eph/planets/Linux/de440/testpo.440",
        "45084a7f406b8e41032e7d99dd8042d4df97a9f752a68c06d1a27af16693429f",
    ),
}
PLANETS = "linux_p1550p2650.440"
ASTEROIDS = "sb441-n16.bsp"
MIN_ASTEROIDS = 1_000_000
MIN_COMETS = 500


def log(msg: str) -> None:
    print(time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), msg, flush=True)


def sha256_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for block in iter(lambda: fh.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def _download(url: str, path: str) -> None:
    tmp = path + ".part"
    with open(tmp, "wb") as fh:
        fh.write(catalog.http_get(url, timeout=1800))
    os.replace(tmp, path)


def ensure_ephemerides(store: Store | None, directory: str) -> dict[str, str]:
    """Local paths of the ephemeris files, from the bucket or else from JPL.

    Every file must match its pinned SHA-256; a new download is also checked
    against JPL's testpo.440 positions before it is mirrored.
    """
    os.makedirs(directory, exist_ok=True)
    paths = {}
    fetched = []
    for name, (url, digest) in EPHEMERIDES.items():
        path = os.path.join(directory, name)
        paths[name] = path
        if os.path.exists(path) and sha256_file(path) == digest:
            continue
        key = f"ephemeris/{name}.zst"
        if store is not None and store.exists(key):
            with open(path + ".part", "wb") as out:
                store_obj = store.client.get_object(store.bucket, key)
                try:
                    zstandard.ZstdDecompressor().copy_stream(store_obj, out)
                finally:
                    store_obj.close()
                    store_obj.release_conn()
            os.replace(path + ".part", path)
        else:
            log(f"downloading {url}")
            _download(url, path)
            fetched.append(name)
        got = sha256_file(path)
        if got != digest:
            os.remove(path)
            raise RuntimeError(f"{name}: sha256 {got}, expected {digest}")
    if fetched:
        n, dpos, dvel = verify.testpo(core.load_ephem(paths[PLANETS], paths[ASTEROIDS]), paths["testpo.440"])
        log(f"testpo.440: {n} values, worst {dpos:.3g} au, {dvel:.3g} au/day")
        if n < 10000 or dpos > 1e-12 or dvel > 1e-14:
            raise RuntimeError("DE440 does not reproduce JPL's test positions")
        if store is not None:
            for name in fetched:
                with open(paths[name], "rb") as fh:
                    store.put_zst(f"ephemeris/{name}.zst", fh.read(), level=10)
                log(f"mirrored {name}")
    return paths


def mirror_sources(store: Store, date: str, workdir: str) -> dict:
    """Download today's MPC and JPL orbits and store them, as fetched, under sources/<date>/."""
    out = {}
    log("fetching MPCORB.DAT.gz")
    out["MPCORB.DAT"] = catalog.fetch_mpcorb()
    log("fetching CometEls.txt")
    out["CometEls.txt"] = catalog.fetch_cometels()
    for name, args in (("sbdb-numbered.json", ("a", True)), ("sbdb-unnumbered.json", ("a", False)), ("sbdb-comets.json", ("c", None))):
        log(f"fetching SBDB {name}")
        out[name] = json.dumps(catalog.fetch_sbdb(*args)).encode()
    for name, data in out.items():
        store.put_zst(f"sources/{date}/{name}.zst", data)
        log(f"stored sources/{date}/{name}.zst ({len(data) / 1e6:.0f} MB raw)")
    return out


def build_catalog(raw: dict) -> tuple[Orbits, np.ndarray]:
    cat, src = catalog.merge(
        raw["MPCORB.DAT"].decode("latin-1"),
        raw["CometEls.txt"].decode("latin-1"),
        json.loads(raw["sbdb-numbered.json"]),
        json.loads(raw["sbdb-unnumbered.json"]),
        json.loads(raw["sbdb-comets.json"]),
    )
    n_ast = int((cat.kind == 0).sum())
    n_com = int((cat.kind == 1).sum())
    if n_ast < MIN_ASTEROIDS or n_com < MIN_COMETS:
        raise RuntimeError(f"catalogue too small: {n_ast} asteroids, {n_com} comets")
    return cat, src


def run(store: Store, workdir: str, grid_start: str, ahead_days: float, retain_days: int, workers: int) -> None:
    now = dt.datetime.now(dt.timezone.utc)
    today = now.date()
    date = today.isoformat()
    build = now.strftime("%Y-%m-%dT%H%M%SZ")
    os.makedirs(workdir, exist_ok=True)
    eph = ensure_ephemerides(store, os.path.join(workdir, "ephemeris"))
    raw = mirror_sources(store, date, workdir)
    cat, src = build_catalog(raw)
    del raw
    path = os.path.join(workdir, "catalog.npz.zst")
    cat.save(path)
    store.put_file(f"catalog/{date}/catalog.npz.zst", path, "application/zstd")
    store.put_file(f"grid/{build}/catalog.npz.zst", path, "application/zstd")
    log(f"catalogue: {len(cat)} orbits, {int((src == catalog.SOURCE_JPL).sum())} from JPL")

    start = core.utc_to_tdb(_jd(grid_start))
    end = core.utc_to_tdb(_jd(today.isoformat())) + ahead_days
    times = grid.times_between(start, end)
    t0 = time.time()
    total = 0
    for t, states in grid.build(cat, eph[PLANETS], eph[ASTEROIDS], times, os.path.join(workdir, "grid"), workers=workers, log=log):
        blob = grid.encode(states)
        total += len(blob)
        store.put(f"grid/{build}/{grid.epoch_name(t)}.zst", blob, "application/zstd")
    build_s = time.time() - t0
    meta = {
        "build": build,
        "date": date,
        "catalog": f"grid/{build}/catalog.npz.zst",
        "objects": len(cat),
        "times_tdb": [float(t) for t in times],
        "step_days": grid.STEP_DAYS,
        "build_seconds": round(build_s),
        "bytes": total,
        "built": dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds"),
    }
    store.put_json(f"grid/{build}/meta.json", meta)
    store.put_json("grid/latest.json", meta)
    log(f"grid {build}: {len(times)} epochs, {total / 1e9:.2f} GB, built and stored in {build_s:.0f}s")
    prune(store, today, retain_days)


def prune(store: Store, today: dt.date, retain_days: int) -> None:
    cutoff = (today - dt.timedelta(days=retain_days)).isoformat()
    for top in ("sources/", "catalog/"):
        for p in store.prefixes(top):
            d = p[len(top) :].strip("/")
            if d < cutoff:
                log(f"pruned {p}: {store.delete_prefix(p)} objects")
    latest = store.get_json("grid/latest.json")
    latest = latest.get("build", latest["date"])
    grids = [p[len("grid/") :].strip("/") for p in store.prefixes("grid/")]
    complete = [d for d in grids if d <= latest and store.exists(f"grid/{d}/meta.json")]
    keep = sorted(complete)[-2:]
    for d in grids:
        if d not in keep and d <= latest:
            log(f"pruned grid/{d}/: {store.delete_prefix(f'grid/{d}/')} objects")


def _jd(date: str) -> float:
    y, m, d = (int(x) for x in date.split("-"))
    from .orbits import calendar_jd

    return calendar_jd(y, m, d)


def cleanup(path: str) -> None:
    shutil.rmtree(path, ignore_errors=True)
