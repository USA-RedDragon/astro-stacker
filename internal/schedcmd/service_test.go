package schedcmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	apiName   = "api"
	queueName = "queue"
	author    = "web"
)

type fakeTransport struct {
	name      string
	up        bool
	status    schedcmd.Status
	sent      []schedcmd.Envelope
	cancelled []string
	cancelAs  schedcmd.Status
}

func (f *fakeTransport) Name() string { return f.name }

func (f *fakeTransport) Send(_ context.Context, e schedcmd.Envelope) (schedcmd.Result, error) {
	if !f.up {
		return schedcmd.Result{}, schedcmd.ErrUnreachable
	}
	f.sent = append(f.sent, e)
	return schedcmd.Result{ID: e.ID, Status: f.status}, nil
}

func (f *fakeTransport) Cancel(_ context.Context, id string) (schedcmd.Result, error) {
	if !f.up {
		return schedcmd.Result{}, schedcmd.ErrUnreachable
	}
	f.cancelled = append(f.cancelled, id)
	st := f.cancelAs
	if st == "" {
		st = schedcmd.StatusCancelled
	}
	return schedcmd.Result{ID: id, Status: st}, nil
}

func newService(t *testing.T, transports ...schedcmd.Transport) *schedcmd.Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&schedcmd.Record{}); err != nil {
		t.Fatal(err)
	}
	n := 0
	clock := time.Date(2026, 10, 10, 1, 52, 0, 0, time.UTC)
	return &schedcmd.Service{
		Log:        schedcmd.NewGormLog(db),
		AppDB:      db,
		Transports: transports,
		Now: func() time.Time {
			clock = clock.Add(time.Second)
			return clock
		},
		NewID: func() string { n++; return fmt.Sprintf("cmd-%d", n) },
	}
}

func priorityEdit(before, after int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"entity":"project","id":5,"name":"Cygnis Loop","changes":[{"field":"priority","before":%d,"after":%d}]}`, before, after))
}

func TestSubmitDeliversOverFirstReachableTransport(t *testing.T) {
	t.Parallel()
	api := &fakeTransport{name: apiName, up: true, status: schedcmd.StatusPending}
	queue := &fakeTransport{name: queueName, up: true, status: schedcmd.StatusQueued}
	s := newService(t, api, queue)
	r, err := s.Submit(context.Background(), schedcmd.KindProjectEdit, priorityEdit(1, 0), author)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != schedcmd.StatusPending || r.Transport != apiName {
		t.Fatalf("got %s via %q", r.Status, r.Transport)
	}
	if len(queue.sent) != 0 {
		t.Fatal("queue used although the API answered")
	}
	if r.Title != "Cygnis Loop · Priority" {
		t.Fatalf("title %q", r.Title)
	}
	if string(r.Diffs[0].Before) != `"Normal"` || string(r.Diffs[0].After) != `"Low"` {
		t.Fatalf("diff %s -> %s", r.Diffs[0].Before, r.Diffs[0].After)
	}
}

func TestSubmitFallsBackToQueue(t *testing.T) {
	t.Parallel()
	api := &fakeTransport{name: apiName}
	queue := &fakeTransport{name: queueName, up: true, status: schedcmd.StatusQueued}
	s := newService(t, api, queue)
	r, err := s.Submit(context.Background(), schedcmd.KindReplan, nil, author)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != schedcmd.StatusQueued || r.Transport != queueName {
		t.Fatalf("got %s via %q", r.Status, r.Transport)
	}
}

func TestSubmitKeepsCommandWhenNothingReachable(t *testing.T) {
	t.Parallel()
	s := newService(t, &fakeTransport{name: apiName})
	r, err := s.Submit(context.Background(), schedcmd.KindReplan, nil, author)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != schedcmd.StatusQueued || r.Transport != "" || r.Attempts != 1 {
		t.Fatalf("got %+v", r)
	}
}

func TestSubmitRejectsInvalid(t *testing.T) {
	t.Parallel()
	s := newService(t)
	_, err := s.Submit(context.Background(), schedcmd.KindProjectEdit, json.RawMessage(`{"id":5,"changes":[{"field":"name","before":"a","after":"b"}]}`), author)
	if !errors.Is(err, schedcmd.ErrInvalid) {
		t.Fatalf("err %v", err)
	}
	_, err = s.Submit(context.Background(), "nope", nil, author)
	if !errors.Is(err, schedcmd.ErrUnknownKind) {
		t.Fatalf("err %v", err)
	}
}

func TestUndoAppliedSendsInverse(t *testing.T) {
	t.Parallel()
	api := &fakeTransport{name: apiName, up: true, status: schedcmd.StatusApplied}
	s := newService(t, api)
	ctx := context.Background()
	r, err := s.Submit(ctx, schedcmd.KindProjectEdit, priorityEdit(1, 0), author)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.Undo(ctx, r.ID, author)
	if err != nil {
		t.Fatal(err)
	}
	if u.UndoOf != r.ID || u.Title != "Undo: Cygnis Loop · Priority" {
		t.Fatalf("undo %+v", u)
	}
	var p schedcmd.EditPayload
	if err := json.Unmarshal(api.sent[1].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if string(p.Changes[0].Before) != "0" || string(p.Changes[0].After) != "1" {
		t.Fatalf("inverse %s -> %s", p.Changes[0].Before, p.Changes[0].After)
	}
	orig, _ := s.Log.Get(ctx, r.ID)
	if orig.UndoneBy != u.ID {
		t.Fatal("original not marked undone")
	}
	if _, err := s.Undo(ctx, r.ID, author); !errors.Is(err, schedcmd.ErrNotUndoable) {
		t.Fatalf("second undo: %v", err)
	}
}

func TestUndoWaitingCancels(t *testing.T) {
	t.Parallel()
	api := &fakeTransport{name: apiName, up: true, status: schedcmd.StatusPending}
	s := newService(t, api)
	ctx := context.Background()
	r, _ := s.Submit(ctx, schedcmd.KindSkip, json.RawMessage(`{"scope":"target","target_id":13,"target_name":"Panel 2","minutes":60}`), author)
	c, err := s.Undo(ctx, r.ID, author)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != r.ID || c.Status != schedcmd.StatusCancelled || len(api.cancelled) != 1 {
		t.Fatalf("got %+v", c)
	}
}

func TestCancelTooLate(t *testing.T) {
	t.Parallel()
	api := &fakeTransport{name: apiName, up: true, status: schedcmd.StatusPending, cancelAs: schedcmd.StatusApplied}
	s := newService(t, api)
	ctx := context.Background()
	r, _ := s.Submit(ctx, schedcmd.KindReplan, nil, author)
	c, err := s.Cancel(ctx, r.ID)
	if !errors.Is(err, schedcmd.ErrNotWaiting) || c.Status != schedcmd.StatusApplied {
		t.Fatalf("got %s %v", c.Status, err)
	}
}

func TestCancelLocalQueueWhileOffline(t *testing.T) {
	t.Parallel()
	s := newService(t, &fakeTransport{name: apiName})
	ctx := context.Background()
	r, _ := s.Submit(ctx, schedcmd.KindReplan, nil, author)
	c, err := s.Cancel(ctx, r.ID)
	if err != nil || c.Status != schedcmd.StatusCancelled {
		t.Fatalf("got %s %v", c.Status, err)
	}
}

func TestResultsNeverRegress(t *testing.T) {
	t.Parallel()
	api := &fakeTransport{name: apiName, up: true, status: schedcmd.StatusPending}
	s := newService(t, api)
	ctx := context.Background()
	r, _ := s.Submit(ctx, schedcmd.KindReplan, nil, author)
	got, _ := s.ApplyResult(ctx, schedcmd.Result{ID: r.ID, Status: schedcmd.StatusQueued})
	if got.Status != schedcmd.StatusPending {
		t.Fatalf("pending regressed to %s", got.Status)
	}
	got, _ = s.ApplyResult(ctx, schedcmd.Result{ID: r.ID, Status: schedcmd.StatusApplied})
	if got.Status != schedcmd.StatusApplied || got.AppliedAt == nil {
		t.Fatalf("got %+v", got)
	}
	got, _ = s.ApplyResult(ctx, schedcmd.Result{ID: r.ID, Status: schedcmd.StatusFailed})
	if got.Status != schedcmd.StatusApplied {
		t.Fatalf("applied regressed to %s", got.Status)
	}
}

func TestRedeliver(t *testing.T) {
	t.Parallel()
	api := &fakeTransport{name: apiName}
	s := newService(t, api)
	ctx := context.Background()
	r, _ := s.Submit(ctx, schedcmd.KindReplan, nil, author)
	api.up = true
	api.status = schedcmd.StatusPending
	if n := s.Redeliver(ctx); n != 1 {
		t.Fatalf("redelivered %d", n)
	}
	got, _ := s.Log.Get(ctx, r.ID)
	if got.Status != schedcmd.StatusPending || got.Transport != apiName {
		t.Fatalf("got %+v", got)
	}
}

func TestListFilters(t *testing.T) {
	t.Parallel()
	api := &fakeTransport{name: apiName, up: true, status: schedcmd.StatusApplied}
	s := newService(t, api)
	ctx := context.Background()
	_, _ = s.Submit(ctx, schedcmd.KindProjectEdit, priorityEdit(1, 2), author)
	_, _ = s.Submit(ctx, schedcmd.KindExposurePlanEdit, json.RawMessage(`{"id":40,"name":"Ha","parent":"Pelican Panel 2","changes":[{"field":"desired","before":300,"after":200}]}`), author)
	all, _ := s.Log.List(ctx, schedcmd.Filter{})
	if len(all) != 2 || all[0].Category != schedcmd.CategoryPlans {
		t.Fatalf("list %+v", all)
	}
	got, _ := s.Log.List(ctx, schedcmd.Filter{Query: "cygnis"})
	if len(got) != 1 || got[0].Kind != schedcmd.KindProjectEdit {
		t.Fatalf("query %+v", got)
	}
	got, _ = s.Log.List(ctx, schedcmd.Filter{Category: schedcmd.CategoryPlans})
	if len(got) != 1 {
		t.Fatalf("category %+v", got)
	}
}

func TestEveryKindHasAnInverseOrSaysNo(t *testing.T) {
	t.Parallel()
	samples := map[schedcmd.Kind]string{
		schedcmd.KindProjectEdit:       string(priorityEdit(0, 1)),
		schedcmd.KindTargetEdit:        `{"id":13,"name":"Panel 2","changes":[{"field":"active","before":true,"after":false}]}`,
		schedcmd.KindExposurePlanEdit:  `{"id":40,"name":"Ha","changes":[{"field":"enabled","before":true,"after":false}]}`,
		schedcmd.KindSkip:              `{"scope":"project","project_id":5,"project_name":"Sadr Region"}`,
		schedcmd.KindUnskip:            `{"scope":"project","project_id":5,"project_name":"Sadr Region"}`,
		schedcmd.KindPause:             `{"mount":"park","resume_after_minutes":30}`,
		schedcmd.KindResume:            `{}`,
		schedcmd.KindReplan:            `{}`,
		schedcmd.KindGoalEdit:          planningSamples[schedcmd.KindGoalEdit],
		schedcmd.KindRuleWeightEdit:    planningSamples[schedcmd.KindRuleWeightEdit],
		"exposuretemplate.edit":        planningSamples["exposuretemplate.edit"],
		schedcmd.KindTemplateBatchEdit: planningSamples[schedcmd.KindTemplateBatchEdit],
		schedcmd.KindTemplateClone:     planningSamples[schedcmd.KindTemplateClone],
		schedcmd.KindTemplateDelete:    planningSamples[schedcmd.KindTemplateDelete],
		schedcmd.KindApplySet:          planningSamples[schedcmd.KindApplySet],
		schedcmd.KindUnapplySet:        planningSamples[schedcmd.KindApplySet],
		schedcmd.KindProjectCreate:     planningSamples[schedcmd.KindProjectCreate],
		schedcmd.KindProjectDelete:     planningSamples[schedcmd.KindProjectCreate],
		schedcmd.KindCatalogMatch:      `{"subject":"project:Garlic Nebula","subject_name":"Garlic Nebula","object_id":"G116.9+00.2","before":"","after":"confirmed"}`,
	}
	reg := schedcmd.Default()
	for _, k := range reg.Kinds() {
		p, ok := samples[k]
		if !ok {
			t.Fatalf("no sample for %s", k)
		}
		spec, _ := reg.Get(k)
		if err := spec.Validate(json.RawMessage(p)); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		ik, inv, err := spec.Inverse(json.RawMessage(p))
		if errors.Is(err, schedcmd.ErrNoInverse) {
			continue
		}
		if err != nil {
			t.Fatalf("%s inverse: %v", k, err)
		}
		ispec, err := reg.Get(ik)
		if err != nil {
			t.Fatalf("%s inverse kind: %v", k, err)
		}
		if err := ispec.Validate(inv); err != nil {
			t.Fatalf("%s inverse invalid: %v", k, err)
		}
	}
}
