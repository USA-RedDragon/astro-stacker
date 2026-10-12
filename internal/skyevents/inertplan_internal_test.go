package skyevents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const inertDraftGolden = "testdata/inert_draft.golden.json"

func planSched(t *testing.T, extra ...string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	stmts := append([]string{
		`create table project ("Id" integer primary key, "profileId" text, name text, description text, state integer, priority integer, minimumtime integer, minimumaltitude real, "maximumAltitude" real, "isMosaic" integer, enablegrader integer, guid text)`,
		`create table target ("Id" integer primary key, name text, active integer, ra real, "dec" real, rotation real, projectid integer, guid text)`,
		`create table exposuretemplate ("Id" integer primary key, "profileId" text, name text, filtername text, gain integer, "offset" integer, bin integer, twilightlevel integer, moonavoidanceenabled integer, moonavoidanceseparation real, moonavoidancewidth integer, maximumhumidity real, defaultexposure real, moonrelaxscale real, moondownenabled integer, ditherevery integer, guid text)`,
		`create table exposureplan ("Id" integer primary key, "profileId" text, exposure real, desired integer, acquired integer, accepted integer, targetid integer, "exposureTemplateId" integer, enabled integer, guid text)`,
		`create table ruleweight ("Id" integer primary key, name text, weight real, projectid integer)`,
		`insert into project values (5,'p','Cygnus',null,1,1,60,20,0,0,1,'pg5')`,
		`insert into target values (12,'Cygnus',1,20.8,30.7,0,5,'g12')`,
		`insert into exposuretemplate values
			(1,'p','H-a','H-a',100,10,1,2,1,30,7,0,600,0,0,1,'t1'),
			(2,'p','Red','Red',0,10,1,0,1,30,7,0,300,0,0,1,'t2'),
			(3,'p','L Low','Luminance',0,10,1,2,1,30,7,0,120,0,0,1,'t3'),
			(4,'p','Luminance','Luminance',0,10,1,0,1,30,7,0,300,0,0,1,'t4'),
			(5,'p','R 600','Red',0,10,1,0,1,60,7,0,600,0,0,1,'t5')`,
		`insert into exposureplan values (40,'p',-1,100,0,0,12,1,1,'e40'),(41,'p',-1,100,0,0,12,2,1,'e41'),(42,'p',-1,100,0,0,12,4,1,'e42'),(43,'p',-1,100,0,0,12,4,1,'e43'),(44,'p',-1,100,0,0,12,5,1,'e44'),(45,'p',-1,100,0,0,12,5,1,'e45'),(46,'p',-1,100,0,0,12,5,1,'e46')`,
	}, extra...)
	for _, q := range stmts {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return db
}

func planSnap(t *testing.T, db *gorm.DB) *planning.Snapshot {
	t.Helper()
	snap, err := planning.Load(context.Background(), db, nil, planning.Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func ids() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("id-%d", n) }
}

func draftEvent() Event {
	at := time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC)
	return Event{ID: "ev-1", Kind: EventBodyDSO, Title: "Saturn near NGC 7000", FirstNight: "2026-11-02", Start: at.Add(-6 * time.Hour), End: at.Add(6 * time.Hour),
		Best: Best{At: at, Separation: 0.4}, Hours: 3, Frame: Frame{CenterRA: 130, CenterDec: 19.6},
		Members: []Member{{Name: "Saturn", Kind: KindPlanet}, {Name: "NGC 7000", Kind: KindDSO}}}
}

// Before the template migration the Plan it draft is what it was before
// filters: the luminance template the most plans use, no filter sent.
func TestEventDraftInertBeforeMigration(t *testing.T) {
	t.Parallel()
	for name, extra := range map[string][]string{
		"no table":    nil,
		"empty table": {`create table ts_template_role (template_id integer not null primary key, template_guid text, profile_id text not null, filter text not null, role text not null, changed_at text not null)`},
	} {
		snap := planSnap(t, planSched(t, extra...))
		tm, ok := pickTemplate(snap)
		if !ok || tm.ID != 4 {
			t.Fatalf("%s: picked %+v", name, tm)
		}
		p, err := (&Service{}).Draft(context.Background(), snap, draftEvent(), PriorityNormal, ids())
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if os.Getenv("ASTRO_GOLDEN_WRITE") != "" {
			if err := os.WriteFile(inertDraftGolden, got, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(inertDraftGolden)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: draft differs from before template roles:\n%s", name, got)
		}
	}
}
