package discoveries

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// Candidate 13267, Andromeda's 2026-10-11 night, is AT 2026acsi, a nova in
// M 31 (TNS); the night's run called it a supernova candidate.
func TestNewStarInM31IsANovaCandidate(t *testing.T) {
	t.Parallel()
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	objs, err := ix.Cone(ctx, 10.679274962197383, 41.26270118660898, 1.7)
	if err != nil {
		t.Fatal(err)
	}
	res := &Result{Candidates: []Candidate{{RA: 10.557966104327647, Dec: 41.27670411346332, Sign: 1, SNR: 10.82}}}
	ls := classifyChanges(ctx, t, res, Known{Galaxies: galaxiesOf(objs)})
	if ls[0].Class != ClassNova {
		t.Fatalf("AT 2026acsi in M 31 classed %s, matches %+v", ls[0].Class, ls[0].Matches)
	}
	if !IsNew(ClassNova) || !counted(app.DiscoveryCandidate{Kind: app.DiscoveryKindChange, Class: ls[0].Class}) {
		t.Error("a nova candidate is new and counts in the morning report")
	}
	objs, err = ix.Cone(ctx, 202.4696, 47.1952, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	res = &Result{Candidates: []Candidate{{RA: 202.4750, Dec: 47.1990, Sign: 1, SNR: 12}}}
	ls = classifyChanges(ctx, t, res, Known{Galaxies: galaxiesOf(objs)})
	if ls[0].Class != ClassSupernova {
		t.Errorf("a new star in M 51 classed %s, matches %+v", ls[0].Class, ls[0].Matches)
	}
}

func TestLocalGroupGalaxies(t *testing.T) {
	t.Parallel()
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for d, want := range map[string]bool{"M 31": true, "M 33": true, "M 32": true, "M 110": true, "NGC 6822": true, "IC 1613": true, "NGC 185": true,
		"M 51": false, "M 81": false, "NGC 891": false} {
		o, ok := ix.Lookup(d)
		if !ok {
			t.Errorf("%s not in the catalogue", d)
			continue
		}
		if got := isLocalGroup(o.Designation, o.Aliases); got != want {
			t.Errorf("%s in the Local Group: %v", d, got)
		}
	}
}
