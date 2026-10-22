package binaries

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"gorm.io/gorm"
)

type fieldSubs struct {
	ownimage.Data
	subs map[string][]ownimage.Sub
}

func (f fieldSubs) Subs(_ context.Context, object string) ([]ownimage.Sub, error) {
	return f.subs[object], nil
}

// garlicSubs are 169 subs of 120 s from 01:34 to 08:18 UTC on 2026-10-22,
// as the night of 2026-10-21 imaged find 27822's second eclipse.
func garlicSubs() []ownimage.Sub {
	first := time.Date(2026, 10, 22, 1, 33, 54, 0, time.UTC)
	step := time.Date(2026, 10, 22, 8, 17, 29, 0, time.UTC).Sub(first) / 168
	out := make([]ownimage.Sub, 169)
	for i := range out {
		out[i] = ownimage.Sub{FrameID: 27000 + i, Filter: redFilter, Start: first.Add(time.Duration(i) * step), Exposure: 120}
	}
	return out
}

func repeatDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.SQLite(t, "repeat")
	if err := db.AutoMigrate(&app.RunNight{}, &app.EclipseRequest{}, &app.DipWatch{}, &app.DiscoverySource{}); err != nil {
		t.Fatal(err)
	}
	return db
}

const (
	garlicField = "Garlic Nebula"
	garlicLive  = "06096b69-bfa4-5778-b270-63487470cdbc"
	garlicStale = "be58011c-80ce-582b-be8d-c7c7a117a4d7"
)

func TestSweepSkipsCancelledRequestOfTheSameEclipse(t *testing.T) {
	t.Parallel()
	db := repeatDB(t)
	start, end := time.Date(2026, 10, 21, 21, 35, 0, 0, time.UTC), time.Date(2026, 10, 22, 15, 43, 0, 0, time.UTC)
	mid := time.Date(2026, 10, 22, 6, 38, 47, 0, time.UTC)
	for _, row := range []any{
		&app.DiscoverySource{ID: 27822, Object: garlicField},
		&app.EclipseRequest{ID: 2, SourceID: 27822, Kind: KindSecondary, Mid: mid, Start: start, End: end, ProjectGUID: garlicStale, State: app.EclipseCancel},
		&app.EclipseRequest{ID: 60, SourceID: 27822, Kind: KindSecondary, Mid: mid, Start: start, End: end, ProjectGUID: garlicLive, State: app.EclipseSent, Attempt: 1},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := nightService(t)
	s.DB = db
	s.Now = func() time.Time { return time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC) }
	s.LiveData = fieldSubs{subs: map[string][]ownimage.Sub{garlicField: garlicSubs()}}
	jobs, err := s.nightJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].guid != garlicLive || jobs[0].nights["2026-10-21"] != 169 {
		t.Fatalf("jobs %+v, want only the live request's night of 169 subs", jobs)
	}
}

func TestSweepSkipsMovedDipWatch(t *testing.T) {
	t.Parallel()
	db := repeatDB(t)
	start, end := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC), time.Date(2026, 10, 22, 16, 0, 0, 0, time.UTC)
	for _, row := range []any{
		&app.DiscoverySource{ID: 15523, Object: garlicField},
		&app.DiscoverySource{ID: 99, Object: garlicField},
		&app.DipWatch{SourceID: 15523, ProjectGUID: "dip-old", Start: start, End: end},
		&app.DipWatch{SourceID: 15523, ProjectGUID: "dip-new", Start: start.Add(20 * time.Minute), End: end},
		&app.DipWatch{SourceID: 99, ProjectGUID: "dip-by-hand", Start: start, End: end},
		&app.EclipseRequest{SourceID: 15523, Kind: KindDip, Start: start, End: end, ProjectGUID: "dip-old", State: app.EclipseMoved},
		&app.EclipseRequest{SourceID: 15523, Kind: KindDip, Start: start.Add(20 * time.Minute), End: end, ProjectGUID: "dip-new", State: app.EclipseSent},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := nightService(t)
	s.DB = db
	s.Now = func() time.Time { return time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC) }
	s.LiveData = fieldSubs{subs: map[string][]ownimage.Sub{garlicField: garlicSubs()}}
	jobs, err := s.nightJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, j := range jobs {
		got[j.guid] = true
	}
	if len(jobs) != 2 || !got["dip-new"] || !got["dip-by-hand"] {
		t.Fatalf("jobs %+v, want the live dip window and the watch made by hand, not the moved one", got)
	}
}

func eclipseRunOf(guid string, mid time.Time) *liveRun {
	m := eclipseModel{mid: mid, err: 5 * time.Minute, half: time.Hour, depth: 0.4, shape: func(time.Duration) float64 { return 0 }}
	return &liveRun{guid: guid, object: garlicField, from: mid.Add(-9 * time.Hour), to: mid.Add(9 * time.Hour), model: &m,
		view: LiveCurve{Kind: LiveEclipse, ProjectGUID: guid, Object: garlicField, SourceID: 27822, Name: "27822",
			Eclipse: &LiveEclipseView{Kind: KindSecondary, Mid: mid, Depth: 0.4}}}
}

func TestTwoProjectsOfOneRunStoreOneNight(t *testing.T) {
	t.Parallel()
	db := repeatDB(t)
	s := nightService(t)
	s.DB = db
	mid := time.Date(2026, 10, 22, 6, 38, 47, 0, time.UTC)
	raw := make([]ownimage.LivePoint, 0, 169)
	for i, sub := range garlicSubs() {
		raw = append(raw, ownimage.LivePoint{FrameID: sub.FrameID, Time: subMid(sub), Filter: redFilter, Mag: 14 + 0.001*float64(i%7), Err: 0.01, Agree: 12, Present: 12})
	}
	ctx := context.Background()
	for _, guid := range []string{garlicStale, garlicLive} {
		if err := s.saveNight(ctx, eclipseRunOf(guid, mid), "2026-10-21", raw, 12, 169); err != nil {
			t.Fatal(err)
		}
	}
	var rows []app.RunNight
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ProjectGUID != garlicLive || rows[0].Subs != 169 {
		guids := make([]string, len(rows))
		for i, r := range rows {
			guids[i] = r.ProjectGUID
		}
		t.Fatalf("rows under %v, want one under the last project to store it", guids)
	}
	other := eclipseRunOf("p-other-night", mid.Add(24*time.Hour))
	later := make([]ownimage.LivePoint, len(raw))
	for i, p := range raw {
		p.Time = p.Time.Add(24 * time.Hour)
		later[i] = p
	}
	if err := s.saveNight(ctx, other, "2026-10-22", later, 12, 169); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Model(&app.RunNight{}).Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("%d rows %v, want the next night's run stored on its own", n, err)
	}
}
