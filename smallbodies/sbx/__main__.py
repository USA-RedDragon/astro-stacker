"""astro-small-bodies: known asteroids and comets in a field, computed locally.

python -m sbx update    mirror MPC and JPL orbits, build the catalogue and grid
python -m sbx serve     the HTTP API
"""

import argparse
import logging
import os
import sys

from . import server, update
from .store import Store


def main() -> int:
    p = argparse.ArgumentParser(prog="sbx")
    sub = p.add_subparsers(dest="cmd", required=True)
    u = sub.add_parser("update")
    u.add_argument("--workdir", default=os.environ.get("WORKDIR", "/work"))
    u.add_argument("--grid-start", default=os.environ.get("GRID_START", "2024-12-01"))
    u.add_argument("--ahead-days", type=float, default=float(os.environ.get("GRID_AHEAD_DAYS", "45")))
    u.add_argument("--retain-days", type=int, default=int(os.environ.get("RETAIN_DAYS", "45")))
    u.add_argument("--workers", type=int, default=int(os.environ.get("WORKERS", "4")))
    s = sub.add_parser("serve")
    s.add_argument("--workdir", default=os.environ.get("WORKDIR", "/work"))
    s.add_argument("--port", type=int, default=int(os.environ.get("PORT", "8080")))
    s.add_argument("--workers", type=int, default=int(os.environ.get("WORKERS", "3")))
    args = p.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s", stream=sys.stdout)
    store = Store.from_env()
    if args.cmd == "update":
        update.run(store, args.workdir, args.grid_start, args.ahead_days, args.retain_days, args.workers)
    else:
        server.serve(store, args.workdir, args.port, args.workers)
    return 0


if __name__ == "__main__":
    sys.exit(main())
