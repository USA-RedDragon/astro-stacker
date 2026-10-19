// Package schedsettle takes back or closes the Target Scheduler project a
// create command made, over as many looks as it takes. A project the
// stacker no longer wants may not be in the scheduler yet when that is
// decided: its create command can still wait on the observatory, or the
// planning snapshot can lag. The caller records what it owes and calls
// Step on each run until Step says it is settled.
package schedsettle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

// What a request owes the scheduler.
const (
	// Withdraw takes the project back, or closes it once its window has
	// begun.
	Withdraw = "withdraw"
	// Close closes the project.
	Close = "close"
)

// GiveUp is how long after its window ends a project that never showed up
// in the scheduler stops being looked for; by then its window keeps the
// plugin from imaging it.
const GiveUp = 7 * 24 * time.Hour

// Commands reads, sends and takes back scheduler commands.
type Commands interface {
	SubmitOnce(ctx context.Context, id string, kind schedcmd.Kind, payload json.RawMessage, author string) (schedcmd.Record, error)
	Status(ctx context.Context, id string) (schedcmd.Record, error)
	Undo(ctx context.Context, id, author string) (schedcmd.Record, error)
}

// Ask is one project owed a withdraw or a close. CloseID names its close
// command; CloseCmd is the close command sent last, if any.
type Ask struct {
	Owe         string
	ProjectGUID string
	CreateCmd   string
	CloseCmd    string
	CloseID     string
	Author      string
	Start, End  time.Time
}

// Snapshots reads the planning snapshot at most once.
type Snapshots struct {
	Read func(ctx context.Context) (*planning.Snapshot, error)
	snap *planning.Snapshot
	err  error
	done bool
}

func (s *Snapshots) get(ctx context.Context) (*planning.Snapshot, error) {
	if s.Read == nil {
		return nil, errors.New("no planning snapshot")
	}
	if !s.done {
		s.snap, s.err = s.Read(ctx)
		s.done = true
	}
	return s.snap, s.err
}

// Step takes one step toward what a owes at now. It says whether a is
// settled and the close command it sent, if any. A create still waiting
// is cancelled; a create the scheduler refused or cancelled counts as
// settled once its window has ended, since a refusal can still be
// followed by the plugin applying it; a create that landed is taken back
// before its window begins, and its project is closed once the snapshot
// shows it otherwise.
func Step(ctx context.Context, cmds Commands, snaps *Snapshots, a Ask, now time.Time) (bool, string, error) {
	if a.CreateCmd == "" || cmds == nil {
		return true, a.CloseCmd, nil
	}
	if now.After(a.End.Add(GiveUp)) {
		return true, a.CloseCmd, nil
	}
	rec, err := cmds.Status(ctx, a.CreateCmd)
	if errors.Is(err, schedcmd.ErrNotFound) {
		return true, a.CloseCmd, nil
	}
	if err != nil {
		return false, a.CloseCmd, err
	}
	switch {
	case rec.Status.Waiting():
		_, err := cmds.Undo(ctx, a.CreateCmd, a.Author)
		if errors.Is(err, schedcmd.ErrUnreachable) || errors.Is(err, schedcmd.ErrNotWaiting) {
			err = nil
		}
		return false, a.CloseCmd, err
	case refused(rec.Status):
		return !now.Before(a.End), a.CloseCmd, nil
	}
	if rec.UndoneBy != "" {
		u, err := cmds.Status(ctx, rec.UndoneBy)
		if err != nil {
			return false, a.CloseCmd, err
		}
		switch {
		case u.Status == schedcmd.StatusApplied || u.Status == schedcmd.StatusSaved:
			return true, a.CloseCmd, nil
		case u.Status.Waiting():
			return false, a.CloseCmd, nil
		}
	} else if a.Owe == Withdraw && a.Start.After(now) {
		_, err := cmds.Undo(ctx, a.CreateCmd, a.Author)
		if !errors.Is(err, schedcmd.ErrNotUndoable) {
			return false, a.CloseCmd, err
		}
	}
	return closeStep(ctx, cmds, snaps, a)
}

// closeStep closes the project once the snapshot shows it open.
func closeStep(ctx context.Context, cmds Commands, snaps *Snapshots, a Ask) (bool, string, error) {
	snap, err := snaps.get(ctx)
	if err != nil {
		return false, a.CloseCmd, err
	}
	var proj *planning.Project
	for i := range snap.Projects {
		if snap.Projects[i].GUID == a.ProjectGUID {
			proj = &snap.Projects[i]
		}
	}
	if proj != nil && proj.State == "Closed" {
		return true, a.CloseCmd, nil
	}
	id := a.CloseID
	if a.CloseCmd != "" {
		rec, err := cmds.Status(ctx, a.CloseCmd)
		if err != nil && !errors.Is(err, schedcmd.ErrNotFound) {
			return false, a.CloseCmd, err
		}
		switch {
		case err == nil && (rec.Status == schedcmd.StatusApplied || rec.Status == schedcmd.StatusSaved):
			return true, a.CloseCmd, nil
		case err == nil && !refused(rec.Status):
			return false, a.CloseCmd, nil
		}
		id = fmt.Sprintf("%s-%d", a.CloseID, time.Now().UnixNano())
	}
	if proj == nil {
		return false, a.CloseCmd, nil
	}
	raw, err := json.Marshal(schedcmd.EditPayload{Entity: "project", ID: int64(proj.ID), GUID: proj.GUID, Name: proj.Name,
		Changes: []schedcmd.FieldChange{{Field: "state", Before: json.RawMessage(StateNumber(proj.State)), After: json.RawMessage("3")}}})
	if err != nil {
		return false, a.CloseCmd, err
	}
	rec, err := cmds.SubmitOnce(ctx, id, schedcmd.KindProjectEdit, raw, a.Author)
	if err != nil {
		return false, a.CloseCmd, err
	}
	return false, rec.ID, nil
}

func refused(st schedcmd.Status) bool {
	return st == schedcmd.StatusRejected || st == schedcmd.StatusFailed || st == schedcmd.StatusConflict || st == schedcmd.StatusCancelled
}

// StateNumber is Target Scheduler's number for a project state name.
func StateNumber(name string) string {
	for k, v := range schedcmd.ProjectStates() {
		if v == name {
			return fmt.Sprint(k)
		}
	}
	return "1"
}
