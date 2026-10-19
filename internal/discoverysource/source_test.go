package discoverysource_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

const (
	ha       = "H-a"
	oiii     = "O-III"
	sii      = "S-II"
	oct5     = "2026-10-05"
	oct6     = "2026-10-06"
	oct7     = "2026-10-07"
	cygnus   = "Cygnis Loop Panel 2"
	barnard  = "Barnard's E"
	andromed = "Andromeda"
	bb       = "broadband"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr(v float64) *float64 { return &v }

type row struct {
	id            int
	object, night string
	group, class  string
	ra, dec       float64
	sign          int
	snr, dmag     float64
	g             *float64
	in, tested    int
}

// realRows are candidates as the backfill stored them, ids included.
func realRows() []row {
	cyg, bar := ptr(18.055428), ptr(15.497252)
	return []row{
		{18554, andromed, "2026-10-11", bb, discoveries.ClassNova, 10.557969552390773, 41.27669384597613, 1, 10.81, -0.351, nil, 28, 28},
		{19745, barnard, "2026-10-10", bb, discoveries.ClassNewVariable, 293.86567733502966, 11.04847351160513, 1, 17.25, -0.077, bar, 11, 15},
		{19332, barnard, "2026-10-13", bb, discoveries.ClassNewVariable, 293.86564423658456, 11.048545507445027, -1, 21.69, 0.110, bar, 12, 14},
		{15045, cygnus, oct5, ha, discoveries.ClassNewVariable, 314.3202891393868, 28.719199158092568, -1, 9.43, 0.573, cyg, 7, 8},
		{15081, cygnus, oct5, oiii, discoveries.ClassLowConfidence, 314.32218552905306, 28.719054988379728, -1, 8.92, 0.665, nil, 8, 8},
		{15111, cygnus, oct5, sii, discoveries.ClassNewVariable, 314.32088786659506, 28.718622603863487, -1, 10.0, 0.580, cyg, 6, 7},
		{15112, cygnus, oct5, sii, discoveries.ClassLowConfidence, 314.3185961796132, 28.719204332703306, -1, 6.75, 0.747, nil, 7, 7},
		{14443, cygnus, oct6, ha, discoveries.ClassLowConfidence, 314.32023180448346, 28.719347760090777, -1, 5.14, 0.329, cyg, 7, 8},
		{14490, cygnus, oct6, oiii, discoveries.ClassLowConfidence, 314.32229477896067, 28.71919929122288, -1, 6.61, 0.499, nil, 7, 7},
		{14520, cygnus, oct6, sii, discoveries.ClassNewVariable, 314.3209138699956, 28.71854971361548, -1, 10.75, 0.673, cyg, 8, 8},
		{14082, cygnus, oct7, sii, discoveries.ClassNewVariable, 314.32090473437324, 28.718528246240453, -1, 9.50, 0.407, cyg, 7, 7},
		{14083, cygnus, oct7, sii, discoveries.ClassLowConfidence, 314.3184284453698, 28.719122274291916, -1, 6.46, 1.039, nil, 7, 7},
	}
}

// realRuns are the runs of those targets, done unless noted.
func realRuns() []app.DiscoveryRun {
	var out []app.DiscoveryRun
	add := func(object, night, group, state string) {
		out = append(out, app.DiscoveryRun{Object: object, Night: day(night), Group: group, State: state, Checked: "VSX,SkyBoT,ALeRCE", Failed: "Fink"})
	}
	add(andromed, "2026-10-11", bb, app.DiscoveryDone)
	add(barnard, "2026-10-10", bb, app.DiscoveryDone)
	add(barnard, "2026-10-12", bb, app.DiscoverySkipped)
	add(barnard, "2026-10-13", bb, app.DiscoveryDone)
	for _, n := range []string{"2026-10-01", oct5, oct6, oct7} {
		for _, g := range []string{ha, oiii, sii, bb} {
			add(cygnus, n, g, app.DiscoveryDone)
		}
	}
	add(cygnus, "2026-10-12", oiii, app.DiscoveryFailed)
	add(cygnus, "2026-10-12", sii, app.DiscoverySkipped)
	add(cygnus, "2026-10-12", bb, app.DiscoveryDone)
	return out
}

func open(t *testing.T) *gorm.DB {
	t.Helper()
	return prepare(t, testdb.SQLite(t, "src"))
}

func prepare(t *testing.T, db *gorm.DB) *gorm.DB {
	t.Helper()
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoverySource{}, &app.DiscoveryVet{}); err != nil {
		t.Fatal(err)
	}
	runs := realRuns()
	if err := db.Create(&runs).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func runID(t *testing.T, db *gorm.DB, object, night, group string) int {
	t.Helper()
	var r app.DiscoveryRun
	if err := db.Where("object = ? AND night = ? AND filter_group = ?", object, day(night), group).First(&r).Error; err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func store(t *testing.T, db *gorm.DB, rows []row) {
	t.Helper()
	for _, r := range rows {
		c := app.DiscoveryCandidate{ID: r.id, RunID: runID(t, db, r.object, r.night, r.group), Object: r.object, Night: day(r.night), Group: r.group,
			Kind: app.DiscoveryKindChange, Class: r.class, RA: r.ra, Dec: r.dec, Sign: r.sign, SNR: r.snr, DeltaMag: ptr(r.dmag), GaiaG: r.g,
			PresentIn: r.in, SubsTested: r.tested, Matches: "[]"}
		if err := db.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func all(t *testing.T, db *gorm.DB) map[int]discoverysource.Summary {
	t.Helper()
	list, _, err := discoverysource.List(context.Background(), db, discoverysource.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]discoverysource.Summary{}
	for _, s := range list {
		for _, m := range s.Members {
			out[m.ID] = s
		}
	}
	return out
}

func sexagesimal(ra, dec float64) string {
	tenths := int(math.Round(ra / 15 * 36000))
	arcsec := int(math.Round(math.Abs(dec) * 3600))
	sign := "+"
	if dec < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%02dh%02dm%04.1fs %s%02d°%02d'%02d\"", tenths/36000, tenths%36000/600, float64(tenths%600)/10, sign, arcsec/3600, arcsec%3600/60, arcsec%60)
}

func TestRealSourcesMerge(t *testing.T) {
	t.Parallel()
	checkRealSourcesMerge(t, open(t))
}

func checkRealSourcesMerge(t *testing.T, db *gorm.DB) {
	store(t, db, realRows())
	by := all(t, db)

	cyg := by[15111]
	for _, id := range []int{15045, 14520, 14082} {
		if by[id].Source.ID != cyg.Source.ID {
			t.Errorf("Cygnus Loop Panel 2 candidate %d is in source %d, want %d", id, by[id].Source.ID, cyg.Source.ID)
		}
	}
	for _, id := range []int{15081, 15112, 14490, 14083} {
		if by[id].Source.ID == cyg.Source.ID {
			t.Errorf("candidate %d, a separate star, joined the Cygnus source", id)
		}
	}
	if got := sexagesimal(cyg.Best.RA, cyg.Best.Dec); got != `20h57m17.0s +28°43'07"` {
		t.Errorf("Cygnus source at %s", got)
	}
	if cyg.Class != discoveries.ClassNewVariable || cyg.Seen != 3 || cyg.Compared != 4 || !cyg.First.Equal(day(oct5)) || !cyg.Last.Equal(day(oct7)) {
		t.Errorf("Cygnus source %s seen %d of %d, %v to %v", cyg.Class, cyg.Seen, cyg.Compared, cyg.First, cyg.Last)
	}
	if cyg.Best.ID != 14082 || !cyg.Look() {
		t.Errorf("Cygnus best %d look %v", cyg.Best.ID, cyg.Look())
	}
	if len(cyg.Checked) != 3 || len(cyg.Failed) != 1 || cyg.Failed[0] != "Fink" {
		t.Errorf("checked %v failed %v", cyg.Checked, cyg.Failed)
	}

	if by[19745].Source.ID != by[19332].Source.ID || by[19332].Seen != 2 || by[19332].Compared != 2 {
		t.Errorf("Barnard's E pair: sources %d and %d, seen %d of %d", by[19745].Source.ID, by[19332].Source.ID, by[19332].Seen, by[19332].Compared)
	}
	nova := by[18554]
	if len(nova.Members) != 1 || nova.Class != discoveries.ClassNova || nova.Seen != 1 || nova.Compared != 1 {
		t.Errorf("M 31 nova: %d members, %s, seen %d of %d", len(nova.Members), nova.Class, nova.Seen, nova.Compared)
	}
}

func TestAVettedArtifactLeavesNeedsALook(t *testing.T) {
	t.Parallel()
	checkAVettedArtifactLeavesNeedsALook(t, open(t))
}

func checkAVettedArtifactLeavesNeedsALook(t *testing.T, db *gorm.DB) {
	store(t, db, realRows())
	id := all(t, db)[15111].Source.ID
	if err := db.Create(&app.DiscoveryVet{SourceID: id, State: "done", Suggested: app.VetArtifact, Confidence: "medium"}).Error; err != nil {
		t.Fatal(err)
	}
	cyg := all(t, db)[15111]
	if cyg.Look() || !cyg.In(discoverysource.ViewArtifacts) {
		t.Errorf("artifact source look %v artifacts %v", cyg.Look(), cyg.In(discoverysource.ViewArtifacts))
	}
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id = ?", id).Update("confidence", "low").Error; err != nil {
		t.Fatal(err)
	}
	if cyg := all(t, db)[15111]; cyg.Look() || !cyg.In(discoverysource.ViewArtifacts) {
		t.Error("a low-confidence artifact stayed in needs a look")
	}
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id = ?", id).Updates(map[string]any{"suggested": app.VetUnsure, "confidence": "low"}).Error; err != nil {
		t.Fatal(err)
	}
	if cyg := all(t, db)[15111]; cyg.Look() || !cyg.In(discoverysource.ViewUnsure) || cyg.In(discoverysource.ViewArtifacts) {
		t.Error("an undecided source is not under not sure")
	}
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id = ?", id).Updates(map[string]any{"suggested": app.VetReal, "confidence": "medium"}).Error; err != nil {
		t.Fatal(err)
	}
	if cyg := all(t, db)[15111]; !cyg.Look() || cyg.In(discoverysource.ViewUnsure) {
		t.Error("a source the vet thinks real is not in needs a look")
	}
}

func TestACatalogListedSourceIsKnown(t *testing.T) {
	t.Parallel()
	checkACatalogListedSourceIsKnown(t, open(t))
}

func checkACatalogListedSourceIsKnown(t *testing.T, db *gorm.DB) {
	store(t, db, realRows())
	id := all(t, db)[15111].Source.ID
	if err := db.Create(&app.DiscoveryVet{SourceID: id, State: "done", Suggested: app.VetKnown, Confidence: "high"}).Error; err != nil {
		t.Fatal(err)
	}
	cyg := all(t, db)[15111]
	if cyg.Look() || !cyg.In(discoverysource.ViewKnown) {
		t.Errorf("listed source look %v known %v", cyg.Look(), cyg.In(discoverysource.ViewKnown))
	}
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id = ?", id).Update("confidence", "medium").Error; err != nil {
		t.Fatal(err)
	}
	if cyg := all(t, db)[15111]; cyg.Look() || !cyg.In(discoverysource.ViewKnown) {
		t.Error("a medium-confidence catalog match stayed in needs a look")
	}
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id = ?", id).Update("confidence", "low").Error; err != nil {
		t.Fatal(err)
	}
	if cyg := all(t, db)[15111]; !cyg.Look() {
		t.Error("a low-confidence match took the source out of needs a look")
	}
}

func TestSourceSurvivesRerunAndKeepsVetting(t *testing.T) {
	t.Parallel()
	checkSourceSurvivesRerunAndKeepsVetting(t, open(t))
}

func checkSourceSurvivesRerunAndKeepsVetting(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	store(t, db, realRows())
	id := all(t, db)[15111].Source.ID
	at := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
	if _, err := discoverysource.Vet(ctx, db, id, app.VetReal, "slow fade", &at); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Model(&app.DiscoveryCandidate{}).Where("source_id = ? AND vetting = ?", id, app.VetReal).Count(&n)
	if n != 5 {
		t.Errorf("%d of the source's 5 candidates carry its vetting", n)
	}

	rerun := runID(t, db, cygnus, oct6, sii)
	if err := db.Where("run_id = ?", rerun).Delete(&app.DiscoveryCandidate{}).Error; err != nil {
		t.Fatal(err)
	}
	again := realRows()[9]
	again.id, again.ra, again.dec = 30001, again.ra+0.4/3600, again.dec-0.3/3600
	store(t, db, []row{again})
	by := all(t, db)
	if by[30001].Source.ID != id || by[30001].Source.Vetting != app.VetReal {
		t.Fatalf("rerun candidate in source %d (%q), want %d", by[30001].Source.ID, by[30001].Source.Vetting, id)
	}
	var c app.DiscoveryCandidate
	db.First(&c, 30001)
	if c.Vetting != app.VetReal || c.VetNote != "slow fade" {
		t.Errorf("rerun candidate vetting %q %q", c.Vetting, c.VetNote)
	}

	list, counts, err := discoverysource.List(ctx, db, discoverysource.Filter{View: discoverysource.ViewLook})
	if err != nil {
		t.Fatal(err)
	}
	if counts.Vetted != 1 || counts.Look != 2 || len(list) != 2 || list[0].Last.Before(list[1].Last) {
		t.Errorf("counts %+v, look list %d", counts, len(list))
	}
}

func TestOnlyTheRerunCandidateLeftStillFindsItsSource(t *testing.T) {
	t.Parallel()
	db := open(t)
	store(t, db, realRows()[:1])
	id := all(t, db)[18554].Source.ID
	if err := db.Delete(&app.DiscoveryCandidate{}, 18554).Error; err != nil {
		t.Fatal(err)
	}
	r := realRows()[0]
	r.id = 40000
	store(t, db, []row{r})
	if got := all(t, db)[40000].Source.ID; got != id {
		t.Errorf("nova rerun got source %d, want %d", got, id)
	}
}

func TestACandidateLinkingTwoSourcesJoinsThem(t *testing.T) {
	t.Parallel()
	checkACandidateLinkingTwoSourcesJoinsThem(t, open(t))
}

func checkACandidateLinkingTwoSourcesJoinsThem(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	rows := realRows()
	a, b := rows[1], rows[2]
	b.ra += 3.0 / 3600
	b.g = nil
	a.g = nil
	store(t, db, []row{a, b})
	by := all(t, db)
	if by[a.id].Source.ID == by[b.id].Source.ID {
		t.Fatal("3″ apart with no shared star joined")
	}
	at := time.Now().UTC()
	if _, err := discoverysource.Vet(ctx, db, by[b.id].Source.ID, app.VetArtifact, "", &at); err != nil {
		t.Fatal(err)
	}
	mid := rows[1]
	mid.id, mid.night, mid.ra, mid.g = 50000, "2026-10-13", mid.ra+1.5/3600, nil
	store(t, db, []row{mid})
	by = all(t, db)
	if by[a.id].Source.ID != by[b.id].Source.ID || by[mid.id].Source.ID != by[a.id].Source.ID {
		t.Fatalf("sources %d %d %d", by[a.id].Source.ID, by[b.id].Source.ID, by[mid.id].Source.ID)
	}
	if by[a.id].Source.Vetting != app.VetArtifact {
		t.Errorf("joined source lost the vetting: %q", by[a.id].Source.Vetting)
	}
	var n int64
	db.Model(&app.DiscoverySource{}).Count(&n)
	if n != 1 {
		t.Errorf("%d sources left", n)
	}
}

func TestMoversStayPerTrack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t)
	run := runID(t, db, barnard, "2026-10-10", bb)
	track := func(ra float64) string {
		return fmt.Sprintf(`{"points":[{"time":"2026-10-11T04:00:00Z","ra":%f,"dec":11.0}],"rate_arcsec_per_min":0.5,"pa_deg":90}`, ra)
	}
	cands := []app.DiscoveryCandidate{
		{ID: 1, RunID: run, Object: barnard, Night: day("2026-10-10"), Group: bb, Kind: app.DiscoveryKindMover, Class: discoveries.ClassKnownAsteroid, RA: 293.8, Dec: 11, Track: track(293.8), Matches: `[{"catalog":"SkyBoT","name":"Randoross","sep_arcsec":1.5}]`},
		{ID: 2, RunID: run, Object: barnard, Night: day("2026-10-10"), Group: bb, Kind: app.DiscoveryKindMover, Class: discoveries.ClassUnknownMover, RA: 293.9, Dec: 11, Track: track(293.9)},
		{ID: 3, RunID: run, Object: barnard, Night: day("2026-10-10"), Group: bb, Kind: app.DiscoveryKindChange, Class: discoveries.ClassKnownAsteroid, RA: 293.8, Dec: 11},
	}
	if err := db.Create(&cands).Error; err != nil {
		t.Fatal(err)
	}
	list, counts, err := discoverysource.List(ctx, db, discoverysource.Filter{View: discoverysource.ViewMovers})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || counts.Movers != 2 || counts.NewMovers != 1 || counts.Look != 1 || counts.Known != 1 {
		t.Fatalf("movers %d, counts %+v", len(list), counts)
	}
	for _, s := range list {
		if s.Class != discoveries.ClassUnknownMover {
			continue
		}
		if err := db.Create(&app.DiscoveryVet{SourceID: s.Source.ID, State: "done", Suggested: app.VetArtifact, Confidence: "medium"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	_, counts, err = discoverysource.List(ctx, db, discoverysource.Filter{View: discoverysource.ViewMovers})
	if err != nil || counts.NewMovers != 0 || counts.KnownMovers != 1 || counts.Artifacts != 1 || counts.Look != 0 {
		t.Fatalf("after an artifact vet: counts %+v %v", counts, err)
	}
	list, _, err = discoverysource.List(ctx, db, discoverysource.Filter{Search: "randoross"})
	if err != nil || len(list) != 1 || list[0].Best.ID != 1 {
		t.Errorf("search by asteroid name: %d %v", len(list), err)
	}
}

func TestListFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t)
	store(t, db, realRows())
	cases := []struct {
		name string
		f    discoverysource.Filter
		want int
	}{
		{"night", discoverysource.Filter{Night: ptrDay(oct6)}, 2},
		{"since", discoverysource.Filter{Since: ptrDay("2026-10-08")}, 2},
		{"until", discoverysource.Filter{Until: ptrDay(oct5)}, 3},
		{"target", discoverysource.Filter{Object: barnard}, 1},
		{"position", discoverysource.Filter{Search: "00h42m13.9s +41°16′36″"}, 1},
		{"name", discoverysource.Filter{Search: "barnard"}, 1},
		{"low", discoverysource.Filter{View: discoverysource.ViewLow}, 2},
	}
	for _, c := range cases {
		list, _, err := discoverysource.List(ctx, db, c.f)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != c.want {
			t.Errorf("%s: %d sources, want %d", c.name, len(list), c.want)
		}
	}
}

func ptrDay(s string) *time.Time {
	t := day(s)
	return &t
}

func TestParsePosition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		ra, dec float64
		ok      bool
	}{
		{"314.32 28.72", 314.32, 28.72, true},
		{"20h57m17s +28°43′07″", 314.3208333, 28.7186111, true},
		{"20:57:17 -28:43:07", 314.3208333, -28.7186111, true},
		{"00 42 13.9 -00 16 36", 10.5579167, -0.2766667, true},
		{"M 31", 0, 0, false},
		{"Andromeda", 0, 0, false},
		{"400 10", 0, 0, false},
	}
	for _, c := range cases {
		ra, dec, ok := discoverysource.ParsePosition(c.in)
		if ok != c.ok || (ok && (math.Abs(ra-c.ra) > 1e-5 || math.Abs(dec-c.dec) > 1e-5)) {
			t.Errorf("%q: %v %v %v", c.in, ra, dec, ok)
		}
	}
}

func TestNewestCandidateVettingWinsOnTheFirstGathering(t *testing.T) {
	t.Parallel()
	checkNewestCandidateVettingWinsOnTheFirstGathering(t, open(t))
}

func checkNewestCandidateVettingWinsOnTheFirstGathering(t *testing.T, db *gorm.DB) {
	rows := realRows()
	store(t, db, []row{rows[5], rows[9]})
	early, late := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	db.Model(&app.DiscoveryCandidate{}).Where("id = ?", rows[5].id).Updates(discoverysource.VetFields(app.VetUnsure, "early", &early))
	db.Model(&app.DiscoveryCandidate{}).Where("id = ?", rows[9].id).Updates(discoverysource.VetFields(app.VetReal, "confirmed", &late))
	s := all(t, db)[rows[5].id]
	if s.Source.Vetting != app.VetReal || s.Source.VetNote != "confirmed" {
		t.Fatalf("source vetting %q %q", s.Source.Vetting, s.Source.VetNote)
	}
	var n int64
	db.Model(&app.DiscoveryCandidate{}).Where("vetting = ? AND vet_note = ?", app.VetReal, "confirmed").Count(&n)
	if n != 2 {
		t.Errorf("%d of 2 candidates carry the newest vetting", n)
	}
}

func TestAClearedSourceIgnoresAStaleRerunCopy(t *testing.T) {
	t.Parallel()
	checkAClearedSourceIgnoresAStaleRerunCopy(t, open(t))
}

func checkAClearedSourceIgnoresAStaleRerunCopy(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	rows := realRows()
	store(t, db, rows[:1])
	id := all(t, db)[rows[0].id].Source.ID
	old := time.Now().UTC().Add(-time.Hour)
	if _, err := discoverysource.Vet(ctx, db, id, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&app.DiscoveryCandidate{}, rows[0].id).Error; err != nil {
		t.Fatal(err)
	}
	copied := rows[0]
	copied.id = 60000
	store(t, db, []row{copied})
	db.Model(&app.DiscoveryCandidate{}).Where("id = ?", copied.id).Updates(discoverysource.VetFields(app.VetArtifact, "", &old))
	s := all(t, db)[copied.id]
	if s.Source.ID != id || s.Source.Vetting != "" {
		t.Fatalf("source %d vetting %q", s.Source.ID, s.Source.Vetting)
	}
	var c app.DiscoveryCandidate
	db.First(&c, copied.id)
	if c.Vetting != "" {
		t.Errorf("the stale copy kept %q", c.Vetting)
	}
}

func TestGatheringManyCandidatesGivesEachItsOwnSource(t *testing.T) {
	t.Parallel()
	checkGatheringManyCandidatesGivesEachItsOwnSource(t, open(t))
}

func checkGatheringManyCandidatesGivesEachItsOwnSource(t *testing.T, db *gorm.DB) {
	run := runID(t, db, cygnus, oct5, sii)
	const n = 2000
	cands := make([]app.DiscoveryCandidate, n)
	for i := range cands {
		cands[i] = app.DiscoveryCandidate{RunID: run, Object: cygnus, Night: day(oct5), Group: sii, Kind: app.DiscoveryKindChange,
			Class: discoveries.ClassLowConfidence, RA: 314 + float64(i%63)*0.02, Dec: 28 + float64(i/63)*0.02}
	}
	if err := db.CreateInBatches(&cands, 500).Error; err != nil {
		t.Fatal(err)
	}
	if err := discoverysource.Sync(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var sources, unassigned int64
	db.Model(&app.DiscoverySource{}).Count(&sources)
	db.Model(&app.DiscoveryCandidate{}).Where("source_id IS NULL").Count(&unassigned)
	if sources != n || unassigned != 0 {
		t.Errorf("%d sources, %d unassigned", sources, unassigned)
	}
}

func TestOnPostgres(t *testing.T) { //nolint:tparallel
	t.Parallel()
	pg := testdb.Postgres(t, "sources", "sources")
	for name, check := range map[string]func(*testing.T, *gorm.DB){ //nolint:paralleltest
		"real":     checkRealSourcesMerge,
		"rerun":    checkSourceSurvivesRerunAndKeepsVetting,
		"newest":   checkNewestCandidateVettingWinsOnTheFirstGathering,
		"cleared":  checkAClearedSourceIgnoresAStaleRerunCopy,
		"many":     checkGatheringManyCandidatesGivesEachItsOwnSource,
		"join":     checkACandidateLinkingTwoSourcesJoinsThem,
		"earlier":  checkARangeCountsTheEarlierSourcesThatNeedALook,
		"listed":   checkACatalogListedSourceIsKnown,
		"artifact": checkAVettedArtifactLeavesNeedsALook,
	} {
		t.Run(name, func(t *testing.T) {
			db := pg.Session(&gorm.Session{NewDB: true})
			if err := db.Migrator().DropTable(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoverySource{}, &app.DiscoveryVet{}); err != nil {
				t.Fatal(err)
			}
			check(t, prepare(t, db))
		})
	}
}
