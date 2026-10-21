package morningreport

import (
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
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
