"""python -m tessvet [--local-dir DIR] [--work DIR] [--max-minutes N]

Process the vetting/tess/requests queue once, oldest first, then exit.
Without --local-dir the store is MinIO from S3_ENDPOINT, S3_ACCESS_KEY, S3_SECRET_KEY,
S3_USE_SSL and S3_BUCKET (default astro-processed).
"""

import argparse
import logging
import os
import signal
import sys


def _scratch_env(work: str) -> None:
    cache = os.path.join(work, ".cache")
    for k, sub in (("HOME", "home"), ("XDG_CACHE_HOME", "xdg"), ("XDG_CONFIG_HOME", "config"),
                   ("ASTROPY_CACHE_DIR", "astropy"), ("ASTROPY_CONFIG_DIR", "astropy"),
                   ("MPLCONFIGDIR", "matplotlib"), ("NUMBA_CACHE_DIR", "numba"), ("TMPDIR", "tmp")):
        os.makedirs(os.environ.setdefault(k, os.path.join(cache, sub)), exist_ok=True)


def main() -> int:
    p = argparse.ArgumentParser(prog="tessvet")
    p.add_argument("--work", default=os.environ.get("WORKDIR", "/work"))
    p.add_argument("--local-dir", default=os.environ.get("LOCAL_DIR"))
    p.add_argument("--max-minutes", type=float, default=float(os.environ.get("MAX_MINUTES", "300")))
    p.add_argument("--field-mag", type=float, default=float(os.environ.get("FIELD_MAG", "16")),
                   help="also fit light curves of every star down to this TESS magnitude; negative turns it off")
    p.add_argument("--threads", type=int, default=int(os.environ.get("TGLC_THREADS", "4")))
    args = p.parse_args()
    _scratch_env(args.work)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s", stream=sys.stdout)
    from .job import Config, Job
    from .mast import Client
    from .store import LocalStore, S3Store

    store = LocalStore(args.local_dir) if args.local_dir else S3Store.from_env()
    cfg = Config(workdir=args.work, max_minutes=args.max_minutes,
                 field_mag=args.field_mag if args.field_mag >= 0 else None, threads=args.threads)
    n = Job(store, Client(), cfg).run()
    logging.info("processed %d requests", n)
    return 0


if __name__ == "__main__":
    sys.exit(main())
