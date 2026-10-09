package stacking

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestRegisteredFormatsStackAlike(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("REGISTERED_COMPARE_DIR")
	if dir == "" {
		t.Skip("set REGISTERED_COMPARE_DIR to a folder of registered subs and a subs.txt of key|exposure|weight")
	}
	f, err := os.Open(filepath.Join(dir, "subs.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var subs []storedSub
	sc := bufio.NewScanner(f)
	for i := 1; sc.Scan(); i++ {
		parts := strings.Split(sc.Text(), "|")
		exp, _ := strconv.ParseFloat(parts[1], 64)
		w, _ := strconv.ParseFloat(parts[2], 64)
		subs = append(subs, storedSub{key: filepath.Join(dir, "s"+leftPad(i)+".fit"), exposure: exp, weight: w})
	}
	f.Close()
	encDir := t.TempDir()
	var encBytes, fitBytes int64
	for _, s := range subs {
		dst := filepath.Join(encDir, filepath.Base(s.key)+".xisf")
		if err := encodeRegisteredFile(s.key, dst); err != nil {
			t.Fatal(err)
		}
		a, _ := os.Stat(s.key)
		b, _ := os.Stat(dst)
		fitBytes += a.Size()
		encBytes += b.Size()
	}
	passes := 2
	if len(subs) < 40 {
		passes = 3
	}
	opts := DefaultOptions()
	fitAcc, err := streamStack(subs, passes, opts, func(_ int, s storedSub) ([]float32, int, int, error) { return readSub(s.key) })
	if err != nil {
		t.Fatal(err)
	}
	encAcc, err := streamStack(subs, passes, opts, func(_ int, s storedSub) ([]float32, int, int, error) {
		return readSub(filepath.Join(encDir, filepath.Base(s.key)+".xisf"))
	})
	if err != nil {
		t.Fatal(err)
	}
	mixAcc, err := streamStack(subs, passes, opts, func(i int, s storedSub) ([]float32, int, int, error) {
		if i%2 == 1 {
			return readSub(filepath.Join(encDir, filepath.Base(s.key)+".xisf"))
		}
		return readSub(s.key)
	})
	if err != nil {
		t.Fatal(err)
	}
	few := subs[:WarmUpSubs-1]
	fitMem := make([]memSub, 0, len(few))
	mixMem := make([]memSub, 0, len(few))
	for i, s := range few {
		a, w, h, err := readSub(s.key)
		if err != nil {
			t.Fatal(err)
		}
		fitMem = append(fitMem, toMemSub(a, s.exposure, s.weight, opts.SaturationLevel))
		if i%2 == 1 {
			if a, _, _, err = readSub(filepath.Join(encDir, filepath.Base(s.key)+".xisf")); err != nil {
				t.Fatal(err)
			}
		}
		mixMem = append(mixMem, toMemSub(a, s.exposure, s.weight, opts.SaturationLevel))
		if i == 0 && (w != fitAcc.W || h != fitAcc.H) {
			t.Fatal("size changed")
		}
	}
	fitMed := medianAnchored(fitMem, fitAcc.W, fitAcc.H, opts)
	mixMed := medianAnchored(mixMem, fitAcc.W, fitAcc.H, opts)
	adu := subs[0].exposure * math.MaxUint16
	rms := func(a, b *Accumulator) float64 {
		var sq float64
		n := 0
		for i := range a.Mean {
			if a.Count[i] > 0 && b.Count[i] > 0 {
				d := float64(a.Mean[i]-b.Mean[i]) * adu
				sq += d * d
				n++
			}
		}
		return math.Sqrt(sq / float64(n))
	}
	t.Logf("mixed formats against FITS: streamed %d subs %.4f ADU RMS; median-anchored %d subs %.4f ADU RMS",
		len(subs), rms(fitAcc, mixAcc), len(few), rms(fitMed, mixMed))
	st := compareStacks(fitAcc, encAcc, adu)
	t.Logf("%d subs, %d passes, %dx%d; stored %.1f MiB/sub as FITS, %.1f MiB/sub as XISF (%.2fx)", len(subs), passes, fitAcc.W, fitAcc.H,
		float64(fitBytes)/float64(len(subs))/(1<<20), float64(encBytes)/float64(len(subs))/(1<<20), float64(fitBytes)/float64(encBytes))
	t.Logf("pixels compared %d; sample counts differ at %d (%.4f%%)", st.n, st.countDiff, 100*float64(st.countDiff)/float64(len(fitAcc.Mean)))
	t.Logf("standard error ratio XISF/FITS: of means %.5f, median per pixel %.5f", st.seRatio, st.medianRatio)
	t.Logf("difference RMS %.4f ADU, p99 %.4f ADU, %.4f ADU RMS where both kept the same samples (sub exposure %.0f s)",
		st.rms, st.p99, st.sameRMS, subs[0].exposure)
	if st.seRatio > 1.01 || st.rms > 0.5 {
		t.Errorf("16-bit subs stack worse than float: standard error ratio %.5f, difference RMS %.4f ADU", st.seRatio, st.rms)
	}
}

type stackComparison struct {
	n, countDiff                            int
	seRatio, medianRatio, rms, p99, sameRMS float64
}

func compareStacks(fitAcc, encAcc *Accumulator, adu float64) stackComparison {
	var sumSq, sameSq, seA, seB float64
	same := 0
	var ratios, diffs []float64
	n, countDiff := 0, 0
	for i := range fitAcc.Mean {
		if fitAcc.Count[i] != encAcc.Count[i] {
			countDiff++
		}
		if fitAcc.Count[i] < 2 || encAcc.Count[i] < 2 || fitAcc.Weight[i] <= 0 || encAcc.Weight[i] <= 0 {
			continue
		}
		d := float64(fitAcc.Mean[i]-encAcc.Mean[i]) * adu
		sumSq += d * d
		diffs = append(diffs, math.Abs(d))
		if fitAcc.Count[i] == encAcc.Count[i] {
			sameSq += d * d
			same++
		}
		a := math.Sqrt(float64(fitAcc.M2[i]/fitAcc.Weight[i]) / float64(fitAcc.Count[i]))
		b := math.Sqrt(float64(encAcc.M2[i]/encAcc.Weight[i]) / float64(encAcc.Count[i]))
		seA += a
		seB += b
		if a > 0 {
			ratios = append(ratios, b/a)
		}
		n++
	}
	slices.Sort(ratios)
	slices.Sort(diffs)
	return stackComparison{
		n: n, countDiff: countDiff, seRatio: seB / seA, medianRatio: ratios[len(ratios)/2],
		rms: math.Sqrt(sumSq / float64(n)), p99: diffs[len(diffs)*99/100], sameRMS: math.Sqrt(sameSq / float64(same)),
	}
}

func leftPad(i int) string {
	s := strconv.Itoa(i)
	if len(s) < 2 {
		s = "0" + s
	}
	return s
}
