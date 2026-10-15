"""Checks that need JPL's ephemeris files: set SBX_EPHEMERIS to their directory."""

import os

import numpy as np
import pytest

from sbx import core, engine, orbits, verify

D = os.environ.get("SBX_EPHEMERIS")
pytestmark = pytest.mark.skipif(not D, reason="SBX_EPHEMERIS not set")


@pytest.fixture(scope="module")
def eng():
    return engine.Engine(os.path.join(D, "linux_p1550p2650.440"), os.path.join(D, "sb441-n16.bsp"))


def test_testpo(eng):
    n, dpos, dvel = verify.testpo(eng.ephem, os.path.join(D, "testpo.440"))
    assert n > 10000 and dpos < 1e-12 and dvel < 1e-14


def test_light_time_series_matches_integration(eng):
    mpc = orbits.parse_mpcorb(
        "-----\n"
        "K16Ge1X 19.79  0.15 K2669 341.15386   33.31778  149.34699    6.48910  0.1106303  0.28229265   2.3014577  8 E2026-T09    10   1    4 days 0.69 M-v 3Ek Pan        2000          2016 GX401         20160405\n"
    )
    t = core.utc_to_tdb(2460735.844356)
    state = eng.states_at(mpc, t)
    obs = core.observer(eng.ephem, t)
    sun, _ = core.body_state(eng.ephem, "Sun", t)
    rp, _, lt = core.light_time_correct(state, obs.pos, sun)
    exact = core.propagate(eng.ephem, t, state, t - lt[0])
    assert np.linalg.norm(rp[0] - exact[0, :3]) * core.AU_KM < 0.01
