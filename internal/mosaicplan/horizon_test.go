package mosaicplan_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"gorm.io/gorm"
)

const (
	profile1    = "prof-1"
	activeState = "Active"
)

func southReport(block float64) *mosaics.HorizonReport {
	h := &mosaics.Horizon{Step: 1, Altitudes: make([]float64, 360)}
	for i := 100; i <= 260; i++ {
		h.Altitudes[i] = block
	}
	return &mosaics.HorizonReport{ProfileID: profile1, Custom: true, Horizon: h}
}

func TestLimitForMirrorsDetermineHorizon(t *testing.T) {
	t.Parallel()
	min20 := fp(20)
	cases := []struct {
		name      string
		hs        mosaicplan.HorizonSettings
		report    *mosaics.HorizonReport
		ok, known bool
		kind      string
		reason    string
		at180     float64
		custom    bool
	}{
		{"flat ignores a reported horizon", mosaicplan.HorizonSettings{ProfileID: profile1, MinAltitude: min20}, southReport(40), true, true, mosaicplan.HorizonMinimumAltitude, "", 20, false},
		{"custom wanted, nothing reported", mosaicplan.HorizonSettings{ProfileID: profile1, MinAltitude: min20, UseCustom: true}, nil, true, false, mosaicplan.HorizonCustom, "has not reported", 20, false},
		{"custom wanted, other profile", mosaicplan.HorizonSettings{ProfileID: "prof-2", MinAltitude: min20, UseCustom: true}, southReport(40), true, false, mosaicplan.HorizonCustom, "prof-2", 20, false},
		{"custom wanted, profile has none", mosaicplan.HorizonSettings{ProfileID: profile1, MinAltitude: min20, UseCustom: true}, &mosaics.HorizonReport{ProfileID: profile1}, true, true, mosaicplan.HorizonMinimumAltitude, "has none", 20, false},
		{"custom applied with offset", mosaicplan.HorizonSettings{ProfileID: profile1, MinAltitude: min20, UseCustom: true, Offset: 5}, southReport(40), true, true, mosaicplan.HorizonCustom, "", 45, true},
		{"null minimum altitude", mosaicplan.HorizonSettings{ProfileID: profile1}, nil, false, false, mosaicplan.HorizonMinimumAltitude, "no minimum altitude", 0, false},
	}
	for _, tc := range cases {
		limit, b, ok := mosaicplan.LimitFor(tc.hs, tc.report)
		if ok != tc.ok || b.Known != tc.known || b.Kind != tc.kind || b.Offset != tc.hs.Offset {
			t.Errorf("%s: ok %v basis %+v", tc.name, ok, b)
		}
		if (tc.reason == "") != (b.Reason == nil) || (b.Reason != nil && !strings.Contains(*b.Reason, tc.reason)) {
			t.Errorf("%s: reason %v, want %q", tc.name, b.Reason, tc.reason)
		}
		if (tc.hs.MinAltitude == nil) != (b.MinimumAltitude == nil) {
			t.Errorf("%s: minimum altitude %v", tc.name, b.MinimumAltitude)
		}
		if ok && (limit.At(180) != tc.at180 || (limit.Horizon != nil) != tc.custom || limit.At(0) != 20) {
			t.Errorf("%s: limit at 180 %v at 0 %v custom %v", tc.name, limit.At(180), limit.At(0), limit.Horizon != nil)
		}
	}
}

type seasonsRig struct {
	t     *testing.T
	sched *gorm.DB
	svc   *mosaicplan.Service
}

func newSeasonsRig(t *testing.T) *seasonsRig {
	t.Helper()
	sched := schedFixture(t)
	svc := mosaicplan.New(appFixture(t), sched)
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	svc.Site = func(context.Context) (mosaics.Site, bool) { return mosaics.Site{Lat: 32, Lon: -97}, true }
	return &seasonsRig{t: t, sched: sched, svc: svc}
}

func (r *seasonsRig) plan(setup string, src mosaicplan.HorizonSource) mosaicplan.SeasonPlan {
	r.t.Helper()
	if err := r.sched.Exec(setup).Error; err != nil {
		r.t.Fatal(err)
	}
	r.svc.Horizon = src
	plan, err := r.svc.Seasons(context.Background(), markarian, "weakest", "", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		r.t.Fatal(err)
	}
	return plan
}

func reported(r *mosaics.HorizonReport) mosaicplan.HorizonSource {
	return func(context.Context) (mosaics.HorizonReport, bool) { return *r, true }
}

func TestSeasonsFollowTheCustomHorizon(t *testing.T) {
	t.Parallel()
	const custom = `UPDATE project SET usecustomhorizon = 1 WHERE "Id" = 1`
	rig := newSeasonsRig(t)
	flat := rig.plan(`UPDATE project SET usecustomhorizon = 0 WHERE "Id" = 1`, reported(southReport(80)))
	if h := flat.Horizon; h.Kind != mosaicplan.HorizonMinimumAltitude || !h.Known || h.Reason != nil || h.MinimumAltitude == nil || *h.MinimumAltitude != 20 {
		t.Fatalf("flat basis %+v", h)
	}
	applied := rig.plan(custom, reported(southReport(80)))
	if h := applied.Horizon; h.Kind != mosaicplan.HorizonCustom || !h.Known || h.Reason != nil {
		t.Fatalf("applied basis %+v", h)
	}
	if !(flat.Months[3].Hours >= 3 && applied.Months[3].Hours < flat.Months[3].Hours-2) || applied.NightsLeft >= flat.NightsLeft {
		t.Errorf("an 80° southern horizon did not cut Markarian's April hours: flat %v custom %v, nights %d vs %d", flat.Months[3], applied.Months[3], flat.NightsLeft, applied.NightsLeft)
	}
	unknown := rig.plan(custom, nil)
	if h := unknown.Horizon; h.Kind != mosaicplan.HorizonCustom || h.Known || h.Reason == nil || !strings.Contains(*h.Reason, "flat minimum altitude") {
		t.Fatalf("unknown basis %+v", h)
	}
	for i := range flat.Months {
		if unknown.Months[i] != flat.Months[i] {
			t.Fatalf("fallback hours differ from the flat minimum altitude: %v vs %v", unknown.Months, flat.Months)
		}
	}
	null := rig.plan(`UPDATE project SET minimumaltitude = NULL WHERE "Id" = 1`, nil)
	if h := null.Horizon; h.Known || h.MinimumAltitude != nil || null.Basis.Reason == nil || !strings.Contains(*null.Basis.Reason, "no minimum altitude") || len(null.Months) != 0 || null.NightsLeft != 0 {
		t.Fatalf("null minimum altitude was not reported as unknown: %+v %+v", h, null.Basis)
	}
	for _, p := range null.PanelPriority {
		if p.MonthsLeft != 0 || p.ThisSeason {
			t.Errorf("panel priority with no altitude limit %+v", p)
		}
	}
}

func TestSeasonBoostsFollowTheCustomHorizon(t *testing.T) {
	t.Parallel()
	southern := func(useCustom bool, null bool) []planning.Project {
		u := useCustom
		return []planning.Project{{Name: "Capricornus", State: activeState, MinimumAltitude: 15, MinAltitudeNull: null, MinimumTime: 30, ProfileID: profile1, UseCustomHorizon: &u,
			Targets: []planning.Target{{GUID: "cap-1", Name: "Capricornus Panel 1", Active: true, RAHours: fp(21.5), Dec: fp(-20), Season: &planning.Season{NightsLeft: 50, SeasonEnd: "2026-11-30"},
				Plans: []planning.Plan{{Filter: "L", Exposure: 600, Desired: 60, Enabled: true}}}}}}
	}
	one := func(ps []planning.Project, r *mosaics.HorizonReport) mosaicplan.TargetSeasonBoost {
		bs := mosaicplan.SeasonBoostsFor(ps, testSite(), r, allClear(), 1, testNow())
		if len(bs) != 1 {
			t.Fatalf("boosts %v", bs)
		}
		return bs[0]
	}
	flat := one(southern(false, false), southReport(60))
	fallback := one(southern(true, false), nil)
	custom := one(southern(true, false), southReport(60))
	if flat.Available <= 0 || fallback.Available != flat.Available {
		t.Fatalf("flat %v, unknown horizon fallback %v", flat.Available, fallback.Available)
	}
	if !(custom.Available < flat.Available/2) || custom.Boost <= flat.Boost {
		t.Errorf("a 60° southern horizon should cut a southern target's season hours and raise its boost: flat %+v custom %+v", flat, custom)
	}
	if null := one(southern(false, true), nil); null.Boost != 0 || null.Available != 0 {
		t.Errorf("null minimum altitude got a boost %+v", null)
	}
}
