"""One tglc run on one sector's FFI cutout, in its own process.

The parent passes a JSON spec and reads result.json back. Running tglc in a child keeps its
memory and its MAST/Gaia retries apart from the queue loop and lets the parent measure peak RSS.

tglc prints to the job log; TQDM_MININTERVAL keeps its progress bar to a line a minute.

A new cutout builds the tglc Source from Gaia DR3 and the TIC (network), fits the ePSF and writes
light curves for every star down to field_mag plus every named star. A cached cutout rebuilds
the Source from the saved catalog state, so the ePSF fingerprint matches and the fit is skipped.
"""

from __future__ import annotations

import glob
import json
import os
import pickle
import re
import sys
import time
import traceback
import warnings

HEAVY = {"hdulist", "flux", "flux_err", "time", "quality", "cadence", "mask", "wcs", "gaia", "sector",
         "sector_list", "cadence_origin"}
EPSF_LABEL = "field"
GAIA_ROW_CAP = 50000


class Permanent(Exception):
    pass


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


def _source_new(spec, log):
    from astropy.io import fits
    import tglc.ffi_cut as fc

    cut = spec["cutout"]
    fc.Tesscut.get_cutouts = lambda **kw: [fits.open(cut, memmap=True)]
    name = f"{spec['center'][0]:.6f} {spec['center'][1]:.6f}"
    last = None
    for attempt in range(spec.get("attempts", 4)):
        try:
            src = fc.Source_cut(name, size=spec["size"], sector=spec["sector"], limit_mag=spec["limit_mag"],
                                transient=None, ffi="SPOC")
            break
        except ValueError as e:
            raise Permanent(str(e)) from e
        except Exception as e:
            last = e
            log(f"Source_cut attempt {attempt + 1} failed: {e!r}"[:400])
            time.sleep(spec.get("backoff", 120) * (attempt + 1))
    else:
        raise RuntimeError(f"catalog queries kept failing: {last!r}"[:300])
    if len(src.catalogdata) >= GAIA_ROW_CAP:
        log(f"warning: Gaia cone returned {len(src.catalogdata)} rows, the archive cap; the outermost stars are missing")
    state = {k: v for k, v in src.__dict__.items() if k not in HEAVY}
    with open(spec["catalog"], "wb") as f:
        pickle.dump(state, f, pickle.HIGHEST_PROTOCOL)
    return src


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


def run(spec: dict) -> dict:
    warnings.filterwarnings("ignore")
    from tglc.target_lightcurve import epsf

    out = spec["workdir"]

    def log(msg):
        print(time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), msg, flush=True)

    t0 = time.time()
    result = {"lcs": {}, "errors": {}, "epsf": None, "timings": {}}
    src = _source_cached(spec) if spec["reuse"] else _source_new(spec, log)
    result["timings"]["source"] = round(time.time() - t0, 1)
    result["n_gaia"] = len(src.gaia)
    epsf_dir = os.path.join(out, "epsf", "SPOC")
    if spec.get("epsf_in"):
        os.makedirs(epsf_dir, exist_ok=True)
        os.replace(spec["epsf_in"], os.path.join(epsf_dir, spec["epsf_name"]))
    common = dict(factor=spec["factor"], sector=src.sector, target=EPSF_LABEL, local_directory=out + "/",
                  save_aper=False, ffi="SPOC", no_progress_bar=False)
    t1 = time.time()
    if spec.get("field_mag") is not None and not spec["reuse"]:
        epsf(src, limit_mag=spec["field_mag"], name=None, **common)
        log("field light curves written")
    result["timings"]["field"] = round(time.time() - t1, 1)
    for gid in spec["names"]:
        if _lc_path(out, gid, src.sector):
            continue
        try:
            epsf(src, limit_mag=spec["limit_mag"], name=f"Gaia DR3 {gid}", **common)
        except ValueError as e:
            result["errors"][gid] = str(e)
    result["timings"]["named"] = round(time.time() - t1 - result["timings"]["field"], 1)
    for p in glob.glob(os.path.join(out, "lc", "SPOC", "*.fits")):
        m = re.search(r"gaiaid-(\d+)-s(\d{4})-", os.path.basename(p))
        if m and int(m.group(2)) == src.sector:
            result["lcs"][m.group(1)] = p
    found = sorted(glob.glob(os.path.join(epsf_dir, f"epsf_{EPSF_LABEL}_sector_{src.sector}_*.npz")),
                   key=os.path.getmtime)
    result["epsf"] = found[-1] if found else None
    result["camera"], result["ccd"] = int(src.camera), int(src.ccd)
    log(f"done: {len(result['lcs'])} light curves, errors {result['errors']}")
    return result


def _lc_path(out, gid, sector):
    hits = glob.glob(os.path.join(out, "lc", "SPOC", f"*gaiaid-{gid}-s{sector:04d}-*.fits"))
    return hits[0] if hits else None


def main(spec_path: str) -> int:
    sys.stderr = _Lines(sys.stderr)
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
