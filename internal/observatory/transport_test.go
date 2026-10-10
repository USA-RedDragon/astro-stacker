package observatory_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"gorm.io/gorm"
)

const resumePayload = `{}`

func queueDB(t *testing.T, withTables bool) *gorm.DB {
	t.Helper()
	db := memDB(t, "sched")
	if withTables {
		if err := db.Exec(schedcmd.QueueSchemaPostgres).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func service(t *testing.T, transports ...schedcmd.Transport) *schedcmd.Service {
	t.Helper()
	db := memDB(t, "app")
	if err := db.AutoMigrate(&schedcmd.Record{}); err != nil {
		t.Fatal(err)
	}
	n := 0
	return &schedcmd.Service{
		Log:        schedcmd.NewGormLog(db),
		AppDB:      db,
		Transports: transports,
		NewID:      func() string { n++; return fmt.Sprintf("cmd-%d", n) },
	}
}

func TestQueueTransportWithoutTable(t *testing.T) {
	t.Parallel()
	q := observatory.NewQueueTransport(queueDB(t, false))
	if _, err := q.Send(context.Background(), schedcmd.Envelope{ID: "a"}); !errors.Is(err, schedcmd.ErrUnreachable) {
		t.Fatalf("send: %v", err)
	}
	if _, err := q.Cancel(context.Background(), "a"); !errors.Is(err, schedcmd.ErrUnreachable) {
		t.Fatalf("cancel: %v", err)
	}
	if q.DB.Migrator().HasTable("ts_command") {
		t.Fatal("the stacker created ts_command")
	}
}

func TestQueueTransportTableCheckIsCached(t *testing.T) {
	t.Parallel()
	db := queueDB(t, false)
	now := time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)
	q := observatory.NewQueueTransport(db)
	q.Table.Now = func() time.Time { return now }
	if _, err := q.Send(context.Background(), schedcmd.Envelope{ID: "a"}); !errors.Is(err, schedcmd.ErrUnreachable) {
		t.Fatal(err)
	}
	if err := db.Exec(schedcmd.QueueSchemaPostgres).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := q.Send(context.Background(), schedcmd.Envelope{ID: "a"}); !errors.Is(err, schedcmd.ErrUnreachable) {
		t.Fatalf("within a minute the old answer holds: %v", err)
	}
	now = now.Add(observatory.TableCheckTTL)
	if r, err := q.Send(context.Background(), schedcmd.Envelope{ID: "a"}); err != nil || r.Status != schedcmd.StatusQueued {
		t.Fatalf("after a minute: %+v %v", r, err)
	}
}

func TestQueueTransportSendAndCancel(t *testing.T) {
	t.Parallel()
	db := queueDB(t, true)
	q := observatory.NewQueueTransport(db)
	ctx := context.Background()
	env := schedcmd.Envelope{ID: "a", Kind: schedcmd.KindResume, Payload: json.RawMessage(resumePayload), Author: "web", UndoOf: "z", CreatedAt: time.Now()}
	for range 2 {
		r, err := q.Send(ctx, env)
		if err != nil || r.Status != schedcmd.StatusQueued || r.ID != "a" {
			t.Fatalf("send: %+v %v", r, err)
		}
	}
	var rows []schedcmd.QueuedCommand
	if err := db.Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].Kind != string(schedcmd.KindResume) || rows[0].UndoOf == nil || *rows[0].UndoOf != "z" {
		t.Fatalf("rows %+v %v", rows, err)
	}
	r, err := q.Cancel(ctx, "a")
	if err != nil || r.Status != schedcmd.StatusCancelled {
		t.Fatalf("cancel: %+v %v", r, err)
	}
	if err := db.Find(&rows).Error; err != nil || rows[0].Cancelled != 1 {
		t.Fatalf("not cancelled: %+v", rows)
	}
	if _, err := q.Cancel(ctx, "missing"); err == nil {
		t.Fatal("cancelled a command that was never queued")
	}
}

func TestServiceFallsBackToQueue(t *testing.T) {
	t.Parallel()
	sched := queueDB(t, true)
	api := observatory.NewAPITransport(observatory.NewClient("", ""))
	svc := service(t, api, observatory.NewQueueTransport(sched))
	r, err := svc.Submit(context.Background(), schedcmd.KindResume, json.RawMessage(resumePayload), "web")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != schedcmd.StatusQueued || r.Transport != "queue" {
		t.Fatalf("record %+v", r)
	}
	var n int64
	sched.Model(&schedcmd.QueuedCommand{}).Count(&n)
	if n != 1 {
		t.Fatalf("%d queued rows", n)
	}

	c, err := svc.Cancel(context.Background(), r.ID)
	if err != nil || c.Status != schedcmd.StatusCancelled {
		t.Fatalf("cancel %+v %v", c, err)
	}
}

func TestServiceWithNothingConfiguredStaysQueued(t *testing.T) {
	t.Parallel()
	svc := service(t, observatory.NewAPITransport(observatory.NewClient("", "")), observatory.NewQueueTransport(queueDB(t, false)))
	r, err := svc.Submit(context.Background(), schedcmd.KindResume, json.RawMessage(resumePayload), "web")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != schedcmd.StatusQueued || r.Attempts != 1 {
		t.Fatalf("record %+v", r)
	}
}

func TestQueueResults(t *testing.T) {
	t.Parallel()
	sched := queueDB(t, true)
	svc := service(t)
	ctx := context.Background()
	r, err := svc.Submit(ctx, schedcmd.KindResume, json.RawMessage(resumePayload), "web")
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.Submit(ctx, schedcmd.KindResume, json.RawMessage(resumePayload), "web")
	if err != nil {
		t.Fatal(err)
	}
	msg := "Applied by the plugin"
	detail := "not json"
	if err := sched.Create(&schedcmd.QueuedResult{CommandID: r.ID, Status: string(schedcmd.StatusApplied), Message: &msg, Detail: &detail, UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := sched.Create(&schedcmd.QueuedResult{CommandID: "unrelated", Status: string(schedcmd.StatusApplied), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	n, err := observatory.NewQueueResults(sched, svc).Once(ctx)
	if err != nil || n != 1 {
		t.Fatalf("applied %d: %v", n, err)
	}
	got, _ := svc.Log.Get(ctx, r.ID)
	if got.Status != schedcmd.StatusApplied || got.Message != msg || string(got.Detail) != `"not json"` || got.AppliedAt == nil {
		t.Fatalf("record %+v", got)
	}
	still, _ := svc.Log.Get(ctx, other.ID)
	if still.Status != schedcmd.StatusQueued {
		t.Fatalf("other %+v", still)
	}

	none, err := observatory.NewQueueResults(queueDB(t, false), svc).Once(ctx)
	if err != nil || none != 0 {
		t.Fatalf("without the table: %d %v", none, err)
	}
}

func TestRedelivererSendsAndReconciles(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	mode := "down"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		m := mode
		mu.Unlock()
		if m == "down" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodPost:
			var e schedcmd.Envelope
			_ = json.NewDecoder(r.Body).Decode(&e)
			_, _ = fmt.Fprintf(w, `{"id":%q,"status":"pending"}`, e.ID)
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"id":"ignored","status":"applied","message":"done"}`)
		}
	}))
	defer srv.Close()
	client := observatory.NewClient(srv.URL, token)
	svc := service(t, observatory.NewAPITransport(client))
	ctx := context.Background()
	r, err := svc.Submit(ctx, schedcmd.KindResume, json.RawMessage(resumePayload), "web")
	if err != nil || r.Status != schedcmd.StatusQueued {
		t.Fatalf("submit %+v %v", r, err)
	}
	mu.Lock()
	mode = "up"
	mu.Unlock()
	rd := &observatory.Redeliverer{Service: svc, Client: client}
	svc.Redeliver(ctx)
	got, _ := svc.Log.Get(ctx, r.ID)
	if got.Status != schedcmd.StatusPending || got.Transport != "api" {
		t.Fatalf("after redeliver %+v", got)
	}
	rd.Once(ctx)
	got, _ = svc.Log.Get(ctx, r.ID)
	if got.Status != schedcmd.StatusApplied || got.Message != "done" {
		t.Fatalf("after reconcile %+v", got)
	}

	runCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	rd.Run(runCtx, 10*time.Millisecond)
}
