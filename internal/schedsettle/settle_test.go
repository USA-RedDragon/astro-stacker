package schedsettle_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/schedsettle"
)

const createCmd = "create"

type cmds struct{ recs map[string]schedcmd.Record }

func (c *cmds) SubmitOnce(_ context.Context, id string, kind schedcmd.Kind, payload json.RawMessage, _ string) (schedcmd.Record, error) {
	r := schedcmd.Record{ID: id, Kind: kind, Payload: payload, Status: schedcmd.StatusApplied}
	c.recs[id] = r
	return r, nil
}

func (c *cmds) Status(_ context.Context, id string) (schedcmd.Record, error) {
	r, ok := c.recs[id]
	if !ok {
		return r, schedcmd.ErrNotFound
	}
	return r, nil
}

func (c *cmds) Undo(_ context.Context, _, _ string) (schedcmd.Record, error) {
	return schedcmd.Record{}, schedcmd.ErrNotUndoable
}

// TestCancelledCreateWatchedToItsWindowEnd keeps a cancelled create owed
// until its window ends, since the plugin can still apply it; when it does,
// its project is closed.
func TestCancelledCreateWatchedToItsWindowEnd(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	c := &cmds{recs: map[string]schedcmd.Record{createCmd: {ID: createCmd, Status: schedcmd.StatusCancelled}}}
	var projects []planning.Project
	read := func(context.Context) (*planning.Snapshot, error) { return &planning.Snapshot{Projects: projects}, nil }
	a := schedsettle.Ask{Owe: schedsettle.Withdraw, ProjectGUID: "g", CreateCmd: createCmd, CloseID: "close-g", Start: now.Add(-time.Hour), End: now.Add(time.Hour)}
	if done, _, err := schedsettle.Step(context.Background(), c, &schedsettle.Snapshots{Read: read}, a, now); done || err != nil {
		t.Fatalf("done %v %v, want a cancelled create watched until its window ends", done, err)
	}
	c.recs[createCmd] = schedcmd.Record{ID: createCmd, Status: schedcmd.StatusApplied}
	projects = []planning.Project{{ID: 7, GUID: "g", Name: "Watch", State: "Active"}}
	done, closeCmd, err := schedsettle.Step(context.Background(), c, &schedsettle.Snapshots{Read: read}, a, now)
	if done || err != nil || closeCmd != "close-g" || c.recs["close-g"].Kind != schedcmd.KindProjectEdit {
		t.Fatalf("done %v close %q %v, want the landed project closed", done, closeCmd, err)
	}
	a.CloseCmd = closeCmd
	if done, _, _ := schedsettle.Step(context.Background(), c, &schedsettle.Snapshots{Read: read}, a, now); !done {
		t.Error("an applied close should settle it")
	}
	gone := a
	gone.CloseCmd = ""
	c.recs[createCmd] = schedcmd.Record{ID: createCmd, Status: schedcmd.StatusCancelled}
	if done, _, _ := schedsettle.Step(context.Background(), c, &schedsettle.Snapshots{Read: read}, gone, now.Add(2*time.Hour)); !done {
		t.Error("a cancelled create is settled once its window has ended")
	}
}
