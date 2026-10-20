package surveys

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

const (
	testToken = "secret-token"
	keyURL    = "url"
)

type atlasFake struct {
	mu         sync.Mutex
	t          *testing.T
	url        string
	queued     int
	throttle   int
	polls      int
	finishAt   int
	deleted    bool
	positions  map[string]int
	form       map[string]string
	postFail   int
	listed     bool
	resultGone bool
	posted     []string
}

func (a *atlasFake) writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		a.t.Error(err)
	}
}

func (a *atlasFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Header.Get("Authorization") != "Token "+testToken {
		http.Error(w, `{"detail":"no token"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/forcedphot/queuepositions.json":
		a.writeJSON(w, map[string]any{"queuepositions": a.positions, "queueoffset": 0})
	case r.URL.Path == "/forcedphot/queue/" && r.Method == http.MethodPost:
		if a.throttle > 0 {
			a.throttle--
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"detail":"Request was throttled."}`))
			return
		}
		if err := r.ParseForm(); err != nil {
			a.t.Error(err)
		}
		if a.postFail > 0 {
			a.postFail--
			a.queued++
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		a.form = map[string]string{}
		for k := range r.PostForm {
			a.form[k] = r.PostForm.Get(k)
		}
		a.queued++
		a.posted = append(a.posted, a.form["comment"])
		w.WriteHeader(http.StatusCreated)
		a.writeJSON(w, map[string]any{keyURL: a.url + "/forcedphot/queue/5193733/", "finishtimestamp": nil})
	case r.URL.Path == "/forcedphot/queue/" && r.Method == http.MethodGet:
		a.listed = true
		a.writeJSON(w, map[string]any{"results": []map[string]any{
			{keyURL: a.url + "/forcedphot/queue/1/", "comment": "someone else"},
			{keyURL: a.url + "/forcedphot/queue/5193733/", "comment": "astro-stacker vetting 2179309875368546432"},
		}})
	case r.URL.Path == "/forcedphot/queue/5193733/" && r.Method == http.MethodGet:
		a.polls++
		st := map[string]any{keyURL: a.url + "/forcedphot/queue/5193733/", "starttimestamp": nil, "finishtimestamp": nil, "result_url": nil, "error_msg": nil, "queuepos": 3}
		if a.polls >= a.finishAt {
			st["starttimestamp"] = "2026-10-15T20:43:00Z"
			st["finishtimestamp"] = "2026-10-15T20:58:10Z"
			st["result_url"] = a.url + "/forcedphot/static/results/job5193733.txt"
		}
		a.writeJSON(w, st)
	case r.URL.Path == "/forcedphot/queue/5193733/" && r.Method == http.MethodDelete:
		a.deleted = true
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/forcedphot/static/results/job5193733.txt" && a.resultGone:
		http.NotFound(w, r)
	case r.URL.Path == "/forcedphot/static/results/job5193733.txt":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(readFixture(a.t, "atlas_ic1396.txt"))
	default:
		http.NotFound(w, r)
	}
}

func TestATLASQueuePollAndRead(t *testing.T) {
	t.Parallel()
	fake := &atlasFake{t: t, finishAt: 2, throttle: 1, positions: map[string]int{}}
	f, st := testFetcher(t, fake)
	fake.url = f.IRSAURL
	f.ATLASToken = testToken
	ctx := t.Context()
	field, star := ic1396Field(), ic1396Star()

	fe := f.ATLAS(ctx, field, star)
	if fe.State != Pending || fake.queued != 1 {
		t.Fatalf("first call %+v, queued %d", fe, fake.queued)
	}
	if fake.form["use_reduced"] != "false" || fake.form["mjd_min"] != "57000.0" || fake.form["radec_epoch_year"] != "2016.0" ||
		!strings.HasPrefix(fake.form["propermotion_ra"], "-4.04") || !strings.HasPrefix(fake.form["ra"], "323.2769976") {
		t.Errorf("form %v", fake.form)
	}
	if _, err := st.Get(ctx, "vetting/cache/ic-1396-panel-1/atlas/2179309875368546432.task.json.zst"); err != nil {
		t.Fatal(err)
	}
	fe = f.ATLAS(ctx, field, star)
	if fe.State != Pending || !strings.Contains(fe.Note, "place 3") || fake.queued != 1 {
		t.Fatalf("second call %+v", fe)
	}
	fe = f.ATLAS(ctx, field, star)
	if fe.State != Done || !fake.deleted {
		t.Fatalf("third call %+v, deleted %v", fe, fake.deleted)
	}
	if _, err := st.Get(ctx, "vetting/cache/ic-1396-panel-1/atlas/2179309875368546432.txt.zst"); err != nil {
		t.Fatal(err)
	}
	polls := fake.polls
	again := f.ATLAS(ctx, field, star)
	if again.State != Done || fake.polls != polls {
		t.Fatalf("cached call %+v asked ATLAS again", again)
	}
	if len(fe.Series) != 4 || !strings.HasPrefix(fe.Series[0].Note, "camera 01a") || fe.Series[0].Band != "c" {
		t.Fatalf("series %+v", fe.Series)
	}
	wantD := 1 / (1 + math.Pow(10, -0.4*(14.498565-14.846538)))
	for _, s := range fe.Series {
		if !s.Relative || s.Survey != lc.ATLAS || math.Abs(s.Dilution-wantD) > 1e-9 {
			t.Errorf("series %s: relative %v dilution %v", s.Band, s.Relative, s.Dilution)
		}
		dm := make([]float64, 0, len(s.Points))
		for _, p := range s.Points {
			dm = append(dm, p.Mag)
			if p.BJD < 2457000 || p.BJD > 2461500 || p.Err <= 0 {
				t.Fatalf("point %+v", p)
			}
		}
		if med := median(dm); math.Abs(med) > 0.01 {
			t.Errorf("%s median %v, want near 0", s.Band, med)
		}
	}
}

func TestATLASQueueFull(t *testing.T) {
	t.Parallel()
	fake := &atlasFake{t: t, positions: map[string]int{"1": 0, "2": 1}}
	f, _ := testFetcher(t, fake)
	fake.url = f.IRSAURL
	f.ATLASToken = testToken
	f.ATLASMaxQueued = 2
	fe := f.ATLAS(t.Context(), ic1396Field(), ic1396Star())
	if fe.State != Pending || fake.queued != 0 || !strings.Contains(fe.Note, "waiting for room") {
		t.Fatalf("%+v queued %d", fe, fake.queued)
	}
}

func TestATLASNoToken(t *testing.T) {
	t.Parallel()
	f, _ := testFetcher(t, http.NotFoundHandler())
	if fe := f.ATLAS(t.Context(), ic1396Field(), ic1396Star()); fe.State != Failed {
		t.Fatalf("%+v", fe)
	}
}

func TestATLASCuts(t *testing.T) {
	t.Parallel()
	rows, err := parseATLAS(readFixture(t, "atlas_ic1396.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 159 {
		t.Fatalf("%d rows", len(rows))
	}
	good := 0
	for _, r := range rows {
		if r.Good() {
			good++
		}
	}
	if good == 0 || good == len(rows) {
		t.Errorf("%d of %d rows pass the cuts", good, len(rows))
	}
	bad := rows[0]
	bad.Maj = 6
	if bad.Good() {
		t.Error("a 6 pixel PSF passed")
	}
	if templateSpan(58416.9) != 0 || templateSpan(58417) != 1 || templateSpan(60000) != 2 {
		t.Error("template spans")
	}
}

func TestATLASUnsurePostIsFoundNotRequeued(t *testing.T) {
	t.Parallel()
	fake := &atlasFake{t: t, finishAt: 1, postFail: 1, positions: map[string]int{}}
	f, _ := testFetcher(t, fake)
	fake.url = f.IRSAURL
	f.ATLASToken = testToken
	ctx := t.Context()
	fe := f.ATLAS(ctx, ic1396Field(), ic1396Star())
	if fe.State != Pending || fake.queued != 1 || !strings.Contains(fe.Note, "no clear answer") {
		t.Fatalf("first %+v queued %d", fe, fake.queued)
	}
	fe = f.ATLAS(ctx, ic1396Field(), ic1396Star())
	if fe.State != Done || !fake.listed || fake.queued != 1 {
		t.Fatalf("second %+v listed %v queued %d", fe, fake.listed, fake.queued)
	}
}

func TestATLASResultGone(t *testing.T) {
	t.Parallel()
	fake := &atlasFake{t: t, finishAt: 1, resultGone: true, positions: map[string]int{}}
	f, _ := testFetcher(t, fake)
	fake.url = f.IRSAURL
	f.ATLASToken = testToken
	ctx := t.Context()
	_ = f.ATLAS(ctx, ic1396Field(), ic1396Star())
	fe := f.ATLAS(ctx, ic1396Field(), ic1396Star())
	if fe.State != Failed || !fake.deleted || !strings.Contains(fe.Note, "result is gone") {
		t.Fatalf("%+v deleted %v", fe, fake.deleted)
	}
	fe = f.ATLAS(ctx, ic1396Field(), ic1396Star())
	if fe.State != Failed || fake.queued != 1 {
		t.Fatalf("retried too soon: %+v", fe)
	}
	now := f.Now()
	f.Now = func() time.Time { return now.Add(ATLASRetryFailed + time.Hour) }
	fake.resultGone = false
	if fe = f.ATLAS(ctx, ic1396Field(), ic1396Star()); fe.State != Pending || fake.queued != 2 {
		t.Fatalf("not queued again after the wait: %+v", fe)
	}
}

func TestATLASStaleTask(t *testing.T) {
	t.Parallel()
	fake := &atlasFake{t: t, finishAt: 1000, positions: map[string]int{}}
	f, _ := testFetcher(t, fake)
	fake.url = f.IRSAURL
	f.ATLASToken = testToken
	ctx := t.Context()
	_ = f.ATLAS(ctx, ic1396Field(), ic1396Star())
	now := f.Now()
	f.Now = func() time.Time { return now.Add(ATLASTaskMaxAge + time.Hour) }
	fe := f.ATLAS(ctx, ic1396Field(), ic1396Star())
	if fe.State != Failed || !fake.deleted {
		t.Fatalf("%+v", fe)
	}
}
