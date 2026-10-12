package horizons

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

func TestCacheKeyIsTheOneBeforeQuantity27(t *testing.T) {
	t.Parallel()
	elev := 300.0
	r := Request{Command: "DES=161P;CAP;NOFRAG", Site: sky.Site{Latitude: 32.5, Longitude: -97.25, Elevation: &elev},
		Start: time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC), Stop: time.Date(2026, 10, 13, 12, 30, 0, 0, time.UTC), Step: 10 * time.Minute}
	if got, want := r.key(), "a7c6c6f34b5402e58fb546cd1a2a2c2988a176cff769c3c9a08a6d5d1fa5f2c3"; got != want {
		t.Fatalf("key %s, want the key computed before quantity 27 was added %s", got, want)
	}
	if q := r.Values().Get("QUANTITIES"); q != "'1,2,3,9,20,27,36'" {
		t.Fatalf("QUANTITIES %s", q)
	}
}
