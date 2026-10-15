import numpy as np
import pytest

from sbx import catalog, core, grid, orbits, query, server

MPCORB = """header
-----
00001    3.34  0.15 K2669 274.41935   73.29420   80.24863   10.58803  0.0796923  0.21430445   2.7655526  0 E2026-SD4  7376 127 1801-2026 0.82 M-v 30k MPCORBFIT  4000      (1) Ceres              20260922
K16Ge1X 19.79  0.15 K2669 341.15386   33.31778  149.34699    6.48910  0.1106303  0.28229265   2.3014577  8 E2026-T09    10   1    4 days 0.69 M-v 3Ek Pan        2000          2016 GX401         20160405
"""
COMETS = """    CJ95O010  1997 03 29.0288  0.925899  0.994895  130.7305  281.8091   89.7338  20261014  -2.0  4.0  C/1995 O1 (Hale-Bopp)                                    MPC194091
0002P         2027 02 10.2277  0.338604  0.847319  187.2873  334.0197   11.3480  20261014  14.4  4.0  2P/Encke                                                 MPC xxxxx
"""


def test_mpcorb():
    o = orbits.parse_mpcorb(MPCORB)
    assert list(o.number) == ["1", ""]
    assert list(o.name) == ["Ceres", "2016 GX401"]
    assert o.epoch[0] == 2461200.5
    assert o.U[1] == 8
    a = o.q / (1 - o.e)
    assert a[0] == pytest.approx(2.7655526, abs=1e-9)
    m = np.degrees(np.sqrt(orbits.GM_SUN_GAUSS / a[0] ** 3) * (o.epoch[0] - o.tp[0]))
    assert m % 360 == pytest.approx(274.41935, abs=1e-8)
    assert catalog.mpc_arcs(MPCORB) == {"00001": pytest.approx(225 * 365.25), "K16Ge1X": 4.0}


def test_comets():
    c = orbits.parse_comets(COMETS)
    assert list(c.name) == ["C/1995 O1 (Hale-Bopp)", "2P/Encke"]
    assert c.tp[0] == pytest.approx(orbits.calendar_jd(1997, 3, 29.0288))
    assert c.kind.tolist() == [orbits.KIND_COMET] * 2
    assert [catalog.comet_key(n) for n in c.name] == ["1995 O1", "2P"]
    assert catalog.comet_key("73P-B/Schwassmann-Wachmann") == "73P-B"


def test_calendar_and_epochs():
    assert orbits.calendar_jd(2000, 1, 1.5) == 2451545.0
    assert orbits.unpack_epoch("K2669") == 2461200.5
    assert orbits.unpack_epoch("J9611") == orbits.calendar_jd(1996, 1, 1)


def test_save_load(tmp_path):
    o = orbits.parse_mpcorb(MPCORB)
    o.nongrav[0] = [1e-9, 2e-10, 0]
    p = tmp_path / "o.npz.zst"
    o.save(str(p))
    back = orbits.Orbits.load(str(p))
    assert back.name.tolist() == o.name.tolist()
    assert np.array_equal(back.nongrav, o.nongrav)


@pytest.mark.parametrize("e", [0.0, 0.3, 0.97, 0.979, 0.981, 0.999, 1.0, 1.001, 1.019, 1.021, 2.5])
def test_kepler_conserves_energy_and_momentum(e):
    q = 1.2
    dt = np.array([-400.0, -3.0, 0.0, 0.5, 30.0, 700.0])
    pos, vel = core.perifocal_state(np.full(6, q), np.full(6, e), dt)
    mu = core.GM_SUN
    r = np.linalg.norm(pos, axis=1)
    energy = 0.5 * np.sum(vel**2, axis=1) - mu / r
    assert np.allclose(energy, -mu * (1 - e) / (2 * q), rtol=0, atol=1e-13)
    h = pos[:, 0] * vel[:, 1] - pos[:, 1] * vel[:, 0]
    assert np.allclose(h, np.sqrt(mu * q * (1 + e)), rtol=1e-12)
    assert r[2] == pytest.approx(q, abs=1e-14)


def test_kepler_branches_agree_at_the_boundaries():
    for e in (0.98, 1.02):
        dt = np.array([50.0])
        a = core.perifocal_state([1.0], [e - 1e-9], dt)[0]
        b = core.perifocal_state([1.0], [e + 1e-9], dt)[0]
        assert np.allclose(a, b, atol=1e-8)


def test_grid_codec_and_hermite():
    rng = np.random.default_rng(1)
    s = rng.normal(size=(1000, 6)).astype(np.float32)
    assert np.array_equal(grid.decode(grid.encode(s)), s)
    t0, t1 = 10.0, 14.0
    f = lambda t: np.array([[t**3, 2 * t**2, -t, 3 * t**2, 4 * t, -1.0]])
    p, v = grid.hermite(12.5, t0, t1, f(t0), f(t1))
    assert np.allclose(p, f(12.5)[:, :3])
    assert np.allclose(v, f(12.5)[:, 3:])


def test_polygon():
    poly = query.Polygon([(10, -1), (12, -1), (12, 1), (10, 1)])
    pts = query.unit(np.array([11.0, 9.9, 11.0, 191.0]), np.array([0.0, 0.0, 0.99, 0.0]))
    assert poly.contains(pts).tolist() == [True, False, True, False]
    wrap = query.Polygon([(359, 0), (1, 0), (1, 1), (359, 1)])
    assert wrap.contains(query.unit(np.array([0.2]), np.array([0.5]))).tolist() == [True]


def test_skybot_text_round_trip():
    meta = {"orbits": "2026-10-14"}
    rows = [
        {"number": "433", "name": "Eros", "class": "NEA>Amor", "kind": "asteroid", "ra": 188.1, "dec": -4.25, "v": 17.25, "dra": -20.5, "ddec": 10.0, "sep_arcsec": 3.0, "delta_au": 0.5, "r_au": 1.4, "phase_deg": 10.0, "elong_deg": 120.0, "helio": [1, 2, 3], "helio_vel": [0.1, 0.2, 0.3], "orbit_epoch_jd": 2461200.5},
        {"number": "", "name": "Jupiter", "class": "Planet", "kind": "planet", "ra": 1, "dec": 1, "v": None, "dra": None, "ddec": None, "sep_arcsec": 1, "delta_au": 5},
    ]
    text = server.skybot_text(meta, rows).splitlines()
    assert len(text) == 4
    f = [x.strip() for x in text[3].split("|")]
    assert f[0] == "433" and f[2] == "12 32 24.0000" and f[3] == "-04 15 00.000" and f[5] == "17.2"


def test_parse_epoch():
    assert server.parse_epoch("2451545.0") == 2451545.0
    assert server.parse_epoch("2000-01-01T12:00:00Z") == pytest.approx(2451545.0)
    with pytest.raises(ValueError):
        server.parse_epoch("12.5")
