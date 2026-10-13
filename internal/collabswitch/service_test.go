package collabswitch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/collabswitch"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	constPath = "Imaging-Items-0-Definition"
	created   = "CREATED"
	finished  = "FINISHED"
	itemsKey  = "Items"
	nameKey   = "Name"
	statusKey = "Status"
	author    = "jacob"
)

func write(w http.ResponseWriter, v map[string]any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(b)
}

type fakeNINA struct {
	mu        sync.Mutex
	started   string
	value     string
	present   bool
	night     string
	status    string
	edits     []string
	stateGets int
	refuse    bool
	broken    bool
	panel     string
	onEdit    func()
}

func (f *fakeNINA) tree(full bool) []any {
	items := []any{}
	if f.present {
		c := map[string]any{nameKey: "Constant", statusKey: created}
		if full {
			c["Identifier"], c["Definition"] = "collab", f.value
		}
		items = append(items, c)
	}
	night := map[string]any{nameKey: f.night + "_Container", statusKey: f.status, itemsKey: []any{
		map[string]any{nameKey: "Collab_Container", statusKey: f.status, itemsKey: []any{
			map[string]any{nameKey: "Starfront collaboration_Container", statusKey: f.status, itemsKey: []any{
				map[string]any{nameKey: f.panelName() + "_Container", statusKey: f.status},
			}},
		}},
		map[string]any{nameKey: "Imaging_Container", statusKey: created},
	}}
	if full {
		night["Triggers"] = []any{map[string]any{nameKey: "When_Trigger", "ConditionWatchdog": map[string]any{"Big": strings.Repeat("x", 1000)}}}
	}
	items = append(items, night)
	done := finished
	if f.status == created {
		done = created
	}
	start := map[string]any{nameKey: "Start_Container", statusKey: done, itemsKey: []any{
		map[string]any{nameKey: "Global Variable", statusKey: done, "Identifier": "collab_done", "Definition": "0"},
		map[string]any{nameKey: "Global Variable", statusKey: done, "Identifier": "collab", "Definition": "7"},
	}}
	return []any{map[string]any{"GlobalTriggers": []any{}}, start, map[string]any{nameKey: "Targets_Container", statusKey: f.status, itemsKey: items},
		map[string]any{nameKey: "End_Container", statusKey: created, itemsKey: []any{}}}
}

func (f *fakeNINA) panelName() string {
	if f.panel == "" {
		return "M31 · panel 2"
	}
	return f.panel
}

func (f *fakeNINA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(v any) {
		write(w, map[string]any{"Response": v, "Error": "", "StatusCode": 200, "Success": true, "Type": "API"})
	}
	switch strings.TrimPrefix(r.URL.Path, "/v2/api") {
	case "/application-start":
		ok(f.started)
	case "/sequence/json":
		ok(f.tree(false))
	case "/sequence/state":
		f.stateGets++
		if f.broken {
			_, _ = w.Write([]byte(`{"Response":[{"Glo`))
			return
		}
		ok(f.tree(true))
	case "/sequence/edit":
		path, v := r.URL.Query().Get("path"), r.URL.Query().Get("value")
		f.edits = append(f.edits, path+"="+v)
		if f.refuse || !f.present || path != constPath {
			write(w, map[string]any{"Response": "", "Error": "Object reference not set", "StatusCode": 500, "Success": false})
			return
		}
		f.value = v
		if f.onEdit != nil {
			f.onEdit()
		}
		ok("Updated setting")
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeNINA) restart(at string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started, f.value, f.status = at, "0", created
}

func (f *fakeNINA) counts() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stateGets, append([]string(nil), f.edits...)
}

type harness struct {
	nina *fakeNINA
	svc  *collabswitch.Service
	cmds *schedcmd.Service
	now  time.Time
}

func newHarness(t *testing.T, present bool) *harness {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "app.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&schedcmd.Record{}, &app.CollabSwitch{}); err != nil {
		t.Fatal(err)
	}
	h := &harness{nina: &fakeNINA{started: "2026-10-13T12:02:54-05:00", value: "0", present: present, night: "Night", status: "RUNNING"},
		now: time.Date(2026, 10, 14, 2, 0, 0, 0, time.UTC)}
	srv := httptest.NewServer(h.nina)
	t.Cleanup(srv.Close)
	h.cmds = &schedcmd.Service{Log: schedcmd.NewGormLog(db), AppDB: db, Now: func() time.Time { return h.now }}
	h.svc = &collabswitch.Service{NINA: collabswitch.NewNINA(srv.URL + "/v2/api/"), DB: db, Commands: h.cmds, Log: h.cmds.Log,
		Now: func() time.Time { return h.now }}
	return h
}

func (h *harness) records(t *testing.T) []schedcmd.Record {
	t.Helper()
	out, err := h.cmds.Log.List(context.Background(), schedcmd.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func byKind(rs []schedcmd.Record, k schedcmd.Kind) []schedcmd.Record {
	var out []schedcmd.Record
	for _, r := range rs {
		if r.Kind == k {
			out = append(out, r)
		}
	}
	return out
}

func TestTurningTheCollabOnSetsReadsBackAndReplans(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	st, rec, err := h.svc.Set(context.Background(), true, author)
	if err != nil {
		t.Fatal(err)
	}
	reads, edits := h.nina.counts()
	if len(edits) != 1 || edits[0] != constPath+"=1" || reads != 2 {
		t.Errorf("edits %v, state reads %d", edits, reads)
	}
	if st.Desired == nil || !st.Desired.On || st.Desired.SetBy != author || !st.ReadBack.Present || st.ReadBack.Value == nil || *st.ReadBack.Value != "1" ||
		!st.Matches || st.ReadBack.At == nil || !st.ReadBack.At.Equal(h.now) {
		t.Errorf("status %+v", st)
	}
	if rec.Kind != schedcmd.KindCollabSwitch || rec.Status != schedcmd.StatusApplied || rec.Message != "NINA reads back collab = 1." || rec.Author != author {
		t.Errorf("history %+v", rec)
	}
	rs := h.records(t)
	if len(byKind(rs, schedcmd.KindReplan)) != 1 || len(byKind(rs, schedcmd.KindCollabSwitch)) != 1 {
		t.Errorf("records %+v", rs)
	}
	if got := strings.Join(st.Running, " > "); got != "Targets > Night > Collab > Starfront collaboration > M31 · panel 2" {
		t.Errorf("running %q", got)
	}
}

func TestPausingTheCollabDoesNotReplan(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	h.nina.value = "1"
	st, rec, err := h.svc.Set(context.Background(), false, author)
	if err != nil {
		t.Fatal(err)
	}
	if st.ReadBack.Value == nil || *st.ReadBack.Value != "0" || st.Desired.On || rec.Status != schedcmd.StatusApplied || rec.Title != "Pause the collab" {
		t.Errorf("status %+v record %+v", st, rec)
	}
	if n := len(byKind(h.records(t), schedcmd.KindReplan)); n != 0 {
		t.Errorf("%d re-plans on pause", n)
	}
}

func TestSwitchWithoutTheConstantSaysSoAndChangesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	st, rec, err := h.svc.Set(context.Background(), true, author)
	if err != nil {
		t.Fatal(err)
	}
	if _, edits := h.nina.counts(); len(edits) != 0 {
		t.Errorf("edited %v", edits)
	}
	if st.ReadBack.Present || st.ReadBack.Value != nil || st.Matches || st.Desired == nil || !st.Desired.On {
		t.Errorf("status %+v", st)
	}
	if rec.Status != schedcmd.StatusFailed || !strings.Contains(rec.Message, "no Powerups constant named collab") {
		t.Errorf("history %+v", rec)
	}
	if n := len(byKind(h.records(t), schedcmd.KindReplan)); n != 0 {
		t.Errorf("re-planned without the constant")
	}
}

func TestRefusedEditIsReportedAndNotApplied(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	h.nina.refuse = true
	st, rec, err := h.svc.Set(context.Background(), true, author)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != schedcmd.StatusFailed || !strings.Contains(rec.Message, "NINA refused the change") || st.Matches {
		t.Errorf("record %+v status %+v", rec, st)
	}
}

func TestDesiredStateIsSentAgainAfterANINARestart(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	ctx := context.Background()
	if _, _, err := h.svc.Set(ctx, true, author); err != nil {
		t.Fatal(err)
	}
	h.svc.Check(ctx)
	reads, edits := h.nina.counts()
	if reads != 2 || len(edits) != 1 {
		t.Fatalf("a quiet check read the full state: reads %d edits %v", reads, edits)
	}
	h.now = h.now.Add(time.Minute)
	h.nina.restart("2026-10-14T03:00:00-05:00")
	h.svc.Check(ctx)
	reads, edits = h.nina.counts()
	if len(edits) != 2 || edits[1] != constPath+"=1" || reads != 4 {
		t.Fatalf("after restart: reads %d edits %v", reads, edits)
	}
	st, err := h.svc.Status(ctx)
	if err != nil || st.ReadBack.Value == nil || *st.ReadBack.Value != "1" || !st.Matches || st.NINAStarted != "2026-10-14T03:00:00-05:00" {
		t.Errorf("status %+v %v", st, err)
	}
	if st.Desired.SetBy != author {
		t.Errorf("a re-send changed who set it: %+v", st.Desired)
	}
	rs := byKind(h.records(t), schedcmd.KindCollabSwitch)
	if len(rs) != 2 {
		t.Fatalf("history %+v", rs)
	}
	var resent schedcmd.Record
	for _, r := range rs {
		if r.Author == collabswitch.Author {
			resent = r
		}
	}
	if resent.Status != schedcmd.StatusApplied || !strings.Contains(resent.Note, "NINA restarted") || resent.Title != "Send the collab switch again: on" {
		t.Errorf("re-send record %+v", resent)
	}
	if n := len(byKind(h.records(t), schedcmd.KindReplan)); n != 2 {
		t.Errorf("%d re-plans, want one per hand-over", n)
	}
	h.now = h.now.Add(time.Minute)
	h.svc.Check(ctx)
	if reads2, edits2 := h.nina.counts(); reads2 != reads || len(edits2) != len(edits) {
		t.Errorf("checked again without a reason: reads %d edits %v", reads2, edits2)
	}
}

func TestDesiredStateIsSentAgainAfterTheSequenceIsReloaded(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	ctx := context.Background()
	if _, _, err := h.svc.Set(ctx, true, author); err != nil {
		t.Fatal(err)
	}
	h.svc.Check(ctx)
	h.nina.mu.Lock()
	h.nina.value, h.nina.night = "0", "Night two"
	h.nina.mu.Unlock()
	h.now = h.now.Add(time.Minute)
	h.svc.Check(ctx)
	if _, edits := h.nina.counts(); len(edits) != 2 || edits[1] != constPath+"=1" {
		t.Errorf("edits %v", edits)
	}
}

func TestNoSettingMeansNoFullReads(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	ctx := context.Background()
	for range 3 {
		h.svc.Check(ctx)
		h.now = h.now.Add(time.Hour)
	}
	h.nina.restart("later")
	h.svc.Check(ctx)
	if reads, edits := h.nina.counts(); reads != 0 || len(edits) != 0 {
		t.Errorf("reads %d edits %v", reads, edits)
	}
	st, err := h.svc.Status(ctx)
	if err != nil || st.Desired != nil || !st.Reachable || st.NINAStarted != "later" || st.Running == nil || len(st.Running) != 0 {
		t.Errorf("status %+v %v", st, err)
	}
}

func TestUnreachableNINAIsReported(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	h.svc.NINA = collabswitch.NewNINA("http://127.0.0.1:1/v2/api")
	ctx := context.Background()
	if _, _, err := h.svc.Set(ctx, true, author); err != nil {
		t.Fatal(err)
	}
	h.svc.Check(ctx)
	st, err := h.svc.Status(ctx)
	if err != nil || st.Reachable || st.NINAError == "" || st.ReadBack.Value != nil || st.ReadBack.Error == "" || st.Desired == nil || !st.Desired.On {
		t.Errorf("status %+v %v", st, err)
	}
}

func TestUnconfiguredNINAKeepsTheSetting(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	h.svc.NINA = collabswitch.NewNINA("")
	st, rec, err := h.svc.Set(context.Background(), true, author)
	if err != nil || st.Configured || st.Desired == nil || !strings.Contains(rec.Message, "not configured") {
		t.Errorf("%+v %+v %v", st, rec, err)
	}
}

func (h *harness) set(fn func(f *fakeNINA)) {
	h.nina.mu.Lock()
	defer h.nina.mu.Unlock()
	fn(h.nina)
}

func TestAFailingFullReadIsNotRepeatedEveryMinute(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	ctx := context.Background()
	if _, _, err := h.svc.Set(ctx, true, author); err != nil {
		t.Fatal(err)
	}
	h.svc.Check(ctx)
	h.set(func(f *fakeNINA) { f.broken = true })
	h.now = h.now.Add(time.Minute)
	h.nina.restart("restarted")
	h.svc.Check(ctx)
	after, _ := h.nina.counts()
	for range 10 {
		h.now = h.now.Add(time.Minute)
		h.svc.Check(ctx)
	}
	if reads, _ := h.nina.counts(); reads != after {
		t.Errorf("%d full reads in 10 minutes after a failed read", reads-after)
	}
	h.set(func(f *fakeNINA) { f.broken = false })
	h.now = h.now.Add(collabswitch.RetryInterval)
	h.svc.Check(ctx)
	if _, edits := h.nina.counts(); len(edits) != 2 || edits[1] != constPath+"=1" {
		t.Errorf("not sent again once NINA answered: %v", edits)
	}
}

func TestReloadingTheSameSequenceBeforeItStartsIsCaught(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	ctx := context.Background()
	h.set(func(f *fakeNINA) { f.status = created })
	if _, _, err := h.svc.Set(ctx, true, author); err != nil {
		t.Fatal(err)
	}
	h.svc.Check(ctx)
	h.set(func(f *fakeNINA) { f.value = "0" })
	h.now = h.now.Add(collabswitch.RetryInterval)
	h.svc.Check(ctx)
	if _, edits := h.nina.counts(); len(edits) != 2 || edits[1] != constPath+"=1" {
		t.Errorf("edits %v", edits)
	}
}

func TestPanelsAddedWhileRunningDoNotForceFullReads(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	ctx := context.Background()
	if _, _, err := h.svc.Set(ctx, true, author); err != nil {
		t.Fatal(err)
	}
	h.svc.Check(ctx)
	before, _ := h.nina.counts()
	for i := range 5 {
		h.set(func(f *fakeNINA) { f.panel = fmt.Sprintf("M31 · panel %d", i+3) })
		h.now = h.now.Add(time.Minute)
		h.svc.Check(ctx)
	}
	if reads, _ := h.nina.counts(); reads != before {
		t.Errorf("%d full reads for new panels", reads-before)
	}
}

func TestASwitchSurvivesTheBrowserLeaving(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	h.set(func(f *fakeNINA) { f.onEdit = cancel })
	_, rec, err := h.svc.Set(ctx, true, author)
	if err != nil || rec.Status != schedcmd.StatusApplied {
		t.Errorf("record %+v %v", rec, err)
	}
	if n := len(byKind(h.records(t), schedcmd.KindReplan)); n != 1 {
		t.Errorf("%d re-plans after the browser left", n)
	}
}
