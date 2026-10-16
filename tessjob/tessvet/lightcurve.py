"""TGLC light curve FITS (HLSP v1 or a local tglc run) to the vetting CSV.

Columns: bjd,flux,flux_err,aper_flux,aper_flux_err,quality
bjd       BJD (TDB) = TESS BTJD + 2457000
flux      cal_psf_flux over its median; flux_err is CPSF_ERR (whole-series MAD) on the same scale
aper_flux cal_aper_flux over its median; aper_flux_err is CAPE_ERR on the same scale
quality   TESS_flags | TGLC_flags << 24, so 0 only when both are 0
"""

from __future__ import annotations

import io

import numpy as np

HEADER = "bjd,flux,flux_err,aper_flux,aper_flux_err,quality"
BTJD_OFFSET = 2457000.0


def _norm(values, good):
    v = np.asarray(values, dtype=np.float64)
    ref = v[good & np.isfinite(v)]
    if not ref.size:
        ref = v[np.isfinite(v)]
    med = float(np.median(ref)) if ref.size else np.nan
    if not np.isfinite(med) or med == 0:
        return np.full(v.shape, np.nan), np.nan
    return v / med, med


def _hdr(h, key):
    v = h.get(key)
    try:
        v = float(v)
    except (TypeError, ValueError):
        return np.nan
    return v if np.isfinite(v) else np.nan


def from_table(time, cal_psf, cal_aper, tess_flags, tglc_flags, cpsf_err, cape_err) -> bytes:
    time = np.asarray(time, dtype=np.float64)
    tess_flags = np.asarray(tess_flags, dtype=np.int64)
    tglc_flags = np.asarray(tglc_flags, dtype=np.int64)
    quality = (tess_flags & 0xFFFFFF) | (tglc_flags << 24)
    good = quality == 0
    flux, fmed = _norm(cal_psf, good)
    aper, amed = _norm(cal_aper, good)
    ferr = cpsf_err / fmed if np.isfinite(fmed) else np.nan
    aerr = cape_err / amed if np.isfinite(amed) else np.nan
    keep = np.isfinite(time)
    buf = io.StringIO()
    buf.write(HEADER + "\n")
    for t, f, a, q in zip(time[keep] + BTJD_OFFSET, flux[keep], aper[keep], quality[keep]):
        buf.write(f"{t:.6f},{_f(f)},{_f(ferr)},{_f(a)},{_f(aerr)},{int(q)}\n")
    return buf.getvalue().encode()


def _f(v) -> str:
    return f"{v:.6f}" if np.isfinite(v) else "NaN"


def from_fits(path: str) -> tuple[bytes, dict]:
    """The CSV bytes and a little metadata (gaia id, sector, rows, good rows) from a TGLC FITS file."""
    from astropy.io import fits

    with fits.open(path, memmap=False) as h:
        d = h[1].data
        hd = h[1].header
        p = h[0].header
        csv = from_table(d["time"], d["cal_psf_flux"], d["cal_aper_flux"], d["TESS_flags"], d["TGLC_flags"],
                         _hdr(hd, "CPSF_ERR"), _hdr(hd, "CAPE_ERR"))
        good = int(((np.asarray(d["TESS_flags"]) == 0) & (np.asarray(d["TGLC_flags"]) == 0)
                    & np.isfinite(np.asarray(d["time"], dtype=float))).sum())
        obj = str(p.get("OBJECT", hd.get("OBJECT", "")))
        meta = {"object": obj, "sector": int(p.get("SECTOR", 0)), "rows": int(len(d)), "good": good,
                "tessmag": _hdr(p, "TESSMAG"), "contamination": _hdr(p, "CONTAMRT")}
    return csv, meta
