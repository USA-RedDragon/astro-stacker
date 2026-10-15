"""Downloads of the orbit catalogues and the merged catalogue the service uses.

Numbered asteroids use JPL's orbits (full precision, with non-gravitational
parameters), unnumbered ones whichever of MPC's and JPL's orbits covers the
longer arc, and comets JPL's orbits with their non-gravitational parameters,
falling back to MPC for objects JPL does not have yet.
"""

from __future__ import annotations

import gzip
import json
import re
import time
import urllib.parse
import urllib.request

import numpy as np

from .orbits import KIND_ASTEROID, KIND_COMET, Orbits, parse_comets, parse_mpcorb

MPCORB_URL = "https://minorplanetcenter.net/iau/MPCORB/MPCORB.DAT.gz"
COMETELS_URL = "https://minorplanetcenter.net/iau/MPCORB/CometEls.txt"
SBDB_URL = "https://ssd-api.jpl.nasa.gov/sbdb_query.api"
SBDB_FIELDS = "pdes,prefix,epoch,e,q,i,om,w,tp,ma,H,G,M1,K1,A1,A2,A3,DT,data_arc,n_obs_used,condition_code"
SBDB_PAGE = 100000
USER_AGENT = "astro-small-bodies/1 (+https://github.com/USA-RedDragon/astro-stacker)"
_PERIODIC = re.compile(r"^(\d+[PDI](?:-[A-Z]+)?)(?:/|$)")
SOURCE_MPC = 0
SOURCE_JPL = 1


def http_get(url: str, timeout: float = 600, retries: int = 5) -> bytes:
    req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    for attempt in range(retries):
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return resp.read()
        except Exception:
            if attempt == retries - 1:
                raise
            time.sleep(10 * (attempt + 1))
    raise RuntimeError("unreachable")


def fetch_mpcorb() -> bytes:
    """MPCORB.DAT, decompressed; MPC serves it gzipped."""
    return gzip.decompress(http_get(MPCORB_URL))


def fetch_cometels() -> bytes:
    return http_get(COMETELS_URL)


def fetch_sbdb(kind: str, numbered: bool | None = None) -> dict:
    """All rows of an SBDB query for asteroids (kind a) or comets (kind c), paged."""
    q = {"fields": SBDB_FIELDS, "sb-kind": kind, "full-prec": "1", "limit": str(SBDB_PAGE)}
    if numbered is not None:
        q["sb-ns"] = "n" if numbered else "u"
    out = None
    start = 0
    while True:
        q["limit-from"] = str(start)
        doc = json.loads(http_get(SBDB_URL + "?" + urllib.parse.urlencode(q)))
        rows = doc.get("data") or []
        if out is None:
            out = {"fields": doc["fields"], "count": int(doc.get("count", 0)), "data": []}
        out["data"].extend(rows)
        start += len(rows)
        if len(rows) < SBDB_PAGE or start >= out["count"]:
            break
    if out["count"] and len(out["data"]) != out["count"]:
        raise RuntimeError(f"sbdb {kind}: got {len(out['data'])} of {out['count']} rows")
    return out


def _num(v, default=np.nan):
    if v is None or v == "":
        return default
    return float(v)


def parse_sbdb(doc: dict, kind: int) -> tuple[Orbits, np.ndarray]:
    """SBDB rows as Orbits, and each orbit's data arc in days."""
    f = {name: k for k, name in enumerate(doc["fields"])}
    names, numbers, packed, rows, ng, arcs = [], [], [], [], [], []
    for r in doc["data"]:
        try:
            epoch = float(r[f["epoch"]])
            q, e = float(r[f["q"]]), float(r[f["e"]])
            inc, node, peri = float(r[f["i"]]), float(r[f["om"]]), float(r[f["w"]])
        except (TypeError, ValueError):
            continue
        tp = _num(r[f["tp"]])
        if np.isnan(tp):
            ma = _num(r[f["ma"]])
            if np.isnan(ma) or e >= 1:
                continue
            a = q / (1 - e)
            tp = epoch - np.radians(ma) / np.sqrt(0.01720209895**2 / a**3)
        pdes = r[f["pdes"]].strip()
        if kind == KIND_COMET:
            h, g = _num(r[f["M1"]]), _num(r[f["K1"]], 10.0) / 2.5
            prefix = (r[f["prefix"]] or "").strip()
            m = _PERIODIC.match(pdes)
            number = re.match(r"\d+", pdes).group(0) if m else ""
            name = pdes if m else (f"{prefix}/{pdes}" if prefix else pdes)
        else:
            h, g = _num(r[f["H"]]), _num(r[f["G"]], 0.15)
            number = pdes if pdes.isdigit() else ""
            name = pdes
        names.append(name)
        numbers.append(number)
        packed.append(pdes)
        rows.append((epoch, q, e, inc, node, peri, tp, h, g))
        ng.append([_num(r[f[k]], 0.0) for k in ("A1", "A2", "A3")])
        arcs.append(_num(r[f["data_arc"]], 0.0))
    n = len(rows)
    col = np.asarray(rows, dtype=np.float64).reshape(n, 9)
    o = Orbits(
        name=np.asarray(names, dtype=str),
        number=np.asarray(numbers, dtype=str),
        packed=np.asarray(packed, dtype=str),
        kind=np.full(n, kind, dtype=np.int8),
        epoch=col[:, 0],
        q=col[:, 1],
        e=col[:, 2],
        inc=col[:, 3],
        node=col[:, 4],
        peri=col[:, 5],
        tp=col[:, 6],
        H=col[:, 7],
        G=col[:, 8],
        U=np.full(n, -1, dtype=np.int8),
        mpc_type=np.zeros(n, dtype=np.int8),
        nongrav=np.asarray(ng, dtype=np.float64).reshape(n, 3),
    )
    return o, np.asarray(arcs, dtype=np.float64)


def mpc_arcs(text: str) -> dict[str, float]:
    """Observed arc in days of each MPCORB object, by packed designation."""
    out = {}
    started = False
    for line in text.splitlines():
        if not started:
            started = line.startswith("-----")
            continue
        if len(line) < 140 or not line[0:7].strip():
            continue
        arc = line[127:136].strip()
        try:
            if arc.endswith("days"):
                days = float(arc.split()[0])
            else:
                y1, y2 = arc.split("-")
                days = (int(y2) - int(y1)) * 365.25
        except ValueError:
            days = 0.0
        out[line[0:7].strip()] = days
    return out


def comet_key(name: str) -> str:
    """The designation shared by MPC's and JPL's comet records: 2P, 73P-B or 2023 A3."""
    base = name.split(" (")[0].strip()
    m = _PERIODIC.match(base)
    if m:
        return m.group(1)
    head, _, tail = base.partition("/")
    return tail or base


def merge(mpcorb: str, cometels: str, numbered: dict, unnumbered: dict, comets: dict) -> tuple[Orbits, np.ndarray]:
    """The service's catalogue and the source (SOURCE_MPC or SOURCE_JPL) of each orbit.

    Names, numbers, MPC orbit types, uncertainty parameters and H, G come
    from MPC when it has the object.
    """
    mpc = parse_mpcorb(mpcorb)
    arcs = mpc_arcs(mpcorb)
    mpc_arc = np.array([arcs.get(p, 0.0) for p in mpc.packed])
    jn, _ = parse_sbdb(numbered, KIND_ASTEROID)
    ju, ju_arc = parse_sbdb(unnumbered, KIND_ASTEROID)
    jc, _ = parse_sbdb(comets, KIND_COMET)
    by_number = {n: k for k, n in enumerate(jn.number)}
    by_desig = {d: k for k, d in enumerate(ju.packed)}

    src = np.full(len(mpc), SOURCE_MPC, dtype=np.int8)
    pick_n = np.array([by_number.get(n, -1) if n else -1 for n in mpc.number])
    pick_u = np.array([by_desig.get(d, -1) if not n else -1 for n, d in zip(mpc.number, mpc.name)])
    use_n = pick_n >= 0
    use_u = (pick_u >= 0) & (ju_arc[np.maximum(pick_u, 0)] >= 0.9 * mpc_arc)
    _copy_orbit(mpc, use_n, jn, pick_n[use_n])
    _copy_orbit(mpc, use_u, ju, pick_u[use_u])
    src[use_n | use_u] = SOURCE_JPL

    mc = parse_comets(cometels)
    by_key = {comet_key(n): k for k, n in enumerate(jc.name)}
    pick_c = np.array([by_key.get(comet_key(n), -1) for n in mc.name])
    use_c = pick_c >= 0
    _copy_orbit(mc, use_c, jc, pick_c[use_c])
    mpc.source = src
    mc.source = np.where(use_c, SOURCE_JPL, SOURCE_MPC).astype(np.int8)
    out = Orbits.concat([mpc, mc])
    return out, out.source


def _copy_orbit(dst: Orbits, mask: np.ndarray, src: Orbits, idx: np.ndarray) -> None:
    """Take src's orbits for dst's masked rows, and its H and G where dst has no H."""
    for name in ("epoch", "q", "e", "inc", "node", "peri", "tp", "nongrav"):
        getattr(dst, name)[mask] = getattr(src, name)[idx]
    rows = np.nonzero(mask)[0]
    blank = np.isnan(dst.H[rows])
    dst.H[rows[blank]] = src.H[idx[blank]]
    dst.G[rows[blank]] = src.G[idx[blank]]
