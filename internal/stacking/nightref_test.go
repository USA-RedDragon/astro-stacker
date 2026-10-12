package stacking

import (
	"context"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	hartley     = "161P"
	hartleyName = "161P/Hartley-IRAS"
)

func frameWCS(ra, dec, rotDeg float64, w, h int) wcs {
	const scale = 1.93 / 3600
	c, s := math.Cos(rotDeg*math.Pi/180), math.Sin(rotDeg*math.Pi/180)
	return wcs{ra0: ra, dec0: dec, px0: float64(w)/2 + 0.5, py0: float64(h)/2 + 0.5,
		cd: [2][2]float64{{-scale * c, scale * s}, {scale * s, scale * c}}, width: w, height: h}
}

func TestIsMovingBody(t *testing.T) {
	t.Parallel()
	stored := map[string]bodies.Target{
		hartleyName:      {Designation: hartley, Kind: bodies.KindComet},
		"6 Hebe":         {Designation: "6", Kind: bodies.KindAsteroid},
		"161P with M 57": {Designation: "event:x", Kind: bodies.KindEvent},
	}
	for name, want := range map[string]bool{hartleyName: true, "6 Hebe": true, "161P with M 57": false, "C/2025 R2 (SWAN)": false, "NGC 7000": false} {
		if got := isMovingBody(stored, name); got != want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestSplitByNight(t *testing.T) {
	t.Parallel()
	n1, n2 := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	batch := []candidate{{frame: app.Frame{ID: 1, Night: &n1}}, {frame: app.Frame{ID: 2, Night: &n2}}, {frame: app.Frame{ID: 3}}, {frame: app.Frame{ID: 4, Night: &n2}}}
	main, others := splitByNight(batch, "2026-10-12")
	if len(main) != 2 || main[0].frame.ID != 1 || main[1].frame.ID != 3 || len(others["2026-10-13"]) != 2 || len(others) != 1 {
		t.Errorf("main %v others %v", main, others)
	}
	all, none := splitByNight(batch, "")
	if len(all) != 4 || len(none) != 0 {
		t.Errorf("no reference night yet: %d %d", len(all), len(none))
	}
}

func TestLaterNightMeasuresOnItsOwnReference(t *testing.T) {
	t.Parallel()
	const w, h, n = 640, 480, 24
	night1 := time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)
	night3 := night1.Add(48 * time.Hour)
	eph := ephemeris{times: []time.Time{night1.Add(-time.Hour), night1.Add(time.Hour), night3.Add(-time.Hour), night3.Add(time.Hour)},
		ra: []float64{200, 200.02, 203.97, 204.03}, dec: []float64{40, 40, 41, 41}}
	stack := &app.Stack{Width: w, Height: h}
	mids := make([]time.Time, n)
	for i := range mids {
		mids[i] = night3.Add(time.Duration(i-n/2) * 2 * time.Minute)
	}
	main := bodyField{sky: frameWCS(200.01, 40, 0, w, h)}
	if _, _, _, _, err := bodyTrack(eph, main, stack, night3, mids); err == nil || err.Error() != "the body was outside the target's reference frame that night" {
		t.Fatalf("on the first night's reference: %v", err)
	}
	own := bodyField{sky: frameWCS(204, 41, 25, w, h)}
	x0, y0, shifts, track, err := bodyTrack(eph, own, stack, night3, mids)
	if err != nil {
		t.Fatal(err)
	}
	trail := math.Hypot(track[n-1][0]-track[0][0], track[n-1][1]-track[0][1])
	if trail < 20 || x0 < 100 || x0 > w-100 || y0 < 100 || y0 > h-100 {
		t.Fatalf("head %.0f,%.0f trail %.0f px", x0, y0, trail)
	}
	subs := make([][]float32, n)
	stored := make([]storedSub, n)
	for i := range subs {
		img := gaussianAt(w, h, track[i][0], track[i][1], 0.004, 0.01)
		for _, st := range [][2]float64{{60, 70}, {500, 90}, {300, 400}, {580, 420}, {120, 380}} {
			for y := int(st[1]) - 4; y <= int(st[1])+4; y++ {
				for x := int(st[0]) - 4; x <= int(st[0])+4; x++ {
					img[y*w+x] += float32(0.2 * math.Exp(-((float64(x)-st[0])*(float64(x)-st[0])+(float64(y)-st[1])*(float64(y)-st[1]))/4.5))
				}
			}
		}
		noise := uint64(i + 1)
		for j := range img {
			noise = noise*6364136223846793005 + 1442695040888963407
			img[j] += float32(0.0003 * (float64(noise>>11)/float64(1<<53) - 0.5) * 3.46)
		}
		subs[i] = img
		stored[i] = storedSub{exposure: 10, weight: 1}
	}
	gs := make([]goals.Sub, n)
	for i := range gs {
		gs[i] = goals.Sub{Exposure: 10, Weight: 1, Effective: 10}
	}
	coma := goals.ComaRegions(w/goals.NoiseBin, h/goals.NoiseBin, x0, y0, nil, 1.93)
	meas, err := goals.NewMeasurer(w, h, gs, goals.Options{Seed: 3, RegionMask: coma.Union()})
	if err != nil {
		t.Fatal(err)
	}
	read := func(i int) ([]float32, int, int, error) { return slices.Clone(subs[i]), w, h, nil }
	mean, err := meanOfSubs(n, w*h, read)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := separateComet(stored, shifts, track, starMask(mean, w, h), DefaultOptions(), read, func(int, int) {},
		func(i int, sub []float32, w, h int, _ float64) {
			bn, err := goals.Bin(sub, w, h, 10, 0.9)
			if err == nil {
				err = meas.Add(i, bn)
			}
			if err != nil {
				t.Error(err)
			}
		}); err != nil {
		t.Fatal(err)
	}
	row := app.BodyNight{EffectiveHours: n * 10.0 / 3600}
	applyComaMeasure(&row, meas, nil, coma, true)
	if row.Error != nil || row.HeadSNR == nil || *row.HeadSNR < 5 || row.SNR <= 0 {
		t.Errorf("third night's measure %+v", row)
	}
	t.Logf("third night: head at %.0f,%.0f on its own reference, trail %.0f px, coma SNR %.1f", x0, y0, trail, row.SNR)
}

func gaussianAt(w, h int, x, y, amp, sky float64) []float32 {
	d := make([]float32, w*h)
	for j := range h {
		for i := range w {
			r2 := (float64(i)-x)*(float64(i)-x) + (float64(j)-y)*(float64(j)-y)
			d[j*w+i] = float32(sky + amp*math.Exp(-r2/(2*8*8)))
		}
	}
	return d
}

func TestCombineNightsAlignsTheBody(t *testing.T) {
	t.Parallel()
	const w, h = 400, 300
	out := frameWCS(100, 20, 0, w, h)
	raC0, decC0 := out.toSky(201, 151)
	n1 := frameWCS(100, 20, 0, w, h)
	n2 := frameWCS(101.5, 20.8, 35, w, h)
	raC2, decC2 := 101.52, 20.79
	px, py, _ := n2.toPixel(raC2, decC2)
	img1 := gaussianAt(w, h, 200, float64(h)-151, 1, 0.1)
	img2 := gaussianAt(w, h, px-1, float64(h)-py, 1, 0.3)
	data, cover := combineNights(out, w, h, raC0, decC0, []nightImage{
		{data: img1, w: w, h: h, sky: n1, raC: raC0, decC: decC0, weight: 1, bg: 0.1},
		{data: img2, w: w, h: h, sky: n2, raC: raC2, decC: decC2, weight: 1, bg: 0.3},
	})
	head := (h-151)*w + 200
	if cover[head] != 2 || math.Abs(float64(data[head])-1.2) > 0.02 {
		t.Errorf("head %v cover %d", data[head], cover[head])
	}
	off := (h-151)*w + 230
	if math.Abs(float64(data[off])-(0.2+math.Exp(-900.0/128))) > 0.02 {
		t.Errorf("30 px off the head %v", data[off])
	}
	r := coverRect(cover, w, h, 2)
	if r.W == 0 || r.H == 0 || r.X > 200 || r.X+r.W < 200 {
		t.Errorf("cover rect %+v", r)
	}
}

func nightDB(t *testing.T) *Pipeline {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}, &app.TargetReference{}, &app.ReferenceReset{}, &app.NightReference{}, &app.BodyNight{}); err != nil {
		t.Fatal(err)
	}
	opts := DefaultPipelineOptions()
	return NewPipeline(nil, "", "", db, nil, siril.Runner{}, t.TempDir(), opts)
}

func addNightFrames(t *testing.T, p *Pipeline, stack *app.Stack, night string, status string, n int) {
	t.Helper()
	day, _ := time.Parse(goals.NightLayout, night)
	for i := range n {
		at := day.Add(27*time.Hour + time.Duration(i)*time.Minute)
		exp := 60.0
		f := app.Frame{Key: fmt.Sprintf("%s-%s-%s-%s-%d", stack.Object, stack.Filter, night, status, i), ETag: "e", LastModified: at, Type: frameTypeLight,
			Object: stack.Object, Filter: stack.Filter, DateObs: &at, Night: &day, Exposure: &exp}
		if err := p.db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		key := "registered/" + f.Key
		sf := app.StackFrame{FrameID: f.ID, StackID: &stack.ID, Status: status, Weight: 1, Score: 1, Exposure: exp, RegisteredKey: &key}
		if status == app.StackStatusRegistration {
			sf.StackID, sf.RegisteredKey, sf.Weight = nil, nil, 0
		}
		if err := p.db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestNightSubsKeepOneFramePerNight(t *testing.T) {
	t.Parallel()
	p := nightDB(t)
	stack := &app.Stack{Object: hartleyName, Filter: filterLuminance}
	if err := p.db.Create(stack).Error; err != nil {
		t.Fatal(err)
	}
	addNightFrames(t, p, stack, "2026-10-12", app.StackStatusAdded, 5)
	addNightFrames(t, p, stack, "2026-10-13", app.StackStatusNight, 4)
	addNightFrames(t, p, stack, "2026-10-13", app.StackStatusAdded, 2)
	addNightFrames(t, p, stack, "2026-10-14", app.StackStatusAdded, 1)
	day, _ := time.Parse(goals.NightLayout, "2026-10-13")
	if err := p.db.Create(&app.NightReference{Object: stack.Object, Night: day, FrameID: 1, ObjectKey: "k"}).Error; err != nil {
		t.Fatal(err)
	}
	nights, err := p.nightSubs(context.Background(), stack)
	if err != nil {
		t.Fatal(err)
	}
	if len(nights["2026-10-12"]) != 5 || len(nights["2026-10-13"]) != 4 || len(nights["2026-10-14"]) != 1 {
		t.Fatalf("nights %d %d %d", len(nights["2026-10-12"]), len(nights["2026-10-13"]), len(nights["2026-10-14"]))
	}
	for _, r := range nights["2026-10-13"] {
		if r.Status != app.StackStatusNight {
			t.Errorf("a sub on the first night's reference mixed into a night with its own: %+v", r)
		}
	}
	if !combineCandidate(nights) {
		t.Error("two nights of three or more subs combine")
	}
	delete(nights, "2026-10-13")
	if combineCandidate(nights) {
		t.Error("one full night does not combine")
	}
}

func TestNightReReferenceAfterManyFailures(t *testing.T) {
	t.Parallel()
	p := nightDB(t)
	ctx := context.Background()
	day, _ := time.Parse(goals.NightLayout, "2026-10-13")
	addNightFrames(t, p, &app.Stack{ID: 1, Object: hartleyName, Filter: filterLuminance}, "2026-10-12", app.StackStatusAdded, 3)
	for round := range maxReReferences + 1 {
		stack := &app.Stack{ID: 1, Object: hartleyName, Filter: fmt.Sprintf("L%d", round)}
		addNightFrames(t, p, stack, "2026-10-13", app.StackStatusNight, 3)
		addNightFrames(t, p, stack, "2026-10-13", app.StackStatusRegistration, reReferenceFailures)
		if err := p.db.Create(&app.NightReference{Object: stack.Object, Night: day, FrameID: 1, ObjectKey: "k"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := p.maybeReReferenceNight(ctx, stack.Object, "2026-10-13"); err != nil {
			t.Fatal(err)
		}
		var refs, left, others int64
		p.db.Model(&app.NightReference{}).Count(&refs)
		p.db.Model(&app.StackFrame{}).Where("status IN ?", []string{app.StackStatusNight, app.StackStatusRegistration}).Count(&left)
		p.db.Model(&app.StackFrame{}).Where("status = ?", app.StackStatusAdded).Count(&others)
		if round < maxReReferences && (refs != 0 || left != 0 || others != 3) {
			t.Fatalf("round %d: refs %d night rows %d other nights %d", round, refs, left, others)
		}
		if round == maxReReferences && refs != 1 {
			t.Errorf("replaced more than %d times", maxReReferences)
		}
	}
}

func TestImagedNightsWaitsAndSkips(t *testing.T) {
	t.Parallel()
	body := bodies.Target{Designation: hartley, Kind: bodies.KindComet}
	rows := func(n int) []nightSub {
		out := make([]nightSub, n)
		for i := range out {
			out[i] = nightSub{RegisteredKey: fmt.Sprintf("k%d", i), Weight: 1, Score: 1}
		}
		return out
	}
	nights := map[string][]nightSub{"2026-10-12": rows(5), "2026-10-13": rows(4), "2026-10-14": rows(2)}
	key := "s"
	at := time.Now()
	imaged := func(night string) app.BodyNight {
		sig := bodyNightSignature(nights[night], body)
		d, _ := time.Parse(goals.NightLayout, night)
		return app.BodyNight{Night: d, Signature: sig, ImageSignature: sig, StarlessKey: &key, RefTime: &at}
	}
	bs := bodyStack{body: body, nights: nights, have: map[string]app.BodyNight{"2026-10-12": imaged("2026-10-12")}}
	if _, ok := imagedNights(bs); ok {
		t.Error("combined while a night waits for its image")
	}
	bs.have["2026-10-13"] = imaged("2026-10-13")
	got, ok := imagedNights(bs)
	if !ok || len(got) != 2 || got[0].Night.After(got[1].Night) {
		t.Errorf("both nights imaged: %v %v", len(got), ok)
	}
	failed := imaged("2026-10-13")
	msg := "the night's registration reference has no plate solution yet"
	failed.Error, failed.ImageSignature = &msg, ""
	bs.have["2026-10-13"] = failed
	got, ok = imagedNights(bs)
	if !ok || len(got) != 1 {
		t.Errorf("a night that cannot be measured is left out: %d %v", len(got), ok)
	}
}
