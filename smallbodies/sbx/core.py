"""Element conversion, time scales, observer positions and ASSIST propagation."""

from __future__ import annotations

import math
from dataclasses import dataclass

import assist
import erfa
import numpy as np
import rebound

from .orbits import GM_SUN_GAUSS, Orbits

JD_J2000 = 2451545.0
AU_KM = 149597870.7
C_AU_DAY = 299792.458 * 86400.0 / AU_KM
EARTH_RADIUS_KM = 6378.137
OBLIQUITY = math.radians(84381.448 / 3600.0)
_CE, _SE = math.cos(OBLIQUITY), math.sin(OBLIQUITY)
ECL_TO_EQ = np.array([[1.0, 0.0, 0.0], [0.0, _CE, -_SE], [0.0, _SE, _CE]])
GM_SUN = GM_SUN_GAUSS


def load_ephem(planets: str, asteroids: str) -> assist.Ephem:
    return assist.Ephem(planets_path=planets, asteroids_path=asteroids)


def utc_to_tdb(jd_utc: float) -> float:
    """TDB Julian date of a UTC Julian date (geocentric TDB-TT)."""
    d1, d2 = math.floor(jd_utc - 0.5) + 0.5, jd_utc - (math.floor(jd_utc - 0.5) + 0.5)
    t1, t2 = erfa.utctai(d1, d2)
    t1, t2 = erfa.taitt(t1, t2)
    return t1 + t2 + erfa.dtdb(t1, t2, d2, 0.0, 0.0, 0.0) / 86400.0


def tdb_to_utc(jd_tdb: float) -> float:
    jd = jd_tdb - 69.184 / 86400.0
    for _ in range(3):
        jd += jd_tdb - utc_to_tdb(jd)
    return jd


def _stumpff(z):
    c = np.empty_like(z)
    s = np.empty_like(z)
    pos = z > 1e-8
    neg = z < -1e-8
    mid = ~(pos | neg)
    sz = np.sqrt(z[pos])
    c[pos] = (1 - np.cos(sz)) / z[pos]
    s[pos] = (sz - np.sin(sz)) / sz**3
    sn = np.sqrt(-z[neg])
    c[neg] = (np.cosh(sn) - 1) / -z[neg]
    s[neg] = (np.sinh(sn) - sn) / sn**3
    zm = z[mid]
    c[mid] = 0.5 - zm / 24 + zm**2 / 720
    s[mid] = 1 / 6 - zm / 120 + zm**2 / 5040
    return c, s


def perifocal_state(q, e, dt, mu=GM_SUN):
    """Position and velocity in the perifocal frame dt days after perihelion.

    Closed forms for clearly elliptic and hyperbolic orbits; universal
    variables near e = 1, where both lose precision.
    """
    q = np.atleast_1d(np.asarray(q, dtype=np.float64))
    e = np.atleast_1d(np.asarray(e, dtype=np.float64))
    dt = np.atleast_1d(np.asarray(dt, dtype=np.float64))
    x = np.empty_like(q)
    y = np.empty_like(q)
    vx = np.empty_like(q)
    vy = np.empty_like(q)
    ell = e < 0.98
    hyp = e > 1.02
    uni = ~(ell | hyp)
    if ell.any():
        qe, ee, de = q[ell], e[ell], dt[ell]
        a = qe / (1 - ee)
        n = np.sqrt(mu / a**3)
        m = np.mod(n * de + np.pi, 2 * np.pi) - np.pi
        big = np.where(ee < 0.8, m + ee * np.sin(m), np.where(m >= 0, np.pi, -np.pi))
        for _ in range(50):
            f = big - ee * np.sin(big) - m
            step = f / (1 - ee * np.cos(big))
            big -= step
            if np.all(np.abs(step) < 1e-15):
                break
        ce, se = np.cos(big), np.sin(big)
        b = np.sqrt(1 - ee * ee)
        den = 1 - ee * ce
        x[ell] = a * (ce - ee)
        y[ell] = a * b * se
        vx[ell] = -a * n * se / den
        vy[ell] = a * n * b * ce / den
    if hyp.any():
        qh, eh, dh = q[hyp], e[hyp], dt[hyp]
        a = qh / (eh - 1)
        n = np.sqrt(mu / a**3)
        m = n * dh
        big = np.arcsinh(m / eh)
        for _ in range(100):
            f = eh * np.sinh(big) - big - m
            step = f / (eh * np.cosh(big) - 1)
            big -= step
            if np.all(np.abs(step) < 1e-15 * np.maximum(1, np.abs(big))):
                break
        ch, sh = np.cosh(big), np.sinh(big)
        b = np.sqrt(eh * eh - 1)
        den = eh * ch - 1
        x[hyp] = a * (eh - ch)
        y[hyp] = a * b * sh
        vx[hyp] = -a * n * sh / den
        vy[hyp] = a * n * b * ch / den
    if uni.any():
        qu, eu, du = q[uni], e[uni], dt[uni]
        sqmu = math.sqrt(mu)
        alpha = (1 - eu) / qu
        ts = sqmu * du
        chi = np.cbrt(6 * ts / np.maximum(eu, 1e-12))
        chi = np.where(np.abs(ts) < qu * 1e-3, ts / qu, chi)
        lo = np.full_like(chi, -np.inf)
        hi = np.full_like(chi, np.inf)
        for _ in range(200):
            z = alpha * chi**2
            c, s = _stumpff(z)
            f = eu * chi**3 * s + qu * chi - ts
            r = eu * chi**2 * c + qu
            lo = np.where(f < 0, np.maximum(lo, chi), lo)
            hi = np.where(f > 0, np.minimum(hi, chi), hi)
            new = chi - f / r
            bad = (new <= lo) | (new >= hi)
            with np.errstate(invalid="ignore"):
                mid = np.where(np.isfinite(lo) & np.isfinite(hi), 0.5 * (lo + hi), np.where(np.isfinite(lo), 2 * np.abs(lo) + 1, -2 * np.abs(hi) - 1))
            new = np.where(bad, mid, new)
            done = np.all(np.abs(new - chi) <= 1e-15 * np.maximum(1, np.abs(chi)))
            chi = new
            if done:
                break
        z = alpha * chi**2
        c, s = _stumpff(z)
        vp = np.sqrt(mu * (1 + eu) / qu)
        fl = 1 - chi**2 * c / qu
        gl = du - chi**3 * s / sqmu
        xu = fl * qu
        yu = gl * vp
        r = np.hypot(xu, yu)
        fdot = sqmu / (r * qu) * (z * s - 1) * chi
        gdot = 1 - chi**2 * c / r
        x[uni] = xu
        y[uni] = yu
        vx[uni] = fdot * qu
        vy[uni] = gdot * vp
    zero = np.zeros_like(x)
    return np.stack([x, y, zero], -1), np.stack([vx, vy, zero], -1)


def helio_ecliptic_state(o: Orbits, t_tt: np.ndarray, mu=GM_SUN):
    """Heliocentric J2000 ecliptic position (au) and velocity (au/day) at t_tt (JD TT)."""
    pos, vel = perifocal_state(o.q, o.e, np.asarray(t_tt) - o.tp, mu)
    w, n, i = np.radians(o.peri), np.radians(o.node), np.radians(o.inc)
    cw, sw, cn, sn, ci, si = np.cos(w), np.sin(w), np.cos(n), np.sin(n), np.cos(i), np.sin(i)
    rot = np.empty((len(o), 3, 3))
    rot[:, 0, 0] = cn * cw - sn * sw * ci
    rot[:, 0, 1] = -cn * sw - sn * cw * ci
    rot[:, 0, 2] = sn * si
    rot[:, 1, 0] = sn * cw + cn * sw * ci
    rot[:, 1, 1] = -sn * sw + cn * cw * ci
    rot[:, 1, 2] = -cn * si
    rot[:, 2, 0] = sw * si
    rot[:, 2, 1] = cw * si
    rot[:, 2, 2] = ci
    return np.einsum("nij,nj->ni", rot, pos), np.einsum("nij,nj->ni", rot, vel)


def body_state(ephem: assist.Ephem, body, t_tdb: float):
    p = ephem.get_particle(body, t_tdb - ephem.jd_ref)
    return np.array([p.x, p.y, p.z]), np.array([p.vx, p.vy, p.vz])


def barycentric_states(ephem: assist.Ephem, o: Orbits) -> np.ndarray:
    """Barycentric ICRF states (N x 6) at each orbit's own epoch."""
    pos, vel = helio_ecliptic_state(o, o.epoch)
    pos = pos @ ECL_TO_EQ.T
    vel = vel @ ECL_TO_EQ.T
    out = np.empty((len(o), 6))
    out[:, :3] = pos
    out[:, 3:] = vel
    sun = {}
    for k, ep in enumerate(o.epoch):
        s = sun.get(ep)
        if s is None:
            s = sun[ep] = np.concatenate(body_state(ephem, "Sun", ep))
        out[k] += s
    return out


@dataclass
class Observer:
    """Barycentric ICRF position (au) and velocity (au/day) of the observer."""

    pos: np.ndarray
    vel: np.ndarray


def site_itrf(lon_deg: float, rho_cos: float, rho_sin: float) -> np.ndarray:
    """Geocentric site vector in km from MPC-style parallax constants (Earth radii)."""
    lon = math.radians(lon_deg)
    return EARTH_RADIUS_KM * np.array([rho_cos * math.cos(lon), rho_cos * math.sin(lon), rho_sin])


def parallax_constants(lat_deg: float, lon_deg: float, height_m: float):
    """MPC parallax constants (longitude, rho cos phi', rho sin phi') of a WGS84 site."""
    xyz = erfa.gd2gc(1, math.radians(lon_deg), math.radians(lat_deg), height_m)
    r = EARTH_RADIUS_KM * 1000.0
    return lon_deg, math.hypot(xyz[0], xyz[1]) / r, xyz[2] / r


def observer(ephem: assist.Ephem, t_tdb: float, site_km: np.ndarray | None = None) -> Observer:
    pos, vel = body_state(ephem, "Earth", t_tdb)
    if site_km is None:
        return Observer(pos, vel)
    t1, t2 = math.floor(t_tdb - 0.5) + 0.5, t_tdb - (math.floor(t_tdb - 0.5) + 0.5)
    jd_utc = tdb_to_utc(t_tdb)
    u1, u2 = math.floor(jd_utc - 0.5) + 0.5, jd_utc - (math.floor(jd_utc - 0.5) + 0.5)
    c2t = erfa.c2t06a(t1, t2, u1, u2, 0.0, 0.0)
    r = c2t.T @ site_km
    omega = 7.292115e-5
    v_itrf = np.cross([0.0, 0.0, omega], site_km)
    v = c2t.T @ v_itrf
    return Observer(pos + r / AU_KM, vel + v * 86400.0 / AU_KM)


MIN_DT = 1e-5
"""Shortest IAS15 step in days (0.86 s). Only an orbit through a planet or
the Moon (an impact, or a poorly determined orbit's nominal path) needs
shorter ones, and without a floor such an orbit takes minutes to hours."""
GRID_MIN_DT = 1e-4
"""The grid's floor (8.6 s): an orbit trapped inside the Earth then costs
under a second per grid interval."""

COMET_G = {"alpha": 0.1112620426, "nk": 4.6142, "nm": 2.15, "nn": 5.093, "r0": 2.808}


def _new_sim(ephem: assist.Ephem, t0_tdb: float, states: np.ndarray, gr: str, nongrav: np.ndarray | None = None, comet: bool = False, min_dt: float = MIN_DT):
    sim = rebound.Simulation()
    sim.t = t0_tdb - ephem.jd_ref
    sim.ri_ias15.adaptive_mode = 1
    sim.ri_ias15.min_dt = min_dt
    for s in states:
        sim.add(x=s[0], y=s[1], z=s[2], vx=s[3], vy=s[4], vz=s[5])
    ex = assist.Extras(sim, ephem)
    if gr != "GR_EIH":
        forces = [f for f in ex.forces if f != "GR_EIH"]
        if gr:
            forces.append(gr)
        ex.forces = forces
    if nongrav is not None and np.any(nongrav):
        ex.particle_params = np.ascontiguousarray(nongrav, dtype=np.float64).ravel()
        if comet:
            for k, v in COMET_G.items():
                setattr(ex, k, v)
    return sim, ex


def propagate(ephem: assist.Ephem, t0_tdb: float, states: np.ndarray, t_tdb: float, gr: str = "GR_EIH", nongrav=None, comet=False) -> np.ndarray:
    """Integrate barycentric states from t0 to t (both JD TDB) with ASSIST.

    Comets' non-gravitational accelerations use the Marsden water-ice g(r);
    asteroids' (Yarkovsky A2) use 1/r^2.
    """
    sim, ex = _new_sim(ephem, t0_tdb, states, gr, nongrav, comet)
    sim.dt = 10.0 if t_tdb >= t0_tdb else -10.0
    ex.integrate_or_interpolate(t_tdb - ephem.jd_ref)
    out = _read(sim, len(states))
    del ex
    return out


def propagate_grid(ephem, t0_tdb, states, times, gr="GR_SIMPLE", epsilon=1e-9, nongrav=None, comet=False, min_dt=GRID_MIN_DT):
    """States (len(times) x N x 6) at each of the sorted times, integrating both ways from t0."""
    times = np.asarray(times)
    out = np.empty((len(times), len(states), 6))
    after = np.nonzero(times >= t0_tdb)[0]
    before = np.nonzero(times < t0_tdb)[0][::-1]
    for idx, direction in ((after, 1.0), (before, -1.0)):
        if not len(idx):
            continue
        sim, ex = _new_sim(ephem, t0_tdb, states, gr, nongrav, comet, min_dt)
        sim.ri_ias15.epsilon = epsilon
        sim.dt = 10.0 * direction
        for k in idx:
            ex.integrate_or_interpolate(times[k] - ephem.jd_ref)
            out[k] = _read(sim, len(states))
        del ex
    return out


def _read(sim, n):
    xyz = np.empty((n, 3))
    v = np.empty((n, 3))
    sim.serialize_particle_data(xyz=xyz, vxvyvz=v)
    return np.hstack([xyz, v])


def light_time_correct(state: np.ndarray, obs_pos: np.ndarray, sun_pos: np.ndarray, iterations: int = 4):
    """Positions and velocities at the emission times, by a Taylor series about t.

    The series uses the Sun's two-body acceleration and jerk; the planets'
    pull over a light time is far below a milliarcsecond for these purposes.
    """
    lt = np.linalg.norm(state[:, :3] - obs_pos, axis=1) / C_AU_DAY
    for _ in range(iterations):
        rp, vp = _taylor(state, -lt, sun_pos)
        lt = np.linalg.norm(rp - obs_pos, axis=1) / C_AU_DAY
    rp, vp = _taylor(state, -lt, sun_pos)
    return rp, vp, lt


def _taylor(state, dt, sun_pos):
    r = state[:, :3] - sun_pos
    v = state[:, 3:]
    rn = np.linalg.norm(r, axis=1)[:, None]
    acc = -GM_SUN * r / rn**3
    rv = np.sum(r * v, axis=1)[:, None]
    jerk = -GM_SUN * (v / rn**3 - 3 * rv * r / rn**5)
    d = dt[:, None]
    pos = state[:, :3] + v * d + 0.5 * acc * d**2 + jerk * d**3 / 6
    vel = v + acc * d + 0.5 * jerk * d**2
    return pos, vel


def radec(vec: np.ndarray):
    x, y, z = vec[:, 0], vec[:, 1], vec[:, 2]
    ra = np.degrees(np.arctan2(y, x)) % 360.0
    dec = np.degrees(np.arctan2(z, np.hypot(x, y)))
    return ra, dec


def aberrate(rho: np.ndarray, obs_vel: np.ndarray) -> np.ndarray:
    """Directions shifted by the observer's velocity (special relativistic aberration)."""
    u = rho / np.linalg.norm(rho, axis=1)[:, None]
    beta = obs_vel / C_AU_DAY
    b = np.linalg.norm(beta)
    gamma_inv = math.sqrt(1 - b * b)
    pb = u @ beta
    out = (gamma_inv * u + (1 + pb / (1 + gamma_inv))[:, None] * beta) / (1 + pb)[:, None]
    return out

