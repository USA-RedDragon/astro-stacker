package discoverysource_test

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	ic1396  = "IC 1396 Panel 1"
	nan2    = "North America Nebula Panel 2"
	icNight = "2025-12-08"
	nNight  = "2025-12-17"
	m31Ecl  = "2026-10-11"
)

// eclipseRows are the eclipses of the three new eclipsing binaries at
// their catalog positions; the IC 1396 star's blend with its neighbor
// moves the broadband spot 1.2″ toward the neighbor.
func eclipseRows() []row {
	return []row{
		{30001, ic1396, icNight, bb, discoveries.ClassNewVariable, 323.27735, 58.63452, -1, 9.1, 0.13, ptr(14.50), 26, 26},
		{30002, ic1396, icNight, ha, discoveries.ClassNewVariable, 323.27705, 58.63432, -1, 6.2, 0.17, nil, 8, 9},
		{30003, nan2, nNight, bb, discoveries.ClassNewVariable, 316.161804, 43.320665, -1, 8.4, 0.19, nil, 7, 7},
		{30004, andromed, m31Ecl, bb, discoveries.ClassNewVariable, 10.889321, 41.662433, -1, 106, 0.31, ptr(15.043443), 28, 28},
	}
}

func eclipseRuns(t *testing.T, db *gorm.DB) {
	t.Helper()
	var runs []app.DiscoveryRun
	add := func(object, night, group string) {
		runs = append(runs, app.DiscoveryRun{Object: object, Night: day(night), Group: group, State: app.DiscoveryDone})
	}
	for _, n := range []string{"2025-11-30", icNight, "2025-12-20", "2026-01-05"} {
		add(ic1396, n, bb)
	}
	add(ic1396, icNight, ha)
	for _, n := range []string{nNight, "2025-12-19", "2025-12-22"} {
		add(nan2, n, bb)
	}
	if err := db.Create(&runs).Error; err != nil {
		t.Fatal(err)
	}
}

func TestEclipsingBinariesAreSourcesWithTheirNights(t *testing.T) {
	t.Parallel()
	db := open(t)
	eclipseRuns(t, db)
	store(t, db, append(realRows()[:1], eclipseRows()...))
	got := all(t, db)
	ic, nan, m31 := got[30001], got[30003], got[30004]
	if got[30002].Source.ID != ic.Source.ID {
		t.Fatalf("IC 1396 broadband and H-a spots are sources %d and %d", ic.Source.ID, got[30002].Source.ID)
	}
	if m31.Source.ID == got[18554].Source.ID {
		t.Fatal("the M 31 eclipsing binary joined the nova")
	}
	cases := []struct {
		name           string
		s              discoverysource.Summary
		members        int
		seen, compared int
		first          string
	}{
		{"IC 1396", ic, 2, 1, 3, icNight},
		{"North America", nan, 1, 1, 3, nNight},
		{"Andromeda", m31, 1, 1, 1, m31Ecl},
	}
	for _, c := range cases {
		s := c.s
		if len(s.Members) != c.members || s.Seen != c.seen || s.Compared != c.compared || s.First.Format(time.DateOnly) != c.first ||
			s.Class != discoveries.ClassNewVariable || !s.Look() {
			t.Errorf("%s: %d members, seen %d of %d from %s, class %s, look %v", c.name, len(s.Members), s.Seen, s.Compared,
				s.First.Format(time.DateOnly), s.Class, s.Look())
		}
	}
}

func TestARangeCountsTheEarlierSourcesThatNeedALook(t *testing.T) {
	t.Parallel()
	checkARangeCountsTheEarlierSourcesThatNeedALook(t, open(t))
}

func checkARangeCountsTheEarlierSourcesThatNeedALook(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	eclipseRuns(t, db)
	store(t, db, append(realRows()[:1], eclipseRows()...))
	since := ptrDay("2026-09-15")
	cases := []struct {
		name string
		f    discoverysource.Filter
		want int
	}{
		{"all targets", discoverysource.Filter{Since: since, View: discoverysource.ViewLook}, 2},
		{"one target", discoverysource.Filter{Since: since, Object: nan2}, 1},
		{"position", discoverysource.Filter{Since: since, Search: "21h04m38.8s +43°19′14″"}, 1},
		{"in range", discoverysource.Filter{Since: since, Object: andromed}, 0},
		{"all time", discoverysource.Filter{}, 0},
		{"until", discoverysource.Filter{Since: since, Until: ptrDay("2026-10-14")}, 0},
	}
	for _, c := range cases {
		_, counts, err := discoverysource.List(ctx, db, c.f)
		if err != nil {
			t.Fatal(err)
		}
		if counts.LookEarlier != c.want {
			t.Errorf("%s: %d earlier, want %d", c.name, counts.LookEarlier, c.want)
		}
	}
	list, counts, err := discoverysource.List(ctx, db, discoverysource.Filter{Since: since, View: discoverysource.ViewLook})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || counts.Look != 2 {
		t.Errorf("in range: %d listed, %d need a look", len(list), counts.Look)
	}
	ic := all(t, db)[30001]
	if _, err := discoverysource.Vet(ctx, db, ic.Source.ID, "real", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, counts, err = discoverysource.List(ctx, db, discoverysource.Filter{Since: since}); err != nil || counts.LookEarlier != 1 {
		t.Errorf("after vetting: %d earlier, %v", counts.LookEarlier, err)
	}
}
