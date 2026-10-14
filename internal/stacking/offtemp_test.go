package stacking

import (
	"context"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const objectHartley = "161P/Hartley-IRAS"

func ruleSince() time.Time { return time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) }

func summer() time.Time { return time.Date(2025, 7, 1, 4, 0, 0, 0, time.UTC) }

func temps(set, ccd *float64) app.Frame {
	return app.Frame{SetTemp: set, CCDTemp: ccd}
}

func TestOffTemp(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		set, ccd *float64
		off      bool
	}{
		{"at setpoint", ptrTo(2.0), ptrTo(2.0), false},
		{"just inside above", ptrTo(2.0), ptrTo(2.4), false},
		{"exactly at the limit above", ptrTo(2.1), ptrTo(2.6), false},
		{"exactly at the limit below", ptrTo(-10.0), ptrTo(-10.5), false},
		{"float32 reading at the limit", ptrTo(2.0), ptrTo(2.5000000953674316), false},
		{"just outside above", ptrTo(2.0), ptrTo(2.51), true},
		{"just outside below", ptrTo(-10.0), ptrTo(-10.51), true},
		{"just inside below", ptrTo(-10.0), ptrTo(-9.6), false},
		{"warm after reconnect", ptrTo(2.0), ptrTo(8.2), true},
		{"setpoint 9", ptrTo(9.0), ptrTo(9.9), true},
		{"setpoint 3", ptrTo(3.0), ptrTo(3.7), true},
		{"colder than setpoint", ptrTo(0.0), ptrTo(-1.0), true},
		{"no sensor reading", ptrTo(2.0), nil, false},
		{"no setpoint", nil, ptrTo(8.2), false},
		{"neither", nil, nil, false},
	} {
		why, off := offTemp(temps(c.set, c.ccd))
		if off != c.off {
			t.Errorf("%s: off %v, want %v (%q)", c.name, off, c.off, why)
		}
		if off == (why == "") {
			t.Errorf("%s: off %v with reason %q", c.name, off, why)
		}
	}
	why, _ := offTemp(temps(ptrTo(2.0), ptrTo(8.2)))
	if want := "the sensor was at 8.2 °C, 6.2 °C off its 2 °C setpoint; lights more than 0.5 °C off it are left out"; why != want {
		t.Errorf("reason %q, want %q", why, want)
	}
}

// The temperature rule wins over every status but a reject in Target
// Scheduler or by hand, for lights taken on or after its date.
func TestWithTemp(t *testing.T) {
	t.Parallel()
	since := ruleSince()
	warm, cold := temps(ptrTo(2.0), ptrTo(10.4)), temps(ptrTo(2.0), ptrTo(2.1))
	warm.DateObs = ptrTo(time.Date(2026, 10, 14, 6, 7, 0, 0, time.UTC))
	cold.DateObs = warm.DateObs
	for _, status := range []string{"", app.StackStatusLowScore, app.StackStatusUnmeasured, app.StackStatusMoon,
		app.StackStatusCalibration, app.StackStatusNoMetadata} {
		if got, why := withTemp(status, warm, since); got != app.StackStatusOffTemp || why == "" {
			t.Errorf("warm %q: %q %q, want off_temp", status, got, why)
		}
		if got, why := withTemp(status, cold, since); got != status || why != "" {
			t.Errorf("cold %q: %q %q, want unchanged", status, got, why)
		}
		if got, why := withTemp(status, warm, time.Time{}); got != status || why != "" {
			t.Errorf("rule off %q: %q %q, want unchanged", status, got, why)
		}
	}
	if got, _ := withTemp(app.StackStatusRejected, warm, since); got != app.StackStatusRejected {
		t.Errorf("a reject in TS became %q", got)
	}
	night := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	before := night.AddDate(0, 0, -1)
	for _, c := range []struct {
		name    string
		dateObs *time.Time
		night   *time.Time
		off     bool
	}{
		{"summer night", ptrTo(summer()), nil, false},
		{"just before the date", ptrTo(since.Add(-time.Second)), &night, false},
		{"at the date", ptrTo(since), nil, true},
		{"night only, on the date", nil, &night, true},
		{"night only, before the date", nil, &before, false},
		{"no date", nil, nil, false},
	} {
		f := temps(ptrTo(2.0), ptrTo(10.4))
		f.DateObs, f.Night = c.dateObs, c.night
		if got, _ := withTemp("", f, since); (got == app.StackStatusOffTemp) != c.off {
			t.Errorf("%s: %q, want off_temp %v", c.name, got, c.off)
		}
	}
}

// A light off its setpoint is told to TS as the stacker's reject, with its
// own reason.
func TestOffTempVerdict(t *testing.T) {
	t.Parallel()
	if v := wantedVerdict(app.StackStatusOffTemp); v != app.TSVerdictReject {
		t.Errorf("verdict %d, want reject", v)
	}
	if r := verdictReason(app.StackStatusOffTemp); r != "stacker: temperature" {
		t.Errorf("reason %q", r)
	}
}

func offTempDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func offTempFrames(t *testing.T, db *gorm.DB, keys []string, ccd []*float64) []app.Frame {
	t.Helper()
	night := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	frames := make([]app.Frame, len(keys))
	for i, k := range keys {
		frames[i] = app.Frame{Key: k, ETag: k, Size: 100, Type: frameTypeLight, Object: objectHartley, Filter: filterRed,
			LastModified: night.Add(-time.Hour), Exposure: ptrTo(60.0), Night: &night, SetTemp: ptrTo(2.0), CCDTemp: ccd[i]}
		if err := db.Create(&frames[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return frames
}

// At ingest a light off its setpoint is left out as off_temp even when it
// would be stacked, and others are stacked or left out as before.
func TestTriageOffTemp(t *testing.T) {
	t.Parallel()
	db := offTempDB(t)
	frames := offTempFrames(t, db,
		[]string{"H/warm_cal.fits", "H/cold_cal.fits", "H/unknown_cal.fits", "H/old-warm_cal.fits", "H/warm-unscored_cal.fits"},
		[]*float64{ptrTo(8.2), ptrTo(2.3), nil, ptrTo(14.0), ptrTo(10.4)})
	old := &frames[3]
	old.DateObs = ptrTo(summer())
	if err := db.Model(old).Update("date_obs", old.DateObs).Error; err != nil {
		t.Fatal(err)
	}
	scores := map[string]quality.SubScore{}
	for _, f := range frames[:4] {
		scores[path.Base(f.Key)] = quality.SubScore{GradingStatus: quality.GradingAccepted, Score: 0.9, TargetBest: 1}
	}
	keys := func(batch []candidate) string {
		got := make([]string, 0, len(batch))
		for _, c := range batch {
			got = append(got, c.frame.Key)
		}
		return strings.Join(got, ",")
	}
	off := &Pipeline{db: offTempDB(t), opts: DefaultPipelineOptions()}
	batch, err := off.triage(context.Background(), frames[:4], scores, nil, nil, map[int]string{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := keys(batch), "H/warm_cal.fits,H/cold_cal.fits,H/unknown_cal.fits,H/old-warm_cal.fits"; got != want {
		t.Errorf("rule off: batch %s, want %s", got, want)
	}
	p := &Pipeline{db: db, opts: DefaultPipelineOptions()}
	p.opts.RejectOffTempSince = ruleSince()
	batch, err = p.triage(context.Background(), frames, scores, nil, nil, map[int]string{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := keys(batch), "H/cold_cal.fits,H/unknown_cal.fits,H/old-warm_cal.fits"; got != want {
		t.Errorf("batch %s, want %s", got, want)
	}
	for _, c := range []struct {
		frame  app.Frame
		status string
	}{{frames[0], app.StackStatusOffTemp}, {frames[4], app.StackStatusOffTemp}} {
		var sf app.StackFrame
		if err := db.Where("frame_id = ?", c.frame.ID).First(&sf).Error; err != nil {
			t.Fatal(err)
		}
		if sf.Status != c.status || sf.NextAttemptAt != nil || sf.Error == nil || !strings.Contains(*sf.Error, "0.5 °C") {
			t.Errorf("%s: status %q next %v error %v", c.frame.Key, sf.Status, sf.NextAttemptAt, sf.Error)
		}
	}
}

// Lights stacked before the rule that break it are taken out of their
// masters, which are marked for the moon sweep to stack again; lights in
// the limit, without readings, or taken before the rule's date stay in, and
// with the rule off nothing changes.
func TestDropOffTemp(t *testing.T) {
	t.Parallel()
	db := offTempDB(t)
	frames := offTempFrames(t, db,
		[]string{"H/a.fits", "H/b.fits", "H/c.fits", "H/d.fits", "H/e.fits", "H/g.fits", "O/f.fits"},
		[]*float64{ptrTo(10.3), ptrTo(2.5), nil, ptrTo(1.4), ptrTo(3.7), ptrTo(17.0), ptrTo(2.0)})
	if err := db.Model(&frames[5]).Update("date_obs", ptrTo(summer())).Error; err != nil {
		t.Fatal(err)
	}
	stack := app.Stack{Object: objectHartley, Filter: filterRed}
	other := app.Stack{Object: objectOther, Filter: filterRed}
	for _, s := range []*app.Stack{&stack, &other} {
		if err := db.Create(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	statuses := []string{app.StackStatusAdded, app.StackStatusAdded, app.StackStatusAdded, app.StackStatusAdded,
		app.StackStatusLowScore, app.StackStatusAdded, app.StackStatusAdded}
	for i, f := range frames {
		id := stack.ID
		if i == len(frames)-1 {
			id = other.ID
		}
		if err := db.Create(&app.StackFrame{ID: 100 + i, FrameID: f.ID, StackID: &id, Status: statuses[i]}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := (&Pipeline{db: db}).dropOffTemp(context.Background()); err != nil {
		t.Fatal(err)
	}
	var unchanged int64
	if err := db.Model(&app.StackFrame{}).Where("status = ?", app.StackStatusAdded).Count(&unchanged).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged != 6 {
		t.Errorf("rule off: %d lights still added, want 6", unchanged)
	}
	p := &Pipeline{db: db, opts: PipelineOptions{RejectOffTempSince: ruleSince()}}
	if err := p.dropOffTemp(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sfs []app.StackFrame
	if err := db.Order("id").Find(&sfs).Error; err != nil {
		t.Fatal(err)
	}
	want := []string{app.StackStatusOffTemp, app.StackStatusAdded, app.StackStatusAdded, app.StackStatusOffTemp,
		app.StackStatusLowScore, app.StackStatusAdded, app.StackStatusAdded}
	for i, sf := range sfs {
		if sf.Status != want[i] || sf.NextAttemptAt != nil {
			t.Errorf("%s: status %q (next %v), want %q", frames[i].Key, sf.Status, sf.NextAttemptAt, want[i])
		}
		if (sf.Status == app.StackStatusOffTemp) != (sf.Error != nil) {
			t.Errorf("%s: error %v", frames[i].Key, sf.Error)
		}
	}
	for _, s := range []app.Stack{stack, other} {
		if err := db.First(&s, s.ID).Error; err != nil {
			t.Fatal(err)
		}
		if s.NeedsRebuild != (s.ID == stack.ID) {
			t.Errorf("%s needs_rebuild %v", s.Object, s.NeedsRebuild)
		}
	}

	if err := db.Model(&app.Stack{}).Where("id = ?", stack.ID).UpdateColumn("needs_rebuild", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := p.dropOffTemp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&stack, stack.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stack.NeedsRebuild {
		t.Error("a second pass with nothing left to take out marked the master again")
	}
}
