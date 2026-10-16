import io
import json
from datetime import datetime, timezone

import numpy as np
import pytest
import requests

from tessvet import job as jobmod
from tessvet import lightcurve, mast
from tessvet.store import Base, LocalStore, unzst, zst

GID = "381299950937609856"
NB = "381299950929510272"


class MemStore(Base):
    def __init__(self):
        self.objs = {}
        self.t = 0

    def put(self, key, data, content_type="application/octet-stream"):
        self.t += 1
        self.objs[key] = (bytes(data), datetime.fromtimestamp(1_700_000_000 + self.t, timezone.utc))

    def get(self, key):
        return self.objs[key][0]

    def put_file(self, key, path, content_type="application/octet-stream"):
        with open(path, "rb") as f:
            self.put(key, f.read())

    def get_file(self, key, path):
        with open(path, "wb") as f:
            f.write(self.get(key))

    def exists(self, key):
        return key in self.objs

    def list(self, prefix):
        return sorted((k, v[1]) for k, v in self.objs.items() if k.startswith(prefix))

    def delete(self, key):
        self.objs.pop(key, None)


def write_tglc_fits(path, gid, sector, n=50, dip=None, v2=True):
    from astropy.io import fits

    t = 1764.7 + np.arange(n) * 0.02
    cal = np.ones(n) * 1.02
    if dip is not None:
        cal[dip] = 0.6
    tess = np.zeros(n, dtype=np.int32)
    tess[3] = 128
    tglcf = np.zeros(n, dtype=np.int32)
    tglcf[5] = 2
    t[7] = np.nan
    cols = [fits.Column(name="time", array=t, format="D"),
            fits.Column(name="psf_flux", array=cal * 200, format="E"),
            fits.Column(name="aperture_flux", array=cal * 190, format="E"),
            fits.Column(name="cal_psf_flux", array=cal, format="E"),
            fits.Column(name="cal_aper_flux", array=cal * 0.5, format="E"),
            fits.Column(name="TESS_flags", array=tess, format="J"),
            fits.Column(name="TGLC_flags", array=tglcf, format="J")]
    p = fits.PrimaryHDU()
    p.header["OBJECT"] = f"Gaia DR3 {gid}"
    p.header["SECTOR"] = sector
    p.header["TESSMAG"] = 14.5
    tb = fits.BinTableHDU.from_columns(cols)
    tb.header["CPSF_ERR"] = 0.0102
    tb.header["CAPE_ERR"] = 0.051
    fits.HDUList([p, tb]).writeto(path, overwrite=True)


def rows(csv: bytes):
    lines = csv.decode().strip().split("\n")
    return lines[0], [line.split(",") for line in lines[1:]]


def test_lightcurve_contract(tmp_path):
    f = tmp_path / "lc.fits"
    write_tglc_fits(f, GID, 17, dip=[10, 11])
    csv, meta = lightcurve.from_fits(str(f))
    head, r = rows(csv)
    assert head == "bjd,flux,flux_err,aper_flux,aper_flux_err,quality"
    assert len(r) == 49
    assert float(r[0][0]) == pytest.approx(2457000 + 1764.7, abs=1e-6)
    assert float(r[0][1]) == pytest.approx(1.0, abs=1e-6)
    assert float(r[0][2]) == pytest.approx(0.0102 / 1.02, abs=1e-6)
    assert float(r[0][3]) == pytest.approx(1.0, abs=1e-6)
    assert float(r[0][4]) == pytest.approx(0.051 / 0.51, abs=1e-6)
    assert float(r[10][1]) == pytest.approx(0.6 / 1.02, abs=1e-5)
    assert r[3][5] == "128"
    assert int(r[5][5]) == 2 << 24
    assert all(x[5] == "0" for i, x in enumerate(r) if i not in (3, 5))
    assert meta["sector"] == 17 and meta["good"] == 47


def test_lightcurve_nan_written_for_go():
    csv = lightcurve.from_table([1.0, 2.0], [np.nan, 1.0], [1.0, 1.0], [0, 0], [0, 0], np.nan, 0.1)
    _, r = rows(csv)
    assert r[0][1] == "NaN" and r[0][2] == "NaN" and r[1][1] == "1.000000"


def test_hlsp_url():
    assert mast.hlsp_url(GID, 17, 2, 4) == (
        "https://archive.stsci.edu/hlsps/tglc/s0017/cam2-ccd4/0038/1299/9509/3760/"
        "hlsp_tglc_tess_ffi_gaiaid-381299950937609856-s0017-cam2-ccd4_tess_v1_llc.fits")
    assert "/0021/7930/9875/3685/" in mast.hlsp_url("2179309875368546432", 16, 2, 4)


class FakeResp:
    def __init__(self, status, body=b"", js=None):
        self.status_code = status
        self.body = body
        self.js = js

    def json(self):
        return self.js

    def iter_content(self, n):
        yield self.body

    def raise_for_status(self):
        if self.status_code >= 400:
            raise requests.HTTPError(str(self.status_code))

    def close(self):
        pass


class FakeSession:
    def __init__(self, responses):
        self.responses = list(responses)
        self.calls = []
        self.headers = {}

    def get(self, url, params=None, timeout=None, stream=False):
        self.calls.append((url, params))
        r = self.responses.pop(0)
        if isinstance(r, Exception):
            raise r
        return r


def test_sectors_retry_and_parse():
    js = {"results": [{"sectorName": "tess-s0057-2-4", "sector": "0057", "camera": "2", "ccd": "4"},
                      {"sectorName": "tess-s0017-2-4", "sector": "0017", "camera": "2", "ccd": "4"}]}
    sleeps = []
    s = FakeSession([FakeResp(503), requests.ConnectionError("x"), FakeResp(200, js=js)])
    c = mast.Client(s, sleep=sleeps.append, backoff=1)
    assert c.sectors(10.9, 41.7) == [{"sector": 17, "camera": 2, "ccd": 4}, {"sector": 57, "camera": 2, "ccd": 4}]
    assert sleeps == [1, 2]


def test_retries_exhausted_and_404():
    c = mast.Client(FakeSession([FakeResp(502)] * 3), attempts=3, sleep=lambda s: None)
    with pytest.raises(mast.Transient):
        c.sectors(1, 2)
    c = mast.Client(FakeSession([FakeResp(404)]), sleep=lambda s: None)
    with pytest.raises(mast.NotFound):
        c.hlsp(GID, 17, 2, 4, "/nonexistent/x")


class FakeMast:
    def __init__(self, tmp, sectors, hlsp=None, cutouts=None):
        self.tmp = tmp
        self._sectors = sectors
        self._hlsp = hlsp or {}
        self.cutouts = []
        self.hlsp_calls = []

    def sectors(self, ra, dec):
        if isinstance(self._sectors, Exception):
            raise self._sectors
        return self._sectors

    def hlsp(self, gid, sector, cam, ccd, path):
        self.hlsp_calls.append((gid, sector))
        if (gid, sector) not in self._hlsp:
            raise mast.NotFound(gid)
        write_tglc_fits(path, gid, sector, dip=self._hlsp[(gid, sector)])

    def cutout(self, ra, dec, size, sector, path, scratch):
        from astropy.io import fits
        from astropy.wcs import WCS

        self.cutouts.append((ra, dec, sector))
        w = WCS(naxis=2)
        w.wcs.crpix = [45.5, 45.5]
        w.wcs.crval = [ra, dec]
        w.wcs.cdelt = [-21 / 3600, 21 / 3600]
        w.wcs.ctype = ["RA---TAN", "DEC--TAN"]
        p = fits.PrimaryHDU()
        p.header["SECTOR"] = sector
        fits.HDUList([p, fits.BinTableHDU.from_columns([fits.Column(name="x", array=[1], format="J")]),
                      fits.ImageHDU(header=w.to_header())]).writeto(path)


def request(slug="m31", gid=GID, when="2026-10-15T12:00:00Z", **kw):
    doc = {"gaia_id": gid, "ra": 10.889321, "dec": 41.662433, "pmra": 2.1, "pmdec": -0.9, "g": 15.04,
           "neighbours": [{"gaia_id": NB, "ra": 10.8907, "dec": 41.6628, "g": 17.2, "sep_arcsec": 3.9}],
           "field": {"object": "M 31", "slug": slug, "ra": 10.68, "dec": 41.27, "radius": 1.0},
           "requested_at": when}
    doc.update(kw)
    return doc


def fake_runner(tmp, calls, produce=(GID, NB), fail=None, dip=20):
    def run(spec):
        calls.append(dict(spec))
        if fail:
            return fail
        lcs = {}
        for gid in produce:
            p = tmp / f"tglc-{gid}-{spec['sector']}.fits"
            write_tglc_fits(p, gid, spec["sector"], dip=[dip])
            lcs[gid] = str(p)
        epsf = tmp / "epsf_field_sector_57_abc.npz"
        np.savez(epsf, e_psf=np.zeros((2, 2)))
        with open(spec["catalog"], "wb") as f:
            f.write(b"catalog")
        return {"ok": True, "lcs": lcs, "errors": {}, "epsf": str(epsf), "camera": 2, "ccd": 4, "n_gaia": 10}
    return run


def make_job(tmp, store, fm, runner, minutes=60):
    cfg = jobmod.Config(workdir=str(tmp / "work"), max_minutes=minutes)
    return jobmod.Job(store, fm, cfg, runner=runner)


SECTORS = [{"sector": 17, "camera": 2, "ccd": 4}, {"sector": 57, "camera": 2, "ccd": 4}]


def test_hlsp_then_tglc_then_cache_reuse(tmp_path):
    st = MemStore()
    st.put_json(f"vetting/tess/requests/{GID}.json", request())
    fm = FakeMast(tmp_path, SECTORS, hlsp={(GID, 17): [4], (NB, 17): None})
    calls = []
    make_job(tmp_path, st, fm, fake_runner(tmp_path, calls)).run()

    status = st.get_json(f"vetting/cache/m31/tess/{GID}/status.json")
    assert status["gaia_id"] == GID
    assert status["sectors_available"] == [17, 57]
    assert status["sectors_done"] == [17, 57]
    assert status["sectors_failed"] == {}
    datetime.strptime(status["updated_at"], "%Y-%m-%dT%H:%M:%SZ")
    head, r = rows(unzst(st.get(f"vetting/cache/m31/tess/{GID}/s0017.csv.zst")))
    assert head == lightcurve.HEADER and float(r[4][1]) < 0.7
    assert st.exists(f"vetting/cache/m31/tess/{GID}/s0057.csv.zst")
    assert st.exists(f"vetting/cache/m31/tess/lc/s0017/{NB}.csv.zst")
    assert st.exists(f"vetting/cache/m31/tess/lc/s0057/{NB}.csv.zst")
    assert not st.exists(f"vetting/tess/requests/{GID}.json")
    assert len(fm.cutouts) == 1 and calls[0]["reuse"] is False and calls[0]["names"] == [GID, NB]
    prefix = "vetting/cache/m31/tess/cutouts/s0057/10.88932+41.66243/"
    meta = st.get_json(prefix + "meta.json")
    assert meta["epsf_name"] == "epsf_field_sector_57_abc.npz" and meta["size"] == 90
    for k in ("cutout.fits.zst", "catalog.pkl.zst", "epsf.npz.zst"):
        assert st.exists(prefix + k)

    st.put_json(f"vetting/tess/requests/{NB}.json", request(gid=NB, ra=10.8907, dec=41.6628, neighbours=[]))
    calls.clear()
    make_job(tmp_path, st, fm, fake_runner(tmp_path, calls)).run()
    assert calls == [] and len(fm.cutouts) == 1
    assert st.get(f"vetting/cache/m31/tess/{NB}/s0057.csv.zst") == st.get(f"vetting/cache/m31/tess/lc/s0057/{NB}.csv.zst")

    other = "381299946636125440"
    fm._sectors = SECTORS[1:]
    st.put_json(f"vetting/tess/requests/{other}.json",
                request(gid=other, ra=10.95, dec=41.70, neighbours=[]))
    before = st.get(f"vetting/cache/m31/tess/lc/s0057/{GID}.csv.zst")
    make_job(tmp_path, st, fm, fake_runner(tmp_path, calls, produce=(other, GID), dip=30)).run()
    assert st.get(f"vetting/cache/m31/tess/lc/s0057/{GID}.csv.zst") == before
    assert len(fm.cutouts) == 1
    assert calls[0]["reuse"] is True and calls[0]["epsf_name"] == "epsf_field_sector_57_abc.npz"
    assert calls[0]["names"] == [other] and calls[0]["field_mag"] is None
    assert st.get_json(f"vetting/cache/m31/tess/{other}/status.json")["sectors_done"] == [57]


def test_far_star_gets_its_own_cutout(tmp_path):
    st = MemStore()
    st.put_json(f"vetting/tess/requests/{GID}.json", request(neighbours=[]))
    fm = FakeMast(tmp_path, SECTORS[1:])
    calls = []
    make_job(tmp_path, st, fm, fake_runner(tmp_path, calls, produce=(GID,))).run()
    far = "381299946636125441"
    st.put_json(f"vetting/tess/requests/{far}.json", request(gid=far, ra=11.2, dec=41.66, neighbours=[]))
    make_job(tmp_path, st, fm, fake_runner(tmp_path, calls, produce=(far,))).run()
    assert len(fm.cutouts) == 2 and calls[1]["reuse"] is False


def test_permanent_and_transient_failures(tmp_path):
    st = MemStore()
    st.put_json(f"vetting/tess/requests/{GID}.json", request(neighbours=[]))
    fm = FakeMast(tmp_path, SECTORS)
    calls = []
    perm = {"ok": False, "permanent": True, "error": "Target does not have one verified Gaia match"}
    make_job(tmp_path, st, fm, fake_runner(tmp_path, calls, fail=perm)).run()
    status = st.get_json(f"vetting/cache/m31/tess/{GID}/status.json")
    assert status["sectors_done"] == []
    assert set(status["sectors_failed"]) == {"17", "57"}
    assert "Gaia match" in status["sectors_failed"]["57"]
    assert not st.exists(f"vetting/tess/requests/{GID}.json")

    st2 = MemStore()
    key = f"vetting/tess/requests/{GID}.json"
    st2.put_json(key, request(neighbours=[]))
    flaky = {"ok": False, "permanent": False, "error": "Gaia TAP timeout"}
    for i in range(3):
        make_job(tmp_path, st2, FakeMast(tmp_path, SECTORS[1:]), fake_runner(tmp_path, calls, fail=flaky)).run()
        if i < 2:
            assert st2.get_json(key)["attempts"] == {"57": i + 1}
            assert st2.get_json(f"vetting/cache/m31/tess/{GID}/status.json")["sectors_failed"] == {}
    assert not st2.exists(key)
    assert "gave up after 3 runs" in st2.get_json(f"vetting/cache/m31/tess/{GID}/status.json")["sectors_failed"]["57"]


def test_sector_lookup_outage_keeps_request(tmp_path):
    st = MemStore()
    st.put_json(f"vetting/tess/requests/{GID}.json", request())
    make_job(tmp_path, st, FakeMast(tmp_path, mast.Transient("down")), fake_runner(tmp_path, [])).run()
    assert st.exists(f"vetting/tess/requests/{GID}.json")


def test_queue_order_budget_and_bad_requests(tmp_path):
    st = MemStore()
    st.put_json("vetting/tess/requests/2.json", request(gid="2", when="2026-10-15T12:00:00Z", neighbours=[]))
    st.put_json("vetting/tess/requests/1.json", request(gid="1", when="2026-10-14T12:00:00Z", neighbours=[]))
    st.put_json("vetting/tess/requests/bad.json", request(slug="../escape"))
    st.put("vetting/tess/requests/junk.json", b"not json")
    j = make_job(tmp_path, st, FakeMast(tmp_path, []), fake_runner(tmp_path, []))
    assert [k for k, _ in j.queue()] == ["vetting/tess/requests/1.json", "vetting/tess/requests/2.json",
                                         "vetting/tess/requests/bad.json"]
    j.run()
    assert not st.exists("vetting/tess/requests/bad.json")
    assert not st.exists("vetting/tess/requests/1.json")
    assert st.get_json("vetting/cache/m31/tess/1/status.json")["sectors_available"] == []

    st.put_json("vetting/tess/requests/3.json", request(gid="3", neighbours=[]))
    make_job(tmp_path, st, FakeMast(tmp_path, SECTORS), fake_runner(tmp_path, []), minutes=0).run()
    assert st.exists("vetting/tess/requests/3.json")


def test_local_store_roundtrip(tmp_path):
    s = LocalStore(str(tmp_path / "s"))
    s.put_json("a/b.json", {"x": 1})
    s.put_zst("a/c.zst", b"hello")
    assert s.get_json("a/b.json") == {"x": 1}
    assert s.get_zst("a/c.zst") == b"hello"
    assert unzst((tmp_path / "s/a/c.zst").read_bytes()) == b"hello"
    big = tmp_path / "big.bin"
    big.write_bytes(b"\0" * 100000)
    s.put_file_zst("a/big.zst", str(big), str(tmp_path))
    out = tmp_path / "back.bin"
    s.get_file_zst("a/big.zst", str(out), str(tmp_path))
    assert out.read_bytes() == big.read_bytes()
    assert [k for k, _ in s.list("a/")] == ["a/b.json", "a/big.zst", "a/c.zst"]
    s.delete("a/b.json")
    assert not s.exists("a/b.json")
    with pytest.raises(ValueError):
        s.put("../x", b"")
    assert zst(b"") is not None
    _ = io, json
