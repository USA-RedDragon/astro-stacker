package dustcheck_test

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/dustcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

type studyMaster struct {
	Key     string  `json:"key"`
	DateObs string  `json:"DATE-OBS"`
	Filter  string  `json:"FILTER"`
	Rotator float64 `json:"ROTATOR"`
}

// readNPY reads a 2-D little-endian float32 .npy written by the study, which
// read FITS bottom row first, and returns it top row first.
func readNPY(t *testing.T, path string) dustcheck.Map {
	t.Helper()
	return readNPYRows(t, path, true)
}

func readNPYRows(t *testing.T, path string, flip bool) dustcheck.Map {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hl := int(binary.LittleEndian.Uint16(b[8:10]))
	hdr := string(b[10 : 10+hl])
	var h, w int
	if _, err := fmt.Sscanf(hdr[strings.Index(hdr, "'shape': (")+len("'shape': ("):], "%d, %d", &h, &w); err != nil {
		t.Fatal(err)
	}
	data := b[10+hl:]
	m := dustcheck.NewMap(w, h)
	for y := range h {
		src := y
		if flip {
			src = h - 1 - y
		}
		for x := range w {
			m.Pix[y*w+x] = math.Float32frombits(binary.LittleEndian.Uint32(data[(src*w+x)*4:]))
		}
	}
	return m
}

func studyFlats(t *testing.T, dir string) []dustcheck.Flat {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "data", "masters.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ms []studyMaster
	if err := json.Unmarshal(b, &ms); err != nil {
		t.Fatal(err)
	}
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].DateObs < ms[j].DateObs })
	seen := map[string]bool{}
	var flats []dustcheck.Flat
	for i, m := range ms {
		k := fmt.Sprintf("%s|%s|%d", m.Filter, m.DateObs[:16], int(math.RoundToEven(m.Rotator)))
		if seen[k] {
			continue
		}
		seen[k] = true
		at, err := time.Parse("2006-01-02T15:04:05.999999", m.DateObs)
		if err != nil {
			t.Fatal(err)
		}
		r := readNPY(t, filepath.Join(dir, "maps", m.Key+".npy"))
		flats = append(flats, dustcheck.Flat{ID: i + 1, Filter: m.Filter, Rotator: m.Rotator, TakenAt: at, Map: dustcheck.Prepare(r)})
	}
	return flats
}

// TestStudyHistory replays the flat history the detector was tuned on. It
// needs the study's maps: DUSTCHECK_STUDY names its folder.
func TestStudyHistory(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("DUSTCHECK_STUDY")
	if dir == "" {
		t.Skip("DUSTCHECK_STUDY not set")
	}
	flats := studyFlats(t, dir)
	changes := map[string][]dustcheck.Located{}
	checked := map[string]map[string]bool{}
	sameAngle, sameAngleChanges := 0, 0
	window := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	total := 0
	for i, cur := range flats {
		prev, ok := dustcheck.Previous(cur, flats[:i])
		if !ok {
			continue
		}
		night := cur.TakenAt.Add(-12 * time.Hour).Format(time.DateOnly)
		if checked[night] == nil {
			checked[night] = map[string]bool{}
		}
		checked[night][cur.Filter] = true
		evs := dustcheck.Compare(prev, cur)
		rm := dustcheck.AngleDiff(prev.Rotator, cur.Rotator) <= dustcheck.RotationTolerance
		if rm && !prev.TakenAt.Before(window) {
			sameAngle++
		}
		for _, e := range evs {
			t.Logf("%-9s %s %5.1f -> %s %5.1f %-8s %4d %4d d%4d %+.3f%% edge %.2f sharp %.2f",
				cur.Filter, prev.TakenAt.Format("2006-01-02T15:04"), prev.Rotator, cur.TakenAt.Format("2006-01-02T15:04"), cur.Rotator,
				e.Class, e.X, 4172-e.Y, e.Diameter, e.Peak, e.Edge, e.Sharp)
			if !dustcheck.Change(e.Class) {
				continue
			}
			total++
			changes[night] = append(changes[night], dustcheck.Located{Filter: cur.Filter, Event: e})
			if rm && !prev.TakenAt.Before(window) {
				sameAngleChanges++
			}
		}
	}
	t.Logf("changes %d, same-angle pairs since 2026-09-29: %d with %d changes", total, sameAngle, sameAngleChanges)
	if sameAngleChanges != 0 {
		t.Errorf("same-angle pairs since 2026-09-29 gave %d changes, want 0", sameAngleChanges)
	}
	if total != 17 {
		t.Errorf("history gave %d changes, want 17", total)
	}
	dawn := dustcheck.GroupChanges(changes["2026-09-28"], len(checked["2026-09-28"]), nil)
	for _, g := range dawn {
		m := g.Members[0]
		t.Logf("2026-09-29 dawn: %s %s at %d,%d in %v", g.Scope, m.Class, m.X, 4172-m.Y, g.Filters())
	}
	want := [][2]int{{2622, 2160}, {4825, 1685}, {5525, 855}}
	if len(dawn) != len(want) {
		t.Fatalf("2026-09-29 dawn gave %d changes, want %d", len(dawn), len(want))
	}
	for _, w := range want {
		found := false
		for _, g := range dawn {
			m := g.Members[0]
			if g.Scope == dustcheck.ScopeWindow && m.Class == dustcheck.ClassNew &&
				math.Hypot(float64(m.X-w[0]), float64(4172-m.Y-w[1])) < 40 {
				found = true
			}
		}
		if !found {
			t.Errorf("2026-09-29 dawn: no new window shadow at %v", w)
		}
	}
}

// TestStudyDustMap checks DustMap against the study's maps of the masters in
// DUSTCHECK_STUDY/masters, read through imagedata.
func TestStudyDustMap(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("DUSTCHECK_STUDY")
	if dir == "" {
		t.Skip("DUSTCHECK_STUDY not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "masters", "*.fit"))
	if len(files) == 0 {
		t.Skip("no masters")
	}
	maps := map[string]dustcheck.Map{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		im, err := imagedata.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		got := dustcheck.DustMap(im.Plane(0), im.W, im.H)
		took := time.Since(start)
		key := strings.TrimSuffix(filepath.Base(f), ".fit")
		want := readNPY(t, filepath.Join(dir, "maps", key+".npy"))
		maps[key] = got
		var worst, sum float64
		n := 0
		for y := dustcheck.Border; y < got.H-dustcheck.Border; y++ {
			for x := dustcheck.Border; x < got.W-dustcheck.Border; x++ {
				d := math.Abs(float64(got.At(x, y) - want.At(x, y)))
				worst = max(worst, d)
				sum += d
				n++
			}
		}
		gs, ws := dustcheck.Prepare(got), dustcheck.Prepare(want)
		var sworst float64
		for i, ok := range gs.Mask {
			if ok {
				sworst = max(sworst, math.Abs(float64(gs.Pix[i]-ws.Pix[i])))
			}
		}
		t.Logf("%s: %v, raw map off by %.5f%% at most, %.6f%% on average; smoothed %.5f%% at most",
			key[:8], took.Round(time.Millisecond), worst*100, sum/float64(n)*100, sworst*100)
		if sworst > 0.0003 {
			t.Errorf("%s: smoothed map off by %.4f%%, want under 0.03%%", key[:8], sworst*100)
		}
	}
}

// TestStudyLights checks Residual against the study's lights map of the
// week from 2026-06-08, whose raw lights are in DUSTCHECK_STUDY/lights/w0608.
func TestStudyLights(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("DUSTCHECK_STUDY")
	if dir == "" {
		t.Skip("DUSTCHECK_STUDY not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "lights", "w0608", "*.xisf"))
	if len(files) == 0 {
		t.Skip("no lights")
	}
	subs := make([]dustcheck.Map, 0, len(files))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		im, err := imagedata.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		pix := im.Plane(0)
		for i := range pix {
			pix[i] -= 502.0 / 65535
		}
		subs = append(subs, subMap(t, pix, im.W, im.H))
	}
	start := time.Now()
	got, err := dustcheck.Residual(subs)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	want := readNPYRows(t, filepath.Join(dir, "maps", "light_w2026-06-08.npy"), false)
	gs, ws := dustcheck.Prepare(got), dustcheck.Prepare(want)
	var worst, sum float64
	n := 0
	for i, ok := range gs.Mask {
		if ok {
			d := math.Abs(float64(gs.Pix[i] - ws.Pix[i]))
			worst = max(worst, d)
			sum += d
			n++
		}
	}
	t.Logf("%d subs in %v; smoothed residual off by %.4f%% at most, %.5f%% on average", len(subs), took.Round(time.Millisecond), worst*100, sum/float64(n)*100)
	for _, d := range dustcheck.ResidualDiscs(gs) {
		t.Logf("residual disc at %d,%d d%d %+.2f%% edge %.2f sharp %.2f", d.X, d.Y, d.Diameter, d.Peak, d.Edge, d.Sharp)
	}
	t.Logf("sigma %.4f%%", dustcheck.Sigma(gs)*100)
	for _, d := range dustcheck.ResidualDiscs(ws) {
		if d.Diameter >= 300 {
			t.Logf("study disc at %d,%d d%d %+.2f%% edge %.2f sharp %.2f", d.X, d.Y, d.Diameter, d.Peak, d.Edge, d.Sharp)
		}
	}
	if worst > 0.001 {
		t.Errorf("residual off by %.3f%%, want under 0.1%%", worst*100)
	}
	discs := dustcheck.ResidualDiscs(gs)
	if len(discs) != 1 || discs[0].Peak > -1 || math.Hypot(float64(discs[0].X-2700), float64(discs[0].Y-2050)) > 150 {
		t.Errorf("residual discs %+v, want only the window dust near 2700,2050", discs)
	}
}
