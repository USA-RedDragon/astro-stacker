import json

from sbx import grid
from sbx.progress import KEY, Progress, read


class FakeStore:
    def __init__(self):
        self.objects = {}

    def put(self, key, data, content_type="application/octet-stream"):
        self.objects[key] = data

    def get(self, key):
        return self.objects[key]


def test_progress_writes_steps_and_the_end():
    s = FakeStore()
    p = Progress(s, every=3600).start()
    first = json.loads(s.objects[KEY])
    assert first["state"] == "running"
    p.set("working out the orbits", 0, 10, "objects")
    p.advance(4, 10)
    p.finish()
    end = json.loads(read(s))
    assert end["state"] == "done"
    assert (end["current"], end["done"], end["total"], end["unit"]) == ("working out the orbits", 4, 10, "objects")
    assert end["updated"].endswith("Z")


def test_progress_records_a_failure():
    s = FakeStore()
    p = Progress(s, every=3600).start()
    p.finish(RuntimeError("catalogue too small"))
    end = json.loads(s.objects[KEY])
    assert end["state"] == "failed" and "too small" in end["error"]


def test_read_without_a_report():
    assert read(FakeStore()) is None


def test_run_reports_each_finished_job():
    class Fut:
        def __init__(self, v):
            self.v = v

        def result(self):
            return self.v

    class Pool:
        def submit(self, fn, args):
            return Fut(fn(args))

    orig = grid.wait
    grid.wait = lambda pending, return_when=None: (set(pending), set())
    try:
        seen = []
        jobs = [([1, 2], 1), ([3], 2)]
        grid._run(Pool(), jobs, lambda a: a, lambda k, r: None, 4, lambda m: None, "t", lambda d, t: seen.append((d, t)))
    finally:
        grid.wait = orig
    assert seen[-1] == (3, 3)
