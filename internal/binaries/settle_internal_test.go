package binaries

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
)

// lateCommands holds creates waiting at an observatory it cannot reach
// until land, applies other commands at once, and takes a landed command
// back with an undo that applies at once.
type lateCommands struct {
	sent map[string]schedcmd.Record
}

func (f *lateCommands) SubmitOnce(_ context.Context, id string, kind schedcmd.Kind, payload json.RawMessage, _ string) (schedcmd.Record, error) {
	if r, ok := f.sent[id]; ok {
		return r, nil
	}
	r := schedcmd.Record{ID: id, Kind: kind, Payload: payload, Status: schedcmd.StatusApplied}
	if kind == schedcmd.KindProjectCreate {
		r.Status = schedcmd.StatusPending
	}
	f.sent[id] = r
	return r, nil
}

func (f *lateCommands) Status(_ context.Context, id string) (schedcmd.Record, error) {
	r, ok := f.sent[id]
	if !ok {
		return r, schedcmd.ErrNotFound
	}
	return r, nil
}

func (f *lateCommands) Undo(_ context.Context, id, _ string) (schedcmd.Record, error) {
	r := f.sent[id]
	if r.Status.Waiting() {
		return r, schedcmd.ErrUnreachable
	}
	u := schedcmd.Record{ID: "undo-" + id, Status: schedcmd.StatusApplied, UndoOf: id}
	f.sent[u.ID] = u
	r.UndoneBy = u.ID
	f.sent[id] = r
	return u, nil
}

func (f *lateCommands) land(id string) {
	r := f.sent[id]
	r.Status = schedcmd.StatusApplied
	f.sent[id] = r
}

func (f *lateCommands) closes() []schedcmd.Record {
	var out []schedcmd.Record
	for _, r := range f.sent {
		if r.Kind == schedcmd.KindProjectEdit {
			out = append(out, r)
		}
	}
	return out
}

// TestCancelAndCloseOutlastALateCreate cancels one eclipse request before
// its window and one under way, and lets a third's window pass, all while
// their creates wait on an unreachable observatory. Once the creates land,
// the first is taken back and the others closed, and nothing is owed.
func TestCancelAndCloseOutlastALateCreate(t *testing.T) {
	t.Parallel()
	db := testdb.SQLite(t, "settle")
	if err := db.AutoMigrate(&app.EclipseRequest{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cmds := &lateCommands{sent: map[string]schedcmd.Record{}}
	reqs := []app.EclipseRequest{
		{SourceID: 1, Kind: KindPrimary, Start: now.Add(48 * time.Hour), End: now.Add(52 * time.Hour)},
		{SourceID: 2, Kind: KindPrimary, Start: now.Add(-time.Hour), End: now.Add(3 * time.Hour)},
		{SourceID: 3, Kind: KindPrimary, Start: now.Add(-6 * time.Hour), End: now.Add(-2 * time.Hour)},
	}
	landed := make([]planning.Project, 0, len(reqs))
	svc := &Service{DB: db, Commands: cmds, Now: func() time.Time { return now },
		Snapshot: func(context.Context) (*planning.Snapshot, error) { return &planning.Snapshot{Projects: landed}, nil }}
	ctx := context.Background()
	for i := range reqs {
		r := &reqs[i]
		r.ProjectGUID, r.State, r.Mid = []string{"p-ahead", "p-under-way", "p-passed"}[i], app.EclipseSent, r.Start.Add(time.Hour)
		rec, _ := cmds.SubmitOnce(ctx, "eclipse-request-"+r.ProjectGUID, schedcmd.KindProjectCreate, json.RawMessage(`{}`), RequestAuthor)
		r.CreateCmd = rec.ID
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int{reqs[0].ID, reqs[1].ID} {
		r, err := svc.CancelRequest(ctx, id)
		if err != nil || r.State != app.EclipseCancel || r.Settle != "withdraw" {
			t.Fatalf("cancel %d: %+v %v, want cancelled and owing its withdraw", id, r, err)
		}
	}
	if err := svc.followRequests(ctx); err != nil {
		t.Fatal(err)
	}
	get := func(id int) app.EclipseRequest {
		var r app.EclipseRequest
		if err := db.First(&r, id).Error; err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := get(reqs[2].ID); r.State != app.EclipseDone || r.Settle != "close" {
		t.Fatalf("passed request %s owing %q, want done and owing its close", r.State, r.Settle)
	}
	for i := range reqs {
		cmds.land(reqs[i].CreateCmd)
		landed = append(landed, planning.Project{ID: 50 + i, GUID: reqs[i].ProjectGUID, Name: "Eclipse", State: "Active"})
	}
	if err := svc.followRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := cmds.Status(ctx, reqs[0].CreateCmd); r.UndoneBy == "" {
		t.Error("the request cancelled before its window was not taken back once it landed")
	}
	closed := map[string]bool{}
	for _, c := range cmds.closes() {
		for _, g := range []string{"p-ahead", "p-under-way", "p-passed"} {
			if strings.Contains(string(c.Payload), g) {
				closed[g] = true
			}
		}
	}
	if len(closed) != 2 || !closed["p-under-way"] || !closed["p-passed"] {
		t.Fatalf("closed %v, want the request under way and the passed one", closed)
	}
	if err := svc.followRequests(ctx); err != nil {
		t.Fatal(err)
	}
	for i := range reqs {
		if r := get(reqs[i].ID); r.Settle != "" {
			t.Errorf("request %s still owes %q", r.ProjectGUID, r.Settle)
		}
	}
}
