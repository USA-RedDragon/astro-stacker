package vetting

import (
	"encoding/csv"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// atlasFixture reads ATLAS forced photometry kept as mjd, uJy, duJy,
// filter, camera and chi/N, of the rows that pass every cut but chi/N.
func atlasFixture(t *testing.T, name string) []surveys.ATLASRow {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	text, err := unzstd(raw)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(string(text))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	num := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	rows := make([]surveys.ATLASRow, 0, len(recs)-1)
	for _, r := range recs[1:] {
		rows = append(rows, surveys.ATLASRow{MJD: num(r[0]), UJy: num(r[1]), DUJy: num(r[2]), Filter: r[3], Obs: r[4], ChiN: num(r[5])})
	}
	return rows
}

// TestATLASFailedSubtraction41445 is 41445 in the Rosette Nebula, a
// change in H-a on 2025-02-28 at G-equivalent 17.26. ATLAS's o frames of
// 2025-02-26 measured 106 and 87 microjansky in the two that passed the
// chi/N cut and 1420 and 3193 at chi/N 814 and 2720 in the two that did
// not; its c frames 12 hours later measured nothing. ATLAS must not
// confirm it, from the night's cut frames or, with those dropped as the
// vet once did, from the c band.
func TestATLASFailedSubtraction41445(t *testing.T) {
	t.Parallel()
	rows := atlasFixture(t, "atlas_41445.csv.zst")
	fx := transientFacts{flagged: []time.Time{nightDate("2025-02-28")}, atlas: surveys.Fetch{State: surveys.Done}, rows: rows,
		reps: map[string]reported{"H-a\x002025-02-28": {mag: 17.26, ok: true}}}
	c := atlasCheckTransient(fx)
	t.Logf("%s: %s", c.Status, c.Evidence)
	if c.Status == check.Pass {
		t.Errorf("ATLAS confirmed light from a night whose subtraction failed: %s", c.Evidence)
	}
	var feb26 *atlasNight
	for _, n := range atlasLevels(rows, fx.flagged) {
		if n.band == "o" && n.mjd > 60732 && n.mjd < 60732.5 {
			feb26 = &n
		}
	}
	if feb26 == nil || feb26.cut != 2 || feb26.count != 2 || !feb26.failed(1) {
		t.Fatalf("2025-02-26 o is not a failed night: %+v", feb26)
	}
	fx.rows = nil
	for _, r := range rows {
		if !r.ChiCut() {
			fx.rows = append(fx.rows, r)
		}
	}
	c = atlasCheckTransient(fx)
	t.Logf("rows cut for chi/N dropped: %s: %s", c.Status, c.Evidence)
	if c.Status == check.Pass {
		t.Errorf("ATLAS confirmed o light its c band 12 hours later did not show: %s", c.Evidence)
	}
}

// TestATLASOutburst23991 is the 2025-05 outburst of Gaia DR3
// 6050104169683139072 (source 23991), first in the user's frames on
// 2025-05-20 and seen by ATLAS at 145 microjansky in c on 2025-05-23, an
// outburst published from these data. ATLAS must still confirm it.
func TestATLASOutburst23991(t *testing.T) {
	t.Parallel()
	fx := transientFacts{flagged: []time.Time{nightDate("2025-05-20")}, atlas: surveys.Fetch{State: surveys.Done}, rows: atlasFixture(t, "atlas_23991.csv.zst"),
		reps: map[string]reported{discoveries.GroupBroadband + "\x002025-05-20": {mag: 18.4, ok: true}}}
	c := atlasCheckTransient(fx)
	t.Logf("%s: %s", c.Status, c.Evidence)
	if c.Status != check.Pass {
		t.Errorf("ATLAS did not confirm a real outburst: %s %s", c.Status, c.Evidence)
	}
}
