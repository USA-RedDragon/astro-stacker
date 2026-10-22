package morningreport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
)

func TestRunsNoneImaged(t *testing.T) {
	t.Parallel()
	f := Formatter{Loc: time.UTC, Zone: "UTC"}
	if got := f.runs(Report{}); got != "<b>Dip and eclipse runs</b>\n• Dip windows: none were imaged.\n• Eclipse requests: none were imaged." {
		t.Errorf("got %q", got)
	}
}

func TestRunsCouldNotJudge(t *testing.T) {
	t.Parallel()
	f := Formatter{Loc: time.UTC, Zone: "UTC"}
	first := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)
	n := app.RunNight{Kind: app.RunNightDip, SourceID: 7, Object: "field", State: app.DipUnsure, Note: "3 subs gave a measurement, 6 are needed",
		Subs: 4, Measured: 3, First: first, Last: first.Add(10 * time.Minute), Smallest: 0.2, Night: "2026-10-20"}
	want := "• <b>7</b> dip window (field): <b>couldn't judge</b>: 3 subs gave a measurement, 6 are needed. 4 subs (3 measured), 02:00–02:10 UTC (10m)."
	if got := f.runs(Report{Runs: []RunChart{{Night: n}}}); !strings.Contains(got, want) {
		t.Errorf("got %q", got)
	}
	if got := f.RunCaption(n); got != "<b>7</b> · dip window, night of Oct 20 · <b>couldn't judge</b> · 3 subs, 02:00–02:10 UTC" {
		t.Errorf("caption %q", got)
	}
}

func TestAboutSpan(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{62 * time.Minute: "about an hour", 40 * time.Minute: "about 40 min", 90 * time.Minute: "about 1 h 30 min", 2 * time.Hour: "about 2 hours"} {
		if got := aboutSpan(d); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
}

const (
	secondary    = "secondary"
	garlicLive   = "06096b69"
	andromedaNow = "6cff9784"
	night1021    = "2026-10-21"
	utcZone      = "UTC"
)

func TestRunsStoredTwiceReportedOnce(t *testing.T) {
	t.Parallel()
	db := testdb.SQLite(t, "runs")
	if err := db.AutoMigrate(&app.RunNight{}, &app.EclipseRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropIndex(&app.RunNight{}, "idx_run_night_run"); err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 22, h, m, 0, 0, time.UTC) }
	for _, r := range []app.EclipseRequest{
		{SourceID: 27822, Kind: secondary, ProjectGUID: "be58011c", State: app.EclipseCancel},
		{SourceID: 27822, Kind: secondary, ProjectGUID: garlicLive, State: app.EclipseSent},
		{SourceID: 236, Kind: secondary, ProjectGUID: "3f99e20a", State: app.EclipseCancel},
		{SourceID: 236, Kind: secondary, ProjectGUID: andromedaNow, State: app.EclipseSent},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	pts := []app.RunPoint{{FrameID: 1, Mag: 0}}
	for _, r := range []app.RunNight{
		{ProjectGUID: garlicLive, Night: night1021, Kind: app.RunNightEclipse, SourceID: 27822, Object: "Garlic Nebula", State: eclipseOnTime,
			First: at(1, 34), Last: at(8, 18), Subs: 169, Measured: 169, Points: pts, JudgedAt: at(9, 22)},
		{ProjectGUID: "be58011c", Night: night1021, Kind: app.RunNightEclipse, SourceID: 27822, Object: "Garlic Nebula", State: eclipseOnTime,
			First: at(1, 34), Last: at(8, 18), Subs: 169, Measured: 169, Points: pts, JudgedAt: at(9, 21)},
		{ProjectGUID: "3f99e20a", Night: night1021, Kind: app.RunNightEclipse, SourceID: 236, Object: "Andromeda", State: eclipseOff,
			First: at(8, 21), Last: at(11, 37), Subs: 87, Measured: 87, Points: pts, JudgedAt: at(15, 36)},
		{ProjectGUID: andromedaNow, Night: night1021, Kind: app.RunNightEclipse, SourceID: 236, Object: "Andromeda", State: eclipseOff,
			First: at(8, 21), Last: at(11, 37), Subs: 87, Measured: 87, Points: pts, JudgedAt: at(15, 35)},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	var charts []string
	c := Collector{App: db, RunChart: func(n app.RunNight) ([]byte, error) {
		charts = append(charts, n.ProjectGUID)
		return []byte{1}, nil
	}}
	r := Report{Night: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)}
	c.runs(context.Background(), &r)
	if len(charts) != 2 || charts[0] != garlicLive || charts[1] != andromedaNow {
		t.Fatalf("charts of %v, want one each under the live requests", charts)
	}
	text := Formatter{Loc: time.UTC, Zone: utcZone}.runs(r)
	if strings.Count(text, "<b>27822</b>") != 1 || strings.Count(text, "<b>236</b>") != 1 {
		t.Errorf("each run once:\n%s", text)
	}
}
