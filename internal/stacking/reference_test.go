package stacking

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func cand(id int, hfr float64, stars int, score float64) candidate {
	return candidate{frame: app.Frame{ID: id}, score: quality.SubScore{HFR: hfr, Stars: stars, Score: score}}
}

func TestPickReferencePrefersSharpStars(t *testing.T) {
	t.Parallel()
	got, ok := pickReference([]candidate{
		cand(1, 4.5, 2000, 1), // soft, best score
		cand(2, 2.5, 1900, 0.8),
		cand(3, 1.9, 600, 0.4), // sharp but hazy: too few stars
	})
	if !ok || got.frame.ID != 2 {
		t.Fatalf("picked %d, want 2", got.frame.ID)
	}
}

func TestPickReferenceWithoutStarData(t *testing.T) {
	t.Parallel()
	got, ok := pickReference([]candidate{cand(1, 0, 0, 0.5), cand(2, 0, 0, 0.9)})
	if !ok || got.frame.ID != 2 {
		t.Fatalf("picked %d, want 2", got.frame.ID)
	}
	if _, ok := pickReference(nil); ok {
		t.Fatal("picked a reference from no subs")
	}
}
