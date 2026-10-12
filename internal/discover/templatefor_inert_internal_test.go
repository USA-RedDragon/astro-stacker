package discover

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const templateForGolden = "testdata/templatefor_inert.golden.txt"

func pickSched(t *testing.T, extra ...string) *gorm.DB {
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
		`insert into target values (12,'Cygnus',1,20.8,30.7,0,5,'g12'),(13,'Veil',1,20.9,31.2,0,5,'g13')`,
		`insert into exposuretemplate values
			(1,'p','H-a','H-a',100,10,1,2,1,30,7,0,600,0,0,1,'t1'),
			(2,'p','HA Low','H-a',0,10,1,2,1,30,7,0,120,0,0,1,'t2'),
			(3,'p','Red','Red',0,10,1,0,1,30,7,0,300,0,0,1,'t3'),
			(4,'p','R 600','Red',0,10,1,0,1,60,7,0,600,0,0,1,'t4'),
			(5,'p','Luminance','Luminance',0,10,1,0,1,30,7,0,300,0,0,1,'t5'),
			(6,'p','L Low','L',0,10,1,2,1,30,7,0,120,0,0,1,'t6'),
			(7,'p','O-III','O-III',100,10,1,2,1,60,7,0,600,0,0,1,'t7'),
			(8,'p','Green','Green',0,10,1,0,1,30,7,0,300,0,0,1,'t8')`,
		`insert into exposureplan values (40,'p',-1,100,0,0,12,1,1,'e40'),(41,'p',-1,100,0,0,12,2,1,'e41'),(42,'p',-1,100,0,0,13,2,1,'e42'),(43,'p',-1,100,0,0,12,3,1,'e43'),(44,'p',-1,100,0,0,12,4,1,'e44'),(45,'p',-1,100,0,0,12,5,1,'e45'),(46,'p',-1,100,0,0,13,7,1,'e46')`,
	}, extra...)
	for _, q := range stmts {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return db
}

func pickSnap(t *testing.T, db *gorm.DB) *planning.Snapshot {
	t.Helper()
	snap, err := planning.Load(context.Background(), db, nil, planning.Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func picks(snap *planning.Snapshot) string {
	var b strings.Builder
	for _, c := range []string{"L", "R", "G", "B", "H", "O", "S"} {
		if tm, _ := templateFor(snap, c); tm != nil {
			fmt.Fprintf(&b, "%s: %d %s %s\n", c, tm.ID, tm.Name, tm.Filter)
		} else {
			fmt.Fprintf(&b, "%s: none\n", c)
		}
	}
	return b.String()
}

// Before the template migration the picked templates are what they were
// before template roles.
func TestTemplateForInertBeforeMigration(t *testing.T) {
	t.Parallel()
	var got strings.Builder
	for _, extra := range [][]string{nil, {`create table ts_template_role (template_id integer not null primary key, template_guid text, profile_id text not null, filter text not null, role text not null, changed_at text not null)`}} {
		got.WriteString(picks(pickSnap(t, pickSched(t, extra...))))
	}
	got.WriteString(picks(&planning.Snapshot{Templates: []planning.Template{
		{ID: 1, Name: "Ha", Filter: "H-alpha", UsedByPlans: 3}, {ID: 7, Name: "Ha short", Filter: "Ha", UsedByPlans: 3},
		{ID: 2, Name: "OIII", Filter: "OIII", UsedByPlans: 2}, {ID: 3, Name: "Lum 300", Filter: "L", UsedByPlans: 4},
		{ID: 4, Name: "Red 300", Filter: "R"}, {ID: 9, Name: "Ared", Filter: "red"},
	}}))
	if os.Getenv("ASTRO_GOLDEN_WRITE") != "" {
		if err := os.WriteFile(templateForGolden, []byte(got.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(templateForGolden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(got.String()), want) {
		t.Errorf("picks differ from before template roles:\n%s", got.String())
	}
}
