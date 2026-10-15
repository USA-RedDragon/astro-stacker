package discoveries

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
)

func tnsFixture(t *testing.T) *tnsmirror.Index {
	t.Helper()
	b, err := os.Open("../tnsmirror/testdata/tns_public_objects.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	f, err := tnsmirror.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return tnsmirror.NewIndex(f.Objects, f.From.Format("20060102"), f.From)
}

func m31(ctx context.Context, t *testing.T) Known {
	t.Helper()
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	objs, err := ix.Cone(ctx, 10.679274962197383, 41.26270118660898, 1.7)
	if err != nil {
		t.Fatal(err)
	}
	return Known{Galaxies: galaxiesOf(objs), Checked: []string{"VSX", "SkyBoT"}}
}

func classifyTNS(ctx context.Context, t *testing.T, res *Result) []Labelled {
	t.Helper()
	ls, tracks, _, failed := Classify(ctx, res, m31(ctx, t), tnsOnly(tnsFixture(t)))
	if len(tracks) != 0 || len(failed) != 0 {
		t.Fatalf("%d tracks, failed %v", len(tracks), failed)
	}
	return ls
}

func tnsOnly(ix *tnsmirror.Index) AlertLookup {
	c := &Catalogs{TNSLocal: LocalTNS(func() *tnsmirror.Index { return ix }), TNSMatchArcsec: 3}
	return func(ctx context.Context, ra, dec, radius float64) ([]Alert, []string, []string) {
		as, err := c.tnsSearch(ctx, ra, dec, radius)
		if err != nil {
			return nil, nil, []string{sourceTNS}
		}
		return as, []string{sourceTNS}, nil
	}
}

// Candidate 18554, Andromeda's 2026-10-11 night (mid 2026-10-12 10:04 UT),
// is AT 2026acsi, reported by XOSS on 2026-09-22.660: with the local TNS
// copy it is known, by that name, and our data are not early.
func TestM31CandidateIsKnownTNSObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mid := time.Date(2026, 10, 12, 10, 4, 12, 0, time.UTC)
	res := &Result{MidTime: mid, Start: mid.Add(-3 * time.Hour), Candidates: []Candidate{{RA: 10.557969552390773, Dec: 41.27669384597613, Sign: 1, SNR: 10.81}}}
	ls, _, checked, failed := Classify(ctx, res, m31(ctx, t), tnsOnly(tnsFixture(t)))
	if len(failed) > 0 {
		t.Errorf("failed: %v", failed)
	}
	l := ls[0]
	if l.Class != ClassKnownTransient {
		t.Fatalf("classed %s, matches %+v", l.Class, l.Matches)
	}
	m, ok := TNSMatch(l.Matches)
	if !ok || m.Name != "AT 2026acsi" || m.SepArcsec > 1 {
		t.Fatalf("TNS match %+v", m)
	}
	if want := "already known: AT 2026acsi (nova candidate, XOSS, Sep 22)"; m.Note != want || KnownText(*m.TNS) != want {
		t.Errorf("label %q, want %q", m.Note, want)
	}
	if m.TNS.EarlyData || m.TNS.OurClass != ClassNova || m.TNS.Mag == nil || *m.TNS.Mag != 17.4 || m.TNS.Filter != "Clear-" {
		t.Errorf("TNS details %+v", *m.TNS)
	}
	if !contains(checked, sourceTNS) {
		t.Errorf("TNS not listed as checked: %v", checked)
	}
}

// A change seen at AT 2026acsi's place on a night before 2026-09-22.660
// is early data.
func TestSeenBeforeTNSDiscoveryIsEarlyData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mid := time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC)
	first := time.Date(2026, 9, 21, 4, 12, 0, 0, time.UTC)
	c := Candidate{RA: 10.55799, Dec: 41.27680, Sign: 1, SNR: 6, Light: []LightPoint{
		{Time: first.Add(-time.Hour), Flux: 1, FluxErr: 1}, {Time: first, Flux: 4, FluxErr: 1}, {Time: first.Add(time.Hour), Flux: 5, FluxErr: 1}}}
	ls := classifyTNS(ctx, t, &Result{MidTime: mid, Start: mid.Add(-4 * time.Hour), Candidates: []Candidate{c}})
	m, ok := TNSMatch(ls[0].Matches)
	if !ok || !m.TNS.EarlyData || !m.TNS.Seen.Equal(first) {
		t.Fatalf("match %+v, details %+v", m, m.TNS)
	}
	if ls[0].Class != ClassKnownTransient || m.Note != "already known: AT 2026acsi (nova candidate, XOSS, Sep 22)" {
		t.Errorf("class %s, label %q", ls[0].Class, m.Note)
	}
}

func TestTNSLocalNotLoaded(t *testing.T) {
	t.Parallel()
	c := &Catalogs{TNSLocal: LocalTNS(func() *tnsmirror.Index { return nil })}
	if _, err := c.tnsSearch(context.Background(), 10, 41, 3); err == nil {
		t.Error("an unloaded local copy without a key answered")
	}
	if c.TNSEnabled() || !c.tnsAvailable() {
		t.Error("availability")
	}
}

func TestKnownTextYear(t *testing.T) {
	t.Parallel()
	got := KnownText(TNSEntry{Name: "SN 2025abc", Type: "SN Ia", Group: "ZTF", Discovered: time.Date(2025, 12, 30, 0, 0, 0, 0, time.UTC), Seen: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)})
	if got != "already known: SN 2025abc (SN Ia, ZTF, Dec 30 2025)" {
		t.Error(got)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// AT 2026acsi seen on 2026-10-11 is early data when the target's comparison
// of 2026-09-20 found the same change, before TNS's 2026-09-22.660.
func TestEarlierNightBeforeTNSDiscoveryIsEarlyData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testdb.SQLite(t, "")
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}); err != nil {
		t.Fatal(err)
	}
	early := time.Date(2026, 9, 21, 6, 30, 0, 0, time.UTC)
	for i, rc := range []struct {
		night time.Time
		mid   time.Time
		class string
		ra    float64
	}{
		{time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), early, ClassNova, 10.55797},
		{time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), early.Add(-72 * time.Hour), ClassLowConfidence, 10.55797},
		{time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC), early.Add(-96 * time.Hour), ClassNova, 10.56},
	} {
		mid := rc.mid
		run := app.DiscoveryRun{Object: "Andromeda", Night: rc.night, Group: GroupBroadband, State: app.DiscoveryDone, MidTime: &mid}
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.DiscoveryCandidate{RunID: run.ID, Object: "Andromeda", Night: rc.night, Group: GroupBroadband, Kind: app.DiscoveryKindChange,
			Class: rc.class, RA: rc.ra, Dec: 41.27669, Sign: 1, SNR: float64(5 + i)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := &Runner{db: db, cat: &Catalogs{TNSMatchArcsec: 3}}
	mid := time.Date(2026, 10, 12, 10, 4, 12, 0, time.UTC)
	res := &Result{MidTime: mid, Candidates: []Candidate{{RA: 10.557969552390773, Dec: 41.27669384597613, Sign: 1, SNR: 10.81}}}
	ls := classifyTNS(ctx, t, res)
	r.earlierTNS(ctx, "Andromeda", ls)
	m, ok := TNSMatch(ls[0].Matches)
	if !ok || !m.TNS.EarlyData || !m.TNS.Seen.Equal(early) {
		t.Fatalf("match %+v, details %+v", m, m.TNS)
	}
	ls = classifyTNS(ctx, t, res)
	r.earlierTNS(ctx, "Triangulum", ls)
	if m, _ := TNSMatch(ls[0].Matches); m.TNS.EarlyData {
		t.Error("another target's sightings counted")
	}
}
