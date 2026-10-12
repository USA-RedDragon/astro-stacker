package skyevents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type keptCommands struct{ sent []schedcmd.Envelope }

func (k *keptCommands) Name() string { return "kept" }

func (k *keptCommands) Send(_ context.Context, e schedcmd.Envelope) (schedcmd.Result, error) {
	k.sent = append(k.sent, e)
	return schedcmd.Result{ID: e.ID, Status: schedcmd.StatusQueued}, nil
}

func (k *keptCommands) Cancel(_ context.Context, id string) (schedcmd.Result, error) {
	return schedcmd.Result{ID: id, Status: schedcmd.StatusCancelled}, nil
}

func TestLoweringCatchesAnAutoPlanSentMeanwhile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.SkyEventChoice{}, &schedcmd.Record{}); err != nil {
		t.Fatal(err)
	}
	kept := &keptCommands{}
	cmds := &schedcmd.Service{Log: schedcmd.NewGormLog(db), AppDB: db, Transports: []schedcmd.Transport{kept}}
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	s := &Service{App: db, Commands: cmds, Now: func() time.Time { return now }}
	e := Event{ID: "jupiter-saturn-2030-02-01", Start: now.AddDate(0, 1, 0), End: now.AddDate(0, 1, 10),
		Members: []Member{{Name: "Jupiter", Kind: KindPlanet, ID: "body:jupiter"}, {Name: "Saturn", Kind: KindPlanet, ID: "body:saturn"}}}
	if err := cmds.Log.Create(ctx, &schedcmd.Record{ID: "auto-1", Kind: schedcmd.KindProjectCreate, Status: schedcmd.StatusQueued, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	was := newChoice(e)
	sent := newChoice(e)
	sent.Priority, sent.CommandID, sent.AutoCommandID, sent.PlannedAt, sent.AutoPlannedAt, sent.TargetGUID = PriorityHigh, "auto-1", ptr("auto-1"), &now, &now, "tg"
	if err := db.Create(&sent).Error; err != nil {
		t.Fatal(err)
	}
	rec, err := s.lowerLateAutoPlan(ctx, e, was, "web")
	if err != nil || rec == nil || rec.Kind != schedcmd.KindTargetWindow {
		t.Fatalf("%+v %v", rec, err)
	}
	var w schedcmd.TargetWindowPayload
	if err := json.Unmarshal(kept.sent[0].Payload, &w); err != nil {
		t.Fatal(err)
	}
	if w.TargetGUID != "tg" || w.Window.Priority != schedcmd.EventPriorityNormal || w.Before.Priority != schedcmd.EventPriorityHigh {
		t.Errorf("window %+v", w)
	}
	var row app.SkyEventChoice
	if err := db.First(&row, "event_id = ?", e.ID).Error; err != nil || row.Priority != PriorityNormal || row.WindowCommand != rec.ID {
		t.Errorf("row %+v %v", row, err)
	}
	if again, err := s.lowerLateAutoPlan(ctx, e, was, "web"); !errors.Is(err, errNothingToSend) || again != nil {
		t.Errorf("lowered twice: %+v %v", again, err)
	}
}
