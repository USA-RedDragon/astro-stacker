package morningreport

import (
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/claimswatch"
)

func TestClaimsLine(t *testing.T) {
	t.Parallel()
	loc, _ := time.LoadLocation("America/Chicago")
	f := Formatter{Loc: loc}
	if f.claims(Report{}) != "" {
		t.Fatal("a report with no new mentions has a claims line")
	}
	added := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	rec := &claimswatch.Record{Name: "Zenodo", Published: time.Date(2026, 10, 21, 0, 12, 24, 0, time.UTC)}
	r := Report{ClaimStars: 31, Claims: []claimswatch.NewMention{{
		MentionView: claimswatch.MentionView{SourceID: 2289, Label: "VSX", Title: "Gaia DR3 2012823714478462848", Dated: &added, URL: "https://vsx.aavso.org/x"},
		StarName:    "Gaia DR3 2012823714478462848", Record: rec}}}
	got := f.claims(r)
	for _, want := range []string{"<b>Your discoveries</b>", "2289 (Gaia DR3 2012823714478462848) is in VSX", "dated Oct 23", "Your Zenodo record is dated Oct 20", "The other 30 stars have no new mentions."} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
