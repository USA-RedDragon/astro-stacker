package discoveries

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/ztflc"
)

// eclipse is a vetted new eclipsing binary as the October 2026 backfill
// measured it on its eclipse night, with the alert brokers' ZTF objects at
// it and the catalogued neighbors nearest it.
type eclipse struct {
	name      string
	ztf       string
	c         Candidate
	alerts    []Alert
	neighbors []Variable
	galaxies  []Galaxy
}

const finkUnknown = "Unknown"

func vettedEclipses() []eclipse {
	g := func(v float64) *float64 { return &v }
	return []eclipse{
		{
			name: "2MASS J00433343+4139446", ztf: "2mass_j00433343+4139446",
			c: Candidate{RA: 10.88928, Dec: 41.66243, Sign: -1, SNR: 113.0, GaiaG: g(15.04), DeltaMag: g(0.236), PresentIn: 28, SubsTested: 28},
			alerts: []Alert{
				{Source: sourceAlerce, ID: "ZTF18abpwsdv", SepArc: 0.11},
				{Source: sourceFink, ID: "ZTF18abpwsdv", Class: finkUnknown, SepArc: 0.06},
			},
			neighbors: []Variable{
				{Name: "Gaia DR3 381299946636131072", Type: "L", RA: 10.884800, Dec: 41.665284, Catalog: catalogVSX},
				{Name: "[VRJ2006] M31V-J00433121+4139505", Type: "V*", RA: 10.880042, Dec: 41.664028, Catalog: catalogSIMBAD},
			},
			galaxies: []Galaxy{{Name: "Andromeda Galaxy", RA: 10.684708, Dec: 41.268750, Major: 190, Minor: 60, PA: 35, HasOrientation: true, LocalGroup: true}},
		},
		{
			name: "Gaia DR3 2179309875368546432", ztf: "gaia_dr3_2179309875368546432",
			c: Candidate{RA: 323.27678, Dec: 58.63426, Sign: -1, SNR: 64.1, GaiaG: g(14.85), DeltaMag: g(0.161), PresentIn: 18, SubsTested: 18},
			alerts: []Alert{
				{Source: sourceAlerce, ID: "ZTF18abildix", SepArc: 0.94},
				{Source: sourceAlerce, ID: "ZTF19acglptt", SepArc: 1.12},
				{Source: sourceAlerce, ID: "ZTF18abcdqpf", SepArc: 0.15},
				{Source: sourceFink, ID: "ZTF18abcdqpf", Class: finkUnknown, SepArc: 0.2},
			},
		},
		{
			name: "2MASS J21043883+4319143", ztf: "2mass_j21043883+4319143",
			c: Candidate{RA: 316.161804, Dec: 43.320664, Sign: -1, SNR: 64.1, GaiaG: g(14.239), DeltaMag: g(0.163), PresentIn: 5, SubsTested: 5, LowConf: true},
			alerts: []Alert{
				{Source: sourceAlerce, ID: "ZTF18abcqncg", SepArc: 0.2},
				{Source: sourceFink, ID: "ZTF18adknimm", Class: finkUnknown, SepArc: 0.39},
			},
			neighbors: []Variable{{Name: "ZTF J210433.85+431936.7", Type: "BY", RA: 316.141042, Dec: 43.326861, Catalog: catalogVSX}},
		},
	}
}

func recordedZTF(t *testing.T, name string) ZTFLookup {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "ztflc", "testdata", name+".csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pts, err := ztflc.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return func(_ context.Context, _, _, amp float64) (ztflc.Verdict, error) {
		return ztflc.Judge(pts, amp), nil
	}
}

// TestVettedEclipsesAreNewVariables replays the three eclipsing binaries
// vetted as new in October 2026 through the classes, each seen fainter on
// one night only: the brokers' ZTF objects at the stars are their own
// variability, their ZTF light curves confirm the change, and no catalogued
// neighbor explains it.
func TestVettedEclipsesAreNewVariables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, e := range vettedEclipses() {
		t.Run(e.name, func(t *testing.T) {
			t.Parallel()
			k := Known{Variables: e.neighbors, Galaxies: e.galaxies, ZTF: recordedZTF(t, e.ztf)}
			alerts := func(context.Context, float64, float64, float64) ([]Alert, []string, []string) {
				return e.alerts, []string{sourceAlerce, sourceFink}, nil
			}
			ls, _, checked, _ := Classify(ctx, &Result{Candidates: []Candidate{e.c}}, k, alerts)
			l := ls[0]
			if l.Class != ClassNewVariable {
				t.Fatalf("%s %+v", l.Class, l.Matches)
			}
			ztf := false
			for _, m := range l.Matches {
				ztf = ztf || (m.Catalog == catalogZTF && m.Type == "variable")
			}
			if !ztf || !IsNew(l.Class) {
				t.Errorf("no ZTF confirmation: %+v, checked %v", l.Matches, checked)
			}
			k.ZTF = func(context.Context, float64, float64, float64) (ztflc.Verdict, error) {
				return ztflc.Verdict{Kind: ztflc.Flat, Amplitude: 0.03, Band: "r"}, nil
			}
			if ls, _, _, _ := Classify(ctx, &Result{Candidates: []Candidate{e.c}}, k, alerts); ls[0].Class != ClassLowConfidence {
				t.Errorf("flat in ZTF: %s", ls[0].Class)
			}
		})
	}
}

func TestBrokerObjectMakesOnlyANewSourceAKnownTransient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g := 16.0
	alerts := func(context.Context, float64, float64, float64) ([]Alert, []string, []string) {
		return []Alert{{Source: sourceAlerce, ID: "ZTF18aaaaaaa", SepArc: 0.3}}, []string{sourceAlerce}, nil
	}
	newSource := Candidate{RA: 184.3847, Dec: 13.8363, Sign: 1, SNR: 20, SubsTested: 20}
	if ls, _, _, _ := Classify(ctx, &Result{Candidates: []Candidate{newSource}}, Known{}, alerts); ls[0].Class != ClassKnownTransient {
		t.Errorf("a new source at a broker object: %s", ls[0].Class)
	}
	onStar := newSource
	onStar.GaiaG = &g
	if ls, _, _, _ := Classify(ctx, &Result{Candidates: []Candidate{onStar}}, Known{}, alerts); ls[0].Class != ClassLowConfidence {
		t.Errorf("an unconfirmed change on a star at a broker object: %s", ls[0].Class)
	}
	tns := func(context.Context, float64, float64, float64) ([]Alert, []string, []string) {
		return []Alert{{Source: sourceTNS, ID: "AT 2024aiji", SepArc: 0.3}}, []string{sourceTNS}, nil
	}
	if ls, _, _, _ := Classify(ctx, &Result{Candidates: []Candidate{onStar}}, Known{}, tns); ls[0].Class != ClassKnownTransient {
		t.Errorf("a change on a star TNS lists: %s", ls[0].Class)
	}
}
