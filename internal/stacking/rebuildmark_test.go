package stacking

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func rebuildMarkDB(t *testing.T) (*Pipeline, app.Stack) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Stack{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	s := app.Stack{Object: "IC 4604 Panel 1", Filter: filterGreen, Subs: 23}
	if err := db.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	return &Pipeline{db: db}, s
}

func needsRebuild(t *testing.T, p *Pipeline, id int) bool {
	t.Helper()
	var s app.Stack
	if err := p.db.First(&s, id).Error; err != nil {
		t.Fatal(err)
	}
	return s.NeedsRebuild
}

// A batch publishing a master it loaded before a sweep took subs out of it
// keeps the sweep's mark, so those subs leave the master at the next rebuild.
func TestPublishKeepsAConcurrentRebuildMark(t *testing.T) {
	t.Parallel()
	p, loaded := rebuildMarkDB(t)
	if err := p.markRebuild(context.Background(), loaded.ID, true); err != nil {
		t.Fatal(err)
	}
	loaded.Subs = 47
	if err := p.saveStack(context.Background(), &loaded); err != nil {
		t.Fatal(err)
	}
	if !needsRebuild(t, p, loaded.ID) {
		t.Fatal("publishing a master loaded before the sweep cleared its mark")
	}
}

// A rebuild that fails leaves the master marked.
func TestFailedRebuildKeepsTheMark(t *testing.T) {
	t.Parallel()
	p, s := rebuildMarkDB(t)
	if err := p.markRebuild(context.Background(), s.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.rebuild(context.Background(), &s); err == nil {
		t.Fatal("rebuilt a master with no subs")
	}
	if !needsRebuild(t, p, s.ID) {
		t.Fatal("a failed rebuild cleared the mark")
	}
}

// Masters left holding subs taken out of them are marked to be restacked.
func TestMarkMiscounted(t *testing.T) {
	t.Parallel()
	p, stale := rebuildMarkDB(t)
	state := "stacks/IC 4604 Panel 1/Green/state.fit"
	key := "registered/a.fit"
	good := app.Stack{Object: stale.Object, Filter: filterRed, Subs: 1, StateKey: &state}
	if err := p.db.Create(&good).Error; err != nil {
		t.Fatal(err)
	}
	if err := p.db.Model(&stale).Updates(map[string]any{"subs": 2, "state_key": state}).Error; err != nil {
		t.Fatal(err)
	}
	for i, sf := range []app.StackFrame{
		{FrameID: 1, StackID: &stale.ID, Status: app.StackStatusAdded, RegisteredKey: &key},
		{FrameID: 2, StackID: &stale.ID, Status: app.StackStatusMoon, RegisteredKey: &key},
		{FrameID: 3, StackID: &good.ID, Status: app.StackStatusAdded, RegisteredKey: &key},
	} {
		if err := p.db.Create(&sf).Error; err != nil {
			t.Fatal(i, err)
		}
	}
	p.markMiscounted(context.Background())
	if !needsRebuild(t, p, stale.ID) {
		t.Error("a master holding a moonlit sub was not marked")
	}
	if needsRebuild(t, p, good.ID) {
		t.Error("a master holding exactly its added subs was marked")
	}
}
