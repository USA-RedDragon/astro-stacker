package schedwatch_test

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedwatch"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

func open(t *testing.T) *gorm.DB {
	t.Helper()
	return testdb.SQLite(t, "")
}

func exec(t *testing.T, db *gorm.DB, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestWatcherCallsOnPlanAndSyncChanges(t *testing.T) {
	t.Parallel()
	db := open(t)
	exec(t, db,
		`create table project ("Id" integer primary key, name text, minimumtime integer)`,
		`create table exposureplan ("Id" integer primary key, desired integer, acquired integer, accepted integer)`,
		`insert into project values (7, 'Garlic Nebula', 60)`,
		`insert into exposureplan values (50, 100, 64, 64)`,
	)
	plans, syncs := 0, 0
	w := &schedwatch.Watcher{DB: db, OnPlans: []func(){func() { plans++ }}, OnSync: []func(){func() { syncs++ }}}
	ctx := context.Background()
	w.Check(ctx)
	w.Check(ctx)
	if plans != 0 || syncs != 0 {
		t.Fatalf("the first checks only note where things stand: plans %d, syncs %d", plans, syncs)
	}
	exec(t, db, `update exposureplan set acquired = 65, accepted = 65`)
	w.Check(ctx)
	if plans != 0 {
		t.Fatal("acquired and accepted counts change with every sub and are left out")
	}
	exec(t, db, `update project set minimumtime = 25`)
	w.Check(ctx)
	w.Check(ctx)
	if plans != 1 {
		t.Fatalf("a project edit is one change: %d", plans)
	}
	exec(t, db, `create table target ("Id" integer primary key, name text, projectid integer)`, `insert into target values (20, 'Garlic Nebula', 7)`)
	w.Check(ctx)
	if plans != 2 {
		t.Fatalf("a new target is a change: %d", plans)
	}
	exec(t, db,
		`create table sym_incoming_batch (batch_id integer, channel_id text, status text)`,
		`create table sym_outgoing_batch (batch_id integer, channel_id text, status text)`,
		`insert into sym_incoming_batch values (5, 'heartbeat', 'OK')`,
	)
	w.Check(ctx)
	if syncs != 0 {
		t.Fatalf("heartbeats are not syncs: %d", syncs)
	}
	exec(t, db, `insert into sym_incoming_batch values (6, 'default', 'OK')`)
	w.Check(ctx)
	w.Check(ctx)
	if syncs != 1 || plans != 2 {
		t.Fatalf("one new batch is one sync: syncs %d, plans %d", syncs, plans)
	}
	exec(t, db, `alter table sym_incoming_batch rename to sym_incoming_batch_gone`)
	w.Check(ctx)
	exec(t, db, `alter table sym_incoming_batch_gone rename to sym_incoming_batch`)
	w.Check(ctx)
	if syncs != 1 {
		t.Fatalf("a failed read of the batches is not a sync: %d", syncs)
	}
}
