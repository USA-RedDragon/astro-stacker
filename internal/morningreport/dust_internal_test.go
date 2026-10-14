package morningreport

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/dustcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

type mapPreviews map[string][]byte

func (m mapPreviews) Get(_ context.Context, key string) ([]byte, error) {
	if b, ok := m[key]; ok {
		return b, nil
	}
	return nil, errors.New("no such key")
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 40, 20))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const filterG = "Green"

func TestReportShowsTheNightsDustChanges(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	night := day(night1013)
	prev := app.DustFlat{SetKey: "a", Filter: filterL, TakenAt: clock("2025-05-14T03:00:00Z"), GroupKey: "a", MapKey: "a"}
	if err := db.Create(&prev).Error; err != nil {
		t.Fatal(err)
	}
	flats := clock("2026-10-14T11:30:00Z")
	found := clock("2026-10-14T14:45:00Z")
	crop := "dust/crops/x-0.png"
	for _, e := range []app.DustEvent{
		{FlatID: 2, PrevID: prev.ID, Filter: "Red", TakenAt: flats, Class: dustcheck.ClassNew, Scope: dustcheck.ScopeWindow,
			Filters: "Green,Red", GroupKey: "g1", X: 2622, Y: 2012, Diameter: 720, Depth: -1.53, CropKey: &crop, DetectedAt: found},
		{FlatID: 3, PrevID: prev.ID, Filter: filterG, TakenAt: flats, Class: dustcheck.ClassNew, Scope: dustcheck.ScopeWindow,
			Filters: "Green,Red", GroupKey: "g1", X: 2624, Y: 2010, Diameter: 725, Depth: -1.69, DetectedAt: found},
		{FlatID: 3, PrevID: prev.ID, Filter: filterG, TakenAt: flats, Class: dustcheck.ClassHalo, X: 2900, Y: 2500, DetectedAt: found},
		{FlatID: 4, PrevID: prev.ID, Filter: "Red", TakenAt: clock("2026-09-29T04:00:00Z"), Class: dustcheck.ClassNew,
			Scope: dustcheck.ScopeWindow, GroupKey: "old", X: 10, Y: 10, Diameter: 500, Depth: -1, DetectedAt: found},
	} {
		if err := db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	preview := "dust/nights/2026-10-13/residual.png"
	if err := db.Create(&app.DustLightCheck{Night: night, Subs: 60, Fields: 2, Status: dustcheck.LightsChecked, PreviewKey: &preview,
		Discs: `[{"X":2648,"Y":1848,"Diameter":600,"Peak":-0.52}]`, Deepest: 0.52, CheckedAt: found}).Error; err != nil {
		t.Fatal(err)
	}
	now := clock("2026-10-14T11:00:00-05:00")
	svc := testService(t, db, &fakeSender{}, &now)
	svc.Collector.Previews = mapPreviews{crop: testPNG(t), preview: testPNG(t)}
	text, photos, r, err := svc.Render(context.Background(), night)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Dust) != 1 {
		t.Fatalf("dust changes %+v, want the one window change of the night", r.Dust)
	}
	for _, want := range []string{
		"<b>Dust</b>",
		"new shadow on the camera window at x 2622, y 2012 · 1.53% deep · 720 px · Green, Red since May 13",
		"lights: a shadow the flats don't have at x 2648, y 1848 · 0.52% · 600 px",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if len(photos) != 2 || !strings.Contains(photos[0].Caption, "before | after | difference") {
		t.Fatalf("photos %+v, want the change's crop and the lights residual", photos)
	}
	idle := Formatter{Loc: time.UTC}.idle(Report{Night: night, Dust: r.Dust})
	if !strings.Contains(idle, "new shadow on the camera window") {
		t.Errorf("a night with nothing imaged leaves out its dust:\n%s", idle)
	}
}
