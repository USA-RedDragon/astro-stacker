package discoveries_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
// with L/*.xisf, master_L.fit, subs.tsv, wcs.json, gaia.tsv (RA, Dec, G and
// optionally pmRA, pmDE), vsx.tsv and skybot.txt.
func TestKnownAnswerIC3393(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("DISCOVERIES_KAT_DIR")
	if dir == "" {
		t.Skip("DISCOVERIES_KAT_DIR not set")
	}
	t.Run("luminance", func(t *testing.T) {
		t.Parallel()
		knownAnswer(t, dir, []string{testFilterL}, "skybot.txt", 2460735.844356)
	})
	t.Run("broadband", func(t *testing.T) {
		t.Parallel()
		if _, err := os.Stat(filepath.Join(dir, "master_Red.fit")); err != nil {
			t.Skip("no colour masters")
		}
		knownAnswer(t, dir, colourFilters(), "skybot_bb.txt", 2460735.840883)
	})
}

func knownAnswer(t *testing.T, dir string, filters []string, skybotFile string, epochJD float64) {
	t.Helper()
	runNight(t, dir, time.Date(2025, 2, 28, 0, 0, 0, 0, time.UTC), filters, skybotFile, epochJD, true)
}

// TestDiagnoseNight runs one night of any target's data in the layout of
// TestKnownAnswerIC3393 and logs what it found and why detections were
// left out: DISCOVERIES_DIAG_DIR, DISCOVERIES_DIAG_NIGHT (YYYY-MM-DD) and
// DISCOVERIES_DIAG_EPOCH (the night's mid time, JD).
func TestDiagnoseNight(t *testing.T) {
	t.Parallel()
	dir, nightS, epochS := os.Getenv("DISCOVERIES_DIAG_DIR"), os.Getenv("DISCOVERIES_DIAG_NIGHT"), os.Getenv("DISCOVERIES_DIAG_EPOCH")
	if dir == "" {
		t.Skip("DISCOVERIES_DIAG_DIR not set")
	}
	night, err := time.Parse(time.DateOnly, nightS)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := strconv.ParseFloat(epochS, 64)
	if err != nil {
		t.Fatal(err)
	}
	runNight(t, dir, night, colourFilters(), "skybot.txt", epoch, false)
}

func colourFilters() []string { return []string{testFilterL, "Red", "Green", "Blue"} }

const testFilterL = "Luminance"

func runNight(t *testing.T, dir string, night time.Time, filters []string, skybotFile string, epochJD float64, assert bool) {
	t.Helper()
	ctx := context.Background()
	in := nightInput(t, dir, night, filters)
	start := time.Now()
	res, err := discoveries.Run(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("took %s: night %d subs, reference %d; hfr %.2f/%.2f convolved %s, kernel tiles %d sum %.3f; apsig %.3g; zp %v depth %v sub depth %v; astrometry rms %v from %d stars; sub detections %d",
		time.Since(start).Round(time.Second), res.NightSubs, res.RefSubs, res.HFRNight, res.HFRRef, res.Convolved, res.KernelTiles, res.KernelSum, res.ApSigma,
		deref(res.ZeroPoint), deref(res.Depth), deref(res.SubDepth), deref(res.AstroRMSArcsec), res.AstroStars, res.SubDetections)
	for k, v := range res.Funnel {
		t.Logf("%s: %+v", k, v)
	}
	epoch := time.Unix(0, int64((epochJD-2440587.5)*86400e9)).UTC()
	vsxB, err := os.ReadFile(filepath.Join(dir, "vsx.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	vsx := parseVSX(t, string(vsxB))
	sky, err := os.ReadFile(filepath.Join(dir, skybotFile))
	if err != nil {
		sky = []byte("# Flag: 1\n")
	}
	bodies, err := discoveries.ParseSkyBoT(sky)
	if err != nil {
		t.Fatal(err)
	}
	labelled, tracks, _, _ := discoveries.Classify(ctx, res, discoveries.Known{Variables: vsx, Bodies: bodies, Epoch: epoch}, nil)
	counts := logCandidates(t, labelled)
	logRejectedVSX(t, vsx, in, res)
	inField, recovered := knownAsteroids(t, bodies, tracks, in.WCS, res, epoch)
	logMoverSubs(t, res)
	if !assert {
		var b strings.Builder
		for _, r := range res.Rejected {
			fmt.Fprintf(&b, "%.1f\t%.1f\t%+d\t%.1f\t%s\n", r.X, r.Y, r.Sign, r.SNR, r.Reason)
		}
		if err := os.WriteFile(filepath.Join(dir, "rejected.tsv"), []byte(b.String()), 0o600); err != nil {
			t.Error(err)
		}
		return
	}
	if counts[discoveries.ClassKnownVariable] < 13 {
		t.Errorf("found %d known variables, the trial found 13", counts[discoveries.ClassKnownVariable])
	}
	unknown := 0
	for _, tr := range tracks {
		if tr.Class == discoveries.ClassUnknownMover {
			unknown++
		}
	}
	if unknown > 2 {
		t.Errorf("%d unknown movers; the trial had none that survived the rules", unknown)
	}
	if inField == 0 || recovered < inField {
		t.Errorf("linked %d of %d known asteroids brighter than V 19.5, the trial linked all", recovered, inField)
	}
}

func nightInput(t *testing.T, dir string, night time.Time, filters []string) discoveries.Input {
	t.Helper()
	subs := readSubs(t, filepath.Join(dir, "subs.tsv"))
	var in discoveries.Input
	for _, name := range filters {
		if _, err := os.Stat(filepath.Join(dir, "master_"+name+".fit")); err != nil {
			continue
		}
		master := decode(t, filepath.Join(dir, "master_"+name+".fit"))
		if in.W == 0 {
			in.W, in.H = master.W, master.H
		}
		f := discoveries.FilterInput{Filter: name, Anchors: discoveries.Anchors(master)}
		for _, s := range subs {
			switch {
			case s.Filter != name:
			case s.Night.Equal(night):
				f.Night = append(f.Night, s)
			case len(f.Ref) < 48:
				f.Ref = append(f.Ref, s)
			}
		}
		in.Filters = append(in.Filters, f)
	}
	in.WCS, in.Gaia, in.Threads = readWCS(t, filepath.Join(dir, "wcs.json"), in.H), readGaia(t, filepath.Join(dir, "gaia.tsv")), 4
	in.Load = func(_ context.Context, s discoveries.Sub) (*discoveries.Plane, error) {
		return decode(t, filepath.Join(dir, s.Filter, filepath.Base(s.Key))), nil
	}
	return in
}

func logCandidates(t *testing.T, labelled []discoveries.Labelled) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, l := range labelled {
		counts[l.Class]++
		name := ""
		for _, m := range l.Matches {
			name += m.Catalog + ":" + m.Name + " "
		}
		t.Logf("%-24s sign %+d snr %6.1f x %7.1f y %7.1f gaia %v dmag %v present %d/%d top %.2f %s", l.Class, l.Sign, l.SNR, l.X, l.Y, deref(l.GaiaG), deref(l.DeltaMag), l.PresentIn, l.SubsTested, l.TopShare, name)
		switch l.Class {
		case discoveries.ClassKnownVariable:
			t.Logf("   per filter:%s", perFilter(l))
		case discoveries.ClassNewVariable:
			for _, p := range l.Light {
				t.Logf("   %s %s flux %.4g err %.4g", p.Time.Format("15:04"), p.Filter, p.Flux, p.FluxErr)
			}
		}
	}
	t.Logf("classes: %v", counts)
	return counts
}

func perFilter(l discoveries.Labelled) string {
	per := map[string][3]float64{}
	for _, p := range l.Light {
		v := per[p.Filter]
		v[0] += float64(l.Sign) * p.Flux / p.FluxErr
		v[1]++
		if float64(l.Sign)*p.Flux > p.FluxErr {
			v[2]++
		}
		per[p.Filter] = v
	}
	line := ""
	for _, f := range colourFilters() {
		if v, ok := per[f]; ok {
			line += fmt.Sprintf(" %s mean %.1fσ present %d/%d;", f[:1], v[0]/v[1], int(v[2]), int(v[1]))
		}
	}
	return line
}

func logRejectedVSX(t *testing.T, vsx []discoveries.Variable, in discoveries.Input, res *discoveries.Result) {
	t.Helper()
	for _, v := range vsx {
		x, y, ok := in.WCS.PixelOf(v.RA, v.Dec)
		if !ok || x < 0 || y < 0 || x >= float64(in.W) || y >= float64(in.H) {
			continue
		}
		for _, r := range res.Rejected {
			if (r.X-x)*(r.X-x)+(r.Y-y)*(r.Y-y) < 4 {
				t.Logf("VSX %s rejected: %s; sign %+d snr %.1f present %d/%d top %.2f mean %.1f sigma median/mean %.2f dipole %.2f frac %.3f",
					v.Name, r.Reason, r.Sign, r.SNR, r.PresentIn, r.Subs, r.TopShare, r.MeanSigma, r.MedianShare, r.Dipole, r.Frac)
			}
		}
	}
	reasons := map[string]int{}
	for _, r := range res.Rejected {
		k := r.Reason
		if i := strings.Index(k, ":"); i > 0 {
			k = k[:i]
		}
		reasons[fmt.Sprintf("%+d %s", r.Sign, k)]++
	}
	t.Logf("rejections: %v", reasons)
}

func knownAsteroids(t *testing.T, bodies []discoveries.Body, tracks []discoveries.LabelledTrack, wcs *discoveries.WCS, res *discoveries.Result, epoch time.Time) (inField, recovered int) {
	t.Helper()
	matched := map[string]bool{}
	for _, tr := range tracks {
		if tr.Match != nil {
			matched[tr.Match.Name] = true
		}
		t.Logf("track %-14s n=%2d rate %.2f\"/min resid %.2f px chi %.2f V %v offset %v", tr.Class, len(tr.Points), tr.RateArcmin, tr.ResidualPx, tr.Chi, deref(tr.BodyV), deref(tr.OffsetArc))
	}
	t.Logf("mover null test: %.2f tracks a shuffle, most detections %d", res.MoverNull.Tracks, res.MoverNull.MaxHits)
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
			continue
		}
		t.Logf("missed %s V %.1f at %.0f,%.0f: transient detections near its path in %d of %d subs", name, b.V, x, y, pathHits(b, wcs, res, epoch), len(res.MoverSubs))
	}
	t.Logf("known asteroids V<19.5 in field %d, linked %d", inField, recovered)
	return inField, recovered
}

func pathHits(b discoveries.Body, wcs *discoveries.WCS, res *discoveries.Result, epoch time.Time) int {
	hits := 0
	for _, ms := range res.MoverSubs {
		bra, bdec := b.At(ms.Time.Sub(epoch))
		bx, by, _ := wcs.PixelOf(bra, bdec)
		for _, p := range ms.Points {
			if (p[0]-bx)*(p[0]-bx)+(p[1]-by)*(p[1]-by) < 9 {
				hits++
				break
			}
		}
	}
	return hits
}

func logMoverSubs(t *testing.T, res *discoveries.Result) {
	t.Helper()
	for i, ms := range res.MoverSubs {
		gap := 0.0
		if i > 0 {
			gap = ms.Time.Sub(res.MoverSubs[i-1].Time).Minutes()
		}
		t.Logf("sub %2d %-9s %s +%4.1f min: %5d detections, %5d transient", i, ms.Filter, ms.Time.Format("15:04:05"), gap, ms.Detections, ms.Transient)
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
		if len(f) != 3 && len(f) != 5 {
			continue
		}
		ra, _ := strconv.ParseFloat(f[0], 64)
		dec, _ := strconv.ParseFloat(f[1], 64)
		g, _ := strconv.ParseFloat(f[2], 64)
		st := discoveries.Star{RA: ra, Dec: dec, G: g}
		if len(f) == 5 {
			st.PMRA, _ = strconv.ParseFloat(f[3], 64)
			st.PMDec, _ = strconv.ParseFloat(f[4], 64)
		}
		out = append(out, st)
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
