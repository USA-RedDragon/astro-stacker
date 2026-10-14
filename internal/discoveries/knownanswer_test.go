package discoveries_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// TestKnownAnswerIC3393 reruns the method's trial: IC 3393 Panel 1,
// Luminance, the night of 2025-02-28 against the field's 6 other nights.
// The trial found 13 VSX variables among the changes and linked all 17
// known asteroids brighter than V 19.5. The data (1.4 GB of registered
// subs) is not in the repository: set DISCOVERIES_KAT_DIR to a directory
// with L/*.xisf, master_L.fit, subs.tsv, wcs.json, gaia.tsv, vsx.tsv and
// skybot.txt.
func TestKnownAnswerIC3393(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("DISCOVERIES_KAT_DIR")
	if dir == "" {
		t.Skip("DISCOVERIES_KAT_DIR not set")
	}
	ctx := context.Background()
	master := decode(t, filepath.Join(dir, "master_L.fit"))
	wcs := readWCS(t, filepath.Join(dir, "wcs.json"), master.H)
	night := time.Date(2025, 2, 28, 0, 0, 0, 0, time.UTC)
	f := discoveries.FilterInput{Filter: "Luminance", Anchors: discoveries.Anchors(master)}
	for _, s := range readSubs(t, filepath.Join(dir, "subs.tsv")) {
		if s.Filter != "Luminance" {
			continue
		}
		if s.Night.Equal(night) {
			f.Night = append(f.Night, s)
		} else {
			f.Ref = append(f.Ref, s)
		}
	}
	in := discoveries.Input{W: master.W, H: master.H, WCS: wcs, Filters: []discoveries.FilterInput{f}, Gaia: readGaia(t, filepath.Join(dir, "gaia.tsv")), Threads: 4,
		Load: func(_ context.Context, s discoveries.Sub) (*discoveries.Plane, error) {
			return decode(t, filepath.Join(dir, "L", filepath.Base(s.Key))), nil
		},
		Progress: func(s string) { t.Log(s) }}
	start := time.Now()
	res, err := discoveries.Run(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("took %s: night %d subs, reference %d; hfr %.2f/%.2f convolved %s, kernel tiles %d sum %.3f; apsig %.3g; zp %v depth %v sub depth %v; astrometry rms %v from %d stars",
		time.Since(start).Round(time.Second), res.NightSubs, res.RefSubs, res.HFRNight, res.HFRRef, res.Convolved, res.KernelTiles, res.KernelSum, res.ApSigma,
		deref(res.ZeroPoint), deref(res.Depth), deref(res.SubDepth), deref(res.AstroRMSArcsec), res.AstroStars)
	for k, v := range res.Funnel {
		t.Logf("%s: %+v", k, v)
	}
	vsxB, err := os.ReadFile(filepath.Join(dir, "vsx.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	vsx := parseVSX(t, string(vsxB))
	sky, err := os.ReadFile(filepath.Join(dir, "skybot.txt"))
	if err != nil {
		t.Fatal(err)
	}
	bodies, err := discoveries.ParseSkyBoT(sky)
	if err != nil {
		t.Fatal(err)
	}
	epoch := time.Unix(0, int64((2460735.844356-2440587.5)*86400e9)).UTC()
	labelled, tracks, _, _ := discoveries.Classify(ctx, res, discoveries.Known{Variables: vsx, Bodies: bodies, Epoch: epoch}, nil)
	counts := map[string]int{}
	for _, l := range labelled {
		counts[l.Class]++
		name := ""
		for _, m := range l.Matches {
			name += m.Catalog + ":" + m.Name + " "
		}
		t.Logf("%-24s sign %+d snr %6.1f x %7.1f y %7.1f gaia %v dmag %v present %d/%d top %.2f %s", l.Class, l.Sign, l.SNR, l.X, l.Y, deref(l.GaiaG), deref(l.DeltaMag), l.PresentIn, l.SubsTested, l.TopShare, name)
		if l.Class == discoveries.ClassNewVariable {
			for _, p := range l.Light {
				t.Logf("   %s flux %.4g err %.4g", p.Time.Format("15:04"), p.Flux, p.FluxErr)
			}
		}
	}
	t.Logf("classes: %v", counts)
	inField, recovered := 0, 0
	matched := map[string]bool{}
	for _, tr := range tracks {
		if tr.Match != nil {
			matched[tr.Match.Name] = true
		}
		t.Logf("track %-14s n=%2d rate %.2f\"/min V %v offset %v", tr.Class, len(tr.Points), tr.RateArcmin, deref(tr.BodyV), deref(tr.OffsetArc))
	}
	for _, b := range bodies {
		if b.V >= 19.5 {
			continue
		}
		ra, dec := b.At(res.MidTime.Sub(epoch))
		x, y, ok := wcs.PixelOf(ra, dec)
		if !ok || x < 0 || y < 0 || int(x) >= res.Diff.W || int(y) >= res.Diff.H || res.Edge[int(y)*res.Diff.W+int(x)] {
			continue
		}
		inField++
		name := b.Name
		if b.Number != "" {
			name = "(" + b.Number + ") " + b.Name
		}
		if matched[name] {
			recovered++
		} else {
			t.Logf("missed %s V %.1f at %.0f,%.0f", name, b.V, x, y)
		}
	}
	t.Logf("known asteroids V<19.5 in field %d, linked %d", inField, recovered)
	if counts[discoveries.ClassKnownVariable] < 13 {
		t.Errorf("found %d known variables, the trial found 13", counts[discoveries.ClassKnownVariable])
	}
	if inField == 0 || recovered < inField-1 {
		t.Errorf("linked %d of %d known asteroids brighter than V 19.5, the trial linked all", recovered, inField)
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return strconv.FormatFloat(*p, 'f', 3, 64)
}

func decode(t *testing.T, path string) *discoveries.Plane {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return discoveries.PlaneOf(im)
}

func readWCS(t *testing.T, path string, h int) *discoveries.WCS {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cards []struct{ Name, Value string }
	if err := json.Unmarshal(b, &cards); err != nil {
		t.Fatal(err)
	}
	kw := map[string]string{}
	for _, c := range cards {
		kw[c.Name] = c.Value
	}
	w, err := discoveries.ParseWCS(kw, h)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func readSubs(t *testing.T, path string) []discoveries.Sub {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var out []discoveries.Sub
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 7 {
			continue
		}
		id, _ := strconv.Atoi(f[0])
		night, _ := time.Parse(time.DateOnly, f[2])
		start, _ := time.Parse(time.RFC3339, f[3])
		exp, _ := strconv.ParseFloat(f[4], 64)
		w, _ := strconv.ParseFloat(f[5], 64)
		out = append(out, discoveries.Sub{FrameID: id, Filter: f[1], Night: night, Start: start, Exposure: exp, Weight: w, Key: f[6]})
	}
	return out
}

func readGaia(t *testing.T, path string) []discoveries.Star {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []discoveries.Star
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		ra, _ := strconv.ParseFloat(f[0], 64)
		dec, _ := strconv.ParseFloat(f[1], 64)
		g, _ := strconv.ParseFloat(f[2], 64)
		out = append(out, discoveries.Star{RA: ra, Dec: dec, G: g})
	}
	return out
}

func parseVSX(t *testing.T, s string) []discoveries.Variable {
	t.Helper()
	vs, err := discoveries.ParseVSX([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return vs
}
