package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/skyevents"
)

const rareTitle = "Jupiter with Saturn"

func rareNow() time.Time { return time.Date(2020, 10, 12, 20, 0, 0, 0, time.UTC) }

func str(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func eventNamed(t *testing.T, out map[string]any, title string) map[string]any {
	t.Helper()
	evs, _ := out["events"].([]any)
	for _, e := range evs {
		m, _ := e.(map[string]any)
		if m["title"] == title {
			return m
		}
	}
	t.Fatalf("no %s in %d events", title, len(evs))
	return nil
}

func rareCount(out map[string]any) int {
	n := 0
	evs, _ := out["events"].([]any)
	for _, e := range evs {
		m, _ := e.(map[string]any)
		r, _ := m["rarity"].(map[string]any)
		if r["rare"] == true {
			n++
		}
	}
	return n
}

func autoState(t *testing.T, rig skyEventsRig) (map[string]any, map[string]any) {
	t.Helper()
	_, out := rig.call(http.MethodGet, "/api/v1/sky-events", "")
	ev := eventNamed(t, out, rareTitle)
	a, _ := ev["auto_plan"].(map[string]any)
	return ev, a
}

func projectCreates(tr *sentCommands) int {
	n := 0
	for _, e := range tr.sent {
		if e.Kind == schedcmd.KindProjectCreate {
			n++
		}
	}
	return n
}

func addWindowTable(t *testing.T, rig skyEventsRig) {
	t.Helper()
	if err := rig.sched.DB().Exec(windowTableSQL).Error; err != nil {
		t.Fatal(err)
	}
}

func TestRareSkyEventsPlanThemselvesOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rig := skyEventsEngineAt(t, false, rareNow())
	_, out := rig.call(http.MethodGet, "/api/v1/sky-events", "")
	rare := rareCount(out)
	ev := eventNamed(t, out, rareTitle)
	r, _ := ev["rarity"].(map[string]any)
	if rare == 0 || r["rare"] != true || r["previous"] == nil || r["next"] == nil {
		t.Fatalf("rarity %v", r)
	}
	if _, a := autoState(t, rig); a["state"] != skyevents.AutoWaiting || a["reason"] == nil {
		t.Fatalf("before the plugin table: %v", a)
	}
	if err := rig.svc.AutoPlanRare(ctx); err != nil || len(rig.tr.sent) != 0 {
		t.Fatalf("planned without the plugin table: %v %+v", err, rig.tr.sent)
	}
	addWindowTable(t, rig)
	if err := rig.svc.AutoPlanRare(ctx); err != nil {
		t.Fatal(err)
	}
	if projectCreates(rig.tr) != rare {
		t.Fatalf("%d project.create for %d rare events", projectCreates(rig.tr), rare)
	}
	var p schedcmd.ProjectCreatePayload
	sentPayload(t, rig.tr, 0, schedcmd.KindProjectCreate, &p)
	if p.Project.Priority != 2 || p.Targets[0].Window.Priority != schedcmd.EventPriorityHigh {
		t.Errorf("not high priority: %+v %+v", p.Project, p.Targets[0].Window)
	}
	ev, a := autoState(t, rig)
	plan, _ := ev["plan"].(map[string]any)
	if a["state"] != skyevents.AutoPlanned || plan["automatic"] != true || ev["priority"] != skyevents.PriorityHigh {
		t.Fatalf("after auto-plan: %v %v", a, plan)
	}
	rec, err := rig.cmds.Log.Get(ctx, str(plan, "command_id"))
	if err != nil || rec.Author != skyevents.AutoAuthor {
		t.Errorf("command %+v %v", rec, err)
	}
	if err := rig.svc.AutoPlanRare(ctx); err != nil || projectCreates(rig.tr) != rare {
		t.Fatalf("second pass: %v, %d sent", err, projectCreates(rig.tr))
	}
	restartAndAutoPlan(t, rig, rare)
	if code, out := rig.call(http.MethodPost, "/api/v1/sky-events/"+str(ev, "id")+"/unplan", ``); code != http.StatusOK {
		t.Fatalf("unplan %d %v", code, out)
	}
	if err := rig.svc.AutoPlanRare(ctx); err != nil || projectCreates(rig.tr) != rare {
		t.Fatalf("re-planned after unplan: %v, %d sent", err, projectCreates(rig.tr))
	}
	if _, a := autoState(t, rig); a["state"] != skyevents.AutoDeclined {
		t.Errorf("after unplan: %v", a)
	}
	if code, out := rig.call(http.MethodPost, "/api/v1/sky-events/"+str(ev, "id")+"/plan", `{"priority":"high"}`); code != http.StatusOK {
		t.Fatalf("planning by hand after unplan: %d %v", code, out)
	}
	ev, _ = autoState(t, rig)
	if plan, _ := ev["plan"].(map[string]any); plan["automatic"] != false {
		t.Errorf("a plan by hand shows as automatic: %v", plan)
	}
}

func TestLoweringARareEventTurnsDownAutoPlanning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rig := skyEventsEngineAt(t, true, rareNow())
	_, out := rig.call(http.MethodGet, "/api/v1/sky-events", "")
	rare := rareCount(out)
	id := str(eventNamed(t, out, rareTitle), "id")
	if code, out := rig.call(http.MethodPost, "/api/v1/sky-events/"+id+"/priority", `{"priority":"normal"}`); code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	if err := rig.svc.AutoPlanRare(ctx); err != nil {
		t.Fatal(err)
	}
	if projectCreates(rig.tr) != rare-1 {
		t.Fatalf("%d project.create for %d rare events, one turned down", projectCreates(rig.tr), rare)
	}
	ev, a := autoState(t, rig)
	if a["state"] != skyevents.AutoDeclined || ev["plan"] != nil {
		t.Errorf("turned down: %v %v", a, ev["plan"])
	}
}

func TestAutoPlanReleasesOnlyUnsentClaims(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rig := skyEventsEngineAt(t, true, rareNow())
	_, out := rig.call(http.MethodGet, "/api/v1/sky-events", "")
	rare := rareCount(out)
	works := rig.svc.Snapshot
	rig.svc.Snapshot = func(context.Context) (*planning.Snapshot, error) { return nil, errors.New("snapshot down") }
	if err := rig.svc.AutoPlanRare(ctx); err == nil || len(rig.tr.sent) != 0 {
		t.Fatalf("%v %+v", err, rig.tr.sent)
	}
	if _, a := autoState(t, rig); a["state"] != skyevents.AutoWaiting {
		t.Errorf("after a failed attempt: %v", a)
	}
	rig.svc.Snapshot = works
	if err := rig.svc.AutoPlanRare(ctx); err != nil || projectCreates(rig.tr) != rare {
		t.Fatalf("retry: %v, %d sent for %d", err, projectCreates(rig.tr), rare)
	}
}

func TestReplicasAutoPlanEachEventOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rig := skyEventsEngineAt(t, true, rareNow())
	_, out := rig.call(http.MethodGet, "/api/v1/sky-events", "")
	rare := rareCount(out)
	var lock sync.Mutex
	other := &sentCommands{}
	cmds := &schedcmd.Service{Transports: []schedcmd.Transport{&lockedSender{mu: &lock, inner: other}}, Log: rig.cmds.Log, AppDB: rig.cmds.AppDB}
	n := 0
	cmds.NewID = func() string { n++; return fmt.Sprintf("replica-%d", n) }
	rig.cmds.Transports = []schedcmd.Transport{&lockedSender{mu: &lock, inner: rig.tr}}
	second := newSkyEventsService(rig.app, rig.sched, cmds, rareNow())
	if err := second.Load(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, s := range []*skyevents.Service{rig.svc, second} {
		wg.Go(func() { _ = s.AutoPlanRare(ctx) })
	}
	wg.Wait()
	for _, s := range []*skyevents.Service{rig.svc, second} {
		if err := s.AutoPlanRare(ctx); err != nil {
			t.Fatal(err)
		}
	}
	lock.Lock()
	defer lock.Unlock()
	if got := projectCreates(rig.tr) + projectCreates(other); got != rare {
		t.Fatalf("%d project.create from two replicas for %d rare events", got, rare)
	}
}

type lockedSender struct {
	mu    *sync.Mutex
	inner *sentCommands
}

func (l *lockedSender) Name() string { return l.inner.Name() }

func (l *lockedSender) Send(ctx context.Context, e schedcmd.Envelope) (schedcmd.Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.Send(ctx, e)
}

func (l *lockedSender) Cancel(ctx context.Context, id string) (schedcmd.Result, error) {
	return l.inner.Cancel(ctx, id)
}

func restartAndAutoPlan(t *testing.T, rig skyEventsRig, rare int) {
	t.Helper()
	ctx := context.Background()
	restarted := newSkyEventsService(rig.app, rig.sched, rig.cmds, rareNow())
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.AutoPlanRare(ctx); err != nil || projectCreates(rig.tr) != rare {
		t.Fatalf("after a restart: %v, %d sent", err, projectCreates(rig.tr))
	}
	if _, err := restarted.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.AutoPlanRare(ctx); err != nil || projectCreates(rig.tr) != rare {
		t.Fatalf("after a restart and a new search: %v, %d sent", err, projectCreates(rig.tr))
	}
}
