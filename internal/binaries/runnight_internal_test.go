package binaries

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/morningreport"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/klauspost/compress/zstd"
)

// night15523 is the whole night of 2026-10-20 on find 15523's dip window,
// 93 subs from 21:10 to 02:19 CT, measured with the live card's own
// photometry after the night; the nearby stars' agreement is the live
// card's own record for the first 87 subs and not known for the last 6.
const night1020 = "2026-10-20"

func night15523(t *testing.T) []ownimage.LivePoint {
	t.Helper()
	raw, err := os.ReadFile("testdata/night15523-20261020.json.zst")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	var rows []struct {
		T       time.Time `json:"t"`
		Mag     float64   `json:"mag"`
		Err     float64   `json:"err"`
		Comp    float64   `json:"comp"`
		Spread  float64   `json:"spread"`
		Agree   int       `json:"agree"`
		Present int       `json:"present"`
	}
	if err := json.NewDecoder(dec).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	out := make([]ownimage.LivePoint, len(rows))
	for i, r := range rows {
		out[i] = ownimage.LivePoint{FrameID: i + 1, Time: r.T, Filter: redFilter, Mag: 15 + r.Mag, Err: r.Err, Comp: r.Comp, Spread: r.Spread,
			Agree: r.Agree, Present: r.Present}
	}
	return out
}

func dipRun15523() *liveRun {
	w := app.DipWatch{SourceID: 15523, ProjectGUID: "p-15523", Start: time.Date(2026, 10, 21, 2, 8, 3, 0, time.UTC),
		End: time.Date(2026, 10, 21, 22, 8, 3, 0, time.UTC), DepthLow: 0.15, DepthHigh: 0.6, ActiveShare: 0.25}
	return &liveRun{guid: w.ProjectGUID, object: dipField, from: w.Start, to: w.End, dip: &w, goal: goalOf(0.15, 2),
		spot: ownimage.Spot{SourceID: 15523, Object: dipField, RA: 312.86, Dec: 31.99},
		view: LiveCurve{Kind: LiveDipWatch, ProjectGUID: w.ProjectGUID, Object: dipField, SourceID: 15523, Name: "15523", From: w.Start, To: w.End,
			Dip: &LiveDipView{Start: w.Start, End: w.End, DepthLow: w.DepthLow, DepthHigh: w.DepthHigh, ActiveShare: w.ActiveShare}}}
}

func nightService(t *testing.T) *Service {
	t.Helper()
	return &Service{Loc: chicago(t), Now: func() time.Time { return time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC) }}
}

func TestNight15523NoDip(t *testing.T) {
	t.Parallel()
	s := nightService(t)
	s.Site = func(context.Context) (sky.Site, error) { return sky.Site{Latitude: 35, Longitude: -110}, nil }
	raw := night15523(t)
	row := s.nightRow(context.Background(), dipRun15523(), night1020, raw, 12, len(raw))
	if row.DarkStart == nil || row.DarkEnd == nil || row.LowFrom == nil || row.DarkStart.Sub(dipRun15523().from) != 0 ||
		row.DarkEnd.Sub(time.Date(2026, 10, 21, 6, 19, 0, 0, time.UTC)).Abs() > 3*time.Minute || row.LowFrom.Sub(*row.DarkEnd).Abs() > 3*time.Minute {
		t.Fatalf("dark %v to %v, low from %v; the planner had dark and high enough to 06:19 UTC", row.DarkStart, row.DarkEnd, row.LowFrom)
	}
	if row.Kind != app.RunNightDip || row.State != app.DipNone || row.Measured != 93 || len(row.Points) != 93 || row.Night != night1020 {
		t.Fatalf("kind %s state %s (%s), %d measured, %d points, night %s", row.Kind, row.State, row.Note, row.Measured, len(row.Points), row.Night)
	}
	if row.Run != 19 || math.Abs(row.Scatter-0.16) > 0.01 || math.Abs(row.Smallest-0.147) > 0.005 {
		t.Errorf("run %d, scatter %.3f, smallest %.3f, want 19, 0.16, 0.147", row.Run, row.Scatter, row.Smallest)
	}
	if row.Comps != 12 || row.AgreeLow != 9 || row.AgreeHigh != 12 {
		t.Errorf("comps %d agreeing %d to %d", row.Comps, row.AgreeLow, row.AgreeHigh)
	}
	n := 0
	for _, b := range row.Bins {
		n += b.Subs
	}
	if len(row.Bins) != 5 || n != 93 || row.Bins[0].Subs != 19 || row.Bins[4].Subs != 17 || !row.Bins[0].Start.Equal(row.Points[0].Time) {
		t.Fatalf("bins %+v", row.Bins)
	}
	if row.Bins[0].Err == row.Bins[1].Err || row.Bins[4].Err <= row.Bins[0].Err {
		t.Errorf("each average should carry its own error, the noisy last one the largest: %+v", row.Bins)
	}
	checkStored15523(t, s, row)
}

func checkStored15523(t *testing.T, s *Service, row app.RunNight) {
	t.Helper()
	var card LiveCurve
	if err := json.Unmarshal([]byte(row.Card), &card); err != nil || !card.Stored || card.Night != night1020 || len(card.Points) != 93 || card.State != LiveNoDip {
		t.Errorf("card %v stored %v night %s points %d state %s", err, card.Stored, card.Night, len(card.Points), card.State)
	}
	text := morningreport.Formatter{Loc: s.loc(), Zone: "CT"}.Text(morningreport.Report{Night: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
		Subs: &morningreport.Subs{Count: 93}, Runs: []morningreport.RunChart{{Night: row}}})
	for _, want := range []string{
		"<b>Dip and eclipse runs</b>\n• <b>15523</b> dip window (Cygnis Loop Panel 1): <b>no dip</b>: steady to within 0.15 mag over every run of 19 subs. 93 subs, 21:10–02:19 CT (5h 08m).",
		"It would have caught a dip of 0.15 mag or more lasting about an hour; past dips were 0.15 to 0.6 mag. In every sub at least 9 of the 12 nearby stars agreed.",
		"• Eclipse requests: none were imaged.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	jpg, err := NightChart(row, s.loc(), "CT")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(jpg))
	if err != nil || cfg.Width != nightW || cfg.Height != nightH {
		t.Fatalf("chart %v %dx%d", err, cfg.Width, cfg.Height)
	}
	if dir := os.Getenv("NIGHT_SAMPLE_DIR"); dir != "" {
		f := morningreport.Formatter{Loc: s.loc(), Zone: "CT"}
		_ = os.WriteFile(filepath.Join(dir, "message.html"), []byte(text+"\n"), 0o600)
		_ = os.WriteFile(filepath.Join(dir, "chart-15523.jpg"), jpg, 0o600)
		_ = os.WriteFile(filepath.Join(dir, "caption.txt"), []byte(f.RunCaption(row)+"\n"), 0o600)
	}
}

func TestNight15523InjectedDip(t *testing.T) {
	t.Parallel()
	s := nightService(t)
	raw := night15523(t)
	from := len(raw) - 30
	for i := from; i < len(raw); i++ {
		raw[i].Mag += 0.3
	}
	row := s.nightRow(context.Background(), dipRun15523(), night1020, raw, 12, len(raw))
	if row.State != app.DipSeen || math.Abs(row.Depth-0.3) > 0.08 || row.DipStart == nil {
		t.Fatalf("state %s (%s) depth %.3f", row.State, row.Note, row.Depth)
	}
	text := morningreport.Formatter{Loc: s.loc(), Zone: "CT"}.Text(morningreport.Report{Night: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
		Subs: &morningreport.Subs{Count: 93}, Runs: []morningreport.RunChart{{Night: row}}})
	want := "<b>15523</b> dip window (Cygnis Loop Panel 1): <b>dip seen</b>: faded " + strings.TrimSpace(strings.Split(strings.Split(text, "faded ")[1], " mag")[0]) +
		" mag from " + row.DipStart.In(s.loc()).Format("15:04") + " CT, nearby stars steady. 93 subs"
	if !strings.Contains(text, want) || !strings.Contains(text, "dip seen") {
		t.Errorf("report lacks %q:\n%s", want, text)
	}
	if row.DipStart.Before(raw[from-row.Run].Time) || row.DipStart.After(raw[from+row.Run].Time) {
		t.Errorf("dip starts %s, truth %s", row.DipStart, raw[from].Time)
	}
}

func TestNightEclipseSynthetic(t *testing.T) {
	t.Parallel()
	s := nightService(t)
	mid := time.Date(2026, 10, 21, 4, 0, 0, 0, time.UTC)
	m := eclipseModel{mid: mid, err: 5 * time.Minute, half: time.Hour, depth: 0.4, shape: func(dt time.Duration) float64 {
		return math.Max(0, 1-math.Abs(dt.Hours()))
	}}
	run := &liveRun{guid: "p-ecl", object: "field", from: mid.Add(-2 * time.Hour), to: mid.Add(2 * time.Hour), model: &m,
		view: LiveCurve{Kind: LiveEclipse, ProjectGUID: "p-ecl", Object: "field", SourceID: 42, Name: "42",
			Eclipse: &LiveEclipseView{Kind: "primary", Mid: mid, Depth: 0.4}}}
	raw := make([]ownimage.LivePoint, 0, 60)
	for i := range 60 {
		at := mid.Add(-90*time.Minute + time.Duration(i)*3*time.Minute)
		raw = append(raw, ownimage.LivePoint{FrameID: i + 1, Time: at, Filter: redFilter, Mag: 14 + 0.4*m.at(at, 0) + 0.005*math.Sin(float64(i)),
			Err: 0.01, Agree: 10, Present: 10})
	}
	row := s.nightRow(context.Background(), run, night1020, raw, 10, 60)
	if row.Kind != app.RunNightEclipse || row.State != LiveFollowing || len(row.Points) != 60 {
		t.Fatalf("kind %s state %s (%s) points %d", row.Kind, row.State, row.Note, len(row.Points))
	}
	text := morningreport.Formatter{Loc: s.loc(), Zone: "CT"}.Text(morningreport.Report{Night: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
		Subs: &morningreport.Subs{Count: 60}, Runs: []morningreport.RunChart{{Night: row}}})
	if !strings.Contains(text, "• <b>42</b> eclipse request (field): <b>on time</b>: the subs sit within the timing error of 5 min. 60 subs") ||
		!strings.Contains(text, "• Dip windows: none were imaged.") {
		t.Errorf("report:\n%s", text)
	}
	if _, err := NightChart(row, s.loc(), "CT"); err != nil {
		t.Error(err)
	}
}

func TestNightStoredAndReused(t *testing.T) {
	t.Parallel()
	db := testdb.SQLite(t, "nights")
	if err := db.AutoMigrate(&app.RunNight{}); err != nil {
		t.Fatal(err)
	}
	s := nightService(t)
	s.DB = db
	raw := night15523(t)
	run := dipRun15523()
	ctx := context.Background()
	for range 2 {
		if err := s.saveNight(ctx, run, night1020, raw, 12, len(raw)); err != nil {
			t.Fatal(err)
		}
	}
	var n int64
	if err := db.Model(&app.RunNight{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("%d rows %v, want one night stored once", n, err)
	}
	card, ok := s.storedCard(ctx)
	if !ok || !card.Stored || card.Night != night1020 || card.State != LiveNoDip || len(card.Dip.Bins) != 5 {
		t.Fatalf("stored card %v %+v", ok, card)
	}
	s.Current = func() *LiveTarget { return &LiveTarget{ProjectGUID: "other"} }
	s.Now = func() time.Time { return time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC) }
	if _, ok := s.storedCard(ctx); ok {
		t.Error("the stored card stays after the next night's imaging started")
	}
	w := dipSpan{n: 1, start: run.from, end: run.to, mid: run.from.Add(10 * time.Hour)}
	pts, ok := s.storedPoints(ctx, 15523, w, 93)
	if !ok || len(pts) != 93 || math.Abs(pts[0].Mag-raw[0].Mag) > 1e-9 {
		t.Fatalf("stored points %v %d", ok, len(pts))
	}
	if res := judgeResult(app.DipResult{}, pts, goalOf(0.15, 2), s.loc()); res.State != app.DipNone {
		t.Errorf("window from stored nights %s (%s)", res.State, res.Note)
	}
	if _, ok := s.storedPoints(ctx, 15523, w, 94); ok {
		t.Error("a window with a sub no night measured uses the stored nights")
	}
}

func TestBinsCountFromTheFirstSub(t *testing.T) {
	t.Parallel()
	pts := dipPoints(func(int) float64 { return 0 }, 10)
	j := dipFit{run: 4, zero: 15}
	b := j.bins(pts)
	if len(b) != 3 || b[0].Subs != 4 || b[2].Subs != 2 || !b[0].Start.Equal(pts[0].Time) || !b[2].End.Equal(pts[9].Time) {
		t.Fatalf("bins %+v", b)
	}
	if b[2].Err <= b[0].Err {
		t.Errorf("the shorter last average should have the larger error: %+v", b)
	}
}
