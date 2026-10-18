import threading
import time

from tessvet import claim
from tessvet import job as jobmod
from tessvet.store import LocalStore

from test_job import GID, SECTORS, FakeMast, MemStore, Tick, fake_runner, make_job, request

KEY = f"vetting/tess/requests/{GID}.json"


def test_two_workers_never_take_the_same_request():
    st = MemStore()
    a = claim.Claims(st, "pod-a")
    b = claim.Claims(st, "pod-b")
    la = a.take(KEY, GID)
    assert la is not None
    assert b.take(KEY, GID) is None
    assert st.get_json(claim.claim_key(KEY))["worker"] == "pod-a"
    la.release()
    assert not st.exists(claim.claim_key(KEY))
    assert b.take(KEY, GID) is not None


def test_racing_workers_get_one_lease_each_request():
    st = MemStore()
    keys = [f"vetting/tess/requests/{i}.json" for i in range(20)]
    won = {k: [] for k in keys}
    start = threading.Barrier(8)

    def worker(name):
        c = claim.Claims(st, name)
        start.wait()
        for k in keys:
            if c.take(k, k) is not None:
                won[k].append(name)

    threads = [threading.Thread(target=worker, args=(f"pod-{i}",)) for i in range(8)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert all(len(v) == 1 for v in won.values())


def test_an_expired_claim_is_retaken_and_the_old_holder_learns_it_lost():
    st = MemStore()
    clock = Tick(1000.0)
    a = claim.Claims(st, "pod-a", ttl=600, clock=clock)
    b = claim.Claims(st, "pod-b", ttl=600, clock=clock)
    la = a.take(KEY, GID)
    clock.t += 300
    assert la.renew()
    clock.t += 599
    assert b.take(KEY, GID) is None
    clock.t += 2
    lb = b.take(KEY, GID)
    assert lb is not None and st.get_json(claim.claim_key(KEY))["worker"] == "pod-b"
    assert not la.renew() and la.lost.is_set()
    la.release()
    assert st.get_json(claim.claim_key(KEY))["worker"] == "pod-b"
    lb.release()
    assert not st.exists(claim.claim_key(KEY))


def test_a_damaged_claim_is_retaken():
    st = MemStore()
    st.put(claim.claim_key(KEY), b"{not json")
    assert claim.Claims(st, "pod-a").take(KEY, GID) is not None


def test_a_store_outage_loses_the_lease_only_after_it_expires():
    st = MemStore()
    clock = Tick(0.0)
    la = claim.Claims(st, "pod-a", ttl=600, clock=clock).take(KEY, GID)
    real = st.put_if

    def down(*a):
        raise ConnectionError("minio down")

    st.put_if = down
    clock.t = 500
    assert la.renew() and not la.lost.is_set()
    clock.t = 601
    assert not la.renew() and la.lost.is_set()
    st.put_if = real


def test_background_renewal_keeps_the_claim():
    st = MemStore()
    la = claim.Claims(st, "pod-a").take(KEY, GID)
    first = st.get_versioned(claim.claim_key(KEY))[1]
    la.start(every=0.01)
    deadline = time.time() + 5
    while st.get_versioned(claim.claim_key(KEY))[1] == first and time.time() < deadline:
        time.sleep(0.01)
    la.release()
    assert la.etag != first
    assert not st.exists(claim.claim_key(KEY))


def test_local_store_conditional_writes(tmp_path):
    s = LocalStore(str(tmp_path / "s"))
    e1 = s.put_if("c/x.json", b"1", None)
    assert e1 and s.put_if("c/x.json", b"2", None) is None
    assert s.put_if("c/x.json", b"2", "wrong") is None
    e2 = s.put_if("c/x.json", b"2", e1)
    assert e2 and s.get_versioned("c/x.json") == (b"2", e2)
    assert s.put_if("c/y.json", b"1", e1) is None
    assert s.get_versioned("c/y.json") is None


def test_job_skips_a_request_another_worker_holds(tmp_path):
    st = MemStore()
    other = "381299946636125440"
    st.put_json(KEY, request(neighbours=[], when="2026-10-14T00:00:00Z"))
    st.put_json(f"vetting/tess/requests/{other}.json", request(gid=other, ra=10.95, dec=41.70, neighbours=[]))
    held = claim.Claims(st, "pod-b").take(KEY, GID)
    calls = []
    j = make_job(tmp_path, st, FakeMast(tmp_path, SECTORS[1:]), fake_runner(tmp_path, calls, produce=(other,)))
    assert j.run() == 1
    assert [c["names"] for c in calls] == [[other]]
    assert st.exists(KEY) and not st.exists(f"vetting/cache/m31/tess/{GID}/status.json")
    assert st.get_json(claim.claim_key(KEY))["worker"] == "pod-b"
    assert not st.exists(claim.claim_key(f"vetting/tess/requests/{other}.json"))
    held.release()


def test_parallel_jobs_fit_each_star_once(tmp_path):
    st = MemStore()
    stars = [str(381299946636125000 + i) for i in range(12)]
    for i, g in enumerate(stars):
        st.put_json(f"vetting/tess/requests/{g}.json",
                    request(gid=g, ra=10.0 + i, dec=41.0, neighbours=[], when=f"2026-10-15T12:{i:02d}:00Z"))
    busy, fitted, overlap = set(), [], []
    guard = threading.Lock()

    def runner_for(tmp):
        def run(spec):
            g = spec["names"][0]
            with guard:
                if g in busy:
                    overlap.append(g)
                busy.add(g)
                fitted.append(g)
            time.sleep(0.02)
            res = fake_runner(tmp, [], produce=(g,))(spec)
            with guard:
                busy.discard(g)
            return res
        return run

    counts = {}

    def worker(i):
        tmp = tmp_path / f"w{i}"
        tmp.mkdir()
        j = jobmod.Job(st, FakeMast(tmp, SECTORS[1:]), jobmod.Config(workdir=str(tmp / "work"), max_minutes=60),
                       runner=runner_for(tmp), claims=claim.Claims(st, f"pod-{i}"))
        counts[i] = j.run()

    threads = [threading.Thread(target=worker, args=(i,)) for i in range(4)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert overlap == []
    assert sorted(fitted) == sorted(stars)
    assert sum(counts.values()) == len(stars) and sum(1 for n in counts.values() if n) > 1
    assert st.keys("vetting/tess/requests/") == [] and st.keys(claim.CLAIMS) == []
    for g in stars:
        assert st.get_json(f"vetting/cache/m31/tess/{g}/status.json")["sectors_done"] == [57]


def test_a_lost_claim_stops_the_star_without_finishing_it(tmp_path):
    st = MemStore()
    st.put_json(KEY, request(neighbours=[]))
    calls = []
    base = fake_runner(tmp_path, calls, produce=(GID,))
    j = make_job(tmp_path, st, FakeMast(tmp_path, SECTORS), None)

    def run(spec):
        res = base(spec)
        j.lease.lost.set()
        return res

    j.runner = run
    j.run()
    assert len(calls) == 1
    assert st.exists(KEY)
    assert st.get_json(f"vetting/cache/m31/tess/{GID}/status.json")["sectors_done"] == [17]


def test_a_lost_claim_kills_tglc(tmp_path):
    j = make_job(tmp_path, MemStore(), FakeMast(tmp_path, []), None)
    j.lease = claim.Claims(MemStore(), "pod-a").take(KEY, GID)
    j.lease.lost.set()
    work = tmp_path / "w"
    work.mkdir()
    t0 = time.monotonic()
    res = j._run_tglc({"workdir": str(work), "sector": 57, "cutout": str(work / "none.fits")})
    assert res["lost"] and not res["ok"]
    assert time.monotonic() - t0 < 30


def test_a_request_finished_by_another_worker_is_not_redone(tmp_path):
    st = MemStore()
    st.put_json(KEY, request(neighbours=[]))
    j = make_job(tmp_path, st, FakeMast(tmp_path, SECTORS), fake_runner(tmp_path, []))
    stale = j.queue()
    st.delete(KEY)
    j.queue = lambda: stale
    assert j.run() == 0
    assert not st.exists(f"vetting/cache/m31/tess/{GID}/status.json")
    assert st.keys(claim.CLAIMS) == []


def test_a_star_in_the_cutout_another_worker_is_making_waits(tmp_path):
    st = MemStore()
    near, far, elsewhere = "381299946636125440", "381299946636125441", "381299946636125442"
    st.put_json(KEY, request(neighbours=[], when="2026-10-14T00:00:00Z"))
    st.put_json(f"vetting/tess/requests/{near}.json", request(gid=near, ra=10.95, dec=41.70, neighbours=[]))
    st.put_json(f"vetting/tess/requests/{elsewhere}.json",
                request(gid=elsewhere, slug="m31-panel-2", ra=10.95, dec=41.70, neighbours=[]))
    st.put_json(f"vetting/tess/requests/{far}.json",
                request(gid=far, ra=11.6, dec=41.66, neighbours=[], when="2026-10-16T00:00:00Z"))
    held = claim.Claims(st, "pod-b").take(KEY, GID, {"slug": "m31", "ra": 10.889321, "dec": 41.662433})
    calls = []
    fm = FakeMast(tmp_path, SECTORS[1:])
    j = make_job(tmp_path, st, fm, fake_runner(tmp_path, calls, produce=(near, far, elsewhere)))
    assert j.run() == 2
    assert [c["names"] for c in calls] == [[elsewhere], [far]]
    assert st.exists(f"vetting/tess/requests/{near}.json")
    held.release()
    calls.clear()
    cutouts = len(fm.cutouts)
    j = make_job(tmp_path, st, fm, fake_runner(tmp_path, calls, produce=(GID, near)))
    assert j.run() == 2
    assert [c["names"] for c in calls] == [[GID]] and len(fm.cutouts) == cutouts + 1
    assert st.get_json(f"vetting/cache/m31/tess/{near}/status.json")["sectors_done"] == [57]
    assert st.keys("vetting/tess/requests/") == []


class RetriedStore(MemStore):
    """A store whose conditional writes land but report a failed precondition, like a retried PUT."""

    def put_if(self, key, data, etag):
        super().put_if(key, data, etag)
        return None


def test_a_retried_write_still_takes_and_renews_the_claim():
    st = RetriedStore()
    clock = Tick(0.0)
    la = claim.Claims(st, "pod-a", ttl=600, clock=clock).take(KEY, GID)
    assert la is not None
    clock.t = 200
    assert la.renew() and la.held()
    la.release()
    assert not st.exists(claim.claim_key(KEY))


def test_a_lease_runs_out_locally_without_the_store():
    st = MemStore()
    clock = Tick(0.0)
    la = claim.Claims(st, "pod-a", ttl=600, clock=clock).take(KEY, GID)
    clock.t = 600 - claim.MARGIN - 1
    assert la.held()
    clock.t = 600 - claim.MARGIN
    assert not la.held()
    clock.t = 601
    lb = claim.Claims(st, "pod-b", ttl=600, clock=clock).take(KEY, GID)
    la.release()
    assert lb is not None and st.get_json(claim.claim_key(KEY))["worker"] == "pod-b"


def test_expired_claims_on_finished_requests_are_cleaned_up():
    st = MemStore()
    clock = Tick(0.0)
    gone = "vetting/tess/requests/9.json"
    claim.Claims(st, "pod-a", ttl=600, clock=clock).take(gone, "9")
    claim.Claims(st, "pod-a", ttl=600, clock=clock).take(KEY, GID)
    st.put_json(KEY, request())
    b = claim.Claims(st, "pod-b", ttl=600, clock=clock)
    assert len(b.others()) == 2
    clock.t = 700
    assert b.others() == []
    assert st.keys(claim.CLAIMS) == [claim.claim_key(KEY)]


def test_the_later_of_two_nearby_claims_yields(tmp_path):
    st = MemStore()
    near = "381299946636125440"
    near_key = f"vetting/tess/requests/{near}.json"
    st.put_json(KEY, request(neighbours=[], when="2026-10-14T00:00:00Z"))
    st.put_json(near_key, request(gid=near, ra=10.95, dec=41.70, neighbours=[]))
    calls = []
    j = make_job(tmp_path, st, FakeMast(tmp_path, SECTORS[1:]), fake_runner(tmp_path, calls))
    real = j.claims.take

    def racing(key, gid, info=None):
        if not st.exists(claim.claim_key(near_key)):
            claim.Claims(st, "pod-0", clock=lambda: time.time() - 5).take(
                near_key, near, {"slug": "m31", "ra": 10.95, "dec": 41.70})
        return real(key, gid, info)

    j.claims.take = racing
    assert j.run() == 0
    assert calls == [] and st.exists(KEY)
    assert st.keys(claim.CLAIMS) == [claim.claim_key(near_key)]


def test_a_store_error_ends_the_run_cleanly(tmp_path):
    st = MemStore()
    st.put_json(KEY, request(neighbours=[]))

    def down(*a):
        raise ConnectionError("minio down")

    st.put_if = down
    j = make_job(tmp_path, st, FakeMast(tmp_path, SECTORS), fake_runner(tmp_path, []))
    assert j.run() == 0


def test_shared_cutout_reach():
    req = jobmod.Request("k", "2", 359.99, 0.0, "f")
    assert jobmod._shares_cutout({"slug": "f", "gaia_id": "1", "ra": 0.1, "dec": 0.1}, req)
    assert not jobmod._shares_cutout({"slug": "f", "gaia_id": "1", "ra": 0.5, "dec": 0.0}, req)
    assert not jobmod._shares_cutout({"slug": "g", "gaia_id": "1", "ra": 0.0, "dec": 0.0}, req)
    assert not jobmod._shares_cutout({"slug": "f", "gaia_id": "1"}, req)
