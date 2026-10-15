package mosaicplan_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicstore"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

func postgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Postgres(t, "stacker", "stacker")
	return db
}

func TestRunAdoptionDoesNotOverwriteADecisionMadeDuringThePass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appDB := appFixtureOn(t, postgresDB(t))
	svc := mosaicplan.New(appDB, schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	_, by := runAdoption(ctx, t, svc)
	mk := by[markarian]
	if err := appDB.Model(&app.MosaicAdoption{}).Where("id = ?", mk.ID).
		Updates(map[string]any{"status": app.AdoptionProposed, "decided_by": "", "decided_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.Where("project_guid = ?", "pm").Delete(&app.MosaicPanel{}).Error; err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	var decideErr error
	decided := make(chan struct{})
	if err := appDB.Callback().Query().After("gorm:query").Register("test:decide", func(db *gorm.DB) {
		if db.Statement.Table != "mosaic_adoptions" {
			return
		}
		once.Do(func() {
			go func() {
				defer close(decided)
				_, decideErr = mosaicstore.SetStatus(ctx, appDB, mk.ID, app.AdoptionProposed, app.AdoptionRejected, byPerson, svc.Rig)
			}()
			select {
			case <-decided:
			case <-time.After(time.Second):
			}
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunAdoption(ctx, false); err != nil {
		t.Fatal(err)
	}
	<-decided
	var row app.MosaicAdoption
	if err := appDB.First(&row, mk.ID).Error; err != nil {
		t.Fatal(err)
	}
	var panels int64
	appDB.Model(&app.MosaicPanel{}).Where("project_guid = ?", "pm").Count(&panels)
	switch {
	case decideErr == nil && (row.Status != app.AdoptionRejected || row.DecidedBy != byPerson || panels != 0):
		t.Errorf("a person's rejection was overwritten by the pass: %s by %s, %d panels", row.Status, row.DecidedBy, panels)
	case decideErr != nil && !errors.Is(decideErr, mosaicstore.ErrConflict):
		t.Errorf("decision failed: %v", decideErr)
	case decideErr != nil && (row.Status != app.AdoptionAuto || panels != 2):
		t.Errorf("pass result %s by %s, %d panels", row.Status, row.DecidedBy, panels)
	}
}
