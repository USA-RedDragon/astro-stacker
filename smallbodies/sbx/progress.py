"""The update's progress, written to the bucket for the processing indicator."""

from __future__ import annotations

import json
import threading
import time

KEY = "progress/update.json"


def _now() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


class Progress:
    def __init__(self, store, every: float = 10.0):
        self.store = store
        self.every = every
        self.lock = threading.Lock()
        self.doc = {"state": "running", "current": "", "done": 0, "total": 0, "unit": "", "started": _now(), "updated": _now()}
        self.stop = threading.Event()
        self.thread = threading.Thread(target=self._loop, daemon=True)

    def start(self) -> "Progress":
        self._write()
        self.thread.start()
        return self

    def set(self, current: str, done: int = 0, total: int = 0, unit: str = "") -> None:
        with self.lock:
            self.doc.update(current=current, done=int(done), total=int(total), unit=unit)

    def advance(self, done: int, total: int) -> None:
        with self.lock:
            self.doc.update(done=int(done), total=int(total))

    def finish(self, error: BaseException | None = None) -> None:
        self.stop.set()
        if self.thread.is_alive():
            self.thread.join(timeout=30)
        with self.lock:
            self.doc["state"] = "failed" if error is not None else "done"
            if error is not None:
                self.doc["error"] = str(error)[:500]
        self._write()

    def _write(self) -> None:
        with self.lock:
            self.doc["updated"] = _now()
            body = json.dumps(self.doc).encode()
        try:
            self.store.put(KEY, body, "application/json")
        except Exception as ex:
            print(_now(), f"could not write progress: {ex}", flush=True)

    def _loop(self) -> None:
        while not self.stop.wait(self.every):
            self._write()


def read(store) -> bytes | None:
    try:
        return store.get(KEY)
    except Exception:
        return None
