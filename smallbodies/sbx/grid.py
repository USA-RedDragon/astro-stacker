"""The precomputed grid: every object's barycentric state at fixed epochs.

The grid only narrows a query to the objects near the field; their exact
positions come from integrating their orbits to the query time. States are
float32, one file per epoch, byte-shuffled and zstd-compressed.
"""

from __future__ import annotations

import itertools
import os
import time
from concurrent.futures import FIRST_COMPLETED, ProcessPoolExecutor, wait

import numpy as np
import zstandard

from . import core
from .engine import PERTURBER_INDEX, SINGLE_Q, SMALL_BATCH_Q, _worker_ephem, batch_size, perturber_state, perturbers
from .orbits import KIND_COMET, Orbits

STEP_DAYS = 4.0


def times_between(start_tdb: float, end_tdb: float, step: float = STEP_DAYS) -> np.ndarray:
    n = int(np.ceil((end_tdb - start_tdb) / step)) + 1
    return start_tdb + step * np.arange(n)


def _grid_job(args):
    planets, asteroids, epoch, states, times, nongrav, comet = args
    eph = _worker_ephem(planets, asteroids)
    return core.propagate_grid(eph, epoch, states, times, nongrav=nongrav, comet=comet).astype(np.float32)


def _run(pool, jobs, fn, sink, window, log, label):
    t0 = last = time.time()
    done = 0
    total = sum(len(j[0]) for j in jobs)
    pending = {}
    it = iter(jobs)
    for key, args in itertools.islice(it, window):
        pending[pool.submit(fn, args)] = key
    while pending:
        finished, _ = wait(pending, return_when=FIRST_COMPLETED)
        for fut in finished:
            key = pending.pop(fut)
            sink(key, fut.result())
            done += len(key)
            nxt = next(it, None)
            if nxt is not None:
                pending[pool.submit(fn, nxt[1])] = nxt[0]
        if time.time() - last > 60:
            last = time.time()
            log(f"{label}: {done}/{total} objects, {last - t0:.0f}s")
    log(f"{label}: {total} objects in {time.time() - t0:.0f}s")


def build(cat: Orbits, planets: str, asteroids: str, times: np.ndarray, directory: str, workers: int = 4, log=print):
    """Integrate the whole catalogue to the grid epochs; yields (epoch, N x 6 float32 states).

    Objects with the same orbit epoch share simulations (see
    engine.batch_size); near-Earth objects and comets go one at a time.
    Results are appended to one file per epoch as jobs finish, so memory
    stays small, then each file is put back in catalogue order.
    """
    os.makedirs(directory, exist_ok=True)
    paths = [os.path.join(directory, f"{k:04d}.f32") for k in range(len(times))]
    files = [open(p, "wb") for p in paths]
    order: list[np.ndarray] = []

    def sink(key, res):
        for k, fh in enumerate(files):
            fh.write(np.ascontiguousarray(res[k], dtype=np.float32).tobytes())
        order.append(np.asarray(key))

    eph = _worker_ephem(planets, asteroids)
    pert = np.nonzero(perturbers(cat))[0]
    if len(pert):
        res = np.empty((len(times), len(pert), 6), dtype=np.float32)
        for i, k in enumerate(pert):
            for j, t in enumerate(times):
                res[j, i] = perturber_state(eph, PERTURBER_INDEX[cat.number[k]], float(t))
        sink(pert, res)
    single = (cat.kind == KIND_COMET) | (cat.q < SINGLE_Q)
    rows = np.nonzero(~np.isin(np.arange(len(cat)), pert))[0]
    states = np.full((len(cat), 6), np.nan)
    states[rows] = core.barycentric_states(eph, cat.take(rows))
    jobs = []
    batch = rows[~single[rows]]
    for ep in np.unique(cat.epoch[batch]):
        idx = batch[cat.epoch[batch] == ep]
        order_q = idx[np.argsort(cat.q[idx])]
        for group in (order_q[cat.q[order_q] < SMALL_BATCH_Q], order_q[cat.q[order_q] >= SMALL_BATCH_Q]):
            if not len(group):
                continue
            n = batch_size(float(cat.q[group[0]]), 0, 256)
            for s in range(0, len(group), n):
                part = group[s : s + n]
                jobs.append((part, (planets, asteroids, float(ep), states[part], times, cat.nongrav[part], False)))
    for i in rows[single[rows]]:
        p = np.array([i])
        jobs.append((p, (planets, asteroids, float(cat.epoch[i]), states[p], times, cat.nongrav[p], bool(cat.kind[i] == KIND_COMET))))
    del states
    jobs.sort(key=lambda j: -len(j[0]))
    with ProcessPoolExecutor(workers) as pool:
        _run(pool, jobs, _grid_job, sink, workers * 4, log, "grid")
    for fh in files:
        fh.close()
    idx = np.concatenate(order)
    if len(idx) != len(cat) or len(np.unique(idx)) != len(cat):
        raise RuntimeError(f"grid covers {len(np.unique(idx))} of {len(cat)} objects")
    failed = 0
    for k, t in enumerate(times):
        raw = np.fromfile(paths[k], dtype=np.float32).reshape(-1, 6)
        out = np.empty((len(cat), 6), dtype=np.float32)
        out[idx] = raw
        os.remove(paths[k])
        if k == 0:
            failed = int((~np.isfinite(out).all(axis=1)).sum())
            if failed:
                log(f"grid: {failed} objects failed to integrate")
        yield float(t), out


def encode(states: np.ndarray, level: int = 3) -> bytes:
    """Byte-shuffled float32 (N x 6), zstd-compressed."""
    a = np.ascontiguousarray(states, dtype=np.float32)
    shuffled = a.view(np.uint8).reshape(-1, 4).T.copy()
    header = np.array([a.shape[0]], dtype=np.int64).tobytes()
    return zstandard.ZstdCompressor(level=level, threads=-1).compress(header + shuffled.tobytes())


def decode(blob: bytes) -> np.ndarray:
    raw = zstandard.ZstdDecompressor().decompressobj().decompress(blob)
    n = int(np.frombuffer(raw[:8], dtype=np.int64)[0])
    body = np.frombuffer(raw[8:], dtype=np.uint8).reshape(4, n * 6)
    return np.ascontiguousarray(body.T).view(np.float32).reshape(n, 6)


def hermite(t: float, t0: float, t1: float, s0: np.ndarray, s1: np.ndarray):
    """Cubic Hermite positions and velocities at t between two grid epochs."""
    h = t1 - t0
    x = (t - t0) / h
    p0, v0 = s0[:, :3].astype(np.float64), s0[:, 3:].astype(np.float64) * h
    p1, v1 = s1[:, :3].astype(np.float64), s1[:, 3:].astype(np.float64) * h
    x2, x3 = x * x, x * x * x
    pos = (2 * x3 - 3 * x2 + 1) * p0 + (x3 - 2 * x2 + x) * v0 + (-2 * x3 + 3 * x2) * p1 + (x3 - x2) * v1
    vel = ((6 * x2 - 6 * x) * p0 + (3 * x2 - 4 * x + 1) * v0 + (-6 * x2 + 6 * x) * p1 + (3 * x2 - 2 * x) * v1) / h
    return pos, vel


def epoch_name(t: float) -> str:
    return f"{t:.1f}"


def files_exist(directory: str, times: np.ndarray) -> bool:
    return all(os.path.exists(os.path.join(directory, epoch_name(t) + ".zst")) for t in times)
