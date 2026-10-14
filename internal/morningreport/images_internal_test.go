package morningreport

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestImagesPickTheCurrentStackAndSayWhenItIsBehind(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	key := func(s string) *string { return &s }
	stacks := []app.Stack{
		{Object: objM15, Filter: filterL, ExposureSeconds: 1200, PreviewKey: key("stacks/M 15/Luminance/preview.jpg"), UpdatedAt: at("03:00:00")},
		{Object: objM15, Filter: filterR, ExposureSeconds: 3600, PreviewKey: key("stacks/M 15/Red/preview.jpg"), UpdatedAt: at("03:00:00")},
		{Object: objM15, Filter: "Green", ExposureSeconds: 3000, UpdatedAt: at("03:00:00")},
		{Object: objLagoon, Filter: filterL, ExposureSeconds: 2700, PreviewKey: key("stacks/Lagoon/Luminance/preview.jpg"), UpdatedAt: at("03:00:00")},
		{Object: objLagoon, Filter: filterR, ExposureSeconds: 2700, PreviewKey: key("stacks/Lagoon/Red/preview.jpg"), UpdatedAt: at("03:00:00")},
		{Object: "Comet", Filter: filterR, ExposureSeconds: 600, PreviewKey: key("stacks/Comet/Red/preview.jpg"), UpdatedAt: at("01:00:00")},
	}
	for i := range stacks {
		if err := db.Create(&stacks[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&app.Cover{Subject: objM15, Palette: "LRGB", PreviewKey: "stacks/M 15/color.jpg", UpdatedAt: at("02:30:00")}).Error; err != nil {
		t.Fatal(err)
	}
	s := &Subs{Targets: []TargetSubs{
		{Object: objM15, Filters: []FilterSubs{{Filter: filterL, Kept: 4, LastProcessed: at("02:50:00")}}},
		{Object: objLagoon, Filters: []FilterSubs{{Filter: filterR, Kept: 2, Pending: 1, LastProcessed: at("02:00:00")}}},
		{Object: "Comet", Filters: []FilterSubs{{Filter: filterR, Kept: 5, ByNight: 5, LastProcessed: at("02:00:00")}}},
		{Object: "Rejected only", Filters: []FilterSubs{{Filter: filterR, Rejected: 3}}},
		{Object: "No stack", Filters: []FilterSubs{{Filter: filterR, Kept: 1}}},
	}}
	c := Collector{App: db, Schedule: chicago(t)}
	got := c.images(context.Background(), s, day(night1013))
	if len(got) != 3 {
		t.Fatalf("got %d images, want M 15, Lagoon and Comet: %+v", len(got), got)
	}
	m15, lagoon, comet := got[0], got[1], got[2]
	if m15.Key != "stacks/M 15/color.jpg" || m15.Label != "LRGB color" || m15.Current || m15.NewSubs != 4 {
		t.Fatalf("M 15 %+v: the cover is older than a kept sub", m15)
	}
	if m15.Exposure != (1200+3600+3000)*time.Second {
		t.Fatalf("M 15 exposure %s, want the LRGB filters' sum", m15.Exposure)
	}
	if lagoon.Label != filterR || lagoon.Current || lagoon.NewSubs != 3 || lagoon.Exposure != 2700*time.Second {
		t.Fatalf("Lagoon %+v: the filter with new subs wins the tie, and a waiting sub makes it behind", lagoon)
	}
	if !comet.ByNight || !comet.Current || comet.Label != filterR {
		t.Fatalf("comet %+v", comet)
	}
}

const objNGC7000 = "NGC 7000"

func TestImagesFallBackToAMasterWhenTheCoverHasNoNewSubs(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	key := "stacks/NGC 7000/Luminance/preview.jpg"
	if err := db.Create(&app.Stack{Object: objNGC7000, Filter: filterL, ExposureSeconds: 900, PreviewKey: &key, UpdatedAt: at("04:00:00")}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.Cover{Subject: objNGC7000, Palette: "SHO", PreviewKey: "stacks/NGC 7000/color.jpg", UpdatedAt: at("04:00:00")}).Error; err != nil {
		t.Fatal(err)
	}
	s := &Subs{Targets: []TargetSubs{{Object: objNGC7000, Filters: []FilterSubs{{Filter: filterL, Kept: 3, LastProcessed: at("03:00:00")}}}}}
	got := Collector{App: db, Schedule: chicago(t)}.images(context.Background(), s, day(night1013))
	if len(got) != 1 || got[0].Key != key || got[0].NewSubs != 3 || !got[0].Current {
		t.Fatalf("got %+v, want the Luminance master that holds the night's subs", got)
	}
}

func TestImagesTellACometCoverByItsNights(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	const comet = "161P/Hartley-IRAS"
	str := func(s string) *string { return &s }
	method := 3
	stacks := map[string]*app.Stack{
		filterR: {Object: comet, Filter: filterR, ExposureSeconds: 60, LinearKey: str("r.bin"), CometLinearKey: str("rc.bin"), CometMethod: &method, CometSubs: 3},
		filterL: {Object: comet, Filter: filterL, ExposureSeconds: 0, LinearKey: str("l.bin"), CometLinearKey: str("lc.bin"), CometMethod: &method, CometSubs: 2},
	}
	for _, s := range stacks {
		if err := db.Create(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&app.Cover{Subject: comet, Palette: "LRGB", PreviewKey: "stacks/161P/color.jpg", UpdatedAt: at("04:00:00")}).Error; err != nil {
		t.Fatal(err)
	}
	n12, n13 := day("2026-10-12"), day(night1013)
	for _, b := range []app.BodyNight{
		{Object: comet, Filter: filterR, Night: n12, Subs: 1, StarlessKey: str("a")},
		{Object: comet, Filter: filterR, Night: n13, Subs: 2, StarlessKey: str("b")},
		{Object: comet, Filter: filterL, Night: n13, Subs: 2, StarlessKey: str("c")},
	} {
		if err := db.Create(&b).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&app.NightReference{Object: comet, Night: n13, FrameID: 1, ObjectKey: "ref"}).Error; err != nil {
		t.Fatal(err)
	}
	subs := []struct {
		filter, status string
		night          time.Time
	}{
		{filterR, app.StackStatusAdded, n12}, {filterR, app.StackStatusNight, n13}, {filterR, app.StackStatusNight, n13},
		{filterL, app.StackStatusNight, n13}, {filterL, app.StackStatusNight, n13},
		{filterL, app.StackStatusAdded, n13},
	}
	for i, s := range subs {
		when := s.night.Add(30 * time.Hour)
		night := s.night
		f := app.Frame{Key: fmt.Sprintf("c%d", i), ETag: "e", Type: lightType, Object: comet, Filter: s.filter, DateObs: &when, Night: &night}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		id := stacks[s.filter].ID
		if err := db.Create(&app.StackFrame{FrameID: f.ID, StackID: &id, Status: s.status, Weight: 1, Exposure: 60, RegisteredKey: str("reg")}).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := &Subs{Targets: []TargetSubs{{Object: comet, Filters: []FilterSubs{
		{Filter: filterR, Kept: 2, ByNight: 2, LastProcessed: at("03:00:00")},
		{Filter: filterL, Kept: 2, ByNight: 2, LastProcessed: at("03:00:00")},
	}}}}
	got := Collector{App: db, Schedule: chicago(t)}.images(context.Background(), s, n13)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	im := got[0]
	if im.ByNight || !im.Current || im.CometSubs != 5 || len(im.CometNights) != 2 || im.ExposureMissing || im.Exposure != 5*time.Minute {
		t.Fatalf("comet %+v, want 5 subs, 5 min from Oct 12 and 13, last night included", im)
	}
	f := Formatter{Loc: chicago(t).Loc, Zone: "CT"}
	want := "<b>161P/Hartley-IRAS</b> · LRGB color · the comet stacked on itself from 2 nights (Oct 12, Oct 13), last night included · 5m total · +4 subs last night"
	if c := f.Caption(im); c != want {
		t.Fatalf("caption %q\nwant    %q", c, want)
	}
	if later := (Collector{App: db, Schedule: chicago(t)}).images(context.Background(), s, day("2026-10-14")); later[0].Current {
		t.Fatal("a cover without the night's comet is current")
	}
}
