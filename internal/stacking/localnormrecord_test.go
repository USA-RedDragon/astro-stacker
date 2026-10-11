package stacking

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestUnevenShare(t *testing.T) {
	t.Parallel()
	const w, h = 70, 10
	acc := func(subs int, low float32, below int) *Accumulator {
		a := NewAccumulator(w, h)
		a.Subs = subs
		for i := range a.Count {
			a.Count[i] = 10
			if i < below {
				a.Count[i] = low
			}
		}
		return a
	}
	for _, tc := range []struct {
		name  string
		acc   *Accumulator
		share float64
		ok    bool
		needs bool
	}{
		{"even", acc(10, 10, 0), 0, true, false},
		{"tenth partial", acc(10, 8, 70), 0.1, true, false},
		{"over a tenth", acc(10, 8, 77), 0.11, true, true},
		{"a third partial", acc(10, 8, 231), 0.33, true, true},
		{"edges outside the frame", acc(10, 4, 231), 0, true, false},
		{"too few subs", acc(2, 8, 231), 0.33, true, false},
		{"empty", NewAccumulator(w, h), 0, false, false},
		{"none", nil, 0, false, false},
	} {
		share, ok := unevenShare(tc.acc)
		if ok != tc.ok || share < tc.share-1e-9 || share > tc.share+1e-9 {
			t.Errorf("%s: unevenShare = %v, %v; want %v, %v", tc.name, share, ok, tc.share, tc.ok)
		}
		if got := needsLocalNorm(tc.acc); got != tc.needs {
			t.Errorf("%s: needsLocalNorm = %v, want %v", tc.name, got, tc.needs)
		}
	}
}

// Every way a master is stacked records whether local normalization ran
// and the coverage share that decided it.
func TestLocalNormRecorded(t *testing.T) {
	t.Parallel()
	const w, h, n, exposure = 1536, 1024, 6, 300.0
	r := rand.New(rand.NewPCG(3, 5))
	truth := lnScene(w, h, r)
	even := make([][]float32, n)
	for i := range even {
		even[i], _ = lnSub(truth, w, h, exposure, r)
	}
	uneven := make([][]float32, n)
	for i, s := range even {
		uneven[i] = slices.Clone(s)
		if i%2 == 1 {
			for y := range h {
				clear(uneven[i][y*w+w*6/10 : (y+1)*w])
			}
		}
	}
	stored := make([]storedSub, n)
	for i := range stored {
		stored[i] = storedSub{exposure: exposure, weight: exposure}
	}
	stream := func(subs [][]float32, ln bool) LocalNormRecord {
		opts := DefaultOptions()
		opts.LocalNorm = ln
		acc, err := streamStack(stored, 2, opts, func(i int, _ storedSub) ([]float32, int, int, error) {
			return slices.Clone(subs[i]), w, h, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return acc.LocalNorm
	}
	median := func(subs [][]float32) LocalNormRecord {
		all := make([]memSub, len(subs))
		for i, s := range subs {
			all[i] = toMemSub(s, exposure, exposure, 0.9)
		}
		return medianAnchored(all, w, h, DefaultOptions()).LocalNorm
	}
	incremental := func(subs [][]float32) LocalNormRecord {
		acc := NewAccumulator(w, h)
		for _, s := range subs {
			if _, err := acc.Add(slices.Clone(s), exposure, exposure, DefaultOptions()); err != nil {
				t.Fatal(err)
			}
		}
		return acc.LocalNorm
	}
	check := func(name string, ln LocalNormRecord, subs int, ran bool) {
		t.Helper()
		if !ln.Known || !ln.Measured || ln.Subs != subs || (ln.Uneven > unevenCoverage) != ran {
			t.Errorf("%s: %+v, want %d subs normalized and uneven above %v %v", name, ln, subs, unevenCoverage, ran)
		}
	}
	check("streaming, even", stream(even, true), 0, false)
	check("streaming, uneven", stream(uneven, true), n, true)
	check("median, even", median(even), 0, false)
	check("median, uneven", median(uneven), n, true)
	check("incremental, even", incremental(even), 0, false)
	// The first three subs come before a master of three to judge.
	check("incremental, uneven", incremental(uneven), n-3, true)
	if ln := stream(uneven, false); ln.Measured || ln.Subs != 0 {
		t.Errorf("without local normalization: %+v", ln)
	}
}

func TestLocalNormCards(t *testing.T) {
	t.Parallel()
	ran := LocalNormRecord{Subs: 12, Known: true, Uneven: 0.25, Measured: true}
	got := map[string]string{}
	for _, c := range localNormCards(ran) {
		got[c.Key] = c.Value
		if len(c.Comment) > 80-len("KEYWORD = ")-20-len(" / ") {
			t.Errorf("%s comment %q is cut off", c.Key, c.Comment)
		}
	}
	if got["LOCALNRM"] != "T" || got["LNSUBS"] != "12" || got["LNUNEVEN"] != "0.25" {
		t.Errorf("cards = %v", got)
	}
	cards := localNormCards(LocalNormRecord{Known: true, Uneven: 0.02, Measured: true})
	if len(cards) != 3 || cards[0].Value != "F" || cards[1].Value != "0" {
		t.Errorf("not run: %+v", cards)
	}
	if cards := localNormCards(LocalNormRecord{Subs: 2, Uneven: 0.3, Measured: true}); len(cards) != 1 || cards[0].Key != "LNUNEVEN" {
		t.Errorf("unknown count: %+v", cards)
	}
	if cards := localNormCards(LocalNormRecord{}); len(cards) != 0 {
		t.Errorf("nothing recorded: %+v", cards)
	}

	subs, uneven := 12, 0.25
	if back := storedLocalNorm(&app.Stack{LocalNormSubs: &subs, LocalNormUneven: &uneven}); back != ran {
		t.Errorf("stored = %+v, want %+v", back, ran)
	}
	if back := storedLocalNorm(&app.Stack{}); back != (LocalNormRecord{}) {
		t.Errorf("stored before recording = %+v", back)
	}
}
