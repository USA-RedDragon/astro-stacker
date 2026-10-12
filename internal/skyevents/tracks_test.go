package skyevents_test

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/skyevents"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestHorizonsCommandForAsteroids(t *testing.T) {
	t.Parallel()
	for in, want := range map[[2]string]string{
		{"C/2025 R2", bodies.KindComet}:     "DES=C/2025 R2;CAP;NOFRAG",
		{"2000 SG344", bodies.KindAsteroid}: "DES=2000 SG344;",
		{"DES=433;", bodies.KindAsteroid}:   "DES=433;",
		{"433", bodies.KindAsteroid}:        "433;",
	} {
		if got := skyevents.HorizonsCommand(in[0], in[1]); got != want {
			t.Errorf("%v: %q, want %q", in, got, want)
		}
	}
}

func TestDesignatedBodiesComeFromHorizons(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../horizons/testdata/comet-c2025r2-10m.json")
	if err != nil {
		t.Fatal(err)
	}
	var cmds []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmds = append(cmds, r.URL.Query().Get("COMMAND")+" "+r.URL.Query().Get("STEP_SIZE"))
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.HorizonsCache{}); err != nil {
		t.Fatal(err)
	}
	stale := app.HorizonsCache{Key: "old", Command: "DES=C/2025 R2;CAP;NOFRAG", Start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Step: "60 m"}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	hz := &horizons.Client{BaseURL: srv.URL, Cache: db}
	site := sky.Site{Latitude: 32, Longitude: -100}
	from := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	tab, err := skyevents.HorizonsTable(context.Background(), hz, site, "C/2025 R2", bodies.KindComet, from, from.Add(12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 || cmds[0] != "'DES=C/2025 R2;CAP;NOFRAG' '60 m'" {
		t.Fatalf("requests %v", cmds)
	}
	var left int64
	db.Model(&app.HorizonsCache{}).Where("key = ?", "old").Count(&left)
	if left != 0 {
		t.Error("the superseded event table stayed in the cache")
	}
	e, err := ephem.Load()
	if err != nil {
		t.Fatal(err)
	}
	g, err := skyevents.NewGrid(e, site, from, from.Add(12*time.Hour), skyevents.NewCriteria(3.32, 2.22))
	if err != nil {
		t.Fatal(err)
	}
	tr := skyevents.TableTrack("C/2025 R2", "C/2025 R2", bodies.KindComet, tab, g)
	dark := 0
	for i, s := range tr.Samples {
		if !g.Dark[i] {
			continue
		}
		dark++
		if !s.OK || math.Abs(s.RA-76.17) > 0.1 || math.Abs(s.Dec-27.86) > 0.1 || math.IsNaN(s.Mag) {
			t.Fatalf("step %s: %+v", g.Times[i], s)
		}
		if want := g.Observers[i].Altitude(s.RA, s.Dec); s.Alt != want {
			t.Fatalf("altitude %v, want %v", s.Alt, want)
		}
	}
	if dark == 0 || tr.Kind != skyevents.KindComet || tr.Source != skyevents.SourceHorizons {
		t.Fatalf("%d dark samples, %+v", dark, tr.Kind)
	}
}
