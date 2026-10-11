package skybright_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"gorm.io/gorm"
)

func TestSourceKeepsTheLastValueWhenATableCheckFails(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	at := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	n := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for k := range 6 {
		add(t, db, k+1, "L", n, n.Add(30*time.Hour), 21)
	}
	src := &skybright.Source{DB: db, TTL: time.Minute, Now: func() time.Time { return at }}
	if fresh := src.Get(context.Background()); fresh.Mag == nil {
		t.Fatalf("fresh %+v", fresh)
	}
	fail := func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "sqlite_master") {
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT 1 FROM injected_failure")
			tx.Statement.Vars = nil
		}
	}
	if err := db.Callback().Row().Before("gorm:row").Register("test:fail", fail); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Hour)
	if v := src.Get(context.Background()); v.Mag == nil || *v.Mag != 21 || !v.Basis.Stale || v.Basis.Error == nil {
		t.Fatalf("a failed table check reads as no samples: %+v %+v", v, v.Basis)
	}
}
