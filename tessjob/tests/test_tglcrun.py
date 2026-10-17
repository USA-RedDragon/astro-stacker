import json
import os
import types

import numpy as np
import pytest
from astropy.table import Table

from tessvet import tglcrun


class Tick:
    def __init__(self, t=0.0):
        self.t = t

    def __call__(self):
        return self.t


def test_clock():
    t = Tick(10)
    c = tglcrun.Clock(20, now=t)
    assert c.left() == 10
    c.check()
    t.t = 20
    with pytest.raises(tglcrun.OutOfTime):
        c.check()
    assert tglcrun.Clock().left() == float("inf")


def gaia(ids):
    return Table({"DESIGNATION": [f"Gaia DR3 {g}" for g in ids], "sector_57_x": [10.0 + i for i in range(len(ids))],
                  "sector_57_y": [5.0] * len(ids)})


def test_guards_check_the_clock_count_and_hide_skipped_stars():
    t = Tick(0)
    logs = []
    tl = types.SimpleNamespace(
        fit_psf=lambda *a, **kw: "psf",
        fit_lc=lambda *a, **kw: "lc",
        get_psf=lambda source, **kw: ("A", "info", 3, np.array([5, 6, 7]), np.array([1, 2, 3])))
    g = tglcrun.Guards(tglcrun.Clock(1000, now=t), logs.append)
    g.install(tl)
    src = types.SimpleNamespace(gaia=gaia(["1", "2", "3"]))
    assert list(tl.get_psf(src)[3]) == [5, 6, 7]
    g.skip = {"2"}
    A, info, over, x, y = tl.get_psf(src)
    assert list(x) == [5, tglcrun.OFF_FRAME, 7] and list(y) == [1, 2, 3]
    assert tl.fit_psf() == "psf" and tl.fit_lc() == "lc"
    g.skip = set()
    g.inside = (1, 13)
    src.sector = 57
    assert list(tl.get_psf(src)[3]) == [5, 6, tglcrun.OFF_FRAME]
    g.inside = None
    t.t = tglcrun.PROGRESS_SECONDS
    tl.fit_lc()
    assert g.fitted == 2 and logs == ["light curves fitted so far: 2"]
    t.t = 1000
    with pytest.raises(tglcrun.OutOfTime):
        tl.fit_psf()
    with pytest.raises(tglcrun.OutOfTime):
        tl.fit_lc()


def test_catalog_retries_stop_at_the_budget(monkeypatch, tmp_path):
    import tglc.ffi_cut as fc

    calls = []

    def broken(*a, **kw):
        calls.append(1)
        raise ConnectionError("Gaia TAP down")

    monkeypatch.setattr(fc, "Source_cut", broken)
    monkeypatch.setattr(tglcrun.time, "sleep", lambda s: pytest.fail("slept past the budget"))
    spec = {"cutout": str(tmp_path / "c.fits"), "center": [1, 2], "size": 90, "sector": 57, "limit_mag": 16,
            "backoff": 120}
    with pytest.raises(tglcrun.OutOfTime):
        tglcrun._source_new(spec, lambda m: None, tglcrun.Clock(60, now=Tick(0)))
    assert len(calls) == 1


def fake_tglc(monkeypatch, tmp_path, stop_after):
    """Replace tglc's fits with ones that write a light curve per star and stop after stop_after stars."""
    import tglc.target_lightcurve as tl

    ids = ["100", "200", "300", "400", "500"]
    src = types.SimpleNamespace(gaia=gaia(ids), sector=57, camera=2, ccd=4)
    state = {"t": 0.0}
    clock = tglcrun.Clock(stop_after, now=lambda: state["t"])
    seen = []

    def epsf(source, limit_mag=16, name=None, local_directory="", **kw):
        os.makedirs(os.path.join(local_directory, "epsf", "SPOC"), exist_ok=True)
        np.savez(os.path.join(local_directory, "epsf", "SPOC", "epsf_field_sector_57_ff.npz"), e_psf=np.zeros(1))
        _, _, _, x, _ = tl.get_psf(source)
        for i, gid in enumerate(ids):
            if name is not None and name != f"Gaia DR3 {gid}":
                continue
            if x[i] == tglcrun.OFF_FRAME:
                continue
            tl.fit_lc()
            seen.append(gid)
            state["t"] += 1
            d = os.path.join(local_directory, "lc", "SPOC")
            os.makedirs(d, exist_ok=True)
            open(os.path.join(d, f"hlsp_tglc_tess_ffi_gaiaid-{gid}-s0057-cam2-ccd4_tess_v2_llc.fits"), "w").close()

    monkeypatch.setattr(tl, "epsf", epsf)
    monkeypatch.setattr(tl, "fit_psf", lambda *a, **kw: None)
    monkeypatch.setattr(tl, "fit_lc", lambda *a, **kw: None)
    monkeypatch.setattr(tl, "get_psf", lambda source, **kw: (None, None, None, np.arange(len(ids)), None))
    monkeypatch.setattr(tglcrun, "_source_cached", lambda spec: src)
    spec = {"workdir": str(tmp_path), "reuse": True, "names": ["300"], "field_mag": 16, "skip": ["100"],
            "factor": 2, "limit_mag": 16, "size": 15, "edge_margin": 1}
    return spec, clock, seen


def test_run_does_named_stars_first_and_stops_the_field_at_the_budget(monkeypatch, tmp_path):
    spec, clock, seen = fake_tglc(monkeypatch, tmp_path, stop_after=2)
    res = tglcrun.run(spec, clock)
    assert seen == ["300", "200"]
    assert res["stopped"] and not res["field_complete"]
    assert sorted(res["lcs"]) == ["200", "300"]
    assert res["positions"]["300"] == [12.0, 5.0]
    assert res["epsf"].endswith("epsf_field_sector_57_ff.npz")


def test_run_finishes_the_field_within_the_budget(monkeypatch, tmp_path):
    spec, clock, seen = fake_tglc(monkeypatch, tmp_path, stop_after=100)
    res = tglcrun.run(spec, clock)
    assert seen == ["300", "200", "400"]
    assert not res["stopped"] and res["field_complete"]


def test_main_writes_a_stopped_result(monkeypatch, tmp_path):
    spec, clock, seen = fake_tglc(monkeypatch, tmp_path, stop_after=0)
    spec["deadline"] = 0
    path = tmp_path / "spec.json"
    path.write_text(json.dumps(spec))
    monkeypatch.setattr(tglcrun.sys, "stderr", tglcrun.sys.stderr)
    assert tglcrun.main(str(path)) == 0
    res = json.loads((tmp_path / "result.json").read_text())
    assert res["ok"] and res["stopped"] and seen == []


def test_a_field_error_keeps_the_named_light_curves(monkeypatch, tmp_path):
    import tglc.target_lightcurve as tl

    spec, clock, seen = fake_tglc(monkeypatch, tmp_path, stop_after=100)
    named = tl.epsf

    def epsf(source, name=None, **kw):
        if name is None:
            raise IndexError("broken field")
        return named(source, name=name, **kw)

    monkeypatch.setattr(tl, "epsf", epsf)
    res = tglcrun.run(spec, clock)
    assert sorted(res["lcs"]) == ["300"] and res["field_complete"] and "broken field" in res["field_error"]


def test_no_pause_after_the_last_catalog_attempt(monkeypatch, tmp_path):
    import tglc.ffi_cut as fc

    monkeypatch.setattr(fc, "Source_cut", lambda *a, **kw: (_ for _ in ()).throw(ConnectionError("down")))
    sleeps = []
    monkeypatch.setattr(tglcrun.time, "sleep", sleeps.append)
    spec = {"cutout": str(tmp_path / "c.fits"), "center": [1, 2], "size": 90, "sector": 57, "limit_mag": 16,
            "backoff": 1, "attempts": 3}
    with pytest.raises(RuntimeError):
        tglcrun._source_new(spec, lambda m: None)
    assert sleeps == [1, 2]
