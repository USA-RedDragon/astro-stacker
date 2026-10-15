"""Orbit catalogues: MPC's MPCORB.DAT and CometEls.txt, and JPL's ELEMENTS files.

Every catalogue becomes one Orbits table: heliocentric osculating elements
referred to the J2000 ecliptic, in the order of the source file.
"""

from __future__ import annotations

import io
from dataclasses import dataclass, fields

import numpy as np
import zstandard

KIND_ASTEROID = 0
KIND_COMET = 1

MPC_TYPES = {
    1: "NEA>Atira",
    2: "NEA>Aten",
    3: "NEA>Apollo",
    4: "NEA>Amor",
    5: "Mars-Crosser",
    6: "Hungaria",
    7: "MB>Phocaea",
    8: "MB>Hilda",
    9: "Trojan",
}


@dataclass
class Orbits:
    """Osculating elements; angles in degrees, epochs and perihelion times in JD TT.

    nongrav holds the Marsden A1, A2, A3 (au/day^2) of objects that have them;
    source is 0 for an MPC orbit and 1 for a JPL one.
    """

    name: np.ndarray
    number: np.ndarray
    packed: np.ndarray
    kind: np.ndarray
    epoch: np.ndarray
    q: np.ndarray
    e: np.ndarray
    inc: np.ndarray
    node: np.ndarray
    peri: np.ndarray
    tp: np.ndarray
    H: np.ndarray
    G: np.ndarray
    U: np.ndarray
    mpc_type: np.ndarray
    nongrav: np.ndarray | None = None
    source: np.ndarray | None = None

    def __post_init__(self):
        if self.nongrav is None:
            self.nongrav = np.zeros((len(self.name), 3))
        if self.source is None:
            self.source = np.zeros(len(self.name), dtype=np.int8)

    def __len__(self) -> int:
        return len(self.name)

    def take(self, idx) -> "Orbits":
        return Orbits(**{f.name: getattr(self, f.name)[idx] for f in fields(self)})

    @staticmethod
    def concat(parts: list["Orbits"]) -> "Orbits":
        return Orbits(**{f.name: np.concatenate([getattr(p, f.name) for p in parts]) for f in fields(Orbits)})

    def save(self, path: str) -> None:
        buf = io.BytesIO()
        np.savez(buf, **{f.name: getattr(self, f.name) for f in fields(self)})
        with open(path, "wb") as fh:
            fh.write(zstandard.ZstdCompressor(level=10, threads=-1).compress(buf.getvalue()))

    @staticmethod
    def load(path: str) -> "Orbits":
        with open(path, "rb") as fh:
            raw = zstandard.ZstdDecompressor().decompressobj().decompress(fh.read())
        z = np.load(io.BytesIO(raw), allow_pickle=False)
        return Orbits(**{f.name: z[f.name] for f in fields(Orbits) if f.name in z.files})


def unpack_epoch(s: str) -> float:
    """JD of a packed MPC date such as K2669."""
    century = {"I": 1800, "J": 1900, "K": 2000}[s[0]]
    year = century + int(s[1:3])
    month = _packed_digit(s[3])
    day = _packed_digit(s[4])
    return calendar_jd(year, month, day)


def _packed_digit(c: str) -> int:
    if c.isdigit():
        return int(c)
    return ord(c) - ord("A") + 10


def calendar_jd(year: int, month: int, day: float) -> float:
    """JD at the start of a Gregorian calendar day, with a fractional day."""
    a = (14 - month) // 12
    y = year + 4800 - a
    m = month + 12 * a - 3
    jdn = int(day) + (153 * m + 2) // 5 + 365 * y + y // 4 - y // 100 + y // 400 - 32045
    return jdn - 0.5 + (day - int(day))


def _f(s: str, default: float = np.nan) -> float:
    s = s.strip()
    if not s:
        return default
    return float(s)


def parse_mpcorb(text: str) -> Orbits:
    """Parse MPCORB.DAT (https://minorplanetcenter.net/iau/info/MPOrbitFormat.html)."""
    rows = []
    started = False
    for line in text.splitlines():
        if not started:
            if line.startswith("-----"):
                started = True
            continue
        if len(line) < 103 or not line[0:7].strip():
            continue
        try:
            a = float(line[92:103])
            e = float(line[70:79])
            m = float(line[26:35])
            epoch = unpack_epoch(line[20:25])
            peri = float(line[37:46])
            node = float(line[48:57])
            inc = float(line[59:68])
        except ValueError:
            continue
        readable = line[166:194].strip()
        number = ""
        name = readable
        if readable.startswith("("):
            close = readable.find(")")
            number = readable[1:close]
            name = readable[close + 1 :].strip()
        flags = int(line[161:165], 16) if line[161:165].strip() else 0
        u = line[105:106].strip()
        rows.append(
            (
                name,
                number,
                line[0:7].strip(),
                epoch,
                a,
                e,
                inc,
                node,
                peri,
                m,
                _f(line[8:13]),
                _f(line[14:19], 0.15),
                int(u) if u.isdigit() else (10 if u else -1),
                flags & 0x3F,
            )
        )
    return _from_mean_anomaly(rows)


def _from_mean_anomaly(rows) -> Orbits:
    n = len(rows)
    col = list(zip(*rows)) if n else [[]] * 14
    a = np.asarray(col[4], dtype=np.float64)
    e = np.asarray(col[5], dtype=np.float64)
    epoch = np.asarray(col[3], dtype=np.float64)
    m = np.radians(np.asarray(col[9], dtype=np.float64))
    m = (m + np.pi) % (2 * np.pi) - np.pi
    mean_motion = np.sqrt(GM_SUN_GAUSS / a**3)
    return Orbits(
        name=np.asarray(col[0], dtype=str),
        number=np.asarray(col[1], dtype=str),
        packed=np.asarray(col[2], dtype=str),
        kind=np.full(n, KIND_ASTEROID, dtype=np.int8),
        epoch=epoch,
        q=a * (1 - e),
        e=e,
        inc=np.asarray(col[6], dtype=np.float64),
        node=np.asarray(col[7], dtype=np.float64),
        peri=np.asarray(col[8], dtype=np.float64),
        tp=epoch - m / mean_motion,
        H=np.asarray(col[10], dtype=np.float64),
        G=np.asarray(col[11], dtype=np.float64),
        U=np.asarray(col[12], dtype=np.int8),
        mpc_type=np.asarray(col[13], dtype=np.int8),
    )


GM_SUN_GAUSS = 0.01720209895**2


def parse_comets(text: str) -> Orbits:
    """Parse MPC's CometEls.txt (https://minorplanetcenter.net/iau/info/CometOrbitFormat.html)."""
    rows = []
    for line in text.splitlines():
        if len(line) < 102:
            continue
        try:
            tp = calendar_jd(int(line[14:18]), int(line[19:21]), float(line[22:29]))
            q = float(line[30:39])
            e = float(line[41:49])
            peri = float(line[51:59])
            node = float(line[61:69])
            inc = float(line[71:79])
        except ValueError:
            continue
        ep = line[81:89].strip()
        epoch = calendar_jd(int(ep[0:4]), int(ep[4:6]), float(ep[6:8])) if len(ep) == 8 else tp
        desig = line[102:158].strip()
        number = line[0:4].strip().lstrip("0")
        rows.append(
            (
                desig,
                number,
                line[0:12].strip(),
                epoch,
                q,
                e,
                inc,
                node,
                peri,
                tp,
                _f(line[91:95]),
                _f(line[96:100], 4.0),
            )
        )
    n = len(rows)
    col = list(zip(*rows)) if n else [[]] * 12
    return Orbits(
        name=np.asarray(col[0], dtype=str),
        number=np.asarray(col[1], dtype=str),
        packed=np.asarray(col[2], dtype=str),
        kind=np.full(n, KIND_COMET, dtype=np.int8),
        epoch=np.asarray(col[3], dtype=np.float64),
        q=np.asarray(col[4], dtype=np.float64),
        e=np.asarray(col[5], dtype=np.float64),
        inc=np.asarray(col[6], dtype=np.float64),
        node=np.asarray(col[7], dtype=np.float64),
        peri=np.asarray(col[8], dtype=np.float64),
        tp=np.asarray(col[9], dtype=np.float64),
        H=np.asarray(col[10], dtype=np.float64),
        G=np.asarray(col[11], dtype=np.float64),
        U=np.full(n, -1, dtype=np.int8),
        mpc_type=np.zeros(n, dtype=np.int8),
    )


def parse_jpl_asteroids(text: str, numbered: bool) -> Orbits:
    """Parse JPL's ELEMENTS.NUMBR or ELEMENTS.UNNUM (https://ssd.jpl.nasa.gov/dat/)."""
    rows = []
    for line in text.splitlines()[2:]:
        if not line.strip():
            continue
        if numbered:
            number, name, rest = line[0:6].strip(), line[7:24].strip(), line[25:].split()
        else:
            number, name, rest = "", line[0:13].strip(), line[14:].split()
        try:
            epoch = float(rest[0]) + 2400000.5
            a, e, inc, peri, node, m = (float(x) for x in rest[1:7])
            h = float(rest[7])
            g = float(rest[8])
        except (ValueError, IndexError):
            continue
        rows.append((name, number, number or name, epoch, a, e, inc, node, peri, m, h, g, -1, 0))
    return _from_mean_anomaly(rows)


def parse_jpl_comets(text: str) -> Orbits:
    """Parse JPL's ELEMENTS.COMET."""
    rows = []
    for line in text.splitlines()[2:]:
        if len(line) < 120:
            continue
        name = line[0:43].strip()
        f = line[44:].split()
        try:
            epoch = float(f[0]) + 2400000.5
            q, e, inc, peri, node = (float(x) for x in f[1:6])
            tps = f[6]
            tp = calendar_jd(int(tps[0:4]), int(tps[4:6]), float(tps[6:]))
        except (ValueError, IndexError):
            continue
        rows.append((name, "", name, epoch, q, e, inc, node, peri, tp))
    n = len(rows)
    col = list(zip(*rows)) if n else [[]] * 10
    return Orbits(
        name=np.asarray(col[0], dtype=str),
        number=np.asarray(col[1], dtype=str),
        packed=np.asarray(col[2], dtype=str),
        kind=np.full(n, KIND_COMET, dtype=np.int8),
        epoch=np.asarray(col[3], dtype=np.float64),
        q=np.asarray(col[4], dtype=np.float64),
        e=np.asarray(col[5], dtype=np.float64),
        inc=np.asarray(col[6], dtype=np.float64),
        node=np.asarray(col[7], dtype=np.float64),
        peri=np.asarray(col[8], dtype=np.float64),
        tp=np.asarray(col[9], dtype=np.float64),
        H=np.full(n, np.nan),
        G=np.full(n, 4.0),
        U=np.full(n, -1, dtype=np.int8),
        mpc_type=np.zeros(n, dtype=np.int8),
    )


def parse_sbdb_comets(doc: dict) -> Orbits:
    """JPL SBDB query API output for comets, with non-gravitational parameters."""
    idx = {name: k for k, name in enumerate(doc["fields"])}
    rows = []
    for r in doc["data"]:
        try:
            epoch = float(r[idx["epoch"]])
            q, e = float(r[idx["q"]]), float(r[idx["e"]])
            inc, node, peri = float(r[idx["i"]]), float(r[idx["om"]]), float(r[idx["w"]])
            tp = float(r[idx["tp"]])
        except (TypeError, ValueError):
            continue
        ng = [float(r[idx[k]]) if r[idx[k]] not in (None, "") else 0.0 for k in ("A1", "A2", "A3")]
        m1 = r[idx["M1"]]
        k1 = r[idx["K1"]]
        rows.append((r[idx["full_name"]].strip(), r[idx["pdes"]], epoch, q, e, inc, node, peri, tp, float(m1) if m1 else np.nan, float(k1) / 2.5 if k1 else 4.0, ng))
    n = len(rows)
    col = list(zip(*rows)) if n else [[]] * 12
    return Orbits(
        name=np.asarray(col[0], dtype=str),
        number=np.asarray([p.split("P")[0] if p[:-1].isdigit() and p[-1] in "PD" else "" for p in col[1]], dtype=str),
        packed=np.asarray(col[1], dtype=str),
        kind=np.full(n, KIND_COMET, dtype=np.int8),
        epoch=np.asarray(col[2], dtype=np.float64),
        q=np.asarray(col[3], dtype=np.float64),
        e=np.asarray(col[4], dtype=np.float64),
        inc=np.asarray(col[5], dtype=np.float64),
        node=np.asarray(col[6], dtype=np.float64),
        peri=np.asarray(col[7], dtype=np.float64),
        tp=np.asarray(col[8], dtype=np.float64),
        H=np.asarray(col[9], dtype=np.float64),
        G=np.asarray(col[10], dtype=np.float64),
        U=np.full(n, -1, dtype=np.int8),
        mpc_type=np.zeros(n, dtype=np.int8),
        nongrav=np.asarray(col[11], dtype=np.float64).reshape(n, 3),
    )
