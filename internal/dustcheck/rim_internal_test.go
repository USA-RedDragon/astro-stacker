package dustcheck

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	rimM15        = "M 15 H-a"
	rimTriangulum = "Triangulum Luminance"
	rimSculptorL  = "Sculptor Luminance"
)

// rimShadows are the two window shadows of the 2026-10-22 Red flat's
// catalog whose rims the crops in testdata/rim hold.
func rimShadows() []Blob {
	return []Blob{
		{X: 2626, Y: 2016, Diameter: 699, Peak: -1.515, Disc: true},
		{X: 4819, Y: 2488, Diameter: 547, Peak: -0.422, Disc: true},
	}
}

func loadRim(t *testing.T, frame string) (Map, Map) {
	t.Helper()
	return loadRimMap(t, frame+".sub.zst"), loadRimMap(t, frame+".tmpl.zst")
}

func loadRimMap(t *testing.T, name string) Map {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "rim", name))
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeSub(b)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// rimFree is stacking.RimFreeDepth: a streak no deeper costs nothing.
const rimFree = 0.25

func flagged(r Rim, ok bool) bool {
	return ok && -r.Depth > rimFree && r.Z >= RimMinZ
}

func logRim(t *testing.T, frame, what string, r Rim, ok bool) {
	t.Helper()
	t.Logf("%s %-22s measured %v depth %+.2f%% z %4.1f angle %3.0f radius %d shadow %d structure %.2f%%",
		frame, what, ok, r.Depth, r.Z, r.Angle, r.Radius, r.X, r.Structure)
}

func TestRimStreakMoonlitNight(t *testing.T) {
	t.Parallel()
	moonlit := map[string]string{
		"27871": "Sculptor Blue", "27873": "Sculptor Red", "27874": "Sculptor Green",
		"27881": "Sculptor Red", "27882": "Sculptor Green", "27883": "Sculptor Blue",
		"27860": rimM15, "27862": rimM15, "27864": rimM15, "27866": rimM15, "27868": rimM15,
	}
	side, told := 0, 0
	for frame, what := range moonlit {
		sub, tmpl := loadRim(t, frame)
		r, ok := RimStreak(sub, tmpl, rimShadows())
		logRim(t, frame, what, r, ok)
		if !ok {
			t.Fatalf("%s %s: no rim measured", frame, what)
		}
		if r.Depth > -0.3 || r.Depth < -1.0 {
			t.Errorf("%s %s: depth %.2f%%, want -0.3%% to -1.0%%", frame, what, r.Depth)
		}
		if r.Angle <= 60 || r.Angle >= 300 {
			side++
		}
		if flagged(r, ok) {
			told++
		}
	}
	if side < len(moonlit) {
		t.Errorf("%d of %d streaks on the +x side of their shadow, want all", side, len(moonlit))
	}
	if told < len(moonlit)-3 {
		t.Errorf("%d of %d streaks stand out, want all but three", told, len(moonlit))
	}
}

func TestRimStreakOnTargetStructure(t *testing.T) {
	t.Parallel()
	on := map[string]string{
		"26822": "Andromeda H-a", "27512": "Garlic Nebula Red",
		"27889": "Triangulum Green", "27891": rimTriangulum, "27895": rimTriangulum,
		"27899": rimTriangulum, "27903": rimTriangulum, "27915": rimTriangulum,
	}
	for frame, what := range on {
		sub, tmpl := loadRim(t, frame)
		r, ok := RimStreak(sub, tmpl, rimShadows())
		logRim(t, frame, what, r, ok)
		if flagged(r, ok) && r.X == 2626 {
			t.Errorf("%s %s: %.2f%% at z %.1f told on the double shadow, which lies on the target", frame, what, r.Depth, r.Z)
		}
		plain, pok := RimStreak(sub, Map{}, rimShadows())
		logRim(t, frame, what+" plain", plain, pok)
	}
	for _, frame := range []string{"26822", "27512"} {
		sub, tmpl := loadRim(t, frame)
		if r, ok := RimStreak(sub, tmpl, rimShadows()); flagged(r, ok) {
			t.Errorf("%s: %.2f%% at z %.1f told on a target's own structure", frame, r.Depth, r.Z)
		}
	}
	for _, frame := range []string{"26822", "27891"} {
		sub, _ := loadRim(t, frame)
		if r, ok := RimStreak(sub, Map{}, rimShadows()); !ok || -r.Depth <= rimFree {
			t.Errorf("%s: without its template the target's structure no longer dips like a streak (%.2f%%); the test no longer tests the template", frame, r.Depth)
		}
	}
}

func TestRimStreakDarkNight(t *testing.T) {
	t.Parallel()
	dark := map[string]string{
		"26400": rimM15, "26402": rimSculptorL, "26404": rimSculptorL,
		"26406": rimSculptorL, "26407": rimSculptorL, "26408": "NGC 752 Red", "26340": "Barnard's E Blue",
	}
	for frame, what := range dark {
		sub, tmpl := loadRim(t, frame)
		r, ok := RimStreak(sub, tmpl, rimShadows())
		logRim(t, frame, what, r, ok)
		if flagged(r, ok) {
			t.Errorf("%s %s: depth %.2f%% at z %.1f on a dark night", frame, what, r.Depth, r.Z)
		}
	}
}
