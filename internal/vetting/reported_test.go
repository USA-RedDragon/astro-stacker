package vetting

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

// TestAReportedTransientOutranksAContaminatedReference checks AT 2025aesw:
// bright for days, its own light went into the reference, which the image
// checks read as a contaminated reference; TNS knows it, so it is known.
func TestAReportedTransientOutranksAContaminatedReference(t *testing.T) {
	t.Parallel()
	known := Known{Listed: "reported to TNS as AT 2025aesw", Reported: "reported to TNS as AT 2025aesw"}
	s := imageSuggest(nil, ownimage.Flags{RefContaminated: true}, known, check.Check{})
	if s == nil || s.State != app.VetKnown {
		t.Fatalf("got %+v", s)
	}
}
