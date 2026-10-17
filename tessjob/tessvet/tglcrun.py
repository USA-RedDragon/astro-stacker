"""One tglc run on one sector's FFI cutout, in its own process.

The parent passes a JSON spec and reads result.json back. Running tglc in a child keeps its
memory and its MAST/Gaia retries apart from the queue loop and lets the parent measure peak RSS.

tglc prints to the job log; TQDM_MININTERVAL keeps its progress bar to a line a minute.

A new cutout builds the tglc Source from Gaia DR3 and the TIC (network), fits the ePSF and writes
light curves for every named star, then for every other star down to field_mag. A cached cutout
rebuilds the Source from the saved catalog state, so the ePSF fingerprint matches and the fit is
skipped.

spec["deadline"] is a Unix time. The ePSF fit, every light curve and every catalog retry check it;
past it the run stops, and result.json lists what was written, with "stopped" set.
"""

from __future__ import annotations

import glob
import json
import os
import pickle
import re
import socket
import sys
import time
import traceback
import warnings

HEAVY = {"hdulist", "flux", "flux_err", "time", "quality", "cadence", "mask", "wcs", "gaia", "sector",
         "sector_list", "cadence_origin"}
EPSF_LABEL = "field"
GAIA_ROW_CAP = 50000
SOCKET_TIMEOUT = 600
PROGRESS_SECONDS = 300
OFF_FRAME = -(10 ** 6)
EPSF_BATCH = 256
_FORK = None


class Permanent(Exception):
    pass


class OutOfTime(Exception):
    pass


class Clock:
    def __init__(self, deadline=None, now=time.time):
        self.deadline = deadline
        self.now = now

    def left(self) -> float:
        return float("inf") if self.deadline is None else self.deadline - self.now()

    def check(self) -> None:
        if self.left() <= 0:
            raise OutOfTime("time budget used")


def _fit_cadence(j):
    from threadpoolctl import threadpool_limits

    fit, a, kw = _FORK
    with threadpool_limits(1):
        return fit(*a, time=j, **kw)


def _ids(source):
    return [str(d).split()[-1] for d in source.gaia["DESIGNATION"]]


class Guards:
    """Wraps tglc's per-cadence and per-star fits to check the clock, count light curves and skip stars.

    While inside is (margin, size), stars closer than margin pixels to the cutout edge are skipped too.
    With workers above 1, the per-cadence ePSF fits run EPSF_BATCH cadences at a time in forked workers.
    """

    def __init__(self, clock, log, workers=1):
        self.clock = clock
        self.log = log
        self.workers = workers
        self.fits = {}
        self.fits_for = None
        self.skip = set()
        self.inside = None
        self.fitted = 0
        self.logged = clock.now()

    def install(self, tl):
        fit_psf, fit_lc, get_psf = tl.fit_psf, tl.fit_lc, tl.get_psf

        def guarded_fit_psf(*a, **kw):
            self.clock.check()
            j = kw.get("time")
            if self.workers <= 1 or j is None or len(a) < 2:
                return fit_psf(*a, **kw)
            key = (id(a[0]), id(a[1]))
            if key != self.fits_for:
                self.fits, self.fits_for = {}, key
            if j not in self.fits:
                self.fits = self._fit_batch(fit_psf, a, kw, j)
            return self.fits.pop(j)

        def guarded_fit_lc(*a, **kw):
            self.clock.check()
            self.fitted += 1
            if self.clock.now() - self.logged >= PROGRESS_SECONDS:
                self.logged = self.clock.now()
                self.log(f"light curves fitted so far: {self.fitted}")
            return fit_lc(*a, **kw)

        def guarded_get_psf(source, *a, **kw):
            out = get_psf(source, *a, **kw)
            if not self.skip and self.inside is None:
                return out
            import numpy as np

            A, star_info, over_size, x_round, y_round = out
            hide = np.array([g in self.skip for g in _ids(source)], dtype=bool)
            if self.inside is not None:
                margin, size = self.inside
                xs = np.asarray(source.gaia[f"sector_{source.sector}_x"], dtype=float)
                ys = np.asarray(source.gaia[f"sector_{source.sector}_y"], dtype=float)
                lo, hi = margin, size - 1 - margin
                hide |= ~((xs >= lo) & (xs <= hi) & (ys >= lo) & (ys <= hi))
            return A, star_info, over_size, np.where(hide, OFF_FRAME, x_round), y_round

        tl.fit_psf, tl.fit_lc, tl.get_psf = guarded_fit_psf, guarded_fit_lc, guarded_get_psf


    def _fit_batch(self, fit_psf, a, kw, j):
        global _FORK
        import multiprocessing

        import numpy as np

        times = np.asarray(a[1].time)
        todo = [j] + [k for k in range(j + 1, min(len(times), j + EPSF_BATCH)) if np.isfinite(times[k])]
        _FORK = (fit_psf, a, {k: v for k, v in kw.items() if k != "time"})
        try:
            with multiprocessing.get_context("fork").Pool(self.workers) as pool:
                rows = pool.map(_fit_cadence, todo, chunksize=max(1, len(todo) // (self.workers * 4)))
        finally:
            _FORK = None
        return dict(zip(todo, rows))


class _Lines:
    """Turn tqdm's carriage returns into newlines so container logs get one line per update."""

    def __init__(self, stream):
        self.stream = stream

    def write(self, s):
        return self.stream.write(s.replace("\r", "\n"))

    def flush(self):
        self.stream.flush()

    def __getattr__(self, name):
        return getattr(self.stream, name)


def _source_new(spec, log, clock=None):
    from astropy.io import fits
    import tglc.ffi_cut as fc

    cut = spec["cutout"]
    fc.Tesscut.get_cutouts = lambda **kw: [fits.open(cut, memmap=True)]
    name = f"{spec['center'][0]:.6f} {spec['center'][1]:.6f}"
    clock = clock or Clock()
    last = None
    attempts = spec.get("attempts", 4)
    for attempt in range(attempts):
        clock.check()
        last_try = attempt + 1 == attempts
        try:
            src = fc.Source_cut(name, size=spec["size"], sector=spec["sector"], limit_mag=spec["limit_mag"],
                                transient=None, ffi="SPOC")
            break
        except json.JSONDecodeError as e:
            last = e
            log(f"Source_cut attempt {attempt + 1}: a catalog service sent a non-JSON reply")
            if not last_try:
                _pause(clock, spec.get("backoff", 120) * (attempt + 1))
            continue
        except ValueError as e:
            raise Permanent(str(e)) from e
        except Exception as e:
            last = e
            log(f"Source_cut attempt {attempt + 1} failed: {e!r}"[:400])
            if not last_try:
                _pause(clock, spec.get("backoff", 120) * (attempt + 1))
    else:
        raise RuntimeError(f"catalog queries kept failing: {last!r}"[:300])
    if len(src.catalogdata) >= GAIA_ROW_CAP:
        log(f"warning: Gaia cone returned {len(src.catalogdata)} rows, the archive cap; the outermost stars are missing")
    state = {k: v for k, v in src.__dict__.items() if k not in HEAVY}
    tmp = spec["catalog"] + ".part"
    with open(tmp, "wb") as f:
        pickle.dump(state, f, pickle.HIGHEST_PROTOCOL)
    os.replace(tmp, spec["catalog"])
    return src


def _pause(clock, seconds):
    if seconds >= clock.left():
        raise OutOfTime("no time left to retry the catalog queries")
    time.sleep(seconds)


def _source_cached(spec):
    from astropy.io import fits
    from tglc.ffi_cut import Source_cut

    with open(spec["catalog"], "rb") as f:
        state = pickle.load(f)
    src = Source_cut.__new__(Source_cut)
    src.__dict__.update(state)
    hdul = fits.open(spec["cutout"], memmap=True)
    src.hdulist = [hdul]
    src.sector_list = [hdul[0].header["SECTOR"]]
    src.sector = 0
    src.select_sector(sector=spec["sector"])
    if src.sector != spec["sector"]:
        raise Permanent(f"cached cutout does not hold sector {spec['sector']}")
    return src


def run(spec: dict, clock: Clock | None = None) -> dict:
    warnings.filterwarnings("ignore")
    import tglc.target_lightcurve as tl

    out = spec["workdir"]

    def log(msg):
        print(time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), msg, flush=True)

    clock = clock or Clock(spec.get("deadline"))
    guards = Guards(clock, log, workers=int(spec.get("workers") or 1))
    guards.install(tl)
    t0 = time.time()
    result = {"lcs": {}, "errors": {}, "epsf": None, "timings": {}, "stopped": False,
              "field_complete": spec.get("field_mag") is None}
    epsf_dir = os.path.join(out, "epsf", "SPOC")
    src = None
    try:
        clock.check()
        src = _source_cached(spec) if spec["reuse"] else _source_new(spec, log, clock)
        result["timings"]["source"] = round(time.time() - t0, 1)
        result["n_gaia"] = len(src.gaia)
        if spec.get("epsf_in"):
            os.makedirs(epsf_dir, exist_ok=True)
            os.replace(spec["epsf_in"], os.path.join(epsf_dir, spec["epsf_name"]))
        common = dict(factor=spec["factor"], sector=src.sector, target=EPSF_LABEL, local_directory=out + "/",
                      save_aper=False, ffi="SPOC", no_progress_bar=False)
        t1 = time.time()
        for gid in spec["names"]:
            if _lc_path(out, gid, src.sector):
                continue
            try:
                tl.epsf(src, limit_mag=spec["limit_mag"], name=f"Gaia DR3 {gid}", **common)
            except ValueError as e:
                result["errors"][gid] = str(e)
        result["timings"]["named"] = round(time.time() - t1, 1)
        if spec.get("field_mag") is not None:
            t2 = time.time()
            guards.skip = set(spec.get("skip") or ()) | set(spec["names"])
            if spec.get("edge_margin") is not None:
                guards.inside = (spec["edge_margin"], spec["size"])
            try:
                tl.epsf(src, limit_mag=spec["field_mag"], name=None, **common)
                log("field light curves written")
            except OutOfTime:
                raise
            except Exception as e:
                result["field_error"] = f"{type(e).__name__}: {e}"[:300]
                log(f"field light curves failed, keeping the named ones: {result['field_error']}")
            guards.skip, guards.inside = set(), None
            result["field_complete"] = True
            result["timings"]["field"] = round(time.time() - t2, 1)
    except OutOfTime as e:
        result["stopped"] = True
        log(f"stopped: {e}; keeping what was written")
    if src is not None:
        _collect(result, src, out, epsf_dir)
    log(f"done: {len(result['lcs'])} light curves, errors {result['errors']}, stopped {result['stopped']}")
    return result


def _collect(result, src, out, epsf_dir):
    for p in glob.glob(os.path.join(out, "lc", "SPOC", "*.fits")):
        m = re.search(r"gaiaid-(\d+)-s(\d{4})-", os.path.basename(p))
        if m and int(m.group(2)) == src.sector:
            result["lcs"][m.group(1)] = p
    found = sorted(glob.glob(os.path.join(epsf_dir, f"epsf_{EPSF_LABEL}_sector_{src.sector}_*.npz")),
                   key=os.path.getmtime)
    result["epsf"] = found[-1] if found else None
    result["camera"], result["ccd"] = int(src.camera), int(src.ccd)
    index = {g: i for i, g in enumerate(_ids(src))}
    xs, ys = src.gaia[f"sector_{src.sector}_x"], src.gaia[f"sector_{src.sector}_y"]
    result["positions"] = {g: [float(xs[index[g]]), float(ys[index[g]])] for g in result["lcs"] if g in index}


def _lc_path(out, gid, sector):
    hits = glob.glob(os.path.join(out, "lc", "SPOC", f"*gaiaid-{gid}-s{sector:04d}-*.fits"))
    return hits[0] if hits else None


def main(spec_path: str) -> int:
    sys.stderr = _Lines(sys.stderr)
    socket.setdefaulttimeout(SOCKET_TIMEOUT)
    with open(spec_path) as f:
        spec = json.load(f)
    res_path = os.path.join(spec["workdir"], "result.json")
    try:
        res = run(spec)
        res["ok"] = True
    except Permanent as e:
        res = {"ok": False, "permanent": True, "error": str(e)[:300]}
    except Exception as e:
        traceback.print_exc()
        res = {"ok": False, "permanent": False, "error": f"{type(e).__name__}: {e}"[:300]}
    with open(res_path, "w") as f:
        json.dump(res, f)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1]))
