package discover_test

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/halpha"
	"github.com/USA-RedDragon/astro-stacker/internal/moon"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	oxygen    = "O-III"
	lightType = "LIGHT"
	m51       = "M 51"
	haShort   = "Ha short"
	ngc7000   = "NGC 7000"
	m8        = "M 8"
	m42       = "M 42"
	m27       = "M 27"
	sulphur   = "S-II"
	pickScale = 206.264806 * 3.76 / 405
	skyNow    = 20.69
)

type band struct {
	lo, hi, r float64
}

func pickBands() []band {
	return []band{
		{-7, -3.5, 1000}, {43.4, 46, 800}, {-26, -23, 100}, {22, 23.5, 60}, {-15.5, -12, 400},
		{46.5, 48, 2}, {68, 70.5, 1}, {53.5, 55.5, 20}, {42.2, 42.55, 30},
	}
}

func pickMap() *halpha.Map {
	m := &halpha.Map{W: 1440, H: 720, CRPix1: 720.5, CRPix2: 360.5, CDelt1: -0.25, CDelt2: 0.25, CRVal1: 180, Data: make([]float32, 1440*720), FetchedAt: time.Now()}
	for j := range m.H {
		dec := (float64(m.H-j) - m.CRPix2) * m.CDelt2
		v := float32(0.5)
		for _, b := range pickBands() {
			if dec >= b.lo && dec < b.hi {
				v = float32(b.r)
			}
		}
		for i := range m.W {
			m.Data[j*m.W+i] = v
		}
	}
	return m
}

func pickFixture(t *testing.T) (*discover.Service, *gorm.DB) {
	t.Helper()
	db := openDB(t, "app")
	return pickFixtureOn(t, db), db
}

func pickFixtureOn(t *testing.T, db *gorm.DB) *discover.Service {
	t.Helper()
	if err := db.AutoMigrate(&app.Stack{}, &app.Frame{}, &app.ObjectXref{}, &app.GoalMeasurement{}, &app.SkySample{}); err != nil {
		t.Fatal(err)
	}
	stacks := map[string][]string{
		m42: {hydrogen, sulphur, oxygen}, ngc7000: {hydrogen, sulphur}, m8: {hydrogen, oxygen}, m27: {hydrogen, oxygen},
		m51: {"L", "R", "G", "B"}, "M 81": {"L", "R", "G", "B"}, "M 101": {"L", hydrogen}, "NGC 891": {"L", hydrogen}, "M 78": {"L"},
	}
	for obj, fs := range stacks {
		for _, f := range fs {
			if err := db.Create(&app.Stack{Object: obj, Filter: f, EffectiveSeconds: 7200, Subs: 24}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	at := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	n := 0
	light := func(obj, filter string, exp float64, count int) {
		for range count {
			n++
			e := exp
			if err := db.Create(&app.Frame{Key: obj + filter + time.Duration(n).String(), ETag: "e", Size: 1, LastModified: at, Type: lightType, Object: obj, Filter: filter, Exposure: &e, DateObs: &at}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	light(m42, "Ha", 300, 5)
	light(m8, "Ha", 600, 2)
	light(m42, "OIII", 600, 3)
	light(m51, "L", 300, 10)
	light("M 78", "L", 120, 3)
	light(m51, "R", 180, 4)
	bad := "unreadable"
	e := 900.0
	db.Create(&app.Frame{Key: "bad", ETag: "e", Size: 1, LastModified: at, Type: lightType, Object: m42, Filter: "Ha", Exposure: &e, IndexError: &bad})
	h := func(obj string, snr, hours float64) app.GoalMeasurement {
		return app.GoalMeasurement{Object: obj, Filter: hydrogen, SNR: snr, EffectiveHours: hours, PixelScale: pickScale, Subs: 24, MeasuredAt: at}
	}
	depth := 25.0
	ms := make([]app.GoalMeasurement, 0, 8)
	ms = append(ms,
		h(m42, 5, 2), h(ngc7000, 10, 3), h(m8, 4, 1), h(m27, 2, 1),
		app.GoalMeasurement{Object: m51, Filter: "L", SNR: 8, EffectiveHours: 4, PixelScale: pickScale, Depth: &depth, DepthSystem: goals.SystemGaiaG, MeasuredAt: at},
		app.GoalMeasurement{Object: "M 81", Filter: "L", SNR: 8, EffectiveHours: 4, PixelScale: pickScale * 1.5, Depth: &depth, DepthSystem: goals.SystemGaiaG, MeasuredAt: at},
	)
	low := h("M 101", 1, 1)
	low.Filter, low.LowConfidence = hydrogen, true
	ms = append(ms, low)
	if err := db.Create(&ms).Error; err != nil {
		t.Fatal(err)
	}
	id := 1000
	for d, sm := range []struct {
		obj string
		mag float64
	}{{m51, 20.4}, {m51, 20.6}, {"M 101", 20.9}} {
		night := time.Date(2026, 9, 10+d, 0, 0, 0, 0, time.UTC)
		dark := darkMoment(night)
		for k := range 5 {
			id++
			when := dark.Add(time.Duration(k) * time.Minute)
			if err := db.Create(&app.SkySample{FrameID: id, Object: sm.obj, Filter: "L", Night: &night, DateObs: &when, SkyMag: sm.mag, MeasuredAt: at}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	m := pickMap()
	mag := skyNow
	s := &discover.Service{
		Catalog: ix, AppDB: db,
		Site:   func(context.Context) (sky.Site, error) { return sky.Site{Latitude: testSiteLa, Longitude: -99.4}, nil },
		Rig:    discover.Rig{Frame: sky.Frame{FocalLength: 405, PixelSize: 3.76, WidthPx: 6248, HeightPx: 4176}, MinAltitude: 30},
		HAlpha: func() (*halpha.Map, halpha.Status) { return m, halpha.Status{State: halpha.StateReady} },
		Sky: func(context.Context) skybright.Value {
			return skybright.Value{Mag: &mag, Basis: skybright.Basis{Source: skybright.SourceMeasured, Band: skybright.Band}}
		},
		Now: func() time.Time { return time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC) },
	}
	return s
}

func darkMoment(night time.Time) time.Time {
	for h := range 48 {
		at := night.Add(time.Duration(h) * 30 * time.Minute)
		if moon.At(at).Altitude(at, testSiteLa, -99.4) < -5 {
			return at
		}
	}
	return night
}

func pickPlans() *planning.Snapshot {
	return &planning.Snapshot{
		Templates: []planning.Template{
			{ID: 1, Name: "Ha", Filter: hydrogen, UsedByPlans: 3}, {ID: 7, Name: haShort, Filter: "Ha", UsedByPlans: 1},
			{ID: 2, Name: "OIII", Filter: oxygen, UsedByPlans: 2}, {ID: 3, Name: "Lum", Filter: "L", UsedByPlans: 4},
			{ID: 4, Name: "Red", Filter: "R"}, {ID: 5, Name: "Green", Filter: "G"}, {ID: 6, Name: "Blue", Filter: "B"},
		},
		Sets: []planning.ExposureSet{
			{ID: "set-lo", Name: "Ha short, OIII", Items: []planning.SetItem{{Template: haShort, Exposure: 300}, {Template: "OIII", Exposure: 300}}, Projects: 2},
		},
	}
}

func filterOf(p discover.ExposurePick, f string) *discover.PickFilter {
	for i := range p.Filters {
		if p.Filters[i].Filter == f {
			return &p.Filters[i]
		}
	}
	return nil
}

func ruleRs(r discover.PickRule) map[string]float64 {
	out := map[string]float64{}
	for _, p := range r.Points {
		out[p.Name] = p.Rayleigh
	}
	return out
}

func TestPickEmissionDerivesTheSIIThreshold(t *testing.T) {
	t.Parallel()
	s, _ := pickFixture(t)
	p, err := s.Pick(context.Background(), "M16", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	if p.Palette != "HOO + S-II" || p.HAlpha == nil || p.HAlpha.Rayleigh != 400 {
		t.Fatalf("pick %+v", p)
	}
	if p.Reason != "HOO + S-II: emission nebula, H-α 400 R at or above 283 R" {
		t.Errorf("reason %q", p.Reason)
	}
	if len(p.Rules) != 1 {
		t.Fatalf("rules %+v", p.Rules)
	}
	r := p.Rules[0]
	if !r.Derived || r.Threshold == nil || *r.Threshold != 282.9 || !r.Met || len(r.Points) != 4 {
		t.Fatalf("rule %+v", r)
	}
	if !strings.Contains(r.Basis, "2 of 2 with S-II are at or above, 2 of 2 without are below") {
		t.Errorf("basis %q", r.Basis)
	}
	if got := []string{p.Filters[0].Filter, p.Filters[1].Filter, p.Filters[2].Filter}; !slices.Equal(got, []string{"H-α", oxygen, sulphur}) {
		t.Errorf("filters %v", got)
	}
	ha := filterOf(p, "H-α")
	if ha.Template == nil || ha.Template.ID != 1 || ha.Exposure == nil || *ha.Exposure != 300 || ha.Subs != 7 || ha.ExposureBasis != "median of your 7 H-α subs" {
		t.Errorf("H-α filter %+v", ha)
	}
	sii := filterOf(p, sulphur)
	if sii.Template != nil || sii.Exposure != nil || sii.ExposureBasis != "no subs in this filter yet" || !slices.Equal(p.Missing, []string{sulphur}) {
		t.Errorf("S-II %+v missing %v", sii, p.Missing)
	}
	if o := filterOf(p, oxygen); o.Hours.Hours != nil || o.Hours.Unknown != "unknown, the goal model will measure it" {
		t.Errorf("O-III hours %+v", o.Hours)
	}
	checkHAlphaHours(t, ha, r)
}

func checkHAlphaHours(t *testing.T, ha *discover.PickFilter, r discover.PickRule) {
	t.Helper()
	rs := ruleRs(r)
	logs := make([]float64, 0, 4)
	for name, m := range map[string][2]float64{m42: {5, 2}, ngc7000: {10, 3}, m8: {4, 1}, m27: {2, 1}} {
		logs = append(logs, math.Log10(m[1]*(10/m[0])*(10/m[0]))+2*math.Log10(rs[name]))
	}
	slices.Sort(logs)
	want := math.Pow(10, (logs[1]+logs[2])/2) / (400 * 400)
	if ha.Hours.Hours == nil || math.Abs(*ha.Hours.Hours-want) > 1e-9*want || ha.Hours.Points != 4 || ha.Hours.Low == nil || *ha.Hours.Low > want || *ha.Hours.High < want {
		t.Errorf("H-α hours %+v want %v", ha.Hours, want)
	}
}

func TestPickReusesAMatchingSet(t *testing.T) {
	t.Parallel()
	s, _ := pickFixture(t)
	p, err := s.Pick(context.Background(), "M27", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	if p.Palette != "HOO" || p.Set == nil || p.Set.ID != "set-lo" {
		t.Fatalf("pick %+v", p)
	}
	if p.Reason != "HOO: planetary nebula, H-α 60 R, below the 283 R S-II rule" {
		t.Errorf("reason %q", p.Reason)
	}
	if ha := filterOf(p, "H-α"); ha.Template == nil || ha.Template.Name != haShort || !strings.HasPrefix(ha.TemplateBasis, "from your set") {
		t.Errorf("H-α template %+v", ha)
	}
}

func TestPickGalaxyDepthHours(t *testing.T) {
	t.Parallel()
	s, _ := pickFixture(t)
	p, err := s.Pick(context.Background(), "M81", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	if p.Palette != discover.PaletteLRGB || len(p.Filters) != 4 {
		t.Fatalf("pick %+v", p)
	}
	r := p.Rules[0]
	if !r.Derived || r.Threshold == nil || *r.Threshold != 6.4 || r.Met {
		t.Errorf("H-α rule %+v", r)
	}
	if p.Reason != "LRGB: galaxy, H-α 1.0 R, below the 6.4 R H-α rule" {
		t.Errorf("reason %q", p.Reason)
	}
	l := filterOf(p, "L")
	d1 := 25 - 1.25*math.Log10(4) + 0.5*(20.6-20.5)
	want := math.Pow(10, (25.8-d1)/1.25)
	if l.Hours.Hours == nil || math.Abs(*l.Hours.Hours-want) > 1e-9 || l.Hours.Points != 1 || l.Hours.Goal != "25.8 mag/arcsec² at SNR 3" {
		t.Fatalf("L hours %+v want %v", l.Hours, want)
	}
	sbG, ok := discover.BandToGaiaG(22.78, 0.87)
	if !ok {
		t.Fatal("B−V 0.87 out of range")
	}
	for _, part := range []string{
		"hours to the 25.8 mag/arcsec² Gaia G depth goal at SNR 3, the depth goal a new target gets for L",
		"22.8 mag/arcsec² B (OpenNGC)",
		fmt.Sprintf("is %.1f in Gaia G", sbG),
		fmt.Sprintf("%.1f mag brighter than the goal, so the galaxy's mean brightness is above the goal depth", 25.8-sbG),
	} {
		if !strings.Contains(l.Hours.Basis, part) {
			t.Errorf("basis %q lacks %q", l.Hours.Basis, part)
		}
	}
	if g := filterOf(p, "G"); g.Hours.Hours != nil || !strings.HasPrefix(g.Hours.Unknown, "unknown: the sky brightness in G is not measured") {
		t.Errorf("G hours %+v", g.Hours)
	}
	if l.Exposure == nil || *l.Exposure != 300 || l.Subs != 13 {
		t.Errorf("L exposure %+v", l)
	}
	if rr := filterOf(p, "R"); rr.Exposure == nil || *rr.Exposure != 180 {
		t.Errorf("R exposure %+v", rr)
	}
}

func TestPickGalaxyWithoutColourStatesBothBands(t *testing.T) {
	t.Parallel()
	s, _ := pickFixture(t)
	var id string
	for _, o := range s.Catalog.All() {
		if o.Type == catalog.TypeGalaxy && o.SurfaceBrightness != nil && o.BMinusV == nil && o.Dec > 0 {
			id = o.ID
			break
		}
	}
	p, err := s.Pick(context.Background(), id, pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	l := filterOf(p, "L")
	if l.Hours.Hours == nil || !strings.Contains(l.Hours.Basis, "in B (OpenNGC), and no B−V is catalogued to put it in Gaia G beside the goal") {
		t.Errorf("%s L hours %+v", id, l.Hours)
	}
}

func TestPickDustUsesLFromDustTargets(t *testing.T) {
	t.Parallel()
	s, _ := pickFixture(t)
	p, err := s.Pick(context.Background(), "B33", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	if p.Palette != discover.PaletteLRGB || p.Reason != "LRGB: dark nebula" || len(p.Rules) != 0 {
		t.Fatalf("pick %+v", p)
	}
	l := filterOf(p, "L")
	if l.Exposure == nil || *l.Exposure != 120 || l.ExposureBasis != "median of your 3 L subs on reflection and dark nebulae" {
		t.Errorf("L %+v", l)
	}
	for _, f := range p.Filters {
		if f.Hours.Hours != nil || f.Hours.Unknown != "unknown, the goal model will measure it" {
			t.Errorf("%s hours %+v", f.Filter, f.Hours)
		}
	}
}

func TestPickFallsBackToANamedRule(t *testing.T) {
	t.Parallel()
	s, db := pickFixture(t)
	if err := db.Where("filter = ?", sulphur).Delete(&app.Stack{}).Error; err != nil {
		t.Fatal(err)
	}
	p, err := s.Pick(context.Background(), "M16", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	r := p.Rules[0]
	if r.Derived || r.Threshold == nil || *r.Threshold != halpha.ScoreFullR || r.Met || p.Palette != "HOO" {
		t.Fatalf("rule %+v palette %s", r, p.Palette)
	}
	if !strings.Contains(r.Basis, "not derived from your targets") || !strings.Contains(r.Basis, "0 with and 4 without") {
		t.Errorf("basis %q", r.Basis)
	}
}

func TestPickClusterAndUnknownType(t *testing.T) {
	t.Parallel()
	s, _ := pickFixture(t)
	p, err := s.Pick(context.Background(), "M13", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != discover.ClassCluster || p.Palette != discover.PaletteLRGB {
		t.Fatalf("pick %+v", p)
	}
	for _, f := range p.Filters {
		if f.Hours.Hours != nil {
			t.Errorf("cluster hours %+v", f)
		}
	}
	if _, err := s.Pick(context.Background(), "no-such-object", nil); err == nil {
		t.Error("unknown object picked")
	}
	p, err = s.Pick(context.Background(), "M42", nil)
	if err != nil || p.Filters[0].Template != nil || p.Filters[0].TemplateBasis != "the scheduler's templates are not loaded" {
		t.Errorf("no plans: %+v %v", p, err)
	}
}

func TestBandToGaiaG(t *testing.T) {
	t.Parallel()
	g, ok := discover.BandToGaiaG(22, 0.5)
	want := 22 + (-0.04749 - 0.0124*0.5 - 0.2901*0.25 + 0.02008*0.125) - 0.5
	if !ok || math.Abs(g-want) > 1e-12 {
		t.Errorf("%v %v want %v", g, ok, want)
	}
	for _, bv := range []float64{-0.5, 1.3, 2} {
		if _, ok := discover.BandToGaiaG(22, bv); ok {
			t.Errorf("B−V %v accepted", bv)
		}
	}
}

func TestPickGalaxyAddsHAlphaWithoutForegroundHours(t *testing.T) {
	t.Parallel()
	s, _ := pickFixture(t)
	p, err := s.Pick(context.Background(), "NGC891", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	if p.Palette != "LRGB + H-α" || p.Reason != "LRGB + H-α: galaxy, H-α 30 R at or above 6.4 R" {
		t.Fatalf("pick %q %q", p.Palette, p.Reason)
	}
	h := filterOf(p, "H-α")
	if h == nil || h.Hours.Hours != nil || !strings.Contains(h.Hours.Unknown, "Milky Way foreground") {
		t.Errorf("H-α hours %+v", h)
	}
}

func TestPickClassFromCatalogueCrossMatch(t *testing.T) {
	t.Parallel()
	s, _ := pickFixture(t)
	p, err := s.Pick(context.Background(), "NGC 7023", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != discover.ClassReflection || p.Palette != discover.PaletteLRGB || p.NoPick != "" ||
		p.ClassBasis != "reflection nebula: listed as vdB 139 (van den Bergh 1966, a catalogue of reflection nebulae), 0.4′ from its centre" {
		t.Fatalf("pick %+v", p)
	}
	p, err = s.Pick(context.Background(), "IC 4628", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != "" || p.Palette != "" || len(p.Filters) != 0 || !strings.Contains(p.NoPick, "emission or a reflection nebula") ||
		!strings.HasPrefix(p.Reason, "nebula: emission or reflection is not known: OpenNGC") {
		t.Fatalf("pick %+v", p)
	}
}
