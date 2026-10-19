package surveys

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestExpired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		age    time.Duration
		maxAge time.Duration
		want   bool
	}{
		{5 * 24 * time.Hour, ATLASMaxAge, false},
		{6*24*time.Hour + time.Minute, ATLASMaxAge, true},
		{7 * 24 * time.Hour, ATLASMaxAge, true},
		{12 * 24 * time.Hour, TESSMaxAge, false},
		{14 * 24 * time.Hour, TESSMaxAge, true},
		{6*24*time.Hour - time.Minute, ZTFReleaseMaxAge, false},
		{8 * 24 * time.Hour, ZTFReleaseMaxAge, true},
	}
	for _, c := range cases {
		if got := expired(now.Add(-c.age), now, c.maxAge); got != c.want {
			t.Errorf("age %s max %s: expired %v, want %v", c.age, c.maxAge, got, c.want)
		}
	}
	if !expired(time.Time{}, now, ATLASMaxAge) {
		t.Error("an answer of unknown age has expired")
	}
	if ATLASMaxAge >= 7*24*time.Hour {
		t.Error("the ATLAS max age must be under the weekly refresh so each refresh asks ATLAS")
	}
}

func TestATLASRefresh(t *testing.T) {
	t.Parallel()
	fake := &atlasFake{t: t, finishAt: 1, positions: map[string]int{}}
	f, st := testFetcher(t, fake)
	fake.url = f.IRSAURL
	f.ATLASToken = testToken
	var mu sync.Mutex
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	step := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	ctx := t.Context()
	field, star := ic1396Field(), ic1396Star()

	if fe := f.ATLAS(ctx, field, star); fe.State != Pending || fake.queued != 1 {
		t.Fatalf("first call %+v", fe)
	}
	first := f.ATLAS(ctx, field, star)
	if first.State != Done || len(first.Series) == 0 {
		t.Fatalf("result %+v", first)
	}
	step(3 * 24 * time.Hour)
	if fe := f.ATLAS(Fresh(ctx), field, star); fe.State != Done || fake.queued != 1 {
		t.Fatalf("a three day old result is kept on a refresh: %+v queued %d", fe, fake.queued)
	}
	step(5 * 24 * time.Hour)
	if fe := f.ATLAS(ctx, field, star); fe.State != Done || fake.queued != 1 {
		t.Fatalf("an old result is kept outside a refresh: %+v queued %d", fe, fake.queued)
	}
	fake.mu.Lock()
	fake.polls, fake.finishAt, fake.deleted = 0, 2, false
	fake.mu.Unlock()
	fe := f.ATLAS(Fresh(ctx), field, star)
	if fe.State != Pending || fake.queued != 2 || len(fe.Series) != len(first.Series) {
		t.Fatalf("an eight day old result is asked for again, the old series kept meanwhile: %+v queued %d", fe, fake.queued)
	}
	fe = f.ATLAS(Fresh(ctx), field, star)
	if fe.State != Pending || fake.queued != 2 || len(fe.Series) == 0 {
		t.Fatalf("while the new task waits the old series stay pending: %+v", fe)
	}
	if fe := f.ATLAS(Fresh(ctx), field, star); fe.State != Done || !fake.deleted || fake.queued != 2 {
		t.Fatalf("the finished refresh is read: %+v", fe)
	}
	if fe := f.ATLAS(Fresh(ctx), field, star); fe.State != Done || fake.queued != 2 {
		t.Fatalf("the new result is fresh: %+v queued %d", fe, fake.queued)
	}
	if _, err := st.Get(ctx, "vetting/cache/ic-1396-panel-1/atlas/2179309875368546432.txt.zst"); err != nil {
		t.Fatal(err)
	}
}

func TestTESSRecheck(t *testing.T) {
	t.Parallel()
	f, st := testFetcher(t, http.NotFoundHandler())
	ctx := t.Context()
	field, star := ic1396Field(), ic1396Star()
	z, err := f.zstdEncode([]byte(tessCSV))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, "vetting/cache/ic-1396-panel-1/tess/2179309875368546432/s0016.csv.zst", z, "application/zstd"); err != nil {
		t.Fatal(err)
	}
	req := "vetting/tess/requests/2179309875368546432.json"
	putStatus(t, st, TESSStatus{GaiaID: GaiaID(star.GaiaID), SectorsAvailable: []int{16}, SectorsDone: []int{16}, UpdatedAt: f.now().Add(-10 * 24 * time.Hour)})
	if fe := f.TESS(Fresh(ctx), field, star); fe.State != Done {
		t.Fatalf("a ten day old status is kept: %+v", fe)
	}
	if _, err := st.Get(ctx, req); err == nil {
		t.Fatal("a ten day old status asks the job nothing")
	}
	putStatus(t, st, TESSStatus{GaiaID: GaiaID(star.GaiaID), SectorsAvailable: []int{16}, SectorsDone: []int{16}, UpdatedAt: f.now().Add(-20 * 24 * time.Hour)})
	if fe := f.TESS(ctx, field, star); fe.State != Done {
		t.Fatalf("outside a refresh an old status is kept: %+v", fe)
	}
	if _, err := st.Get(ctx, req); err == nil {
		t.Fatal("outside a refresh the job is asked nothing")
	}
	fe := f.TESS(Fresh(ctx), field, star)
	if fe.State != Pending || len(fe.Series) != 1 {
		t.Fatalf("a twenty day old status is checked again, its sectors kept: %+v", fe)
	}
	if _, err := st.Get(ctx, req); err != nil {
		t.Fatalf("the job is asked to look for new sectors: %v", err)
	}
}
